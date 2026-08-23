// Command domestique reconciles a library of cycling routes into each rider's
// Garmin Connect and Wahoo account.
//
// The library is a database — PostgreSQL for a deployment, SQLite for a
// laptop. Routes get in by upload, by Komoot import, or by `domestique import`
// from a directory of files. The app itself holds no route data.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/basemap"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/komoot"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/oidcflow"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/sync"
	"github.com/wncservices/domestique/apps/api/internal/targets"
	"github.com/wncservices/domestique/apps/api/internal/telemetry"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

const usage = `Domestique — fetch-and-carry for cycling routes

usage: domestique <command> [flags]

commands:
  validate      read the library and report problems
  plan          show what would change on each account
  push          apply the plan (use --dry-run to preview)
  state         list what each account is recorded as holding
  import        load a directory of .gpx files into the database
  komoot        list or import routes from a Komoot account
  fit           export a route as a Garmin FIT course
  serve         run the HTTP API and the web UI
  rename-rider  move one rider's routes, accounts and sign-ins to a new identity
                (see docs/rider-migration.md before running this for real;
                --replace resolves a conflict by keeping the old rider's row)
  keygen        print a new DOMESTIQUE_ENCRYPTION_KEY
  version       print the version

common flags:
  --config PATH    app config (default domestique.yaml)

database:
  --db DSN         PostgreSQL URL, or a SQLite file path
                   (also DOMESTIQUE_SOURCE_DSN, which is how a deployment
                   supplies a password without writing it to a file)

serve flags:
  --addr ADDR      listen address (default :8080)
  --web-dir PATH   built frontend to serve (default apps/web/dist)

import flags:
  --from PATH      directory of .gpx files to load into the database

fit:
  domestique fit <slug> [--out FILE] [--cues]

  Writes a FIT course, which can be copied straight onto a device over USB.
  --cues adds turn cues inferred from the track's shape; they are a heuristic,
  so check them before trusting them on a ride.

komoot:
  domestique komoot list             show the account's planned routes
  domestique komoot import [ids...]  import them (all planned routes if no ids)

  Credentials come from KOMOOT_EMAIL and KOMOOT_PASSWORD in the environment.
  --owner RIDER    rider to record as the owner of imported routes — the CLI
                   has no signed-in identity of its own, so this is how an
                   imported route gets one at all. Omit it and the routes
                   come in ownerless, same as before; an owner can still be
                   claimed for them afterward from the web UI.
`

