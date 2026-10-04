# Seed history and custom MIME

Use an account's `api_base` for all fixture operations. JSON fixtures and raw
MIME share `POST {api_base}/messages`; the Content-Type selects the format.

## Historical messages

```sh
curl -fsS http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages \
  -H 'Content-Type: application/json' -d '{
    "folder":"INBOX",
    "from":"Alice <alice@customer.test>",
    "to":["app@example.test"],
    "subject":"Earlier reply",
    "message_id":"history@example.test",
    "date":"2020-01-02T03:04:05Z",
    "internal_date":"2021-02-03T04:05:06Z",
    "body":"A reply that existed before the application started",
    "flags":[]
  }'
```

`date` becomes the message's Date header. `internal_date` controls IMAP's
INTERNALDATE. Set `flags` to `["\\Seen"]` for an already-read fixture.
The HTTP response includes the stored ID and IMAP UID.

## Attachments, threading, or unusual MIME

Prepare a `.eml` with the desired headers and MIME parts, then import it:

```sh
curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages?folder=INBOX' \
  -H 'Content-Type: message/rfc822' --data-binary @message.eml
```

Use `In-Reply-To` and `References` to seed a thread. Reuse a Message-ID in
multiple folders to test the application's deduplication. Duplicate IDs are
accepted by default. Arbitrary raw content must still parse as MIME within
configured size, nesting, and part-count limits.

Create an additional folder first using
`POST {api_base}/folders` with `{"name":"History"}`. IMAP APPEND is also available
when the test should seed mail through an independent protocol client.

## Reset a scenario

Delete matching fixtures with
`DELETE {api_base}/messages?query=message-id:history@example.test`.
Omit the query to empty the account, or delete the entire account at teardown.
GET inspection never sets `\Seen`. Change flags explicitly with
`PATCH {api_base}/messages/{id}` and `{"flags":[]}`.
