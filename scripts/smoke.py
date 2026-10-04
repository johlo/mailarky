#!/usr/bin/env python3
"""Exercise the Compose service using independent standard-library clients."""
import imaplib
import json
import os
from pathlib import Path
import smtplib
import ssl
import urllib.request
import uuid

base = os.environ.get("MAILARKY_HTTP_URL", "http://localhost:8026").rstrip("/")
host = os.environ.get("MAILARKY_SMOKE_HOST", "localhost")
smtp_port = int(os.environ.get("MAILARKY_SMTP_PORT", "1025"))
imap_port = int(os.environ.get("MAILARKY_IMAP_PORT", "1993"))
context = ssl.create_default_context(cafile=str(Path(__file__).resolve().parents[1] / "testdata/tls/server.crt"))


def api(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(base + path, data, method=method,
                                     headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=10) as response:
        raw = response.read()
        return json.loads(raw) if raw else None


def mime(message_id):
    return (f"From: app@example.test\r\nTo: alice@customer.test\r\n"
            f"Subject: Mailarky smoke\r\nMessage-ID: <{message_id}>\r\n"
            f"Content-Type: text/plain; charset=utf-8\r\n\r\nHello {message_id}\r\n").encode()


def submit(account, message_id, recipients=None):
    with smtplib.SMTP(host, smtp_port, timeout=10) as smtp:
        if account is not None:
            smtp.starttls(context=context)
            smtp.login(account["username"], account["password"])
        smtp.sendmail("app@example.test", recipients or ["alice@customer.test"], mime(message_id))


accounts = []
try:
    assert api("GET", "/healthz")["status"] == "ok"
    for _ in range(2):
        accounts.append(api("POST", "/api/v1/accounts", {}))
    first, second = accounts
    message_id = f"smoke-{uuid.uuid4()}@example.test"
    for account in accounts:
        submit(account, message_id)
        listing = api("GET", account["api_base"] + "/messages")
        assert listing["total"] == 1
        assert listing["messages"][0]["message_id"] == message_id
    message = api("GET", first["api_base"] + "/messages")["messages"][0]
    message_path = first["api_base"] + "/messages/" + message["id"]
    assert api("GET", message_path)["read"] is False

    with imaplib.IMAP4_SSL(host, imap_port, ssl_context=context, timeout=10) as imap:
        imap.login(first["username"], first["password"])
        assert imap.select("Sent")[0] == "OK"
        status, data = imap.uid("search", None, "ALL")
        assert status == "OK" and data[0] == b"1"
        status, body = imap.uid("fetch", data[0], "(BODY.PEEK[])")
        assert status == "OK" and mime(message_id) == body[0][1]
        assert api("GET", message_path)["read"] is False
        assert imap.uid("fetch", data[0], "(BODY[])")[0] == "OK"
        assert api("GET", message_path)["read"] is True

        api("POST", first["api_base"] + "/faults", {
            "name": "select-once", "max_hits": 1,
            "trigger": {"protocol": "imap", "command": "SELECT", "phase": "before"},
            "filter": {"folder": "INBOX"},
            "action": {"type": "reject", "status": "NO", "response_code": "UNAVAILABLE"},
        })
        assert imap.select("INBOX")[0] == "NO"
        assert imap.select("INBOX")[0] == "OK"

    retry_id = f"retry-{uuid.uuid4()}@example.test"
    api("POST", first["api_base"] + "/faults", {
        "name": "reject-once", "max_hits": 1,
        "trigger": {"protocol": "smtp", "command": "DATA", "phase": "content"},
        "filter": {"message": {"message_id": retry_id}},
        "action": {"type": "reject", "code": 451, "enhanced_code": "4.3.0"},
    })
    try:
        submit(first, retry_id)
    except smtplib.SMTPDataError as error:
        assert error.smtp_code == 451
    else:
        raise AssertionError("SMTP content fault did not reject")
    assert api("GET", first["api_base"] + "/messages")["total"] == 1
    submit(first, retry_id)
    assert api("GET", first["api_base"] + "/faults/reject-once")["hits"] == 1
    assert api("GET", second["api_base"] + "/messages")["total"] == 1

    submit(None, f"fanout-{uuid.uuid4()}@example.test",
           [first["recipients"][0], second["recipients"][0]])
    assert api("GET", first["api_base"] + "/messages")["total"] == 3
    assert api("GET", second["api_base"] + "/messages")["total"] == 2
    print("Authenticated SMTP, fan-out, HTTP inspection, verified TLS IMAP, read flags, faults and recovery passed")
finally:
    for account in accounts:
        api("DELETE", "/api/v1/accounts/" + account["id"])