// version is set at build time: -ldflags="-X main.version=v1.2.3".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	configPath := fs.String("config", "domestique.yaml", "app config file")
	dsn := fs.String("db", "", "PostgreSQL URL or SQLite file path")
	dryRun := fs.Bool("dry-run", false, "print what push would do without doing it")
	replace := fs.Bool("replace", false,
		"rename-rider: on conflict, delete the new rider's existing row and let the old rider's take its place")
	addr := fs.String("addr", ":8080", "listen address for serve")
	webDir := fs.String("web-dir", filepath.Join("apps", "web", "dist"), "built frontend to serve")
	from := fs.String("from", "", "directory of GPX routes to import")
	out := fs.String("out", "", "file to write (default <slug>.fit)")
	cues := fs.Bool("cues", false, "add turn cues inferred from the track's shape")
	owner := fs.String("owner", "", "komoot import: rider to record as the owner of imported routes")

	var positional []string

	switch cmd {
	case "validate", "plan", "push", "state", "serve", "import", "komoot", "fit", "rename-rider", "keygen":
		// Go's flag package stops at the first positional argument, so
		// `fit <slug> --cues` would silently ignore --cues. Parse in a loop,
		// peeling off positionals, so flags and arguments can interleave in
		// whatever order reads naturally.
		var err error
		if positional, err = parseInterleaved(fs, rest); err != nil {
			return err
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "-v", "--version", "version":
		fmt.Println("domestique", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try: domestique help)", cmd)
	}

	// Before the config and the database: generating a key is what you do
	// *before* there is a deployment to point it at.
	if cmd == "keygen" {
		return runKeygen()
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	applyOverrides(cfg, *dsn)
	if err := cfg.Validate(); err != nil {
		return err
	}

	src, err := openSource(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	store, err := openState(src)
	if err != nil {
		return err
	}

	linkedAccounts, err := openAccounts(src)
	if err != nil {
		return err
	}
	crews, err := openCrews(src)
	if err != nil {
		return err
	}
	switch cmd {
	case "validate":
		return runValidate(src, linkedAccounts, crews)
	case "plan":
		return runPlan(src, linkedAccounts, store, crews)
	case "push":
		return runPush(src, linkedAccounts, store, crews, *dryRun)
	case "import":
		return runImport(src, *from)
	case "state":
		return runState(store)
	case "serve":
		return runServe(src, cfg, store, *addr, *webDir)
	case "komoot":
		return runKomoot(src, cfg, positional, *owner)
	case "fit":
		return runFIT(src, positional, *out, *cues)
	case "rename-rider":
		return runRenameRider(src, positional, *dryRun, *replace)
	}
	return nil
}

// runKeygen prints a fresh encryption key.
//
// To stdout on its own line, so it can be piped straight into a secret store
// without a human copying it through a terminal scrollback.
func runKeygen() error {
	key, err := secrets.GenerateKey()
	if err != nil {
		return err
	}
	fmt.Println(key)
	return nil
}

// runFIT writes a route out as a FIT course.
//
// This is how the conversion gets proven: copy the file onto a real head unit
// and see whether it navigates. Nothing in a test suite can establish that.
func runFIT(src *source.DB, args []string, out string, cues bool) error {
	if len(args) == 0 {
		return errors.New("fit needs a route slug (see: domestique validate)")
	}
	slug := args[0]

	raw, err := src.GPX(context.Background(), slug)
	if err != nil {
		return err
	}
	points, err := gpx.ParsePoints(raw)
	if err != nil {
		return err
	}

	name := slug
	if routes, _, listErr := src.List(context.Background()); listErr == nil {
		for _, route := range routes {
			if route.Slug == slug {
				name = route.Name
				break
			}
		}
	}

	fitBytes, err := fitcourse.Encode(points, fitcourse.Options{Name: name, TurnCues: cues})
	if err != nil {
		return err
	}

	if out == "" {
		// Derive the filename from the slug, but never let a slug decide where
		// on disk the file lands: flatten separators and take the base, so the
		// result is always a plain name in the working directory.
		out = filepath.Base(strings.NewReplacer("/", "-", `\`, "-").Replace(slug)) + ".fit"
	}

	// #nosec G703 -- --out is an operator-supplied path, the same as any
	// shell redirect; a slug-derived name is flattened above.
	if err := os.WriteFile(out, fitBytes, 0o600); err != nil {
		return err
	}

	turns := 0
	if cues {
		turns = len(fitcourse.Turns(points))
	}
	fmt.Printf("wrote %s (%d bytes, %d points", out, len(fitBytes), len(points))
	if cues {
		fmt.Printf(", %d turn cue(s)", turns)
	}
	fmt.Println(")")
	return nil
}

// managementDomain pulls the bare host out of an OIDC issuer URL —
// auth0mgmt.Config.Domain carries no scheme, the same convention
// auth.OIDCConfig.Issuer itself does not follow (it is a full URL, since
// discovery needs one), so this is the one place the two conventions meet.
func managementDomain(issuer string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not a valid URL: %q", issuer)
	}
	return u.Host, nil
}

// komootClient logs in using the environment. Credentials never come from the
// config file — that file is meant to be readable, and this is a password.
func komootClient() (*komoot.Client, error) {
	email, password := os.Getenv("KOMOOT_EMAIL"), os.Getenv("KOMOOT_PASSWORD")
	if email == "" || password == "" {
		return nil, errors.New("set KOMOOT_EMAIL and KOMOOT_PASSWORD to use Komoot")
	}

	client := komoot.New()
	if err := client.Login(context.Background(), email, password); err != nil {
		return nil, err
	}
	return client, nil
}

func runKomoot(dst *source.DB, cfg *config.Config, args []string, owner string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}

	client, err := komootClient()
	if err != nil {
		return err
	}

	tours, err := client.Tours(context.Background(), cfg.Komoot.IncludeRecorded)
	if err != nil {
		return err
	}

	switch sub {
	case "list":
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSPORT\tDISTANCE\tASCENT")
		for _, t := range tours {
			fmt.Fprintf(w, "%s\t%s\t%s\t%.1f km\t%.0f m\n",
				t.ID, t.Name, t.Sport, t.DistanceM/1000, t.AscentM)
		}
		w.Flush()
		fmt.Printf("\n%d tour(s) in %s's Komoot account\n", len(tours), client.DisplayName())
		return nil

	case "import":
		wanted := map[string]bool{}
		for _, id := range args {
			wanted[id] = true
		}

		var imported int
		var problems []string
		for _, tour := range tours {
			if len(wanted) > 0 && !wanted[tour.ID] {
				continue
			}

			raw, err := client.GPX(context.Background(), tour.ID)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s (%s): %v", tour.Name, tour.ID, err))
				continue
			}
			if _, err := dst.Create(context.Background(), source.CreateRequest{
				Filename:   tour.Name + ".gpx",
				Name:       tour.Name,
				Tags:       []string{"komoot", "komoot:" + tour.ID},
				UploadedBy: owner,
				GPX:        raw,
			}); err != nil {
				problems = append(problems, fmt.Sprintf("%s (%s): %v", tour.Name, tour.ID, err))
				continue
			}
			imported++
			fmt.Printf("imported %s (%s)\n", tour.Name, tour.ID)
		}

		fmt.Printf("\n%d tour(s) imported into %s\n", imported, dst.Describe())
		return reportProblems(problems)

	default:
		return fmt.Errorf("unknown komoot subcommand %q (want list or import)", sub)
	}
}

// parseInterleaved parses flags that may appear before, after or between
// positional arguments, and returns the positionals in order.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string

	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}

	return positional, nil
}

func applyOverrides(cfg *config.Config, dsn string) {
	if dsn != "" {
		cfg.Source.DSN = dsn
	}
}

func openSource(cfg *config.Config) (*source.DB, error) {
	if cfg.Source.DSN == "" {
		return nil, errors.New("no database configured: set --db, source.dsn, or DOMESTIQUE_SOURCE_DSN")
	}
	return source.OpenDB(cfg.Source.DSN)
}

// openAccounts reads the linked head units.
//
// They live in the database beside the routes, put there by riders through the
// UI. A directory-backed library has no database, so there is nothing to link
// against and the CLI reports none — plan and push then have nothing to do,
// which is the honest answer.
func openAccounts(src *source.DB) ([]model.Account, error) {
	store, err := accountStoreFor(src)
	if err != nil {
		return nil, err
	}
	return store.List()
}

// accountStoreFor builds the accounts store the API links through.
//
// Linking needs somewhere to write, so it needs a database. A directory-backed
// library has none, and the API says so rather than pretending.
func accountStoreFor(src *source.DB) (*accounts.Store, error) {
	return accounts.UseDB(src.Conn(), src.DSN())
}

// openCrews reads every crew and its current approved membership, the same
// snapshot the API's own crewSnapshot helper builds — the CLI has to close
// the same gap TargetsFor closes for the server, or a route shared to a
// crew would silently reach nobody but the owner when pushed from a laptop.
func openCrews(src *source.DB) (crew.Snapshot, error) {
	store, err := crew.UseDB(src.Conn(), src.DSN())
	if err != nil {
		return crew.Snapshot{}, err
	}
	return store.Snapshot(context.Background())
}

// openState decides where sync state lives.
//
// With a database source it goes in that same database, which is the whole
// point: a deployment then needs one database and no volume. A directory
// source has no database to borrow, so it falls back to the JSON file.
func openState(src *source.DB) (state.Store, error) {
	return state.UseDB(src.Conn(), src.DSN())
}

func runValidate(src *source.DB, linked []model.Account, crews crew.Snapshot) error {
	routes, problems, err := src.List(context.Background())
	if err != nil {
		return err
	}

	fmt.Println(src.Describe())
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROUTE\tNAME\tDISTANCE\tASCENT\tPOINTS\tTARGETS")
	for _, r := range routes {
		fmt.Fprintf(w, "%s\t%s\t%.1f km\t%.0f m\t%d\t%v\n",
			r.Slug, r.Name, r.Stats.DistanceM/1000, r.Stats.AscentM,
			r.Stats.PointCount, config.TargetsFor(r, linked, crews))
		for _, unknown := range config.UnknownTargets(r, crews) {
			problems = append(problems, fmt.Sprintf("%s: unknown target %q", r.Slug, unknown))
		}
	}
	w.Flush()

	fmt.Printf("\n%d route(s), %d linked account(s)\n", len(routes), len(linked))
	return reportProblems(problems)
}

func runPlan(src *source.DB, linked []model.Account, store state.Store, crews crew.Snapshot) error {
	routes, problems, err := src.List(context.Background())
	if err != nil {
		return err
	}

	plan, err := sync.BuildPlan(context.Background(), routes, linked, store, crews)
	if err != nil {
		return err
	}

	printPlan(plan)
	return reportProblems(problems)
}

func runPush(src *source.DB, linked []model.Account, store state.Store, crews crew.Snapshot, dryRun bool) error {
	routes, problems, err := src.List(context.Background())
	if err != nil {
		return err
	}

	plan, err := sync.BuildPlan(context.Background(), routes, linked, store, crews)
	if err != nil {
		return err
	}
	printPlan(plan)

	if dryRun {
		fmt.Println("\ndry run — nothing pushed")
		return reportProblems(problems)
	}
	if len(plan.Changes()) == 0 {
		return reportProblems(problems)
	}

	byAccount := map[string]targets.Target{}
	for _, account := range linked {
		target, err := targets.Build(account)
		if err != nil {
			return err
		}
		byAccount[account.ID] = target
	}

	failures := sync.Apply(context.Background(), plan, store, byAccount, nil)
	if err := reportProblems(problems); err != nil {
		return err
	}
	if len(failures) > 0 {
		fmt.Fprintln(os.Stderr)
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, "failed:", f)
		}
		return fmt.Errorf("%d of %d change(s) failed", len(failures), len(plan.Changes()))
	}

	fmt.Printf("\npushed %d change(s)\n", len(plan.Changes()))
	return nil
}

// importName picks the name a file should land under.
//
// Usually the filename. But a tree of `<route-name>/route.gpx` is a common
// export shape — and was this app's own layout once — where every file is
// called the same thing. Falling back to the directory keeps those from all
// importing as "Route".
func importName(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	switch strings.ToLower(stem) {
	case "route", "track", "gpx", "index":
		if parent := filepath.Base(filepath.Dir(path)); parent != "." && parent != string(filepath.Separator) {
			return parent + filepath.Ext(path)
		}
	}
	return filepath.Base(path)
}

// runImport loads a directory of .gpx files into the database.
//
// A one-off, for routes that already exist as files somewhere. It is not a
// storage mode: nothing keeps reading that directory afterwards.
func runImport(dst *source.DB, from string) error {
	if from == "" {
		return errors.New("import needs --from <directory>")
	}

	var files []string
	// #nosec G703 -- --from is an operator-supplied directory, the same as any
	// argument to cp; nothing here comes from a user of the running service.
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".gpx") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)

	var imported int
	var problems []string
	for _, path := range files {
		// #nosec G304 -- the directory is an operator-supplied argument.
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, readErr))
			continue
		}

		created, createErr := dst.Create(context.Background(), source.CreateRequest{
			Filename: importName(path),
			GPX:      raw,
		})
		if createErr != nil {
			// One unreadable file must not abandon the rest of the batch.
			problems = append(problems, fmt.Sprintf("%s: %v", path, createErr))
			continue
		}

		imported++
		fmt.Printf("imported %s -> %s\n", filepath.Base(path), created.Slug)
	}

	fmt.Printf("\n%d of %d file(s) imported into %s\n", imported, len(files), dst.Describe())
	return reportProblems(problems)
}

func runState(store state.Store) error {
	fmt.Println(describeStore(store))

	entries, err := store.All(context.Background())
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("no routes recorded — nothing has been pushed yet")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tROUTE\tREMOTE ID\tUPDATED")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.AccountID, e.Slug, e.RemoteID, e.UpdatedAt)
	}
	return w.Flush()
}

func runServe(src *source.DB, cfg *config.Config, store state.Store, addr, webDir string) error {
	routes, problems, err := src.List(context.Background())
	if err != nil {
		return err
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "problem:", p)
	}
	// An ownerless route (CLI-imported, or uploaded before ownership was
	// tracked) with no explicit targets used to reach every linked account;
	// now it reaches nobody — TargetsFor has nobody to resolve "own
	// accounts" against. Small, enumerable blast radius on the 1-2-rider
	// deployment this actually runs on, worth naming rather than
	// discovering at the next missed ride.
	for _, r := range routes {
		if r.Owner == "" && r.Targets == nil {
			fmt.Fprintf(os.Stderr,
				"problem: %s has no owner and no explicit targets — it will not reach any account; set an owner or share it to a crew\n", r.Slug)
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Traces only actually export once OTEL_EXPORTER_OTLP_ENDPOINT (or
	// _TRACES_ENDPOINT) is set — see internal/telemetry's own doc. Shutdown
	// flushes whatever the batch exporter is still holding; wired to the
	// same signal-triggered shutdown as the HTTP server below so a pod
	// restart does not drop the last few seconds of spans.
	shutdownTraces, err := telemetry.Setup(context.Background(), "domestique", version)
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}

	authenticator, err := auth.New(cfg.Auth)
	if err != nil {
		return err
	}

	accountStore, err := accountStoreFor(src)
	if err != nil {
		return err
	}

	// Wired unconditionally, the same as Accounts — a crew needs nothing
	// beyond the database every deployment already has.
	crewStore, err := crew.UseDB(src.Conn(), src.DSN())
	if err != nil {
		return err
	}

	// Wired unconditionally, the same as Crew.
	scheduleStore, err := schedule.UseDB(src.Conn(), src.DSN())
	if err != nil {
		return err
	}

	srv := &api.Server{
		Source:   src,
		Config:   cfg,
		Store:    store,
		Accounts: accountStore,
		Crew:     crewStore,
		Schedule: scheduleStore,
		Auth:     authenticator,
		Log:      log,
		// Pure in-memory, no external credential to be missing — wired
		// unconditionally, the same as Crew. 5 attempts per rider per 15
		// minutes is enough for someone who mistypes a password twice; see
		// ConnectLimiter's own doc comment for why this app's own traffic
		// (its own auth, its own API) is deliberately not limited here too
		// — that belongs to Traefik/Cloudflare in front of it, not this.
		ConnectLimiter: ratelimit.New(5, 15*time.Minute),
		// A separate instance from ConnectLimiter — see AuthActionLimiter's
		// own doc comment for why the two must not share one budget.
		AuthActionLimiter: ratelimit.New(5, 15*time.Minute),
	}

	srv.LandingHost = cfg.Web.LandingHost
	if host := os.Getenv("DOMESTIQUE_LANDING_HOST"); host != "" {
		srv.LandingHost = host
	}

	srv.KomootEnabled = cfg.Komoot.Enabled
	srv.Connector = api.LiveKomoot{}

	// Riders connect their own Komoot and Garmin from the UI. Without an
	// encryption key the store refuses to save anything, which is the intended
	// outcome: a session belongs in the database encrypted or not at all.
	box, err := secrets.FromEnv()
	switch {
	case errors.Is(err, secrets.ErrNoKey):
		log.Warn("no encryption key: riders cannot sign in to Komoot or Garmin from the UI",
			"hint", "set "+secrets.EnvKey+" (generate one with `domestique keygen`)")
	case err != nil:
		return err
	}
	links, err := providerlink.UseDB(src.Conn(), src.DSN(), box)
	if err != nil {
		return err
	}
	srv.Links = links

	// Deployment-wide settings an admin can change from the UI. Same key as
	// the sign-ins, so a deployment without one stores neither.
	appSettings, err := settings.UseDB(src.Conn(), src.DSN(), box)
	if err != nil {
		return err
	}
	srv.Settings = appSettings

	// Basemap update history, wired unconditionally like Settings above —
	// harmless to keep even when the Job side (below) never runs, and
	// keeps Store/Client's two "am I available" questions independent: the
	// UI can distinguish "never triggered" from "can't trigger."
	basemapStore, err := basemap.UseDB(src.Conn(), src.DSN())
	if err != nil {
		return err
	}
	srv.Basemap = basemapStore

	// Route-preview background geometry, precomputed and cached server-side
	// instead of every client decoding PMTiles vector tiles itself — see
	// basemap/preview.go. Independent of the Job-triggering block below
	// (that gates *creating* a basemap; this only *reads* whatever one
	// already exists), and like it, opt-in: unset means srv.PreviewTiles
	// stays nil and handleTrackPreview reports the feature unavailable,
	// same "quietly missing" shape as everything else in this file.
	if cfg.Basemap.TilesServiceURL != "" {
		previewCache, err := basemap.UsePreviewCacheDB(src.Conn(), src.DSN())
		if err != nil {
			return err
		}
		srv.PreviewCache = previewCache
		srv.PreviewTiles = basemap.NewPreviewTiles(cfg.Basemap.TilesServiceURL)
	}

	// The Job-triggering side is opt-in twice over: cfg.Basemap.TilesNamespace
	// unset means this deployment never asked for it, and InCluster
	// returning (nil, nil) means it asked but isn't actually running in a
	// cluster (a laptop) — either way srv.BasemapJobs stays nil and the UI
	// reports the feature unavailable, the same shape every other
	// deployment-wide integration in this file already degrades through.
	if cfg.Basemap.TilesNamespace != "" {
		jobs, err := basemap.InCluster(basemap.JobConfig{
			TilesNamespace:        cfg.Basemap.TilesNamespace,
			TilesPVCName:          cfg.Basemap.TilesPVCName,
			ExtractImage:          cfg.Basemap.ExtractImage,
			CopyImage:             cfg.Basemap.CopyImage,
			CPURequest:            cfg.Basemap.CPURequest,
			MemRequest:            cfg.Basemap.MemRequest,
			MemLimit:              cfg.Basemap.MemLimit,
			ActiveDeadlineSeconds: cfg.Basemap.ActiveDeadlineSeconds,
		})
		if err != nil {
			return err
		}
		if jobs == nil {
			log.Warn("basemap.tiles_namespace is set but this process is not running in a cluster",
				"hint", "the basemap update feature will report unavailable")
		}
		srv.BasemapJobs = jobs
	}

	// OIDC sessions live in the database the same way, and are wired
	// unconditionally like Links/Settings above — a mode-none or mode-proxy
	// deployment simply never reads from this table. Auth needs it too, since
	// identifying a mode-oidc request means looking a session cookie up.
	sessionStore, err := sessions.UseDB(src.Conn(), src.DSN(), box)
	if err != nil {
		return err
	}
	srv.Sessions = sessionStore
	srv.Box = box
	authenticator.UseSessions(sessionStore)

	if cfg.Auth.Mode == auth.ModeOIDC {
		// Unlike Komoot/Garmin, mode oidc cannot run degraded without a
		// client secret — there is no "the sign-in button is just hidden"
		// fallback when the mode itself is what the operator asked for, so
		// this fails startup rather than warning.
		clientSecret := os.Getenv(oidcflow.EnvClientSecret)
		if clientSecret == "" {
			return fmt.Errorf("auth.mode is oidc but %s is not set", oidcflow.EnvClientSecret)
		}

		// Bounded so a DNS hiccup against the issuer does not hang `serve`
		// forever — this is the one network call anything in auth.mode oidc
		// makes before the server can start answering requests at all.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		oidcCfg := authenticator.OIDC()
		flow, err := oidcflow.New(ctx, oidcflow.Config{
			Issuer:       oidcCfg.Issuer,
			ClientID:     oidcCfg.ClientID,
			ClientSecret: clientSecret,
			GroupsClaim:  oidcCfg.GroupsClaim,
			Scopes:       oidcCfg.Scopes,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("oidc discovery: %w", err)
		}
		srv.OIDC = flow
		log.Info("oidc discovery complete", "issuer", oidcCfg.Issuer)

		// The admin People page — optional, same shape as Komoot/Garmin's
		// own credentials: absent means the page reports "not configured"
		// rather than failing startup, since inviting people is not core to
		// serving routes the way the sign-in flow above is. Only reachable
		// under mode oidc in the first place: SendInviteEmail and the
		// Management API token exchange both need this issuer and this
		// client, neither of which mode proxy's own auth block populates.
		mgmtClientID := os.Getenv("DOMESTIQUE_AUTH0_MGMT_CLIENT_ID")
		mgmtClientSecret := os.Getenv("DOMESTIQUE_AUTH0_MGMT_CLIENT_SECRET")
		switch {
		case mgmtClientID == "" || mgmtClientSecret == "":
			log.Info("no auth0 management api credentials in the environment",
				"hint", "an admin can invite riders once DOMESTIQUE_AUTH0_MGMT_CLIENT_ID and "+
					"DOMESTIQUE_AUTH0_MGMT_CLIENT_SECRET are set")
		default:
			domain, err := managementDomain(oidcCfg.Issuer)
			if err != nil {
				return fmt.Errorf("deriving the management api domain from auth.oidc.issuer: %w", err)
			}
			srv.People = auth0mgmt.New(auth0mgmt.Config{
				Domain:         domain,
				ClientID:       mgmtClientID,
				ClientSecret:   mgmtClientSecret,
				SignInClientID: oidcCfg.ClientID,
			})
			log.Info("auth0 management api access configured", "domain", domain)
		}
	}

	// Garmin needs no config of its own: a rider signs in from the UI, and the
	// OAuth1 consumer comes either from the environment or from an admin
	// pasting it into Settings. Wiring the connector unconditionally is what
	// makes the "not set up yet" message reachable — a nil connector would
	// look the same as a deployment that never wanted Garmin at all.
	srv.Garmin = api.LiveGarmin{Log: log.Warn}
	if _, _, ok := garmin.ConsumerFromEnv(); !ok {
		log.Info("no garmin OAuth1 consumer in the environment",
			"hint", "an admin can set one in Settings, or set "+
				garmin.EnvConsumerKey+" and "+garmin.EnvConsumerSecret)
	}

	// Wahoo needs its own client id/secret from the environment (Vault, in a
	// cluster — the same rule as every other credential) and a redirect URL
	// from config, since it must equal exactly what is registered with
	// Wahoo. Riders connect their own account from Settings the same way
	// they do for Garmin/Komoot, just via a redirect instead of a password
	// form.
	if clientID, clientSecret := os.Getenv("WAHOO_CLIENT_ID"), os.Getenv("WAHOO_CLIENT_SECRET"); clientID != "" && clientSecret != "" {
		if cfg.Wahoo.RedirectURL == "" {
			return errors.New("WAHOO_CLIENT_ID is set but wahoo.redirect_url is not configured")
		}
		srv.Wahoo = wahoo.New(wahoo.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  cfg.Wahoo.RedirectURL,
		})
		log.Info("wahoo cloud api configured", "redirect_url", cfg.Wahoo.RedirectURL)
	} else {
		log.Info("no wahoo client credentials in the environment",
			"hint", "set WAHOO_CLIENT_ID and WAHOO_CLIENT_SECRET to let riders connect Wahoo")
	}

	if cfg.Komoot.Enabled {
		switch {
		case os.Getenv("KOMOOT_EMAIL") == "" || os.Getenv("KOMOOT_PASSWORD") == "":
			// No deployment-wide account configured — completely normal, and
			// not a problem, when riders sign in to their own Komoot account
			// from Settings instead. Same shape and level as the Garmin
			// OAuth1 consumer message just above: informational, not a
			// warning, because nothing here failed.
			log.Info("no deployment-wide komoot account in the environment",
				"hint", "riders can sign in to their own Komoot account from Settings instead")
		default:
			// Unlike the case above, credentials were actually supplied —
			// an operator configured this on purpose, so a login failure
			// here is worth a warning. Komoot is still optional: losing it
			// must not stop the app serving routes that are already here.
			client, err := komootClient()
			if err != nil {
				log.Warn("deployment-wide komoot account failed to sign in", "err", err)
			} else {
				srv.Komoot = client
				log.Info("komoot import enabled", "account", client.DisplayName())
			}
		}
	}

	if !authenticator.Enabled() {
		log.Warn("running without authentication — every visitor is an admin",
			"hint", "set auth.mode: proxy behind Authelia before exposing this")
	}

	// #nosec G703 -- webDir is an operator-supplied flag, not user input.
	if info, err := os.Stat(webDir); err == nil && info.IsDir() {
		srv.WebFS = os.DirFS(webDir)
		log.Info("serving web UI", "dir", webDir)
	} else {
		log.Warn("no built frontend found, serving API only", "dir", webDir,
			"hint", "run `just build-web`")
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// SIGTERM is what a pod restart actually sends. Without catching it, the
	// process just dies where it stands — dropping in-flight requests and,
	// now that there is a batch trace exporter, whatever spans it was still
	// holding since the last export tick.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("http server shutdown", "err", err)
		}
	}()

	// Polls every connected rider's Wahoo/Komoot/Garmin for new routes and,
	// if auto-sync is on, imports and pushes them out — the unattended half
	// of auto-sync, same shutdown signal as the HTTP server above.
	go srv.RunAutoImportLoop(ctx)

	log.Info("listening", "addr", addr, "library", src.Describe(),
		"auth", authenticator.Mode())
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdownTraces(flushCtx); err != nil {
		log.Warn("telemetry shutdown", "err", err)
	}
	return nil
}

// describeStore names where state lives, for the startup log.
func describeStore(store state.Store) string {
	if d, ok := store.(interface{ Describe() string }); ok {
		return d.Describe()
	}
	return "file"
}

func printPlan(plan model.Plan) {
	changes := plan.Changes()
	if len(changes) == 0 {
		fmt.Printf("everything up to date (%d route/account pair(s))\n", len(plan.Items))
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OP\tACCOUNT\tROUTE\tREASON")
	for _, item := range changes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", item.Op, item.AccountID, item.Slug, item.Reason)
	}
	w.Flush()

	fmt.Printf("\n%d change(s), %d already in sync\n", len(changes), len(plan.Items)-len(changes))
}

func reportProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	fmt.Fprintln(os.Stderr)
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "problem:", p)
	}
	return fmt.Errorf("%d problem(s) in the library", len(problems))
}
