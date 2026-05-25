# ADR-010: Documents storage (filesystem-backed, no extraction)

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent), direction by Yan
- Builds on: ADR-007 (fault_case aggregate), ADR-008 (current user in ctx)

## Context

A fault case in real aftersales rarely lives without supporting
artefacts: failure photos, dealer reports, invoices, datasheets.
M5 adds the ability to attach files (mostly PDFs and images) to a
case, list them in the detail view, and download them later.

Earlier plans included text extraction (pdftotext + indexing). Yan
correctly pushed back: there is no concrete consumer for the extracted
text yet (no full-text search, no AI features in scope). Extraction
is deferred indefinitely. M5 ships storage + retrieval only.

## Decisions

### D1. Filesystem-backed storage

- Files live on disk at `/data/documents/<document_id>` (no
  extension, no nested subdirectories).
- The directory is a Docker bind-mount from `~/data/documents/` on
  the Hetzner host so files survive container rebuilds.
- We do NOT store bytes in Postgres. Reasons:
  - Bloats the event log + breaks `pg_dump` pipelines.
  - Streams cleanly via `sendfile`-style reads from the FS.
  - Easy GDPR redaction: delete file + append a redacted event.

### D2. Single event: `DocumentAttached`

Lives in `internal/document/events.go` but uses the existing fault
aggregate (a document belongs to a case):

```go
type DocumentAttached struct {
    DocumentID       uuid.UUID `json:"document_id"`
    SHA256           string    `json:"sha256"`
    ContentType      string    `json:"content_type"`
    OriginalFilename string    `json:"original_filename"`
    ByteSize         int64     `json:"byte_size"`
    AttachedByUserID uuid.UUID `json:"attached_by_user_id"`
}
```

- `aggregate_type = "fault_case"`, `aggregate_id = case_id`.
- `Type = "DocumentAttached"`.
- Path on disk is derived from `DocumentID`; not stored in the event
  to keep it relocatable (move the directory, no migration needed).
- SHA-256 in the event is the integrity anchor: replay can verify
  files on disk match what the log says was uploaded.

No `DocumentExtracted` event (extraction is deferred indefinitely).
No `DocumentReplaced` (mutations on documents are antipattern in an
append-only log: attach a new one + optionally `DocumentRedacted`
later).

### D3. Per-row idempotency in the projector

`current_documents.last_event_id < $eventID` guard ensures replay
does not double-insert. The INSERT uses `ON CONFLICT (id) DO NOTHING`
because each DocumentID is unique by construction (UUIDv7 generated
at upload time, never re-emitted).

### D4. Streaming upload + hash

The upload handler reads from the multipart reader through an
`io.MultiWriter` that writes to a temp file AND a SHA-256 hasher
simultaneously. After the body is fully read:
1. Detect content type from the first 512 bytes (`http.DetectContentType`).
2. Atomically rename the temp file to its final path
   `/data/documents/<doc_id>` (same filesystem = `rename(2)` is atomic).
3. Append the `DocumentAttached` event.
4. The projector picks it up within the runner poll interval.

If the body exceeds `STELE_DOCS_MAX_BYTES` (default 25 MiB), the
read is cut off, the temp file is unlinked, and the handler returns
413 Payload Too Large. No event is appended.

### D5. Read model: `current_documents`

```sql
current_documents (
    id                  uuid        PRIMARY KEY,
    case_id             uuid        NOT NULL,
    sha256              text        NOT NULL,
    filename            text        NOT NULL,
    content_type        text        NOT NULL,
    byte_size           bigint      NOT NULL,
    attached_by_user_id uuid        NOT NULL,
    attached_at         timestamptz NOT NULL,
    last_event_id       uuid        NOT NULL
);
CREATE INDEX current_documents_case_id_idx ON current_documents (case_id);
```

No FK to `current_cases` (read models are independent rebuilds).
No unique on sha256: the same file uploaded twice by the same user is
a legitimate event (two distinct attachments, audit visible).

### D6. Download contract

`GET /documents/{id}/raw` streams the file with:
- `Content-Type: <stored content_type>`
- `Content-Disposition: attachment; filename="<sanitised>"`
- `Content-Length: <byte_size>`
- `Cache-Control: private, max-age=0` (auth-gated content; no public CDN cache)

Access control: any authenticated user can download any document.
Per-case ACL deferred until role-based per-case visibility lands
(probably M6+ when dealers/external users join).

Filename sanitisation: strip path separators, control chars, and
emoji-like sequences from `original_filename`. If the result is
empty, fall back to `document-<id-prefix>.<ext-from-content-type>`.

### D7. Configuration

```
STELE_DOCS_DIR        default /data/documents
STELE_DOCS_MAX_BYTES  default 26214400 (25 MiB)
```

The app refuses to start if the directory is not writable. A test
file is created+deleted at boot to verify.

### D8. Backup

The nightly `pg_dump` script gains a sibling:
`~/backups/stele/docs-<TS>.tar.gz` from `~/data/documents/`.
Same 7-day rotation. Small initially (a few PDFs); revisit retention
strategy if total exceeds a few GB.

### D9. Deferred items (explicit, indefinitely)

- Text extraction (pdftotext et al.). Skip until search/AI consumer.
- Image thumbnails. Skip until UI needs them.
- Per-case ACL beyond "logged in". Skip until external users.
- Chunked/resumable upload. Skip until users hit the 25 MiB limit
  routinely.
- Virus scanning (ClamAV sidecar). Skip until external attachments
  flow in.
- S3/object storage. Skip; bind-mount on RAID-1 disk is enough at
  Phase-1 scale.

## Consequences

- One more event type in the fault aggregate log. The web summarize()
  needs a tiny dispatch: try fault decode, fall back to document
  decode. Bounded.
- Backups grow with attachments. Plan to revisit retention when
  `du -sh ~/data/documents/` exceeds 1 GB.
- A move to S3 later is mechanical: change `storage.Store` interface
  to use minio-go, keep events untouched.

## Open questions deferred

- "Show inline" vs "download" toggle for PDFs: M6, when there is a
  reading workflow.
- Drag-and-drop upload, multi-file: same.
- Document-level audit ("who downloaded what"): possibly M7 if needed.
