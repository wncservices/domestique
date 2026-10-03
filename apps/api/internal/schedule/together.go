package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Ride together. A rider says which weekdays they are open to a shared ride with
// a crew (crew_ride_together), and the server, reading each opted-in rider's own
// plan on behalf of the group, proposes one shared day and route for a week
// (ride_together_proposals, with one row per rider in ride_together_members).
//
// What is stored here crosses between riders by design, and nothing else does:
// the days flag, and a proposal's week, day, route, riders and each one's
// status. A proposal carries no session, no duration and nothing about a rider's
// plan; the one session id it holds (Member.WorkoutID) is the rider's own, kept
// so their acceptance can be re-checked, and is never sent to a peer.
//
// The store only holds state. Whether a rider may write a row, and whether a
// proposal still holds, is internal/api's and internal/crewplan's job.

// ProposalStatus is where a proposal stands.
type ProposalStatus string

// A proposal is open until every rider has accepted (agreed), any rider has
// declined or left (ended), or its premise stopped holding (stale).
const (
	ProposalOpen   ProposalStatus = "open"
	ProposalAgreed ProposalStatus = "agreed"
	ProposalEnded  ProposalStatus = "ended"
	ProposalStale  ProposalStatus = "stale"
)

// MemberStatus is one rider's answer.
type MemberStatus string

const (
	MemberPending  MemberStatus = "pending"
	MemberAccepted MemberStatus = "accepted"
	MemberDeclined MemberStatus = "declined"
)

// Member is one rider in a proposal. WorkoutID is the rider's own session that
// they moved when they accepted, "" until then.
type Member struct {
	Rider     string
	Status    MemberStatus
	WorkoutID string
}

// Proposal is one shared ride suggested for a crew in a week.
type Proposal struct {
	ID        string
	CrewID    string
	WeekStart string // the Monday, "YYYY-MM-DD"
	Day       string // "YYYY-MM-DD"
	RouteSlug string
	Status    ProposalStatus
	CreatedAt string
	// Members are the riders in the proposal, sorted by name. On CreateProposal
	// the caller names them in Riders and they start pending.
	Members []Member
	Riders  []string
}

const togetherSchema = `
CREATE TABLE IF NOT EXISTS crew_ride_together (
    crew_id    TEXT NOT NULL,
    rider      TEXT NOT NULL,
    days       TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (crew_id, rider)
);
CREATE TABLE IF NOT EXISTS ride_together_proposals (
    id         TEXT PRIMARY KEY,
    crew_id    TEXT NOT NULL,
    week_start TEXT NOT NULL,
    day        TEXT NOT NULL,
    route_slug TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open',
    created_at TEXT NOT NULL,
    UNIQUE (crew_id, week_start)
);
CREATE TABLE IF NOT EXISTS ride_together_members (
    proposal_id TEXT NOT NULL,
    rider       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    workout_id  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (proposal_id, rider)
)`

// SetTogether records the weekdays rider is open to riding with the crew, as
// lowercase three-letter names. An empty list deletes the row: no row means
// opted out. It writes only the one rider's own row.
func (s *Store) SetTogether(ctx context.Context, crewID, rider string, days []string) error {
	rider = normalizeRider(rider)
	if crewID == "" || rider == "" {
		return errors.New("schedule: together needs a crew and a rider")
	}
	seen := map[string]bool{}
	var clean []string
	for _, d := range days {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !seen[d] {
			seen[d] = true
			clean = append(clean, d)
		}
	}
	sort.Slice(clean, func(i, j int) bool { return weekdayIndex(clean[i]) < weekdayIndex(clean[j]) })
	if len(clean) == 0 {
		_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
			`DELETE FROM crew_ride_together WHERE crew_id = ? AND rider = ?`), crewID, rider)
		return err
	}
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO crew_ride_together (crew_id, rider, days, updated_at) VALUES (?, ?, ?, ?)
        ON CONFLICT (crew_id, rider) DO UPDATE SET days = excluded.days, updated_at = excluded.updated_at`),
		crewID, rider, strings.Join(clean, ","), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("set together days: %w", err)
	}
	return nil
}

var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func weekdayIndex(d string) int {
	for i, w := range weekdays {
		if w == d {
			return i
		}
	}
	return len(weekdays)
}

// TogetherFor is every rider's open days in one crew, by rider.
func (s *Store) TogetherFor(ctx context.Context, crewID string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT rider, days FROM crew_ride_together WHERE crew_id = ?`), crewID)
	if err != nil {
		return nil, fmt.Errorf("read together days: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var rider, days string
		if err := rows.Scan(&rider, &days); err != nil {
			return nil, err
		}
		if days != "" {
			out[rider] = strings.Split(days, ",")
		}
	}
	return out, rows.Err()
}

