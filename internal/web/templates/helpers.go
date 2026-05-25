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
// the active classified/closed tab. Empty value = "all kinds".
func kindChipHref(value string) string {
	if value == "" {
		return "/cases?tab=classified"
	}
	return "/cases?tab=classified&kind=" + value
}

// derefStr returns *s or "" if nil. Useful inside templates.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
