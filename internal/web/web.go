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

	"github.com/Th3r4c3r/stele/internal/auth"
	"github.com/Th3r4c3r/stele/internal/dealer"
	"github.com/Th3r4c3r/stele/internal/document"
	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/mail"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/web/static"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// Deps is the set of dependencies the web package needs.
type Deps struct {
	Pool       *pgxpool.Pool
	Store      *event.PostgresStore
	Resolver   *fault.PgResolver
	Users      *userpkg.Repo
	Dealers    *dealer.Repo
	Sessions   *auth.Sessions
	Resets     *auth.ResetTokens
	RateLimit  *auth.LoginRateLimit
	MailSender mail.Sender
	DocStore   *document.Storage
	BaseURL    string
}

// Mount registers all routes. The handler tree is built like an onion:
// AuthMiddleware wraps the whole tree and bypasses itself on public
// paths (login/forgot/reset/healthz/static); AdminOnly wraps the
// /admin subtree.
func Mount(mux *http.ServeMux, d Deps) {
	h := &handlers{pool: d.Pool, store: d.Store, resolver: d.Resolver, users: d.Users}
	ah := &authHandlers{
		users:      d.Users,
		sessions:   d.Sessions,
		resets:     d.Resets,
		rateLimit:  d.RateLimit,
		mailSender: d.MailSender,
		baseURL:    d.BaseURL,
	}
	acc := &accountHandlers{users: d.Users, sessions: d.Sessions}
	docs := &docHandlers{pool: d.Pool, store: d.Store, storage: d.DocStore}
	adm := &adminHandlers{
		pool:       d.Pool,
		users:      d.Users,
		dealers:    d.Dealers,
		resolver:   d.Resolver,
		sessions:   d.Sessions,
		resets:     d.Resets,
		mailSender: d.MailSender,
		baseURL:    d.BaseURL,
	}

	authMW := NewAuthMiddleware(d.Sessions, d.Users)
	wrap := func(fn http.HandlerFunc) http.Handler { return authMW.Wrap(fn) }
	wrapAdmin := func(fn http.HandlerFunc) http.Handler {
		return authMW.Wrap(AdminOnly(d.Users, http.HandlerFunc(fn)))
	}

	// Public (auth middleware skips publicPath)
	mux.Handle("GET /login", wrap(ah.loginGET))
	mux.Handle("POST /login", wrap(ah.loginPOST))
	mux.Handle("POST /logout", wrap(ah.logoutPOST))
	mux.Handle("GET /forgot", wrap(ah.forgotGET))
	mux.Handle("POST /forgot", wrap(ah.forgotPOST))
	mux.Handle("GET /reset", wrap(ah.resetGET))
	mux.Handle("POST /reset", wrap(ah.resetPOST))

	// Authenticated
	mux.Handle("GET /", wrap(h.rootRedirect))
	mux.Handle("GET /claims", wrap(h.legacyClaimsRedirect))
	mux.Handle("GET /cases", wrap(h.listCases))
	mux.Handle("GET /cases/new", wrap(h.newCaseForm))
	mux.Handle("POST /cases", wrap(h.createCase))
	mux.Handle("GET /cases/{id}", wrap(h.showCase))
	mux.Handle("POST /cases/{id}/notes", wrap(h.addNote))
	mux.Handle("POST /cases/{id}/classify", wrap(h.classifyCase))
	mux.Handle("POST /cases/{id}/close", wrap(h.closeCase))
	mux.Handle("POST /cases/{id}/transfer", wrap(h.transferCase))
	mux.Handle("POST /cases/{id}/documents", wrap(docs.uploadDocument))
	mux.Handle("GET /documents/{id}/raw", wrap(docs.downloadDocument))
	mux.Handle("POST /documents/{id}/delete", wrap(docs.deleteDocument))

	// Per-user self-service
	mux.Handle("GET /account", wrap(acc.page))
	mux.Handle("POST /account/password", wrap(acc.changePassword))

	// Admin
	mux.Handle("GET /admin", wrapAdmin(adm.overview))
	mux.Handle("GET /admin/users", wrapAdmin(adm.usersList))
	mux.Handle("POST /admin/users", wrapAdmin(adm.usersCreate))
	mux.Handle("GET /admin/users/{id}", wrapAdmin(adm.userEdit))
	mux.Handle("POST /admin/users/{id}", wrapAdmin(adm.userUpdate))
	mux.Handle("POST /admin/users/{id}/reset", wrapAdmin(adm.userResetEmail))
	mux.Handle("POST /admin/users/{id}/deactivate", wrapAdmin(adm.userDeactivate))
	mux.Handle("POST /admin/users/{id}/reactivate", wrapAdmin(adm.userReactivate))
	mux.Handle("GET /admin/rules", wrapAdmin(adm.rulesList))
	mux.Handle("POST /admin/rules", wrapAdmin(adm.rulesCreate))
	mux.Handle("GET /admin/dealers", wrapAdmin(adm.dealersList))
	mux.Handle("POST /admin/dealers", wrapAdmin(adm.dealersCreate))

	staticFS, err := fs.Sub(static.FS, ".")
	if err != nil {
		panic(fmt.Sprintf("static FS sub: %v", err))
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
}

type handlers struct {
	pool     *pgxpool.Pool
	store    *event.PostgresStore
	resolver fault.Resolver
	users    *userpkg.Repo
}

func (h *handlers) rootRedirect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/cases", http.StatusFound)
}

