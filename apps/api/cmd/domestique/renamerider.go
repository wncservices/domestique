package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// normalizeRider matches accounts.ID's own normalization
// (strings.ToLower(strings.TrimSpace(rider))) — the same rider string has to
// normalize identically everywhere or a rename could target a row that a
// case- or whitespace-difference means was never actually renamed.
func normalizeRider(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// runRenameRider is the one-off migration docs/rider-migration.md walks an
// operator through: moving every row keyed to an old rider identity onto a
// new one, in a single transaction, once.
//
// The work is api.RenameRider, driven by the same registry (riderTables) the
// purge of a removed rider is, so a table added to the schema cannot be missed
// here: the command used to hard-code four tables and left all the training
// data under the old name. This function only validates the arguments and
// reports.
//
// provider_links is created by the API server (providerlink.UseDB), which this
// path never runs, so it is created here before anything reads it. The other
// tables are only renamed where they already exist.
func runRenameRider(src *source.DB, args []string, dryRun, replace bool) error {
	if len(args) != 2 {
		return errors.New("rename-rider needs exactly two arguments: <old-rider> <new-rider>\n" +
			"       see docs/rider-migration.md before running this for real")
	}
	old, next := normalizeRider(args[0]), normalizeRider(args[1])
	if old == "" || next == "" {
		return errors.New("rename-rider: neither rider may be empty")
	}
	if old == next {
		return errors.New("rename-rider: old and new are the same rider — nothing to do")
	}
	if !accounts.RiderPattern.MatchString(next) {
		return fmt.Errorf("rename-rider: %q has characters that cannot appear in an account id", next)
	}

	d, err := dbx.For(src.DSN())
	if err != nil {
		return err
	}
	// A box is not needed — this touches only the rider column, never a sealed
	// secret — so nil is fine; UseDB's table creation is the only reason to
	// call it here.
	if _, err := providerlink.UseDB(src.Conn(), src.DSN(), nil); err != nil {
		return err
	}

	sum, err := api.RenameRider(src.Conn(), d, old, next, api.RenameOptions{DryRun: dryRun, Replace: replace})
	if err != nil {
		return err
	}

	fmt.Printf("routes:              %d\n", sum.Tables["routes"])
	fmt.Printf("accounts:            %d\n", sum.Accounts)
	fmt.Printf("sync state rows:     %d\n", sum.SyncState)
	fmt.Printf("provider sign-ins:   %d\n", sum.ProviderLinks)
	// Everything else the registry moved, by table, so what a training-data
	// rename did is visible and a dry run says what it would do.
	var others []string
	for table := range sum.Tables {
		if table != "routes" {
			others = append(others, table)
		}
	}
	sort.Strings(others)
	for _, table := range others {
		fmt.Printf("%-20s %d\n", table+":", sum.Tables[table])
	}
	if replace && (sum.ReplacedAccounts > 0 || sum.ReplacedSyncState > 0 || sum.ReplacedProviderLinks > 0) {
		fmt.Printf("\nreplaced on conflict (deleted, %s's row took over):\n", next)
		fmt.Printf("  accounts:          %d\n", sum.ReplacedAccounts)
		fmt.Printf("  sync state rows:   %d\n", sum.ReplacedSyncState)
		fmt.Printf("  provider sign-ins: %d\n", sum.ReplacedProviderLinks)
	}
	if dryRun {
		fmt.Println("\ndry run — nothing written")
	} else {
		fmt.Printf("\nrenamed %q to %q\n", old, next)
	}
	return nil
}
