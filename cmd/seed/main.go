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
			TRUNCATE events, projection_cursors, projection_event_counts, current_cases, case_parts;
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

	// Load real VINs + parts from the masters (post ADR-013). If the
	// masters are empty (fresh DB pre-pilot-import) the seeder falls
	// back to synthetic VINs and skips part events: analytics queries
	// will still run, just return empty rows.
	realVINs, err := loadRealVINs(ctx, pool)
	if err != nil {
		slog.Error("load vins", "err", err)
		os.Exit(1)
	}
	realParts, err := loadRealParts(ctx, pool)
	if err != nil {
		slog.Error("load parts", "err", err)
		os.Exit(1)
	}
	slog.Info("masters loaded", "vins", len(realVINs), "parts", len(realParts))

	gen := caseGen{
		store: store, resolver: resolver, openerID: yan.ID,
		vins: realVINs, parts: realParts,
	}

	start := time.Now()
	totalEvents := 0
	for i := 0; i < *count; i++ {
		_, evs, err := gen.generate(ctx, r)
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

// partMaster carries the bits the seeder needs from the parts table.
type partMaster struct {
	PN       string
	Price    float64 // 0 when null in master
}

// caseGen bundles the dependencies a single case generation needs.
// Pulling them into a struct avoids the long parameter list as the
// seeder grows (note, classify, parts, close).
type caseGen struct {
	store    *event.PostgresStore
	resolver fault.Resolver
	openerID uuid.UUID
	vins     []string     // empty -> seeder uses random VINs
	parts    []partMaster // empty -> seeder skips part events
}

// pickVIN returns a real VIN from the master when available, otherwise
// a synthetic one. This is what makes M10 analytics queries (joining
// case.vin -> vehicles -> model) actually return rows.
func (g caseGen) pickVIN(r *rand.Rand) string {
	if len(g.vins) > 0 {
		return g.vins[r.IntN(len(g.vins))]
	}
	return randomVIN(r)
}

// kindToPartKind maps case classification to the closed enum allowed
// on PartReplaced.Kind. Returns ("", false) for kinds without cost
// attribution (recall handled via campaign, unrelated/education have
// no parts swapped).
func kindToPartKind(caseKind string) (string, bool) {
	switch caseKind {
	case fault.KindWarranty:
		return fault.PartKindWarranty, true
	case fault.KindGoodwill:
		return fault.PartKindGoodwill, true
	case fault.KindOutOfWarranty:
		return fault.PartKindOutOfWarranty, true
	default:
		return "", false
	}
}

func (g caseGen) generate(ctx context.Context, r *rand.Rand) (uuid.UUID, int, error) {
	dealerCode := seedDealers[r.IntN(len(seedDealers))].Code
	fc := pick(r, faultCodes)
	desc := pick(r, descriptions)

	id, err := fault.OpenCase(ctx, g.store, g.resolver, g.openerID, fault.CaseOpened{
		Dealer: dealerCode, VIN: g.pickVIN(r), FaultCode: fc, Description: desc,
	})
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("open: %w", err)
	}
	events := 2 // CaseOpened + CaseAssigned auto-emitted

	noteCount := 1 + r.IntN(5)
	for i := 0; i < noteCount; i++ {
		err := fault.AddNote(ctx, g.store, id, fault.NoteAdded{
			Author: pick(r, noteAuthors),
			Text:   pick(r, noteSnippets),
		})
		if err != nil {
			return id, events, fmt.Errorf("note: %w", err)
		}
		events++
	}

	var caseKind string
	if r.Float64() < 0.80 {
		caseKind = pickKind(r)
		err := fault.Classify(ctx, g.store, id, fault.Classified{
			Kind: caseKind, Reasoning: pick(r, reasoningByKind[caseKind]),
		})
		if err != nil {
			return id, events, fmt.Errorf("classify: %w", err)
		}
		events++
	}

	// Part events: only when the case is classified into a kind with
	// cost attribution AND the parts master is populated. Probability
	// 50% means roughly 0.80 * (warranty+goodwill+ofw share, ~0.65) *
	// 0.50 = ~26% of cases carry at least one part event. Enough to
	// stress analytics queries without flooding the timeline.
	if caseKind != "" && len(g.parts) > 0 && r.Float64() < 0.50 {
		evs, err := g.emitParts(ctx, id, caseKind, r)
		if err != nil {
			return id, events, fmt.Errorf("parts: %w", err)
		}
		events += evs
	}

	if r.Float64() < 0.90 {
		err := fault.CloseCase(ctx, g.store, id, fault.CaseClosed{
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

// emitParts appends 1-2 PartReplaced/PartQuoted events on the case.
// For out_of_warranty kinds we flip a coin between Replaced (customer
// paid, repair done) and Quoted (estimate only): both are realistic
// outcomes and let the analytics layer differentiate them.
func (g caseGen) emitParts(ctx context.Context, caseID uuid.UUID, caseKind string, r *rand.Rand) (int, error) {
	partKind, ok := kindToPartKind(caseKind)
	if !ok {
		return 0, nil
	}
	n := 1 + r.IntN(2) // 1 or 2 part lines per case
	events := 0
	for i := 0; i < n; i++ {
		p := g.parts[r.IntN(len(g.parts))]
		qty := 1 + r.IntN(2) // 1 or 2 units
		// Quote vs Replace: for out_of_warranty cases, 40% are just
		// quotes (customer never signed off). Warranty/goodwill are
		// always replacements.
		if partKind == fault.PartKindOutOfWarranty && r.Float64() < 0.40 {
			price := p.Price
			if price == 0 {
				price = 50 + r.Float64()*450 // fallback 50-500 EUR
			}
			amount := price * float64(qty)
			if err := fault.RecordPartQuoted(ctx, g.store, caseID, g.openerID, p.PN, qty, amount); err != nil {
				return events, err
			}
		} else {
			if err := fault.RecordPartReplaced(ctx, g.store, caseID, g.openerID, p.PN, qty, partKind, ""); err != nil {
				return events, err
			}
		}
		events++
	}
	return events, nil
}

// loadRealVINs pulls every VIN from the vehicles master. Empty slice
// when the master is empty (pre-pilot-import). The full list fits
// comfortably in RAM at pilot scale (~38k VINs ~ 700 KiB).
func loadRealVINs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT vin FROM vehicles`)
	if err != nil {
		return nil, fmt.Errorf("load vins: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// loadRealParts returns the PN + reference price for every row in the
// parts master. NULL price -> 0; emitParts falls back to a random
// range when it needs an amount for a quote.
func loadRealParts(ctx context.Context, pool *pgxpool.Pool) ([]partMaster, error) {
	rows, err := pool.Query(ctx, `SELECT pn, COALESCE(price_eur, 0) FROM parts`)
	if err != nil {
		return nil, fmt.Errorf("load parts: %w", err)
	}
	defer rows.Close()
	var out []partMaster
	for rows.Next() {
		var p partMaster
		if err := rows.Scan(&p.PN, &p.Price); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
