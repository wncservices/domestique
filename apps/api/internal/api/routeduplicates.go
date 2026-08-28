package api

import (
	"math"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// routeDuplicateToleranceM is the flat floor for how close two routes'
// distances have to be to count as the same real ride, imported more than
// once — a fixed minimum for a short route, where 2% would round to
// nothing. distanceWithinTolerance below is what actually decides.
const routeDuplicateToleranceM = 100

// distanceWithinTolerance is the same forgiving comparison
// possibleDuplicateOf already uses against the library: 100m, or 2% of the
// distance, whichever is more forgiving. A flat 100m alone missed real
// duplicates on longer rides — found live, running this feature's own
// cleanup: a 76km ride's two copies were 355m apart, an 89km ride's were
// 288m, a 97km ride's were 384m, all real re-encodes of the same GPX, none
// within a flat 100m. 2% of each of those covers all three.
func distanceWithinTolerance(a, b float64) bool {
	delta := math.Abs(a - b)
	return delta <= routeDuplicateToleranceM || delta <= math.Max(a, b)*0.02
}

type routeDuplicateGroupDTO struct {
	Name   string     `json:"name"`
	Routes []routeDTO `json:"routes"`
}

// handleRouteDuplicates groups library routes that look like repeated
// imports of the same real ride — the shape a rider actually hits when
// Garmin sync-back (or a Komoot import, or a plain re-upload) runs more
// than once against the same source. Cross-rider by nature (the same route
// can turn up uploaded by two different identities, exactly what this
// deployment's own history produced), so this is admin-scoped rather than
// "my own routes" the way most of this file's other handlers are.
func (s *Server) handleRouteDuplicates(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermEditAny) {
		return
	}

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	linked, ok := s.linkedAccounts(r.Context(), w)
	if !ok {
		return
	}
	crews, ok := s.crewSnapshot(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, groupDuplicateRoutes(routes, func(rt model.Route) routeDTO {
		return s.toRouteDTO(r.Context(), rt, linked, crews)
	}))
}

// groupDuplicateRoutes groups routes sharing a name (case-insensitive,
// trimmed) and a distance within tolerance of each other (see
// distanceWithinTolerance), returning only groups with more than one
// member — same shape and same reasoning as garmincourses.go's
// groupDuplicateCourses: content_hash alone would miss it, since Garmin
// re-encodes a GPX slightly differently on every download, so the same
// real ride imported twice from Garmin does not reliably hash the same
// even though its name and distance do. Compared against each group's own
// anchor distance, not an independently-rounded bucket, for the identical
// reason groupDuplicateCourses does — see its own comment.
func groupDuplicateRoutes(routes []model.Route, toDTO func(model.Route) routeDTO) []routeDuplicateGroupDTO {
	type group struct {
		name    string
		anchor  float64
		members []model.Route
	}

	var groups []*group
	for _, rt := range routes {
		name := strings.ToLower(strings.TrimSpace(rt.Name))
		var target *group
		for _, g := range groups {
			if g.name == name && distanceWithinTolerance(g.anchor, rt.Stats.DistanceM) {
				target = g
				break
			}
		}
		if target == nil {
			target = &group{name: name, anchor: rt.Stats.DistanceM}
			groups = append(groups, target)
		}
		target.members = append(target.members, rt)
	}

	out := make([]routeDuplicateGroupDTO, 0)
	for _, g := range groups {
		if len(g.members) < 2 {
			continue
		}
		dto := routeDuplicateGroupDTO{Name: g.members[0].Name}
		for _, rt := range g.members {
			dto.Routes = append(dto.Routes, toDTO(rt))
		}
		out = append(out, dto)
	}
	return out
}
