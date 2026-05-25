package fault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rule is one assignment_rules row.
type Rule struct {
	ID                uuid.UUID
	Name              string
	Priority          int
	MatchFaultPrefix  string // empty = no fault-prefix predicate
	MatchDealerRegion string // empty = no region predicate
	AssigneeID        uuid.UUID
}

// RouteInput is what Route needs to decide on an assignee.
type RouteInput struct {
	FaultCode    string
	DealerCode   string
	DealerRegion string // pre-resolved; empty if unknown
	OpenerID     uuid.UUID
}

// Decision is the routing output.
type Decision struct {
	AssigneeID uuid.UUID
	Reason     string // one of ReasonOpener / ReasonRule*
	RuleName   string // empty unless ReasonRule*
}

// Route is a pure function: given an input + the active rule set,
// return the assignment decision. Rules are evaluated in priority
// order (lowest first); first match wins. Falls back to opener.
func Route(in RouteInput, rules []Rule) Decision {
	for _, r := range rules {
		if matchRule(r, in) {
			reason := ReasonRuleFaultPrefix
			if r.MatchFaultPrefix == "" && r.MatchDealerRegion != "" {
				reason = ReasonRuleDealerRegion
			}
			return Decision{AssigneeID: r.AssigneeID, Reason: reason, RuleName: r.Name}
		}
	}
	return Decision{AssigneeID: in.OpenerID, Reason: ReasonOpener}
}

func matchRule(r Rule, in RouteInput) bool {
	// Both empty = no-op rule, never matches.
	if r.MatchFaultPrefix == "" && r.MatchDealerRegion == "" {
		return false
	}
	if r.MatchFaultPrefix != "" && !strings.HasPrefix(in.FaultCode, r.MatchFaultPrefix) {
		return false
	}
	if r.MatchDealerRegion != "" && r.MatchDealerRegion != in.DealerRegion {
		return false
	}
	return true
}

// Resolver decides who owns a new case. The web handler and the seeder
// both call ResolveForOpen before OpenCase appends CaseAssigned.
type Resolver interface {
	ResolveForOpen(ctx context.Context, in RouteInput) (Decision, error)
}

// PgResolver loads rules + dealer region from Postgres at each call.
// At ~12 dealers + 4-5 rules the overhead is trivial; cache when it
// grows past hundreds.
type PgResolver struct {
	pool *pgxpool.Pool
}

func NewPgResolver(pool *pgxpool.Pool) *PgResolver {
	return &PgResolver{pool: pool}
}

// ResolveForOpen looks up the dealer region (if any) and the active
// rules, then calls Route.
func (r *PgResolver) ResolveForOpen(ctx context.Context, in RouteInput) (Decision, error) {
	if in.DealerRegion == "" && in.DealerCode != "" {
		region, err := r.regionForDealer(ctx, in.DealerCode)
		if err != nil {
			// Unknown dealer is not fatal: routing falls back to opener.
			if !errors.Is(err, errDealerUnknown) {
				return Decision{}, err
			}
		}
		in.DealerRegion = region
	}
	rules, err := r.activeRules(ctx)
	if err != nil {
		return Decision{}, err
	}
	if in.OpenerID == uuid.Nil {
		return Decision{}, fmt.Errorf("routing: opener_id required")
	}
	return Route(in, rules), nil
}

var errDealerUnknown = errors.New("dealer unknown")

func (r *PgResolver) regionForDealer(ctx context.Context, code string) (string, error) {
	var region string
	err := r.pool.QueryRow(ctx, `SELECT region FROM dealers WHERE code = $1`, code).Scan(&region)
	if err != nil {
		// pgx returns its own ErrNoRows; check by error string to avoid
		// importing pgx solely for this.
		if strings.Contains(err.Error(), "no rows") {
			return "", errDealerUnknown
		}
		return "", fmt.Errorf("regionForDealer: %w", err)
	}
	return region, nil
}

func (r *PgResolver) activeRules(ctx context.Context) ([]Rule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, priority,
		       COALESCE(match_fault_prefix, ''),
		       COALESCE(match_dealer_region, ''),
		       assignee_id
		FROM assignment_rules
		WHERE active
		ORDER BY priority ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("activeRules: %w", err)
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var ru Rule
		if err := rows.Scan(&ru.ID, &ru.Name, &ru.Priority, &ru.MatchFaultPrefix, &ru.MatchDealerRegion, &ru.AssigneeID); err != nil {
			return nil, err
		}
		out = append(out, ru)
	}
	return out, rows.Err()
}

// UpsertRule is the seeder/admin entry point for managing rules.
func (r *PgResolver) UpsertRule(ctx context.Context, ru Rule) error {
	if ru.ID == uuid.Nil {
		ru.ID = uuid.Must(uuid.NewV7())
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO assignment_rules
		    (id, name, priority, match_fault_prefix, match_dealer_region, assignee_id, active)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, true)
		ON CONFLICT (id) DO UPDATE
		   SET name = EXCLUDED.name,
		       priority = EXCLUDED.priority,
		       match_fault_prefix = EXCLUDED.match_fault_prefix,
		       match_dealer_region = EXCLUDED.match_dealer_region,
		       assignee_id = EXCLUDED.assignee_id,
		       active = true
	`, ru.ID, ru.Name, ru.Priority, ru.MatchFaultPrefix, ru.MatchDealerRegion, ru.AssigneeID)
	if err != nil {
		return fmt.Errorf("UpsertRule: %w", err)
	}
	return nil
}
