// Stele synthetic seeder.
//
// Seeds master data (users, dealers, assignment rules), then generates
// synthetic fault cases via the fault package's command API. Each
// OpenCase invocation runs the routing resolver, so the seeded cases
// land with a realistic assignee distribution.
//
// USAGE:
//
//	stele-seed -count 200 [-clean] [-skip-master]
//
// Reads STELE_DATABASE_URL from the environment.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/auth"
	"github.com/Th3r4c3r/stele/internal/dealer"
	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/migrations"
)

// DevPassword is the dev/seed password assigned to every seeded user.
// Echoed to stderr at seed time so it cannot be silently forgotten.
const DevPassword = "stele-dev-2026"

// Master data definitions — fully synthetic.

var seedUsers = []user.User{
	{Email: "yan@stele.local", Name: "Yan", Role: "admin"},
	{Email: "mario.bms@stele.local", Name: "Mario Bossi", Role: "battery_specialist",
		Specializations: []string{"BMS_", "CHARGER_"}},
	{Email: "ana.motor@stele.local", Name: "Ana Motor", Role: "motor_specialist",
		Specializations: []string{"MOTOR_"}},
	{Email: "jp.es@stele.local", Name: "JP Iberia", Role: "regional_ops",
		Region: ptr("ES")},
	{Email: "kris.de@stele.local", Name: "Kris Bauer", Role: "regional_ops",
		Region: ptr("DE")},
}

func ptr[T any](v T) *T { return &v }

var seedDealers = []dealer.Dealer{
	{Code: "DEALER_01", Name: "Milano EV Center", Region: "IT", Country: "IT"},
	{Code: "DEALER_02", Name: "Torino Mobility", Region: "IT", Country: "IT"},
	{Code: "DEALER_03", Name: "Bologna Two-Wheels", Region: "IT", Country: "IT"},
	{Code: "DEALER_04", Name: "Roma EV Hub", Region: "IT", Country: "IT"},
	{Code: "DEALER_05", Name: "Paris VE Service", Region: "FR", Country: "FR"},
	{Code: "DEALER_06", Name: "Lyon Mobility", Region: "FR", Country: "FR"},
	{Code: "DEALER_07", Name: "Marseille Scoot", Region: "FR", Country: "FR"},
	{Code: "DEALER_08", Name: "Madrid Mobilidad", Region: "ES", Country: "ES"},
	{Code: "DEALER_09", Name: "Barcelona Moto-E", Region: "ES", Country: "ES"},
	{Code: "DEALER_10", Name: "Valencia EV", Region: "ES", Country: "ES"},
	{Code: "DEALER_11", Name: "Berlin E-Mobil", Region: "DE", Country: "DE"},
	{Code: "DEALER_12", Name: "Munich Roller", Region: "DE", Country: "DE"},
}

// Rule specs: (name, priority, fault prefix, region, assignee email).
var seedRules = []struct {
	Name     string
	Priority int
	Prefix   string
	Region   string
	Email    string
}{
	{"battery faults to mario", 10, "BMS_", "", "mario.bms@stele.local"},
	{"motor faults to ana", 20, "MOTOR_", "", "ana.motor@stele.local"},
	{"spanish dealers to jp", 30, "", "ES", "jp.es@stele.local"},
	{"german dealers to kris", 40, "", "DE", "kris.de@stele.local"},
}

var faultCodes = []string{
	"BMS_FAULT_01", "BMS_FAULT_03", "BMS_OVERTEMP",
	"MOTOR_ENC_LOSS", "MOTOR_OVERHEAT",
	"DASH_NO_BOOT", "DASH_BACKLIGHT",
	"CHARGER_NO_AC", "CHARGER_CC_FAIL",
	"BRAKE_REGEN_FAIL", "FRAME_WELD_INSPECT",
}

var descriptions = []string{
	"Customer reports intermittent fault during normal operation.",
	"Issue observed after first full charge cycle.",
	"Vehicle returned by customer after 30-day cool-off.",
	"Dealer inspection during scheduled service.",
	"Fault triggered safety mode; vehicle would not start.",
	"Customer reports performance degradation over time.",
	"Issue reproduced on bench test by service technician.",
	"Field report from group ride; multiple units affected.",
}

var noteAuthors = []string{"yan", "system", "service_team", "ops"}

