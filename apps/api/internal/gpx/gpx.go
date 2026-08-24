package gpx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

const (
	// ascentThresholdM ignores elevation wobble below this when summing ascent.
	// GPS barometers are noisy; without it a flat ride reports a few hundred
	// metres of climbing.
	ascentThresholdM = 3.0

	// hashPrecision is the coordinate precision used for hashing: ~1m, well
	// below any meaningful route edit.
	hashPrecision = 5

	earthRadiusM = 6371000.0
)

// Point is a single track point. Elevation is optional.
type Point struct {
	Lat, Lon float64
	Ele      float64
	HasEle   bool
}

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

type gpxPoint struct {
	Lat float64 `xml:"lat,attr"`
	Lon float64 `xml:"lon,attr"`
	Ele *string `xml:"ele"`
}

func (p gpxPoint) toPoint() Point {
	out := Point{Lat: p.Lat, Lon: p.Lon}
	if p.Ele != nil {
		var ele float64
		if _, err := fmt.Sscanf(strings.TrimSpace(*p.Ele), "%g", &ele); err == nil {
			out.Ele, out.HasEle = ele, true
		}
	}
	return out
}

// ReadPoints flattens a GPX file on disk into a single ordered point list.
func ReadPoints(path string) ([]Point, error) {
	// #nosec G304 -- callers resolve and validate the path; the FS source
	// refuses anything that escapes the library root.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	points, err := ParsePoints(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return points, nil
}

// ParsePoints flattens GPX bytes into a single ordered point list.
//
// It accepts tracks or routes: planners disagree about which element a planned
// route belongs in, and we treat both as the same thing.
func ParsePoints(raw []byte) ([]Point, error) {
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

// NeedsElevation reports whether points carries no usable elevation of its
// own — either no point has any (HasEle false throughout, the ordinary
// shape for a GPX that never had an <ele> tag at all), or, the shape that
// actually prompted this function, every single point that does have one
// reports exactly 0.0. Real GPS or barometric elevation is never that
// uniform for more than a couple of points in a row — the only source
// that produces it is a route-planning tool that never queried a real
// terrain source and used 0 as a placeholder rather than omitting the
// field. A route with even a little genuine, nonzero elevation data
// (however sparse) is left alone; this only flags the case where there is
// nothing worth trusting at all.
func NeedsElevation(points []Point) bool {
	for _, p := range points {
		if p.HasEle && p.Ele != 0 {
			return false
		}
	}
	return true
}

// ComputeStats derives the metrics providers ask for at create time.
func ComputeStats(points []Point) model.RouteStats {
	var distance, ascent float64
	for i := 1; i < len(points); i++ {
		distance += haversineM(points[i-1], points[i])
	}

	var lastEle float64
	haveLast := false
	for _, p := range points {
		if !p.HasEle {
			continue
		}
		if !haveLast {
			lastEle, haveLast = p.Ele, true
			continue
		}
		switch delta := p.Ele - lastEle; {
		case delta >= ascentThresholdM:
			ascent += delta
			lastEle = p.Ele
		case delta <= -ascentThresholdM:
			lastEle = p.Ele
		}
	}

	return model.RouteStats{
		DistanceM:  distance,
		AscentM:    ascent,
		StartLat:   points[0].Lat,
		StartLng:   points[0].Lon,
		PointCount: len(points),
	}
}

// ContentHash is a stable hash of what a provider would actually see.
//
// It deliberately excludes timestamps, extensions and whitespace, so
// re-exporting the same route from a different planner is not a change.
func ContentHash(points []Point, name, description string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s", strings.TrimSpace(name), strings.TrimSpace(description))
	for _, p := range points {
		fmt.Fprintf(h, "\x00%.*f,%.*f", hashPrecision, p.Lat, hashPrecision, p.Lon)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// renderDoc mirrors just enough of the GPX schema to write a planned route —
// the write side of gpxDoc, kept separate since a `*string` element pointer
// (gpxPoint.Ele) reads absent-vs-empty cleanly but marshals a nil into
// nothing useful; Render always has a real elevation or none at all, never
// something in between worth a pointer over.
type renderDoc struct {
	XMLName xml.Name `xml:"gpx"`
	Version string   `xml:"version,attr"`
	Creator string   `xml:"creator,attr"`
	NS      string   `xml:"xmlns,attr"`
	Trk     struct {
		Name string `xml:"name"`
		Seg  struct {
			Points []renderPoint `xml:"trkpt"`
		} `xml:"trkseg"`
	} `xml:"trk"`
}

type renderPoint struct {
	Lat float64  `xml:"lat,attr"`
	Lon float64  `xml:"lon,attr"`
	Ele *float64 `xml:"ele,omitempty"`
}

// Render writes points as a minimal GPX 1.1 track — the reverse of
// ParsePoints. For a track that did not start life as GPX (a FIT course
// decoded via fitcourse.Decode, for instance) but still needs to become one
// to reach source.CreateRequest.GPX, the one input every route in the
// library is built from.
func Render(name string, points []Point) ([]byte, error) {
	if len(points) < 2 {
		return nil, fmt.Errorf("gpx: need at least 2 points to render, got %d", len(points))
	}

	var doc renderDoc
	doc.Version = "1.1"
	doc.Creator = "Domestique"
	doc.NS = "http://www.topografix.com/GPX/1/1"
	doc.Trk.Name = name

	for _, p := range points {
		point := renderPoint{Lat: p.Lat, Lon: p.Lon}
		if p.HasEle {
			ele := p.Ele
			point.Ele = &ele
		}
		doc.Trk.Seg.Points = append(doc.Trk.Seg.Points, point)
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("gpx: render: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// DistanceM is the great-circle distance between two points, in metres.
func DistanceM(a, b Point) float64 { return haversineM(a, b) }

func haversineM(a, b Point) float64 {
	p1, p2 := a.Lat*math.Pi/180, b.Lat*math.Pi/180
	dp := p2 - p1
	dl := (b.Lon - a.Lon) * math.Pi / 180
	x := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(x))
}
