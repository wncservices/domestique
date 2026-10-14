package gpx

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Limits on what an upload may ask this server to hold in memory (audit N6).
// A 20 MiB file of empty <trkpt/> elements used to cost 540 MiB and a second
// to parse, and nested unknown elements cost three seconds of CPU; real routes
// are a few hundred kB and rarely past 50k points.
const (
	// MaxPoints is the most track or route points one uploaded route may hold:
	// about 55 hours of a 1 Hz recording.
	MaxPoints = 200_000
	// MaxWaypoints bounds the <wpt> elements, which ParseCues keeps in full.
	MaxWaypoints = 50_000
	// MaxDepth is how deeply elements may nest. GPX itself is five or six deep.
	MaxDepth = 32
	// MaxElements bounds every element of any kind, so a file cannot cost CPU
	// by being made of things nothing reads.
	MaxElements = 2_000_000

	// Elevation limits for an uploaded point, metres. Wider than any road.
	minElevation = -1000.0
	maxElevation = 10000.0

	// A stored route was accepted under whatever limits applied then, and is
	// re-read on every view, so reading is bounded far looser than uploading:
	// only enough to stop a pathological blob from taking the process down.
	readMaxPoints   = 5_000_000
	readMaxDepth    = 1000
	readMaxElements = 50_000_000
)

var (
	// ErrTooManyPoints, ErrTooDeep and ErrBadCoordinate are what an upload is
	// refused with. They are fit to show the rider and contain no coordinates.
	ErrTooManyPoints  = errors.New("too many points")
	ErrTooDeep        = errors.New("elements nested too deeply")
	ErrBadCoordinate  = errors.New("invalid coordinate")
	errTooManyElement = errors.New("too many elements")
)

// parseSlots limits how many uploads are being parsed at once, process-wide.
// Parsing is CPU and allocation heavy by nature, so a burst of uploads queues
// rather than multiplying; a request waiting for a slot gives up with its
// context.
var parseSlots = make(chan struct{}, 2)

