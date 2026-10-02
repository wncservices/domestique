// Package routefixture builds synthetic routes for tests: a straight line
// north from an arbitrary point, its elevation following a list of
// (length, grade) pieces. It exists so no test ever needs a real GPX file,
// since a real track is personal location data (AGENTS.md). Nothing here is
// a real place or a real ride.
package routefixture

import (
	"bytes"
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// StartLat and StartLon are where every fixture begins. Distinctive on
// purpose, so a test can scan a response body for them and for the latitudes
// the track climbs through, and fail if any coordinate leaked.
const (
	StartLat = 51.23456
	StartLon = 4.56789
)

// Piece is a stretch of the route: its length in metres and its grade in
// percent.
type Piece struct{ LengthM, Grade float64 }

// Points returns the route as points spacingM apart, starting at baseEle.
func Points(spacingM, baseEle float64, pieces ...Piece) []gpx.Point {
	latStep := spacingM / 111_320.0
	pts := []gpx.Point{{Lat: StartLat, Lon: StartLon, Ele: baseEle, HasEle: true}}
	lat, ele := StartLat, baseEle
	for _, p := range pieces {
		for d := 0.0; d < p.LengthM; d += spacingM {
			lat += latStep
			ele += spacingM * p.Grade / 100
			pts = append(pts, gpx.Point{Lat: lat, Lon: StartLon, Ele: ele, HasEle: true})
		}
	}
	return pts
}

// GPX is Points written as a GPX 1.1 track named name. withEle false leaves
// the <ele> tags out, the shape a route with no elevation arrives in.
func GPX(name string, withEle bool, spacingM, baseEle float64, pieces ...Piece) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="routefixture" xmlns="http://www.topografix.com/GPX/1/1">
  <trk><name>%s</name><trkseg>
`, name)
	for _, p := range Points(spacingM, baseEle, pieces...) {
		if withEle {
			fmt.Fprintf(&b, "    <trkpt lat=\"%.6f\" lon=\"%.6f\"><ele>%.2f</ele></trkpt>\n", p.Lat, p.Lon, p.Ele)
		} else {
			fmt.Fprintf(&b, "    <trkpt lat=\"%.6f\" lon=\"%.6f\"></trkpt>\n", p.Lat, p.Lon)
		}
	}
	b.WriteString("  </trkseg></trk>\n</gpx>\n")
	return b.Bytes()
}

// Hilly is the standard fixture: a flat lead-in, a 2 km climb at 6 %, a
// descent, a second 1.2 km climb at 7 %, and a flat finish. About 9 km with
// roughly 200 m of ascent, two climbs a training bar of 1 km and 3 % keeps.
func Hilly() []Piece {
	return []Piece{
		{1500, 0}, {2000, 6}, {1000, -5}, {500, 0}, {1200, 7}, {1000, -4}, {1500, 0},
	}
}
