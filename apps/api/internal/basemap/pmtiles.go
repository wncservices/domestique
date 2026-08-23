package basemap

// A minimal, read-only PMTiles v3 client — only what fetching a single tile
// by z/x/y over HTTP Range requests needs. Deliberately not
// github.com/protomaps/go-pmtiles/pmtiles: that package's Bucket
// abstraction pulls in gocloud.dev's S3/GCS/Azure/Mongo-backed drivers
// transitively just to open a local or HTTP archive, which is a lot of
// unrelated dependency surface for a homelab's own security scanning to
// have to reason about for code paths that would never execute here (this
// app only ever reads one file, over plain HTTP, from a Service inside its
// own cluster).
//
// Ported directly from the reference implementation — the `pmtiles` npm
// package apps/web already depends on — rather than derived from the spec's
// prose, and cross-checked against every z/x/y/tileId example in the spec's
// own reference table (github.com/protomaps/PMTiles/blob/main/spec/v3/spec.md)
// before being trusted. In particular the tile-ID Hilbert-curve mapping has
// no pseudocode in the spec itself; this is the same rotate/accumulate
// algorithm the JS client uses, which is itself the standard textbook
// Hilbert d2xy/rotate construction.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
)

const pmtilesHeaderSize = 127

type pmtilesHeader struct {
	RootDirectoryOffset, RootDirectoryLength uint64
	LeafDirectoryOffset                      uint64
	TileDataOffset                           uint64
	InternalCompression, TileCompression     uint8
	MinZoom, MaxZoom                         uint8
}

type pmtilesDirEntry struct {
	TileID, Offset, Length, RunLength uint64
}

// pmtilesClient reads tiles from one archive over HTTP. Safe for concurrent
// use; holds nothing but the URL and an http.Client, so — unlike the JS
// client — it does not cache the header or directories across calls. That
// cache would need its own invalidation the moment an admin replaces the
// archive (see basemap.go's update Job), and this only ever runs on a
// preview-cache miss — once per route, ever, until the basemap itself is
// rebuilt — so the extra handful of small in-cluster requests that costs
// is not worth the staleness risk of getting that invalidation wrong.
type pmtilesClient struct {
	url        string
	httpClient *http.Client
}

func newPMTilesClient(url string) *pmtilesClient {
	return &pmtilesClient{url: url, httpClient: http.DefaultClient}
}

func (c *pmtilesClient) getRange(ctx context.Context, offset, length uint64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("pmtiles: build request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pmtiles: fetch range: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pmtiles: unexpected status %d fetching bytes %d-%d",
			resp.StatusCode, offset, offset+length-1)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pmtiles: read range body: %w", err)
	}
	return data, nil
}

func (c *pmtilesClient) getHeader(ctx context.Context) (*pmtilesHeader, error) {
	raw, err := c.getRange(ctx, 0, pmtilesHeaderSize)
	if err != nil {
		return nil, err
	}
	return parsePMTilesHeader(raw)
}

func parsePMTilesHeader(b []byte) (*pmtilesHeader, error) {
	if len(b) < pmtilesHeaderSize {
		return nil, fmt.Errorf("pmtiles: header short read (%d bytes)", len(b))
	}
	if string(b[0:7]) != "PMTiles" {
		return nil, fmt.Errorf("pmtiles: bad magic number")
	}
	if b[7] != 3 {
		return nil, fmt.Errorf("pmtiles: unsupported spec version %d (this reader only supports v3)", b[7])
	}
	u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }
	return &pmtilesHeader{
		RootDirectoryOffset: u64(8),
		RootDirectoryLength: u64(16),
		LeafDirectoryOffset: u64(40),
		TileDataOffset:      u64(56),
		InternalCompression: b[97],
		TileCompression:     b[98],
		MinZoom:             b[100],
		MaxZoom:             b[101],
	}, nil
}

// pmtilesDecompress supports only what this cluster's own basemap archives
// actually use (confirmed against the live file: internal and tile
// compression are both gzip) — Brotli/Zstd are valid per spec but nothing
// here produces them, so a clear error beats silently mis-decoding.
func pmtilesDecompress(data []byte, method uint8) ([]byte, error) {
	switch method {
	case 1: // none
		return data, nil
	case 2: // gzip
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("pmtiles: gzip: %w", err)
		}
		defer func() { _ = r.Close() }()
		out, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("pmtiles: gzip read: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("pmtiles: unsupported compression method %d", method)
	}
}

