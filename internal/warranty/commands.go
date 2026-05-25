package warranty

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Th3r4c3r/stele/internal/event"
)

// ErrValidation is returned for user-facing validation failures.
// Handlers should map this to HTTP 422.
var ErrValidation = errors.New("validation")

// OpenClaim creates a new warranty claim and returns its id.
//
// The event is appended with OccurredAt = now. Callers that need to
// backdate an event (M5 time-travel) will use a different command.
func OpenClaim(ctx context.Context, store *event.PostgresStore, p ClaimOpened) (uuid.UUID, error) {
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

	claimID := uuid.Must(uuid.NewV7())
	payload, err := MarshalPayload(p)
	if err != nil {
		return uuid.Nil, err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   claimID,
		Type:          EventClaimOpened,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return uuid.Nil, fmt.Errorf("OpenClaim: append: %w", err)
	}
	return claimID, nil
}

// AddNote appends a NoteAdded event to an existing claim.
//
// The command does not verify the claim exists; the projector will
// silently ignore notes whose aggregate_id has no row in current_claims
// (replay-safe). At single-user scale this is acceptable; M5+ may add
// a precondition check.
func AddNote(ctx context.Context, store *event.PostgresStore, claimID uuid.UUID, p NoteAdded) error {
	p.Author = strings.TrimSpace(p.Author)
	p.Text = strings.TrimSpace(p.Text)
	if claimID == uuid.Nil {
		return fmt.Errorf("%w: claim_id required", ErrValidation)
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
		AggregateID:   claimID,
		Type:          EventNoteAdded,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("AddNote: append: %w", err)
	}
	return nil
}

// CloseClaim transitions a claim to "closed".
//
// Closing a claim already closed is a no-op at the projector level
// (last_event_id guard + status check). The command does not check
// status; safe to call multiple times.
func CloseClaim(ctx context.Context, store *event.PostgresStore, claimID uuid.UUID, p ClaimClosed) error {
	p.Resolution = strings.TrimSpace(p.Resolution)
	p.ClosedBy = strings.TrimSpace(p.ClosedBy)
	if claimID == uuid.Nil {
		return fmt.Errorf("%w: claim_id required", ErrValidation)
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
		AggregateID:   claimID,
		Type:          EventClaimClosed,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("CloseClaim: append: %w", err)
	}
	return nil
}
