package api

// riderTable says what purgeRiderData does with one table that holds
// something about a rider.
type riderTable struct {
	// Purged is true when removing a rider deletes their rows from the table.
	Purged bool
	// Note is why a table is kept (Purged false), or which store method does
	// the deleting when it is not obvious from the table's own name.
	Note string
	// Rename is what rename-rider does with the table; see renameRule.
	Rename renameRule
	// Taken names the columns that hold a rider's name when Rename has none to
	// offer (accounts and provider_links are renamed bespoke). Sign-in treats a
	// name found in any of them as taken; see riderNameColumns.
	Taken []string
}

// uniqueKey is one unique index that includes the rider.
type uniqueKey struct {
	// Columns are all of the index's columns, the rider's among them.
	Columns []string
	// NonEmpty names columns of a partial index (WHERE col <> ''): two rows only
	// collide when these are not empty.
	NonEmpty []string
}

// renameRule says how rename-rider treats one registered table. Exactly one of
// Columns, Bespoke or Skip is set; TestEveryRegisteredTableHasARenameRule holds
// every entry to that and to naming every rider column the real schema has.
//
// Attribution columns (owner, created_by, decided_by, updated_by) are renamed
// along with the rider column proper. They name the same person: leaving
// "created_by = old" on a ride would credit someone who no longer exists.
type renameRule struct {
	// Columns are the columns that hold a rider's name; each is rewritten from
	// the old name to the new. The first is the one a unique key is built on.
	Columns []string
	// Unique is true when the rider is part of a unique key (a primary key or a
	// unique constraint), so the new rider's existing rows can collide. With is
	// the rest of that key; empty means the rider alone is the key. A collision
	// is refused, never merged.
	Unique bool
	With   []string
	// Also is every further unique key that includes the rider, beyond the one
	// Unique/With state (a second unique index, a partial one). A merge has to
	// know each of them to keep the target's row instead of aborting on a
	// collision; TestEveryUniqueIndexOnARiderKeyedTableIsInTheRegistry compares
	// them with the schema's real indexes.
	Also []uniqueKey
	// Bespoke tables are renamed by RenameRider's own code: a derived id
	// ("<provider>:<rider>") and --replace need more than a column rewrite.
	Bespoke bool
	// Skip is why a table has nothing to rename: it carries no rider name and
	// reaches the rider through an id that does not change.
	Skip string
}

