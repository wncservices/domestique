// Package crew controls who a rider's routes may reach beyond their own
// devices.
//
// Without it, a route with no explicit targets goes to every linked account
// in the deployment (config.TargetsFor's documented default), and nothing
// stops a rider from naming another rider's account directly — there is no
// consent or relationship check anywhere on that path. A crew is that
// relationship: a rider creates one and becomes its owner, other riders
// request to join and the owner approves or denies, or the owner starts it
// from their own end by adding someone directly instead of waiting on a
// request. Either direction still needs the other party's say-so before it
// counts as membership — a self-request needs the owner's approval
// (Approve), an owner's invite needs the invited rider's own confirmation
// (Confirm), unless that rider already had a pending request in, in which
// case the consent side is already satisfied and the owner's AddMember call
// is itself the approval. Push
// access is exactly what membership gates: a route may be shared to a crew
// the route's owner belongs to, which resolves — at push time, not at share
// time — to every currently approved member's accounts, so an invite that
// stayed pending forever never reaches anyone's device. Membership is
// deliberately not baked into a route when it is shared: a member leaving
// or being removed takes effect on the next push with nobody touching the
// route.
//
// "Crew" rather than "group" on purpose. This codebase already uses "group"
// for Authelia/Auth0 role-mapping groups (see internal/auth), an unrelated
// concept — reusing the word here would read as the same thing in code and
// docs when it is not.
package crew

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// ErrNotFound is returned for a crew id nothing matches.
var ErrNotFound = errors.New("no such crew")

// ErrAlreadyMember is returned when a rider requests to join a crew they
// already belong to, or already have a pending request for.
var ErrAlreadyMember = errors.New("already a member or already requested")

// ErrConfirmationRequired is returned by Approve when the pending row it was
// asked to grant is an invite (OriginInvite) rather than a self-request
// (OriginSelf) — the owner cannot complete the other party's half of the
// consent themselves. Only Confirm, called by the invited rider, may grant
// that row.
var ErrConfirmationRequired = errors.New("crew: this is an invite — only the invited rider can confirm it")

// ErrNoInvite is returned by Confirm when the calling rider has no pending
// invite to confirm — either nothing is there, it is already approved, or
// it is a self-request (OriginSelf), which only the owner may grant.
var ErrNoInvite = errors.New("crew: no pending invite for that rider")

// ErrLastOwner is returned by SetOwner when asked to revoke the crew's last
// remaining owner grant. Every crew keeps at least one owner able to manage
// it directly — short of an admin's own override (see the API package's
// canManageCrew) — so the caller must promote someone else first.
var ErrLastOwner = errors.New("crew: cannot remove the last owner")

// MemberStatus is where a rider stands with a crew.
type MemberStatus string

const (
	StatusPending  MemberStatus = "pending"
	StatusApproved MemberStatus = "approved"
)

// MemberOrigin is which side of the relationship started a pending row —
// it decides who may grant it: a self-request needs the owner's approval, an
// invite needs the invited rider's own confirmation. It has no meaning once
// a row is approved; both directions end up identical members.
type MemberOrigin string

const (
	// OriginSelf is a rider's own request to join (RequestJoin) — the owner
	// grants it (Approve).
	OriginSelf MemberOrigin = "self"
	// OriginInvite is the owner starting the relationship from their end
	// (AddMember, first time) — the invited rider grants it (Confirm).
	OriginInvite MemberOrigin = "invite"
)

// Crew is a set of riders who trust each other with their routes.
type Crew struct {
	ID   string
	Name string
	// Owner is who originally created this crew — informational only.
	// Ownership itself (who may manage the crew) now lives per-row on
	// crew_members.is_owner (see Member.IsOwner), so a crew survives one
	// owner's departure as long as another owner grant remains. Kept rather
	// than dropped: repurposing an existing column is a smaller, reversible
	// change than a schema migration nothing else here needs.
	Owner string
	// AutoShare, when true, makes this crew a default target: a member who
	// uploads a route with no explicit sharing choice of their own gets it
	// shared here automatically, instead of reaching only their own
	// accounts. It never touches a route that already exists, and it never
	// overrides an explicit choice — only fills in when the rider made none.
	AutoShare bool
	CreatedAt string
	UpdatedAt string
}

