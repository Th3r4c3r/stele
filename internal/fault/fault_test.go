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

// stubResolver always assigns to the opener, no rules.
type stubResolver struct{}

func (stubResolver) ResolveForOpen(_ context.Context, in RouteInput) (Decision, error) {
	return Decision{AssigneeID: in.OpenerID, Reason: ReasonOpener}, nil
}

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
		TRUNCATE assignment_rules, users, dealers CASCADE;
		SET session_replication_role = origin;
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool, event.NewPostgresStore(pool)
}

func TestRoutePure(t *testing.T) {
	mario := uuid.Must(uuid.NewV7())
	jp := uuid.Must(uuid.NewV7())
	opener := uuid.Must(uuid.NewV7())
	rules := []Rule{
		{Name: "bms", Priority: 10, MatchFaultPrefix: "BMS_", AssigneeID: mario},
		{Name: "es", Priority: 30, MatchDealerRegion: "ES", AssigneeID: jp},
	}
	cases := []struct {
		name string
		in   RouteInput
		want Decision
	}{
		{"bms wins on fault prefix",
			RouteInput{FaultCode: "BMS_FAULT_03", DealerRegion: "ES", OpenerID: opener},
			Decision{AssigneeID: mario, Reason: ReasonRuleFaultPrefix, RuleName: "bms"}},
		{"ES region applies when no fault rule matches",
			RouteInput{FaultCode: "DASH_NO_BOOT", DealerRegion: "ES", OpenerID: opener},
			Decision{AssigneeID: jp, Reason: ReasonRuleDealerRegion, RuleName: "es"}},
		{"no rule -> opener",
			RouteInput{FaultCode: "DASH_NO_BOOT", DealerRegion: "FR", OpenerID: opener},
			Decision{AssigneeID: opener, Reason: ReasonOpener}},
		{"opener required is enforced by the caller (Route assumes set)",
			RouteInput{FaultCode: "X", DealerRegion: "", OpenerID: opener},
			Decision{AssigneeID: opener, Reason: ReasonOpener}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Route(tc.in, rules)
			if got != tc.want {
				t.Fatalf("Route mismatch:\n got  %+v\n want %+v", got, tc.want)
			}
		})
	}
}

func TestOpenCaseEmitsCaseAssigned(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	opener := uuid.Must(uuid.NewV7())

	id, err := OpenCase(ctx, store, stubResolver{}, opener, CaseOpened{
		Dealer: "DEALER_01", VIN: "ABCDEFGHJKLMN1234", FaultCode: "BMS_FAULT_03",
		Description: "Battery does not charge past 80%",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Two events should exist: CaseOpened + CaseAssigned, both on this aggregate.
	rows, err := pool.Query(ctx, `SELECT type FROM events WHERE aggregate_id = $1 ORDER BY id`, id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		types = append(types, s)
	}
	if len(types) != 2 || types[0] != EventCaseOpened || types[1] != EventCaseAssigned {
		t.Fatalf("expected [CaseOpened CaseAssigned], got %v", types)
	}
}

func TestOpenCaseValidation(t *testing.T) {
	pool, store := requirePostgres(t)
	_ = pool
	ctx := context.Background()
	opener := uuid.Must(uuid.NewV7())
	_, err := OpenCase(ctx, store, stubResolver{}, opener, CaseOpened{
		Dealer: "", VIN: "12345678901234567", FaultCode: "F", Description: "x",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("empty dealer: got %v", err)
	}
	_, err = OpenCase(ctx, store, stubResolver{}, opener, CaseOpened{
		Dealer: "D", VIN: "SHORT", FaultCode: "F", Description: "x",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("short VIN: got %v", err)
	}
	_, err = OpenCase(ctx, store, stubResolver{}, uuid.Nil, CaseOpened{
		Dealer: "D", VIN: "AAAAAAAAAAAAAAAAA", FaultCode: "F", Description: "d",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("missing opener: got %v", err)
	}
}

func TestClassifyRejectsUnknownKind(t *testing.T) {
	pool, store := requirePostgres(t)
	_ = pool
	ctx := context.Background()
	opener := uuid.Must(uuid.NewV7())
	id, err := OpenCase(ctx, store, stubResolver{}, opener, CaseOpened{
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

func TestFullLifecycleWithAssignmentProjection(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	opener := uuid.Must(uuid.NewV7())

	id, err := OpenCase(ctx, store, stubResolver{}, opener, CaseOpened{
		Dealer: "DEALER_01", VIN: "ABCDEFGHJKLMN1234", FaultCode: "BMS_FAULT_03",
		Description: "Battery does not charge past 80%",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = AddNote(ctx, store, id, NoteAdded{Author: "yan", Text: "initial inspection"})
	if err := Classify(ctx, store, id, Classified{Kind: KindWarranty, Reasoning: "BMS within 24m"}); err != nil {
		t.Fatalf("classify: %v", err)
	}
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
	var assignee uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT status, kind, note_count, assignee_id
		FROM current_cases WHERE id = $1`, id,
	).Scan(&status, &kind, &noteCount, &assignee)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "closed" || kind != KindWarranty || noteCount != 1 || assignee != opener {
		t.Fatalf("got status=%s kind=%s notes=%d assignee=%s; want closed/warranty/1/opener",
			status, kind, noteCount, assignee)
	}
}

func TestReassignTransfersAssignee(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	yan := uuid.Must(uuid.NewV7())
	mario := uuid.Must(uuid.NewV7())

	id, err := OpenCase(ctx, store, stubResolver{}, yan, CaseOpened{
		Dealer: "DEALER_01", VIN: "ABCDEFGHJKLMN1234", FaultCode: "DASH_NO_BOOT", Description: "d",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Reassign(ctx, store, id, mario, &yan); err != nil {
		t.Fatalf("reassign: %v", err)
	}

	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentCasesProjector())
	if err := runner.RunOnce(ctx, "current_cases"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var assignee uuid.UUID
	pool.QueryRow(ctx, `SELECT assignee_id FROM current_cases WHERE id = $1`, id).Scan(&assignee)
	if assignee != mario {
		t.Fatalf("assignee after reassign: got %s want %s", assignee, mario)
	}

	// Same-assignee reassign is rejected.
	if err := Reassign(ctx, store, id, mario, &mario); !errors.Is(err, ErrValidation) {
		t.Fatalf("self-reassign should fail: %v", err)
	}
}

func TestReclassification(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	opener := uuid.Must(uuid.NewV7())
	id, _ := OpenCase(ctx, store, stubResolver{}, opener, CaseOpened{
		Dealer: "D", VIN: "AAAAAAAAAAAAAAAAA", FaultCode: "F", Description: "d",
	})
	_ = Classify(ctx, store, id, Classified{Kind: KindWarranty, Reasoning: "first call"})
	_ = Classify(ctx, store, id, Classified{Kind: KindCustomerEducation, Reasoning: "second look"})

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

func TestProjectorReplaySafeWithAssignment(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	opener := uuid.Must(uuid.NewV7())
	id, _ := OpenCase(ctx, store, stubResolver{}, opener, CaseOpened{
		Dealer: "D", VIN: "REPLAYTEST1234567", FaultCode: "F", Description: "d",
	})
	_ = AddNote(ctx, store, id, NoteAdded{Author: "a", Text: "n1"})
	_ = AddNote(ctx, store, id, NoteAdded{Author: "a", Text: "n2"})

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
