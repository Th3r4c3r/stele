// Package document attaches files to fault cases. Files live on the
// filesystem; the event log carries metadata + sha256 + the document
// id used as filename on disk. See docs/adr/0010-documents-storage.md.
package document

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// EventDocumentAttached is the event type string stored on events.type.
// The aggregate_type is the parent case's "fault_case".
const EventDocumentAttached = "DocumentAttached"

// DocumentAttached is the payload struct. Path on disk is NOT stored
// here: derive it via storage.PathFor(DocumentID) so the directory
// can be moved without re-writing events.
type DocumentAttached struct {
	DocumentID       uuid.UUID `json:"document_id"`
	SHA256           string    `json:"sha256"`
	ContentType      string    `json:"content_type"`
	OriginalFilename string    `json:"original_filename"`
	ByteSize         int64     `json:"byte_size"`
	AttachedByUserID uuid.UUID `json:"attached_by_user_id"`
}

// MarshalPayload encodes the event for storage in events.payload.
func MarshalPayload(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("document.MarshalPayload: %w", err)
	}
	return b, nil
}

// DecodePayload returns the typed struct for known event types in
// this package. Used by the web layer to render the timeline.
func DecodePayload(eventType string, payload json.RawMessage) (any, error) {
	switch eventType {
	case EventDocumentAttached:
		var v DocumentAttached
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode DocumentAttached: %w", err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("document: unknown event type %q", eventType)
	}
}
