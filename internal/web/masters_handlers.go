package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"log/slog"

	"github.com/Th3r4c3r/stele/internal/audit"
	"github.com/Th3r4c3r/stele/internal/newplat"
	"github.com/Th3r4c3r/stele/internal/part"
	"github.com/Th3r4c3r/stele/internal/telemetry"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/vehicle"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// mastersHandlers serves /admin/vehicles and /admin/parts. Admin-gated.
//
// All routes follow the same pattern: render the list page with an
// optional ImportReportView pointer; on POST upload, run the import,
// re-fetch the list, and render the same page with the report.
type mastersHandlers struct {
	pool         *pgxpool.Pool
	vehicles     *vehicle.Repo
	parts        *part.Repo
	users        *userpkg.Repo
	newplat      *newplat.Client    // nil disables /admin/vehicles/import-from-vin
	telemetrySvc *telemetry.Service // optional; if set, import-from-vin also stores a telemetry snapshot
}

// --- vehicles ---

func (m *mastersHandlers) vehiclesPage(w http.ResponseWriter, r *http.Request) {
	m.renderVehicles(w, r, nil)
}

func (m *mastersHandlers) vehiclesImport(w http.ResponseWriter, r *http.Request) {
	report, ok := m.runImport(w, r, func(r *http.Request) (templates.ImportReportView, error) {
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return templates.ImportReportView{}, err
		}
		defer f.Close()
		rep, err := m.vehicles.ImportVehiclesCSV(r.Context(), f)
		return toReportView(hdr.Filename, rep.RowsInserted, rep.RowsUpdated, rep.RowsSkipped, toVehicleErrs(rep.Errors)), err
	})
	if !ok {
		return
	}
	audit.SetSummary(r.Context(), fmt.Sprintf("imported vehicles CSV %s: +%d / ~%d / skip %d",
		report.Filename, report.RowsInserted, report.RowsUpdated, report.RowsSkipped))
	m.renderVehicles(w, r, &report)
}

func (m *mastersHandlers) vehiclesImportModels(w http.ResponseWriter, r *http.Request) {
	report, ok := m.runImport(w, r, func(r *http.Request) (templates.ImportReportView, error) {
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return templates.ImportReportView{}, err
		}
		defer f.Close()
		rep, err := m.vehicles.ImportModelsCSV(r.Context(), f)
		return toReportView(hdr.Filename, rep.RowsInserted, rep.RowsUpdated, rep.RowsSkipped, toVehicleErrs(rep.Errors)), err
	})
	if !ok {
		return
	}
	audit.SetSummary(r.Context(), fmt.Sprintf("imported vehicle_models CSV %s: +%d / ~%d / skip %d",
		report.Filename, report.RowsInserted, report.RowsUpdated, report.RowsSkipped))
	m.renderVehicles(w, r, &report)
}

