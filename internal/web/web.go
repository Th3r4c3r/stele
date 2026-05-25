// Package web wires HTTP handlers for the fault-case UI.
//
// Handlers follow the page/fragment split from ADR-006 D3: every
// endpoint can be invoked with or without HTMX, returning the
// appropriate shape based on the HX-Request header.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/web/static"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// Mount registers the fault-case UI on the provided ServeMux.
func Mount(mux *http.ServeMux, pool *pgxpool.Pool, store *event.PostgresStore) {
	h := &handlers{pool: pool, store: store}

	mux.HandleFunc("GET /", h.rootRedirect)
	mux.HandleFunc("GET /claims", h.legacyClaimsRedirect)
	mux.HandleFunc("GET /cases", h.listCases)
	mux.HandleFunc("GET /cases/new", h.newCaseForm)
	mux.HandleFunc("POST /cases", h.createCase)
	mux.HandleFunc("GET /cases/{id}", h.showCase)
	mux.HandleFunc("POST /cases/{id}/notes", h.addNote)
	mux.HandleFunc("POST /cases/{id}/classify", h.classifyCase)
	mux.HandleFunc("POST /cases/{id}/close", h.closeCase)

	staticFS, err := fs.Sub(static.FS, ".")
	if err != nil {
		panic(fmt.Sprintf("static FS sub: %v", err))
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
}

type handlers struct {
	pool  *pgxpool.Pool
	store *event.PostgresStore
}

func (h *handlers) rootRedirect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/cases", http.StatusFound)
}

// legacyClaimsRedirect: ADR-007 keeps /claims for one release as a 301
// to /cases so any bookmarks survive the rename.
func (h *handlers) legacyClaimsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/cases", http.StatusMovedPermanently)
}

