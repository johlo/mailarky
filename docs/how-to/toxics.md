# Simulate failures with toxics

A toxic makes the sandbox misbehave for **specific messages only**, such as
rejecting one SMTP delivery or hiding one message from IMAP. Other tests keep
working normally, even in parallel.

## Example

Reject the next delivery that carries `X-Test-ID: run-42`:

```sh
curl -fsS http://localhost:8026/api/v1/toxics -H 'Content-Type: application/json' -d '{
  "name": "run-42-reject",
  "type": "smtp_reject",
  "selector": {"headers": {"X-Test-ID": "run-42"}},
  "attributes": {"stage": "data", "code": 451},
  "max_hits": 1
}'

# ... run the test ...

curl -fsS -X DELETE http://localhost:8026/api/v1/toxics/run-42-reject
```

For a [separate mailbox](separate-mailboxes.md), prefix the path with its
`api_base`.

## Types

| Type | Attributes | Effect |
| --- | --- | --- |
| `smtp_reject` | `code` (400–599), `stage` | Rejects the SMTP transaction. Rejected mail is not stored. |
| `smtp_delay` | `delay_ms` (≤ 30000), `stage` | Delays the SMTP command |
| `imap_delay` | `delay_ms` (≤ 30000) | Delays SEARCH/FETCH results for the message |
| `imap_hide` | — | Leaves the message out of SEARCH/FETCH results |
| `imap_replace_header` | `header`, `value` | Changes a header in the IMAP response (empty value removes it) |

SMTP `stage` is `sender`, `recipient` or `data` (the default). Before `data`
the server knows only envelope addresses, so select by `from`/`to` there.

IMAP toxics change only what IMAP returns. The stored message and the HTTP API
are unaffected.

## Selectors

All given fields must match. Provide at least one of `message_id`, `headers`,
`from` or `to`.

| Field | Matches |
| --- | --- |
| `message_id` | Exact Message-ID (angle brackets optional) |
| `headers` | Exact header values; header names ignore case |
| `from` / `to` | Exact envelope address, ignoring case |
| `folder` | Additional restriction on the folder |

Wildcards and selectors that match everything are rejected.

## Options and lifecycle

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | `PATCH /api/v1/toxics/{name}` with `{"enabled": false}` switches it off |
| `probability` | `1` | Chance (0–1) that a matching message is affected |
| `max_hits` | `0` (unlimited) | Stop after this many hits. `hits` shows the count. |

`PUT /api/v1/toxics/{name}` replaces a toxic, `GET` inspects it, `DELETE`
removes it. Toxics are lost on restart.

## Keeping tests isolated

- Use a unique token per test (a UUID in `X-Test-ID`, Message-ID or recipient).
  A shared address such as the clinic mailbox would hit every test.
- Remove your toxics in `finally`/defer. Never delete other tests' toxics.
- Keep one test's messages in their own SMTP transaction and IMAP command. A
  delay or rejection applies to the whole command it happens in.

There is no global failure switch. `/api/v1/chaos` exists only to return an error.
