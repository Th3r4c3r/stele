package event

import (
	"context"
	"iter"
	"time"

	"github.com/google/uuid"
)

// Store is the append-only event log API.
//
// Implementations MUST refuse to mutate or remove events; the Postgres
// implementation backs this with a DB-level trigger (see migration 0001).
type Store interface {
	// Append writes a batch atomically. Any Event with a zero ID is
	// assigned a UUIDv7 by the Store. RecordedAt and RecordedBy are
	// always set by the Store and ignored on input.
	Append(ctx context.Context, evs []Event) error

	// Load returns the events for a single aggregate, ordered by
	// OccurredAt ascending. since is exclusive; pass a zero time to
	// load from the beginning.
	Load(ctx context.Context, aggregateID uuid.UUID, since time.Time) ([]Event, error)

	// Stream yields events in RecordedAt order for projection workers.
	// The iterator stops at the first error.
	Stream(ctx context.Context, opts StreamOptions) iter.Seq2[Event, error]
}
