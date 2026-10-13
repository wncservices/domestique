package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A name that appears in any registered table is taken. This is what stops a
// sign-in carrying someone's name from landing on them just because they
// have, say, only training data and no binding yet: the check is built from
// riderTables, and this holds every entry in it to that.
func TestAnyRegisteredTableMakesARiderNameTaken(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		checked := 0
		for _, s := range allRenameSeeds() {
			rt := riderTables[s.table]
			// Reached through an id a rider owns (the account, workout or goal
			// already counted by its own table) or a keyed stand-in.
			if rt.Rename.Skip != "" || s.table == "sync_state" {
				continue
			}
			rider := strings.ReplaceAll("taken-"+s.table, "_", "-") + "-" + tok
			if err := s.insert(env, rider, "t"+tok); err != nil {
				t.Fatalf("seeding %s: %v", s.table, err)
			}
			claim, err := env.srv.Identities.ClaimFree(context.Background(), "https://issuer.test/", "sub|"+s.table+tok, "", rider, riderNameColumns())
			if err != nil {
				t.Fatalf("%s: %v", s.table, err)
			}
			if !claim.Suffixed || claim.Rider != rider+"-2" {
				t.Errorf("%s: a sign-in named %q got %+v, want %q because the name is in that table", s.table, rider, claim, rider+"-2")
			}
			checked++
		}
		if checked < 20 {
			t.Errorf("only %d tables were checked; the seeds no longer cover the registry", checked)
		}
	})
}

// A registered table that offers no column for the check would silently let a
// name through, so each one names a column or says why it has none.
func TestEveryRegisteredTableOffersTheTakenCheckAColumnOrAReason(t *testing.T) {
	cols := riderNameColumns()
	for table, rt := range riderTables {
		if len(cols[table]) > 0 {
			continue
		}
		if rt.Rename.Skip == "" && table != "sync_state" {
			t.Errorf("%s: no column for the taken check and no Skip reason (see riderNameColumns)", table)
		}
	}
}
