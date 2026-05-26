package web

import (
	"net/http"

	"github.com/Th3r4c3r/stele/internal/analytics"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

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
