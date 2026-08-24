// Package sync is the diff engine: routes + state -> plan, and plan -> execution.
package sync

import (
	"context"
	"fmt"
	"sort"

	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

// BuildPlan compares the routes on offer against recorded remote state, for
// each linked account.
//
// The accounts are passed in rather than read from config: they are linked by
// riders through the UI and live in the database, so the caller fetches them.
// crews is likewise fetched fresh by the caller — a member removed since the
// last plan must stop appearing on this one, not on the next process restart.
//
// crewSharing picks which of config's two target-resolution rules decides
// who is pushed which route. false — every general-purpose caller (the
// CLI, the Library page's own "Push to devices", auto-sync, and this
// function's own read-only cousin behind GET /api/plan) — uses
// config.PushTargetsFor, which never reaches past a route's own owner. true
// is reserved for the crew ride scheduler's own explicit, one-route
// "sync now" action (see api.handleSyncRide), the one deliberate case
// where a rider is knowingly asking for a specific route to reach their
// crew's devices right now; it uses the older, crew-aware
// config.TargetsFor. See PushTargetsFor's own doc comment for why the
// general-purpose path is restricted this way at all.
//
// It returns an error rather than treating unreadable state as empty: an empty
// plan reads as "nothing to do", but empty *state* means "push everything
// again", and the two must never be confused.
func BuildPlan(ctx context.Context, routes []model.Route, linked []model.Account, store state.Store, crews crew.Snapshot, crewSharing bool) (model.Plan, error) {
	var plan model.Plan

	for _, account := range linked {
		recorded, err := store.ForAccount(ctx, account.ID)
		if err != nil {
			return model.Plan{}, fmt.Errorf("read state for %s: %w", account.ID, err)
		}

		desired := map[string]model.Route{}
		for _, route := range routes {
			wantedBy := config.PushTargetsFor(route, linked)
			if crewSharing {
				wantedBy = config.TargetsFor(route, linked, crews)
			}
			for _, target := range wantedBy {
				if target == account.ID {
					desired[route.Slug] = route
				}
			}
		}

		for _, slug := range sortedRouteKeys(desired) {
			route := desired[slug]
			known, seen := recorded[slug]
			switch {
			case !seen:
				plan.Items = append(plan.Items, model.PlanItem{
					Op: model.OpCreate, AccountID: account.ID, Slug: slug,
					Route: routePtr(route), Reason: "never pushed",
				})
			case known.ContentHash != route.ContentHash:
				plan.Items = append(plan.Items, model.PlanItem{
					Op: model.OpUpdate, AccountID: account.ID, Slug: slug,
					Route: routePtr(route), RemoteID: known.RemoteID,
					Reason: "route changed since last push",
				})
			default:
				plan.Items = append(plan.Items, model.PlanItem{
					Op: model.OpNoop, AccountID: account.ID, Slug: slug,
					Route: routePtr(route), RemoteID: known.RemoteID,
					Reason: "up to date",
				})
			}
		}

		for _, slug := range sortedEntryKeys(recorded) {
			if _, wanted := desired[slug]; wanted {
				continue
			}
			plan.Items = append(plan.Items, model.PlanItem{
				Op: model.OpDelete, AccountID: account.ID, Slug: slug,
				RemoteID: recorded[slug].RemoteID,
				Reason:   "removed from the library or no longer targeted",
			})
		}
	}

	return plan, nil
}

// Apply executes a plan. It returns per-item failures; one bad route never
// aborts the run, because the other rider's routes should still go out.
//
// onResult, if non-nil, is called once per changed item — success (err ==
// nil) or failure — after that item is fully processed. This is the seam
// observability hooks into (the API server's own push metrics, notably)
// without this package importing anything about metrics itself: it stays
// exactly what its own doc says, pure, given routes/state/targets, nothing
// else. Pass nil where nothing needs it, the CLI push command among them.
func Apply(ctx context.Context, plan model.Plan, store state.Store, byAccount map[string]targets.Target, onResult func(item model.PlanItem, err error)) []error {
	var failures []error

	for _, item := range plan.Changes() {
		target, ok := byAccount[item.AccountID]
		if !ok {
			err := fmt.Errorf("%s: no configured target adapter", item.AccountID)
			failures = append(failures, err)
			if onResult != nil {
				onResult(item, err)
			}
			continue
		}

		err := applyOne(ctx, item, store, target)
		if err != nil {
			err = fmt.Errorf("%s %s: %s failed: %w", item.AccountID, item.Slug, item.Op, err)
			failures = append(failures, err)
		}
		if onResult != nil {
			onResult(item, err)
		}
	}

	return failures
}

func applyOne(ctx context.Context, item model.PlanItem, store state.Store, target targets.Target) error {
	switch item.Op {
	case model.OpCreate:
		remoteID, err := target.Create(ctx, *item.Route)
		if err != nil {
			return err
		}
		return store.Record(ctx, state.Entry{
			AccountID: item.AccountID, Slug: item.Slug, RemoteID: remoteID,
			ContentHash: item.Route.ContentHash, Name: item.Route.Name,
		})

	case model.OpUpdate:
		remoteID, err := target.Update(ctx, item.RemoteID, *item.Route)
		if err != nil {
			return err
		}
		return store.Record(ctx, state.Entry{
			AccountID: item.AccountID, Slug: item.Slug, RemoteID: remoteID,
			ContentHash: item.Route.ContentHash, Name: item.Route.Name,
		})

	case model.OpDelete:
		if err := target.Delete(ctx, item.RemoteID); err != nil {
			return err
		}
		return store.Forget(ctx, item.AccountID, item.Slug)
	}

	return nil
}

func routePtr(r model.Route) *model.Route { return &r }

func sortedRouteKeys(m map[string]model.Route) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedEntryKeys(m map[string]state.Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