func (m *mastersHandlers) renderVehicles(w http.ResponseWriter, r *http.Request, report *templates.ImportReportView) {
	models, err := m.vehicles.ListModels(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	modelRows := make([]templates.AdminModelRow, 0, len(models))
	for _, mo := range models {
		modelRows = append(modelRows, templates.AdminModelRow{
			Code:        mo.Code,
			Name:        mo.Name,
			Generation:  mo.Generation,
			Segment:     mo.Segment,
			CapacityKWh: mo.CapacityKWh,
		})
	}
	vehicleRows, err := m.listVehicles(r)
	if err != nil {
		httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminVehiclesPage(navFor(r.Context(), m.users), modelRows, vehicleRows, report).Render(r.Context(), w)
}

// listVehicles fetches vehicles joined with their model name, ordered
// by VIN. Capped at 500 rows (admin page; if the fleet grows beyond
// that, paginate; for now Vmoto pilot is in the low hundreds).
func (m *mastersHandlers) listVehicles(r *http.Request) ([]templates.AdminVehicleRow, error) {
	rows, err := m.pool.Query(r.Context(), `
		SELECT v.vin, v.model_code, mo.name, v.manufactured_year, v.country
		FROM vehicles v
		JOIN vehicle_models mo ON mo.code = v.model_code
		ORDER BY v.vin
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]templates.AdminVehicleRow, 0)
	for rows.Next() {
		var vr templates.AdminVehicleRow
		if err := rows.Scan(&vr.VIN, &vr.ModelCode, &vr.ModelName, &vr.ManufacturedYear, &vr.Country); err != nil {
			return nil, err
		}
		out = append(out, vr)
	}
	return out, rows.Err()
}

// --- parts ---

func (m *mastersHandlers) partsPage(w http.ResponseWriter, r *http.Request) {
	m.renderParts(w, r, nil)
}

func (m *mastersHandlers) partsImport(w http.ResponseWriter, r *http.Request) {
	report, ok := m.runImport(w, r, func(r *http.Request) (templates.ImportReportView, error) {
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return templates.ImportReportView{}, err
		}
		defer f.Close()
		rep, err := m.parts.ImportCSV(r.Context(), f)
		return toReportView(hdr.Filename, rep.RowsInserted, rep.RowsUpdated, rep.RowsSkipped, toPartErrs(rep.Errors)), err
	})
	if !ok {
		return
	}
	audit.SetSummary(r.Context(), fmt.Sprintf("imported parts CSV %s: +%d / ~%d / skip %d",
		report.Filename, report.RowsInserted, report.RowsUpdated, report.RowsSkipped))
	m.renderParts(w, r, &report)
}

func (m *mastersHandlers) renderParts(w http.ResponseWriter, r *http.Request, report *templates.ImportReportView) {
	all, err := m.parts.List(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	rows := make([]templates.AdminPartRow, 0, len(all))
	for _, p := range all {
		rows = append(rows, templates.AdminPartRow{
			PN:          p.PN,
			Description: p.Description,
			Category:    p.Category,
			PriceEUR:    p.PriceEUR,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminPartsPage(navFor(r.Context(), m.users), rows, report).Render(r.Context(), w)
}

// --- shared upload plumbing ---

// runImport parses the multipart form, delegates to fn for the actual
// CSV processing, and on hard failure renders an HTTP error. Returns
// (report, true) on success so the caller can re-render with the
// report card.
func (m *mastersHandlers) runImport(w http.ResponseWriter, r *http.Request, fn func(*http.Request) (templates.ImportReportView, error)) (templates.ImportReportView, bool) {
	// 32 MiB in memory is plenty for our masters (Vmoto pilot has
	// hundreds of VINs, low thousands of parts). Anything bigger gets
	// spilled to disk by net/http automatically.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return templates.ImportReportView{}, false
	}
	report, err := fn(r)
	if err != nil {
		// Hard failure (e.g., missing required column): show as an
		// error, not as a partial report.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return templates.ImportReportView{}, false
	}
	return report, true
}

func toReportView(filename string, inserted, updated, skipped int, errs []templates.ImportRowError) templates.ImportReportView {
	// Cap to 20 errors shown in the UI; the rest are still counted in
	// RowsSkipped so the operator knows the true scope.
	if len(errs) > 20 {
		errs = errs[:20]
	}
	return templates.ImportReportView{
		Filename:     filename,
		RowsInserted: inserted,
		RowsUpdated:  updated,
		RowsSkipped:  skipped,
		Errors:       errs,
	}
}

func toVehicleErrs(in []vehicle.ImportError) []templates.ImportRowError {
	out := make([]templates.ImportRowError, len(in))
	for i, e := range in {
		out[i] = templates.ImportRowError{Line: e.Line, Reason: e.Reason}
	}
	return out
}

func toPartErrs(in []part.ImportError) []templates.ImportRowError {
	out := make([]templates.ImportRowError, len(in))
	for i, e := range in {
		out[i] = templates.ImportRowError{Line: e.Line, Reason: e.Reason}
	}
	return out
}

// --- import single VIN from newplat ---

// vehicleImportFromVINPage handles GET /admin/vehicles/import-from-vin.
// Queries newplat for the VIN, builds a preview form pre-filled with
// the fields newplat exposes, lets the operator confirm / edit / pick
// the right Stele model_code, then POST commits.
func (m *mastersHandlers) vehicleImportFromVINPage(w http.ResponseWriter, r *http.Request) {
	vin := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("vin")))
	ret := safeReturn(strings.TrimSpace(r.URL.Query().Get("return")))
	if len(vin) != 17 {
		http.Error(w, "vin must be 17 chars", http.StatusBadRequest)
		return
	}
	// Already in master? Short-circuit to the return path.
	if exists, err := m.vehicles.Exists(r.Context(), vin); err == nil && exists {
		http.Redirect(w, r, ret, http.StatusSeeOther)
		return
	}

	// Fetch newplat. ErrNotFound → friendly "not on newplat" page;
	// ErrTokenInvalid (after the client tried auto-refresh) → same
	// page with a token-issue hint; other errors → same page with
	// the raw error.
	detail, err := m.newplat.FetchVIN(r.Context(), vin)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = templates.AdminVINImportNotFound(navFor(r.Context(), m.users),
			templates.VINImportNotFound{VIN: vin, ReturnURL: ret, Reason: friendlyNewplatErr(err)}).
			Render(r.Context(), w)
		return
	}

	models, err := m.vehicles.ListModels(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}

	preview := templates.VINImportPreview{
		VIN:              vin,
		ReturnURL:        ret,
		ManufacturedYear: vehicle.YearFromVIN(vin),
		Models:           toAdminModelRows(models),
	}
	if detail.Pojo != nil {
		preview.NewplatCountry = "" // pojo.countryCode is a mobile code, not ISO
	}
	preview.NewplatModelName = detail.CarBaseInfo.CarModelName
	preview.MotorSN = detail.CarBaseInfo.CarMotorNumber
	if detail.Device != nil {
		if t := newplat.ParseNewplatTime(detail.Device.SimStartTime); !t.IsZero() {
			// proDate would be cleaner but Device struct currently
			// does not expose it; SimStartTime is a close proxy
			// (issued ~same day as production at the pilot).
			preview.SoldAt = t.Format("2006-01-02")
		}
	}
	preview.PreselectedCode = guessModelCode(preview.NewplatModelName, models)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AdminVINImportPage(navFor(r.Context(), m.users), preview).
		Render(r.Context(), w)
}

// vehicleImportFromVINCommit handles POST /admin/vehicles/import-from-vin.
// Builds a Vehicle from the form, upserts, redirects to the return URL.
func (m *mastersHandlers) vehicleImportFromVINCommit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	vin := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("vin")))
	modelCode := strings.TrimSpace(r.PostForm.Get("model_code"))
	if len(vin) != 17 || modelCode == "" {
		http.Error(w, "vin and model_code required", http.StatusBadRequest)
		return
	}
	ret := safeReturn(strings.TrimSpace(r.PostForm.Get("return")))

	v := vehicle.Vehicle{VIN: vin, ModelCode: modelCode}
	if y, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("manufactured_year"))); err == nil && y > 1900 {
		v.ManufacturedYear = &y
	}
	if c := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("country"))); c != "" {
		v.Country = &c
	}
	if col := strings.TrimSpace(r.PostForm.Get("color")); col != "" {
		v.Color = &col
	}
	if sn := strings.TrimSpace(r.PostForm.Get("motor_sn")); sn != "" {
		v.MotorSN = &sn
	}
	if sa := strings.TrimSpace(r.PostForm.Get("sold_at")); sa != "" {
		if t, err := time.Parse("2006-01-02", sa); err == nil {
			v.SoldAt = &t
		}
	}
	if err := m.vehicles.UpsertVehicle(r.Context(), v); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	// Operator intent on this surface is "I want to know this VIN
	// fully". Piggy-back a telemetry snapshot on the same trip so
	// the case detail's TelemetryBlock lights up immediately, no
	// second click required. Best-effort: a failure here does not
	// abort the vehicle import (audit captures the partial success).
	teleStatus := "no telemetry sync (service disabled)"
	if m.telemetrySvc != nil {
		if _, err := m.telemetrySvc.Sync(r.Context(), vin); err != nil {
			slog.Error("import-from-vin telemetry sync failed",
				"vin", vin, "err", err)
			teleStatus = "telemetry sync failed: " + err.Error()
		} else {
			teleStatus = "telemetry snapshot stored"
		}
	}
	audit.SetSummary(r.Context(), fmt.Sprintf("imported vehicle %s (model %s) from newplat; %s",
		vin, modelCode, teleStatus))
	http.Redirect(w, r, ret, http.StatusSeeOther)
}

// guessModelCode picks the most-likely Stele model_code for a
// newplat carModelName. Heuristic: normalised case-insensitive
// prefix match against vehicle_models.name. Returns "" when zero
// or multiple matches make the choice ambiguous; the form then
// shows an unselected dropdown.
func guessModelCode(newplatName string, models []vehicle.Model) string {
	needle := normalizeModelName(newplatName)
	if needle == "" {
		return ""
	}
	matches := make([]string, 0, 2)
	for _, m := range models {
		if strings.HasPrefix(normalizeModelName(m.Name), needle) {
			matches = append(matches, m.Code)
			if len(matches) > 1 {
				return "" // ambiguous, let the operator pick.
			}
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func normalizeModelName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func toAdminModelRows(in []vehicle.Model) []templates.AdminModelRow {
	out := make([]templates.AdminModelRow, len(in))
	for i, mo := range in {
		out[i] = templates.AdminModelRow{
			Code: mo.Code, Name: mo.Name,
			Generation: mo.Generation, Segment: mo.Segment, CapacityKWh: mo.CapacityKWh,
		}
	}
	return out
}

// friendlyNewplatErr maps newplat client errors to operator-readable
// strings on the "not found" page. ErrTokenInvalid is the only one
// that requires action from the admin (refresh the credentials).
func friendlyNewplatErr(err error) string {
	switch {
	case errors.Is(err, newplat.ErrNotFound):
		return "VIN not present on newplat (the bike may never have been activated by HQ)"
	case errors.Is(err, newplat.ErrTokenInvalid):
		return "newplat token expired AND auto-refresh failed (check STELE_NEWPLAT_ACCOUNT / STELE_NEWPLAT_PASSWORD)"
	default:
		return err.Error()
	}
}
