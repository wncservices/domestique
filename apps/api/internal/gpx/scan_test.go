package gpx

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// gpxDoc mirrors just enough of the GPX schema to read a planned route.
type gpxDoc struct {
	XMLName xml.Name `xml:"gpx"`
	Tracks  []struct {
		Segments []struct {
			Points []gpxPoint `xml:"trkpt"`
		} `xml:"trkseg"`
	} `xml:"trk"`
	Routes []struct {
		Points []gpxPoint `xml:"rtept"`
	} `xml:"rte"`
}

// oldParsePoints is ParsePoints as it was before the streaming scan: a full
// encoding/xml.Unmarshal into a struct tree. It is kept here only as the
// oracle the streaming version has to agree with.
func oldParsePoints(raw []byte) ([]Point, error) {
	var doc gpxDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("could not parse GPX: %w", err)
	}
	var points []Point
	for _, track := range doc.Tracks {
		for _, seg := range track.Segments {
			for _, p := range seg.Points {
				points = append(points, p.toPoint())
			}
		}
	}
	if len(points) == 0 {
		for _, route := range doc.Routes {
			for _, p := range route.Points {
				points = append(points, p.toPoint())
			}
		}
	}
	if len(points) < 2 {
		return nil, fmt.Errorf("needs at least 2 track points, found %d", len(points))
	}
	return points, nil
}

func gpxWith(points string) []byte {
	return []byte(`<?xml version="1.0"?><gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>` + points + `</trkseg></trk></gpx>`)
}

func manyPoints(n int) []byte {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(`<trkpt lat="51.0" lon="4.0"/>`)
	}
	return gpxWith(sb.String())
}

func TestScanAgreesWithTheOldParserOnRealShapes(t *testing.T) {
	for name, raw := range map[string][]byte{
		"track with elevation": gpxWith(`<trkpt lat="51.0" lon="4.0"><ele>12.5</ele><time>2026-01-01T00:00:00Z</time></trkpt><trkpt lat="51.1" lon="4.1"><ele>13</ele></trkpt>`),
		"no elevation":         gpxWith(`<trkpt lat="51.0" lon="4.0"/><trkpt lat="51.1" lon="4.1"/>`),
		"blank and junk ele":   gpxWith(`<trkpt lat="51.0" lon="4.0"><ele></ele></trkpt><trkpt lat="51.1" lon="4.1"><ele>abc</ele></trkpt>`),
		"route only":           []byte(`<gpx><rte><rtept lat="1" lon="2"><ele>3</ele></rtept><rtept lat="1.5" lon="2.5"/></rte></gpx>`),
		"track beats route":    []byte(`<gpx><rte><rtept lat="9" lon="9"/><rtept lat="8" lon="8"/></rte><trk><trkseg><trkpt lat="1" lon="2"/><trkpt lat="3" lon="4"/></trkseg></trk></gpx>`),
		"two segments":         []byte(`<gpx><trk><trkseg><trkpt lat="1" lon="2"/></trkseg><trkseg><trkpt lat="3" lon="4"/></trkseg></trk><trk><trkseg><trkpt lat="5" lon="6"/></trkseg></trk></gpx>`),
		"metadata and wpt":     []byte(`<gpx><metadata><name>x</name></metadata><wpt lat="1" lon="1"><name>a</name></wpt><trk><name>t</name><trkseg><trkpt lat="1" lon="2"/><trkpt lat="3" lon="4"/></trkseg></trk></gpx>`),
		"extensions in a pt":   gpxWith(`<trkpt lat="1" lon="2"><extensions><ele>999</ele><hr>140</hr></extensions><ele>5</ele></trkpt><trkpt lat="3" lon="4"/>`),
		"one point":            gpxWith(`<trkpt lat="1" lon="2"/>`),
		"wrong root":           []byte(`<kml><trk/></kml>`),
		"truncated":            []byte(`<gpx><trk><trkseg><trkpt lat="1" lon="2"/>`),
		"bad lat":              gpxWith(`<trkpt lat="north" lon="2"/><trkpt lat="3" lon="4"/>`),
		"empty":                {},
		"not xml":              []byte("plain text"),
	} {
		t.Run(name, func(t *testing.T) {
			want, wantErr := oldParsePoints(raw)
			got, gotErr := scanPoints(raw, scanLimits{points: readMaxPoints, waypoints: readMaxPoints, depth: readMaxDepth, elements: readMaxElements})
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("old err = %v, scan err = %v", wantErr, gotErr)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("old = %+v\nscan = %+v", want, got)
			}
		})
	}
}

