---
name: mailarky
description: Test applications that send or read email with Mailarky, a standard SMTP and IMAP server built for automated tests, with an HTTP API for accounts, fixtures, assertions and injected failures. Use this skill whenever you write, fix or run tests for code that sends mail over SMTP or reads a mailbox over IMAP — notification emails, reply or inbox import, mailbox sync, IMAP IDLE, send retries, XOAUTH2 login — or set up a mail server for local development or CI, even if the user doesn't name Mailarky. Also use it when a project already runs ghcr.io/johlo/mailarky.
---

# Testing email with Mailarky

Mailarky is a real SMTP and IMAP server, so the application under test keeps
its normal mail libraries and configuration. Tests drive Mailarky over HTTP:
create an account, put mail in it, run the application, check what was sent,
inject failures, and delete the account. Mail never leaves the machine.

This skill describes Mailarky v0.2. The API may change before 1.0, so check
`GET /api/v1/info` and the
[OpenAPI schema](https://github.com/johlo/mailarky/blob/main/openapi.yaml)
if a request is rejected unexpectedly.

## 1. Start the server

```sh
docker run -d --name mailarky \
  -e MAILARKY_SMTP_TLS_MODE=starttls \
  -p 127.0.0.1:1025:1025 -p 127.0.0.1:1993:1993 -p 127.0.0.1:8026:8026 \
  ghcr.io/johlo/mailarky:v0.2.0
docker cp mailarky:/certs/server.crt ./mailarky.crt
curl -fsS http://localhost:8026/healthz
```

| Interface | Address | Security |
| --- | --- | --- |
| SMTP | `localhost:1025` | STARTTLS (with the variable above) |
| IMAP | `localhost:1993` | Implicit TLS, always |
| HTTP API | `http://localhost:8026/api/v1` | None by default |

Pin a version tag rather than `latest`. If the project already uses Docker
Compose or CI services, add Mailarky there instead; see
[references/setup.md](references/setup.md) for Compose and GitHub Actions
snippets, running without Docker, and ports that are already taken.

**TLS:** the certificate is a public test fixture valid for `localhost`,
`127.0.0.1` and `mailarky`. Make the application's mail clients trust
`mailarky.crt` (the CA file setting of its SMTP/IMAP library) and connect
using one of those hostnames. Prefer that over disabling verification, which
would hide TLS bugs the tests should catch.

## 2. Give each test its own account

```sh
curl -fsS -X POST http://localhost:8026/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{}'
```

The response contains `id`, `username`, `password`, `recipients` and
`api_base` (`/api/v1/accounts/{id}`). The password is only returned here. The
same username and password log in to **both SMTP and IMAP**. Create the account
in test setup, configure the application with its credentials, and
`DELETE /api/v1/accounts/{id}` in teardown, even when the test fails.

Separate accounts keep parallel tests from seeing each other's mail, faults
and IMAP UIDs, even when they use the same recipient addresses and
Message-IDs. Avoid the built-in `default` account (`user@example.test` /
`local-imap-only`) in tests; anything sent without authentication lands there,
so it is shared.

Pytest-style fixture; adapt it to the project's test framework and language:

```python
import json, urllib.request

MAILARKY = "http://localhost:8026"

def api(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(MAILARKY + path, data, method=method,
                                     headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request) as response:
        raw = response.read()
        return json.loads(raw) if raw else None

@pytest.fixture
def mail_account():
    account = api("POST", "/api/v1/accounts", {})
    yield account
    api("DELETE", "/api/v1/accounts/" + account["id"])
```

## 3. Configure the application

Point the application's SMTP and IMAP settings at Mailarky and use the test
account's `username` and `password` for both.

- **Authenticate SMTP.** Authentication is what ties sent mail to the test's
  account: it is stored in that account's `Sent` folder, whatever the
  recipients are. Unauthenticated mail is routed by recipient address and
  usually ends up in the shared `default` account.
- If the application really cannot authenticate SMTP, send to one of the
  account's `recipients` addresses; that mail lands in its `Sent` folder.
- Inside the same Docker network, use host `mailarky` with container ports
  1025, 1993 and 8026.

## 4. Seed the mailbox

Add mail the application should find, for example earlier customer replies:

```sh
curl -fsS -X POST http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages \
  -H 'Content-Type: application/json' -d '{
    "folder": "INBOX",
    "from": "Alice <alice@customer.test>",
    "to": ["app@example.test"],
    "subject": "Re: Order 42",
    "body": "Thanks!",
    "message_id": "reply-42@customer.test",
    "date": "2026-01-02T03:04:05Z"
  }'
```

`from` and at least one `to` are required; unknown fields are rejected.
`flags: ["\\Seen"]` makes a message already read. For attachments, threads or
unusual MIME, POST a raw `.eml` with `Content-Type: message/rfc822` and
`?folder=INBOX`. Accounts start with `INBOX`, `Sent` and `Archive`; create
others with `POST {api_base}/folders` and `{"name": "History"}`. Seeded mail is visible over IMAP immediately, including
to a client that is idling.

## 5. Check what the application did

```sh
curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages?query=folder:Sent'
```

The listing has `matched` (how many messages match the query), `messages`
(the matches, newest first, 50 per page by default) and `total` (every message
in the account, matching or not). Assert on `matched`, not `total`. Each
summary has `id`,
`subject`, `from` (`{name, address}`), `to`, `folder`, `read`, `snippet` and
`attachments`. `GET {api_base}/messages/{id}` adds `text`, `html`, `cc`,
`message_id`, attachment metadata and `flags`; `/raw` returns the exact MIME
and `/headers` the parsed headers.

- Query terms are ANDed: `folder:Sent to:alice@customer.test subject:"Order 42"`,
  plus `is:unread`, `has:attachment`, `message-id:...`. Matching is
  case-insensitive substring matching.
- **HTTP reads never mark mail as read,** so assertions don't disturb the
  application. To check whether the *application* marked mail as read, assert
  on `read` or `flags`.
- An SMTP submission is stored before the server acknowledges it. If the
  application sends in the background, poll the listing with a short timeout
  rather than sleeping a fixed time.
- If you read mail over IMAP in a test, use `BODY.PEEK[]` or `EXAMINE`; a plain
  `BODY[]` fetch marks the message as read.

## 6. Inject failures

Faults make SMTP or IMAP misbehave so you can test retries and recovery. POST
a rule to the account's registry, `{api_base}/faults`, so it only affects that
test. This rule rejects the next submission with a temporary error, then lets
the retry through:

```sh
curl -fsS -X POST http://localhost:8026/api/v1/accounts/ACCOUNT_ID/faults \
  -H 'Content-Type: application/json' -d '{
    "name": "temporary-send-failure",
    "trigger": {"protocol": "smtp", "command": "DATA", "phase": "content"},
    "action": {"type": "reject", "code": 451, "enhanced_code": "4.3.0"},
    "max_hits": 1
  }'
```

`GET {api_base}/faults/temporary-send-failure` returns `hits`, the number of
times the rule applied, which is worth asserting on. Prefer `max_hits` or a
`sequence` such as `["pass", "apply"]` over `probability`, so the test behaves
the same on every run.

Rules are strict: commands are uppercase, each action only works in certain
phases, and unknown fields are rejected with a 400 explaining why. Before
writing anything other than the example above, read
[references/faults.md](references/faults.md); it has the rule format, working
examples for common scenarios (failed login, dropped connection, IMAP errors,
IDLE timeouts, lost acknowledgements) and the constraints that trip people up.

## Other features

- **Enforced limits:** per-account connection, sending-rate and storage
  limits, plus a recipients-per-message limit, reject excess work like a
  provider would. Pass `"limits": {...}` when creating the account. See
  [enforced limits](https://github.com/johlo/mailarky/blob/main/docs/reference/configuration.md#enforced-limits).
- **OAuth (XOAUTH2):** create the account with
  `"oauth_tokens": [{"token": "test-token"}]` and configure the application to
  log in with XOAUTH2 and that token. Tokens can be `expired` or `rejected`,
  and `PUT {api_base}/oauth-tokens` replaces them to test refresh. Mailarky
  doesn't issue tokens, so stub the application's token endpoint. See
  [test OAuth authentication](https://github.com/johlo/mailarky/blob/main/docs/how-to/integrate-with-application.md#test-oauth-authentication).
- **Browsing mail by hand:** the repository's Compose file has an optional
  Roundcube webmail profile.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| Sent mail isn't in the test account | SMTP didn't authenticate, so it went to `default` |
| SMTP AUTH isn't offered | STARTTLS isn't enabled (`MAILARKY_SMTP_TLS_MODE=starttls`), or the client didn't use it |
| TLS verification fails | The client doesn't trust `mailarky.crt`, or connects with another hostname |
| A fault has `hits: 0` | Wrong registry, command (`UID FETCH` and `FETCH` differ), phase or filter; see [references/faults.md](references/faults.md) |
| A message became read unexpectedly | Something fetched `BODY[]` over IMAP |
| `404` from the API | Use `/api/v1/accounts/{id}/...`; the account may already be deleted |

Service logs: `docker logs mailarky`. Full documentation:
<https://github.com/johlo/mailarky/tree/main/docs>.
