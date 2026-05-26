package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/audit"
	"github.com/Th3r4c3r/stele/internal/auth"
	"github.com/Th3r4c3r/stele/internal/dealer"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/mail"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// adminHandlers serves /admin/*. All routes are gated by AdminOnly.
type adminHandlers struct {
	pool       *pgxpool.Pool
	users      *userpkg.Repo
	dealers    *dealer.Repo
	resolver   *fault.PgResolver
	sessions   *auth.Sessions
	resets     *auth.ResetTokens
	mailSender mail.Sender
	baseURL    string
	audit      *audit.Repo // set by Mount; used by auditList read path
}

func (a *adminHandlers) overview(w http.ResponseWriter, r *http.Request) {
	var active, deactivated, dealers, rules, sessions, recallCodes int
	_ = a.pool.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE deactivated_at IS NULL`).Scan(&active)
	_ = a.pool.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE deactivated_at IS NOT NULL`).Scan(&deactivated)
	_ = a.pool.QueryRow(r.Context(), `SELECT count(*) FROM dealers`).Scan(&dealers)
	_ = a.pool.QueryRow(r.Context(), `SELECT count(*) FROM assignment_rules`).Scan(&rules)
	_ = a.pool.QueryRow(r.Context(), `SELECT count(*) FROM sessions WHERE expires_at > now()`).Scan(&sessions)
	_ = a.pool.QueryRow(r.Context(), `SELECT count(DISTINCT recall_code) FROM vehicle_recalls`).Scan(&recallCodes)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminOverview(navFor(r.Context(), a.users), active, deactivated, dealers, rules, sessions, recallCodes).Render(r.Context(), w)
}

// --- recalls ---

