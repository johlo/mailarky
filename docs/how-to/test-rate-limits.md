# Test rate limits and client backoff

Use per-account SMTP and IMAP command limits to test request pacing, shared
connection pools, and recovery from overload. Start Mailarky v0.6.0 or later
using the [quick start](../../README.md#quick-start), with SMTP STARTTLS enabled.

## Configure the application's allowance

Create an account with independent command counts and time windows:

```sh
curl -fsS http://localhost:8026/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{
    "limits": {
      "smtp_commands": 10,
      "smtp_command_window": "1s",
      "imap_commands": 20,
      "imap_command_window": "1s"
    }
  }'
```

Configure your application's SMTP and IMAP clients with the returned username
and password. Limits apply after authentication; anonymous SMTP does not
consume a recipient account's allowance. Keep the account ID for cleanup.

Choose limits based on **commands**, not messages. Sending one SMTP message
usually needs MAIL, one RCPT per recipient, and DATA. Each BDAT chunk also
counts. IMAP SELECT, SEARCH, FETCH, NOOP, and their supported UID forms consume
the IMAP allowance. Entry into IDLE counts once; notifications and DONE do not.

Limits are fixed when the account is created. Create a fresh account for each
scenario or threshold. Omitted command counts, or `0`, mean unlimited. Each
nonzero count requires its corresponding positive window duration. A `limits`
object replaces all account defaults, so include any additional connection,
submission, or storage limits needed by the test.

## Assert overload and recovery

Run these checks against your application's client:

1. Keep its command rate below the allowance and assert normal operation.
2. Exceed the allowance within a window. Expect SMTP `451 4.7.0` or a tagged
   IMAP `NO [LIMIT]`, then assert that the client waits before retrying.
3. Retry after the window expires and assert successful recovery, including
   on the same connection. Check message storage through HTTP to verify that
   rejected delivery or APPEND attempts did not create mail.
4. Repeat with multiple connections using the same credentials. Their combined
   commands share one allowance per protocol. Reconnecting must not bypass it.
5. Run another account concurrently and verify that its allowance is independent.

The window starts with the first admitted command. Admitted commands count even
if normal validation or a fault later rejects them; rate-limited attempts do
not extend the window. Fixed windows can admit bursts near a boundary. For a
repeatable threshold check, use a fresh account and send a short burst of NOOPs.

HTTP seeding and inspection remain available during overload and do not consume
either protocol's allowance. SMTP RSET/QUIT and IMAP LOGOUT remain available
for cleanup without consuming capacity. Existing fault rules still apply.

## Run a complete protocol check

This Python 3 example creates its own account, exhausts both allowances,
checks HTTP availability, retries after expiry on the same connections, and
deletes the account. It trusts the certificate published by the local server.
Set `MAILARKY_HTTP_URL`, `MAILARKY_SMTP_PORT`, or `MAILARKY_IMAP_PORT` if your
listeners use different ports.

```sh
python3 - <<'PY'
import imaplib
import json
import os
import smtplib
import ssl
import time
import urllib.request

base = os.environ.get("MAILARKY_HTTP_URL", "http://localhost:8026").rstrip("/")
smtp_port = int(os.environ.get("MAILARKY_SMTP_PORT", "1025"))
imap_port = int(os.environ.get("MAILARKY_IMAP_PORT", "1993"))

def api(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(base + path, data, method=method,
                                     headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=10) as response:
        raw = response.read()
        return json.loads(raw) if raw else None

with urllib.request.urlopen(base + "/api/v1/certificate", timeout=10) as response:
    tls = ssl.create_default_context(cadata=response.read().decode())

account = api("POST", "/api/v1/accounts", {"limits": {
    "smtp_commands": 3, "smtp_command_window": "2s",
    "imap_commands": 3, "imap_command_window": "2s",
}})
try:
    with smtplib.SMTP("localhost", smtp_port, timeout=10) as smtp:
        smtp.starttls(context=tls)
        smtp.login(account["username"], account["password"])
        with imaplib.IMAP4_SSL("localhost", imap_port,
                              ssl_context=tls, timeout=10) as imap:
            imap.login(account["username"], account["password"])
            for _ in range(3):
                assert smtp.noop()[0] == 250
                assert imap.noop()[0] == "OK"
            code, reply = smtp.noop()
            assert code == 451 and b"4.7.0" in reply, (code, reply)
            status, reply = imap.noop()
            assert status == "NO" and b"[LIMIT]" in reply[0], (status, reply)
            assert api("GET", account["api_base"] + "/messages")["total"] == 0
            time.sleep(2.1)
            assert smtp.noop()[0] == 250
            assert imap.noop()[0] == "OK"
    print("SMTP and IMAP overload, HTTP inspection, and recovery passed")
finally:
    api("DELETE", account["api_base"])
PY
```

Use the same checks around your application's send or sync operation to verify
its retry behavior. The example's fixed wait checks server recovery; assert your
client's own backoff policy using [command history](inspect-client-behavior.md).
Compare each retry's `started_at` with the previous rejection's `completed_at`,
and group by `connection_id` when checking reconnections. For an account created
manually, clean up with `DELETE /api/v1/accounts/ACCOUNT_ID` after the test.

For limits on successful SMTP submissions, use `send_messages` and `send_window`.
For simultaneous sessions, use `smtp_connections` and `imap_connections`.
See [enforced limits](../reference/configuration.md#enforced-limits) for all
settings, responses, YAML keys, environment variables, and flags. Combine rate
limits with [fault rules](fault-injection.md) to test additional failures.
