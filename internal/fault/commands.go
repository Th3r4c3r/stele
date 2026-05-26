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

// OpenCase records a new fault case and immediately emits its first
// CaseAssigned via the routing resolver. Status starts as "triage".
//
// openerID is the user who opens the case (e.g., the current logged-in
// user). It is the routing fallback when no rule matches.
func OpenCase(
	ctx context.Context,
	store *event.PostgresStore,
	resolver Resolver,
	openerID uuid.UUID,
	p CaseOpened,
) (uuid.UUID, error) {
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
	if openerID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: opener_id required", ErrValidation)
	}

	caseID := uuid.Must(uuid.NewV7())
	openedPayload, err := MarshalPayload(p)
	if err != nil {
		return uuid.Nil, err
	}
	now := time.Now().UTC()

	// Compute routing BEFORE the first append so we can batch both
	// events atomically. A routing failure aborts the open entirely.
	dec, err := resolver.ResolveForOpen(ctx, RouteInput{
		FaultCode:  p.FaultCode,
		DealerCode: p.Dealer,
		OpenerID:   openerID,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("OpenCase: routing: %w", err)
	}
	assignedPayload, err := MarshalPayload(CaseAssigned{
		AssigneeID: dec.AssigneeID,
		Reason:     dec.Reason,
		RuleName:   dec.RuleName,
	})
	if err != nil {
		return uuid.Nil, err
	}

	evs := []event.Event{
		{
			AggregateType: AggregateType,
			AggregateID:   caseID,
			Type:          EventCaseOpened,
			Payload:       openedPayload,
			OccurredAt:    now,
		},
		{
			AggregateType: AggregateType,
			AggregateID:   caseID,
			Type:          EventCaseAssigned,
			Payload:       assignedPayload,
			OccurredAt:    now,
		},
	}
	if err := store.Append(ctx, evs); err != nil {
		return uuid.Nil, fmt.Errorf("OpenCase: append: %w", err)
	}
	return caseID, nil
}

// Reassign transfers a case to newAssigneeID. transferredFrom should be
// the current assignee (nullable if unknown). Use ReasonManual.
func Reassign(
	ctx context.Context,
	store *event.PostgresStore,
	caseID, newAssigneeID uuid.UUID,
	transferredFrom *uuid.UUID,
) error {
	if caseID == uuid.Nil {
		return fmt.Errorf("%w: case_id required", ErrValidation)
	}
	if newAssigneeID == uuid.Nil {
		return fmt.Errorf("%w: new_assignee required", ErrValidation)
	}
	if transferredFrom != nil && *transferredFrom == newAssigneeID {
		return fmt.Errorf("%w: case already assigned to that user", ErrValidation)
	}
	payload, err := MarshalPayload(CaseAssigned{
		AssigneeID:      newAssigneeID,
		Reason:          ReasonManual,
		TransferredFrom: transferredFrom,
	})
	if err != nil {
		return err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   caseID,
		Type:          EventCaseAssigned,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("Reassign: append: %w", err)
	}
	return nil
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

// RecordPartReplaced appends a PartReplaced event. Resolves the
// part's reference price from the parts master (looked up via priceFn
// callback) so the projector can snapshot it at event time.
func RecordPartReplaced(
	ctx context.Context,
	store *event.PostgresStore,
	caseID, byUserID uuid.UUID,
	pn string, qty int, kind, reason string,
) error {
	pn = strings.TrimSpace(pn)
	if caseID == uuid.Nil || byUserID == uuid.Nil {
		return fmt.Errorf("%w: case_id and by_user required", ErrValidation)
	}
	if pn == "" {
		return fmt.Errorf("%w: pn required", ErrValidation)
	}
	if qty <= 0 {
		return fmt.Errorf("%w: qty must be > 0", ErrValidation)
	}
	switch kind {
	case PartKindWarranty, PartKindGoodwill, PartKindOutOfWarranty:
	default:
		return fmt.Errorf("%w: kind must be warranty|goodwill|out_of_warranty", ErrValidation)
	}
	payload, err := MarshalPayload(PartReplaced{
		PartNumber: pn, Qty: qty, Kind: kind, Reason: strings.TrimSpace(reason),
		ByUserID: byUserID,
	})
	if err != nil {
		return err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   caseID,
		Type:          EventPartReplaced,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("RecordPartReplaced: append: %w", err)
	}
	return nil
}

// RecordPartQuoted appends a PartQuoted event for out-of-warranty.
func RecordPartQuoted(
	ctx context.Context,
	store *event.PostgresStore,
	caseID, byUserID uuid.UUID,
	pn string, qty int, quotedAmountEUR float64,
) error {
	pn = strings.TrimSpace(pn)
	if caseID == uuid.Nil || byUserID == uuid.Nil {
		return fmt.Errorf("%w: case_id and by_user required", ErrValidation)
	}
	if pn == "" {
		return fmt.Errorf("%w: pn required", ErrValidation)
	}
	if qty <= 0 {
		return fmt.Errorf("%w: qty must be > 0", ErrValidation)
	}
	if quotedAmountEUR < 0 {
		return fmt.Errorf("%w: quoted_amount must be >= 0", ErrValidation)
	}
	payload, err := MarshalPayload(PartQuoted{
		PartNumber: pn, Qty: qty, QuotedAmountEUR: quotedAmountEUR, ByUserID: byUserID,
	})
	if err != nil {
		return err
	}
	ev := event.Event{
		AggregateType: AggregateType,
		AggregateID:   caseID,
		Type:          EventPartQuoted,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
	if err := store.Append(ctx, []event.Event{ev}); err != nil {
		return fmt.Errorf("RecordPartQuoted: append: %w", err)
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