// recallsList aggregates the recall master + current_cases to show
// the operator a scoreboard: per recall code, how many VINs are
// affected and how many of those VINs already have at least one
// open/closed case. Single GROUP BY query, ordered by VINCount DESC
// so the most-applied recalls float to the top.
func (a *adminHandlers) recallsList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.pool.Query(r.Context(), `
		WITH vin_status AS (
			SELECT vr.recall_code,
			       vr.vin,
			       BOOL_OR(c.status IN ('triage','classified')) AS has_open,
			       count(c.id) FILTER (WHERE c.status IN ('triage','classified')) AS open_cnt,
			       count(c.id) FILTER (WHERE c.status = 'closed') AS closed_cnt
			FROM vehicle_recalls vr
			LEFT JOIN current_cases c ON c.vin = vr.vin
			GROUP BY vr.recall_code, vr.vin
		)
		SELECT recall_code,
		       count(*)                                  AS vin_count,
		       count(*) FILTER (WHERE has_open)          AS vins_with_open,
		       COALESCE(sum(open_cnt), 0)                AS open_cases,
		       COALESCE(sum(closed_cnt), 0)              AS closed_cases
		FROM vin_status
		GROUP BY recall_code
		ORDER BY vin_count DESC, recall_code ASC
	`)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	var items []templates.AdminRecallSummary
	for rows.Next() {
		var s templates.AdminRecallSummary
		if err := rows.Scan(&s.Code, &s.VINCount, &s.VINsWithOpenCase, &s.OpenCases, &s.ClosedCases); err != nil {
			httpErr(w, err)
			return
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminRecallsPage(navFor(r.Context(), a.users), items).Render(r.Context(), w)
}

// recallDetail lists every VIN tagged with a given recall code,
// ordered by "has open cases first" so urgent rows surface to the
// top. Capped at 500 rows: a recall touching > 500 VINs is rare
// enough that pagination is a follow-up if it ever comes up.
const recallDetailCap = 500

func (a *adminHandlers) recallDetail(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.PathValue("code"))
	if code == "" {
		http.NotFound(w, r)
		return
	}
	rows, err := a.pool.Query(r.Context(), `
		SELECT v.vin,
		       m.name                                                          AS model_name,
		       v.color,
		       v.sold_at,
		       count(c.id) FILTER (WHERE c.status IN ('triage','classified')) AS open_cases,
		       count(c.id)                                                     AS total_cases,
		       (SELECT id          FROM current_cases x WHERE x.vin = v.vin ORDER BY x.opened_at DESC LIMIT 1) AS latest_id,
		       (SELECT case_number FROM current_cases x WHERE x.vin = v.vin ORDER BY x.opened_at DESC LIMIT 1) AS latest_num
		FROM vehicle_recalls vr
		JOIN vehicles       v ON v.vin       = vr.vin
		JOIN vehicle_models m ON m.code      = v.model_code
		LEFT JOIN current_cases c ON c.vin   = v.vin
		WHERE vr.recall_code = $1
		GROUP BY v.vin, m.name, v.color, v.sold_at
		ORDER BY (count(c.id) FILTER (WHERE c.status IN ('triage','classified'))) DESC,
		         v.vin ASC
		LIMIT $2
	`, code, recallDetailCap+1) // +1 so we know if we hit the cap
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	var out []templates.AdminRecallVINRow
	for rows.Next() {
		var x templates.AdminRecallVINRow
		if err := rows.Scan(&x.VIN, &x.ModelName, &x.Color, &x.SoldAt,
			&x.OpenCases, &x.TotalCases, &x.LatestCaseID, &x.LatestCaseNumber); err != nil {
			httpErr(w, err)
			return
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		httpErr(w, err)
		return
	}
	capped := false
	if len(out) > recallDetailCap {
		out = out[:recallDetailCap]
		capped = true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminRecallDetailPage(navFor(r.Context(), a.users), code, out, capped).Render(r.Context(), w)
}

// --- users ---

func (a *adminHandlers) usersList(w http.ResponseWriter, r *http.Request) {
	users, err := a.users.ListAll(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	rows := make([]templates.AdminUser, 0, len(users))
	for _, u := range users {
		rows = append(rows, toAdminUser(u))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminUsersPage(navFor(r.Context(), a.users), rows, templates.AdminUserFormData{Role: "ops"}).Render(r.Context(), w)
}

func (a *adminHandlers) usersCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := parseUserForm(r)
	if data.Email == "" || data.Name == "" {
		data.ErrorMsg = "email and name are required"
		a.usersRenderListWithForm(w, r, data)
		return
	}
	if _, err := a.users.ByEmail(r.Context(), data.Email); err == nil {
		data.ErrorMsg = "a user with that email already exists"
		a.usersRenderListWithForm(w, r, data)
		return
	}

	u := userpkg.User{
		Email:           data.Email,
		Name:            data.Name,
		Role:            data.Role,
		Specializations: splitCommas(data.Specializations),
	}
	if data.Region != "" {
		region := data.Region
		u.Region = &region
	}
	if err := a.users.Upsert(r.Context(), u); err != nil {
		httpErr(w, err)
		return
	}
	// Send a password-set link so the invitee chooses their own password.
	created, err := a.users.ByEmail(r.Context(), data.Email)
	if err != nil {
		httpErr(w, err)
		return
	}
	token, err := a.resets.Create(r.Context(), created.ID)
	if err != nil {
		httpErr(w, err)
		return
	}
	link := a.baseURL + "/reset?token=" + token
	_ = a.mailSender.Send(created.Email, "Stele — set your password",
		"Hi "+created.Name+",\n\nAn admin invited you to Stele.\nUse this link to choose your password (valid for 1 hour):\n"+link+"\n")
	audit.SetSummary(r.Context(), "invited user "+created.Email+" (role "+created.Role+")")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *adminHandlers) usersRenderListWithForm(w http.ResponseWriter, r *http.Request, data templates.AdminUserFormData) {
	users, err := a.users.ListAll(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	rows := make([]templates.AdminUser, 0, len(users))
	for _, u := range users {
		rows = append(rows, toAdminUser(u))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = templates.AdminUsersPage(navFor(r.Context(), a.users), rows, data).Render(r.Context(), w)
}

func (a *adminHandlers) userEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAdminID(w, r, "id")
	if !ok {
		return
	}
	u, err := a.users.ByID(r.Context(), id)
	if errors.Is(err, userpkg.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminUserEditPage(navFor(r.Context(), a.users), toAdminUser(u), prefillUserForm(u)).Render(r.Context(), w)
}

func (a *adminHandlers) userUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAdminID(w, r, "id")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	existing, err := a.users.ByID(r.Context(), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	data := parseUserForm(r)
	if data.Email == "" || data.Name == "" {
		http.Error(w, "email and name required", http.StatusBadRequest)
		return
	}
	existing.Email = data.Email
	existing.Name = data.Name
	existing.Role = data.Role
	if data.Region != "" {
		region := data.Region
		existing.Region = &region
	} else {
		existing.Region = nil
	}
	existing.Specializations = splitCommas(data.Specializations)
	if err := a.users.Upsert(r.Context(), existing); err != nil {
		httpErr(w, err)
		return
	}
	audit.SetSummary(r.Context(), "updated user "+existing.Email+" (role "+existing.Role+")")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *adminHandlers) userResetEmail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAdminID(w, r, "id")
	if !ok {
		return
	}
	u, err := a.users.ByID(r.Context(), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	token, err := a.resets.Create(r.Context(), u.ID)
	if err != nil {
		httpErr(w, err)
		return
	}
	link := a.baseURL + "/reset?token=" + token
	_ = a.mailSender.Send(u.Email, "Stele — set your password",
		"Hi "+u.Name+",\n\nAn admin requested a password reset.\nUse this link (valid for 1 hour):\n"+link+"\n")
	audit.SetSummary(r.Context(), "sent password-reset email to "+u.Email)
	http.Redirect(w, r, "/admin/users/"+id.String(), http.StatusSeeOther)
}

func (a *adminHandlers) userDeactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAdminID(w, r, "id")
	if !ok {
		return
	}
	if err := a.users.Deactivate(r.Context(), id); err != nil {
		httpErr(w, err)
		return
	}
	_ = a.sessions.InvalidateAllForUser(r.Context(), id)
	if u, err := a.users.ByID(r.Context(), id); err == nil {
		audit.SetSummary(r.Context(), "deactivated user "+u.Email+" (sessions revoked)")
	} else {
		audit.SetSummary(r.Context(), "deactivated user "+id.String())
	}
	http.Redirect(w, r, "/admin/users/"+id.String(), http.StatusSeeOther)
}

func (a *adminHandlers) userReactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAdminID(w, r, "id")
	if !ok {
		return
	}
	if err := a.users.Reactivate(r.Context(), id); err != nil {
		httpErr(w, err)
		return
	}
	if u, err := a.users.ByID(r.Context(), id); err == nil {
		audit.SetSummary(r.Context(), "reactivated user "+u.Email)
	} else {
		audit.SetSummary(r.Context(), "reactivated user "+id.String())
	}
	http.Redirect(w, r, "/admin/users/"+id.String(), http.StatusSeeOther)
}

// --- rules ---

func (a *adminHandlers) rulesList(w http.ResponseWriter, r *http.Request) {
	rules, err := a.queryRules(r)
	if err != nil {
		httpErr(w, err)
		return
	}
	assignees, err := a.assigneeOptions(r)
	if err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminRulesPage(navFor(r.Context(), a.users), rules, assignees, templates.AdminRuleFormData{Priority: "50"}).Render(r.Context(), w)
}

func (a *adminHandlers) rulesCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := templates.AdminRuleFormData{
		Name:              strings.TrimSpace(r.PostForm.Get("name")),
		Priority:          r.PostForm.Get("priority"),
		MatchFaultPrefix:  strings.TrimSpace(r.PostForm.Get("match_fault_prefix")),
		MatchDealerRegion: strings.TrimSpace(r.PostForm.Get("match_dealer_region")),
		AssigneeID:        r.PostForm.Get("assignee_id"),
	}
	renderErr := func(msg string) {
		rules, _ := a.queryRules(r)
		assignees, _ := a.assigneeOptions(r)
		data.ErrorMsg = msg
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = templates.AdminRulesPage(navFor(r.Context(), a.users), rules, assignees, data).Render(r.Context(), w)
	}
	priority, err := strconv.Atoi(data.Priority)
	if err != nil || priority < 1 {
		renderErr("priority must be a positive integer")
		return
	}
	if data.MatchFaultPrefix == "" && data.MatchDealerRegion == "" {
		renderErr("at least one predicate (fault prefix or dealer region) must be set")
		return
	}
	assigneeID, err := uuid.Parse(data.AssigneeID)
	if err != nil {
		renderErr("invalid assignee")
		return
	}
	if err := a.resolver.UpsertRule(r.Context(), fault.Rule{
		Name: data.Name, Priority: priority,
		MatchFaultPrefix: data.MatchFaultPrefix, MatchDealerRegion: data.MatchDealerRegion,
		AssigneeID: assigneeID,
	}); err != nil {
		httpErr(w, err)
		return
	}
	audit.SetSummary(r.Context(), fmt.Sprintf("created/updated rule %q (priority %d, prefix=%q region=%q)",
		data.Name, priority, data.MatchFaultPrefix, data.MatchDealerRegion))
	http.Redirect(w, r, "/admin/rules", http.StatusSeeOther)
}

func (a *adminHandlers) queryRules(r *http.Request) ([]templates.AdminRule, error) {
	rows, err := a.pool.Query(r.Context(), `
		SELECT r.id, r.name, r.priority,
		       COALESCE(r.match_fault_prefix, ''),
		       COALESCE(r.match_dealer_region, ''),
		       u.name, r.assignee_id, r.active
		FROM assignment_rules r
		JOIN users u ON u.id = r.assignee_id
		ORDER BY r.priority ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("queryRules: %w", err)
	}
	defer rows.Close()
	var out []templates.AdminRule
	for rows.Next() {
		var ar templates.AdminRule
		if err := rows.Scan(&ar.ID, &ar.Name, &ar.Priority, &ar.MatchFaultPrefix,
			&ar.MatchDealerRegion, &ar.AssigneeName, &ar.AssigneeID, &ar.Active); err != nil {
			return nil, err
		}
		out = append(out, ar)
	}
	return out, rows.Err()
}

func (a *adminHandlers) assigneeOptions(r *http.Request) ([]templates.UserOption, error) {
	users, err := a.users.List(r.Context())
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

// --- dealers ---

func (a *adminHandlers) dealersList(w http.ResponseWriter, r *http.Request) {
	dealers, err := a.dealers.List(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	rows := make([]templates.AdminDealer, 0, len(dealers))
	for _, d := range dealers {
		rows = append(rows, templates.AdminDealer{Code: d.Code, Name: d.Name, Region: d.Region, Country: d.Country})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminDealersPage(navFor(r.Context(), a.users), rows, templates.AdminDealerFormData{}).Render(r.Context(), w)
}

func (a *adminHandlers) dealersCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := templates.AdminDealerFormData{
		Code:    strings.ToUpper(strings.TrimSpace(r.PostForm.Get("code"))),
		Name:    strings.TrimSpace(r.PostForm.Get("name")),
		Region:  strings.ToUpper(strings.TrimSpace(r.PostForm.Get("region"))),
		Country: strings.ToUpper(strings.TrimSpace(r.PostForm.Get("country"))),
	}
	if data.Code == "" || data.Name == "" || data.Region == "" || data.Country == "" {
		dealers, _ := a.dealers.List(r.Context())
		rows := make([]templates.AdminDealer, 0, len(dealers))
		for _, d := range dealers {
			rows = append(rows, templates.AdminDealer{Code: d.Code, Name: d.Name, Region: d.Region, Country: d.Country})
		}
		data.ErrorMsg = "all fields are required"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = templates.AdminDealersPage(navFor(r.Context(), a.users), rows, data).Render(r.Context(), w)
		return
	}
	if err := a.dealers.Upsert(r.Context(), dealer.Dealer{
		Code: data.Code, Name: data.Name, Region: data.Region, Country: data.Country,
	}); err != nil {
		httpErr(w, err)
		return
	}
	audit.SetSummary(r.Context(), "created/updated dealer "+data.Code+" "+data.Name+" ("+data.Region+"/"+data.Country+")")
	http.Redirect(w, r, "/admin/dealers", http.StatusSeeOther)
}

// --- audit log ---

// auditList renders the most recent admin actions. Read-only; the
// audit table is append-only so a redaction flow is not needed for
// the pilot. Cap at 200 to keep the page responsive without paging.
func (a *adminHandlers) auditList(w http.ResponseWriter, r *http.Request) {
	const cap = 200
	entries, err := a.audit.List(r.Context(), cap)
	if err != nil {
		httpErr(w, err)
		return
	}
	rows := make([]templates.AdminAuditRow, len(entries))
	for i, e := range entries {
		rows[i] = templates.AdminAuditRow{
			ID: e.ID, At: e.At, ActorID: e.ActorID, ActorEmail: e.ActorEmail,
			Method: e.Method, Path: e.Path, Status: e.Status,
			Summary: e.Summary, IP: e.IP, UserAgent: e.UserAgent,
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminAuditPage(navFor(r.Context(), a.users), rows, cap).Render(r.Context(), w)
}

// --- shared helpers ---

func parseUserForm(r *http.Request) templates.AdminUserFormData {
	return templates.AdminUserFormData{
		Email:           strings.ToLower(strings.TrimSpace(r.PostForm.Get("email"))),
		Name:            strings.TrimSpace(r.PostForm.Get("name")),
		Role:            strings.TrimSpace(r.PostForm.Get("role")),
		Region:          strings.ToUpper(strings.TrimSpace(r.PostForm.Get("region"))),
		Specializations: strings.TrimSpace(r.PostForm.Get("specializations")),
	}
}

func prefillUserForm(u userpkg.User) templates.AdminUserFormData {
	region := ""
	if u.Region != nil {
		region = *u.Region
	}
	return templates.AdminUserFormData{
		Email:           u.Email,
		Name:            u.Name,
		Role:            u.Role,
		Region:          region,
		Specializations: strings.Join(u.Specializations, ", "),
	}
}

func toAdminUser(u userpkg.User) templates.AdminUser {
	return templates.AdminUser{
		ID:              u.ID,
		Email:           u.Email,
		Name:            u.Name,
		Role:            u.Role,
		Region:          u.Region,
		Specializations: u.Specializations,
		DeactivatedAt:   u.DeactivatedAt,
		HasPassword:     u.PasswordHash != "",
	}
}

func splitCommas(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseAdminID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}
