# Your first shared mailbox

This tutorial uses Docker Compose, curl and Python 3's standard library. Start
from a clone of this repository and run commands from its root.

## Start the service

```sh
docker compose up -d --build --wait
curl -fsS http://localhost:8026/healthz
```

Expect `ok`. If ports are occupied, see [host port overrides](../how-to/run-and-test.md).

## Send one message over SMTP

```sh
python3 - <<'PYTHON'
import smtplib
from email.message import EmailMessage
mail = EmailMessage()
mail['From'] = 'alice@example.test'
mail['To'] = 'clinic@example.test'
mail['Message-ID'] = '<tutorial@example.test>'
mail['X-Test-ID'] = 'tutorial'
mail['Subject'] = 'Shared mailbox'
mail.set_content('The same mail is available through SMTP, HTTP and IMAP.')
with smtplib.SMTP('localhost', 1025) as smtp:
    smtp.send_message(mail)
PYTHON
```

## Inspect it through HTTP

```sh
curl -fsS 'http://localhost:8026/api/v1/search?query=message-id:tutorial@example.test'
curl -fsS http://localhost:8026/api/v1/message/latest/raw
```

The search contains a message with `Folder: Sent`. Its database `ID` differs
from the `MessageID` header and the numeric IMAP `UID`.

## Read it with verified TLS IMAP

```sh
python3 - <<'PYTHON'
import imaplib, ssl
context = ssl.create_default_context(cafile='testdata/tls/server.crt')
with imaplib.IMAP4_SSL('localhost', 1993, ssl_context=context) as imap:
    imap.login('clinic@example.test', 'local-imap-only')
    imap.select('Sent', readonly=True)
    status, results = imap.uid('search', None, 'HEADER', 'Message-ID', 'tutorial@example.test')
    assert status == 'OK' and results[0]
    status, message = imap.uid('fetch', results[0].split()[0], '(BODY.PEEK[])')
    assert status == 'OK'
    print(message[0][1].decode())
PYTHON
```

The returned MIME is the SMTP delivery. No copying job is involved.

## Add incoming history

```sh
curl -fsS http://localhost:8026/messages -H 'Content-Type: application/json'   -d '{"from":"patient@example.test","to":["clinic@example.test"],"subject":"An older reply","date":"2020-01-02T03:04:05Z"}'
```

This appears in `INBOX`. HTTP `Created` records ingestion now; the Date header
is historical. Stop with `docker compose down`; default in-memory mail is lost.
Next, try [a failure scoped to one test](../how-to/toxics.md).
