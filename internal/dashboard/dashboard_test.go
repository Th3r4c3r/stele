package dashboard

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/internal/projection"
	"github.com/Th3r4c3r/stele/migrations"
)

var migrateOnce sync.Once
var migrateErr error

func setup(t *testing.T) (*pgxpool.Pool, *event.PostgresStore, uuid.UUID) {
	t.Helper()
	url := os.Getenv("STELE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STELE_TEST_DATABASE_URL not set")
	}
	migrateOnce.Do(func() { migrateErr = migrate.Up(migrations.FS, url) })
	if migrateErr != nil {
		t.Fatalf("migrate: %v", migrateErr)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `
		SET session_replication_role = replica;
		TRUNCATE events, projection_cursors, projection_event_counts,
		         current_cases, current_documents CASCADE;
		TRUNCATE assignment_rules, users, dealers CASCADE;
		SET session_replication_role = origin;
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// One user for the my_open KPI.
	opener := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(context.Background(), `
		INSERT INTO users (id, email, name, role, specializations)
		VALUES ($1, 'dash@stele.local', 'Dash Tester', 'ops', '{}')
	`, opener)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return pool, event.NewPostgresStore(pool), opener
}

// stubResolver assigns to opener; not testing routing here.
type stubResolver struct{}

func (stubResolver) ResolveForOpen(_ context.Context, in fault.RouteInput) (fault.Decision, error) {
	return fault.Decision{AssigneeID: in.OpenerID, Reason: fault.ReasonOpener}, nil
}

func TestKPIs(t *testing.T) {
	pool, store, me := setup(t)
	ctx := context.Background()

	// 3 open + 1 closed.
	openIDs := make([]uuid.UUID, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := fault.OpenCase(ctx, store, stubResolver{}, me, fault.CaseOpened{
			Dealer: "DEALER_X", VIN: "AAAAAAAAAAAAAAAAA", FaultCode: "BMS_X", Description: "d",
		})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		openIDs = append(openIDs, id)
	}
	closedID, _ := fault.OpenCase(ctx, store, stubResolver{}, me, fault.CaseOpened{
		Dealer: "DEALER_X", VIN: "BBBBBBBBBBBBBBBBB", FaultCode: "BMS_X", Description: "d",
	})
	_ = fault.CloseCase(ctx, store, closedID, fault.CaseClosed{Resolution: "x", ClosedBy: "yan"})

	runner := projection.NewRunner(store, pool)
	runner.Register(fault.CurrentCasesProjector())
	if err := runner.RunOnce(ctx, "current_cases"); err != nil {
		t.Fatalf("project: %v", err)
	}

	svc := New(pool)
	kpis, err := svc.KPIs(ctx, me)
	if err != nil {
		t.Fatalf("kpis: %v", err)
	}
	if kpis.TotalOpen != 3 {
		t.Errorf("total_open: got %d want 3", kpis.TotalOpen)
	}
	if kpis.MyOpen != 3 {
		t.Errorf("my_open: got %d want 3", kpis.MyOpen)
	}
	if kpis.OpenedLast7 != 4 { // 3 open + 1 closed all opened today
		t.Errorf("opened_last7: got %d want 4", kpis.OpenedLast7)
	}
	if kpis.ClosedLast7 != 1 {
		t.Errorf("closed_last7: got %d want 1", kpis.ClosedLast7)
	}
}

func TestClassificationMix(t *testing.T) {
	pool, store, me := setup(t)
	ctx := context.Background()

	// 2 warranty, 1 customer_education.
	for i := 0; i < 2; i++ {
		id, _ := fault.OpenCase(ctx, store, stubResolver{}, me, fault.CaseOpened{
			Dealer: "DX", VIN: "CCCCCCCCCCCCCCCCC", FaultCode: "BMS_X", Description: "d",
		})
		_ = fault.Classify(ctx, store, id, fault.Classified{Kind: fault.KindWarranty, Reasoning: "r"})
	}
	id, _ := fault.OpenCase(ctx, store, stubResolver{}, me, fault.CaseOpened{
		Dealer: "DX", VIN: "DDDDDDDDDDDDDDDDD", FaultCode: "BMS_X", Description: "d",
	})
	_ = fault.Classify(ctx, store, id, fault.Classified{Kind: fault.KindCustomerEducation, Reasoning: "r"})

	runner := projection.NewRunner(store, pool)
	runner.Register(fault.CurrentCasesProjector())
	if err := runner.RunOnce(ctx, "current_cases"); err != nil {
		t.Fatalf("project: %v", err)
	}

	mix, err := New(pool).ClassificationMix(ctx)
	if err != nil {
		t.Fatalf("mix: %v", err)
	}
	want := map[string]int{fault.KindWarranty: 2, fault.KindCustomerEducation: 1}
	got := map[string]int{}
	for _, m := range mix {
		got[m.Kind] = m.Count
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("kind %s: got %d want %d", k, got[k], v)
		}
	}
}

func TestActivityLast7DaysShape(t *testing.T) {
	pool, _, _ := setup(t)
	ctx := context.Background()

	days, err := New(pool).ActivityLast7Days(ctx)
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	if len(days) != 7 {
		t.Fatalf("days: got %d want 7", len(days))
	}
	for i := 1; i < len(days); i++ {
		if !days[i].Day.After(days[i-1].Day) {
			t.Fatalf("days not chronological: %v then %v", days[i-1].Day, days[i].Day)
		}
	}
	// Newest day should be today UTC.
	if !sameDay(days[len(days)-1].Day, time.Now().UTC()) {
		t.Errorf("last day not today: %v vs %v", days[len(days)-1].Day, time.Now().UTC())
	}
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
