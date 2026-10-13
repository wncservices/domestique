// Package model holds the core domain types.
//
// The library on disk is the desired state; the state store records what each
// remote account actually holds. Everything else is a diff between the two.
package model

import "fmt"

// Provider is a route host we can push to.
type Provider string

const (
	ProviderGarmin Provider = "garmin"
	ProviderWahoo  Provider = "wahoo"
)

// Account is one rider's account on one provider, e.g. "garmin:wilant".
type Account struct {
	ID       string   `yaml:"id"`
	Provider Provider `yaml:"provider"`
	Rider    string   `yaml:"rider"`
	Label    string   `yaml:"label,omitempty"`
	// AutoPush is whether auto-sync's background push includes this account.
	// Defaults true (an opt-out, not an opt-in) — see accounts.schema's own
	// comment for why.
	AutoPush bool `yaml:"autoPush"`
}

// EnvPrefix is the env var prefix for this account's credentials, e.g. GARMIN_WILANT.
func (a Account) EnvPrefix() string {
	return sanitizeEnv(fmt.Sprintf("%s_%s", a.Provider, a.Rider))
}

// Sport distinguishes what kind of activity a route is for. It changes how
// a route reaches a device, not just how it looks in the library: a FIT
// course's own Sport field (see internal/fitcourse) decides whether a head
// unit shows pace or speed and how it auto-laps, and Wahoo's Cloud API
// takes a separate, explicit workout_type_family_id per route (see
// internal/wahoo) that has to agree with it.
type Sport string

const (
	SportCycling Sport = "cycling"
	SportRunning Sport = "running"
)

// RouteMeta is a route's editable metadata — the contents of route.yaml in a
// filesystem library, or the metadata columns in a database one.
type RouteMeta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// Targets nil means "use the library default"; an empty list means "push nowhere".
	Targets *[]string `yaml:"targets,omitempty"`
	Tags    []string  `yaml:"tags,omitempty"`
	Enabled *bool     `yaml:"enabled,omitempty"`
	// Sport defaults to SportCycling when empty — see EffectiveSport. This
	// library was cycling-only before this field existed, so every route
	// from before it keeps behaving exactly as it did.
	Sport Sport `yaml:"sport,omitempty"`
}

// IsEnabled reports whether the route should be synced. Absent means yes.
func (m RouteMeta) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

// EffectiveSport is Sport, defaulting to SportCycling when unset — the one
// place that default is decided, so every caller (FIT encoding, the Wahoo
// push, the UI) reads it the same way rather than each repeating the same
// `if sport == "" { ... }` check.
func (m RouteMeta) EffectiveSport() Sport {
	if m.Sport == "" {
		return SportCycling
	}
	return m.Sport
}

// RouteStats is derived from the GPX track. Wahoo requires these at create time.
type RouteStats struct {
	DistanceM  float64
	AscentM    float64
	StartLat   float64
	StartLng   float64
	PointCount int
}

// Route is one route in the library: a GPX track plus its metadata and stats.
//
// It carries no file paths on purpose — a route may come from a directory or
// from a database row, and only its source knows which. Fetch the track itself
// through the source.
type Route struct {
	RouteMeta

	Slug        string
	Stats       RouteStats
	ContentHash string
	// Origin is a human-readable hint about where this route came from
	// (a path, or "database"). Display only.
	Origin string
	// Owner is the user who uploaded the route, when known. Role checks use
	// it to decide who may edit or delete: riders own what they upload,
	// admins may touch anything. Empty for routes from a git library, where
	// ownership is git history's job.
	Owner string
	// UpdatedAt is when the route last changed in its source, if known.
	UpdatedAt string
	// CreatedAt is when the route was created. It is the generation marker of
	// the slug: slugs are reusable once a route is deleted, so anything that
	// remembers a route by slug (a share link) also remembers this.
	CreatedAt string
}

// Op is the kind of change a plan item represents.
type Op string

const (
	OpCreate Op = "create"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
	OpNoop   Op = "noop"
)

// PlanItem is one intended change against one account.
type PlanItem struct {
	Op        Op
	AccountID string
	Slug      string
	Reason    string
	RemoteID  string
	Route     *Route
}

// Plan is the full set of intended changes across all accounts.
type Plan struct {
	Items []PlanItem
}

// Changes returns everything that is not already up to date.
func (p Plan) Changes() []PlanItem {
	out := make([]PlanItem, 0, len(p.Items))
	for _, item := range p.Items {
		if item.Op != OpNoop {
			out = append(out, item)
		}
	}
	return out
}

// PlanKey identifies one item within a plan — the pair a rider actually picks
// from when choosing which changes to push.
type PlanKey struct {
	AccountID string
	Slug      string
}

// Select narrows a plan to only the given account/slug pairs. A nil or empty
// set is a no-op: the caller means "everything", the same as before this
// existed.
func (p Plan) Select(keys map[PlanKey]bool) Plan {
	if len(keys) == 0 {
		return p
	}
	out := Plan{Items: make([]PlanItem, 0, len(p.Items))}
	for _, item := range p.Items {
		if keys[PlanKey{AccountID: item.AccountID, Slug: item.Slug}] {
			out.Items = append(out.Items, item)
		}
	}
	return out
}

func sanitizeEnv(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - 32
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}
