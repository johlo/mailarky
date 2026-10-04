# IMAP behavior

The listener uses implicit TLS and account credentials shared with SMTP AUTH.
Every account has its own folders and UID space. Initial folders are `INBOX`,
`Sent`, `Archive`, and the configured SMTP folder.

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
| APPEND | Store raw MIME in an existing folder |
| COPY / UID COPY | Copy with a new destination UID |
| EXPUNGE | Remove messages carrying `\Deleted` |
| CLOSE | Deselect; expunge only for a writable selection |
| CREATE / DELETE / RENAME | Folder operations; INBOX cannot be deleted |
| NOOP | Flush pending external changes |
| IDLE | Notify while selected; DONE ends IDLE |

UIDs increase and are not reused within an epoch. Persistence preserves UIDs
and UIDVALIDITY. An in-memory restart generates new epochs. A reset fault
changes UIDVALIDITY; existing selected sessions are disconnected on their next
command or update poll so clients must reconnect and resynchronize.

Selected clients receive EXISTS, EXPUNGE, and flag updates for changes from
HTTP, retention, or other IMAP sessions. Sequence-number views remain stable
until the corresponding EXPUNGE is sent. EXPUNGE is deferred during non-UID
FETCH, STORE, and SEARCH. Use UID commands for synchronization.

A writable body FETCH commits its implicit `\Seen` changes in one store
mutation before sending the prepared results. Hidden messages and messages at
or after a terminating content fault do not acquire `\Seen` from that fetch.
Content disconnects and raw replies follow any earlier FETCH results and end
that FETCH stream.

Message faults alter the selected response, not stored MIME. A `hide` fault can
make SEARCH/FETCH omit a message while folder counts still reflect actual
storage; this inconsistency is intentional fault behavior.

The implementation uses the [go-imap v2 fork](https://github.com/johlo/go-imap/tree/imap-v2-protocol-hooks).
The listener advertises IMAP4rev1. There is no CONDSTORE, QRESYNC, or UIDPLUS.
MOVE is not implemented. IDLE requires a selected folder. For TCP behavior,
combine the service with Toxiproxy.
