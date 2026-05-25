-- 0007_documents: read model for case attachments.
-- See docs/adr/0010-documents-storage.md.

CREATE TABLE current_documents (
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
