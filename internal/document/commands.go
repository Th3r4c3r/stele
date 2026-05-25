package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
)

// ErrValidation is returned for input failures (empty case id, etc.).
var ErrValidation = errors.New("document: validation")

// AttachDocument streams body into Storage, then appends a
// DocumentAttached event on the case aggregate. Returns the new
// document id and the metadata for the read model.
//
// The caller (HTTP handler) is responsible for limiting how much
// data ever reaches body; Storage.Write will additionally cap at
// its own MaxBytes and return ErrTooLarge.
func AttachDocument(
	ctx context.Context,
	store *event.PostgresStore,
	storage *Storage,
	caseID, attachedBy uuid.UUID,
	body io.Reader,
	originalFilename, contentTypeHint string,
) (DocumentAttached, error) {
	if caseID == uuid.Nil {
		return DocumentAttached{}, fmt.Errorf("%w: case_id required", ErrValidation)
	}
	if attachedBy == uuid.Nil {
		return DocumentAttached{}, fmt.Errorf("%w: attached_by required", ErrValidation)
	}
	res, err := storage.Write(body, contentTypeHint)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return DocumentAttached{}, ErrTooLarge
		}
		return DocumentAttached{}, fmt.Errorf("AttachDocument: write: %w", err)
	}

	doc := DocumentAttached{
		DocumentID:       res.DocumentID,
		SHA256:           res.SHA256,
		ContentType:      res.ContentType,
		OriginalFilename: SanitizeFilename(originalFilename),
		ByteSize:         res.ByteSize,
		AttachedByUserID: attachedBy,
	}
	payload, err := MarshalPayload(doc)
	if err != nil {
		// Rollback the file write: the event is the source of truth.
		_ = storage.Delete(res.DocumentID)
		return DocumentAttached{}, err
	}
	ev := event.Event{
		AggregateType: fault.AggregateType,
		AggregateID:   caseID,
		Type:          EventDocumentAttached,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		_ = storage.Delete(res.DocumentID)
		return DocumentAttached{}, fmt.Errorf("AttachDocument: append: %w", err)
	}
	return doc, nil
}
