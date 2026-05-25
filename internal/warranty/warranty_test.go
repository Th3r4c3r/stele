package warranty

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/internal/projection"
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
		TRUNCATE events, projection_cursors, projection_event_counts, current_claims;
		SET session_replication_role = origin;
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool, event.NewPostgresStore(pool)
}

func TestOpenClaimValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	pool, store := requirePostgres(t)
	_ = pool
	ctx := context.Background()
	_, err := OpenClaim(ctx, store, ClaimOpened{
		Dealer: "", VIN: "12345678901234567", FaultCode: "F01", Description: "x",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("empty dealer: got %v want ErrValidation", err)
	}
	_, err = OpenClaim(ctx, store, ClaimOpened{
		Dealer: "D1", VIN: "SHORT", FaultCode: "F01", Description: "x",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("short VIN: got %v want ErrValidation", err)
	}
}

func TestFullClaimLifecycle(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()

	id, err := OpenClaim(ctx, store, ClaimOpened{
		Dealer: "DEALER_01", VIN: "ABCDEFGHJKLMN1234", FaultCode: "BMS_FAULT_03",
		Description: "Battery does not charge past 80%",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := AddNote(ctx, store, id, NoteAdded{Author: "yan", Text: "first note"}); err != nil {
		t.Fatalf("note 1: %v", err)
	}
	if err := AddNote(ctx, store, id, NoteAdded{Author: "yan", Text: "second note"}); err != nil {
		t.Fatalf("note 2: %v", err)
	}
	if err := CloseClaim(ctx, store, id, ClaimClosed{
		Resolution: "battery replaced under warranty", ClosedBy: "yan",
	}); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Run the projector to materialize the read model.
	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentClaimsProjector())
	if err := runner.RunOnce(ctx, "current_claims"); err != nil {
		t.Fatalf("run projector: %v", err)
	}

	row := pool.QueryRow(ctx,
		`SELECT status, dealer, vin, note_count, closed_at IS NOT NULL FROM current_claims WHERE id = $1`,
		id)
	var status, dealer, vin string
	var noteCount int
	var hasClosedAt bool
	if err := row.Scan(&status, &dealer, &vin, &noteCount, &hasClosedAt); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "closed" {
		t.Fatalf("status: got %q want closed", status)
	}
	if dealer != "DEALER_01" {
		t.Fatalf("dealer: got %q", dealer)
	}
	if vin != "ABCDEFGHJKLMN1234" {
		t.Fatalf("vin: got %q", vin)
	}
	if noteCount != 2 {
		t.Fatalf("note_count: got %d want 2", noteCount)
	}
	if !hasClosedAt {
		t.Fatalf("closed_at should be set")
	}
}

func TestProjectorIsReplaySafe(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()

	id, err := OpenClaim(ctx, store, ClaimOpened{
		Dealer: "DEALER_02", VIN: "REPLAYTEST1234567", FaultCode: "F",
		Description: "d",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = AddNote(ctx, store, id, NoteAdded{Author: "a", Text: "n1"})
	_ = AddNote(ctx, store, id, NoteAdded{Author: "a", Text: "n2"})
	_ = AddNote(ctx, store, id, NoteAdded{Author: "a", Text: "n3"})

	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentClaimsProjector())
	if err := runner.RunOnce(ctx, "current_claims"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	var nc1 int
	pool.QueryRow(ctx, `SELECT note_count FROM current_claims WHERE id = $1`, id).Scan(&nc1)
	if nc1 != 3 {
		t.Fatalf("first run note_count: got %d want 3", nc1)
	}

	if err := runner.ResetCursor(ctx, "current_claims"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := runner.RunOnce(ctx, "current_claims"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	var nc2 int
	pool.QueryRow(ctx, `SELECT note_count FROM current_claims WHERE id = $1`, id).Scan(&nc2)
	if nc2 != 3 {
		t.Fatalf("replay note_count: got %d want 3 (idempotency broken)", nc2)
	}
}

func TestCloseClaimIdempotent(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	id, _ := OpenClaim(ctx, store, ClaimOpened{
		Dealer: "D", VIN: "AAAAAAAAAAAAAAAAA", FaultCode: "F", Description: "d",
	})
	_ = CloseClaim(ctx, store, id, ClaimClosed{Resolution: "r1", ClosedBy: "u"})
	time.Sleep(2 * time.Millisecond) // ensure UUIDv7 monotonic
	_ = CloseClaim(ctx, store, id, ClaimClosed{Resolution: "r2", ClosedBy: "u"})

	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentClaimsProjector())
	if err := runner.RunOnce(ctx, "current_claims"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var status string
	pool.QueryRow(ctx, `SELECT status FROM current_claims WHERE id = $1`, id).Scan(&status)
	if status != "closed" {
		t.Fatalf("status: got %q", status)
	}
	// The second close event exists in the log but did not corrupt the row.
	var closeCount int
	pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE aggregate_id = $1 AND type = $2`,
		id, EventClaimClosed).Scan(&closeCount)
	if closeCount != 2 {
		t.Fatalf("close events: got %d want 2", closeCount)
	}
}

// silence unused
var _ = uuid.Nil
