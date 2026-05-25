// Package warranty is Stele's first domain: warranty claims.
//
// Event types are Go structs that marshal to JSON and are stored as
// the `payload` jsonb on events. See docs/adr/0005-warranty-domain.md.
package warranty

import (
	"encoding/json"
	"fmt"
)

const AggregateType = "warranty_claim"

// Event type names. Stable strings, used as the `type` column on events.
const (
	EventClaimOpened = "ClaimOpened"
	EventNoteAdded   = "NoteAdded"
	EventClaimClosed = "ClaimClosed"
)

// ClaimOpened is the birth event for a warranty claim.
type ClaimOpened struct {
	Dealer      string `json:"dealer"`
	VIN         string `json:"vin"`
	FaultCode   string `json:"fault_code"`
	Description string `json:"description"`
}

// NoteAdded appends a note during investigation. Does not change status.
type NoteAdded struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

// ClaimClosed is the terminal event. Status -> "closed".
type ClaimClosed struct {
	Resolution string `json:"resolution"`
	ClosedBy   string `json:"closed_by"`
}

// MarshalPayload encodes a domain event struct as raw JSON for storage.
func MarshalPayload(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("warranty.MarshalPayload: %w", err)
	}
	return b, nil
}

// DecodePayload extracts the typed event struct for a given event type.
// Returns an error if the type is unknown or the payload is malformed.
func DecodePayload(eventType string, payload json.RawMessage) (any, error) {
	switch eventType {
	case EventClaimOpened:
		var v ClaimOpened
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode ClaimOpened: %w", err)
		}
		return v, nil
	case EventNoteAdded:
		var v NoteAdded
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode NoteAdded: %w", err)
		}
		return v, nil
	case EventClaimClosed:
		var v ClaimClosed
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode ClaimClosed: %w", err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("warranty: unknown event type %q", eventType)
	}
}