func (h *handlers) legacyClaimsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/cases", http.StatusMovedPermanently)
}

func (h *handlers) listCases(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	switch tab {
	case "mine", "triage", "classified", "closed":
		// ok
	default:
		tab = "triage"
	}
	kindFilter := r.URL.Query().Get("kind")
	if kindFilter != "" && !fault.IsKnownKind(kindFilter) {
		kindFilter = ""
	}

	currentID, _ := userpkg.FromCtx(r.Context())
	currentUser, err := h.users.ByID(r.Context(), currentID)
	if err != nil {
		httpErr(w, err)
		return
	}

	// assignee filter: "" / "all" / "<uuid>" / "unassigned" (mapped to the
	// configured opener-fallback user, i.e., the ops generalist).
	// The filter applies only on triage/classified/closed (not on "mine",
	// which is already an assignee filter).
	assigneeParam := r.URL.Query().Get("assignee")
	var assigneeFilter uuid.UUID
	if tab != "mine" {
		switch assigneeParam {
		case "", "all":
			// no extra filter
		case "unassigned":
			// "Unassigned" = ops-generalist (the default opener). Pragmatic
			// definition: cases that no specialist rule grabbed.
			if opsGen, err := h.users.ByEmail(r.Context(), "yan@stele.local"); err == nil {
				assigneeFilter = opsGen.ID
			}
		default:
			if id, err := uuid.Parse(assigneeParam); err == nil {
				assigneeFilter = id
			}
		}
	}

	triageRows, err := h.queryCases(r.Context(), "triage", "",
		ifThenUUID(tab == "triage", assigneeFilter))
	if err != nil {
		httpErr(w, err)
		return
	}
	classifiedRows, err := h.queryCases(r.Context(), "classified",
		ifThen(tab == "classified", kindFilter, ""),
		ifThenUUID(tab == "classified", assigneeFilter))
	if err != nil {
		httpErr(w, err)
		return
	}
	closedRows, err := h.queryCases(r.Context(), "closed",
		ifThen(tab == "closed", kindFilter, ""),
		ifThenUUID(tab == "closed", assigneeFilter))
	if err != nil {
		httpErr(w, err)
		return
	}
	mineRows, err := h.queryCases(r.Context(), "", "", currentID)
	if err != nil {
		httpErr(w, err)
		return
	}

	if err := h.hydrateAssignees(r.Context(),
		triageRows, classifiedRows, closedRows, mineRows); err != nil {
		httpErr(w, err)
		return
	}

	userOpts, err := h.userOptions(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}

	_ = currentUser // name now flows through nav
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.CasesListPage(navFor(r.Context(), h.users),
		triageRows, classifiedRows, closedRows, mineRows,
		tab, kindFilter, assigneeParam, userOpts).Render(r.Context(), w)
}

func ifThenUUID(cond bool, v uuid.UUID) uuid.UUID {
	if cond {
		return v
	}
	return uuid.Nil
}

func ifThen(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func (h *handlers) newCaseForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.NewCasePage(navFor(r.Context(), h.users)).Render(r.Context(), w)
}

