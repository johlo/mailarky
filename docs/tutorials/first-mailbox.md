# Your first shared mailbox

You'll send one message over SMTP and then find the same message through
HTTP and IMAP. You need Docker, curl and Python 3. Run the commands from the
repository root.

## Start the service

```sh
docker compose up -d --build --wait
curl -fsS http://localhost:8026/healthz
```

Expect `ok`. If a port is taken, see [run and test](../how-to/run-and-test.md).

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

The message is in `Sent`, the folder where SMTP deliveries go.

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

IMAP returns the exact bytes that SMTP received.

## Add incoming history

```sh
curl -fsS http://localhost:8026/messages -H 'Content-Type: application/json'   -d '{"from":"patient@example.test","to":["clinic@example.test"],"subject":"An older reply","date":"2020-01-02T03:04:05Z"}'
```

Fixtures go to `INBOX` and keep the date you give them. Your application can
now import this history over IMAP.

Stop with `docker compose down`. In-memory mail is lost.

Next: [give each test its own mailbox](../how-to/separate-mailboxes.md) or
[make one delivery fail](../how-to/toxics.md).
