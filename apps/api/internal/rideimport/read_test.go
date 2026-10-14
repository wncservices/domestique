package rideimport

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFIT is the smallest thing the reader accepts as a FIT: a 14-byte header
// with the ".FIT" magic at byte 8, then a marker. The reader only checks the
// magic; decoding is Parse's job (ride.go), and these are synthetic bytes
// built here, never a real ride.
func fakeFIT(marker string) []byte {
	h := make([]byte, 14)
	h[0] = 14
	copy(h[8:12], ".FIT")
	return append(h, []byte(marker)...)
}

// noise is incompressible, so a fixture built from it is large on the wire
// as well as unpacked, and trips the size caps rather than the ratio guard.
func noise(n int) string {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return string(b)
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type entry struct {
	name string
	data []byte
}

func buildZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// spool writes b to a temp file and opens it, as the upload handler does.
func spool(t *testing.T, b []byte) (*os.File, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "spool-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, int64(len(b))
}

func testLimits() Limits {
	return Limits{MaxEntryBytes: 1 << 20, MaxTotalBytes: 16 << 20, MaxArchiveBytes: 8 << 20, MaxEntries: 100, MaxDepth: 2, MaxRatio: 200}
}

type collector struct{ got []string }

func (c *collector) emit(b []byte) error {
	c.got = append(c.got, string(b[14:]))
	return nil
}

func read(t *testing.T, raw []byte, l Limits) (Report, []string) {
	t.Helper()
	f, size := spool(t, raw)
	c := &collector{}
	rep := Read(f, size, l, c.emit)
	return rep, c.got
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		seen[w]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestStravaShapedZip(t *testing.T) {
	raw := buildZip(t,
		entry{"activities/1001.fit.gz", gz(t, fakeFIT("one"))},
		entry{"activities/1002.fit", fakeFIT("two")},
		entry{"activities/1003.gpx", []byte("<gpx/>")},
		entry{"activities/1004.tcx", []byte("<tcx/>")},
		entry{"activities/1005.GPX.gz", gz(t, []byte("<gpx/>"))},
		entry{"activities.csv", []byte("id,name\n")},
		entry{"media/", nil},
	)
	rep, got := read(t, raw, testLimits())
	if rep.Err != nil {
		t.Fatal(rep.Err)
	}
	if !sameSet(got, "one", "two") {
		t.Errorf("emitted %q, want one and two", got)
	}
	if rep.Files != 2 || rep.Unsupported != 3 || rep.Oversize != 0 {
		t.Errorf("report = %+v, want 2 files and 3 unsupported (gpx, tcx, gpx.gz); the csv and folder are not counted", rep)
	}
}

func TestGarminShapedNestedZipReadAtDepthTwo(t *testing.T) {
	level3 := buildZip(t, entry{"deep.fit", fakeFIT("third level")})
	level2 := buildZip(t,
		entry{"2020-01-01-aaa.fit", fakeFIT("a")},
		entry{"2020-01-02-bbb.fit", fakeFIT("b")},
		entry{"more.zip", level3},
	)
	outer := buildZip(t,
		entry{"DI_CONNECT/DI-Connect-Fitness-Uploaded-Files/UploadedFiles_0-_Part1.zip", level2},
		entry{"DI_CONNECT/DI-Connect-Fitness/summarizedActivities.json", []byte("{}")},
	)
	rep, got := read(t, outer, testLimits())
	if rep.Err != nil {
		t.Fatal(rep.Err)
	}
	if !sameSet(got, "a", "b") {
		t.Errorf("emitted %q, want a and b; a third zip level is ignored", got)
	}
	if rep.Files != 2 || rep.Unsupported != 1 {
		t.Errorf("report = %+v, want 2 files and the third-level zip counted unsupported", rep)
	}
}

func TestLooseFilesAreNotZips(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  []byte
		want []string
		rep  Report
	}{
		"fit":     {fakeFIT("loose"), []string{"loose"}, Report{Files: 1}},
		"fit.gz":  {gz(t, fakeFIT("loose gz")), []string{"loose gz"}, Report{Files: 1}},
		"junk":    {[]byte("this is not a ride at all, just text"), nil, Report{Unsupported: 1}},
		"gz junk": {gz(t, []byte("this is not a ride at all, just text")), nil, Report{Unsupported: 1}},
		"empty":   {nil, nil, Report{Unsupported: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			rep, got := read(t, tc.raw, testLimits())
			if rep.Err != nil || rep.Files != tc.rep.Files || rep.Unsupported != tc.rep.Unsupported {
				t.Errorf("report = %+v, want %+v", rep, tc.rep)
			}
			if !sameSet(got, tc.want...) {
				t.Errorf("emitted %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTypeIsConfirmedByTheFITHeader(t *testing.T) {
	raw := buildZip(t,
		entry{"a.fit", []byte("<html>not a fit file, only named like one</html>")},
		entry{"b.fit.gz", gz(t, []byte("<html>nor this</html>"))},
		entry{"c.fit", fakeFIT("real")},
	)
	rep, got := read(t, raw, testLimits())
	if rep.Err != nil {
		t.Fatal(rep.Err)
	}
	if !sameSet(got, "real") || rep.Files != 1 || rep.Unsupported != 2 {
		t.Errorf("emitted %q, report %+v: mislabelled entries must be skipped and counted", got, rep)
	}
}

func TestCorruptCompressedEntryIsUnreadableAndTheRestGoOn(t *testing.T) {
	bad := gz(t, fakeFIT("will be damaged"))
	bad = bad[:len(bad)-6] // truncated stream
	raw := buildZip(t, entry{"a.fit.gz", bad}, entry{"b.fit", fakeFIT("fine")})
	rep, got := read(t, raw, testLimits())
	if rep.Err != nil || !sameSet(got, "fine") || rep.Unreadable != 1 {
		t.Errorf("emitted %q, report %+v, want one unreadable and the good file through", got, rep)
	}
}

// bombEntry is `n` zeros that deflate to a few KiB.
func bombZip(t *testing.T, n int, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for left := n; left > 0; left -= len(chunk) {
		c := chunk
		if left < len(c) {
			c = c[:left]
		}
		if _, err := w.Write(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestZipBombStopsAtTheRatioWithoutAllocatingPastIt(t *testing.T) {
	// 100 MiB of zeros in well under 200 KiB. The entry cap is raised so only
	// the ratio guard can be what stops it.
	raw := bombZip(t, 100<<20, "activities/1.fit")
	if len(raw) > 250<<10 {
		t.Fatalf("fixture is %d bytes, not a bomb", len(raw))
	}
	l := testLimits()
	l.MaxEntryBytes = 1 << 30
	l.MaxTotalBytes = 1 << 30
	var rep Report
	alloc := allocated(func() { rep, _ = read(t, raw, l) })
	if !errors.Is(rep.Err, ErrCompressionRatio) {
		t.Fatalf("err = %v, want ErrCompressionRatio", rep.Err)
	}
	if rep.Files != 0 {
		t.Errorf("a bomb must emit nothing, report %+v", rep)
	}
	// The guard trips at 200x the compressed size (about 20 MiB here); reading
	// all 100 MiB into a growing buffer would allocate over 200 MiB in total.
	if alloc > 100<<20 {
		t.Errorf("allocated %d MiB reading a bomb, want it stopped at the ratio", alloc>>20)
	}
}

func TestGzipBombInsideAZipStopsAtTheRatioToo(t *testing.T) {
	var gzbuf bytes.Buffer
	w := gzip.NewWriter(&gzbuf)
	chunk := make([]byte, 1<<20)
	for i := 0; i < 60; i++ {
		_, _ = w.Write(chunk)
	}
	_ = w.Close()
	raw := buildZip(t, entry{"activities/1.fit.gz", gzbuf.Bytes()})
	l := testLimits()
	l.MaxEntryBytes = 1 << 30
	l.MaxTotalBytes = 1 << 30
	rep, _ := read(t, raw, l)
	if !errors.Is(rep.Err, ErrCompressionRatio) {
		t.Errorf("err = %v, want ErrCompressionRatio", rep.Err)
	}
}

func TestEntryOverTheCapIsOversizeAndTheJobGoesOn(t *testing.T) {
	big := fakeFIT(noise(2 << 20)) // over the 1 MiB test cap, and not compressible, so not a bomb
	raw := buildZip(t, entry{"SECRET-place.fit", big}, entry{"b.fit", fakeFIT("small")})
	rep, got := read(t, raw, testLimits())
	if rep.Err != nil {
		t.Fatalf("an oversize entry must not stop the job: %v", rep.Err)
	}
	if rep.Oversize != 1 || !sameSet(got, "small") {
		t.Errorf("report %+v, emitted %q, want the big one counted oversize and the small one through", rep, got)
	}
}

func TestSizeIsCountedFromBytesReadNotTheHeader(t *testing.T) {
	// A header that claims 10 bytes over a stream that inflates to 4 MiB.
	var deflated bytes.Buffer
	fw, _ := flate.NewWriter(&deflated, flate.BestSpeed)
	data := fakeFIT(noise(4 << 20))
	_, _ = fw.Write(data)
	_ = fw.Close()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: "lie.fit", Method: zip.Deflate,
		CompressedSize64: uint64(deflated.Len()), UncompressedSize64: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(deflated.Bytes())
	_ = zw.Close()

	var rep Report
	var got []string
	alloc := allocated(func() { rep, got = read(t, buf.Bytes(), testLimits()) })
	if len(got) != 0 {
		t.Fatalf("emitted %d bytes of an entry larger than its cap", len(got[0]))
	}
	if rep.Oversize+rep.Unreadable != 1 {
		t.Errorf("report = %+v, want the lying entry refused", rep)
	}
	if alloc > 16<<20 {
		t.Errorf("allocated %d MiB, want it bounded by the 1 MiB cap", alloc>>20)
	}
}

func TestTotalCapStopsTheRead(t *testing.T) {
	var entries []entry
	for i := 0; i < 6; i++ {
		entries = append(entries, entry{fmt.Sprintf("a%d.fit", i), fakeFIT(strings.Repeat(string(rune('a'+i)), 300<<10))})
	}
	l := testLimits()
	l.MaxTotalBytes = 1 << 20 // 1 MiB: room for three of the six
	rep, got := read(t, buildZip(t, entries...), l)
	if !errors.Is(rep.Err, ErrTotalSize) {
		t.Fatalf("err = %v, want ErrTotalSize", rep.Err)
	}
	if len(got) == 0 || len(got) >= 6 {
		t.Errorf("emitted %d of 6: rides before the breach stay, nothing after it", len(got))
	}
}

func TestEntryCountCapStopsBeforeListingTheArchive(t *testing.T) {
	var entries []entry
	for i := 0; i < 30; i++ {
		entries = append(entries, entry{fmt.Sprintf("a%d.fit", i), fakeFIT("x")})
	}
	l := testLimits()
	l.MaxEntries = 20
	rep, got := read(t, buildZip(t, entries...), l)
	if !errors.Is(rep.Err, ErrTooManyEntries) {
		t.Fatalf("err = %v, want ErrTooManyEntries", rep.Err)
	}
	if len(got) != 0 {
		t.Errorf("emitted %d files from an archive over the entry cap: it must be refused from the directory count alone", len(got))
	}
}

func TestEntryCountIsSharedAcrossNestedArchives(t *testing.T) {
	var inner []entry
	for i := 0; i < 15; i++ {
		inner = append(inner, entry{fmt.Sprintf("i%d.fit", i), fakeFIT("x")})
	}
	outer := buildZip(t,
		entry{"one.zip", buildZip(t, inner...)},
		entry{"two.zip", buildZip(t, inner...)},
	)
	l := testLimits()
	l.MaxEntries = 20
	rep, _ := read(t, outer, l)
	if !errors.Is(rep.Err, ErrTooManyEntries) {
		t.Errorf("err = %v, want ErrTooManyEntries: two archives of 15 are 30 entries", rep.Err)
	}
}

func TestNestedArchiveOverItsCapIsOversize(t *testing.T) {
	inner := buildZip(t, entry{"a.fit", fakeFIT(noise(3 << 20))})
	outer := buildZip(t, entry{"big.zip", inner}, entry{"b.fit", fakeFIT("ok")})
	l := testLimits()
	l.MaxArchiveBytes = 1 << 20
	l.MaxEntryBytes = 8 << 20
	rep, got := read(t, outer, l)
	if rep.Err != nil || rep.Oversize != 1 || !sameSet(got, "ok") {
		t.Errorf("report %+v emitted %q, want the oversize archive skipped and the rest read", rep, got)
	}
}

func TestEntryNamesAreNeverUsedAsPathsOrReported(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	inner := buildZip(t, entry{"../../../etc/inner-evil.fit", fakeFIT("inner")})
	raw := buildZip(t,
		entry{"../../evil.fit", fakeFIT("traversal")},
		entry{"/abs/evil.fit", fakeFIT("absolute")},
		entry{`..\win\evil.fit`, fakeFIT("backslash")},
		entry{"SECRET-Home-Street-1.zip", inner},
		entry{"SECRET-Home-Street-2.fit", []byte("not a fit")},
	)
	rep, got := read(t, raw, testLimits())
	if rep.Err != nil {
		t.Fatal(rep.Err)
	}
	if !sameSet(got, "traversal", "absolute", "backslash", "inner") {
		t.Errorf("emitted %q: hostile names are only labels, the bytes are still read", got)
	}
	if strings.Contains(fmt.Sprintf("%+v %v", rep, rep.Err), "SECRET") {
		t.Errorf("a name leaked into the report: %+v", rep)
	}
	// Nothing was written next to or above the temp dirs, and the nested
	// archive's spool is gone.
	if leftover, _ := filepath.Glob(filepath.Join(tmp, "*")); len(leftover) != 0 {
		t.Errorf("%d temp files survived the read", len(leftover))
	}
	if _, err := os.Stat("/etc/inner-evil.fit"); err == nil {
		t.Error("an entry name was used as a path")
	}
}

func TestEncryptedAndNonRegularEntriesAreSkipped(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	enc, _ := zw.CreateRaw(&zip.FileHeader{Name: "enc.fit", Method: zip.Store, Flags: 0x1, CompressedSize64: 20, UncompressedSize64: 20})
	_, _ = enc.Write(fakeFIT("hidden")[:20])
	link := &zip.FileHeader{Name: "link.fit", Method: zip.Store}
	link.SetMode(os.ModeSymlink | 0o777)
	lw, _ := zw.CreateHeader(link)
	_, _ = lw.Write(fakeFIT("link"))
	ok, _ := zw.Create("ok.fit")
	_, _ = ok.Write(fakeFIT("fine"))
	_ = zw.Close()

	rep, got := read(t, buf.Bytes(), testLimits())
	if rep.Err != nil || !sameSet(got, "fine") || rep.Unsupported != 1 {
		t.Errorf("report %+v emitted %q, want the encrypted entry counted unsupported and the symlink silently skipped", rep, got)
	}
}

func TestNotAnArchiveButZipShaped(t *testing.T) {
	rep, _ := read(t, append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0}, 64)...), testLimits())
	if !errors.Is(rep.Err, ErrBadArchive) {
		t.Errorf("err = %v, want ErrBadArchive for a damaged top-level zip", rep.Err)
	}
}

func TestEmitErrorStopsTheRead(t *testing.T) {
	raw := buildZip(t, entry{"a.fit", fakeFIT("1")}, entry{"b.fit", fakeFIT("2")})
	f, size := spool(t, raw)
	stop := errors.New("shutting down")
	calls := 0
	rep := Read(f, size, testLimits(), func([]byte) error { calls++; return stop })
	if !errors.Is(rep.Err, stop) || calls != 1 {
		t.Errorf("err = %v after %d calls, want the emit error and a stop", rep.Err, calls)
	}
}

func TestDefaultLimitsAreTheSpecs(t *testing.T) {
	d := DefaultLimits
	if d.MaxEntryBytes != 64<<20 || d.MaxTotalBytes != 6<<30 || d.MaxEntries != 20000 || d.MaxDepth != 2 || d.MaxRatio != 200 {
		t.Errorf("DefaultLimits = %+v, want 64 MiB, 6 GiB, 20000, depth 2, 200x", d)
	}
}

// craftedZip64 is a one-entry zip whose end record says "one entry" and "the
// directory offset is in the zip64 record", where a zip64 record declares
// `declared` entries: the shape that sent archive/zip to the zip64 record
// without the old guard looking there.
func craftedZip64(t *testing.T, declared uint64) []byte {
	t.Helper()
	base := buildZip(t, entry{"a.fit", fakeFIT("x")})
	eocd := append([]byte(nil), base[len(base)-22:]...)
	body := append([]byte(nil), base[:len(base)-22]...)
	cdSize := binary.LittleEndian.Uint32(eocd[12:])
	cdOff := binary.LittleEndian.Uint32(eocd[16:])

	rec := make([]byte, 56)
	binary.LittleEndian.PutUint32(rec[0:], 0x06064b50)
	binary.LittleEndian.PutUint64(rec[4:], 44)
	binary.LittleEndian.PutUint16(rec[12:], 45)
	binary.LittleEndian.PutUint16(rec[14:], 45)
	binary.LittleEndian.PutUint64(rec[24:], declared)
	binary.LittleEndian.PutUint64(rec[32:], declared)
	binary.LittleEndian.PutUint64(rec[40:], uint64(cdSize))
	binary.LittleEndian.PutUint64(rec[48:], uint64(cdOff))
	loc := make([]byte, 20)
	binary.LittleEndian.PutUint32(loc[0:], 0x07064b50)
	binary.LittleEndian.PutUint64(loc[8:], uint64(len(body)))
	binary.LittleEndian.PutUint32(loc[16:], 1)

	binary.LittleEndian.PutUint32(eocd[16:], 0xFFFFFFFF) // offset: "look in the zip64 record"
	out := append(body, rec...)
	out = append(out, loc...)
	return append(out, eocd...)
}

func TestZip64RecordIsReadWhenOnlyTheOffsetPointsThere(t *testing.T) {
	rep, got := read(t, craftedZip64(t, 1_000_000), testLimits())
	if !errors.Is(rep.Err, ErrTooManyEntries) {
		t.Fatalf("err = %v, want ErrTooManyEntries before the archive is listed", rep.Err)
	}
	if len(got) != 0 {
		t.Errorf("emitted %d files from an archive declaring a million entries", len(got))
	}
}

func TestEntryCountThatLiesLowIsStillBoundedByTheDirectoryLength(t *testing.T) {
	// archive/zip reads entries until the directory ends, whatever the end
	// record says, so a count patched down to 1 must not let 40 long-named
	// entries through a cap of 1.
	var entries []entry
	for i := 0; i < 40; i++ {
		entries = append(entries, entry{fmt.Sprintf("%s/%d.csv", strings.Repeat("d", 100), i), nil})
	}
	raw := buildZip(t, entries...)
	binary.LittleEndian.PutUint16(raw[len(raw)-22+8:], 1)
	binary.LittleEndian.PutUint16(raw[len(raw)-22+10:], 1)
	l := testLimits()
	l.MaxEntries = 1
	rep, _ := read(t, raw, l)
	if !errors.Is(rep.Err, ErrTooManyEntries) {
		t.Errorf("err = %v, want ErrTooManyEntries from the directory's byte length", rep.Err)
	}
}

func TestZip64ArchiveWithMoreThan65535Entries(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 70000; i++ {
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("e/%d.csv", i), Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f, size := spool(t, buf.Bytes())
	n, _, ok := zipDirectory(f, size)
	if !ok || n != 70000 {
		t.Fatalf("declared entries = %d (ok %v), want 70000 from the zip64 record", n, ok)
	}
	l := testLimits()
	l.MaxEntries = 20000
	if rep := Read(f, size, l, func([]byte) error { return nil }); !errors.Is(rep.Err, ErrTooManyEntries) {
		t.Errorf("cap 20000: err = %v, want ErrTooManyEntries", rep.Err)
	}
	l.MaxEntries = 100000
	if rep := Read(f, size, l, func([]byte) error { return nil }); rep.Err != nil {
		t.Errorf("cap 100000: err = %v, want the whole archive read", rep.Err)
	}
}

func TestTheBudgetIsForTheWholeUploadNotEachPart(t *testing.T) {
	part := func(n int) []byte {
		var es []entry
		for i := 0; i < n; i++ {
			es = append(es, entry{fmt.Sprintf("%d.fit", i), fakeFIT(noise(100 << 10))})
		}
		return buildZip(t, es...)
	}
	open := func(raw []byte) Part { f, size := spool(t, raw); return Part{File: f, Size: size} }
	emit := func([]byte) error { return nil }

	// Each part is 600 KiB, under a 1 MiB cap; together they are not.
	l := testLimits()
	l.MaxTotalBytes = 1 << 20
	rep := ReadAll([]Part{open(part(6)), open(part(6))}, l, emit)
	if !errors.Is(rep.Err, ErrTotalSize) {
		t.Errorf("total cap: err = %v, want ErrTotalSize across parts", rep.Err)
	}

	// Each part holds 60 entries, under a cap of 100; together they hold 120.
	l = testLimits()
	l.MaxEntries = 100
	small := func() []byte {
		var es []entry
		for i := 0; i < 60; i++ {
			es = append(es, entry{fmt.Sprintf("%d.csv", i), nil})
		}
		return buildZip(t, es...)
	}
	rep = ReadAll([]Part{open(small()), open(small())}, l, emit)
	if !errors.Is(rep.Err, ErrTooManyEntries) {
		t.Errorf("entry cap: err = %v, want ErrTooManyEntries across parts", rep.Err)
	}
}