// ParseUpload is ParsePoints for a file someone has just sent: it holds one of
// a few process-wide parse slots, bounds the size, depth and element count of
// what it reads, and refuses a point that is not a place on Earth (NaN or
// infinite, latitude past 90, longitude past 180, an elevation nobody has
// reached). Stored routes are read with ParsePoints, which only bounds the
// pathological.
func ParseUpload(ctx context.Context, raw []byte) ([]Point, error) {
	select {
	case parseSlots <- struct{}{}:
		defer func() { <-parseSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return scanPoints(raw, scanLimits{
		points: MaxPoints, waypoints: MaxWaypoints, depth: MaxDepth, elements: MaxElements, validate: true,
	})
}

type scanLimits struct {
	points, waypoints, depth, elements int
	validate                           bool
}

// scanPoints reads track and route points with a streaming decoder, so memory
// is the points kept and nothing for the tree encoding/xml.Unmarshal would
// build. It accepts exactly what the old struct-tag version did: points of
// <gpx><trk><trkseg><trkpt> in document order, or, when there are none,
// <gpx><rte><rtept>.
func scanPoints(raw []byte, lim scanLimits) ([]Point, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var (
		stack         []string
		track, route  []Point
		cur           *gpxPoint
		inEle         bool
		eleText       strings.Builder
		elements      int
		waypoints     int
		pointsSeen    int
		wasRootGPX    bool
		sawAnyElement bool
	)

scan:
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("could not parse GPX: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if !sawAnyElement {
				sawAnyElement = true
				wasRootGPX = name == "gpx"
				if !wasRootGPX {
					return nil, fmt.Errorf("could not parse GPX: expected element type <gpx> but have <%s>", name)
				}
			}
			elements++
			if elements > lim.elements {
				return nil, fmt.Errorf("could not parse GPX: %w", errTooManyElement)
			}
			stack = append(stack, name)
			if len(stack) > lim.depth {
				return nil, fmt.Errorf("could not parse GPX: %w (more than %d levels)", ErrTooDeep, lim.depth)
			}

			switch {
			case pathIs(stack, "gpx", "trk", "trkseg", "trkpt"), pathIs(stack, "gpx", "rte", "rtept"):
				pointsSeen++
				if pointsSeen > lim.points {
					return nil, fmt.Errorf("%w: a route may have at most %d points", ErrTooManyPoints, lim.points)
				}
				p, err := pointAttrs(t)
				if err != nil {
					return nil, fmt.Errorf("could not parse GPX: %w", err)
				}
				cur = &p
			case pathIs(stack, "gpx", "wpt"):
				waypoints++
				if waypoints > lim.waypoints {
					return nil, fmt.Errorf("%w: a route may have at most %d waypoints", ErrTooManyPoints, lim.waypoints)
				}
			case cur != nil && name == "ele" && (pathIs(stack, "gpx", "trk", "trkseg", "trkpt", "ele") || pathIs(stack, "gpx", "rte", "rtept", "ele")):
				inEle = true
				eleText.Reset()
			}

		case xml.CharData:
			if inEle {
				eleText.Write(t)
			}

		case xml.EndElement:
			switch {
			case inEle && (pathIs(stack, "gpx", "trk", "trkseg", "trkpt", "ele") || pathIs(stack, "gpx", "rte", "rtept", "ele")):
				s := eleText.String()
				cur.Ele = &s
				inEle = false
			case cur != nil && pathIs(stack, "gpx", "trk", "trkseg", "trkpt"):
				p := cur.toPoint()
				if lim.validate {
					if err := validPoint(p, cur, pointsSeenIndex(track)); err != nil {
						return nil, err
					}
				}
				track = append(track, p)
				cur = nil
			case cur != nil && pathIs(stack, "gpx", "rte", "rtept"):
				p := cur.toPoint()
				if lim.validate {
					if err := validPoint(p, cur, pointsSeenIndex(route)); err != nil {
						return nil, err
					}
				}
				route = append(route, p)
				cur = nil
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 {
				// Like xml.Unmarshal, stop at the end of the root element:
				// whatever follows it is not read.
				break scan
			}
		}
	}
	if !sawAnyElement {
		return nil, fmt.Errorf("could not parse GPX: %w", io.EOF)
	}

	points := track
	if len(points) == 0 {
		points = route
	}
	if len(points) < 2 {
		return nil, fmt.Errorf("needs at least 2 track points, found %d", len(points))
	}
	return points, nil
}

func pointsSeenIndex(pts []Point) int { return len(pts) }

func pathIs(stack []string, want ...string) bool {
	if len(stack) != len(want) {
		return false
	}
	for i := range want {
		if stack[i] != want[i] {
			return false
		}
	}
	return true
}

// pointAttrs reads lat and lon the way encoding/xml does for a float
// attribute: absent is zero, anything unparseable is an error.
func pointAttrs(t xml.StartElement) (gpxPoint, error) {
	var p gpxPoint
	for _, a := range t.Attr {
		if a.Name.Space != "" {
			continue
		}
		switch a.Name.Local {
		case "lat", "lon":
			v := strings.TrimSpace(a.Value)
			if v == "" {
				continue
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return p, err
			}
			if a.Name.Local == "lat" {
				p.Lat = f
			} else {
				p.Lon = f
			}
		}
	}
	return p, nil
}

// validPoint refuses a point that is not a place on Earth. The message names
// the point's position and what is wrong with it, never the value.
func validPoint(p Point, src *gpxPoint, index int) error {
	switch {
	case !finite(p.Lat) || !finite(p.Lon):
		return fmt.Errorf("%w: point %d is not a number", ErrBadCoordinate, index+1)
	case p.Lat < -90 || p.Lat > 90:
		return fmt.Errorf("%w: point %d has a latitude outside -90 to 90", ErrBadCoordinate, index+1)
	case p.Lon < -180 || p.Lon > 180:
		return fmt.Errorf("%w: point %d has a longitude outside -180 to 180", ErrBadCoordinate, index+1)
	}
	if src.Ele != nil && strings.TrimSpace(*src.Ele) != "" {
		// Unreadable text is ignored, as toPoint has always done; only a number
		// that is not a height on Earth is refused.
		var ele float64
		if _, err := fmt.Sscanf(strings.TrimSpace(*src.Ele), "%g", &ele); err != nil {
			return nil
		}
		if !finite(ele) || ele < minElevation || ele > maxElevation {
			return fmt.Errorf("%w: point %d has an elevation outside %.0f to %.0f metres", ErrBadCoordinate, index+1, minElevation, maxElevation)
		}
	}
	return nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
