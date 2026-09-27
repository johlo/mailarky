# Give each test a separate mailbox

Each mailbox is an independent account: its own messages, folders, UIDs, IMAP
login, SMTP addresses and toxics. All accounts share the same ports.

## Create

```sh
curl -fsS http://localhost:8026/api/v1/mailboxes -H 'Content-Type: application/json' -d '{}'
```

```json
{
  "id": "3f1c…",
  "username": "mailbox-3f1c…",
  "password": "…",
  "recipients": ["3f1c…@mailbox.test"],
  "api_base": "/mailboxes/3f1c…"
}
```

Keep the response in your test fixture. The password is returned only here.
You can also set `name`, `username`, `password` and `recipients` yourself.
Names, usernames and recipients must be unique across accounts.

## Use

| Interface | How to reach the account |
| --- | --- |
| SMTP (port 1025) | Send to one of its `recipients`. Mail lands in `Sent`. |
| IMAP (port 1993) | Log in with its `username` and `password`. |
| HTTP | Prefix any API path with `api_base`, e.g. `/mailboxes/3f1c…/api/v1/messages`. |

```sh
API=http://localhost:8026/mailboxes/3f1c…
curl -fsS "$API/api/v1/search?query=subject:Welcome"
curl -fsS "$API/messages" -H 'Content-Type: application/json' \
  -d '{"from":"alice@example.test","to":["3f1c…@mailbox.test"],"subject":"Incoming fixture"}'
```

Rules to know:

- SMTP routes by the **envelope recipient**, not the `To` header. Unknown
  addresses go to the default account.
- One SMTP transaction can only reach one account. Mixing accounts returns 553.
- HTTP fixtures and IMAP APPEND go to the account you address, whatever the
  headers say.
- The generated credentials are for IMAP only. SMTP and HTTP use the
  server-wide auth settings.
- Paths without a prefix address the default account.

## Delete

```sh
curl -fsS -X DELETE http://localhost:8026/api/v1/mailboxes/3f1c…
```

This removes the account's messages, toxics and data file. Its addresses then
return 550 instead of falling back to the default account. The default account
cannot be deleted.

## Persistence

`GET /api/v1/mailboxes` lists accounts. Without `MP_DATABASE` they disappear
on restart. With it, accounts and their mail survive, stored in
`<MP_DATABASE>.mailboxes/`. Toxics never survive a restart.
