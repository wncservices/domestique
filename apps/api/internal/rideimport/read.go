package rideimport

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"math"
	"os"
	"strings"
)

// Read walks an upload and hands each FIT it holds to emit as bytes. The
// upload is a zip (read to MaxDepth levels, since a Garmin export keeps its
// FITs in a zip inside the zip) or a single loose .fit or .fit.gz; spool is
// that file, size its length. It never writes or reports an entry name:
// an entry's type is chosen by its extension and confirmed by the FIT header,
// and only counts come back.
//
// One entry is held in memory at a time, bounded by MaxEntryBytes. A zip too
// large for archive/zip to list, or one that inflates absurdly, stops the
// read with a typed error in Report.Err; rides emitted before it stay.
func Read(spool *os.File, size int64, l Limits, emit func(fit []byte) error) Report {
	r := &reader{l: l, emit: emit}
	r.rep.Err = r.top(spool, size)
	return r.rep
}

type reader struct {
	l       Limits
	emit    func([]byte) error
	rep     Report
	total   int64
	entries int
}

// outcome is how one bounded copy ended.
type outcome int

const (
	copied outcome = iota
	oversize
	unreadable
)

func (r *reader) top(spool *os.File, size int64) error {
	var head [4]byte
	n, _ := spool.ReadAt(head[:], 0)
	switch {
	case n >= 4 && bytes.Equal(head[:], []byte("PK\x03\x04")), n >= 4 && bytes.Equal(head[:], []byte("PK\x05\x06")):
		return r.archive(spool, size, 1, true)
	case n >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return r.fit(io.NewSectionReader(spool, 0, size), size, true)
	case n >= 4:
		return r.fit(io.NewSectionReader(spool, 0, size), size, false)
	default:
		r.rep.Unsupported++
		return nil
	}
}

// archive reads one zip. A top-level zip that cannot be opened is an error
// for the whole upload; a nested one that cannot is one more thing not
// supported, and the rest go on.
func (r *reader) archive(ra io.ReaderAt, size int64, depth int, topLevel bool) error {
	// #nosec G115 -- r.entries never exceeds MaxEntries here, so this is not negative.
	if n, ok := zipEntryCount(ra, size); ok && n > uint64(r.l.MaxEntries-r.entries) {
		return ErrTooManyEntries
	}
	zr, err := zip.NewReader(ra, size)
	// Entry names are never used as paths, so an insecure one is harmless:
	// archive/zip reports it but hands back a usable reader.
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		if topLevel {
			return ErrBadArchive
		}
		r.rep.Unsupported++
		return nil
	}
	for _, f := range zr.File {
		r.entries++
		if r.entries > r.l.MaxEntries {
			return ErrTooManyEntries
		}
		if err := r.entry(f, depth); err != nil {
			return err
		}
	}
	return nil
}

// kind is what an entry's name says it is. The name is read here and nowhere
// else, and is never stored, logged or used as a path.
type kind int

const (
	kindIgnore kind = iota
	kindFIT
	kindFITGz
	kindUnsupported
	kindZip
)

func kindOf(name string) kind {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".fit"):
		return kindFIT
	case strings.HasSuffix(n, ".fit.gz"):
		return kindFITGz
	case strings.HasSuffix(n, ".gpx"), strings.HasSuffix(n, ".tcx"), strings.HasSuffix(n, ".gpx.gz"), strings.HasSuffix(n, ".tcx.gz"):
		return kindUnsupported
	case strings.HasSuffix(n, ".zip"):
		return kindZip
	}
	return kindIgnore
}

func (r *reader) entry(f *zip.File, depth int) error {
	mode := f.Mode()
	if mode.IsDir() || !mode.IsRegular() {
		return nil
	}
	k := kindOf(f.Name)
	if k == kindIgnore {
		return nil
	}
	if f.Flags&0x1 != 0 { // encrypted: no password to give
		r.rep.Unsupported++
		return nil
	}
	switch k {
	case kindUnsupported:
		r.rep.Unsupported++
		return nil
	case kindZip:
		if depth >= r.l.MaxDepth {
			r.rep.Unsupported++
			return nil
		}
		return r.nested(f, depth)
	}
	rc, err := f.Open()
	if err != nil {
		r.rep.Unreadable++
		return nil
	}
	defer func() { _ = rc.Close() }()
	return r.fit(rc, compressedSize(f), k == kindFITGz)
}

