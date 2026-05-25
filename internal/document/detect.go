package document

import "net/http"

// detectViaNetHTTP wraps the stdlib detector. Kept in its own file so
// storage.go doesn't import net/http (smaller surface for the tests).
func detectViaNetHTTP(b []byte) string {
	return http.DetectContentType(b)
}
