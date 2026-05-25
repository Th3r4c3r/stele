// Package web wires HTTP handlers for the warranty UI.
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
	"github.com/Th3r4c3r/stele/internal/warranty"
	"github.com/Th3r4c3r/stele/internal/web/static"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// Mount registers the warranty UI on the provided ServeMux.
func Mount(mux *http.ServeMux, pool *pgxpool.Pool, store *event.PostgresStore) {
	h := &handlers{pool: pool, store: store}

	mux.HandleFunc("GET /", h.rootRedirect)
	mux.HandleFunc("GET /claims", h.listClaims)
	mux.HandleFunc("GET /claims/new", h.newClaimForm)
	mux.HandleFunc("POST /claims", h.createClaim)
	mux.HandleFunc("GET /claims/{id}", h.showClaim)
	mux.HandleFunc("POST /claims/{id}/notes", h.addNote)
	mux.HandleFunc("POST /claims/{id}/close", h.closeClaim)

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
	http.Redirect(w, r, "/claims", http.StatusFound)
}

func (h *handlers) listClaims(w http.ResponseWriter, r *http.Request) {
	open, err := h.queryClaims(r.Context(), "open")
	if err != nil {
		httpErr(w, err)
		return
	}
	closed, err := h.queryClaims(r.Context(), "closed")
	if err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.ClaimsListPage(open, closed).Render(r.Context(), w)
}

func (h *handlers) newClaimForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.NewClaimPage().Render(r.Context(), w)
}

func (h *handlers) createClaim(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := templates.NewClaimFormData{
		Dealer:      r.PostForm.Get("dealer"),
		VIN:         r.PostForm.Get("vin"),
		FaultCode:   r.PostForm.Get("fault_code"),
		Description: r.PostForm.Get("description"),
	}
	id, err := warranty.OpenClaim(r.Context(), h.store, warranty.ClaimOpened{
		Dealer:      data.Dealer,
		VIN:         data.VIN,
		FaultCode:   data.FaultCode,
		Description: data.Description,
	})
	if errors.Is(err, warranty.ErrValidation) {
		data.ErrorMsg = friendlyValidation(err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = templates.NewClaimForm(data).Render(r.Context(), w)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	// HTMX: tell the client to follow the redirect target.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/claims/"+id.String())
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/claims/"+id.String(), http.StatusSeeOther)
}

func (h *handlers) showClaim(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	row, err := h.queryOneClaim(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Claim event might exist but projection lagging; render a hint.
		http.Error(w, "claim not found (projection may be lagging)", http.StatusNotFound)
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
	_ = templates.ClaimDetailPage(row, timeline).Render(r.Context(), w)
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
	err := warranty.AddNote(r.Context(), h.store, id, warranty.NoteAdded{
		Author: r.PostForm.Get("author"),
		Text:   r.PostForm.Get("text"),
	})
	if errors.Is(err, warranty.ErrValidation) {
		// Re-render the form fragment with the error inline.
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
	// Re-render the timeline fragment for HTMX swap. Brief wait so the
	// runner has a chance to project the new event before we read back.
	if r.Header.Get("HX-Request") == "true" {
		waitForEvent(r.Context(), h.pool, id, "NoteAdded", 3*time.Second)
		timeline, err := h.queryTimeline(r.Context(), id)
		if err != nil {
			httpErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = templates.ClaimTimeline(timeline).Render(r.Context(), w)
		return
	}
	http.Redirect(w, r, "/claims/"+id.String(), http.StatusSeeOther)
}

func (h *handlers) closeClaim(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err := warranty.CloseClaim(r.Context(), h.store, id, warranty.ClaimClosed{
		Resolution: r.PostForm.Get("resolution"),
		ClosedBy:   r.PostForm.Get("closed_by"),
	})
	if errors.Is(err, warranty.ErrValidation) {
		http.Error(w, friendlyValidation(err), http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	http.Redirect(w, r, "/claims/"+id.String(), http.StatusSeeOther)
}

// --- queries ---

func (h *handlers) queryClaims(ctx context.Context, status string) ([]templates.ClaimRow, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT id, status, dealer, vin, fault_code, description,
		       opened_at, closed_at, last_update, note_count
		FROM current_claims
		WHERE status = $1
		ORDER BY opened_at DESC
	`, status)
	if err != nil {
		return nil, fmt.Errorf("queryClaims: %w", err)
	}
	defer rows.Close()
	var out []templates.ClaimRow
	for rows.Next() {
		var c templates.ClaimRow
		var closedAt *time.Time
		if err := rows.Scan(&c.ID, &c.Status, &c.Dealer, &c.VIN, &c.FaultCode, &c.Description,
			&c.OpenedAt, &closedAt, &c.LastUpdate, &c.NoteCount); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		c.ClosedAt = closedAt
		out = append(out, c)
	}
	return out, rows.Err()
}

func (h *handlers) queryOneClaim(ctx context.Context, id uuid.UUID) (templates.ClaimRow, error) {
	var c templates.ClaimRow
	var closedAt *time.Time
	err := h.pool.QueryRow(ctx, `
		SELECT id, status, dealer, vin, fault_code, description,
		       opened_at, closed_at, last_update, note_count
		FROM current_claims
		WHERE id = $1
	`, id).Scan(&c.ID, &c.Status, &c.Dealer, &c.VIN, &c.FaultCode, &c.Description,
		&c.OpenedAt, &closedAt, &c.LastUpdate, &c.NoteCount)
	if err != nil {
		return c, err
	}
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
	v, err := warranty.DecodePayload(eventType, payload)
	if err != nil {
		return ""
	}
	switch x := v.(type) {
	case warranty.ClaimOpened:
		return fmt.Sprintf("Dealer %s opened claim for VIN %s (fault %s)", x.Dealer, x.VIN, x.FaultCode)
	case warranty.NoteAdded:
		return fmt.Sprintf("%s: %s", nonEmpty(x.Author, "system"), x.Text)
	case warranty.ClaimClosed:
		return fmt.Sprintf("Closed by %s — %s", nonEmpty(x.ClosedBy, "system"), x.Resolution)
	default:
		return ""
	}
}

// waitForEvent polls the events table for up to dur waiting for an
// event of the given type on the given aggregate, then waits for the
// projection runner to catch up by polling current_claims.last_event_id.
// Best-effort UX nicety; if it times out the fragment will simply not
// reflect the new note yet.
func waitForEvent(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, eventType string, dur time.Duration) {
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		var lastEventID uuid.UUID
		var maxNoteID uuid.UUID
		row := pool.QueryRow(ctx, `
			SELECT cc.last_event_id, COALESCE(MAX(e.id), '00000000-0000-0000-0000-000000000000'::uuid)
			FROM current_claims cc
			LEFT JOIN events e ON e.aggregate_id = cc.id AND e.type = $2
			WHERE cc.id = $1
			GROUP BY cc.last_event_id
		`, id, eventType)
		if err := row.Scan(&lastEventID, &maxNoteID); err == nil {
			if maxNoteID == uuid.Nil || lastEventID.String() >= maxNoteID.String() {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// --- small helpers ---

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid claim id", http.StatusBadRequest)
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
	// Caller logs via the request-log middleware on the status code.
	_ = json.NewEncoder(_devnull{}).Encode(err.Error())
}

type _devnull struct{}

func (_devnull) Write(p []byte) (int, error) { return len(p), nil }