// deserializePMTilesDirectory decodes one directory blob (already
// decompressed) into its entries — delta-varint tile IDs, then run
// lengths, then lengths, then offsets, in that order (not interleaved per
// entry). A run_length of 0 marks a leaf-directory pointer rather than a
// tile. Offsets use the spec's "value, or 0 meaning contiguous with the
// previous entry" encoding.
func deserializePMTilesDirectory(data []byte) ([]pmtilesDirEntry, error) {
	pos := 0
	readVarint := func() (uint64, error) {
		v, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return 0, fmt.Errorf("pmtiles: invalid varint at byte %d", pos)
		}
		pos += n
		return v, nil
	}

	numEntries, err := readVarint()
	if err != nil {
		return nil, err
	}
	entries := make([]pmtilesDirEntry, numEntries)

	var tileID uint64
	for i := range entries {
		delta, err := readVarint()
		if err != nil {
			return nil, err
		}
		tileID += delta
		entries[i].TileID = tileID
	}
	for i := range entries {
		rl, err := readVarint()
		if err != nil {
			return nil, err
		}
		entries[i].RunLength = rl
	}
	for i := range entries {
		length, err := readVarint()
		if err != nil {
			return nil, err
		}
		entries[i].Length = length
	}
	for i := range entries {
		v, err := readVarint()
		if err != nil {
			return nil, err
		}
		if v == 0 && i > 0 {
			entries[i].Offset = entries[i-1].Offset + entries[i-1].Length
		} else {
			entries[i].Offset = v - 1
		}
	}
	return entries, nil
}

// findPMTile binary-searches entries (sorted by TileID) for the one
// covering tileID — either an exact match, or the preceding entry if it
// either is a leaf pointer (RunLength 0, which always "covers" everything
// from its own TileID up) or its own contiguous run reaches tileID.
func findPMTile(entries []pmtilesDirEntry, tileID uint64) *pmtilesDirEntry {
	lo, hi := 0, len(entries)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case entries[mid].TileID < tileID:
			lo = mid + 1
		case entries[mid].TileID > tileID:
			hi = mid - 1
		default:
			return &entries[mid]
		}
	}
	if hi >= 0 && (entries[hi].RunLength == 0 || tileID-entries[hi].TileID < entries[hi].RunLength) {
		return &entries[hi]
	}
	return nil
}

// pmtilesRotate is the textbook Hilbert-curve quadrant rotation step.
func pmtilesRotate(n, x, y, rx, ry uint64) (uint64, uint64) {
	if ry == 0 {
		if rx != 0 {
			x = n - 1 - x
			y = n - 1 - y
		}
		return y, x
	}
	return x, y
}

// zxyToPMTileID converts a z/x/y tile coordinate to the single cumulative
// ID PMTiles directories are sorted and looked up by. Verified against
// every row of the spec's own z/x/y/tileID table (z0..z2). z is uint32
// (matching maptile.Zoom's own underlying type, and orb's Tile.Z field) so
// callers never need a narrowing conversion to reach it.
func zxyToPMTileID(z uint32, x, y uint64) uint64 {
	if z == 0 {
		return 0
	}
	zz := uint64(z)
	acc := ((uint64(1)<<zz)*(uint64(1)<<zz) - 1) / 3
	tx, ty := x, y
	for a := uint64(1) << (zz - 1); a > 0; a >>= 1 {
		rx := tx & a
		ry := ty & a
		acc += (3*rx ^ ry) * a
		tx, ty = pmtilesRotate(a, tx, ty, rx, ry)
	}
	return acc
}

// GetTile fetches and decompresses the raw tile bytes at z/x/y, or nil if
// the archive has no tile there (outside its coverage) — not an error, the
// same "best-effort, just skip it" contract the client-side decoder uses
// for a route near the edge of whatever an admin extracted. z is uint32,
// widened against header's own uint8 MinZoom/MaxZoom (the wire format's
// actual field width) rather than narrowing z down to uint8 — z's real
// range only ever comes from this package's own previewMinZoom/MaxZoom
// clamp (8..13), but there is no reason to risk a narrowing conversion
// bug here when the comparison works exactly as well widened the other way.
func (c *pmtilesClient) GetTile(ctx context.Context, header *pmtilesHeader, z uint32, x, y uint64) ([]byte, error) {
	if z < uint32(header.MinZoom) || z > uint32(header.MaxZoom) {
		return nil, nil
	}
	tileID := zxyToPMTileID(z, x, y)

	dirOffset, dirLength := header.RootDirectoryOffset, header.RootDirectoryLength
	const maxDirectoryDepth = 4
	for depth := 0; depth < maxDirectoryDepth; depth++ {
		raw, err := c.getRange(ctx, dirOffset, dirLength)
		if err != nil {
			return nil, err
		}
		dec, err := pmtilesDecompress(raw, header.InternalCompression)
		if err != nil {
			return nil, err
		}
		entries, err := deserializePMTilesDirectory(dec)
		if err != nil {
			return nil, err
		}
		entry := findPMTile(entries, tileID)
		if entry == nil {
			return nil, nil
		}
		if entry.RunLength > 0 {
			raw, err := c.getRange(ctx, header.TileDataOffset+entry.Offset, entry.Length)
			if err != nil {
				return nil, err
			}
			return pmtilesDecompress(raw, header.TileCompression)
		}
		dirOffset = header.LeafDirectoryOffset + entry.Offset
		dirLength = entry.Length
	}
	return nil, fmt.Errorf("pmtiles: maximum directory depth exceeded")
}
