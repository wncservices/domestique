// Package rideimport turns a rider's exported ride history (a Strava or Garmin
// account export, or loose FIT files) into sessions, without ever storing the
// upload.
//
// Uploads are large and hostile by default, and a ride file carries GPS and
// health data, so this package keeps three promises: it reads one bounded
// entry at a time, it never uses or reports an entry name (names carry dates
// and places), and it never writes anything but a 0600 temp spool that is
// removed on every path. It is pure: no HTTP, no store.
package rideimport

import (
	"encoding/binary"
	"errors"
	"io"
)

// Limits bound what one upload may cost. Every cap is enforced by counting
// bytes actually read, never by trusting a size an archive declares.
type Limits struct {
	// MaxEntryBytes caps one entry after decompression (gzip-in-zip counts
	// against the entry's own cap). An entry over it is skipped and counted.
	MaxEntryBytes int64
	// MaxTotalBytes caps everything decompressed across the whole upload.
	MaxTotalBytes int64
	// MaxArchiveBytes caps a nested zip, which is spooled to disk to be
	// opened. It is separate from MaxEntryBytes because a Garmin export's
	// inner zip holds years of rides and is far larger than any one FIT.
	MaxArchiveBytes int64
	// MaxEntries caps entries across every archive in the upload, checked
	// against a zip's own directory count before it is listed.
	MaxEntries int
	// MaxDepth is how many zips deep to read: 2 reads a zip inside a zip, and
	// a third level is ignored (counted unsupported).
	MaxDepth int
	// MaxRatio aborts the upload when an entry decompresses beyond this many
	// times its compressed size.
	MaxRatio int
}

// DefaultLimits are the spec's: 64 MiB per entry, 6 GiB in all, 20 000
// entries, depth 2, a 200x ratio guard, 1 GiB for a nested archive (the same
// as the request cap, since it cannot be bigger than what was uploaded).
var DefaultLimits = Limits{
	MaxEntryBytes:   64 << 20,
	MaxTotalBytes:   6 << 30,
	MaxArchiveBytes: 1 << 30,
	MaxEntries:      20000,
	MaxDepth:        2,
	MaxRatio:        200,
}

// ratioFloor is how much an entry may inflate before the ratio is looked at:
// a tiny file can legitimately compress 200x, a bomb has to be big to matter.
const ratioFloor = 1 << 20

// The errors that stop a whole upload. A breach ends the job with nothing
// further written; rides already committed stay, each being idempotent.
var (
	ErrTotalSize        = errors.New("rideimport: the upload decompresses to more than the allowed total")
	ErrTooManyEntries   = errors.New("rideimport: the upload holds more files than allowed")
	ErrCompressionRatio = errors.New("rideimport: an entry decompresses far beyond its compressed size")
	ErrBadArchive       = errors.New("rideimport: the upload is not a readable zip")
)

// Report is what a read found. Counts only: no names, no sizes, nothing from
// inside a file.
type Report struct {
	// Files is the FITs handed to emit.
	Files int
	// Unsupported is what was recognised but not importable (GPX, TCX), or
	// named like a FIT but not one, or encrypted, or beyond the depth cap.
	Unsupported int
	// Oversize is entries skipped for exceeding a size cap.
	Oversize int
	// Unreadable is entries whose compressed stream was damaged.
	Unreadable int
	// Err is why the read stopped early, nil when it ran to the end.
	Err error
}

// zipDirectory reads what a zip declares about its central directory from the
// end-of-central-directory records, so an archive that would make archive/zip
// hold millions of entries in memory is refused before it is listed.
//
// entries is the largest count the records declare. region is the number of
// bytes the central directory actually occupies on disk, which matters
// because archive/zip does not stop at the declared count: it reads entries
// until the directory ends, so a count that lies low bounds nothing, and the
// byte length is the only honest limit.
//
// archive/zip switches to the zip64 records when the count is 0xFFFF, the
// directory size is 0xFFFF or the offset is 0xFFFFFFFF. The same three
// conditions are mirrored here, and the zip64 locator is also read whenever it
// is present, taking the larger of the two answers: an attacker only gains by
// the two disagreeing. ok is false when no record is found (the caller then
// lets archive/zip produce the error).
func zipDirectory(ra io.ReaderAt, size int64) (entries, region uint64, ok bool) {
	const eocdLen, maxComment = 22, 1 << 16
	tail := int64(eocdLen + maxComment)
	if tail > size {
		tail = size
	}
	buf := make([]byte, tail)
	if _, err := ra.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return 0, 0, false
	}
	at := -1
	for i := len(buf) - eocdLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) == 0x06054b50 {
			at = i
			break
		}
	}
	if at < 0 {
		return 0, 0, false
	}
	eocdPos := uint64(size-tail) + uint64(at) // #nosec G115 -- both are non-negative file offsets.
	entries = uint64(binary.LittleEndian.Uint16(buf[at+10:]))
	dirSize := uint64(binary.LittleEndian.Uint32(buf[at+12:]))
	dirOff := uint64(binary.LittleEndian.Uint32(buf[at+16:]))
	dirEnd := eocdPos

	// The locator sits directly before the EOCD and points at the zip64 record.
	const locLen = 20
	sentinel := entries == 0xFFFF || dirSize == 0xFFFF || dirOff == 0xFFFFFFFF
	if at >= locLen && binary.LittleEndian.Uint32(buf[at-locLen:]) == 0x07064b50 {
		rawOff := binary.LittleEndian.Uint64(buf[at-locLen+8:])
		rec := make([]byte, 56)
		if rawOff <= uint64(size) && rawOff+uint64(len(rec)) <= uint64(size) { // #nosec G115 -- size is a file length.
			if _, err := ra.ReadAt(rec, int64(rawOff)); err == nil && binary.LittleEndian.Uint32(rec) == 0x06064b50 { // #nosec G115 -- bounded just above.
				if n := binary.LittleEndian.Uint64(rec[32:]); n > entries {
					entries = n
				}
				dirEnd = rawOff
				if sentinel || dirOff == 0xFFFFFFFF {
					dirOff = binary.LittleEndian.Uint64(rec[48:])
				}
			}
		}
	}
	if dirOff < dirEnd {
		region = dirEnd - dirOff
	}
	return entries, region, true
}

// maxDirectoryBytesPerEntry bounds the central directory by what a real entry
// needs: 46 bytes of header plus a name, comfortably under a kilobyte. A
// directory longer than the entry cap allows at this size is not a ride export.
const maxDirectoryBytesPerEntry = 1024
