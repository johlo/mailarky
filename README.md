# Mailarky

Mailarky is a programmable mail server for testing applications that send, read,
or sync email. Give your application a mailbox full of messages, inspect what it
sends, and make the server fail on demand to check how it recovers.

Use it to:

- **Test outgoing mail:** check recipients, subjects, message bodies, and
  attachments in signup emails, receipts, or notifications.
- **Test incoming-mail workflows:** seed a customer reply and verify that your
  application imports it, updates a ticket, or sends a response.
- **Test mailbox synchronization:** import an mbox or Maildir export to exercise
  your application against real message history, folders, attachments, and read
  flags.
- **Test failure recovery:** reject a login, fail a send, impose a quota, or drop
  an IMAP connection, then check retries and reconnection.

Your application connects over standard SMTP and IMAP using its normal mail
libraries. Your tests use an HTTP API to create accounts, load messages,
configure failures, and inspect the results. Inspection doesn’t change read
flags or otherwise disturb the mailbox.

Each test can have its own isolated account, so suites can run in parallel.
Mailarky runs locally or in CI, and captured mail stays inside the test
environment.

> [!WARNING]
> Mailarky is a test service. Its HTTP control API is unauthenticated by
> default, the default password is public, and the bundled TLS key is a public
> fixture. Do not expose it to untrusted networks. See [SECURITY.md](SECURITY.md).

Mailarky is pre-1.0. The HTTP API, configuration, and fault rule schema may
change between minor versions; release notes call out breaking changes.

## Quick start

```sh
docker run -d --name mailarky \
  -e MAILARKY_SMTP_TLS_MODE=starttls \
  -p "127.0.0.1:${MAILARKY_SMTP_PORT:-1025}:1025" \
  -p "127.0.0.1:${MAILARKY_IMAP_PORT:-1993}:1993" \
  -p "127.0.0.1:${MAILARKY_HTTP_PORT:-8026}:8026" \
  ghcr.io/johlo/mailarky:latest
docker cp mailarky:/certs/server.crt ./mailarky.crt
```