var noteSnippets = []string{
	"Initial diagnosis attempted, awaiting parts.",
	"Parts ordered, ETA 7 business days.",
	"Replaced suspected component, fault persists.",
	"Escalated to engineering for root-cause analysis.",
	"Customer notified of expected timeline.",
	"Bench test confirms intermittent behaviour.",
	"Firmware reflashed to latest stable version.",
	"Dealer reports no further occurrences after fix.",
}

var reasoningByKind = map[string][]string{
	fault.KindWarranty: {
		"Component within 24-month warranty window.",
		"Standard manufacturer defect, claim approved.",
		"Within warranty mileage and time limits.",
	},
	fault.KindOutOfWarranty: {
		"Vehicle out of warranty window by more than 90 days.",
		"Mileage exceeded warranty cap; paid repair quoted.",
		"Second-hand owner without transferable warranty.",
	},
	fault.KindGoodwill: {
		"Out of warranty by 2 weeks; goodwill approved by service manager.",
		"Loyal customer; goodwill repair authorised.",
		"Defect bordering known issue; goodwill agreed.",
	},
	fault.KindRecall: {
		"Matches active BMS recall campaign 2025-04.",
		"VIN range within charger recall scope.",
		"Frame inspection recall applies to this unit.",
	},
	fault.KindUnrelated: {
		"User-installed third-party accessory caused the fault.",
		"Crash damage, not a manufacturing defect.",
		"Not reproducible after extensive testing; closed pending recurrence.",
	},
	fault.KindCustomerEducation: {
		"Product working as designed; user expected different behaviour.",
		"Charging time consistent with spec; education provided.",
		"Range estimate matches spec under reported conditions.",
	},
}

var resolutions = []string{
	"Replaced component under warranty.",
	"Reflashed firmware; no further reports.",
	"Replaced charger assembly; closed.",
	"Could not reproduce after extended test; closed pending recurrence.",
	"Part repaired; customer satisfied.",
	"Customer educated on correct usage; case closed.",
	"Recall remedy applied per campaign instructions.",
}

var kindWeights = []struct {
	kind   string
	weight int
}{
	{fault.KindWarranty, 35},
	{fault.KindOutOfWarranty, 20},
	{fault.KindCustomerEducation, 15},
	{fault.KindUnrelated, 15},
	{fault.KindGoodwill, 10},
	{fault.KindRecall, 5},
}

var vinAlphabet = []rune("ABCDEFGHJKLMNPRSTUVWXYZ0123456789")

func randomVIN(r *rand.Rand) string {
	var sb strings.Builder
	sb.Grow(17)
	for i := 0; i < 17; i++ {
		sb.WriteRune(vinAlphabet[r.IntN(len(vinAlphabet))])
	}
	return sb.String()
}

func pick[T any](r *rand.Rand, xs []T) T {
	return xs[r.IntN(len(xs))]
}

func pickKind(r *rand.Rand) string {
	const total = 100
	x := r.IntN(total)
	cum := 0
	for _, kw := range kindWeights {
		cum += kw.weight
		if x < cum {
			return kw.kind
		}
	}
	return kindWeights[len(kindWeights)-1].kind
}

