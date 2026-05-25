package fault

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

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
		TRUNCATE events, projection_cursors, projection_event_counts, current_cases;
		SET session_replication_role = origin;
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool, event.NewPostgresStore(pool)
}

func TestOpenCaseValidation(t *testing.T) {
	pool, store := requirePostgres(t)
	_ = pool
	ctx := context.Background()
	_, err := OpenCase(ctx, store, CaseOpened{Dealer: "", VIN: "12345678901234567", FaultCode: "F", Description: "x"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("empty dealer: got %v", err)
	}
	_, err = OpenCase(ctx, store, CaseOpened{Dealer: "D", VIN: "SHORT", FaultCode: "F", Description: "x"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("short VIN: got %v", err)
	}
}

func TestClassifyRejectsUnknownKind(t *testing.T) {
	pool, store := requirePostgres(t)
	_ = pool
	ctx := context.Background()
	id, err := OpenCase(ctx, store, CaseOpened{
		Dealer: "DEALER_01", VIN: "ABCDEFGHJKLMN1234", FaultCode: "BMS", Description: "d",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	err = Classify(ctx, store, id, Classified{Kind: "made_up_kind", Reasoning: "x"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown kind: got %v want ErrValidation", err)
	}
}

func TestFullLifecycleTriageClassifyClose(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()

	id, err := OpenCase(ctx, store, CaseOpened{
		Dealer: "DEALER_01", VIN: "ABCDEFGHJKLMN1234", FaultCode: "BMS_FAULT_03",
		Description: "Battery does not charge past 80%",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = AddNote(ctx, store, id, NoteAdded{Author: "yan", Text: "initial inspection"})
	if err := Classify(ctx, store, id, Classified{Kind: KindWarranty, Reasoning: "BMS within 24 months"}); err != nil {
		t.Fatalf("classify: %v", err)
	}
	_ = AddNote(ctx, store, id, NoteAdded{Author: "yan", Text: "parts ordered"})
	if err := CloseCase(ctx, store, id, CaseClosed{Resolution: "BMS replaced", ClosedBy: "yan"}); err != nil {
		t.Fatalf("close: %v", err)
	}

	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentCasesProjector())
	if err := runner.RunOnce(ctx, "current_cases"); err != nil {
		t.Fatalf("run: %v", err)
	}

	var status, kind string
	var noteCount int
	var hasClassifiedAt, hasClosedAt bool
	err = pool.QueryRow(ctx, `
		SELECT status, kind, note_count, classified_at IS NOT NULL, closed_at IS NOT NULL
		FROM current_cases WHERE id = $1`, id,
	).Scan(&status, &kind, &noteCount, &hasClassifiedAt, &hasClosedAt)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "closed" {
		t.Fatalf("status: got %q want closed", status)
	}
	if kind != KindWarranty {
		t.Fatalf("kind: got %q want %q", kind, KindWarranty)
	}
	if noteCount != 2 {
		t.Fatalf("note_count: got %d want 2", noteCount)
	}
	if !hasClassifiedAt || !hasClosedAt {
		t.Fatalf("timestamps: classified=%v closed=%v", hasClassifiedAt, hasClosedAt)
	}
}

func TestReclassification(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	id, _ := OpenCase(ctx, store, CaseOpened{
		Dealer: "D", VIN: "AAAAAAAAAAAAAAAAA", FaultCode: "F", Description: "d",
	})
	_ = Classify(ctx, store, id, Classified{Kind: KindWarranty, Reasoning: "first call"})
	_ = Classify(ctx, store, id, Classified{Kind: KindCustomerEducation, Reasoning: "after second look, works as designed"})

	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentCasesProjector())
	if err := runner.RunOnce(ctx, "current_cases"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var kind string
	pool.QueryRow(ctx, `SELECT kind FROM current_cases WHERE id = $1`, id).Scan(&kind)
	if kind != KindCustomerEducation {
		t.Fatalf("reclassify wins: got %q want %q", kind, KindCustomerEducation)
	}
}

func TestProjectorIsReplaySafe(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	id, _ := OpenCase(ctx, store, CaseOpened{
		Dealer: "D", VIN: "REPLAYTEST1234567", FaultCode: "F", Description: "d",
	})
	_ = AddNote(ctx, store, id, NoteAdded{Author: "a", Text: "n1"})
	_ = AddNote(ctx, store, id, NoteAdded{Author: "a", Text: "n2"})
	_ = Classify(ctx, store, id, Classified{Kind: KindGoodwill, Reasoning: "out of warranty by 2 weeks"})

	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentCasesProjector())
	_ = runner.RunOnce(ctx, "current_cases")
	var nc1 int
	pool.QueryRow(ctx, `SELECT note_count FROM current_cases WHERE id = $1`, id).Scan(&nc1)
	if nc1 != 2 {
		t.Fatalf("first run note_count: %d", nc1)
	}
	_ = runner.ResetCursor(ctx, "current_cases")
	_ = runner.RunOnce(ctx, "current_cases")
	var nc2 int
	pool.QueryRow(ctx, `SELECT note_count FROM current_cases WHERE id = $1`, id).Scan(&nc2)
	if nc2 != 2 {
		t.Fatalf("replay note_count: %d (idempotency broken)", nc2)
	}
}

// silence unused
var _ = uuid.Nil
