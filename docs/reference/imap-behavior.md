# IMAP behavior reference

The server uses `github.com/emersion/go-imap`'s IMAP4rev1 server and in-memory
backend. The dependency version is recorded in [go.mod](../../go.mod).
Project-specific restrictions are implemented in [mailbox.go](../../mailbox.go).

## Operations

| Operation | Behavior |
| --- | --- |
| Authenticate | One fixed account; incorrect credentials are rejected |
| List, select, and inspect folders | `INBOX`, `Sent`, and `Archive` exist from startup |
| Search and fetch | Supports message metadata and bodies, including UID-based searches and fetches |
| Append through IMAP | Supported; HTTP seeding is a convenience interface over the same mailbox |
| Create, delete, or rename folders | Rejected |
| Change subscriptions | Rejected |
| Change existing message flags | Rejected; initial flags can be set on append |
| Copy messages through IMAP | Rejected; seed copies explicitly |
| Expunge messages | Rejected |

Use read-only selection and `BODY.PEEK[...]` for importer tests that should
leave message flags unchanged. This is a test server with a limited mutation
model, not a complete replacement for a production mail service.

## Identity and lifetime

- Messages are held in process memory; every startup begins with empty folders.
- Each appended message gets a UID within its folder. Existing UIDs remain
  stable during that process's lifetime.
- Startup chooses a fresh random nonzero UIDVALIDITY, shared by the initial
  folders. UIDs from a previous process must not be treated as current cursors.
- Track IMAP identity with mailbox/folder, UIDVALIDITY, and UID. A UID by itself
  is not a cross-folder or cross-restart identifier.
- Message-ID is a message header. The emulator allows repeated IDs within and
  across folders and performs no content deduplication.
- Reads, searches, status requests, and appends are serialized per folder by
  a mutex, allowing HTTP writes and IMAP polling to share the mailbox.

## Message times

| Value | Source for HTTP fixtures |
| --- | --- |
| Date header / envelope date | `date`, or current UTC time when omitted; formatted at second precision |
| IMAP INTERNALDATE | `internal_date`, or the resolved `date` when omitted |
| Application ingestion time | Determined by the consuming application, outside the emulator |

For a message with an old Date header but a later mailbox arrival time, supply
both fields. The [historical mail recipe](../how-to/seed-test-scenarios.md#create-historical-mail-with-a-later-received-date)
shows how.

## Scope

The emulator has no SMTP listener, outbound delivery, durable storage,
automatic message mirroring, application sync scheduling, or HTTP reset API.
The fixture API emits plain-text MIME messages; tests needing a raw MIME
message can append one using an IMAP client.

See [configuration](configuration.md) for connection settings and
[how the emulator works](../explanation/design.md) for the reasons behind these
choices.

[Documentation index](../README.md)
