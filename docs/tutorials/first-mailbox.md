# Your first test account

This tutorial sends a message through SMTP, inspects it through HTTP, and reads
it through IMAP. You need Docker Compose and Python 3.

Start Mailarky from the repository root:

```sh
docker compose up -d --build --wait
```

Save the following as a temporary Python script and run it from the repository
root. It creates and removes its own account.

```python
import imaplib
import json
import smtplib
import ssl
import urllib.request

base = "http://localhost:8026"

def api(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data, method=method,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req) as response:
        raw = response.read()
        return json.loads(raw) if raw else None

account = api("POST", "/api/v1/accounts", {})
account_base = account["api_base"]
context = ssl.create_default_context(cafile="testdata/tls/server.crt")
try:
    with smtplib.SMTP("localhost", 1025) as smtp:
        smtp.starttls(context=context)
        smtp.login(account["username"], account["password"])
        smtp.sendmail("app@example.test", ["alice@customer.test"],
                      "From: app@example.test\r\nTo: alice@customer.test\r\n"
                      "Subject: Welcome\r\nMessage-ID: <first@example.test>\r\n\r\nHello!")

    listing = api("GET", account_base + "/messages?query=folder:Sent")
    assert listing["total"] == 1
    message = api("GET", account_base + "/messages/" + listing["messages"][0]["id"])
    assert message["subject"] == "Welcome"
    assert message["read"] is False

    with imaplib.IMAP4_SSL("localhost", 1993, ssl_context=context) as imap:
        imap.login(account["username"], account["password"])
        imap.select("Sent")
        status, result = imap.uid("search", None, "ALL")
        assert status == "OK" and result[0]
        status, result = imap.uid("fetch", result[0], "(BODY.PEEK[])")
        assert status == "OK"
        assert b"Hello!" in result[0][1]
finally:
    api("DELETE", "/api/v1/accounts/" + account["id"])
```

The message is captured in the submitting account's Sent folder even though
`alice@customer.test` was never registered. HTTP inspection and BODY.PEEK leave
it unread. A normal BODY fetch in a writable selection would mark it read.

Next, [seed incoming history](../how-to/seed-test-scenarios.md) or
[inject a retryable failure](../how-to/fault-injection.md).
