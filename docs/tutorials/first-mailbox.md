# Your first test mailbox

In this tutorial, you will create an email through HTTP and read that same
email through IMAP with certificate verification enabled.

You need access to this repository, Git, Docker with Compose supporting
`--wait`, curl, and Python 3.9 or newer. Python's standard library is sufficient.
Run every command in the same terminal, from the repository root after cloning.

## 1. Start your mailbox

```sh
git clone https://github.com/johlo/imap-emulator.git
cd imap-emulator
export COMPOSE_PROJECT_NAME=imap-tutorial
export IMAP_EMULATOR_PORT=${IMAP_EMULATOR_PORT:-1993}
export IMAP_EMULATOR_HTTP_PORT=${IMAP_EMULATOR_HTTP_PORT:-8026}
docker compose up -d --build --wait --wait-timeout 60
```

The first build downloads the build dependencies. When the command finishes,
the container should be healthy. If those ports are occupied, set different
values as described in [choose host ports](../how-to/run-and-test.md#choose-host-ports)
and repeat the startup command.

Check the fixture endpoint:

```sh
curl -fsS "http://localhost:${IMAP_EMULATOR_HTTP_PORT}/healthz"
```

You should see:

```text
ok
```

## 2. Put a message in INBOX

```sh
curl -fsS "http://localhost:${IMAP_EMULATOR_HTTP_PORT}/messages" \
  -H 'Content-Type: application/json' \
  -d '{"folder":"INBOX","from":"alice@example.test","to":["clinic@example.test"],"subject":"Hello from the tutorial","message_id":"tutorial-hello@example.test","date":"2020-01-02T03:04:05Z","body":"This message came through the fixture API."}'
```

The response confirms the folder and Message-ID:

```json
{"folder":"INBOX","message_id":"tutorial-hello@example.test"}
```

No email was delivered externally. The message is now stored in the running
emulator and available to an IMAP client.

## 3. Read the message over TLS

Run this Python client. It trusts the bundled certificate, logs in with the
test account, searches for your Message-ID, and fetches the message without
marking it as read.

```sh
python3 - <<'PY'
import email
import imaplib
import os
import ssl
from email import policy

context = ssl.create_default_context(cafile="testdata/tls/server.crt")
port = int(os.environ["IMAP_EMULATOR_PORT"])
with imaplib.IMAP4_SSL("localhost", port, ssl_context=context, timeout=10) as client:
    client.login("clinic@example.test", "local-imap-only")
    client.select("INBOX", readonly=True)
    status, matches = client.uid(
        "SEARCH", None, "HEADER", "Message-ID", '"<tutorial-hello@example.test>"'
    )
    assert status == "OK" and matches[0], "Tutorial message was not found"
    uid = matches[0].split()[-1]
    status, parts = client.uid("FETCH", uid, "(BODY.PEEK[])")
    assert status == "OK", "Could not fetch the message"
    raw = next(part[1] for part in parts if isinstance(part, tuple))
    message = email.message_from_bytes(raw, policy=policy.default)
    print("Subject:", message["Subject"])
    print("Message-ID:", message["Message-ID"])
    print("Date:", message["Date"])
    print("Body:", message.get_content().strip())
PY
```

Expected output:

```text
Subject: Hello from the tutorial
Message-ID: <tutorial-hello@example.test>
Date: Thu, 02 Jan 2020 03:04:05 +0000
Body: This message came through the fixture API.
```

You have exercised both interfaces: HTTP created the fixture, and an ordinary
IMAP client retrieved it over verified TLS.

## 4. Stop the tutorial server

```sh
docker compose down
unset COMPOSE_PROJECT_NAME IMAP_EMULATOR_PORT IMAP_EMULATOR_HTTP_PORT
```

Stopping the process discards its messages. A fresh start has empty folders.

Next, [connect your application](../how-to/integrate-with-application.md) or
[seed more test scenarios](../how-to/seed-test-scenarios.md).

[Documentation index](../README.md)
