package web

import (
	"net/http"

	"github.com/Th3r4c3r/stele/internal/dashboard"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// dashboardPage serves /dashboard. Bundles all queries into one render.
func (h *handlers) dashboardPage(w http.ResponseWriter, r *http.Request) {
	uid, err := userpkg.FromCtx(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	svc := dashboard.New(h.pool)

	kpis, err := svc.KPIs(r.Context(), uid)
	if err != nil {
		httpErr(w, err)
		return
	}
	mix, err := svc.ClassificationMix(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	queue, err := svc.QueuePerAssignee(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	dealers, err := svc.TopDealersLast30(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	activity, err := svc.ActivityLast7Days(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}

	cards := []templates.KPICard{
		{Label: "Total open", Value: kpis.TotalOpen, Sub: "triage + classified"},
		{Label: "My open", Value: kpis.MyOpen, Sub: "assigned to me"},
		{Label: "Opened", Value: kpis.OpenedLast7, Sub: "last 7 days"},
		{Label: "Closed", Value: kpis.ClosedLast7, Sub: "last 7 days"},
	}
	mixRows := make([]templates.DashKindRow, len(mix))
	for i, m := range mix {
		mixRows[i] = templates.DashKindRow{Kind: m.Kind, Count: m.Count}
	}
	queueRows := make([]templates.DashQueueRow, len(queue))
	for i, q := range queue {
		queueRows[i] = templates.DashQueueRow{UserID: q.UserID, UserName: q.UserName, Role: q.Role, Open: q.Open}
	}
	dealerRows := make([]templates.DashDealerRow, len(dealers))
	for i, d := range dealers {
		dealerRows[i] = templates.DashDealerRow{Dealer: d.Dealer, Region: d.Region, Open: d.Open, Closed: d.Closed, Total: d.Total}
	}
	activityRows := make([]templates.DashDay, len(activity))
	for i, a := range activity {
		activityRows[i] = templates.DashDay{Day: a.Day, Count: a.Count}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.DashboardPage(navFor(r.Context(), h.users),
		cards, mixRows, queueRows, dealerRows, activityRows).Render(r.Context(), w)
}
