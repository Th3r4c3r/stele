package web

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/audit"
	"github.com/Th3r4c3r/stele/internal/part"
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
	pool     *pgxpool.Pool
	vehicles *vehicle.Repo
	parts    *part.Repo
	users    *userpkg.Repo
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
