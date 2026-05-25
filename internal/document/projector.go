package document

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/projection"
)

// CurrentDocumentsProjector materialises the current_documents read
// model from DocumentAttached events on fault_case aggregates.
// Idempotent on replay (ON CONFLICT (id) DO NOTHING because each
// document id is unique by construction).
func CurrentDocumentsProjector() projection.Projector {
	return projection.Projector{
		Name:          "current_documents",
		AggregateType: fault.AggregateType,
		Apply:         apply,
	}
}

func apply(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	if ev.Type != EventDocumentAttached {
		return nil // other fault_case events are projected by other projectors
	}
	v, err := decodeAs(ev.Payload)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO current_documents
		    (id, case_id, sha256, filename, content_type, byte_size,
		     attached_by_user_id, attached_at, last_event_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO NOTHING
	`
	_, err = tx.Exec(ctx, q,
		v.DocumentID, ev.AggregateID, v.SHA256, v.OriginalFilename, v.ContentType, v.ByteSize,
		v.AttachedByUserID, ev.OccurredAt, ev.ID,
	)
	if err != nil {
		return fmt.Errorf("current_documents insert: %w", err)
	}
	return nil
}

func decodeAs(payload []byte) (DocumentAttached, error) {
	v, err := DecodePayload(EventDocumentAttached, payload)
	if err != nil {
		return DocumentAttached{}, err
	}
	out, ok := v.(DocumentAttached)
	if !ok {
		return DocumentAttached{}, fmt.Errorf("document: payload type mismatch")
	}
	return out, nil
}
