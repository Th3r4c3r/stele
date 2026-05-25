package document

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// ErrTooLarge is returned when an upload exceeds MaxBytes.
var ErrTooLarge = errors.New("document: upload exceeds size limit")

// Storage handles the bytes on disk. Backed by a directory; one file
// per document, named after the document UUID (no extension).
type Storage struct {
	dir      string
	maxBytes int64
}

// NewStorage prepares dir (creates if missing) and verifies it is
// writable by creating + deleting a probe file. Returns an error
// before the app starts serving if anything is wrong.
func NewStorage(dir string, maxBytes int64) (*Storage, error) {
	if dir == "" {
		return nil, errors.New("document.NewStorage: empty dir")
	}
	if maxBytes <= 0 {
		return nil, errors.New("document.NewStorage: maxBytes must be > 0")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("document.NewStorage: mkdir: %w", err)
	}
	probe := filepath.Join(dir, ".stele-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		return nil, fmt.Errorf("document.NewStorage: dir not writable: %w", err)
	}
	_ = os.Remove(probe)
	return &Storage{dir: dir, maxBytes: maxBytes}, nil
}

// MaxBytes returns the configured upload limit.
func (s *Storage) MaxBytes() int64 { return s.maxBytes }

// PathFor returns the canonical absolute path where the document is
// (or will be) stored. No I/O.
func (s *Storage) PathFor(id uuid.UUID) string {
	return filepath.Join(s.dir, id.String())
}

// WriteResult bundles what Write reports after a successful upload.
type WriteResult struct {
	DocumentID uuid.UUID
	SHA256     string
	ByteSize   int64
	// ContentType is detected from the first 512 bytes via
	// http.DetectContentType when the caller passes "" as hint.
	ContentType string
}

// Write streams the body into a temp file while hashing, then
// atomically renames into place. Enforces s.maxBytes; the underlying
// reader must be wrapped with io.LimitReader by the caller (the
// handler) to bound the bytes read off the wire.
//
// contentTypeHint comes from the multipart Content-Type header. The
// real content type is detected from the file bytes (the hint is
// often wrong/missing on uploaded data); the hint is kept only as a
// tie-breaker for ambiguous detections.
func (s *Storage) Write(body io.Reader, contentTypeHint string) (WriteResult, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return WriteResult{}, fmt.Errorf("document.Write: id: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return WriteResult{}, fmt.Errorf("document.Write: tmp: %w", err)
	}
	tmpPath := tmp.Name()
	// Clean up tmp on any error.
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	// Sniff buffer for content-type detection (max 512 bytes).
	var sniff [512]byte
	sniffLen := 0
	mw := io.MultiWriter(tmp, hasher)

	// Cap the read at maxBytes+1 to detect overrun.
	limited := io.LimitReader(body, s.maxBytes+1)
	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, rerr := limited.Read(buf)
		if n > 0 {
			if sniffLen < len(sniff) {
				room := len(sniff) - sniffLen
				if n < room {
					copy(sniff[sniffLen:], buf[:n])
					sniffLen += n
				} else {
					copy(sniff[sniffLen:], buf[:room])
					sniffLen = len(sniff)
				}
			}
			if _, werr := mw.Write(buf[:n]); werr != nil {
				return WriteResult{}, fmt.Errorf("document.Write: %w", werr)
			}
			total += int64(n)
			if total > s.maxBytes {
				return WriteResult{}, ErrTooLarge
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return WriteResult{}, fmt.Errorf("document.Write: read: %w", rerr)
		}
	}
	if total == 0 {
		return WriteResult{}, errors.New("document.Write: empty upload")
	}
	if err := tmp.Sync(); err != nil {
		return WriteResult{}, fmt.Errorf("document.Write: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return WriteResult{}, fmt.Errorf("document.Write: close tmp: %w", err)
	}
	final := s.PathFor(id)
	if err := os.Rename(tmpPath, final); err != nil {
		return WriteResult{}, fmt.Errorf("document.Write: rename: %w", err)
	}
	committed = true

	ct := detectContentType(sniff[:sniffLen], contentTypeHint)

	return WriteResult{
		DocumentID:  id,
		SHA256:      hex.EncodeToString(hasher.Sum(nil)),
		ByteSize:    total,
		ContentType: ct,
	}, nil
}

// Open returns a ReadCloser for streaming download. The caller closes.
func (s *Storage) Open(id uuid.UUID) (*os.File, error) {
	return os.Open(s.PathFor(id))
}

// Delete removes the file. Used by the (future) redaction ceremony.
func (s *Storage) Delete(id uuid.UUID) error {
	err := os.Remove(s.PathFor(id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SanitizeFilename strips path separators and control characters,
// caps length at 200. Empty result -> "document".
func SanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "\x00", "")
	var b strings.Builder
	for _, r := range s {
		if r < 32 || r == 127 {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 200 {
		out = out[:200]
	}
	if out == "" {
		return "document"
	}
	return out
}

// detectContentType applies http.DetectContentType then falls back to
// the multipart hint when the detector returns "application/octet-stream".
func detectContentType(sniff []byte, hint string) string {
	got := httpDetect(sniff)
	if got == "" || got == "application/octet-stream" {
		hint = strings.TrimSpace(strings.ToLower(hint))
		if hint != "" {
			return hint
		}
	}
	return got
}

// httpDetect is split out for testability + to avoid importing net/http
// at storage-package load time (cyclomatic minimal).
var httpDetect = func(b []byte) string {
	// stdlib net/http.DetectContentType. Imported lazily to keep this
	// file's import surface tight; in practice fine to import directly.
	return detectViaNetHTTP(b)
}
