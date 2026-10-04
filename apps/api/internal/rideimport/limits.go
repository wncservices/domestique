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

// zipEntryCount reads the entry count a zip declares in its end-of-central-
// directory record, so an archive that claims millions of entries is refused
// before archive/zip tries to hold them all in memory. ok is false when no
// record is found (the caller then lets archive/zip produce the error).
func zipEntryCount(ra io.ReaderAt, size int64) (n uint64, ok bool) {
	const eocdLen, maxComment = 22, 1 << 16
	tail := int64(eocdLen + maxComment)
	if tail > size {
		tail = size
	}
	buf := make([]byte, tail)
	if _, err := ra.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return 0, false
	}
	at := -1
	for i := len(buf) - eocdLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) == 0x06054b50 {
			at = i
			break
		}
	}
	if at < 0 {
		return 0, false
	}
	n = uint64(binary.LittleEndian.Uint16(buf[at+10:]))
	if n != 0xFFFF {
		return n, true
	}
	// Zip64: the real count is in the zip64 end record, which a locator just
	// before the EOCD points to.
	const locLen = 20
	if at < locLen || binary.LittleEndian.Uint32(buf[at-locLen:]) != 0x07064b50 {
		return n, true
	}
	rawOff := binary.LittleEndian.Uint64(buf[at-locLen+8:])
	rec := make([]byte, 56)
	if rawOff > uint64(size) { // #nosec G115 -- size is a file length, never negative.
		return n, true
	}
	off := int64(rawOff) // #nosec G115 -- checked against size just above.
	if off+int64(len(rec)) > size {
		return n, true
	}
	if _, err := ra.ReadAt(rec, off); err != nil || binary.LittleEndian.Uint32(rec) != 0x06064b50 {
		return n, true
	}
	return binary.LittleEndian.Uint64(rec[32:]), true
}