// fit reads one FIT (or gzipped FIT) from src, whose compressed size is comp,
// and emits it if the header confirms it.
func (r *reader) fit(src io.Reader, comp int64, gzipped bool) error {
	var buf bytes.Buffer
	res, err := r.pump(&buf, src, comp, gzipped, r.l.MaxEntryBytes)
	if err != nil {
		return err
	}
	switch res {
	case oversize:
		r.rep.Oversize++
		return nil
	case unreadable:
		r.rep.Unreadable++
		return nil
	}
	b := buf.Bytes()
	if len(b) < 12 || string(b[8:12]) != ".FIT" {
		r.rep.Unsupported++
		return nil
	}
	if err := r.emit(b); err != nil {
		return err
	}
	r.rep.Files++
	return nil
}

// nested spools a zip inside the zip to a 0600 temp file, which archive/zip
// needs for random access, reads it one level deeper, and removes it on every
// path.
func (r *reader) nested(f *zip.File, depth int) error {
	rc, err := f.Open()
	if err != nil {
		r.rep.Unreadable++
		return nil
	}
	defer func() { _ = rc.Close() }()

	tmp, err := os.CreateTemp("", "domestique-import-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name()) // best effort: the spool holds only what the upload did
	}()

	res, err := r.pump(tmp, rc, compressedSize(f), false, r.l.MaxArchiveBytes)
	if err != nil {
		return err
	}
	switch res {
	case oversize:
		r.rep.Oversize++
		return nil
	case unreadable:
		r.rep.Unreadable++
		return nil
	}
	st, err := tmp.Stat()
	if err != nil {
		return err
	}
	return r.archive(tmp, st.Size(), depth+1, false)
}

// compressedSize is the size a zip entry declares for its compressed bytes.
// It only feeds the ratio guard (the byte caps count what is actually read),
// so a lie here can at worst delay that guard, never defeat the size caps.
func compressedSize(f *zip.File) int64 {
	if f.CompressedSize64 > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(f.CompressedSize64) // #nosec G115 -- bounded just above.
}

// counter counts the bytes that pass through it.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// pump copies src to dst under every cap, counting the bytes actually read
// and never what a header declares. comp is the compressed size of the zip
// entry src came from; gzipped says src is itself a gzip stream. The loop
// stops as soon as a cap is crossed, so a bomb costs a megabyte or so of work
// before it is refused, not its full inflated size.
func (r *reader) pump(dst io.Writer, src io.Reader, comp int64, gzipped bool, limit int64) (outcome, error) {
	zipSide := &counter{r: io.LimitReader(src, limit+1)}
	var out io.Reader = zipSide
	if gzipped {
		g, err := gzip.NewReader(zipSide)
		if err != nil {
			return unreadable, nil
		}
		defer func() { _ = g.Close() }()
		out = g
	}

	ratio := int64(r.l.MaxRatio)
	if comp < 1 {
		comp = 1
	}
	var n int64
	chunk := make([]byte, 32<<10)
	for {
		m, rerr := out.Read(chunk)
		if m > 0 {
			n += int64(m)
			r.total += int64(m)
			if n > limit || zipSide.n > limit {
				return oversize, nil
			}
			if r.total > r.l.MaxTotalBytes {
				return copied, ErrTotalSize
			}
			// Two layers can each inflate: the zip entry, and a gzip stream
			// inside it. Either beyond the ratio, once past the floor, is a bomb.
			if zipSide.n > ratioFloor && zipSide.n > comp*ratio {
				return copied, ErrCompressionRatio
			}
			if gzipped && n > ratioFloor && n > zipSide.n*ratio {
				return copied, ErrCompressionRatio
			}
			if _, werr := dst.Write(chunk[:m]); werr != nil {
				return copied, werr
			}
		}
		if rerr == io.EOF {
			return copied, nil
		}
		if rerr != nil {
			// A damaged stream, or a zip checksum that does not match.
			return unreadable, nil
		}
	}
}
