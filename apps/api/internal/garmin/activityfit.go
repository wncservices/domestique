package garmin

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// activityFITPath is Connect's own FIT export for one completed activity —
// the same file the "Export Original" button in Connect's UI produces.
const activityFITPath = "/download-service/files/activity/"

// MaxFITBytes caps a downloaded FIT file, compressed or not. Without it an
// unbounded read — of the response body, or of a zip entry's decompressed
// stream — is a way a misbehaving or malicious upstream could take this pod
// down; see internal/fitcourse's own FIT-encoding doc comment for why FIT is
// a binary format worth taking seriously rather than trusting blindly.
const MaxFITBytes = 32 << 20

// fitSignature is a FIT file header's own magic bytes, at offset 8 of the
// header, per the Global FIT SDK.
var fitSignature = []byte(".FIT")

// ActivityFIT downloads one activity's original FIT file.
//
// Connect's own download endpoint answers with a zip holding exactly one
// .fit entry — confirmed against Connect's own "Export Original" download —
// though a bare FIT body (no zip wrapper) has also been observed, so both
// are accepted rather than assuming the zip is always present.
func (c *Client) ActivityFIT(ctx context.Context, activityID string) ([]byte, error) {
	if strings.TrimSpace(activityID) == "" {
		return nil, errors.New("garmin: no activity id to download")
	}

	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := c.APIBase + activityFITPath + activityID
	if err := c.allowedHost(endpoint); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	// The same headers Activities sends: this is the same authenticated
	// Connect session, and Connect's bot protection is what motivated
	// sending them there in the first place.
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("garmin: downloading activity FIT: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readLimited(resp.Body, MaxFITBytes)
	if err != nil {
		return nil, fmt.Errorf("garmin: reading activity FIT: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("garmin: activity FIT download returned %d: %s", resp.StatusCode, snippet(raw))
	}

	fit, err := extractFIT(raw)
	if err != nil {
		return nil, fmt.Errorf("garmin: activity %s: %w", activityID, err)
	}
	return fit, nil
}

// readLimited reads up to max+1 bytes and reports an error if the source had
// more than that — the only way to tell "read everything, it was small" from
// "silently truncated at the cap" apart, which io.ReadAll(io.LimitReader(...))
// alone cannot do.
func readLimited(r io.Reader, max int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("response exceeded the %d byte limit", max)
	}
	return raw, nil
}

// extractFIT accepts either a zip containing one .fit entry (Connect's own
// shape) or a bare FIT body, and validates the FIT signature either way —
// so a 404 or an HTML error page fails here with a clear reason rather than
// being handed back as if it were ride data.
func extractFIT(raw []byte) ([]byte, error) {
	if zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw))); err == nil {
		return extractFITFromZip(zr)
	}
	return validateFIT(raw)
}

func extractFITFromZip(zr *zip.Reader) ([]byte, error) {
	var entry *zip.File
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".fit") {
			continue
		}
		if entry != nil {
			// Connect's own download is documented nowhere, and this
			// package has only ever observed one .fit per zip. A second
			// one means the assumption "there is exactly one" no longer
			// holds, and picking the first silently would risk returning
			// the wrong ride's data — safer to fail loudly than guess.
			return nil, fmt.Errorf("the zip contained more than one .fit entry (at least %q and %q), which .fit to use is ambiguous", entry.Name, f.Name)
		}
		entry = f
	}
	if entry == nil {
		return nil, errors.New("the zip contained no .fit entry")
	}

	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("opening the zip entry: %w", err)
	}
	defer func() { _ = rc.Close() }()

	// entry.Open() decompresses on read: capping via io.LimitReader here
	// stops an inflated entry past MaxFITBytes without decompressing the
	// rest of it, the zip-bomb case a size check on the compressed bytes
	// alone would miss entirely.
	data, err := readLimited(rc, MaxFITBytes)
	if err != nil {
		return nil, fmt.Errorf("reading the zip entry: %w", err)
	}
	return validateFIT(data)
}

func validateFIT(data []byte) ([]byte, error) {
	if len(data) < 12 || !bytes.Equal(data[8:12], fitSignature) {
		return nil, errors.New("not a FIT file (missing the .FIT signature)")
	}
	return data, nil
}
