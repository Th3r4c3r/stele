# Vision

## The thesis

Mainstream business systems (SAP, Odoo, Salesforce) treat the current state of the
database as truth. The history of how that state came to be is a second-class citizen,
buried in audit logs and `modified_at` columns. This makes basic things hard:

- "What did we know on March 15?" requires archaeology, not a query.
- Correcting a backdated entry corrupts every report run before the correction.
- Undo is unreliable; branching the data to test a change is impossible.
- The PDF the customer signed is an attachment to the transaction, not the source of it.

Stele inverts the model:

- The event log **is** the database. Current state is a projection, recalculable.
- Every fact has two timestamps: when it happened in the business (`occurred_at`),
  and when the system learned about it (`recorded_at`).
- Documents are first-class; transactions are extracted from them, not the reverse.
- The current "view" is one of many projections, queryable as of any point in time.

## Scope (Phase 1)

Intentionally narrow: warranty claim management for one small business, one user,
one warehouse, one currency. Prove the foundations on a domain we know well.

## Non-goals

- General-purpose ERP. Stele is opinionated about domain.
- AI features. Reserved for later phases. Foundations must hold without them.
- Multi-tenant SaaS. Self-hosted, single tenant.
- Backward compatibility with anything. Greenfield.

## Audience

The author (Yan) and the agent (Claude) running as project manager. Open source so
the curious can read along. Not seeking users, contributors, or stars in Phase 1.