// Member is one rider's standing with one crew.
type Member struct {
	CrewID      string
	Rider       string
	Status      MemberStatus
	Origin      MemberOrigin
	RequestedAt string
	DecidedBy   string
	DecidedAt   string
	// CanSchedule is whether this rider may schedule a crew ride (see
	// package schedule) beyond the owner/admin, who always may regardless
	// of this flag. Owner-granted, per member, per crew — a rider trusted
	// to plan rides in one crew is not automatically trusted in another.
	// Meaningless for a pending row; only ever set by the owner/admin on an
	// approved member.
	CanSchedule bool
	// IsOwner is whether this rider holds an owner grant on the crew — may
	// manage it (delete, auto-share, add/remove members, promote/demote
	// other owners) the same way the single crews.owner column used to mean
	// exclusively. Meaningless for a pending row; only ever set on an
	// approved member, by SetOwner or by Create for the crew's creator.
	IsOwner bool
}

// MemberSet is which riders currently, approvedly, belong to which crews —
// keyed by crew id. It exists as its own type (rather than a bare map) so
// the "is this rider a current member" check has exactly one definition,
// used both when resolving a route's push targets and when validating a
// route's targets at write time.
type MemberSet map[string][]string

// Has reports whether rider is a current, approved member of crewID.
// Case-insensitive, matching auth.Identity.CanEditRoute's own EqualFold —
// rider identity is compared the same way everywhere in this app, not
// exact-match here and case-insensitive there. Found live: a route whose
// owner and crew membership were both genuinely the same rider, entered
// with different casing on two different paths (a typed --owner flag vs.
// the OIDC token's own lowercased claim), silently resolved to "not a
// member" — which is not just a wrong tooltip, config.TargetsFor calls
// this too, so it silently drops the route from that rider's own push
// targets.
func (m MemberSet) Has(crewID, rider string) bool {
	for _, r := range m[crewID] {
		if strings.EqualFold(r, rider) {
			return true
		}
	}
	return false
}

// normalizeRider is the one place a rider identifier gets its canonical
// form before being stored — lowercased and trimmed, the same
// transformation identityFromToken already applies to every OIDC claim
// before it ever becomes an Identity.User. Every write here goes through
// it, not just the ones fed by a signed-in session (RequestJoin, the
// self-service path): AddMember and Create both accept a rider identifier
// typed by someone else (the crew owner, an operator), which is exactly
// the path that will not already be normalized on its own.
func normalizeRider(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Snapshot is every crew and its current approved membership, fetched
// together because everywhere either is needed, both are — resolving a
// route's targets needs to know both which crews exist and who is in them.
type Snapshot struct {
	Crews          []Crew
	ApprovedRiders MemberSet
}

// Store holds crews and their membership.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

// schema takes the dialect because auto_share needs a real per-engine
// boolean type, not a hardcoded one — SQLite is loosely typed enough that
// "INTEGER ... DEFAULT FALSE" works by accident, but Postgres rejects FALSE
// as a default for an INTEGER column outright (no implicit bool→int cast).
// routesSchema in internal/source already draws on d.Boolean for exactly
// this reason; this mirrors it.
func schema(d dbx.Dialect) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS crews (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    owner      TEXT NOT NULL,
    auto_share %s NOT NULL DEFAULT FALSE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS crew_members (
    crew_id      TEXT NOT NULL,
    rider        TEXT NOT NULL,
    status       TEXT NOT NULL,
    origin       TEXT NOT NULL DEFAULT 'self',
    requested_at TEXT NOT NULL,
    decided_by   TEXT NOT NULL DEFAULT '',
    decided_at   TEXT NOT NULL DEFAULT '',
    can_schedule %s NOT NULL DEFAULT FALSE,
    is_owner     %s NOT NULL DEFAULT FALSE,
    PRIMARY KEY (crew_id, rider)
);`, d.Boolean, d.Boolean, d.Boolean)
}

// UseDB puts the crew tables in an already-open database — the same one
// holding the routes, accounts, and sync state, so a deployment needs
// exactly one.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db, dialect: d}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate crew tables: %w", err)
	}
	if err := store.addAutoShareColumn(); err != nil {
		return nil, fmt.Errorf("migrate crew tables: %w", err)
	}
	if err := store.addOriginColumn(); err != nil {
		return nil, fmt.Errorf("migrate crew tables: %w", err)
	}
	if err := store.addCanScheduleColumn(); err != nil {
		return nil, fmt.Errorf("migrate crew tables: %w", err)
	}
	if err := store.addIsOwnerColumn(); err != nil {
		return nil, fmt.Errorf("migrate crew tables: %w", err)
	}
	if err := store.normalizeExistingOwners(context.Background()); err != nil {
		return nil, fmt.Errorf("normalize crew owners: %w", err)
	}
	if err := store.backfillOwnerFlag(context.Background()); err != nil {
		return nil, fmt.Errorf("backfill crew owner flag: %w", err)
	}
	return store, nil
}

// normalizeExistingOwners lowercases and trims crews.owner on every row not
// already in that form — Create does this for anything written from here
// on, but a crew created before that keeps whatever casing its owner was
// given until this runs. Safe unconditionally: owner carries no uniqueness
// constraint of its own (id is the table's key), so this can never collide.
// crew_members.rider is deliberately not touched the same way — it is part
// of a composite primary key, and two existing rows for the same real rider
// under different casing (a genuine possibility this bug could have caused)
// would collide on normalization instead of merging. MemberSet.Has being
// case-insensitive is what covers that table instead, with no data rewrite
// needed.
func (s *Store) normalizeExistingOwners(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE crews SET owner = LOWER(TRIM(owner))
        WHERE owner <> LOWER(TRIM(owner))`))
	return err
}

