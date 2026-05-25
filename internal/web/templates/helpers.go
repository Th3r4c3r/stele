package templates

import (
	"strings"

	"github.com/a-h/templ"

	"github.com/Th3r4c3r/stele/internal/fault"
)

// kindLabel returns a UI-friendly label for a kind. Kept in plain Go
// (not in a .templ) so it is callable from inside template expressions
// as a regular function.
func kindLabel(k string) string {
	switch k {
	case fault.KindWarranty:
		return "Warranty"
	case fault.KindOutOfWarranty:
		return "Out of warranty"
	case fault.KindGoodwill:
		return "Goodwill"
	case fault.KindRecall:
		return "Recall"
	case fault.KindUnrelated:
		return "Unrelated"
	case fault.KindCustomerEducation:
		return "Customer education"
	default:
		return k
	}
}

// kindChipHref builds the /cases href that selects a kind filter on
// the active tab. Empty value = "all kinds" on the same tab.
func kindChipHref(value string, tab string) string {
	if tab == "" {
		tab = "classified"
	}
	if value == "" {
		return "/cases?tab=" + tab
	}
	return "/cases?tab=" + tab + "&kind=" + value
}

// assigneeChipHref builds the /cases href that selects an assignee
// filter on the active tab. Empty value = "all assignees".
func assigneeChipHref(value string, tab string) string {
	if tab == "" {
		tab = "triage"
	}
	if value == "" {
		return "/cases?tab=" + tab
	}
	return "/cases?tab=" + tab + "&assignee=" + value
}

// derefStr returns *s or "" if nil. Useful inside templates.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// triageOrClassifiedDefaultOpen returns the attrs for a <details>
// element so that triage cases open the Classify section by default
// (it's the next expected action), while classified cases keep
// "Re-classify" collapsed (you rarely re-classify, but it's there).
func triageOrClassifiedDefaultOpen(status string) templ.Attributes {
	if status == "triage" {
		return templ.Attributes{"open": ""}
	}
	return templ.Attributes{}
}

// joinComma joins a slice with ", " or returns "—" when empty.
func joinComma(xs []string) string {
	if len(xs) == 0 {
		return "—"
	}
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// userFormAction returns the POST target for the admin user form.
// Empty id = create; a UUID = update.
func userFormAction(id string) string {
	if id == "" {
		return "/admin/users"
	}
	return "/admin/users/" + id
}

// highlightHTML wraps every case-insensitive occurrence of term in
// text inside a <mark>...</mark> span. Both sides are HTML-escaped to
// prevent injection; the <mark> tags themselves are added afterwards.
func highlightHTML(text, term string) string {
	if text == "" || term == "" {
		return htmlEscape(text)
	}
	low := strings.ToLower(text)
	lowTerm := strings.ToLower(term)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(low[i:], lowTerm)
		if j < 0 {
			b.WriteString(htmlEscape(text[i:]))
			break
		}
		b.WriteString(htmlEscape(text[i : i+j]))
		b.WriteString("<mark>")
		b.WriteString(htmlEscape(text[i+j : i+j+len(term)]))
		b.WriteString("</mark>")
		i = i + j + len(term)
	}
	return b.String()
}

func htmlEscape(s string) string {
	// Tiny inlined escaper. Avoids importing html/template just for one helper.
	repl := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return repl.Replace(s)
}
