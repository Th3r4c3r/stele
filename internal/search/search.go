// Package search runs the global search over cases, notes, and
// documents. See docs/adr/0011-global-search.md.
package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Result groups the three searchable surfaces.
type Result struct {
	Term      string
	Cases     []CaseHit
	Notes     []NoteHit
	Documents []DocumentHit
}

// CaseHit is a fault_case that matched on one of its top-level fields.
type CaseHit struct {
	CaseID    uuid.UUID
	Field     string // "vin" / "dealer" / "fault_code" / "description"
	Snippet   string // substring with the term, ~80 chars
	Status    string
	Kind      *string
	Dealer    string
	VIN       string
	FaultCode string
}

// NoteHit is a NoteAdded event whose text matched.
type NoteHit struct {
	CaseID    uuid.UUID
	OccurredAt time.Time
	Author    string
	Snippet   string
}

// DocumentHit is a current_documents row whose filename matched.
type DocumentHit struct {
	DocumentID uuid.UUID
	CaseID     uuid.UUID
	Filename   string
	ContentType string
	AttachedAt time.Time
}

// Service is the search entry point. Pool-backed; no cache.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Find returns the grouped result. Empty term yields an empty result
// without hitting the DB. Term is clipped to 200 chars and trimmed.
func (s *Service) Find(ctx context.Context, raw string) (Result, error) {
	term := strings.TrimSpace(raw)
	if len(term) > 200 {
		term = term[:200]
	}
	out := Result{Term: term}
	if len(term) < 2 {
		return out, nil
	}

	cases, err := s.findCases(ctx, term)
	if err != nil {
		return out, err
	}
	out.Cases = cases

	notes, err := s.findNotes(ctx, term)
	if err != nil {
		return out, err
	}
	out.Notes = notes

	docs, err := s.findDocuments(ctx, term)
	if err != nil {
		return out, err
	}
	out.Documents = docs

	return out, nil
}

func (s *Service) findCases(ctx context.Context, term string) ([]CaseHit, error) {
	pattern := "%" + term + "%"
	rows, err := s.pool.Query(ctx, `
		SELECT id, status, kind, dealer, vin, fault_code, description,
		       CASE
		         WHEN vin         ILIKE $1 THEN 'vin'
		         WHEN dealer      ILIKE $1 THEN 'dealer'
		         WHEN fault_code  ILIKE $1 THEN 'fault_code'
		         WHEN description ILIKE $1 THEN 'description'
		       END AS matched_field,
		       CASE
		         WHEN description ILIKE $1 THEN description
		         ELSE ''
		       END AS desc_for_snippet
		FROM current_cases
		WHERE vin         ILIKE $1
		   OR dealer      ILIKE $1
		   OR fault_code  ILIKE $1
		   OR description ILIKE $1
		ORDER BY opened_at DESC
		LIMIT 50
	`, pattern)
	if err != nil {
		return nil, fmt.Errorf("search.findCases: %w", err)
	}
	defer rows.Close()
	var out []CaseHit
	for rows.Next() {
		var h CaseHit
		var desc string
		var kind *string
		if err := rows.Scan(&h.CaseID, &h.Status, &kind, &h.Dealer, &h.VIN, &h.FaultCode,
			&desc, &h.Field, &desc); err != nil {
			return nil, err
		}
		h.Kind = kind
		h.Snippet = snippet(desc, term, 80)
		if h.Snippet == "" {
			h.Snippet = matchedFieldValue(h, h.Field)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func matchedFieldValue(h CaseHit, field string) string {
	switch field {
	case "vin":
		return h.VIN
	case "dealer":
		return h.Dealer
	case "fault_code":
		return h.FaultCode
	default:
		return ""
	}
}

func (s *Service) findNotes(ctx context.Context, term string) ([]NoteHit, error) {
	pattern := "%" + term + "%"
	rows, err := s.pool.Query(ctx, `
		SELECT aggregate_id,
		       occurred_at,
		       payload->>'author'  AS author,
		       payload->>'text'    AS text
		FROM events
		WHERE type = 'NoteAdded'
		  AND aggregate_type = 'fault_case'
		  AND payload->>'text' ILIKE $1
		ORDER BY occurred_at DESC
		LIMIT 50
	`, pattern)
	if err != nil {
		return nil, fmt.Errorf("search.findNotes: %w", err)
	}
	defer rows.Close()
	var out []NoteHit
	for rows.Next() {
		var h NoteHit
		var author, text *string
		if err := rows.Scan(&h.CaseID, &h.OccurredAt, &author, &text); err != nil {
			return nil, err
		}
		if author != nil {
			h.Author = *author
		}
		if text != nil {
			h.Snippet = snippet(*text, term, 100)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Service) findDocuments(ctx context.Context, term string) ([]DocumentHit, error) {
	pattern := "%" + term + "%"
	rows, err := s.pool.Query(ctx, `
		SELECT id, case_id, filename, content_type, attached_at
		FROM current_documents
		WHERE filename ILIKE $1
		ORDER BY attached_at DESC
		LIMIT 50
	`, pattern)
	if err != nil {
		return nil, fmt.Errorf("search.findDocuments: %w", err)
	}
	defer rows.Close()
	var out []DocumentHit
	for rows.Next() {
		var h DocumentHit
		if err := rows.Scan(&h.DocumentID, &h.CaseID, &h.Filename, &h.ContentType, &h.AttachedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// snippet returns a context window of ~maxLen chars around the first
// case-insensitive occurrence of term in s. The matched term is
// preserved with its original case. Returns "" if not found.
func snippet(s, term string, maxLen int) string {
	if s == "" || term == "" {
		return ""
	}
	idx := strings.Index(strings.ToLower(s), strings.ToLower(term))
	if idx < 0 {
		return ""
	}
	half := (maxLen - len(term)) / 2
	if half < 0 {
		half = 0
	}
	start := idx - half
	if start < 0 {
		start = 0
	}
	end := idx + len(term) + half
	if end > len(s) {
		end = len(s)
	}
	out := s[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(s) {
		out = out + "…"
	}
	return out
}
