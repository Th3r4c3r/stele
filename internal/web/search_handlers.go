package web

import (
	"net/http"

	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// searchPage handles GET /search?q=...
func (h *handlers) searchPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	res, err := h.searchSvc.Find(r.Context(), q)
	if err != nil {
		httpErr(w, err)
		return
	}
	cases := make([]templates.SearchCaseHit, 0, len(res.Cases))
	for _, c := range res.Cases {
		cases = append(cases, templates.SearchCaseHit{
			CaseID:    c.CaseID,
			Number:    c.Number,
			Status:    c.Status,
			Dealer:    c.Dealer,
			VIN:       c.VIN,
			FaultCode: c.FaultCode,
			Field:     c.Field,
			Snippet:   c.Snippet,
		})
	}
	notes := make([]templates.SearchNoteHit, 0, len(res.Notes))
	for _, n := range res.Notes {
		notes = append(notes, templates.SearchNoteHit{
			CaseID:     n.CaseID,
			OccurredAt: n.OccurredAt,
			Author:     n.Author,
			Snippet:    n.Snippet,
		})
	}
	docs := make([]templates.SearchDocHit, 0, len(res.Documents))
	for _, d := range res.Documents {
		docs = append(docs, templates.SearchDocHit{
			DocumentID:  d.DocumentID,
			CaseID:      d.CaseID,
			Filename:    d.Filename,
			ContentType: d.ContentType,
			AttachedAt:  d.AttachedAt,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.SearchResultsPage(navFor(r.Context(), h.users),
		res.Term, cases, notes, docs).Render(r.Context(), w)
}