// riderTables is the one place every table that is keyed by a rider — by a
// rider-ish column, or through an id a rider owns (workout_id, session_id,
// goal_id, account_id) — has to be registered, with what removing a rider does
// to it. A new table that is not listed here fails
// TestEveryRiderKeyedTableIsRegistered, because a table nobody thought about
// is how a deleted rider's HRV, sleep and ride history stayed behind.
//
// Registering a table as Purged is a promise that purgeRiderData reaches it:
// TestPurgeRemovesEveryRidersData seeds every Purged table and checks it is
// empty afterwards, and refuses a Purged entry it has no seed for.
//
// Kept entries are shared artefacts that outlive their author on purpose (a
// crew is its members', not its creator's). Where a kept table still names the
// rider as author, the author is blanked rather than the row deleted.
var riderTables = map[string]riderTable{
	// internal/source
	"routes": {Purged: true, Note: "routes the rider uploaded are deleted (Source.Delete)",
		Rename: renameRule{Columns: []string{"uploaded_by"}}},

	// internal/accounts, internal/state, internal/providerlink
	"accounts":       {Purged: true, Note: "Accounts.Unlink", Rename: renameRule{Bespoke: true}, Taken: []string{"rider"}},
	"sync_state":     {Purged: true, Note: "keyed through account_id; Store.Forget per account", Rename: renameRule{Bespoke: true}},
	"provider_links": {Purged: true, Note: "Links.Delete per provider", Rename: renameRule{Bespoke: true}, Taken: []string{"rider"}},

	// The pre-provider_links Komoot table, still there on a deployment that
	// ran the old image. Left behind on purpose, so a purge has to reach it or
	// the next start adopts the sealed token straight back; a rename has to
	// reach it for the same reason.
	"komoot_links": {Purged: true, Note: "Links.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true}},

	// internal/sessions: rider_key is a keyed one-way stand-in for the rider;
	// the sealed identity cannot be renamed by the generic column rewriter.
	"sessions": {Purged: true, Note: "Sessions.DeleteRider: every login of the rider ends at once",
		Rename: renameRule{Skip: "rider_key is a keyed HMAC, not the rider name"}},

	// internal/identities: which issuer accounts sign in as the rider. A purge
	// removes them so an old login cannot reach the rider again; a rename moves
	// them with the rider. The key is (issuer, sub), so a rename cannot collide.
	"rider_identities": {Purged: true, Note: "Identities.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},

	// internal/identities: the accounts the startup bootstrap held back, and the
	// rider each was held back from, so the People page can still say why after
	// a restart. It names a rider, so a purge and a rename follow it.
	"bootstrap_flags": {Purged: true, Note: "Identities.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},

	// internal/garminmfa
	"garmin_mfa_challenges": {Purged: true, Note: "GarminMFA.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},

	// internal/crew, internal/schedule
	"crew_members": {Purged: true, Note: "Crew.RemoveRiderEverywhere",
		Rename: renameRule{Columns: []string{"rider", "decided_by"}, Unique: true, With: []string{"crew_id"}}},
	"crews": {Note: "the crew belongs to its members; a crew left without an owner is handled by the admin override",
		Rename: renameRule{Columns: []string{"owner"}}},
	"crew_rides": {Note: "the ride is the crew's plan; Schedule.ClearCreatedBy blanks the author",
		Rename: renameRule{Columns: []string{"created_by"}}},
	"ride_series": {Note: "the series is the crew's plan; Schedule.ClearCreatedBy blanks the author",
		Rename: renameRule{Columns: []string{"created_by"}}},

	// What a rider said about the crew's rides: it is theirs, so a purge deletes
	// it. A rider's own fixed sessions are workouts and go with Training.
	"crew_ride_going": {Purged: true, Note: "Schedule.DeleteRider: which crew rides the rider said they were going to",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"ride_id"}}},

	// Ride together. A purge removes the rider's flag and takes them off every
	// proposal they are in, and a proposal a rider leaves is ended, not left open
	// for the others to accept: Schedule.DeleteRider does both. The same happens
	// when a crew removes a member (Schedule.RemoveRider), for that crew.
	//
	// ride_together_proposals is deliberately not listed: it is the crew's, has no
	// rider column and no id a rider owns, so the guard that holds this registry
	// to rider-keyed tables would reject it. It holds a week, a day and a route
	// slug and nothing about anyone's plan; its members are the rows below.
	"crew_ride_together": {Purged: true, Note: "Schedule.DeleteRider: the weekdays the rider is open to a shared ride",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"crew_id"}}},
	"ride_together_members": {Purged: true, Note: "Schedule.DeleteRider: the rider's answer on a proposal; the proposal is ended",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"proposal_id"}}},

	// internal/routeshare
	"route_shares": {Purged: true, Note: "Shares.DeleteRider: links the rider created; Shares.DeleteRoute: links to a deleted route",
		Rename: renameRule{Columns: []string{"created_by", "route_owner"}}},
	"route_share_redemptions": {Purged: true, Note: "Shares.DeleteRider: what the rider viewed, and who viewed theirs",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"share_id"}}},

	// internal/calendarfeed: the hashed token of a rider's private calendar
	// link. Purging it is what kills a departed rider's feed URL.
	"calendar_feeds": {Purged: true, Note: "CalendarFeeds.DeleteRider: the rider's calendar link stops working",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true}},

	// internal/morningsummary: the opt-in and the address mail goes to. Purging
	// it is what stops mail to a rider who has left.
	"morning_summaries": {Purged: true, Note: "MorningSummaries.DeleteRider: the opt-in and the stored address",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true}},

	// internal/pacingpush
	"pacing_pushes": {Purged: true, Note: "PacingPushes.DeleteRider: the record of which course each pacing plan became on the rider's own account",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"provider", "key"}}},

	// internal/weather
	"weather_locations": {Purged: true, Note: "WeatherPrefs.Delete",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true}},

	// internal/ridestart: where the rider's planned rides start, kept at about
	// 110 m. Location data, so it goes with the rider and follows a rename.
	"ride_start_points": {Purged: true, Note: "RideStarts.Delete",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true}},

	// internal/workout, all of it through Training.DeleteRider
	"goals": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},
	"rider_profiles": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true}},
	"workouts": {Purged: true, Note: "Training.DeleteRider",
		// workouts_crew_ride_idx: a rider has one fixed session per crew ride.
		Rename: renameRule{Columns: []string{"rider"},
			Also: []uniqueKey{{Columns: []string{"rider", "crew_ride_id"}, NonEmpty: []string{"crew_ride_id"}}}}},
	"workout_pushes": {Purged: true, Note: "keyed through workout_id; Training.DeleteRider",
		Rename: renameRule{Skip: "keyed through workout_id, which a rename does not change"}},
	"scheduled_weeks": {Purged: true, Note: "keyed through goal_id; Training.DeleteRider",
		Rename: renameRule{Skip: "keyed through goal_id, which a rename does not change"}},
	"completed_sessions": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},
	"session_analyses": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},
	"ride_imports": {Purged: true, Note: "Training.DeleteRider: the counts of the rider's own history imports",
		Rename: renameRule{Columns: []string{"rider"}}},
	"session_links": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},
	"fitness_snapshots": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"date"}}},
	"daily_wellness": {Purged: true, Note: "Training.DeleteRider: HRV, sleep and resting heart rate",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"date"}}},
	"progression_levels": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true, With: []string{"sport", "zone"}}},
	"progression_history": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},
	"threshold_suggestions": {Purged: true, Note: "Training.DeleteRider",
		Rename: renameRule{Columns: []string{"rider"}}},
	"life_events": {Purged: true, Note: "Training.DeleteRider: where a rider was ill or away is health data",
		Rename: renameRule{Columns: []string{"rider"}}},
	"adjustments": {Purged: true, Note: "Training.DeleteRiderAdjustments",
		Rename: renameRule{Columns: []string{"rider"}, Unique: true,
			With: []string{"subject_kind", "subject_id", "day", "rule"}}},

	// internal/settings: updated_by names the admin who last saved a
	// deployment-wide value. The value is the deployment's, not the admin's,
	// so a purge keeps it; a rename still follows the admin's new name.
	"settings": {Note: "deployment-wide configuration; updated_by is attribution only",
		Rename: renameRule{Columns: []string{"updated_by"}}},
	"flags": {Note: "deployment-wide switches; updated_by is attribution only",
		Rename: renameRule{Columns: []string{"updated_by"}}},
}
