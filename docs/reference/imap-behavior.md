# IMAP behavior

IMAP4rev1 over implicit TLS (TLS 1.2 or later) on port 1993. Every account
starts with `INBOX`, `Sent` and `Archive`.

## Supported commands

| Command | Notes |
| --- | --- |
| LOGIN | The username chooses the account |
| LIST, LSUB, SELECT, EXAMINE, STATUS | |
| SEARCH, FETCH (and UID variants) | [Toxics](../how-to/toxics.md) may delay, hide or alter results |
| APPEND | Keeps the supplied flags and INTERNALDATE |
| STORE | Flags are saved and show up as read/unread in HTTP |
| COPY | The copy gets a new UID |
| EXPUNGE | Removes messages flagged `\Deleted` |
| CREATE, DELETE, RENAME, SUBSCRIBE, UNSUBSCRIBE | `INBOX` cannot be deleted |

## Things to rely on

- **UIDs never change.** Deleting mail doesn't renumber them. Sequence numbers
  can change, so use UIDs.
- **UIDVALIDITY** is random per folder. It changes when in-memory state is lost
  on restart, and survives restarts when persistence is on.
- **No push.** IDLE isn't supported, so poll with STATUS or UID SEARCH.
- **Reading doesn't mark as read** over IMAP. Only STORE does. The HTTP message
  detail endpoint *does* mark as read.

## Where mail comes from

| Source | Folder | INTERNALDATE |
| --- | --- | --- |
| SMTP | `Sent` (configurable) | Arrival time |
| `POST /messages` | `INBOX` unless `folder` is given | `internal_date`, else `date`, else now |
| Raw import | The `folder` parameter | Arrival time |
| IMAP APPEND | Target folder | As supplied |

All sources share one store, so HTTP and IMAP always see the same messages.
Raw MIME is stored unchanged. Bcc recipients known only from the SMTP envelope
appear in HTTP metadata, not in the MIME.

Duplicate Message-IDs are allowed unless `MP_IGNORE_DUPLICATE_IDS=true`.
