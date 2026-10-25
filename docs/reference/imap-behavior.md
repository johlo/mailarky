# IMAP behavior

The listener uses implicit TLS and account credentials shared with SMTP AUTH.
Every account has its own folders and UID space. Initial folders are `INBOX`,
`Sent`, `Archive`, and the configured SMTP folder.
Authentication supports LOGIN, SASL PLAIN, and test-token XOAUTH2.

| Operation | Behavior |
| --- | --- |
| LIST / LSUB | Account-local folders and subscriptions |
| SELECT / EXAMINE | Read-write / read-only selected view |
| STATUS | Counts, UIDNEXT, UIDVALIDITY, flags |
| SEARCH / UID SEARCH | Criteria evaluated against that account's messages |
| FETCH / UID FETCH | Envelope, flags, dates, body structure and sections |
| BODY[] / BODY[section] / RFC822 / RFC822.TEXT | Set `\Seen` in a writable selection |
| BODY.PEEK / RFC822.HEADER / metadata | Preserve `\Seen` |
| STORE / UID STORE | Change flags; SILENT suppresses replies for that command's changes, while other sessions' changes are still reported |
| APPEND | Store raw MIME in an existing folder and return APPENDUID; reject full account quota with `NO [OVERQUOTA]` |
| COPY / UID COPY | Return COPYUID with source/destination UID mappings; quota failure leaves all copies uncommitted |
| EXPUNGE | Remove messages carrying `\Deleted` |
| UID EXPUNGE | Remove only messages matching the UID set that also carry `\Deleted` |
| GETMETADATA / SETMETADATA | Read and atomically update server or mailbox annotations |
| CLOSE | Deselect; expunge only for a writable selection |
| CREATE / DELETE / RENAME | Folder operations; INBOX cannot be deleted |
| NOOP | Flush pending external changes |
| IDLE | Notify while selected; DONE ends IDLE |

UIDs increase and are not reused within an epoch. Persistence preserves UIDs
and UIDVALIDITY. An in-memory restart generates new epochs. A reset fault
changes UIDVALIDITY; existing selected sessions are disconnected on their next
command or update poll so clients must reconnect and resynchronize.

APPENDUID and COPYUID report the destination UIDVALIDITY captured in the same
transaction as the assigned UIDs. A COPY matching no messages succeeds without
COPYUID. With duplicate suppression enabled, APPEND returns the existing UID
if the duplicate is in the requested folder; if the suppressed duplicate is
in another folder, it succeeds without APPENDUID.

## Metadata

METADATA supports server annotations (`mailbox=""`) and annotations on existing
folders, including `/private/vendor/alps/…` settings, identities, and WebAuthn
data. Values are opaque bytes: empty values are distinct from NIL, which
deletes an entry. Entry names are case-insensitive. GETMETADATA supports
`DEPTH 0`, `DEPTH 1`, `DEPTH infinity`, and `MAXSIZE`, with `METADATA LONGENTRIES`
reporting the largest omitted value.

Each account is an isolated server namespace: both `/private` and `/shared`
entries stay within that account. The server entry `/shared/admin` is read-only.
Up to 128 entries are stored per scope (server or individual folder), with
values up to 64 KiB. SETMETADATA applies all changes or none. A rejected
oversized non-synchronizing literal closes the connection after the error.

Metadata survives reconnects, and survives restarts when bbolt persistence is
enabled. Folder rename moves its annotations; INBOX rename copies them so
INBOX retains its values. Folder deletion removes its annotations. Metadata
changes do not change message flags or emit message events. Unsolicited
METADATA change notifications through ENABLE are not implemented.

## Updates and faults

Selected clients receive EXISTS, EXPUNGE, and flag updates for changes from
HTTP, retention, or other IMAP sessions. Sequence-number views remain stable
until the corresponding EXPUNGE is sent. EXPUNGE is deferred during non-UID
FETCH, STORE, and SEARCH. Use UID commands for synchronization.

Explicit IDLE notification-drop faults intentionally break that guarantee:
the server view advances but selected wire updates are lost. Dropped updates
are not replayed. Reselect or reconnect to resynchronize. Active IDLE
disconnects run after the configured interval and are cancelled by DONE;
see the [fault guide](../how-to/fault-injection.md#interrupt-active-idle-or-lose-a-notification).

A writable body FETCH commits its implicit `\Seen` changes in one store
mutation before sending the prepared results. Hidden messages and messages at
or after a terminating content fault do not acquire `\Seen` from that fetch.
Content disconnects and raw replies follow any earlier FETCH results and end
that FETCH stream.

Message faults alter the selected response, not stored MIME. A `hide` fault can
make SEARCH/FETCH omit a message while folder counts still reflect actual
storage; this inconsistency is intentional fault behavior.

The implementation uses the [go-imap v2 fork](https://github.com/johlo/go-imap/tree/imap-v2-protocol-hooks).
The listener advertises IMAP4rev1, METADATA, and UIDPLUS. There is no CONDSTORE or QRESYNC.
MOVE is not implemented. IDLE requires a selected folder. For TCP behavior,
combine the service with Toxiproxy.
