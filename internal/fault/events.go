// Package fault is Stele's primary domain: fault cases reported from
// the field, triaged, classified, and eventually closed.
//
// See docs/adr/0007-fault-case-refactor.md for the model rationale.
// This package supersedes the earlier internal/warranty package.
package fault

import (
	"encoding/json"
	"fmt"
)

// AggregateType is the canonical string for the aggregate_type column.
const AggregateType = "fault_case"

// Event type names. Stable strings used as the events.type column.
const (
	EventCaseOpened = "CaseOpened"
	EventNoteAdded  = "NoteAdded"
	EventClassified = "Classified"
	EventCaseClosed = "CaseClosed"
)

// Status values for the current_cases read model.
const (
	StatusTriage     = "triage"
	StatusClassified = "classified"
	StatusClosed     = "closed"
)

// Kind values for classified cases. Closed enum, enforced by the
// projector and by a CHECK constraint on current_cases.
const (
	KindWarranty          = "warranty"
	KindOutOfWarranty     = "out_of_warranty"
	KindGoodwill          = "goodwill"
	KindRecall            = "recall"
	KindUnrelated         = "unrelated"
	KindCustomerEducation = "customer_education"
)

// AllKinds is the canonical set, suitable for select inputs.
var AllKinds = []string{
	KindWarranty,
	KindOutOfWarranty,
	KindGoodwill,
	KindRecall,
	KindUnrelated,
	KindCustomerEducation,
}

// IsKnownKind returns true if k is in AllKinds. Projector uses this
// to ignore Classified events with an unrecognised kind, so that a
// forgotten migration cannot corrupt the read model.
func IsKnownKind(k string) bool {
	for _, x := range AllKinds {
		if x == k {
			return true
		}
	}
	return false
}

// CaseOpened is the birth event of a fault case. Status -> "triage".
type CaseOpened struct {
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

// Classified sets (or re-sets) the kind for a case. Status -> "classified".
// Multiple Classified events are allowed and reflect re-classification.
type Classified struct {
	Kind      string `json:"kind"`
	Reasoning string `json:"reasoning"`
}

// CaseClosed is the terminal event. Status -> "closed". May arrive
// before any Classified (closed-from-triage); in that case the row's
// kind stays NULL.
type CaseClosed struct {
	Resolution string `json:"resolution"`
	ClosedBy   string `json:"closed_by"`
}

// MarshalPayload encodes a domain event struct as raw JSON for storage.
func MarshalPayload(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("fault.MarshalPayload: %w", err)
	}
	return b, nil
}

// DecodePayload extracts the typed event struct for a given event type.
func DecodePayload(eventType string, payload json.RawMessage) (any, error) {
	switch eventType {
	case EventCaseOpened:
		var v CaseOpened
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode CaseOpened: %w", err)
		}
		return v, nil
	case EventNoteAdded:
		var v NoteAdded
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode NoteAdded: %w", err)
		}
		return v, nil
	case EventClassified:
		var v Classified
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode Classified: %w", err)
		}
		return v, nil
	case EventCaseClosed:
		var v CaseClosed
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode CaseClosed: %w", err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("fault: unknown event type %q", eventType)
	}
}
