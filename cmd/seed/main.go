// Stele synthetic seeder.
//
// Generates synthetic fault cases via the fault package's command API
// with a realistic kind distribution. Useful for stress-testing the
// projection runner and exercising the UI on a non-trivial volume.
//
// USAGE:
//
//	stele-seed -count 200 [-clean]
//
// Reads STELE_DATABASE_URL from the environment. The -clean flag wipes
// events + projection tables first (dev only; production has the
// append-only trigger which the script bypasses with
// session_replication_role=replica).
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

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/migrations"
)

var dealers = []string{
	"DEALER_01", "DEALER_02", "DEALER_03", "DEALER_04",
	"DEALER_05", "DEALER_06", "DEALER_07", "DEALER_08",
	"DEALER_09", "DEALER_10", "DEALER_11", "DEALER_12",
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

var authors = []string{"yan", "system", "service_team", "ops"}

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

// reasoningByKind seeds plausible reasoning strings for each kind, so
// the timeline isn't all "lorem ipsum". Domain-flavoured.
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

// pickKind returns a kind drawn from the cumulative distribution given
// by the kindWeights slice. The numbers sum to 100.
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

var kindWeights = []struct {
	kind   string
	weight int // out of 100
}{
	{fault.KindWarranty, 35},
	{fault.KindOutOfWarranty, 20},
	{fault.KindCustomerEducation, 15},
	{fault.KindUnrelated, 15},
	{fault.KindGoodwill, 10},
	{fault.KindRecall, 5},
}

func main() {
	count := flag.Int("count", 200, "number of cases to generate")
	clean := flag.Bool("clean", false, "wipe events + projections first (dev only)")
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
		slog.Info("cleaning tables (dev)")
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

	store := event.NewPostgresStore(pool)
	r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xCAFEBABE))

	start := time.Now()
	totalEvents := 0
	for i := 0; i < *count; i++ {
		_, evs, err := generateCase(ctx, store, r)
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

// generateCase: open, 1-5 notes, 80% Classified, 90% Closed
// (uniformly across the population; not all branches matter).
func generateCase(ctx context.Context, store *event.PostgresStore, r *rand.Rand) (uuid.UUID, int, error) {
	dealer := pick(r, dealers)
	fc := pick(r, faultCodes)
	desc := pick(r, descriptions)

	id, err := fault.OpenCase(ctx, store, fault.CaseOpened{
		Dealer: dealer, VIN: randomVIN(r), FaultCode: fc, Description: desc,
	})
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("open: %w", err)
	}
	events := 1

	noteCount := 1 + r.IntN(5)
	for i := 0; i < noteCount; i++ {
		err := fault.AddNote(ctx, store, id, fault.NoteAdded{
			Author: pick(r, authors),
			Text:   pick(r, noteSnippets),
		})
		if err != nil {
			return id, events, fmt.Errorf("note: %w", err)
		}
		events++
	}

	classify := r.Float64() < 0.80
	var lastKind string
	if classify {
		lastKind = pickKind(r)
		err := fault.Classify(ctx, store, id, fault.Classified{
			Kind:      lastKind,
			Reasoning: pick(r, reasoningByKind[lastKind]),
		})
		if err != nil {
			return id, events, fmt.Errorf("classify: %w", err)
		}
		events++
	}

	if r.Float64() < 0.90 {
		err := fault.CloseCase(ctx, store, id, fault.CaseClosed{
			Resolution: pick(r, resolutions),
			ClosedBy:   pick(r, authors),
		})
		if err != nil {
			return id, events, fmt.Errorf("close: %w", err)
		}
		events++
	}
	return id, events, nil
}
