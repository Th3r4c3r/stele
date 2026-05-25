package event

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/migrations"
)

var migrateOnce sync.Once
var migrateErr error

func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("STELE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STELE_TEST_DATABASE_URL not set; skipping integration test")
	}
	migrateOnce.Do(func() {
		migrateErr = migrate.Up(migrations.FS, url)
	})
	if migrateErr != nil {
		t.Fatalf("migrate up: %v", migrateErr)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// Append-only trigger blocks plain TRUNCATE/DELETE; bypass with session_replication_role.
	if _, err := pool.Exec(context.Background(),
		`SET session_replication_role = replica; TRUNCATE events; SET session_replication_role = origin;`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func TestAppendAndLoad(t *testing.T) {
	pool := requirePostgres(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	agg := uuid.Must(uuid.NewV7())

	evs := []Event{
		{
			AggregateType: "claim",
			AggregateID:   agg,
			Type:          "ClaimOpened",
			Payload:       json.RawMessage(`{"dealer":"X"}`),
			OccurredAt:    time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		},
		{
			AggregateType: "claim",
			AggregateID:   agg,
			Type:          "NoteAdded",
			Payload:       json.RawMessage(`{"text":"hello"}`),
			OccurredAt:    time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC),
		},
	}
	if err := store.Append(ctx, evs); err != nil {
		t.Fatalf("append: %v", err)
	}
	for i, ev := range evs {
		if ev.ID == uuid.Nil {
			t.Fatalf("event %d: id not assigned", i)
		}
	}

	loaded, err := store.Load(ctx, agg, time.Time{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d events, want 2", len(loaded))
	}
	if loaded[0].Type != "ClaimOpened" || loaded[1].Type != "NoteAdded" {
		t.Fatalf("order: got %q,%q", loaded[0].Type, loaded[1].Type)
	}
	if loaded[0].RecordedAt.IsZero() {
		t.Fatalf("recorded_at not populated")
	}
	if loaded[0].RecordedBy != "system" {
		t.Fatalf("recorded_by: got %q want system", loaded[0].RecordedBy)
	}
}

func TestLoadSinceCursor(t *testing.T) {
	pool := requirePostgres(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	agg := uuid.Must(uuid.NewV7())

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	t3 := t2.Add(time.Hour)

	err := store.Append(ctx, []Event{
		{AggregateType: "claim", AggregateID: agg, Type: "a", OccurredAt: t1},
		{AggregateType: "claim", AggregateID: agg, Type: "b", OccurredAt: t2},
		{AggregateType: "claim", AggregateID: agg, Type: "c", OccurredAt: t3},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.Load(ctx, agg, t1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0].Type != "b" || got[1].Type != "c" {
		t.Fatalf("since=t1 want [b,c], got %v", typesOf(got))
	}
}

func TestStreamPagination(t *testing.T) {
	pool := requirePostgres(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	// Insert 25 events across 3 aggregates, two types.
	for i := 0; i < 25; i++ {
		ev := Event{
			AggregateType: "claim",
			AggregateID:   uuid.Must(uuid.NewV7()),
			Type:          "ClaimOpened",
			OccurredAt:    time.Now().Add(time.Duration(i) * time.Second),
		}
		if i%5 == 0 {
			ev.AggregateType = "vehicle"
			ev.Type = "VehicleRegistered"
		}
		if err := store.Append(ctx, []Event{ev}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	var claimSeen, vehicleSeen int
	for ev, err := range store.Stream(ctx, StreamOptions{BatchSize: 7}) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		switch ev.AggregateType {
		case "claim":
			claimSeen++
		case "vehicle":
			vehicleSeen++
		}
	}
	if claimSeen+vehicleSeen != 25 {
		t.Fatalf("total %d, want 25 (claim=%d, vehicle=%d)", claimSeen+vehicleSeen, claimSeen, vehicleSeen)
	}

	// Filter by type.
	var only int
	for ev, err := range store.Stream(ctx, StreamOptions{AggregateType: "vehicle"}) {
		if err != nil {
			t.Fatalf("stream filtered: %v", err)
		}
		if ev.AggregateType != "vehicle" {
			t.Fatalf("filter leaked: got %q", ev.AggregateType)
		}
		only++
	}
	if only != vehicleSeen {
		t.Fatalf("filtered got %d, want %d", only, vehicleSeen)
	}
}

func TestAppendOnlyEnforcedByDB(t *testing.T) {
	pool := requirePostgres(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	agg := uuid.Must(uuid.NewV7())
	if err := store.Append(ctx, []Event{
		{AggregateType: "claim", AggregateID: agg, Type: "x", OccurredAt: time.Now()},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	_, err := pool.Exec(ctx, `UPDATE events SET type = 'y' WHERE aggregate_id = $1`, agg)
	if err == nil {
		t.Fatalf("UPDATE succeeded, expected trigger to reject")
	}
	_, err = pool.Exec(ctx, `DELETE FROM events WHERE aggregate_id = $1`, agg)
	if err == nil {
		t.Fatalf("DELETE succeeded, expected trigger to reject")
	}
}

func typesOf(evs []Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}
