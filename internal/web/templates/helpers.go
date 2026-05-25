package templates

import (
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
