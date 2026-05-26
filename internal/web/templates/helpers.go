package templates

import (
	"fmt"
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

// casesCSVHref points at /cases.csv with the same tab / kind /
// assignee filters currently active on the list page.
func casesCSVHref(tab, kind, assignee string) string {
	q := "?tab=" + tab
	if kind != "" {
		q += "&kind=" + kind
	}
	if assignee != "" {
		q += "&assignee=" + assignee
	}
	return "/cases.csv" + q
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

// firstWord returns everything up to the first space. Used by the
// assignee chip set to shorten "Mario Bossi" -> "Mario" so the chip
// stays compact. If there is no space, returns the input unchanged.
func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
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

// humanKind converts the enum kind string into a readable label.
// Falls back to the raw value (e.g. for "unclassified" or future kinds).
func humanKind(k string) string {
	switch k {
	case "warranty":
		return "Warranty"
	case "out_of_warranty":
		return "Out of warranty"
	case "goodwill":
		return "Goodwill"
	case "recall":
		return "Recall"
	case "unrelated":
		return "Unrelated"
	case "customer_education":
		return "Customer education"
	case "unclassified":
		return "Unclassified"
	default:
		return k
	}
}

// renderSparklineSVG produces a 7-bar SVG bar chart inline. ~15 lines,
// no JS, no chart library. Width scales with max count.
func renderSparklineSVG(days []DashDay) string {
	if len(days) == 0 {
		return ""
	}
	maxCount := 1
	for _, d := range days {
		if d.Count > maxCount {
			maxCount = d.Count
		}
	}
	const (
		barW = 50
		gap  = 8
		maxH = 80
		padY = 24 // top label space
		botY = 16 // bottom label space
	)
	totalW := len(days)*(barW+gap) - gap
	totalH := maxH + padY + botY
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="sparkline" width="%d" height="%d" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Activity last 7 days">`,
		totalW, totalH, totalW, totalH)
	for i, d := range days {
		x := i * (barW + gap)
		bh := 0
		if d.Count > 0 {
			bh = (d.Count * maxH) / maxCount
			if bh < 2 {
				bh = 2
			}
		}
		y := padY + (maxH - bh)
		fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="%d" height="%d" rx="2" fill="#335577"/>`,
			x, y, barW, bh)
		// Value above the bar.
		fmt.Fprintf(&sb, `<text x="%d" y="%d" text-anchor="middle" font-size="11" fill="#1c1c1c">%d</text>`,
			x+barW/2, padY-6, d.Count)
		// Weekday short name below.
		fmt.Fprintf(&sb, `<text x="%d" y="%d" text-anchor="middle" font-size="11" fill="#6c6c6c">%s</text>`,
			x+barW/2, padY+maxH+12, d.Day.Format("Mon"))
	}
	sb.WriteString(`</svg>`)
	return sb.String()
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