func main() {
	count := flag.Int("count", 200, "number of cases to generate")
	clean := flag.Bool("clean", false, "wipe events + projections first (dev only)")
	skipMaster := flag.Bool("skip-master", false, "skip seeding users/dealers/rules (use when only re-seeding cases)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))
	slog.SetDefault(logger)

	dbURL := os.Getenv("STELE_DATABASE_URL")
	if dbURL == "" {
		slog.Error("STELE_DATABASE_URL not set")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := migrate.Up(migrations.FS, dbURL); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *clean {
		slog.Info("cleaning event log + projections (dev)")
		_, err := pool.Exec(ctx, `
			SET session_replication_role = replica;
			TRUNCATE events, projection_cursors, projection_event_counts, current_cases;
			SET session_replication_role = origin;
		`)
		if err != nil {
			slog.Error("clean", "err", err)
			os.Exit(1)
		}
	}

	userRepo := user.NewRepo(pool)
	dealerRepo := dealer.NewRepo(pool)
	resolver := fault.NewPgResolver(pool)

	if !*skipMaster {
		slog.Info("seeding master data")
		if err := seedMaster(ctx, userRepo, dealerRepo, resolver); err != nil {
			slog.Error("master", "err", err)
			os.Exit(1)
		}
	}

	yan, err := userRepo.ByEmail(ctx, "yan@stele.local")
	if err != nil {
		slog.Error("resolve yan", "err", err)
		os.Exit(1)
	}

	store := event.NewPostgresStore(pool)
	r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xCAFEBABE))

	start := time.Now()
	totalEvents := 0
	for i := 0; i < *count; i++ {
		_, evs, err := generateCase(ctx, store, resolver, yan.ID, r)
		if err != nil {
			slog.Error("generate", "i", i, "err", err)
			os.Exit(1)
		}
		totalEvents += evs
		if (i+1)%25 == 0 {
			slog.Info("progress", "cases", i+1, "events_total", totalEvents)
		}
	}
	slog.Info("seed complete",
		"cases", *count,
		"events_total", totalEvents,
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
}

func seedMaster(ctx context.Context, ur *user.Repo, dr *dealer.Repo, res *fault.PgResolver) error {
	devHash, err := auth.HashPassword(DevPassword)
	if err != nil {
		return fmt.Errorf("hash dev password: %w", err)
	}
	slog.Warn("seeded dev password",
		"password", DevPassword,
		"hint", "all seeded users share this password; change after first login")
	for _, u := range seedUsers {
		if err := ur.Upsert(ctx, u); err != nil {
			return fmt.Errorf("user %s: %w", u.Email, err)
		}
		// Upsert preserves an existing password_hash if u.PasswordHash is empty.
		// Force the dev password on every seed (idempotent).
		full, err := ur.ByEmail(ctx, u.Email)
		if err != nil {
			return fmt.Errorf("resolve %s after upsert: %w", u.Email, err)
		}
		if err := ur.SetPassword(ctx, full.ID, devHash); err != nil {
			return fmt.Errorf("set dev password for %s: %w", u.Email, err)
		}
	}
	for _, d := range seedDealers {
		if err := dr.Upsert(ctx, d); err != nil {
			return fmt.Errorf("dealer %s: %w", d.Code, err)
		}
	}
	emailToID := map[string]uuid.UUID{}
	for _, u := range seedUsers {
		full, err := ur.ByEmail(ctx, u.Email)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", u.Email, err)
		}
		emailToID[u.Email] = full.ID
	}
	for _, spec := range seedRules {
		assignee, ok := emailToID[spec.Email]
		if !ok {
			return fmt.Errorf("rule '%s' references unknown user %s", spec.Name, spec.Email)
		}
		if err := res.UpsertRule(ctx, fault.Rule{
			Name:              spec.Name,
			Priority:          spec.Priority,
			MatchFaultPrefix:  spec.Prefix,
			MatchDealerRegion: spec.Region,
			AssigneeID:        assignee,
		}); err != nil {
			return fmt.Errorf("rule '%s': %w", spec.Name, err)
		}
	}
	return nil
}

func generateCase(ctx context.Context, store *event.PostgresStore, resolver fault.Resolver, openerID uuid.UUID, r *rand.Rand) (uuid.UUID, int, error) {
	dealerCode := seedDealers[r.IntN(len(seedDealers))].Code
	fc := pick(r, faultCodes)
	desc := pick(r, descriptions)

	id, err := fault.OpenCase(ctx, store, resolver, openerID, fault.CaseOpened{
		Dealer: dealerCode, VIN: randomVIN(r), FaultCode: fc, Description: desc,
	})
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("open: %w", err)
	}
	events := 2 // CaseOpened + CaseAssigned auto-emitted

	noteCount := 1 + r.IntN(5)
	for i := 0; i < noteCount; i++ {
		err := fault.AddNote(ctx, store, id, fault.NoteAdded{
			Author: pick(r, noteAuthors),
			Text:   pick(r, noteSnippets),
		})
		if err != nil {
			return id, events, fmt.Errorf("note: %w", err)
		}
		events++
	}

	if r.Float64() < 0.80 {
		k := pickKind(r)
		err := fault.Classify(ctx, store, id, fault.Classified{
			Kind: k, Reasoning: pick(r, reasoningByKind[k]),
		})
		if err != nil {
			return id, events, fmt.Errorf("classify: %w", err)
		}
		events++
	}

	if r.Float64() < 0.90 {
		err := fault.CloseCase(ctx, store, id, fault.CaseClosed{
			Resolution: pick(r, resolutions),
			ClosedBy:   pick(r, noteAuthors),
		})
		if err != nil {
			return id, events, fmt.Errorf("close: %w", err)
		}
		events++
	}
	return id, events, nil
}
