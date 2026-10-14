package identities

import (
	"context"
	"fmt"
	"strings"
)

// Why the startup bootstrap left an account unbound. The strings are stored,
// and the People page keys off them, so they are a contract.
const (
	// FlagNewerThanData: the only account claiming the rider joined the tenant
	// after the rider's data existed, which looks like someone who renamed
	// themselves rather than the person the data belongs to.
	FlagNewerThanData = "newer-than-data"
	// FlagConflict: the accounts claiming the rider disagree on who they are.
	FlagConflict = "conflict"
	// FlagNeedsAttention: the rider has data, and an account claiming it has an
	// unverified or unknown email.
	FlagNeedsAttention = "needs-attention"
	// FlagAdminElsewhere: an admin's account newer than the rider's data, which
	// the bootstrap would have bound, was already bound to another rider (it
	// signed in before the bootstrap could). A bootstrap never moves a binding,
	// so this says why the owner is not on their own rider yet.
	FlagAdminElsewhere = "admin-bound-elsewhere"
)

// Flag is one account the bootstrap held back, and the rider it was held back
// from. Rider is the rider the account would have been bound to, not one it
// is bound to now.
type Flag struct {
	Sub    string
	Rider  string
	Reason string
}

func flagSchemas() []string {
	return []string{`
CREATE TABLE IF NOT EXISTS bootstrap_flags (
    issuer     TEXT NOT NULL,
    sub        TEXT NOT NULL,
    rider      TEXT NOT NULL,
    reason     TEXT NOT NULL,
    flagged_at TEXT NOT NULL,
    PRIMARY KEY (issuer, sub)
)`,
		`CREATE INDEX IF NOT EXISTS bootstrap_flags_rider ON bootstrap_flags (rider)`,
	}
}

// ReplaceFlags makes flags the complete set for an issuer, in one transaction.
// The bootstrap re-derives them at every start, so a condition that no longer
// holds is dropped here rather than lingering on the People page.
func (s *Store) ReplaceFlags(ctx context.Context, issuer string, flags []Flag) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("identities: saving flags: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM bootstrap_flags WHERE issuer = ?`), issuer); err != nil {
		return fmt.Errorf("identities: clearing flags: %w", err)
	}
	now := s.stamp()
	for _, f := range flags {
		sub, rider := strings.TrimSpace(f.Sub), normalizeRider(f.Rider)
		if sub == "" || rider == "" || f.Reason == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.dialect.Rebind(
			`INSERT INTO bootstrap_flags (issuer, sub, rider, reason, flagged_at) VALUES (?, ?, ?, ?, ?)
             ON CONFLICT (issuer, sub) DO UPDATE SET rider = excluded.rider, reason = excluded.reason`),
			issuer, sub, rider, f.Reason, now); err != nil {
			return fmt.Errorf("identities: saving a flag: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("identities: saving flags: %w", err)
	}
	return nil
}

// ClearFlag drops one account's flag, for when an admin has settled it.
func (s *Store) ClearFlag(ctx context.Context, issuer, sub string) error {
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM bootstrap_flags WHERE issuer = ? AND sub = ?`), issuer, strings.TrimSpace(sub)); err != nil {
		return fmt.Errorf("identities: clearing a flag: %w", err)
	}
	return nil
}

// Flags is the held-back accounts of an issuer, by sub.
func (s *Store) Flags(ctx context.Context, issuer string) (map[string]Flag, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT sub, rider, reason FROM bootstrap_flags WHERE issuer = ?`), issuer)
	if err != nil {
		return nil, fmt.Errorf("identities: reading flags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Flag{}
	for rows.Next() {
		var f Flag
		if err := rows.Scan(&f.Sub, &f.Rider, &f.Reason); err != nil {
			return nil, err
		}
		out[f.Sub] = f
	}
	return out, rows.Err()
}
