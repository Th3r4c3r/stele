package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Th3r4c3r/stele/internal/fault"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
)

// exportCasesCSV streams the (filtered) cases list as CSV.
// Same query-string contract as GET /cases: tab + kind + assignee.
// Filename: cases-YYYYMMDD.csv. UTF-8 BOM up front so Excel on a
// localised system treats the file as UTF-8 instead of Windows-1252.
func (h *handlers) exportCasesCSV(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	switch tab {
	case "mine", "triage", "classified", "closed":
	default:
		tab = "triage"
	}
	kindFilter := r.URL.Query().Get("kind")
	if kindFilter != "" && !fault.IsKnownKind(kindFilter) {
		kindFilter = ""
	}
	assigneeParam := r.URL.Query().Get("assignee")
	var assigneeFilter uuid.UUID
	if tab != "mine" {
		switch assigneeParam {
		case "", "all":
		case "unassigned":
			if opsGen, err := h.users.ByEmail(r.Context(), "yan@stele.local"); err == nil {
				assigneeFilter = opsGen.ID
			}
		default:
			if id, err := uuid.Parse(assigneeParam); err == nil {
				assigneeFilter = id
			}
		}
	}

	var (
		rows []rowForCSV
		err  error
	)
	if tab == "mine" {
		currentID, _ := userpkg.FromCtx(r.Context())
		rows, err = h.queryCasesForCSV(r.Context(), "", "", currentID)
	} else {
		var status string
		switch tab {
		case "triage", "classified", "closed":
			status = tab
		}
		rows, err = h.queryCasesForCSV(r.Context(), status, kindFilter, assigneeFilter)
	}
	if err != nil {
		httpErr(w, err)
		return
	}

	filename := fmt.Sprintf("cases-%s.csv", time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "private, max-age=0")

	// UTF-8 BOM so Excel on EU locales reads accents correctly.
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"number", "status", "kind", "opened_at", "classified_at", "closed_at",
		"dealer", "vin", "fault_code", "description",
		"assignee", "notes",
	})
	for _, r := range rows {
		_ = cw.Write([]string{
			fmt.Sprintf("C-%d", r.Number),
			r.Status,
			derefStrCSV(r.Kind),
			r.OpenedAt.Format(time.RFC3339),
			timeOr(r.ClassifiedAt),
			timeOr(r.ClosedAt),
			r.Dealer,
			r.VIN,
			r.FaultCode,
			r.Description,
			r.AssigneeName,
			strconv.Itoa(r.NoteCount),
		})
	}
	cw.Flush()
}

// rowForCSV is a flat shape close to current_cases + assignee name.
type rowForCSV struct {
	Number       int64
	Status       string
	Kind         *string
	OpenedAt     time.Time
	ClassifiedAt *time.Time
	ClosedAt     *time.Time
	Dealer       string
	VIN          string
	FaultCode    string
	Description  string
	AssigneeName string
	NoteCount    int
}

func (h *handlers) queryCasesForCSV(ctx contextLike, status, kindFilter string, assigneeFilter uuid.UUID) ([]rowForCSV, error) {
	args := []any{}
	q := `
		SELECT c.case_number, c.status, c.kind, c.opened_at, c.classified_at,
		       c.closed_at, c.dealer, c.vin, c.fault_code, c.description,
		       COALESCE(u.name, '') AS assignee_name, c.note_count
		FROM current_cases c
		LEFT JOIN users u ON u.id = c.assignee_id
		WHERE 1=1`
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND c.status = $%d", len(args))
	}
	if kindFilter != "" {
		args = append(args, kindFilter)
		q += fmt.Sprintf(" AND c.kind = $%d", len(args))
	}
	if assigneeFilter != uuid.Nil {
		args = append(args, assigneeFilter)
		q += fmt.Sprintf(" AND c.assignee_id = $%d", len(args))
	}
	q += ` ORDER BY c.opened_at DESC`
	rows, err := h.pool.Query(asStdCtx(ctx), q, args...)
	if err != nil {
		return nil, fmt.Errorf("queryCasesForCSV: %w", err)
	}
	defer rows.Close()
	var out []rowForCSV
	for rows.Next() {
		var r rowForCSV
		if err := rows.Scan(&r.Number, &r.Status, &r.Kind, &r.OpenedAt, &r.ClassifiedAt,
			&r.ClosedAt, &r.Dealer, &r.VIN, &r.FaultCode, &r.Description,
			&r.AssigneeName, &r.NoteCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func derefStrCSV(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timeOr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}