// addAutoShareColumn adds auto_share to a crews table that predates the
// column — CREATE TABLE IF NOT EXISTS above is a no-op against a table that
// already exists, so a genuinely new column needs its own step. This runs
// every startup; once the column is there, the ALTER fails with a
// database-specific "it's already there" error (SQLite says "duplicate
// column name", Postgres says "already exists") that is the expected
// steady state, not a real failure — anything else still surfaces.
func (s *Store) addAutoShareColumn() error {
	_, err := s.db.Exec(fmt.Sprintf(
		`ALTER TABLE crews ADD COLUMN auto_share %s NOT NULL DEFAULT FALSE`, s.dialect.Boolean))
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

// addOriginColumn adds origin to a crew_members table that predates the
// column, the same way addAutoShareColumn does for crews.auto_share.
// Defaulting to 'self' is the correct read of every pre-existing row: before
// this migration AddMember always inserted approved directly, so any
// pending row already in the table can only have come from RequestJoin.
func (s *Store) addOriginColumn() error {
	_, err := s.db.Exec(`ALTER TABLE crew_members ADD COLUMN origin TEXT NOT NULL DEFAULT 'self'`)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

// addCanScheduleColumn adds can_schedule to a crew_members table that
// predates the column, the same way addOriginColumn does for origin.
func (s *Store) addCanScheduleColumn() error {
	_, err := s.db.Exec(fmt.Sprintf(
		`ALTER TABLE crew_members ADD COLUMN can_schedule %s NOT NULL DEFAULT FALSE`, s.dialect.Boolean))
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

// addIsOwnerColumn adds is_owner to a crew_members table that predates the
// column, the same way addCanScheduleColumn does for can_schedule.
func (s *Store) addIsOwnerColumn() error {
	_, err := s.db.Exec(fmt.Sprintf(
		`ALTER TABLE crew_members ADD COLUMN is_owner %s NOT NULL DEFAULT FALSE`, s.dialect.Boolean))
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

// backfillOwnerFlag is the one-time migration from the old singular-owner
// column to per-row ownership: for every crew, it sets is_owner on whichever
// crew_members row matches crews.owner. A plain per-crew loop, not a
// cross-dialect UPDATE...JOIN — this runs once per startup against however
// many crews exist, small enough that a straightforward loop is the right
// trade, matching normalizeExistingOwners' own preference for simplicity
// over cleverness in a one-time migration. Safe to run every startup: a row
// already flagged an owner is simply set to TRUE again.
func (s *Store) backfillOwnerFlag(ctx context.Context) error {
	crews, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range crews {
		if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
            UPDATE crew_members SET is_owner = ? WHERE crew_id = ? AND rider = ?`),
			true, c.ID, c.Owner); err != nil {
			return fmt.Errorf("backfill owner flag for %s: %w", c.ID, err)
		}
	}
	return nil
}

// idPrefix marks a target as a crew rather than the raw account ids
// Targets held before this package existed. It is what lets a route's
// Targets list hold crew ids in the same field/namespace a legacy account
// id occupies, and lets a resolver tell the two apart with a string check
// rather than a lookup — a stale or foreign account id never starts with
// this prefix, so it can never accidentally resolve as a crew.
const idPrefix = "crew:"

// nonSlug matches everything a crew id may not contain. Duplicated from
// source.Slugify's regex rather than imported: source pulls in the whole
// route/GPX/tracing stack for one regexp, which this small package has no
// other reason to depend on.
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Create makes a new crew and enrolls its owner as an approved member —
// there is no separate "or you own it" case anywhere membership is
// checked, because the owner already appears in ApprovedRiders like anyone
// else.
func (s *Store) Create(ctx context.Context, name, owner string) (Crew, error) {
	name = strings.TrimSpace(name)
	owner = normalizeRider(owner)
	if name == "" {
		return Crew{}, errors.New("crew: name is required")
	}
	if owner == "" {
		return Crew{}, errors.New("crew: no owner — who is creating this?")
	}

	id, err := s.uniqueID(ctx, slugify(name))
	if err != nil {
		return Crew{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO crews (id, name, owner, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)`),
		id, name, owner, now, now); err != nil {
		return Crew{}, fmt.Errorf("create crew: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO crew_members (crew_id, rider, status, origin, requested_at, decided_by, decided_at, is_owner)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		id, owner, string(StatusApproved), string(OriginSelf), now, owner, now, true); err != nil {
		return Crew{}, fmt.Errorf("enroll crew owner: %w", err)
	}

	return s.Get(ctx, id)
}

// uniqueID appends -2, -3, … so two crews with the same name don't collide.
func (s *Store) uniqueID(ctx context.Context, base string) (string, error) {
	if base == "" {
		base = "crew"
	}
	candidate := idPrefix + base
	for attempt := 2; attempt < 1000; attempt++ {
		var exists int
		err := s.db.QueryRowContext(ctx,
			s.dialect.Rebind(`SELECT COUNT(1) FROM crews WHERE id = ?`), candidate).Scan(&exists)
		if err != nil {
			return "", err
		}
		if exists == 0 {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s%s-%d", idPrefix, base, attempt)
	}
	return "", fmt.Errorf("could not find a free id for %q", base)
}

// Get returns one crew.
func (s *Store) Get(ctx context.Context, id string) (Crew, error) {
	var c Crew
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT id, name, owner, auto_share, created_at, updated_at FROM crews WHERE id = ?`), id).
		Scan(&c.ID, &c.Name, &c.Owner, &c.AutoShare, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Crew{}, ErrNotFound
	}
	return c, err
}

// List returns every crew, in a stable order.
func (s *Store) List(ctx context.Context) ([]Crew, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, name, owner, auto_share, created_at, updated_at FROM crews ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read crews: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Crew
	for rows.Next() {
		var c Crew
		if err := rows.Scan(&c.ID, &c.Name, &c.Owner, &c.AutoShare, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("read crews: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetAutoShare flips whether this crew is a default target for its
// members' future uploads. Existing routes are never touched by this call —
// it only changes what an upload with no explicit target choice defaults
// to, from the next upload onward.
func (s *Store) SetAutoShare(ctx context.Context, id string, autoShare bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE crews SET auto_share = ?, updated_at = ? WHERE id = ?`),
		autoShare, now, id)
	if err != nil {
		return fmt.Errorf("set crew auto-share: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// AutoShareCrewsFor returns the ids of every crew rider currently,
// approvedly belongs to that has auto-share on — what a new upload with no
// explicit target choice of its own should default to, in place of nil's
// usual owner-only default.
func (s Snapshot) AutoShareCrewsFor(rider string) []string {
	var out []string
	for _, c := range s.Crews {
		if c.AutoShare && s.ApprovedRiders.Has(c.ID, rider) {
			out = append(out, c.ID)
		}
	}
	return out
}

// Snapshot returns every crew and its current approved membership in two
// queries, not one per crew — everywhere a caller needs one, it needs the
// other too (resolving a route's targets, rendering the crews page), so
// this is what both use.
func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	crews, err := s.List(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT crew_id, rider FROM crew_members WHERE status = ?`), string(StatusApproved))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read crew membership: %w", err)
	}
	defer func() { _ = rows.Close() }()

	approved := MemberSet{}
	for rows.Next() {
		var crewID, rider string
		if err := rows.Scan(&crewID, &rider); err != nil {
			return Snapshot{}, fmt.Errorf("read crew membership: %w", err)
		}
		approved[crewID] = append(approved[crewID], rider)
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, err
	}

	return Snapshot{Crews: crews, ApprovedRiders: approved}, nil
}

// Members returns every rider's standing with a crew — pending and
// approved together, which is what the owner's own view needs.
func (s *Store) Members(ctx context.Context, crewID string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT crew_id, rider, status, origin, requested_at, decided_by, decided_at, can_schedule, is_owner
        FROM crew_members WHERE crew_id = ? ORDER BY requested_at`), crewID)
	if err != nil {
		return nil, fmt.Errorf("read crew members: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Member
	for rows.Next() {
		var m Member
		var status, origin string
		if err := rows.Scan(&m.CrewID, &m.Rider, &status, &origin, &m.RequestedAt, &m.DecidedBy, &m.DecidedAt, &m.CanSchedule, &m.IsOwner); err != nil {
			return nil, fmt.Errorf("read crew members: %w", err)
		}
		m.Status = MemberStatus(status)
		m.Origin = MemberOrigin(origin)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetCanSchedule grants or revokes an approved member's permission to
// schedule a crew ride — the owner/admin may always schedule regardless of
// this flag (see package schedule); this is only ever checked for someone
// else. A no-op silently succeeds rather than erroring on a pending or
// unknown row: the caller (crews.go) already resolved the crew and rider
// from the roster it is currently showing, so a mismatch here would only
// mean a race with something else editing the same row, not a caller
// mistake worth surfacing differently from any other write.
func (s *Store) SetCanSchedule(ctx context.Context, crewID, rider string, can bool) error {
	rider = normalizeRider(rider)
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE crew_members SET can_schedule = ? WHERE crew_id = ? AND rider = ? AND status = ?`),
		can, crewID, rider, string(StatusApproved))
	if err != nil {
		return fmt.Errorf("set crew member schedule permission: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// IsOwner reports whether rider currently holds an owner grant on crewID —
// approved membership required; a pending invite carries no authority yet.
func (s *Store) IsOwner(ctx context.Context, crewID, rider string) (bool, error) {
	rider = normalizeRider(rider)
	var isOwner bool
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT is_owner FROM crew_members WHERE crew_id = ? AND rider = ? AND status = ?`),
		crewID, rider, string(StatusApproved)).Scan(&isOwner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check crew ownership: %w", err)
	default:
		return isOwner, nil
	}
}

// ownerCount reports how many approved members currently hold an owner
// grant on crewID — what SetOwner checks before revoking one, so the crew
// never ends up with none left through this call (it can still end up with
// none if its sole owner is deleted outright — see the API package's
// purgeRiderData / RemoveRiderEverywhere, an accepted outcome handled by an
// admin's own override rather than by refusing the deletion).
func (s *Store) ownerCount(ctx context.Context, crewID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT COUNT(1) FROM crew_members WHERE crew_id = ? AND status = ? AND is_owner = ?`),
		crewID, string(StatusApproved), true).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count crew owners: %w", err)
	}
	return count, nil
}

// SetOwner grants or revokes rider's owner grant on crewID — approved
// membership required, the same as IsOwner. Revoking the crew's last
// remaining owner is refused (ErrLastOwner): every crew keeps at least one
// owner able to manage it directly, short of an admin's own override.
func (s *Store) SetOwner(ctx context.Context, crewID, rider string, isOwner bool) error {
	rider = normalizeRider(rider)

	if !isOwner {
		currentlyOwner, err := s.IsOwner(ctx, crewID, rider)
		if err != nil {
			return err
		}
		if currentlyOwner {
			count, err := s.ownerCount(ctx, crewID)
			if err != nil {
				return err
			}
			if count <= 1 {
				return ErrLastOwner
			}
		}
	}

	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE crew_members SET is_owner = ? WHERE crew_id = ? AND rider = ? AND status = ?`),
		isOwner, crewID, rider, string(StatusApproved))
	if err != nil {
		return fmt.Errorf("set crew ownership: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveRiderEverywhere takes rider out of every crew's membership, pending
// or approved, in one statement — only for when the rider themselves is
// deleted (see the API package's purgeRiderData), never a substitute for
// Deny/Remove's per-crew semantics. Removing this row also removes any
// owner grant that lived on it (is_owner is a column on this same row) — a
// crew left with zero owners is an accepted outcome, handled by
// canManageCrew's admin override rather than refused here.
func (s *Store) RemoveRiderEverywhere(ctx context.Context, rider string) (int, error) {
	rider = normalizeRider(rider)
	if rider == "" {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM crew_members WHERE rider = ?`), rider)
	if err != nil {
		return 0, fmt.Errorf("remove rider from all crews: %w", err)
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// HasRider reports whether rider holds any row in crew_members, pending or
// approved, across every crew — not just approved ones like Snapshot's own
// ApprovedRiders, since a rider mid-request is still a real rider nobody
// else may become. Used to check a normalized identity is not already
// somebody's before a rename is allowed to claim it.
func (s *Store) HasRider(ctx context.Context, rider string) (bool, error) {
	rider = normalizeRider(rider)
	if rider == "" {
		return false, nil
	}
	var exists int
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT 1 FROM crew_members WHERE rider = ? LIMIT 1`), rider).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check crew membership: %w", err)
	default:
		return true, nil
	}
}

// RequestJoin records a rider's request to join a crew. It is a no-op
// error, not silent, if the rider already has a pending or approved row —
// callers should not double-request over an existing one.
func (s *Store) RequestJoin(ctx context.Context, crewID, rider string) (Member, error) {
	rider = normalizeRider(rider)
	if rider == "" {
		return Member{}, errors.New("crew: no rider — who is requesting to join?")
	}
	if _, err := s.Get(ctx, crewID); err != nil {
		return Member{}, err
	}

	var exists int
	if err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT COUNT(1) FROM crew_members WHERE crew_id = ? AND rider = ?`),
		crewID, rider).Scan(&exists); err != nil {
		return Member{}, err
	}
	if exists > 0 {
		return Member{}, ErrAlreadyMember
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO crew_members (crew_id, rider, status, origin, requested_at, decided_by, decided_at)
        VALUES (?, ?, ?, ?, ?, '', '')`),
		crewID, rider, string(StatusPending), string(OriginSelf), now); err != nil {
		return Member{}, fmt.Errorf("request to join crew: %w", err)
	}

	return Member{CrewID: crewID, Rider: rider, Status: StatusPending, Origin: OriginSelf, RequestedAt: now}, nil
}

// AddMember is the owner's other way to start a membership, from their own
// end instead of waiting for the other rider to find the crew and ask. It
// does not, on its own, grant anything: a fresh invite lands pending, the
// same as RequestJoin's, until the invited rider confirms it themselves via
// Confirm — see the package doc comment for why an owner's say-so is not
// enough by itself. A rider who already has a pending self-request (they
// asked first) is approved rather than left pending a second time — real
// consent already exists there, the owner is just completing the round trip
// the other way, and there is no reason to make them deny-then-add instead.
// Calling this again while an invite is still unconfirmed is a harmless
// no-op — it must not silently grant what only Confirm may.
func (s *Store) AddMember(ctx context.Context, crewID, rider, addedBy string) (Member, error) {
	rider = normalizeRider(rider)
	if rider == "" {
		return Member{}, errors.New("crew: no rider — who is being added?")
	}
	if _, err := s.Get(ctx, crewID); err != nil {
		return Member{}, err
	}

	var status, origin string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT status, origin FROM crew_members WHERE crew_id = ? AND rider = ?`),
		crewID, rider).Scan(&status, &origin)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
            INSERT INTO crew_members (crew_id, rider, status, origin, requested_at, decided_by, decided_at)
            VALUES (?, ?, ?, ?, ?, '', '')`),
			crewID, rider, string(StatusPending), string(OriginInvite), now); err != nil {
			return Member{}, fmt.Errorf("invite crew member: %w", err)
		}
		return Member{CrewID: crewID, Rider: rider, Status: StatusPending, Origin: OriginInvite, RequestedAt: now}, nil
	case err != nil:
		return Member{}, err
	case status == string(StatusApproved):
		return Member{}, ErrAlreadyMember
	case origin == string(OriginInvite):
		// Already invited, still unconfirmed — re-inviting changes nothing;
		// only the invited rider's own Confirm may grant this row.
		return Member{CrewID: crewID, Rider: rider, Status: StatusPending, Origin: OriginInvite}, nil
	default:
		if err := s.Approve(ctx, crewID, rider, addedBy); err != nil {
			return Member{}, err
		}
		return Member{CrewID: crewID, Rider: rider, Status: StatusApproved, DecidedBy: addedBy}, nil
	}
}

