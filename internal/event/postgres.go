package event

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the production Store implementation.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps an existing pool. The pool's lifecycle is the
// caller's responsibility.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Append inserts a batch in a single transaction.
func (s *PostgresStore) Append(ctx context.Context, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("event.Append: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `
		INSERT INTO events
		    (id, aggregate_type, aggregate_id, type, payload, occurred_at)
		VALUES
		    ($1, $2, $3, $4, $5, $6)
		RETURNING recorded_at, recorded_by
	`
	for i := range evs {
		if evs[i].ID == uuid.Nil {
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("event.Append: new id: %w", err)
			}
			evs[i].ID = id
		}
		if evs[i].OccurredAt.IsZero() {
			evs[i].OccurredAt = time.Now().UTC()
		}
		if len(evs[i].Payload) == 0 {
			evs[i].Payload = []byte("{}")
		}
		err = tx.QueryRow(ctx, q,
			evs[i].ID,
			evs[i].AggregateType,
			evs[i].AggregateID,
			evs[i].Type,
			evs[i].Payload,
			evs[i].OccurredAt,
		).Scan(&evs[i].RecordedAt, &evs[i].RecordedBy)
		if err != nil {
			return fmt.Errorf("event.Append: insert %s: %w", evs[i].ID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("event.Append: commit: %w", err)
	}
	return nil
}

// Load returns events for one aggregate, ordered by OccurredAt asc.
func (s *PostgresStore) Load(ctx context.Context, aggregateID uuid.UUID, since time.Time) ([]Event, error) {
	const q = `
		SELECT id, aggregate_type, aggregate_id, type, payload,
		       occurred_at, recorded_at, recorded_by
		FROM events
		WHERE aggregate_id = $1 AND occurred_at > $2
		ORDER BY occurred_at ASC, id ASC
	`
	rows, err := s.pool.Query(ctx, q, aggregateID, since)
	if err != nil {
		return nil, fmt.Errorf("event.Load: query: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event.Load: rows: %w", err)
	}
	return out, nil
}

// Stream paginates with keyset on (recorded_at, id) so concurrent
// inserts cannot cause skipped or duplicated rows across batches.
func (s *PostgresStore) Stream(ctx context.Context, opts StreamOptions) iter.Seq2[Event, error] {
	batch := opts.BatchSize
	if batch <= 0 {
		batch = 500
	}
	return func(yield func(Event, error) bool) {
		cursorTime := opts.AfterRecordedAt
		cursorID := opts.AfterID
		for {
			evs, err := s.streamBatch(ctx, opts.AggregateType, cursorTime, cursorID, batch)
			if err != nil {
				yield(Event{}, err)
				return
			}
			if len(evs) == 0 {
				return
			}
			for _, ev := range evs {
				if !yield(ev, nil) {
					return
				}
			}
			last := evs[len(evs)-1]
			cursorTime = last.RecordedAt
			cursorID = last.ID
		}
	}
}

func (s *PostgresStore) streamBatch(
	ctx context.Context,
	aggregateType string,
	afterRecordedAt time.Time,
	afterID uuid.UUID,
	limit int,
) ([]Event, error) {
	const q = `
		SELECT id, aggregate_type, aggregate_id, type, payload,
		       occurred_at, recorded_at, recorded_by
		FROM events
		WHERE ($1 = '' OR aggregate_type = $1)
		  AND (recorded_at, id) > ($2, $3)
		ORDER BY recorded_at ASC, id ASC
		LIMIT $4
	`
	rows, err := s.pool.Query(ctx, q, aggregateType, afterRecordedAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("event.Stream: query: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event.Stream: rows: %w", err)
	}
	return out, nil
}

func scanEvent(rows pgx.Rows) (Event, error) {
	var ev Event
	err := rows.Scan(
		&ev.ID,
		&ev.AggregateType,
		&ev.AggregateID,
		&ev.Type,
		&ev.Payload,
		&ev.OccurredAt,
		&ev.RecordedAt,
		&ev.RecordedBy,
	)
	if err != nil {
		return Event{}, fmt.Errorf("scan event: %w", err)
	}
	return ev, nil
}

// ErrAppendOnly is returned by an UPDATE/DELETE attempt at the DB layer.
// Callers should never see this if they only use Append.
var ErrAppendOnly = errors.New("events is append-only")
