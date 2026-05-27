// Package fault is Stele's primary domain: fault cases reported from
// the field, triaged, classified, and eventually closed.
//
// See docs/adr/0007-fault-case-refactor.md for the model rationale.
// This package supersedes the earlier internal/warranty package.
package fault

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// AggregateType is the canonical string for the aggregate_type column.
const AggregateType = "fault_case"

// Event type names. Stable strings used as the events.type column.
const (
	EventCaseOpened   = "CaseOpened"
	EventCaseAssigned = "CaseAssigned"
	EventNoteAdded    = "NoteAdded"
	EventClassified   = "Classified"
	EventCaseClosed   = "CaseClosed"
	EventPartReplaced = "PartReplaced"
	EventPartQuoted   = "PartQuoted"
	EventStageChanged = "StageChanged"
)

// Part kind discriminator: both events carry it for downstream cost
// attribution. PartReplaced uses 'warranty' / 'goodwill' /
// 'out_of_warranty' to mirror the case classification; PartQuoted
// is always 'out_of_warranty' by definition.
const (
	PartKindWarranty      = "warranty"
	PartKindGoodwill      = "goodwill"
	PartKindOutOfWarranty = "out_of_warranty"
)

// Reason values for CaseAssigned.
const (
	ReasonOpener           = "opener"
	ReasonRuleFaultPrefix  = "rule:fault_prefix"
	ReasonRuleDealerRegion = "rule:dealer_region"
	ReasonManual           = "manual"
)

// Status values for the current_cases read model.
const (
	StatusTriage     = "triage"
	StatusClassified = "classified"
	StatusClosed     = "closed"
)

// Stage values track the repair workflow. Orthogonal to Status: a
// classified case can be in any repair stage. Closing a case auto-
// transitions stage to 'resolved' in the projector. See migration
// 0016. The order in AllStages is the canonical forward sequence
// shown by the UI stepper, but transitions are not constrained to
// neighbours: an operator can jump to any stage to model edge cases
// (parts came faster than expected, regression back to diagnosis,
// etc.).
const (
	StageNew          = "new"
	StageDiagnosis    = "diagnosis"
	StagePartsOrdered = "parts_ordered"
	StagePartsWaiting = "parts_waiting"
	StageRepair       = "repair"
	StageResolved     = "resolved"
)

// AllStages is the canonical forward order. UI iterates this for the
// stepper.
var AllStages = []string{
	StageNew, StageDiagnosis, StagePartsOrdered, StagePartsWaiting, StageRepair, StageResolved,
}

// IsKnownStage returns true if s is in AllStages.
func IsKnownStage(s string) bool {
	for _, x := range AllStages {
		if x == s {
			return true
		}
	}
	return false
}

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

// CaseAssigned records an assignment or transfer. The first
// CaseAssigned on a case is auto-emitted by OpenCase via routing.
// Subsequent ones come from Reassign.
type CaseAssigned struct {
	AssigneeID      uuid.UUID  `json:"assignee_id"`
	Reason          string     `json:"reason"`
	RuleName        string     `json:"rule_name,omitempty"`
	TransferredFrom *uuid.UUID `json:"transferred_from,omitempty"`
}

// PartReplaced records a part that was physically swapped under the
// case. Cost attribution depends on Kind (warranty = vendor pays,
// goodwill = we pay as gesture, out_of_warranty = customer pays).
type PartReplaced struct {
	PartNumber string    `json:"pn"`
	Qty        int       `json:"qty"`
	Kind       string    `json:"kind"` // warranty | goodwill | out_of_warranty
	Reason     string    `json:"reason,omitempty"`
	ByUserID   uuid.UUID `json:"by_user_id"`
}

// PartQuoted records a price quote sent to the customer for an
// out-of-warranty repair. Whether the customer accepts is not yet
// modelled (would be a separate event when needed).
type PartQuoted struct {
	PartNumber      string    `json:"pn"`
	Qty             int       `json:"qty"`
	QuotedAmountEUR float64   `json:"quoted_amount_eur"`
	ByUserID        uuid.UUID `json:"by_user_id"`
}

// CaseClosed is the terminal event. Status -> "closed". May arrive
// before any Classified (closed-from-triage); in that case the row's
// kind stays NULL. The projector also pins stage='resolved' on this
// event (operationally a closed case is a resolved case).
type CaseClosed struct {
	Resolution string `json:"resolution"`
	ClosedBy   string `json:"closed_by"`
}

// StageChanged records a transition through the repair workflow.
// `From` is informational (projector source-of-truth is the row's
// current stage, not the event payload); kept so the timeline can
// render "moved from Diagnosis to Parts ordered" without resolving
// state from a sibling event.
type StageChanged struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Reason   string    `json:"reason,omitempty"`
	ByUserID uuid.UUID `json:"by_user_id"`
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
	case EventCaseAssigned:
		var v CaseAssigned
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode CaseAssigned: %w", err)
		}
		return v, nil
	case EventPartReplaced:
		var v PartReplaced
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode PartReplaced: %w", err)
		}
		return v, nil
	case EventPartQuoted:
		var v PartQuoted
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode PartQuoted: %w", err)
		}
		return v, nil
	case EventCaseClosed:
		var v CaseClosed
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode CaseClosed: %w", err)
		}
		return v, nil
	case EventStageChanged:
		var v StageChanged
		if err := json.Unmarshal(payload, &v); err != nil {
			return nil, fmt.Errorf("decode StageChanged: %w", err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("fault: unknown event type %q", eventType)
	}
}
