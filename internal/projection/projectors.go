package projection

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/Th3r4c3r/stele/internal/event"
)

// EventCountByType counts events grouped by (aggregate_type, type),
// keeping per-row idempotency: an event is only incorporated if its id
// is strictly greater (lexicographically; UUIDv7 ids are time-sortable)
// than the row's last_event_id. Replay-safe.
func EventCountByType() Projector {
	return Projector{
		Name: "event_count_by_type",
		Apply: func(ctx context.Context, tx pgx.Tx, ev event.Event) error {
			const q = `
				INSERT INTO projection_event_counts
				    (aggregate_type, type, count, last_event_id)
				VALUES ($1, $2, 1, $3)
				ON CONFLICT (aggregate_type, type) DO UPDATE
				   SET count         = projection_event_counts.count + 1,
				       last_event_id = EXCLUDED.last_event_id
				 WHERE projection_event_counts.last_event_id < EXCLUDED.last_event_id
			`
			_, err := tx.Exec(ctx, q, ev.AggregateType, ev.Type, ev.ID)
			return err
		},
	}
}
