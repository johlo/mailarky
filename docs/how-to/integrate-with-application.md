# Connect an application

Start the [Compose stack](run-and-test.md), create an account, and configure
both mail clients with the credentials returned by `POST /api/v1/accounts`.

| Setting | Application on host | Application in the same Compose network |
| --- | --- | --- |
| SMTP host | `localhost` | `mailarky` |
| SMTP port | `1025` or `MAILARKY_SMTP_PORT` | `1025` |
| SMTP security | STARTTLS with the bundled CA trusted | STARTTLS with the bundled CA trusted |
| IMAP host | `localhost` | `mailarky` |
| IMAP port | `1993` or `MAILARKY_IMAP_PORT` | `1993` |
| IMAP security | Implicit TLS | Implicit TLS |
| HTTP base | `http://localhost:8026/api/v1` | `http://mailarky:8026/api/v1` |

Trust `testdata/tls/server.crt`; it covers `localhost`, `mailarky`, and
`127.0.0.1`. The private key is an intentional public test fixture. Supply your
own certificate when using a different hostname.

Authenticate SMTP even if authentication is optional: AUTH is what ties
outgoing customer mail to the test account. The recipients can be arbitrary
addresses, including the same addresses used by other tests. Mail is stored in
that account's `Sent`; no external delivery occurs. Seed incoming history in
`INBOX` or a chosen folder via HTTP.

For an application that cannot use SMTP AUTH, configure its recipients to the
account's registered recipient addresses. Unclaimed anonymous recipients route
to `default`.

When running the binary outside Compose, enable STARTTLS with
`MAILARKY_SMTP_TLS_MODE=starttls`, or explicitly allow local plaintext AUTH
with `MAILARKY_SMTP_AUTH_ALLOW_INSECURE=true`. IMAP always uses implicit TLS.

## Test OAuth authentication

Configure local test tokens when creating the application's account:

```sh
curl -fsS http://localhost:8026/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{
    "username":"app@example.test",
    "oauth_tokens":[
      {"token":"test-access-token"},
      {"token":"old-access-token","status":"expired"},
      {"token":"blocked-access-token","status":"rejected"}
    ]
  }'
```

Set the application to use **XOAUTH2**, the returned username, and
`test-access-token` on SMTP and IMAP. The same TLS requirements apply as for
password authentication. This exercises the SASL format used by
[Microsoft 365](https://learn.microsoft.com/en-us/exchange/client-developer/legacy-protocols/how-to-authenticate-an-imap-pop-smtp-application-by-using-oauth)
and [Gmail](https://developers.google.com/workspace/gmail/imap/xoauth2-protocol):
base64 of `user=USERNAME\x01auth=Bearer TOKEN\x01\x01`.

Use `old-access-token` to trigger an expired-token response, or
`blocked-access-token` for rejection. For time-based expiry, give a token an
`expires_at` RFC3339 timestamp. Test renewal by replacing the set while the
application is running:

```sh
curl -fsS -X PUT http://localhost:8026/api/v1/accounts/ACCOUNT_ID/oauth-tokens \
  -H 'Content-Type: application/json' -d '{
    "tokens":[
      {"token":"test-access-token","status":"expired"},
      {"token":"refreshed-access-token"}
    ]
  }'
```

Make the application's token supplier return `refreshed-access-token` on
refresh. Existing authenticated sessions remain usable, so reconnect to test
authentication with the changed token. Use an account IDLE disconnect fault
when testing that reconnect path.

Mailarky tests mail authentication, not the OAuth HTTP flow. Stub the
application's token endpoint or token supplier separately; no real Gmail or
Microsoft tokens, app registrations, or provider connections are needed.
See [token reference](../reference/http-api.md#test-oauth-tokens) for exact
failure replies. Existing AUTH/AUTHENTICATE faults and deterministic sequences
also apply to XOAUTH2, for example to reject only the third authentication.