// The same agreement, over whatever the fuzzer invents.
func FuzzScanAgreesWithTheOldParser(f *testing.F) {
	f.Add(gpxWith(`<trkpt lat="51.0" lon="4.0"><ele>12.5</ele></trkpt><trkpt lat="51.1" lon="4.1"/>`))
	f.Add([]byte(`<gpx><rte><rtept lat="1" lon="2"/><rtept lat="3" lon="4"/></rte></gpx>`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		want, wantErr := oldParsePoints(raw)
		got, gotErr := scanPoints(raw, scanLimits{points: readMaxPoints, waypoints: readMaxPoints, depth: readMaxDepth, elements: readMaxElements})
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("old err = %v, scan err = %v", wantErr, gotErr)
		}
		if wantErr == nil && !reflect.DeepEqual(want, got) {
			t.Fatalf("old = %+v\nscan = %+v", want, got)
		}
	})
}

func TestParseUploadRefusesTooManyPoints(t *testing.T) {
	if _, err := ParseUpload(context.Background(), manyPoints(MaxPoints)); err != nil {
		t.Fatalf("exactly the limit is fine: %v", err)
	}
	_, err := ParseUpload(context.Background(), manyPoints(MaxPoints+1))
	if !errors.Is(err, ErrTooManyPoints) {
		t.Fatalf("err = %v, want ErrTooManyPoints", err)
	}
}

func TestParseUploadRefusesDeepNesting(t *testing.T) {
	deep := `<gpx>` + strings.Repeat("<a>", 5000) + strings.Repeat("</a>", 5000) + `</gpx>`
	start := time.Now()
	_, err := ParseUpload(context.Background(), []byte(deep))
	if !errors.Is(err, ErrTooDeep) {
		t.Fatalf("err = %v, want ErrTooDeep", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("refusing the nesting took %v", time.Since(start))
	}
}

func TestParseUploadRefusesPointsThatAreNotOnEarth(t *testing.T) {
	ok := `<trkpt lat="51.0" lon="4.0"/>`
	for name, bad := range map[string]string{
		"NaN latitude":     `<trkpt lat="NaN" lon="4"/>`,
		"Inf longitude":    `<trkpt lat="51" lon="+Inf"/>`,
		"latitude 91":      `<trkpt lat="91" lon="4"/>`,
		"latitude -90.1":   `<trkpt lat="-90.1" lon="4"/>`,
		"longitude 181":    `<trkpt lat="51" lon="181"/>`,
		"huge":             `<trkpt lat="1e300" lon="4"/>`,
		"elevation huge":   `<trkpt lat="51" lon="4"><ele>1e9</ele></trkpt>`,
		"elevation NaN":    `<trkpt lat="51" lon="4"><ele>NaN</ele></trkpt>`,
		"elevation -5000":  `<trkpt lat="51" lon="4"><ele>-5000</ele></trkpt>`,
		"elevation 20000m": `<trkpt lat="51" lon="4"><ele>20000</ele></trkpt>`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseUpload(context.Background(), gpxWith(ok+bad+ok))
			if !errors.Is(err, ErrBadCoordinate) {
				t.Fatalf("err = %v, want ErrBadCoordinate", err)
			}
			if strings.Contains(err.Error(), "1e300") || strings.Contains(err.Error(), "NaN") {
				t.Errorf("error %q echoes the value", err)
			}
		})
	}

	for name, good := range map[string]string{
		"poles and antimeridian": `<trkpt lat="-90" lon="180"/><trkpt lat="90" lon="-180"/>`,
		"dead sea":               `<trkpt lat="31.5" lon="35.5"><ele>-430</ele></trkpt><trkpt lat="31.6" lon="35.5"/>`,
		"stelvio":                `<trkpt lat="46.5" lon="10.4"><ele>2758</ele></trkpt><trkpt lat="46.6" lon="10.4"/>`,
		"unreadable ele":         `<trkpt lat="46.5" lon="10.4"><ele>n/a</ele></trkpt><trkpt lat="46.6" lon="10.4"/>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseUpload(context.Background(), gpxWith(good)); err != nil {
				t.Fatalf("err = %v, want it accepted", err)
			}
		})
	}
}

// Only so many uploads parse at once; the rest wait for a slot or for their
// request to give up.
func TestParseUploadWaitsForAParseSlot(t *testing.T) {
	for i := 0; i < cap(parseSlots); i++ {
		parseSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(parseSlots); i++ {
			<-parseSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := ParseUpload(ctx, manyPoints(2))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the request's deadline while every slot is busy", err)
	}
}