To build from source instead, see [run from source](docs/how-to/run-and-test.md#start-locally).

| Interface | Default address |
| --- | --- |
| SMTP | `localhost:1025`, with STARTTLS |
| IMAP | `localhost:1993`, implicit TLS |
| HTTP control API | `http://localhost:8026/api/v1` |

Trust the copied `mailarky.crt` in your test clients. The default credentials
are `user@example.test` / `local-imap-only`, or use a test account's generated
credentials. For optional Roundcube webmail on port 8027, follow
[view mail in a browser](docs/how-to/run-and-test.md#view-mail-in-a-browser).

All published ports bind to loopback. Set `MAILARKY_SMTP_PORT`,
`MAILARKY_IMAP_PORT`, and `MAILARKY_HTTP_PORT` to change them in the command
above. See [configuration](docs/reference/configuration.md) for listener settings.
Pin an image tag such as `ghcr.io/johlo/mailarky:v0.1.0` in CI;
`latest` follows stable releases. Stop and remove the container with
`docker stop mailarky` and `docker rm mailarky`.

To use `go install` or a standalone binary, follow
[run without Docker](docs/how-to/run-and-test.md#run-without-docker), including
the local TLS certificate setup. Use `mailarky --help` for flags and
`mailarky --version` to identify the installed build.

## An account per test

```sh
# Create an account; keep its id, username, password and api_base.
curl -fsS http://localhost:8026/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{}'

# Seed history in that account's INBOX.
curl -fsS http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages \
  -H 'Content-Type: application/json' \
  -d '{"from":"alice@customer.test","to":["app@example.test"],"subject":"Earlier reply","date":"2020-01-02T03:04:05Z","body":"Hello"}'

# Configure the application with the returned credentials for SMTP and IMAP.
# After it sends mail, inspect the account without marking anything as read.
curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages?query=folder:Sent'

# Clean up after the test.
curl -fsS -X DELETE http://localhost:8026/api/v1/accounts/ACCOUNT_ID
```

Authenticated SMTP stores one copy in the authenticated account's `Sent`
folder, regardless of the recipient addresses. Anonymous SMTP routes by the
account's registered recipient addresses, delivering one copy per account in
a multi-recipient transaction. Unclaimed addresses go to `default`.

The [first-account tutorial](docs/tutorials/first-mailbox.md) includes working
SMTP, HTTP, and IMAP clients.

## Programmable failures

Every fault has a **trigger**, optional **filter**, and typed **action**:

```json
{
  "name": "temporary-submit-error",
  "trigger": {"protocol": "smtp", "command": "DATA", "phase": "content"},
  "filter": {"message": {"headers": {"X-Test-ID": "retry-case"}}},
  "action": {"type": "reject", "code": 451, "enhanced_code": "4.3.0"},
  "max_hits": 1
}
```

POST this to `/api/v1/accounts/ACCOUNT_ID/faults`. Server-wide faults use a
separate registry at `/api/v1/faults`. Rules support delays, disconnects,
rejections, malformed replies, capability changes, lost acknowledgements,
UIDVALIDITY resets, selected IMAP content changes, timed IDLE disconnects, and
dropped IDLE notifications. A `sequence` such as `["pass","pass","apply"]`
fails only the third matching event, and `"repeat":true` repeats the pattern.
Inspect match and hit counts, disable rules, or bound them by expiry, maximum
hits, or probability. See [inject failures](docs/how-to/fault-injection.md).

For **latency, bandwidth limits, TCP resets, and connection outages**, combine
Mailarky with [Toxiproxy](https://github.com/Shopify/toxiproxy). Keep the HTTP
control API directly accessible while SMTP or IMAP connections are failing.

## Enforced limits

Limits reject excess work the way a provider would, without any fault rules:
recipients per transaction, authenticated connections per account, sending rate
for authenticated SMTP, and per-account storage quotas. Use them to test how a
client adapts its batching, concurrency, and retries. See
[enforced limits](docs/reference/configuration.md#enforced-limits).

## Test OAuth

Give an account test tokens and authenticate with **XOAUTH2** on SMTP and IMAP,
as with Gmail or Microsoft 365. Tokens can be valid, expired, or rejected, and
can be replaced while the application runs to test refresh. Mailarky doesn't
issue tokens; stub your application's token endpoint separately. See
[test OAuth authentication](docs/how-to/integrate-with-application.md#test-oauth-authentication).

## Use with coding agents

The [`skills/mailarky`](skills/mailarky/SKILL.md) skill teaches coding agents
to write tests against Mailarky: one account per test, authenticated SMTP,
assertions that don't change read state, and valid fault rules. It follows the
[Agent Skills](https://agentskills.io) standard, so the same folder works in
Claude Code, Codex and other compatible agents. Only the install location
differs:

| Agent | One project | All projects |
| --- | --- | --- |
| Claude Code | `.claude/skills/` | `~/.claude/skills/` |
| Codex | `.agents/skills/` | `~/.agents/skills/` |

For example, to add it to a project for Claude Code (use `.agents/skills` for
Codex):

```sh
mkdir -p .claude/skills
curl -fsSL https://github.com/johlo/mailarky/archive/refs/heads/main.tar.gz |
  tar -xz -C .claude/skills --strip-components=2 mailarky-main/skills/mailarky
```

Release archives include the skill for that version.

## Focus and boundaries

- Raw MIME is the source of truth. HTTP inspection has no read-state side effects;
  IMAP BODY fetches set `\Seen`, while BODY.PEEK and EXAMINE do not.
- Selected IMAP sessions track their own sequence numbers. External changes
  produce EXISTS, EXPUNGE, and flag updates at safe command boundaries and during IDLE.
- Storage is in memory by default. Optional bbolt persistence preserves accounts,
  credentials, messages, IMAP metadata, and UID identity. Fault rules are ephemeral.
- IMAP supports METADATA for client settings and UIDPLUS for tracking appended
  and copied messages and selectively expunging deleted mail.
- Retention is unlimited by default. Optional retention prunes old mail; storage
  quotas reject new mail without evicting fixtures. Both span an account's folders.
- Mail is captured locally. There is no relay, forwarding, POP3, or built-in webmail UI.
  Roundcube provides browsing over the real mail protocols.
- SMTP has one final result per transaction. A content fault rejects a fan-out
  transaction before storing copies; an after fault runs after storage. Commits
  across separate account databases are not atomic if a persistence write fails.
- This is a single-process test service. The HTTP control API administers all accounts;
  it is not a tenancy boundary for mutually untrusted users.

## Documentation

| Task | Guide |
| --- | --- |
| Connect an application | [Integration](docs/how-to/integrate-with-application.md) |
| Isolate parallel tests | [Accounts](docs/how-to/separate-mailboxes.md) |
| Seed history or raw MIME | [Fixtures](docs/how-to/seed-test-scenarios.md) |
| Load an exported mailbox | [Import](docs/how-to/import-mailbox-export.md) |
| Run, persist, or browse mail | [Run and test](docs/how-to/run-and-test.md) |
| Look up API and settings | [HTTP API](docs/reference/http-api.md), [OpenAPI](openapi.yaml), [configuration](docs/reference/configuration.md) |
| Understand protocol behavior | [IMAP](docs/reference/imap-behavior.md), [capabilities](docs/reference/capabilities.md) |
| Understand the implementation | [Architecture](docs/explanation/architecture.md), [design](docs/explanation/design.md) |
| Diagnose a problem | [Troubleshooting](docs/how-to/troubleshoot.md) |

## Development

`make test` runs Go tests with the race detector and then `go vet`. Tests cover
real SMTP/IMAP sockets, account isolation, persistence, API behavior, fault
recovery, IMAP updates, and concurrent mutation. `scripts/smoke.py` exercises the
Docker service using Python's independent SMTP and IMAP clients.

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, testing, updating the
protocol forks, and publishing releases.

## Acknowledgements

Mailarky's protocol implementations are built on
[emersion/go-imap v2](https://github.com/emersion/go-imap/tree/v2) and
[emersion/go-smtp](https://github.com/emersion/go-smtp). Mailarky imports public
[IMAP](https://github.com/johlo/go-imap/tree/imap-v2-protocol-hooks) and
[SMTP](https://github.com/johlo/go-smtp/tree/smtp-protocol-hooks) forks that add
the hooks needed for protocol fault injection. They use their own module paths,
so `go.mod` pins exact revisions without `replace` directives. Each fork keeps
its upstream license and tests and runs its own CI.

go-imap v2 is still in development upstream, so Mailarky keeps independent
protocol tests around its session backend and fault hooks. The listener
advertises IMAP4rev1; IMAP4rev2 features are not enabled.

## License

Mailarky is licensed under the [MIT License](LICENSE).
