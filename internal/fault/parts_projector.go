package fault

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/projection"
)

// CasePartsProjector consumes PartReplaced + PartQuoted events on
// fault_case aggregates and materialises case_parts. The projector
// holds a pool to look up the parts master at event time (so the
// price snapshot is the price the master had when the event was
// recorded; later master edits do not retroactively rewrite history).
func CasePartsProjector(pool *pgxpool.Pool) projection.Projector {
	return projection.Projector{
		Name:          "case_parts",
		AggregateType: AggregateType,
		Apply: func(ctx context.Context, tx pgx.Tx, ev event.Event) error {
			switch ev.Type {
			case EventPartReplaced:
				return applyPartReplaced(ctx, tx, ev, pool)
			case EventPartQuoted:
				return applyPartQuoted(ctx, tx, ev, pool)
			default:
				return nil
			}
		},
	}
}

func applyPartReplaced(ctx context.Context, tx pgx.Tx, ev event.Event, pool *pgxpool.Pool) error {
	v, err := decodeAs[PartReplaced](EventPartReplaced, ev.Payload)
	if err != nil {
		return err
	}
	price := lookupPartPrice(ctx, pool, v.PartNumber)
	cost := price * float64(v.Qty)
	return insertCasePart(ctx, tx, ev.ID, ev.AggregateID, v.PartNumber, v.Qty, "replaced", cost, ev.OccurredAt)
}

func applyPartQuoted(ctx context.Context, tx pgx.Tx, ev event.Event, pool *pgxpool.Pool) error {
	v, err := decodeAs[PartQuoted](EventPartQuoted, ev.Payload)
	if err != nil {
		return err
	}
	// For quotes we use the explicit amount in the event (the operator
	// may have negotiated a different price than the master reference).
	return insertCasePart(ctx, tx, ev.ID, ev.AggregateID, v.PartNumber, v.Qty, "quoted", v.QuotedAmountEUR, ev.OccurredAt)
}

func insertCasePart(ctx context.Context, tx pgx.Tx, eventID, caseID uuid.UUID, pn string, qty int, kind string, cost float64, recordedAt interface{}) error {
	const q = `
		INSERT INTO case_parts (id, case_id, pn, qty, kind, cost_at_event, recorded_at, last_event_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $1)
		ON CONFLICT (id) DO NOTHING
	`
	_, err := tx.Exec(ctx, q, eventID, caseID, pn, qty, kind, cost, recordedAt)
	if err != nil {
		return fmt.Errorf("case_parts insert: %w", err)
	}
	return nil
}

func lookupPartPrice(ctx context.Context, pool *pgxpool.Pool, pn string) float64 {
	var p *float64
	err := pool.QueryRow(ctx, `SELECT price_eur FROM parts WHERE pn = $1`, pn).Scan(&p)
	if err != nil || p == nil {
		return 0
	}
	return *p
}
