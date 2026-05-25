package document

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
		TRUNCATE events, projection_cursors, projection_event_counts,
		         current_cases, current_documents CASCADE;
		SET session_replication_role = origin;
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool, event.NewPostgresStore(pool)
}

func newStorage(t *testing.T) *Storage {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStorage(dir, 1<<20) // 1 MiB cap for tests
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	return s
}

func TestStorageWriteAndRead(t *testing.T) {
	s := newStorage(t)
	body := bytes.Repeat([]byte("Stele "), 100) // 600 bytes
	res, err := s.Write(bytes.NewReader(body), "text/plain")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.ByteSize != int64(len(body)) {
		t.Fatalf("size: got %d want %d", res.ByteSize, len(body))
	}
	if !strings.HasPrefix(res.ContentType, "text/plain") {
		t.Fatalf("ct: got %q", res.ContentType)
	}
	if len(res.SHA256) != 64 {
		t.Fatalf("sha256: got len %d, want 64", len(res.SHA256))
	}
	// File present on disk.
	if _, err := os.Stat(filepath.Join(t.TempDir(), res.DocumentID.String())); err == nil {
		t.Fatal("file should be in storage dir, not test tempdir")
	}
	f, err := s.Open(res.DocumentID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, body) {
		t.Fatalf("read-back mismatch")
	}
}

func TestStorageEnforcesMaxBytes(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir, 1024) // 1 KiB cap
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	body := bytes.Repeat([]byte("X"), 2048) // 2 KiB
	if _, err := s.Write(bytes.NewReader(body), ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	// tmp file must have been cleaned up.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") {
			t.Fatalf("leaked temp file: %s", e.Name())
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"normal.pdf", "normal.pdf"},
		{"../../etc/passwd", ".._.._etc_passwd"},
		{"weird\x01name\x7f.txt", "weirdname.txt"},
		{"", "document"},
		{strings.Repeat("a", 300), strings.Repeat("a", 200)},
	}
	for _, tc := range cases {
		got := SanitizeFilename(tc.in)
		if got != tc.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAttachAndProjectAndRedact(t *testing.T) {
	pool, store := requirePostgres(t)
	ctx := context.Background()
	s := newStorage(t)

	opener := uuid.Must(uuid.NewV7())
	caseID, err := fault.OpenCase(ctx, store, stubResolver{opener}, opener, fault.CaseOpened{
		Dealer: "DEALER_01", VIN: "AAAAAAAAAAAAAAAAA", FaultCode: "BMS_X", Description: "d",
	})
	if err != nil {
		t.Fatalf("open case: %v", err)
	}

	body := []byte("%PDF-1.4\n%fake pdf bytes for test\n")
	doc, err := AttachDocument(ctx, store, s, caseID, opener,
		bytes.NewReader(body), "report.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if doc.SHA256 == "" || doc.ByteSize != int64(len(body)) {
		t.Fatalf("bad metadata: %+v", doc)
	}

	runner := projection.NewRunner(store, pool)
	runner.Register(CurrentDocumentsProjector(s))
	if err := runner.RunOnce(ctx, "current_documents"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM current_documents WHERE case_id = $1`, caseID).Scan(&n)
	if n != 1 {
		t.Fatalf("projected rows: %d want 1", n)
	}

	// Replay must not double-count.
	_ = runner.ResetCursor(ctx, "current_documents")
	_ = runner.RunOnce(ctx, "current_documents")
	pool.QueryRow(ctx, `SELECT count(*) FROM current_documents WHERE case_id = $1`, caseID).Scan(&n)
	if n != 1 {
		t.Fatalf("replay rows: %d want 1 (idempotency broken)", n)
	}

	// RedactDocument: appending the event + running the projector
	// must remove the row AND the file.
	if err := RedactDocument(ctx, store, caseID, doc.DocumentID, opener, doc.OriginalFilename, "smoke"); err != nil {
		t.Fatalf("redact: %v", err)
	}
	if err := runner.RunOnce(ctx, "current_documents"); err != nil {
		t.Fatalf("run after redact: %v", err)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM current_documents WHERE id = $1`, doc.DocumentID).Scan(&n)
	if n != 0 {
		t.Fatalf("after redact rows: %d want 0", n)
	}
	if _, err := s.Open(doc.DocumentID); !os.IsNotExist(err) {
		t.Fatalf("expected file removed by projector, got %v", err)
	}

	// Replay after redact: still 0 (DELETE is idempotent; missing file is no-op).
	_ = runner.ResetCursor(ctx, "current_documents")
	_ = runner.RunOnce(ctx, "current_documents")
	pool.QueryRow(ctx, `SELECT count(*) FROM current_documents WHERE id = $1`, doc.DocumentID).Scan(&n)
	if n != 0 {
		t.Fatalf("after replay+redact rows: %d want 0", n)
	}
}

// stubResolver: opener gets everything, no rules.
type stubResolver struct{ opener uuid.UUID }

func (r stubResolver) ResolveForOpen(_ context.Context, in fault.RouteInput) (fault.Decision, error) {
	return fault.Decision{AssigneeID: in.OpenerID, Reason: fault.ReasonOpener}, nil
}