func (h *handlers) listCases(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	if tab != "triage" && tab != "classified" && tab != "closed" {
		tab = "triage"
	}
	kindFilter := r.URL.Query().Get("kind")
	if kindFilter != "" && !fault.IsKnownKind(kindFilter) {
		kindFilter = ""
	}

	// Always load counts for all three tabs; load rows only for the active one.
	triageRows, err := h.queryCases(r.Context(), "triage", "")
	if err != nil {
		httpErr(w, err)
		return
	}
	classifiedRows, err := h.queryCases(r.Context(), "classified",
		ifThen(tab == "classified", kindFilter, ""))
	if err != nil {
		httpErr(w, err)
		return
	}
	closedRows, err := h.queryCases(r.Context(), "closed",
		ifThen(tab == "closed", kindFilter, ""))
	if err != nil {
		httpErr(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.CasesListPage(triageRows, classifiedRows, closedRows, tab, kindFilter).Render(r.Context(), w)
}

func ifThen(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func (h *handlers) newCaseForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.NewCasePage().Render(r.Context(), w)
}

func (h *handlers) createCase(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := templates.NewCaseFormData{
		Dealer:      r.PostForm.Get("dealer"),
		VIN:         r.PostForm.Get("vin"),
		FaultCode:   r.PostForm.Get("fault_code"),
		Description: r.PostForm.Get("description"),
	}
	id, err := fault.OpenCase(r.Context(), h.store, fault.CaseOpened{
		Dealer:      data.Dealer,
		VIN:         data.VIN,
		FaultCode:   data.FaultCode,
		Description: data.Description,
	})
	if errors.Is(err, fault.ErrValidation) {
		data.ErrorMsg = friendlyValidation(err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = templates.NewCaseForm(data).Render(r.Context(), w)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/cases/"+id.String())
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/cases/"+id.String(), http.StatusSeeOther)
}

func (h *handlers) showCase(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	row, err := h.queryOneCase(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "case not found (projection may be lagging)", http.StatusNotFound)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	timeline, err := h.queryTimeline(r.Context(), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.CaseDetailPage(row, timeline).Render(r.Context(), w)
}

func (h *handlers) addNote(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err := fault.AddNote(r.Context(), h.store, id, fault.NoteAdded{
		Author: r.PostForm.Get("author"),
		Text:   r.PostForm.Get("text"),
	})
	if errors.Is(err, fault.ErrValidation) {
		data := templates.NoteFormData{
			Author:   r.PostForm.Get("author"),
			Text:     r.PostForm.Get("text"),
			ErrorMsg: friendlyValidation(err),
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = templates.NoteForm(id, data).Render(r.Context(), w)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		waitForCaseAdvance(r.Context(), h.pool, id, 3*time.Second)
		timeline, err := h.queryTimeline(r.Context(), id)
		if err != nil {
			httpErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = templates.CaseTimeline(timeline).Render(r.Context(), w)
		return
	}
	http.Redirect(w, r, "/cases/"+id.String(), http.StatusSeeOther)
}

func (h *handlers) classifyCase(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err := fault.Classify(r.Context(), h.store, id, fault.Classified{
		Kind:      r.PostForm.Get("kind"),
		Reasoning: r.PostForm.Get("reasoning"),
	})
	if errors.Is(err, fault.ErrValidation) {
		http.Error(w, friendlyValidation(err), http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	http.Redirect(w, r, "/cases/"+id.String(), http.StatusSeeOther)
}

func (h *handlers) closeCase(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err := fault.CloseCase(r.Context(), h.store, id, fault.CaseClosed{
		Resolution: r.PostForm.Get("resolution"),
		ClosedBy:   r.PostForm.Get("closed_by"),
	})
	if errors.Is(err, fault.ErrValidation) {
		http.Error(w, friendlyValidation(err), http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	http.Redirect(w, r, "/cases/"+id.String(), http.StatusSeeOther)
}

// --- queries ---

// queryCases returns the rows in the given status. If kindFilter is
// non-empty, only rows with that kind are returned (used inside the
// classified/closed tabs).
func (h *handlers) queryCases(ctx context.Context, status, kindFilter string) ([]templates.CaseRow, error) {
	args := []any{status}
	q := `
		SELECT id, status, kind, dealer, vin, fault_code, description,
		       opened_at, classified_at, closed_at, last_update, note_count
		FROM current_cases
		WHERE status = $1`
	if kindFilter != "" {
		q += ` AND kind = $2`
		args = append(args, kindFilter)
	}
	q += ` ORDER BY opened_at DESC`
	rows, err := h.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("queryCases: %w", err)
	}
	defer rows.Close()
	var out []templates.CaseRow
	for rows.Next() {
		var c templates.CaseRow
		var kind *string
		var classifiedAt, closedAt *time.Time
		if err := rows.Scan(&c.ID, &c.Status, &kind, &c.Dealer, &c.VIN, &c.FaultCode, &c.Description,
			&c.OpenedAt, &classifiedAt, &closedAt, &c.LastUpdate, &c.NoteCount); err != nil {
			return nil, err
		}
		c.Kind = kind
		c.ClassifiedAt = classifiedAt
		c.ClosedAt = closedAt
		out = append(out, c)
	}
	return out, rows.Err()
}

func (h *handlers) queryOneCase(ctx context.Context, id uuid.UUID) (templates.CaseRow, error) {
	var c templates.CaseRow
	var kind *string
	var classifiedAt, closedAt *time.Time
	err := h.pool.QueryRow(ctx, `
		SELECT id, status, kind, dealer, vin, fault_code, description,
		       opened_at, classified_at, closed_at, last_update, note_count
		FROM current_cases
		WHERE id = $1
	`, id).Scan(&c.ID, &c.Status, &kind, &c.Dealer, &c.VIN, &c.FaultCode, &c.Description,
		&c.OpenedAt, &classifiedAt, &closedAt, &c.LastUpdate, &c.NoteCount)
	if err != nil {
		return c, err
	}
	c.Kind = kind
	c.ClassifiedAt = classifiedAt
	c.ClosedAt = closedAt
	return c, nil
}

func (h *handlers) queryTimeline(ctx context.Context, id uuid.UUID) ([]templates.TimelineEntry, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT type, payload, occurred_at
		FROM events
		WHERE aggregate_id = $1
		ORDER BY occurred_at ASC, id ASC
	`, id)
	if err != nil {
		return nil, fmt.Errorf("queryTimeline: %w", err)
	}
	defer rows.Close()
	var out []templates.TimelineEntry
	for rows.Next() {
		var typ string
		var payload []byte
		var when time.Time
		if err := rows.Scan(&typ, &payload, &when); err != nil {
			return nil, err
		}
		out = append(out, templates.TimelineEntry{
			When:    when,
			Type:    typ,
			Summary: summarize(typ, payload),
		})
	}
	return out, rows.Err()
}

func summarize(eventType string, payload []byte) string {
	v, err := fault.DecodePayload(eventType, payload)
	if err != nil {
		return ""
	}
	switch x := v.(type) {
	case fault.CaseOpened:
		return fmt.Sprintf("Dealer %s opened a case for VIN %s (fault %s)", x.Dealer, x.VIN, x.FaultCode)
	case fault.NoteAdded:
		return fmt.Sprintf("%s: %s", nonEmpty(x.Author, "system"), x.Text)
	case fault.Classified:
		return fmt.Sprintf("Classified as %s — %s", x.Kind, x.Reasoning)
	case fault.CaseClosed:
		return fmt.Sprintf("Closed by %s — %s", nonEmpty(x.ClosedBy, "system"), x.Resolution)
	default:
		return ""
	}
}

// waitForCaseAdvance polls current_cases.last_event_id until it has
// caught up to the latest event id for the given case, or the timeout
// expires. Best-effort UX nicety so the HTMX fragment reflects the
// note we just posted.
func waitForCaseAdvance(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, dur time.Duration) {
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		var cursorID, maxID uuid.UUID
		err := pool.QueryRow(ctx, `
			SELECT cc.last_event_id, COALESCE(MAX(e.id), cc.last_event_id)
			FROM current_cases cc
			LEFT JOIN events e ON e.aggregate_id = cc.id
			WHERE cc.id = $1
			GROUP BY cc.last_event_id
		`, id).Scan(&cursorID, &maxID)
		if err == nil && cursorID.String() >= maxID.String() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// --- small helpers ---

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid case id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func friendlyValidation(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "validation: "); i >= 0 {
		return strings.TrimSpace(msg[i+len("validation: "):])
	}
	return msg
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func httpErr(w http.ResponseWriter, err error) {
	http.Error(w, "internal error", http.StatusInternalServerError)
	_ = json.NewEncoder(_devnull{}).Encode(err.Error())
}

type _devnull struct{}

func (_devnull) Write(p []byte) (int, error) { return len(p), nil }