// CreateProposal stores p once per crew and week, so every rider reads the same
// one. A proposal already there is returned unchanged (created false), except a
// stale one, which is replaced: its premise stopped holding and the week is
// worth proposing afresh. An ended or agreed one is final for its week.
func (s *Store) CreateProposal(ctx context.Context, p Proposal) (Proposal, bool, error) {
	if p.CrewID == "" || p.WeekStart == "" || p.Day == "" || p.RouteSlug == "" || len(p.Riders) < 2 {
		return Proposal{}, false, errors.New("schedule: a proposal needs a crew, week, day, route and two riders")
	}
	if existing, ok, err := s.ProposalFor(ctx, p.CrewID, p.WeekStart); err != nil {
		return Proposal{}, false, err
	} else if ok && existing.Status != ProposalStale {
		return existing, false, nil
	} else if ok {
		if err := s.deleteProposal(ctx, existing.ID); err != nil {
			return Proposal{}, false, err
		}
	}

	id, err := newID()
	if err != nil {
		return Proposal{}, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, false, fmt.Errorf("create proposal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO ride_together_proposals (id, crew_id, week_start, day, route_slug, status, created_at)
        VALUES (?, ?, ?, ?, ?, 'open', ?)`), id, p.CrewID, p.WeekStart, p.Day, p.RouteSlug, now); err != nil {
		_ = tx.Rollback()
		// Raced another reader computing the same week: theirs stands.
		if existing, ok, ferr := s.ProposalFor(ctx, p.CrewID, p.WeekStart); ferr == nil && ok {
			return existing, false, nil
		}
		return Proposal{}, false, fmt.Errorf("create proposal: %w", err)
	}
	riders := make([]string, 0, len(p.Riders))
	for _, r := range p.Riders {
		riders = append(riders, normalizeRider(r))
	}
	sort.Strings(riders)
	for _, r := range riders {
		if _, err := tx.ExecContext(ctx, s.dialect.Rebind(
			`INSERT INTO ride_together_members (proposal_id, rider, status) VALUES (?, ?, 'pending')`), id, r); err != nil {
			return Proposal{}, false, fmt.Errorf("create proposal: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, false, fmt.Errorf("create proposal: %w", err)
	}
	got, err := s.GetProposal(ctx, id)
	return got, true, err
}

// ProposalFor is the proposal stored for a crew and week.
func (s *Store) ProposalFor(ctx context.Context, crewID, weekStart string) (Proposal, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT id FROM ride_together_proposals WHERE crew_id = ? AND week_start = ?`), crewID, weekStart).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, false, nil
	}
	if err != nil {
		return Proposal{}, false, err
	}
	p, err := s.GetProposal(ctx, id)
	return p, err == nil, err
}

// GetProposal reads one proposal with its members.
func (s *Store) GetProposal(ctx context.Context, id string) (Proposal, error) {
	var p Proposal
	var status string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT id, crew_id, week_start, day, route_slug, status, created_at
        FROM ride_together_proposals WHERE id = ?`), id).
		Scan(&p.ID, &p.CrewID, &p.WeekStart, &p.Day, &p.RouteSlug, &status, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	p.Status = ProposalStatus(status)
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT rider, status, workout_id FROM ride_together_members WHERE proposal_id = ? ORDER BY rider`), id)
	if err != nil {
		return Proposal{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var m Member
		var ms string
		if err := rows.Scan(&m.Rider, &ms, &m.WorkoutID); err != nil {
			return Proposal{}, err
		}
		m.Status = MemberStatus(ms)
		p.Members = append(p.Members, m)
	}
	return p, rows.Err()
}

// ProposalsForRider is the proposals rider is a member of, for the given weeks.
func (s *Store) ProposalsForRider(ctx context.Context, rider string, weekStarts []string) ([]Proposal, error) {
	rider = normalizeRider(rider)
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT p.id, p.week_start FROM ride_together_proposals p
        JOIN ride_together_members m ON m.proposal_id = p.id
        WHERE m.rider = ? ORDER BY p.week_start, p.crew_id`), rider)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id, week string
		if err := rows.Scan(&id, &week); err != nil {
			_ = rows.Close()
			return nil, err
		}
		for _, w := range weekStarts {
			if w == week {
				ids = append(ids, id)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	out := make([]Proposal, 0, len(ids))
	for _, id := range ids {
		p, err := s.GetProposal(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// SetProposalStatus moves a proposal to a new status.
func (s *Store) SetProposalStatus(ctx context.Context, id string, status ProposalStatus) error {
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE ride_together_proposals SET status = ? WHERE id = ?`), string(status), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMemberStatus records one rider's answer, and the session they moved when
// they accepted. It writes the one member row it is given, which the API only
// ever names from the session.
func (s *Store) SetMemberStatus(ctx context.Context, proposalID, rider string, status MemberStatus, workoutID string) error {
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE ride_together_members SET status = ?, workout_id = ? WHERE proposal_id = ? AND rider = ?`),
		string(status), workoutID, proposalID, normalizeRider(rider))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) deleteProposal(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM ride_together_members WHERE proposal_id = ?`), id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM ride_together_proposals WHERE id = ?`), id)
	return err
}

// leaveProposals ends every proposal rider is a member of (in one crew, or in
// all when crewID is empty) and takes the rider off them. An ended proposal
// stays for its week, so the same week is not proposed again behind the back of
// somebody who just left; the other riders' rows are untouched.
func (s *Store) leaveProposals(ctx context.Context, crewID, rider string) error {
	q := `SELECT p.id FROM ride_together_proposals p
          JOIN ride_together_members m ON m.proposal_id = p.id WHERE m.rider = ?`
	args := []any{rider}
	if crewID != "" {
		q += ` AND p.crew_id = ?`
		args = append(args, crewID)
	}
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(q), args...)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
			`DELETE FROM ride_together_members WHERE proposal_id = ? AND rider = ?`), id, rider); err != nil {
			return err
		}
		if err := s.SetProposalStatus(ctx, id, ProposalEnded); err != nil {
			return err
		}
	}
	return nil
}
