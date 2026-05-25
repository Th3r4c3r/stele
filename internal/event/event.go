// Package event is Stele's append-only event log.
//
// Events carry a bi-temporal pair: OccurredAt is when the fact is true in
// business reality (user-provided, can be backdated), RecordedAt is when
// Stele learned about it (system-provided, immutable). See ADR-001 and
// ADR-003 for the model.
package event

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event is one immutable fact in the log.
type Event struct {
	ID            uuid.UUID       `json:"id"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   uuid.UUID       `json:"aggregate_id"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurred_at"`
	RecordedAt    time.Time       `json:"recorded_at"`
	RecordedBy    string          `json:"recorded_by"`
}

// StreamOptions controls a projection-engine scan over the log.
//
// Filtering is conjunctive: a non-empty AggregateType narrows to that
// type, a non-zero AfterRecordedAt narrows to events recorded strictly
// after the cursor.
type StreamOptions struct {
	AggregateType   string
	AfterRecordedAt time.Time
	BatchSize       int // default 500
}
