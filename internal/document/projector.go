package document

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/projection"
)

// CurrentDocumentsProjector materialises the current_documents read
// model. Attach INSERTs a row; Redact deletes the row + unlinks the
// file. Both branches are idempotent.
//
// The projector holds a reference to Storage so the FS side-effect
// happens inside the same Apply call (still atomic for the DB row;
// the file unlink is best-effort and may run again on replay).
func CurrentDocumentsProjector(storage *Storage) projection.Projector {
	return projection.Projector{
		Name:          "current_documents",
		AggregateType: fault.AggregateType,
		Apply: func(ctx context.Context, tx pgx.Tx, ev event.Event) error {
			return apply(ctx, tx, ev, storage)
		},
	}
}

func apply(ctx context.Context, tx pgx.Tx, ev event.Event, storage *Storage) error {
	switch ev.Type {
	case EventDocumentAttached:
		return applyAttached(ctx, tx, ev)
	case EventDocumentRedacted:
		return applyRedacted(ctx, tx, ev, storage)
	default:
		return nil // other fault_case events go to other projectors
	}
}

func applyAttached(ctx context.Context, tx pgx.Tx, ev event.Event) error {
	v, err := decodeAttached(ev.Payload)
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

func applyRedacted(ctx context.Context, tx pgx.Tx, ev event.Event, storage *Storage) error {
	v, err := decodeRedacted(ev.Payload)
	if err != nil {
		return err
	}
	// Idempotent: a DELETE on a missing row is a no-op.
	_, err = tx.Exec(ctx, `DELETE FROM current_documents WHERE id = $1`, v.DocumentID)
	if err != nil {
		return fmt.Errorf("current_documents delete: %w", err)
	}
	// File removal is best-effort: it may have already been removed on a
	// previous apply, or by a manual disk operation. Errors that aren't
	// "missing" should be logged but not block the projector.
	if storage != nil {
		if err := storage.Delete(v.DocumentID); err != nil && !os.IsNotExist(err) {
			// Don't fail the tx; surface only.
			fmt.Fprintf(os.Stderr, "projector: failed to unlink %s: %v\n", v.DocumentID, err)
		}
	}
	return nil
}

func decodeAttached(payload []byte) (DocumentAttached, error) {
	v, err := DecodePayload(EventDocumentAttached, payload)
	if err != nil {
		return DocumentAttached{}, err
	}
	out, ok := v.(DocumentAttached)
	if !ok {
		return DocumentAttached{}, fmt.Errorf("document: payload type mismatch (attached)")
	}
	return out, nil
}

func decodeRedacted(payload []byte) (DocumentRedacted, error) {
	v, err := DecodePayload(EventDocumentRedacted, payload)
	if err != nil {
		return DocumentRedacted{}, err
	}
	out, ok := v.(DocumentRedacted)
	if !ok {
		return DocumentRedacted{}, fmt.Errorf("document: payload type mismatch (redacted)")
	}
	return out, nil
}
