package fault

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/projection"
)

// CurrentCasesProjector materialises the current_cases read model
// from fault_case events. Idempotent on replay via per-row
// last_event_id guards.
func CurrentCasesProjector() projection.Projector {
	return projection.Projector{
		Name:          "current_cases",
		AggregateType: AggregateType,
		Apply:         applyCurrentCases,
	}
}

func applyCurrentCases(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	switch ev.Type {
	case EventCaseOpened:
		return applyCaseOpened(ctx, tx, ev)
	case EventCaseAssigned:
		return applyCaseAssigned(ctx, tx, ev)
	case EventNoteAdded:
		return applyNoteAdded(ctx, tx, ev)
	case EventClassified:
		return applyClassified(ctx, tx, ev)
	case EventCaseClosed:
		return applyCaseClosed(ctx, tx, ev)
	default:
		// Forward-compatibility: unknown event type does not block the
		// runner. Will surface as "this case has events we don't render"
		// in the timeline UI, prompting a code update.
		return nil
	}
}

func applyCaseOpened(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	v, err := decodeAs[CaseOpened](EventCaseOpened, ev.Payload)
	if err != nil {
		return err
	}
	if v.Dealer == "" || v.VIN == "" || v.FaultCode == "" {
		// Defensive: a Case born without the required fields is garbage
		// (legacy smoke events from M0/M1). Skip rather than insert.
		return nil
	}
	// nextval is consumed only when the INSERT actually happens (no
	// conflict on id). On replay, ON CONFLICT DO NOTHING preserves the
	// existing number assigned at first application.
	const q = `
		INSERT INTO current_cases
		    (id, status, dealer, vin, fault_code, description,
		     opened_at, last_update, note_count, last_event_id, case_number)
		VALUES ($1, 'triage', $2, $3, $4, $5, $6, $6, 0, $7, nextval('case_number_seq'))
		ON CONFLICT (id) DO NOTHING
	`
	_, err = tx.Exec(ctx, q,
		ev.AggregateID, v.Dealer, v.VIN, v.FaultCode, v.Description,
		ev.OccurredAt, ev.ID,
	)
	if err != nil {
		return fmt.Errorf("applyCaseOpened: %w", err)
	}
	return nil
}

func applyNoteAdded(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	const q = `
		UPDATE current_cases
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

func applyClassified(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	v, err := decodeAs[Classified](EventClassified, ev.Payload)
	if err != nil {
		return err
	}
	if !IsKnownKind(v.Kind) {
		// Forward-compat: a future event with a new kind we have not
		// declared yet is ignored by this projector. Surface in /debug.
		return nil
	}
	const q = `
		UPDATE current_cases
		   SET status        = CASE WHEN status = 'closed' THEN 'closed' ELSE 'classified' END,
		       kind          = $4,
		       classified_at = COALESCE(classified_at, $2),
		       last_update   = $2,
		       last_event_id = $3
		 WHERE id = $1
		   AND last_event_id < $3
	`
	_, err = tx.Exec(ctx, q, ev.AggregateID, ev.OccurredAt, ev.ID, v.Kind)
	if err != nil {
		return fmt.Errorf("applyClassified: %w", err)
	}
	return nil
}

func applyCaseAssigned(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	v, err := decodeAs[CaseAssigned](EventCaseAssigned, ev.Payload)
	if err != nil {
		return err
	}
	const q = `
		UPDATE current_cases
		   SET assignee_id   = $4,
		       last_update   = $2,
		       last_event_id = $3
		 WHERE id = $1
		   AND last_event_id < $3
	`
	_, err = tx.Exec(ctx, q, ev.AggregateID, ev.OccurredAt, ev.ID, v.AssigneeID)
	if err != nil {
		return fmt.Errorf("applyCaseAssigned: %w", err)
	}
	return nil
}

func applyCaseClosed(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	const q = `
		UPDATE current_cases
		   SET status        = 'closed',
		       closed_at     = $2,
		       last_update   = $2,
		       last_event_id = $3
		 WHERE id = $1
		   AND last_event_id < $3
		   AND status <> 'closed'
	`
	_, err := tx.Exec(ctx, q, ev.AggregateID, ev.OccurredAt, ev.ID)
	if err != nil {
		return fmt.Errorf("applyCaseClosed: %w", err)
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
		return zero, fmt.Errorf("fault: payload %T does not match %s", v, eventType)
	}
	return out, nil
}
