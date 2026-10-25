# Import a mailbox export

Load an exported mailbox into an account, so the application reads realistic
history: real threads, folders, attachments and read status. One request
imports a whole mbox file or Maildir. Even 20,000 messages take about a second,
where seeding them one at a time takes minutes.

> [!WARNING]
> An export of a real mailbox contains other people's mail. Mailarky's control
> API is unauthenticated unless you set `MAILARKY_HTTP_AUTH`. The optional webmail
> profile and `MAILARKY_DUMP_PATH` expose imported mail too. Keep such data on your own
> machine, or use a mailbox created for testing.

## Export the mailbox

| Source | How to export | What to upload |
| --- | --- | --- |
| Gmail | [Google Takeout](https://takeout.google.com/), select only **Mail** | The `.mbox` file in `Takeout/Mail/`, with `?labels=gmail` |
| Thunderbird | Each folder is already an mbox file, in the profile's `Mail/` or `ImapMail/` directory. Compact folders first to drop deleted mail. | One folder file at a time, such as `INBOX` (not the `.msf` index) |
| Apple Mail | **Mailbox › Export Mailbox…** | The `mbox` file inside the exported `Name.mbox` directory |
| Outlook (PST) | Convert with `readpst` from [libpst](https://www.five-ten-sg.com/libpst/) | The mbox files it writes, one per folder |
| Dovecot, mutt, `mbsync`, `offlineimap` | These store Maildir. | A tar of the Maildir root |

## Import an mbox file

Each request imports into one folder, `INBOX` by default:

```sh
curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/import?folder=Archive/2023' \
  -H 'Content-Type: application/mbox' --data-binary @Archive.mbox
```

Missing folders are created and subscribed. Use `/` for subfolders, such as
`Clients/Acme`. Mailarky reads the read, answered and flagged state that mail
programs store in the file: Thunderbird's `X-Mozilla-Status`, or the standard
`Status` and `X-Status` headers. Messages that Thunderbird marked as deleted are
skipped. Those headers, and Gmail's, are removed, because a real server never
sends them. Each message's INTERNALDATE comes from its mbox `From ` line, or
from its `Date` header if that line has no date.

mbox files quote body lines that start with `From ` as `>From `, and Mailarky
removes one `>` from each such line. Some mail programs also quote lines that
already started with `>From ` (the mboxrd variant), and others don't (mboxo).
The file doesn't say which variant it is, so in an mboxo file, a body line that
really started with `>From ` loses its `>`.

### Gmail labels

Takeout puts all mail in one file and lists each message's labels in an
`X-Gmail-Labels` header. Add `?labels=gmail` to turn them into folders:

```sh
curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/import?labels=gmail' \
  -H 'Content-Type: application/mbox' --data-binary @'All mail Including Spam and Trash.mbox'
```

| Gmail label | Becomes |
| --- | --- |
| Inbox, Sent, Drafts, Spam, Trash | `INBOX`, `Sent`, `Drafts`, `Spam`, `Trash` |
| Your own labels, including nested `Work/Projects` | A folder with the same name |
| No folder label (archived mail) | `Archive` |
| Unread | No `\Seen` flag; all other messages are `\Seen` |
| Starred | `\Flagged` |
| Important, Opened, Archived, Category … | Ignored |
| Chat | Skipped; chat transcripts are not email |

A message with several labels gets a copy in each folder, as Gmail shows it
over IMAP. With `MAILARKY_IGNORE_DUPLICATE_IDS`, only the first copy is kept.

## Import a Maildir

Upload a tar of the Maildir root, created from inside that directory:

```sh
tar -C ~/Maildir -czf - . | curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/import' \
  -H 'Content-Type: application/x-tar' --data-binary @-
```

| Directory | Folder |
| --- | --- |
| `cur/`, `new/` at the root | `INBOX` |
| `.Sent/`, `.Work.Projects/` (Maildir++, as Dovecot uses) | `Sent`, `Work/Projects` |
| `Work/Projects/cur/` (plain nested, as `mbsync` can use) | `Work/Projects` |

Flags come from the file name suffix (`:2,S` is `\Seen`, `R` is `\Answered`,
`F` is `\Flagged`, `T` is `\Deleted` and `D` is `\Draft`); mail in `new/` is unread.
Each message's INTERNALDATE is its file's modification time, and UIDs follow that
order. Files in `tmp/` and Dovecot's index files are ignored.

## Read the result

Both mbox and tar uploads may be gzip-compressed. The response summarizes the import:

```json
{
  "imported": 18412,
  "duplicates": 0,
  "skipped": 3,
  "folders": {"INBOX": 2210, "Sent": 4102, "Archive": 12100},
  "problems": [{"source": "message 977", "error": "message exceeds size limit"}]
}
```

A message that can't be imported is skipped and listed under `problems`, which
shows the first 20. The causes are invalid MIME, exceeding
`MAILARKY_MAX_MESSAGE_SIZE`, or an invalid folder name. A problem with the whole
request stores nothing: an unreadable file or archive gets HTTP 400, and
exceeding the account's quota gets 507. Retention limits apply after the import,
as for any new mail.

The import does not trigger webhooks. WebSocket subscribers get a single
`import` event, `{"type":"import","data":{"messages":18412}}`.
`messages` counts new messages remaining after retention. If retention removes
existing messages, the same event includes their count in `deleted`, for example
`{"type":"import","data":{"messages":128,"deleted":128}}`. It replaces
individual deletion events; refresh the mailbox when receiving an import event.

## Size and reuse

Mailarky keeps every message in memory, including decoded text and HTML. For
text-heavy mail, budget up to 4 times the export's size in memory, and about
3 times on disk with persistence. Attachments add little to memory, because they
are decoded only when requested. Exports of a few hundred megabytes are
practical. Import a subset of folders, or a date range, from a large mailbox.

To avoid importing again for every test run, import once with
[persistence](run-and-test.md#persist-state) enabled. Then stop Mailarky and
copy `mail.db` and `mail.db.mailboxes/`. Starting from a copy restores the
accounts, folders, flags and UIDs without importing.
