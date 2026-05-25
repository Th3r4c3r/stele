package warranty

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/projection"
)

// CurrentClaimsProjector materializes the current_claims read model
// from warranty_claim events. Idempotent on replay via per-row
// last_event_id guards.
func CurrentClaimsProjector() projection.Projector {
	return projection.Projector{
		Name:          "current_claims",
		AggregateType: AggregateType,
		Apply:         applyCurrentClaims,
	}
}

func applyCurrentClaims(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	switch ev.Type {
	case EventClaimOpened:
		return applyClaimOpened(ctx, tx, ev)
	case EventNoteAdded:
		return applyNoteAdded(ctx, tx, ev)
	case EventClaimClosed:
		return applyClaimClosed(ctx, tx, ev)
	default:
		// Unknown event type on this aggregate: skip rather than fail,
		// so future event types deployed before the projector handles
		// them do not block the runner.
		return nil
	}
}

func applyClaimOpened(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	v, err := decodeAs[ClaimOpened](EventClaimOpened, ev.Payload)
	if err != nil {
		return err
	}
	// Skip malformed payloads (e.g., legacy smoke events without the
	// typed shape). Required fields enforced by the OpenClaim command;
	// if any are missing here the event predates the command layer.
	if v.Dealer == "" || v.VIN == "" || v.FaultCode == "" {
		return nil
	}
	// INSERT, but be replay-safe: if the row already exists with a newer
	// or equal last_event_id, skip.
	const q = `
		INSERT INTO current_claims
		    (id, status, dealer, vin, fault_code, description,
		     opened_at, last_update, note_count, last_event_id)
		VALUES ($1, 'open', $2, $3, $4, $5, $6, $6, 0, $7)
		ON CONFLICT (id) DO NOTHING
	`
	_, err = tx.Exec(ctx, q,
		ev.AggregateID, v.Dealer, v.VIN, v.FaultCode, v.Description,
		ev.OccurredAt, ev.ID,
	)
	if err != nil {
		return fmt.Errorf("applyClaimOpened: %w", err)
	}
	return nil
}

func applyNoteAdded(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	const q = `
		UPDATE current_claims
		   SET note_count    = note_count + 1,
		       last_update   = $2,
		       last_event_id = $3
		 WHERE id = $1
		   AND last_event_id < $3
	`
	_, err := tx.Exec(ctx, q, ev.AggregateID, ev.OccurredAt, ev.ID)
	if err != nil {
		return fmt.Errorf("applyNoteAdded: %w", err)
	}
	return nil
}

func applyClaimClosed(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	const q = `
		UPDATE current_claims
		   SET status        = 'closed',
		       closed_at     = $2,
		       last_update   = $2,
		       last_event_id = $3
		 WHERE id = $1
		   AND last_event_id < $3
		   AND status = 'open'
	`
	_, err := tx.Exec(ctx, q, ev.AggregateID, ev.OccurredAt, ev.ID)
	if err != nil {
		return fmt.Errorf("applyClaimClosed: %w", err)
	}
	return nil
}

func decodeAs[T any](eventType string, payload []byte) (T, error) {
	v, err := DecodePayload(eventType, payload)
	if err != nil {
		var zero T
		return zero, err
	}
	out, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("warranty: payload %T does not match %s", v, eventType)
	}
	return out, nil
}