func (h *handlers) createCase(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	openerID, err := userpkg.FromCtx(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	data := templates.NewCaseFormData{
		Dealer:      r.PostForm.Get("dealer"),
		VIN:         r.PostForm.Get("vin"),
		FaultCode:   r.PostForm.Get("fault_code"),
		Description: r.PostForm.Get("description"),
	}
	id, err := fault.OpenCase(r.Context(), h.store, h.resolver, openerID, fault.CaseOpened{
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
	if err := h.hydrateAssignees(r.Context(), []templates.CaseRow{row}); err != nil {
		httpErr(w, err)
		return
	}
	// First hydrate copy is detached; re-fetch the populated row.
	rows := []templates.CaseRow{row}
	_ = h.hydrateAssignees(r.Context(), rows)
	row = rows[0]

	timeline, err := h.queryTimeline(r.Context(), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	userOpts, err := h.userOptions(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	docs, err := h.queryDocuments(r.Context(), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.CaseDetailPage(navFor(r.Context(), h.users), row, timeline, userOpts, docs).Render(r.Context(), w)
}

func (h *handlers) queryDocuments(ctx context.Context, caseID uuid.UUID) ([]templates.DocumentRow, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT d.id, d.filename, d.content_type, d.byte_size, d.attached_at,
		       COALESCE(u.name, d.attached_by_user_id::text)
		FROM current_documents d
		LEFT JOIN users u ON u.id = d.attached_by_user_id
		WHERE d.case_id = $1
		ORDER BY d.attached_at DESC
	`, caseID)
	if err != nil {
		return nil, fmt.Errorf("queryDocuments: %w", err)
	}
	defer rows.Close()
	var out []templates.DocumentRow
	for rows.Next() {
		var d templates.DocumentRow
		if err := rows.Scan(&d.ID, &d.Filename, &d.ContentType, &d.ByteSize, &d.AttachedAt, &d.AttachedByName); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
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

func (h *handlers) transferCase(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newAssigneeStr := r.PostForm.Get("assignee_id")
	newAssignee, err := uuid.Parse(newAssigneeStr)
	if err != nil {
		http.Error(w, "invalid assignee_id", http.StatusBadRequest)
		return
	}
	// Look up current assignee (for transferred_from). Best-effort.
	current, err := h.queryOneCase(r.Context(), id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		httpErr(w, err)
		return
	}
	var from *uuid.UUID
	if current.AssigneeID != nil {
		from = current.AssigneeID
	}
	if err := fault.Reassign(r.Context(), h.store, id, newAssignee, from); err != nil {
		if errors.Is(err, fault.ErrValidation) {
			http.Error(w, friendlyValidation(err), http.StatusUnprocessableEntity)
			return
		}
		httpErr(w, err)
		return
	}
	http.Redirect(w, r, "/cases/"+id.String(), http.StatusSeeOther)
}

// --- queries ---

// queryCases returns the rows in the given status, optionally narrowed
// by kindFilter and/or assigneeFilter. If status is empty AND
// assigneeFilter is set, it acts as the "my cases" lookup (no status
// constraint).
func (h *handlers) queryCases(ctx context.Context, status, kindFilter string, assigneeFilter uuid.UUID) ([]templates.CaseRow, error) {
	args := []any{}
	q := `
		SELECT id, status, kind, dealer, vin, fault_code, description,
		       opened_at, classified_at, closed_at, last_update, note_count, assignee_id
		FROM current_cases
		WHERE 1=1`
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if kindFilter != "" {
		args = append(args, kindFilter)
		q += fmt.Sprintf(" AND kind = $%d", len(args))
	}
	if assigneeFilter != uuid.Nil {
		args = append(args, assigneeFilter)
		q += fmt.Sprintf(" AND assignee_id = $%d", len(args))
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
		var assignee *uuid.UUID
		if err := rows.Scan(&c.ID, &c.Status, &kind, &c.Dealer, &c.VIN, &c.FaultCode, &c.Description,
			&c.OpenedAt, &classifiedAt, &closedAt, &c.LastUpdate, &c.NoteCount, &assignee); err != nil {
			return nil, err
		}
		c.Kind = kind
		c.ClassifiedAt = classifiedAt
		c.ClosedAt = closedAt
		c.AssigneeID = assignee
		out = append(out, c)
	}
	return out, rows.Err()
}

func (h *handlers) queryOneCase(ctx context.Context, id uuid.UUID) (templates.CaseRow, error) {
	var c templates.CaseRow
	var kind *string
	var classifiedAt, closedAt *time.Time
	var assignee *uuid.UUID
	err := h.pool.QueryRow(ctx, `
		SELECT id, status, kind, dealer, vin, fault_code, description,
		       opened_at, classified_at, closed_at, last_update, note_count, assignee_id
		FROM current_cases
		WHERE id = $1
	`, id).Scan(&c.ID, &c.Status, &kind, &c.Dealer, &c.VIN, &c.FaultCode, &c.Description,
		&c.OpenedAt, &classifiedAt, &closedAt, &c.LastUpdate, &c.NoteCount, &assignee)
	if err != nil {
		return c, err
	}
	c.Kind = kind
	c.ClassifiedAt = classifiedAt
	c.ClosedAt = closedAt
	c.AssigneeID = assignee
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
	users, err := h.users.List(ctx)
	if err != nil {
		return nil, err
	}
	nameByID := map[uuid.UUID]string{}
	for _, u := range users {
		nameByID[u.ID] = u.Name
	}
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
			Summary: summarize(typ, payload, nameByID),
		})
	}
	return out, rows.Err()
}

// hydrateAssignees fills the AssigneeName field on every row across
// the supplied slices using one query.
func (h *handlers) hydrateAssignees(ctx context.Context, slices ...[]templates.CaseRow) error {
	ids := map[uuid.UUID]struct{}{}
	for _, s := range slices {
		for _, r := range s {
			if r.AssigneeID != nil {
				ids[*r.AssigneeID] = struct{}{}
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	users, err := h.users.List(ctx) // small set; one query
	if err != nil {
		return err
	}
	nameByID := map[uuid.UUID]string{}
	for _, u := range users {
		nameByID[u.ID] = u.Name
	}
	for _, s := range slices {
		for i := range s {
			if s[i].AssigneeID != nil {
				s[i].AssigneeName = nameByID[*s[i].AssigneeID]
			}
		}
	}
	return nil
}

func (h *handlers) userOptions(ctx context.Context) ([]templates.UserOption, error) {
	users, err := h.users.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]templates.UserOption, 0, len(users))
	for _, u := range users {
		label := u.Name
		if u.Role != "" {
			label = u.Name + " (" + u.Role + ")"
		}
		out = append(out, templates.UserOption{ID: u.ID, Label: label})
	}
	return out, nil
}

func summarize(eventType string, payload []byte, nameByID map[uuid.UUID]string) string {
	// Try fault events first. Fall back to document events. Other
	// packages can register more decoders here as the domain grows.
	v, err := fault.DecodePayload(eventType, payload)
	if err != nil {
		if dv, derr := document.DecodePayload(eventType, payload); derr == nil {
			return summarizeDocument(dv, nameByID)
		}
		return ""
	}
	switch x := v.(type) {
	case fault.CaseOpened:
		return fmt.Sprintf("Dealer %s opened a case for VIN %s (fault %s)", x.Dealer, x.VIN, x.FaultCode)
	case fault.NoteAdded:
		return fmt.Sprintf("%s: %s", nonEmpty(x.Author, "system"), x.Text)
	case fault.Classified:
		return fmt.Sprintf("Classified as %s — %s", x.Kind, x.Reasoning)
	case fault.CaseAssigned:
		assignee := nameByID[x.AssigneeID]
		if assignee == "" {
			assignee = x.AssigneeID.String()[:8]
		}
		var sb strings.Builder
		sb.WriteString("Assigned to ")
		sb.WriteString(assignee)
		sb.WriteString(" (")
		sb.WriteString(x.Reason)
		if x.RuleName != "" {
			sb.WriteString(" via rule '")
			sb.WriteString(x.RuleName)
			sb.WriteString("'")
		}
		sb.WriteString(")")
		if x.TransferredFrom != nil {
			prev := nameByID[*x.TransferredFrom]
			if prev == "" {
				prev = x.TransferredFrom.String()[:8]
			}
			sb.WriteString(", transferred from ")
			sb.WriteString(prev)
		}
		return sb.String()
	case fault.CaseClosed:
		return fmt.Sprintf("Closed by %s — %s", nonEmpty(x.ClosedBy, "system"), x.Resolution)
	default:
		return ""
	}
}

func summarizeDocument(v any, nameByID map[uuid.UUID]string) string {
	switch d := v.(type) {
	case document.DocumentAttached:
		who := nameByID[d.AttachedByUserID]
		if who == "" {
			who = d.AttachedByUserID.String()[:8]
		}
		return fmt.Sprintf("%s uploaded %s (%s, %s)",
			who, d.OriginalFilename, d.ContentType, humanBytes(d.ByteSize))
	case document.DocumentRedacted:
		who := nameByID[d.RedactedByUserID]
		if who == "" {
			who = d.RedactedByUserID.String()[:8]
		}
		s := fmt.Sprintf("%s removed %s", who, d.OriginalFilename)
		if d.Reason != "" {
			s += " (reason: " + d.Reason + ")"
		}
		return s
	default:
		return ""
	}
}

func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(k))
	case n < k*k*k:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(k*k))
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(k*k*k))
	}
}

// waitForCaseAdvance polls until the projection catches up.
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
