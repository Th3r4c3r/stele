package templates

import (
	"fmt"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/Th3r4c3r/stele/internal/fault"
)

// humanTimeAgo turns a timestamp into a coarse "5 min ago" string.
// Used by the telemetry block + admin telemetry list. Thresholds are
// chosen to be informative without overstating precision: a snapshot
// 3h47m old says "3h ago", not "3 hours 47 minutes ago".
func humanTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return t.UTC().Format("2006-01-02 15:04")
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d d ago", int(d/(24*time.Hour)))
	default:
		return t.UTC().Format("2006-01-02")
	}
}

// humanTimeAgoPtr is humanTimeAgo for nullable timestamps.
func humanTimeAgoPtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return humanTimeAgo(*t)
}

// humanDatePtr renders a nullable timestamp as YYYY-MM-DD, or "—".
func humanDatePtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.UTC().Format("2006-01-02")
}

// telemetryReturn picks the return URL for the TelemetryBlock's
// Refresh button. Empty input => default to /cases (list). Keeps
// the template free of conditional logic in the value attribute.
func telemetryReturn(url string) string {
	if url == "" {
		return "/cases"
	}
	return url
}

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

// casesURL builds /cases?... preserving every filter dimension so
// clicking one chip never clobbers another. The "override" args
// (kind, assignee, stage) are taken as-is — pass "" to clear that
// dimension or a value to set it. Designed so chip templates call
// it with the same currently-active filters and only flip the one
// being clicked.
func casesURL(tab, kind, assignee, stage string) string {
	if tab == "" {
		tab = "triage"
	}
	q := "/cases?tab=" + tab
	if kind != "" {
		q += "&kind=" + kind
	}
	if assignee != "" {
		q += "&assignee=" + assignee
	}
	if stage != "" {
		q += "&stage=" + stage
	}
	return q
}

// kindChipHref builds the /cases href that toggles the kind filter
// to value while preserving the other dimensions. Empty value clears
// kind.
func kindChipHref(value, tab, assignee, stage string) string {
	if tab == "" {
		tab = "classified"
	}
	return casesURL(tab, value, assignee, stage)
}

// assigneeChipHref toggles assignee, preserving kind + stage.
func assigneeChipHref(value, tab, kind, stage string) string {
	if tab == "" {
		tab = "triage"
	}
	return casesURL(tab, kind, value, stage)
}

// stageChipHref toggles stage, preserving kind + assignee.
func stageChipHref(value, tab, kind, assignee string) string {
	if tab == "" {
		tab = "triage"
	}
	return casesURL(tab, kind, assignee, value)
}

// casesCSVHref points at /cases.csv with the same filters as the
// list page (tab/kind/assignee/stage).
func casesCSVHref(tab, kind, assignee, stage string) string {
	q := "?tab=" + tab
	if kind != "" {
		q += "&kind=" + kind
	}
	if assignee != "" {
		q += "&assignee=" + assignee
	}
	if stage != "" {
		q += "&stage=" + stage
	}
	return "/cases.csv" + q
}

// stageUILabel mirrors stageBadge text without the badge styling
// (used by filter chips).
func stageUILabel(stage string) string {
	switch stage {
	case "new":
		return "New"
	case "diagnosis":
		return "Diagnosis"
	case "parts_ordered":
		return "Parts ordered"
	case "parts_waiting":
		return "Awaiting parts"
	case "repair":
		return "Repair"
	case "resolved":
		return "Resolved"
	default:
		return stage
	}
}

// allStages exposes fault.AllStages to templates via a wrapper, so
// the templ file does not need its own import statement.
var allStages = []string{"new", "diagnosis", "parts_ordered", "parts_waiting", "repair", "resolved"}

// derefStr returns *s or "" if nil. Useful inside templates.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// priceString formats a nullable EUR price as a fixed-2 decimal,
// or returns "" when nil. Used as a data-* attribute value on the
// add-part datalist so a tiny inline script can show it in a preview
// without a server round-trip.
func priceString(p *float64) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *p)
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
