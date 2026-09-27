# Seed test data

There are three ways to put mail into a mailbox without SMTP:

| Method | Use it for |
| --- | --- |
| `POST /messages` | Plain-text mail with any date, folder and flags |
| `POST /api/v1/messages/raw?folder=…` | Exact MIME from an `.eml` file, including custom headers and attachments |
| `POST /api/v1/send` | Multipart mail built from JSON, with base64 attachments |

The examples use the default account. For a [separate mailbox](separate-mailboxes.md),
prefix the paths with its `api_base`.

## Historical mail

```sh
curl -fsS http://localhost:8026/messages -H 'Content-Type: application/json' -d '{
  "from": "alice@example.test",
  "to": ["clinic@example.test"],
  "subject": "Historical reply",
  "message_id": "history@example.test",
  "date": "2020-01-02T03:04:05Z",
  "internal_date": "2021-02-03T04:05:06Z"
}'
```

`date` becomes the `Date` header. `internal_date` is when the message entered
the mailbox, as IMAP reports it. It defaults to `date`, and both default to now.
The response (201) contains `folder` and `message_id`.

## Sent mail with flags

```sh
curl -fsS http://localhost:8026/messages -H 'Content-Type: application/json' -d '{
  "folder": "Sent",
  "from": "Clinic <clinic@example.test>",
  "to": ["alice@example.test"],
  "subject": "Your appointment",
  "flags": ["\\Seen"],
  "body": "A synthetic appointment message."
}'
```

Fixtures are only stored, never delivered. Mail your application sends over
SMTP already lands in `Sent`, so you don't need to add a copy.

## Duplicates across folders

Post the same `message_id` to `INBOX` and `Archive` to test your importer's
deduplication. Every POST adds a new copy. Keep `MP_IGNORE_DUPLICATE_IDS` off
(the default), or the sandbox drops the duplicates itself.

## Custom MIME

```sh
curl -fsS 'http://localhost:8026/api/v1/messages/raw?folder=INBOX' \
  -H 'Content-Type: message/rfc822' --data-binary @fixture.eml
```

The raw bytes are stored unchanged, including headers such as `X-Test-ID`
that [toxics](toxics.md) can select on.

## Clean up

Delete only the IDs your test created:

```sh
curl -fsS -X DELETE http://localhost:8026/api/v1/messages \
  -H 'Content-Type: application/json' -d '{"IDs":["MESSAGE-UUID"]}'
```

An empty or missing `IDs` list deletes **all** mail in the account. With a
separate mailbox per test, deleting the mailbox is simpler.

## Parallel tests

Use a unique address or Message-ID per test, and search by it. Don't assert
total message counts in a shared account. Seed fixtures before triggering the
application's sync, then poll for the result with a time limit.
