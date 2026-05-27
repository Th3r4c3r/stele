package web

import (
	"net/http"

	"github.com/Th3r4c3r/stele/internal/analytics"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// stagesPage serves /analytics/stages. Three workflow-time views:
// time-in-stage, key-to-key cycle, and currently-stuck cases.
func (h *handlers) stagesPage(w http.ResponseWriter, r *http.Request) {
	svc := analytics.New(h.pool)
	durations, err := svc.StageDurations(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	cycle, err := svc.CycleTime(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	stuck, err := svc.Stuck(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}

	durRows := make([]templates.StageDurationRow, len(durations))
	for i, d := range durations {
		durRows[i] = templates.StageDurationRow{
			Stage: d.Stage, CompletedVisits: d.CompletedVisits,
			AvgDays: d.AvgDays, MedianDays: d.MedianDays, P90Days: d.P90Days,
		}
	}
	cycleRows := make([]templates.CycleTimeRow, len(cycle))
	for i, c := range cycle {
		cycleRows[i] = templates.CycleTimeRow{
			Kind: c.Kind, ClosedCases: c.ClosedCases,
			AvgDays: c.AvgDays, MedianDays: c.MedianDays, P90Days: c.P90Days,
		}
	}
	stuckRows := make([]templates.StuckRow, len(stuck))
	for i, s := range stuck {
		stuckRows[i] = templates.StuckRow{
			Stage: s.Stage, OpenCases: s.OpenCases,
			AvgDaysStuck: s.AvgDaysStuck, MaxDaysStuck: s.MaxDaysStuck,
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AnalyticsStagesPage(navFor(r.Context(), h.users),
		durRows, cycleRows, stuckRows).Render(r.Context(), w)
}

// analyticsPage serves /analytics. Three independent queries; if any
// fails we abort the render with a 500 (no partial dashboards).
func (h *handlers) analyticsPage(w http.ResponseWriter, r *http.Request) {
	svc := analytics.New(h.pool)
	const (
		windowDays = 90
		topLimit   = 20
		failLimit  = 30
		costLimit  = 30
	)

	top, err := svc.TopFailingPN(r.Context(), windowDays, topLimit)
	if err != nil {
		httpErr(w, err)
		return
	}
	failure, err := svc.FailureRateByFaultModel(r.Context(), failLimit)
	if err != nil {
		httpErr(w, err)
		return
	}
	cost, err := svc.AvgCostByKindModel(r.Context(), costLimit)
	if err != nil {
		httpErr(w, err)
		return
	}

	topRows := make([]templates.AnalyticsPNRow, len(top))
	for i, t := range top {
		topRows[i] = templates.AnalyticsPNRow{
			ModelCode: t.ModelCode, ModelName: t.ModelName,
			PN: t.PN, Description: t.Description,
			CaseCount: t.CaseCount, TotalQty: t.TotalQty, TotalCost: t.TotalCost,
		}
	}
	failureRows := make([]templates.AnalyticsFailureRow, len(failure))
	for i, f := range failure {
		failureRows[i] = templates.AnalyticsFailureRow{
			ModelCode: f.ModelCode, ModelName: f.ModelName,
			ManufacturedYear: f.ManufacturedYear, FaultCode: f.FaultCode,
			Cases: f.Cases, Fleet: f.Fleet, Per1000: f.Per1000,
		}
	}
	costRows := make([]templates.AnalyticsCostRow, len(cost))
	for i, c := range cost {
		costRows[i] = templates.AnalyticsCostRow{
			Kind: c.Kind, ModelCode: c.ModelCode, ModelName: c.ModelName,
			CaseCount: c.CaseCount, AvgCostEUR: c.AvgCostEUR, TotalCost: c.TotalCost,
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AnalyticsPage(navFor(r.Context(), h.users),
		topRows, failureRows, costRows, windowDays).Render(r.Context(), w)
}