// Approve grants a pending self-request (OriginSelf), recording who decided
// it and when — the owner's or an admin's call. It refuses an invite
// (OriginInvite): that half of the consent belongs to the invited rider
// alone, via Confirm, not to whoever is calling this.
func (s *Store) Approve(ctx context.Context, crewID, rider, decidedBy string) error {
	rider = normalizeRider(rider)

	var origin string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT origin FROM crew_members WHERE crew_id = ? AND rider = ? AND status = ?`),
		crewID, rider, string(StatusPending)).Scan(&origin)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("approve crew member: %w", err)
	case origin == string(OriginInvite):
		return ErrConfirmationRequired
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE crew_members SET status = ?, decided_by = ?, decided_at = ?
        WHERE crew_id = ? AND rider = ?`),
		string(StatusApproved), decidedBy, now, crewID, rider)
	if err != nil {
		return fmt.Errorf("approve crew member: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Confirm grants an invited rider's own pending invite (OriginInvite) — the
// invited rider's half of the consent an owner's AddMember alone cannot
// supply. It refuses a self-request (OriginSelf): that row is already the
// rider's own consent, waiting on the owner's Approve, not a second
// confirmation from the same person who filed it.
func (s *Store) Confirm(ctx context.Context, crewID, rider string) error {
	rider = normalizeRider(rider)

	var origin string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT origin FROM crew_members WHERE crew_id = ? AND rider = ? AND status = ?`),
		crewID, rider, string(StatusPending)).Scan(&origin)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNoInvite
	case err != nil:
		return fmt.Errorf("confirm crew invite: %w", err)
	case origin != string(OriginInvite):
		return ErrNoInvite
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE crew_members SET status = ?, decided_by = ?, decided_at = ?
        WHERE crew_id = ? AND rider = ?`),
		string(StatusApproved), rider, now, crewID, rider)
	if err != nil {
		return fmt.Errorf("confirm crew invite: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNoInvite
	}
	return nil
}

// Deny removes a pending request. The rider may request again later —
// denying is not a ban, it is declining this particular request.
func (s *Store) Deny(ctx context.Context, crewID, rider string) error {
	return s.delete(ctx, crewID, rider, string(StatusPending))
}

// Remove takes an approved member out of a crew — an owner removing
// someone, or a member leaving on their own, are the same operation from
// the store's point of view; the API layer decides who may call it for
// whom.
func (s *Store) Remove(ctx context.Context, crewID, rider string) error {
	return s.delete(ctx, crewID, rider, string(StatusApproved))
}

func (s *Store) delete(ctx context.Context, crewID, rider, status string) error {
	rider = normalizeRider(rider)
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        DELETE FROM crew_members WHERE crew_id = ? AND rider = ? AND status = ?`),
		crewID, rider, status)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a crew and its membership. A route that named this crew
// in its targets simply stops resolving anywhere beyond the route owner's
// own accounts on the next push — nothing else to clean up.
func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM crews WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM crew_members WHERE crew_id = ?`), id); err != nil {
		return fmt.Errorf("delete crew membership: %w", err)
	}
	return nil
}
