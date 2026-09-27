# IMAP behavior

The transport is IMAP4rev1 over implicit TLS (minimum TLS 1.2), using go-imap's
protocol implementation and this project's shared store. There is a configurable
default account and API-provisioned independent accounts, each with `INBOX`, `Sent` and `Archive` initially present and subscribed. A different configured SMTP folder is also created at startup.

| Operation | Behavior |
| --- | --- |
| LOGIN | Selects account by its unique username/password; wrong credentials rejected |
| LIST/LSUB, SELECT/EXAMINE, STATUS | Inspect shared folders and message counts |
| SEARCH / UID SEARCH | Header/body/date/flag criteria from the IMAP protocol library |
| FETCH / UID FETCH | Metadata, raw bodies and MIME sections, with scoped toxics |
| APPEND | Store MIME with supplied flags and INTERNALDATE |
| CREATE, DELETE, RENAME | Mutable folders, including hierarchical renames; INBOX cannot be deleted |
| SUBSCRIBE / UNSUBSCRIBE | Persisted subscription state |
| STORE / UID STORE | Persisted flags, reflected in HTTP read state |
| COPY / UID COPY | New message identity and destination UID, preserving raw MIME |
| EXPUNGE | Removes messages flagged Deleted without reusing UIDs |

Clients should poll using SELECT/STATUS and UID SEARCH/FETCH. Unsolicited mailbox
updates and IDLE notifications are not implemented. The library's asynchronous
update dispatcher races with session state, so this service deliberately does not
use it. Explicit STORE controls Seen flags; body fetches do not implicitly mark
messages read. HTTP GET of message detail does mark it read. Use BODY.PEEK for
portable importer behavior and refresh selection after external mutations.

Each folder has its own nonzero random UIDVALIDITY and monotonically increasing
UIDs. Deletion never shifts existing UIDs. Sequence numbers may change; use UIDs.
With persistent storage, folder counters/validity survive restart. An in-memory
restart starts fresh. Message-ID remains a header and duplicates are permitted
unless `MP_IGNORE_DUPLICATE_IDS=true`.

SMTP deliveries default to `Sent`; HTTP fixtures default to `INBOX`. They use the
same store, so no copy/mirroring process is required. Raw SMTP/APPEND MIME remains
unchanged in storage. Bcc recipients available only in an SMTP envelope are
included in HTTP metadata; they are not injected into the raw MIME or IMAP envelope.

Date comes from the message header. INTERNALDATE is append/receipt time unless a
fixture supplies `internal_date`. HTTP Created is always ingestion time.
Toxics operate on selected message snapshots, outside storage locks; see
[the isolation rules](../how-to/toxics.md).
