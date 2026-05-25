// Package projection materializes read models from the event log.
//
// See docs/adr/0004-projection-engine.md for the design.
//
// A Projector is a named function that, given an event and a
// transaction, writes to a read-model table. The Runner consumes the
// event Store, calls each registered Projector in its own goroutine,
// and advances per-projector cursors atomically with the side-effect.
//
// Projectors MUST be idempotent. The Runner exposes them to the same
// event again on crash recovery or replay.
package projection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/event"
)

// Projector is a registered read-model builder.
type Projector struct {
	// Name is unique across the registry and is used as the cursor key.
	Name string
	// AggregateType narrows the event stream. Empty consumes all.
	AggregateType string
	// Apply writes the read-model side-effect using tx, which is the
	// same transaction that will commit the cursor advance. Apply must
	// be idempotent.
	Apply func(ctx context.Context, tx pgx.Tx, ev event.Event) error
}

// Runner owns the lifecycle of all registered projectors.
type Runner struct {
	store     *event.PostgresStore
	pool      *pgxpool.Pool
	projs     []Projector
	pollEvery time.Duration
	batchSize int
}

// NewRunner builds a Runner. Poll interval defaults to 2s (overridable
// with STELE_PROJECTION_POLL_INTERVAL_MS). Batch size defaults to 200.
func NewRunner(store *event.PostgresStore, pool *pgxpool.Pool) *Runner {
	poll := 2 * time.Second
	if v := os.Getenv("STELE_PROJECTION_POLL_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			poll = time.Duration(n) * time.Millisecond
		}
	}
	return &Runner{
		store:     store,
		pool:      pool,
		pollEvery: poll,
		batchSize: 200,
	}
}

// Register adds a projector. Call before Start.
func (r *Runner) Register(p Projector) {
	if p.Name == "" {
		panic("projection.Register: Name required")
	}
	if p.Apply == nil {
		panic("projection.Register: Apply required")
	}
	r.projs = append(r.projs, p)
}

// Start launches one goroutine per registered projector. Returns a
// WaitGroup the caller can use to await graceful shutdown when ctx
// is cancelled.
func (r *Runner) Start(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, p := range r.projs {
		wg.Add(1)
		go func(p Projector) {
			defer wg.Done()
			r.run(ctx, p)
		}(p)
	}
	return &wg
}

// RunOnce processes one projector to completion (no goroutine, no
// polling sleep after catching up). Used by the replay sub-command.
func (r *Runner) RunOnce(ctx context.Context, name string) error {
	for _, p := range r.projs {
		if p.Name == name {
			return r.drain(ctx, p)
		}
	}
	return fmt.Errorf("projection.RunOnce: no projector named %q", name)
}

// ResetCursor deletes the cursor row so the next pass replays from the
// beginning. The read-model table is left intact; the projector must
// be idempotent per ADR-004 D5.
func (r *Runner) ResetCursor(ctx context.Context, name string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM projection_cursors WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("projection.ResetCursor: %w", err)
	}
	return nil
}

// Names returns the names of all registered projectors.
func (r *Runner) Names() []string {
	out := make([]string, len(r.projs))
	for i, p := range r.projs {
		out[i] = p.Name
	}
	return out
}

func (r *Runner) run(ctx context.Context, p Projector) {
	log := slog.With("projector", p.Name)
	log.Info("projector started")
	for {
		select {
		case <-ctx.Done():
			log.Info("projector stopped")
			return
		default:
		}
		processed, err := r.processBatch(ctx, p)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Error("projector batch failed; retrying", "err", err)
			sleep(ctx, r.pollEvery)
			continue
		}
		if processed == 0 {
			sleep(ctx, r.pollEvery)
		}
	}
}

func (r *Runner) drain(ctx context.Context, p Projector) error {
	for {
		processed, err := r.processBatch(ctx, p)
		if err != nil {
			return err
		}
		if processed == 0 {
			return nil
		}
	}
}

// processBatch reads one batch starting from the cursor, applies each
// event in its own transaction (Apply + cursor update atomic), and
// returns the count processed.
//
// The stream is fully drained into a local slice before any applyOne
// runs. This decouples the read connection from the write transactions
// so they cannot contend for the same pool slot.
func (r *Runner) processBatch(ctx context.Context, p Projector) (int, error) {
	cursorTime, cursorID, err := r.readCursor(ctx, p.Name)
	if err != nil {
		return 0, err
	}
	opts := event.StreamOptions{
		AggregateType:   p.AggregateType,
		AfterRecordedAt: cursorTime,
		AfterID:         cursorID,
		BatchSize:       r.batchSize,
	}
	var batch []event.Event
	for ev, err := range r.store.Stream(ctx, opts) {
		if err != nil {
			return 0, fmt.Errorf("stream: %w", err)
		}
		batch = append(batch, ev)
		if len(batch) >= r.batchSize {
			break
		}
	}
	if len(batch) == 0 {
		return 0, nil
	}
	for _, ev := range batch {
		if err := r.applyOne(ctx, p, ev); err != nil {
			return 0, err
		}
	}
	return len(batch), nil
}

func (r *Runner) applyOne(ctx context.Context, p Projector, ev event.Event) error {
	// Hard per-event timeout so a stuck PG or a poison-pill projector is
	// visible (error logged) instead of hanging the runner forever.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := p.Apply(ctx, tx, ev); err != nil {
		return fmt.Errorf("apply %s: %w", ev.ID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO projection_cursors (name, last_recorded_at, last_event_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE
		   SET last_recorded_at = EXCLUDED.last_recorded_at,
		       last_event_id    = EXCLUDED.last_event_id,
		       updated_at       = now()
	`, p.Name, ev.RecordedAt, ev.ID); err != nil {
		return fmt.Errorf("cursor upsert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (r *Runner) readCursor(ctx context.Context, name string) (time.Time, uuid.UUID, error) {
	var t time.Time
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT last_recorded_at, last_event_id FROM projection_cursors WHERE name = $1`,
		name,
	).Scan(&t, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, uuid.Nil, nil
	}
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("read cursor %s: %w", name, err)
	}
	return t, id, nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
