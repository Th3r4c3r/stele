package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Th3r4c3r/stele/internal/audit"
	"github.com/Th3r4c3r/stele/internal/telemetry"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// telemetryHandlers serves /admin/telemetry/*. Admin-gated by Mount.
type telemetryHandlers struct {
	repo    *telemetry.Repo
	service *telemetry.Service
	users   *userpkg.Repo
}

// listPage is GET /admin/telemetry — the recent snapshots view.
func (t *telemetryHandlers) listPage(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	snaps, err := t.repo.ListRecent(r.Context(), limit)
	if err != nil {
		httpErr(w, err)
		return
	}
	caseVINs, _ := t.repo.CaseVINs(r.Context()) // best-effort for the "sync open cases" hint.

	rows := make([]templates.TelemetryRow, len(snaps))
	for i, s := range snaps {
		rows[i] = toTelemetryRow(s)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminTelemetryPage(navFor(r.Context(), t.users), rows, len(caseVINs), limit).
		Render(r.Context(), w)
}

// sync is POST /admin/telemetry/sync. Mode dispatch:
//   - vin=<single>: one VIN.
//   - mode=case-vins: every VIN that's on an open case.
//   - mode=all + limit=N: any-VIN sweep (bounded).
//
// Synchronous: the handler waits for the batch. Acceptable at pilot
// scale (case-vins is ~tens, single is one). For larger batches a
// background runner is a follow-up.
func (t *telemetryHandlers) sync(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	mode := strings.TrimSpace(r.PostForm.Get("mode"))
	singleVIN := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("vin")))
	redirect := strings.TrimSpace(r.PostForm.Get("return"))
	if redirect == "" {
		redirect = "/admin/telemetry"
	}

	var vins []string
	switch {
	case singleVIN != "":
		if len(singleVIN) != 17 {
			http.Error(w, "vin must be 17 chars", http.StatusBadRequest)
			return
		}
		vins = []string{singleVIN}
	case mode == "case-vins":
		v, err := t.repo.CaseVINs(r.Context())
		if err != nil {
			httpErr(w, err)
			return
		}
		vins = v
	case mode == "all":
		// Defer to a follow-up: needs vehicles repo here, plus
		// pagination, plus a real cap. For MVP we expose only the
		// two surfaces that have a clear operational meaning.
		http.Error(w, "mode=all not implemented yet (use mode=case-vins or vin=<single>)", http.StatusNotImplemented)
		return
	default:
		http.Error(w, "specify vin=<single> or mode=case-vins", http.StatusBadRequest)
		return
	}

	res := t.service.SyncBatch(r.Context(), vins)
	summary := fmt.Sprintf("telemetry sync (%s): %d synced, %d not-found, %d errors",
		describeMode(mode, singleVIN), res.Synced, res.NotFound, len(res.Errors))
	if res.TokenExpired {
		summary += " — STELE_NEWPLAT_TOKEN expired, batch short-circuited"
	}
	audit.SetSummary(r.Context(), summary)

	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func describeMode(mode, vin string) string {
	if vin != "" {
		return "vin=" + vin
	}
	if mode != "" {
		return "mode=" + mode
	}
	return "unspecified"
}

// toTelemetryRow maps a Snapshot into the template view, flattening
// pointers with safe defaults for display.
func toTelemetryRow(s telemetry.Snapshot) templates.TelemetryRow {
	r := templates.TelemetryRow{
		VIN:        s.VIN,
		SnapshotAt: s.SnapshotAt,
		IsOnline:   s.IsOnline,
	}
	if s.IMEI != nil {
		r.IMEI = *s.IMEI
	}
	if s.ICCID != nil {
		r.ICCID = *s.ICCID
	}
	if s.SimEndTime != nil {
		r.SimEndTime = s.SimEndTime
	}
	if s.LastOnlineAt != nil {
		r.LastOnlineAt = s.LastOnlineAt
	}
	if s.SOCPct != nil {
		r.SOCPct = s.SOCPct
	}
	if s.EnduranceKm != nil {
		r.EnduranceKm = s.EnduranceKm
	}
	if s.TotalMileageKm != nil {
		r.TotalMileageKm = s.TotalMileageKm
	}
	if s.FotaVersion != nil {
		r.FotaVersion = *s.FotaVersion
	}
	return r
}
