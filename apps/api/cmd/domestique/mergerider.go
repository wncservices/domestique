package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// runMergeRider moves everything one rider owns onto another that already has
// data of its own: the fix for a person who ended up as two riders (an old
// name-derived one such as wilant.nackaerts, and the one they are bound to),
// where rename-rider refuses because the target is not empty.
//
// The work is api.MergeRider, driven by the same registry (riderTables) as the
// rename and the purge. Where both riders have a row on one key the target's
// stays and the source's is dropped; the output says in which tables, and
// --dry-run says it before anything is written (the same work, rolled back).
// docs/rider-migration.md has the runbook.
//
// Sessions of both riders end when the deployment's key is available, since
// they are found by a keyed hash of the rider; without it they expire on their
// own and the command says so.
func runMergeRider(src *source.DB, args []string, dryRun bool) error {
	if len(args) != 2 {
		return errors.New("merge-rider needs exactly two arguments: <from-rider> <into-rider>\n" +
			"       the first rider's data moves onto the second; see docs/rider-migration.md")
	}
	from, into := normalizeRider(args[0]), normalizeRider(args[1])
	if from == "" || into == "" {
		return errors.New("merge-rider: neither rider may be empty")
	}
	if from == into {
		return errors.New("merge-rider: both names are the same rider — nothing to do")
	}
	if !accounts.RiderPattern.MatchString(into) {
		return fmt.Errorf("merge-rider: %q has characters that cannot appear in an account id", into)
	}

	d, err := dbx.For(src.DSN())
	if err != nil {
		return err
	}
	// provider_links is created by the API server, which this path never runs.
	if _, err := providerlink.UseDB(src.Conn(), src.DSN(), nil); err != nil {
		return err
	}

	box, err := sessionKey()
	if err != nil {
		return err
	}
	opt := api.MergeOptions{DryRun: dryRun}
	sessionsNote := ""
	if !dryRun {
		if box == nil {
			sessionsNote = fmt.Sprintf("sessions:   not ended — %s is not set, so they cannot be found; they expire on their own", secrets.EnvKey)
		} else {
			ss, err := sessions.UseDB(src.Conn(), src.DSN(), box)
			if err != nil {
				return err
			}
			opt.EndSessions = ss.DeleteRider
		}
	}

	sum, err := api.MergeRider(context.Background(), src.Conn(), d, from, into, opt)
	if err != nil {
		return err
	}

	fmt.Printf("%-26s %8s %12s %15s\n", "table", "moved", "kept-target", "dropped-source")
	for _, table := range sum.TableNames() {
		c := sum.Tables[table]
		fmt.Printf("%-26s %8d %12d %15d\n", table, c.Moved, c.KeptTarget, c.DroppedSource)
	}
	t := sum.Totals()
	fmt.Printf("%-26s %8d %12d %15d\n", "total", t.Moved, t.KeptTarget, t.DroppedSource)
	if t.DroppedSource > 0 {
		fmt.Printf("\nwhere both riders had a row on the same key, %s's was kept and %s's dropped\n", into, from)
	}
	switch {
	case dryRun:
		fmt.Println("\ndry run — nothing written")
	default:
		if sessionsNote != "" {
			fmt.Println(sessionsNote)
		} else {
			fmt.Printf("sessions:   %d ended; both riders sign in again\n", sum.SessionsEnded)
		}
		fmt.Printf("\nmerged %q into %q\n", from, into)
	}
	return nil
}
