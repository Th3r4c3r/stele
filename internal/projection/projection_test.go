package projection

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/migrations"
)

var migrateOnce sync.Once
var migrateErr error

func requirePostgres(t *testing.T) (*pgxpool.Pool, *event.PostgresStore) {
	t.Helper()
	url := os.Getenv("STELE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STELE_TEST_DATABASE_URL not set; skipping integration test")
	}
	migrateOnce.Do(func() { migrateErr = migrate.Up(migrations.FS, url) })
	if migrateErr != nil {
		t.Fatalf("migrate up: %v", migrateErr)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `
		SET session_replication_role = replica;
		TRUNCATE events, projection_cursors, projection_event_counts;
		SET session_replication_role = origin;
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool, event.NewPostgresStore(pool)
}

func TestProjectorAdvancesCursorAndCounts(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()

	// 5 claim events, 3 vehicle events.
	var evs []event.Event
	for i := 0; i < 5; i++ {
		evs = append(evs, event.Event{
			AggregateType: "claim",
			AggregateID:   uuid.Must(uuid.NewV7()),
			Type:          "ClaimOpened",
			OccurredAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	for i := 0; i < 3; i++ {
		evs = append(evs, event.Event{
			AggregateType: "vehicle",
			AggregateID:   uuid.Must(uuid.NewV7()),
			Type:          "VehicleRegistered",
			OccurredAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	if err := store.Append(ctx, evs); err != nil {
		t.Fatalf("append: %v", err)
	}

	runner := NewRunner(store, pool)
	runner.Register(EventCountByType())

	if err := runner.RunOnce(ctx, "event_count_by_type"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	counts := readCounts(t, pool, ctx)
	if counts["claim/ClaimOpened"] != 5 {
		t.Fatalf("claim/ClaimOpened: got %d want 5", counts["claim/ClaimOpened"])
	}
	if counts["vehicle/VehicleRegistered"] != 3 {
		t.Fatalf("vehicle/VehicleRegistered: got %d want 3", counts["vehicle/VehicleRegistered"])
	}

	// Cursor should be at the most-recent event.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_cursors WHERE name = $1`,
		"event_count_by_type").Scan(&n); err != nil {
		t.Fatalf("count cursors: %v", err)
	}
	if n != 1 {
		t.Fatalf("cursor row count: got %d want 1", n)
	}
}

func TestProjectorIdempotentOnReplay(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if err := store.Append(ctx, []event.Event{{
			AggregateType: "claim",
			AggregateID:   uuid.Must(uuid.NewV7()),
			Type:          "ClaimOpened",
			OccurredAt:    time.Now().Add(time.Duration(i) * time.Second),
		}}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	runner := NewRunner(store, pool)
	runner.Register(EventCountByType())

	if err := runner.RunOnce(ctx, "event_count_by_type"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := readCounts(t, pool, ctx)["claim/ClaimOpened"]; got != 4 {
		t.Fatalf("first run count: got %d want 4", got)
	}

	// Replay: reset cursor, re-run. Counts must stay at 4 (idempotent
	// per-row via last_event_id guard).
	if err := runner.ResetCursor(ctx, "event_count_by_type"); err != nil {
		t.Fatalf("ResetCursor: %v", err)
	}
	if err := runner.RunOnce(ctx, "event_count_by_type"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := readCounts(t, pool, ctx)["claim/ClaimOpened"]; got != 4 {
		t.Fatalf("replay count: got %d want 4 (idempotency broken)", got)
	}
}

func TestRunnerIncrementalAdvance(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()

	if err := store.Append(ctx, []event.Event{
		{AggregateType: "claim", AggregateID: uuid.Must(uuid.NewV7()), Type: "ClaimOpened", OccurredAt: time.Now()},
		{AggregateType: "claim", AggregateID: uuid.Must(uuid.NewV7()), Type: "ClaimOpened", OccurredAt: time.Now().Add(time.Second)},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	runner := NewRunner(store, pool)
	runner.Register(EventCountByType())
	if err := runner.RunOnce(ctx, "event_count_by_type"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := readCounts(t, pool, ctx)["claim/ClaimOpened"]; got != 2 {
		t.Fatalf("after first: got %d want 2", got)
	}

	// Append more, run again WITHOUT resetting. Cursor picks up where it stopped.
	if err := store.Append(ctx, []event.Event{
		{AggregateType: "claim", AggregateID: uuid.Must(uuid.NewV7()), Type: "ClaimOpened", OccurredAt: time.Now().Add(2 * time.Second)},
		{AggregateType: "claim", AggregateID: uuid.Must(uuid.NewV7()), Type: "ClaimClosed", OccurredAt: time.Now().Add(3 * time.Second)},
	}); err != nil {
		t.Fatalf("append more: %v", err)
	}
	if err := runner.RunOnce(ctx, "event_count_by_type"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	counts := readCounts(t, pool, ctx)
	if counts["claim/ClaimOpened"] != 3 {
		t.Fatalf("opened after second: got %d want 3", counts["claim/ClaimOpened"])
	}
	if counts["claim/ClaimClosed"] != 1 {
		t.Fatalf("closed after second: got %d want 1", counts["claim/ClaimClosed"])
	}
}

func readCounts(t *testing.T, pool *pgxpool.Pool, ctx context.Context) map[string]int64 {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT aggregate_type, type, count FROM projection_event_counts`)
	if err != nil {
		t.Fatalf("read counts: %v", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var agg, typ string
		var n int64
		if err := rows.Scan(&agg, &typ, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[agg+"/"+typ] = n
	}
	return out
}
