package fault

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Th3r4c3r/stele/internal/event"
)

// ErrValidation is the user-facing input-validation sentinel.
// Handlers map it to HTTP 422.
var ErrValidation = errors.New("validation")

// OpenCase records a new fault case. Status starts as "triage".
func OpenCase(ctx context.Context, store *event.PostgresStore, p CaseOpened) (uuid.UUID, error) {
	p.Dealer = strings.TrimSpace(p.Dealer)
	p.VIN = strings.TrimSpace(strings.ToUpper(p.VIN))
	p.FaultCode = strings.TrimSpace(p.FaultCode)
	p.Description = strings.TrimSpace(p.Description)

	if p.Dealer == "" {
		return uuid.Nil, fmt.Errorf("%w: dealer required", ErrValidation)
	}
	if len(p.VIN) != 17 {
		return uuid.Nil, fmt.Errorf("%w: VIN must be 17 characters (got %d)", ErrValidation, len(p.VIN))
	}
	if p.FaultCode == "" {
		return uuid.Nil, fmt.Errorf("%w: fault_code required", ErrValidation)
	}
	if p.Description == "" {
		return uuid.Nil, fmt.Errorf("%w: description required", ErrValidation)
	}

	caseID := uuid.Must(uuid.NewV7())
	payload, err := MarshalPayload(p)
	if err != nil {
		return uuid.Nil, err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   caseID,
		Type:          EventCaseOpened,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return uuid.Nil, fmt.Errorf("OpenCase: append: %w", err)
	}
	return caseID, nil
}

// AddNote appends a NoteAdded event. Allowed in any status.
func AddNote(ctx context.Context, store *event.PostgresStore, caseID uuid.UUID, p NoteAdded) error {
	p.Author = strings.TrimSpace(p.Author)
	p.Text = strings.TrimSpace(p.Text)
	if caseID == uuid.Nil {
		return fmt.Errorf("%w: case_id required", ErrValidation)
	}
	if p.Author == "" {
		p.Author = "system"
	}
	if p.Text == "" {
		return fmt.Errorf("%w: note text required", ErrValidation)
	}
	payload, err := MarshalPayload(p)
	if err != nil {
		return err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   caseID,
		Type:          EventNoteAdded,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("AddNote: append: %w", err)
	}
	return nil
}

// Classify sets or updates the kind on a case. Multiple Classified
// events are allowed; the latest by id wins in the read model.
func Classify(ctx context.Context, store *event.PostgresStore, caseID uuid.UUID, p Classified) error {
	p.Kind = strings.TrimSpace(p.Kind)
	p.Reasoning = strings.TrimSpace(p.Reasoning)
	if caseID == uuid.Nil {
		return fmt.Errorf("%w: case_id required", ErrValidation)
	}
	if !IsKnownKind(p.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrValidation, p.Kind)
	}
	if p.Reasoning == "" {
		return fmt.Errorf("%w: reasoning required", ErrValidation)
	}
	payload, err := MarshalPayload(p)
	if err != nil {
		return err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   caseID,
		Type:          EventClassified,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("Classify: append: %w", err)
	}
	return nil
}

// CloseCase appends a CaseClosed event. Allowed from any status;
// closing an already-closed case is idempotent at the projector level.
func CloseCase(ctx context.Context, store *event.PostgresStore, caseID uuid.UUID, p CaseClosed) error {
	p.Resolution = strings.TrimSpace(p.Resolution)
	p.ClosedBy = strings.TrimSpace(p.ClosedBy)
	if caseID == uuid.Nil {
		return fmt.Errorf("%w: case_id required", ErrValidation)
	}
	if p.Resolution == "" {
		return fmt.Errorf("%w: resolution required", ErrValidation)
	}
	if p.ClosedBy == "" {
		p.ClosedBy = "system"
	}
	payload, err := MarshalPayload(p)
	if err != nil {
		return err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   caseID,
		Type:          EventCaseClosed,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("CloseCase: append: %w", err)
	}
	return nil
}
