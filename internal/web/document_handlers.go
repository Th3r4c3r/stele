package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/document"
	"github.com/Th3r4c3r/stele/internal/event"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
)

// docHandlers serves upload + download for case attachments.
type docHandlers struct {
	pool    *pgxpool.Pool
	store   *event.PostgresStore
	storage *document.Storage
}

// uploadDocument: POST /cases/{id}/documents
// Accepts multipart/form-data with field name "file". Streams the
// body, hashes it, writes to storage, appends a DocumentAttached
// event. Redirects (or returns fragment) on success.
func (d *docHandlers) uploadDocument(w http.ResponseWriter, r *http.Request) {
	caseID, ok := parseID(w, r)
	if !ok {
		return
	}
	openerID, err := userpkg.FromCtx(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	// Cap the multipart size so the handler never reads more than the
	// limit from the wire, even if the field is poorly framed.
	r.Body = http.MaxBytesReader(w, r.Body, d.storage.MaxBytes()+512*1024)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		// 413 if MaxBytesReader rejected it.
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, fmt.Sprintf("upload too large (max %d bytes)", d.storage.MaxBytes()),
				http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	contentTypeHint := ""
	originalName := ""
	if header != nil {
		originalName = header.Filename
		if header.Header != nil {
			contentTypeHint = header.Header.Get("Content-Type")
		}
	}

	_, err = document.AttachDocument(r.Context(), d.store, d.storage,
		caseID, openerID, file, originalName, contentTypeHint)
	if errors.Is(err, document.ErrTooLarge) {
		http.Error(w, fmt.Sprintf("upload too large (max %d bytes)", d.storage.MaxBytes()),
			http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	http.Redirect(w, r, "/cases/"+caseID.String(), http.StatusSeeOther)
}

// deleteDocument: POST /documents/{id}/delete
// Appends a DocumentRedacted event; the projector removes the row +
// unlinks the file. Redirects back to the parent case detail.
func (d *docHandlers) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseDocID(w, r)
	if !ok {
		return
	}
	redactedBy, err := userpkg.FromCtx(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	// Look up the case_id + filename so the event carries them, and so
	// we can redirect to the right detail page.
	var caseID uuid.UUID
	var filename string
	err = d.pool.QueryRow(r.Context(),
		`SELECT case_id, filename FROM current_documents WHERE id = $1`, id,
	).Scan(&caseID, &filename)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already gone. Send back to /cases since we don't know the parent.
		http.Redirect(w, r, "/cases", http.StatusSeeOther)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	reason := r.PostFormValue("reason") // best-effort: form may not have one
	if err := document.RedactDocument(r.Context(), d.store, caseID, id, redactedBy, filename, reason); err != nil {
		httpErr(w, err)
		return
	}
	http.Redirect(w, r, "/cases/"+caseID.String(), http.StatusSeeOther)
}

// downloadDocument: GET /documents/{id}/raw
// Streams the file with Content-Disposition: attachment.
func (d *docHandlers) downloadDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseDocID(w, r)
	if !ok {
		return
	}
	meta, err := d.metaFor(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	f, err := d.storage.Open(id)
	if err != nil {
		http.Error(w, "file missing on disk (run replay to repair projection if needed)", http.StatusGone)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", meta.contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.byteSize, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+meta.filename+`"`)
	w.Header().Set("Cache-Control", "private, max-age=0")
	_, _ = io.Copy(w, f)
}

type docMeta struct {
	contentType string
	filename    string
	byteSize    int64
}

func (d *docHandlers) metaFor(ctx context.Context, id uuid.UUID) (docMeta, error) {
	var m docMeta
	err := d.pool.QueryRow(ctx,
		`SELECT content_type, filename, byte_size FROM current_documents WHERE id = $1`, id,
	).Scan(&m.contentType, &m.filename, &m.byteSize)
	return m, err
}

func parseDocID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid document id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}
