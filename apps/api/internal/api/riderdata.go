package api

// riderTable says what purgeRiderData does with one table that holds
// something about a rider.
type riderTable struct {
	// Purged is true when removing a rider deletes their rows from the table.
	Purged bool
	// Note is why a table is kept (Purged false), or which store method does
	// the deleting when it is not obvious from the table's own name.
	Note string
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
	"routes": {Purged: true, Note: "routes the rider uploaded are deleted (Source.Delete)"},

	// internal/accounts, internal/state, internal/providerlink
	"accounts":       {Purged: true, Note: "Accounts.Unlink"},
	"sync_state":     {Purged: true, Note: "keyed through account_id; Store.Forget per account"},
	"provider_links": {Purged: true, Note: "Links.Delete per provider"},

	// The pre-provider_links Komoot table, still there on a deployment that
	// ran the old image. Left behind on purpose, so a purge has to reach it or
	// the next start adopts the sealed token straight back.
	"komoot_links": {Purged: true, Note: "Links.DeleteRider"},

	// internal/sessions: the identity is sealed, so rider_key is a keyed
	// one-way stand-in for the rider, which is how their logins are found.
	"sessions": {Purged: true, Note: "Sessions.DeleteRider: every login of the rider ends at once"},

	// internal/garminmfa
	"garmin_mfa_challenges": {Purged: true, Note: "GarminMFA.DeleteRider"},

	// internal/crew, internal/schedule
	"crew_members": {Purged: true, Note: "Crew.RemoveRiderEverywhere"},
	"crews":        {Note: "the crew belongs to its members; a crew left without an owner is handled by the admin override"},
	"crew_rides":   {Note: "the ride is the crew's plan; Schedule.ClearCreatedBy blanks the author"},
	"ride_series":  {Note: "the series is the crew's plan; Schedule.ClearCreatedBy blanks the author"},

	// internal/routeshare
	"route_shares":            {Purged: true, Note: "Shares.DeleteRider: links the rider created"},
	"route_share_redemptions": {Purged: true, Note: "Shares.DeleteRider: what the rider viewed, and who viewed theirs"},

	// internal/weather
	"weather_locations": {Purged: true, Note: "WeatherPrefs.Delete"},

	// internal/workout — all of it through Training.DeleteRider
	"goals":                 {Purged: true, Note: "Training.DeleteRider"},
	"rider_profiles":        {Purged: true, Note: "Training.DeleteRider"},
	"workouts":              {Purged: true, Note: "Training.DeleteRider"},
	"workout_pushes":        {Purged: true, Note: "keyed through workout_id; Training.DeleteRider"},
	"scheduled_weeks":       {Purged: true, Note: "keyed through goal_id; Training.DeleteRider"},
	"completed_sessions":    {Purged: true, Note: "Training.DeleteRider"},
	"session_analyses":      {Purged: true, Note: "Training.DeleteRider"},
	"session_links":         {Purged: true, Note: "Training.DeleteRider"},
	"fitness_snapshots":     {Purged: true, Note: "Training.DeleteRider"},
	"daily_wellness":        {Purged: true, Note: "Training.DeleteRider: HRV, sleep and resting heart rate"},
	"progression_levels":    {Purged: true, Note: "Training.DeleteRider"},
	"progression_history":   {Purged: true, Note: "Training.DeleteRider"},
	"threshold_suggestions": {Purged: true, Note: "Training.DeleteRider"},

	// internal/settings: updated_by names the admin who last saved a
	// deployment-wide value. The value is the deployment's, not the admin's.
	"settings": {Note: "deployment-wide configuration; updated_by is attribution only"},
	"flags":    {Note: "deployment-wide switches; updated_by is attribution only"},
}
