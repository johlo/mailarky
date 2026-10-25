#!/usr/bin/env python3
"""Exercise the Compose service using independent standard-library clients."""
import gzip
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

    export = (b"From 1@xxx Fri Oct 02 12:00:00 +0000 2020\n"
              b"X-Gmail-Labels: Sent,Opened,Starred,Work/Projects\n"
              b"From: me@example.test\nTo: alice@customer.test\n"
              b"Message-ID: <imported@example.test>\n\n>From the archive\n")
    request = urllib.request.Request(base + second["api_base"] + "/import?labels=gmail", gzip.compress(export),
                                     method="POST", headers={"Content-Type": "application/mbox"})
    with urllib.request.urlopen(request, timeout=10) as response:
        report = json.loads(response.read())
    assert report["imported"] == 2 and report["folders"] == {"Sent": 1, "Work/Projects": 1}, report
    with imaplib.IMAP4_SSL(host, imap_port, ssl_context=context, timeout=10) as imap:
        imap.login(second["username"], second["password"])
        assert imap.select('"Work/Projects"')[0] == "OK"
        status, data = imap.fetch("1", "(FLAGS BODY.PEEK[])")
        assert status == "OK" and b"\\Flagged" in data[0][0] and b"\\Seen" in data[0][0], data
        assert data[0][1].endswith(b"\r\n\r\nFrom the archive\r\n"), data
    with imaplib.IMAP4_SSL(host, imap_port, ssl_context=context, timeout=10) as imap:
        imap.login(first["username"], first["password"])
        capabilities = imap.capability()[1][0].split()
        assert b"METADATA" in capabilities and b"UIDPLUS" in capabilities
        assert imap.xatom("SETMETADATA", '""', '(/private/vendor/alps/settings "saved-settings")')[0] == "OK"
        assert imap.xatom("GETMETADATA", "(DEPTH infinity)", '""', "/private/vendor/alps")[0] == "OK"
        assert b'"/private/vendor/alps/settings" "saved-settings"' in imap.response("METADATA")[1][0]
        assert imap.create("UIDPlusSmoke")[0] == "OK"
        assert imap.create("UIDPlusCopy")[0] == "OK"
        validity = None
        for uid in range(1, 4):
            assert imap.append("UIDPlusSmoke", None, None, mime(f"uidplus-{uid}@example.test"))[0] == "OK"
            epoch, assigned = imap.response("APPENDUID")[1][0].split()
            assert int(assigned) == uid and int(epoch) > 0
            assert validity is None or validity == epoch
            validity = epoch
        assert imap.select("UIDPlusSmoke")[0] == "OK"
        assert imap.uid("COPY", "1,3", "UIDPlusCopy")[0] == "OK"
        epoch, source, destination = imap.response("COPYUID")[1][0].split()
        assert int(epoch) > 0 and source == b"1,3" and destination == b"1:2"
        assert imap.uid("STORE", "1:2", "+FLAGS", r"(\Deleted)")[0] == "OK"
        assert imap.uid("EXPUNGE", "2:3")[0] == "OK"
        assert imap.uid("SEARCH", None, "ALL")[1][0] == b"1 3"
    for account in accounts:
        with imaplib.IMAP4_SSL(host, imap_port, ssl_context=context, timeout=10) as imap:
            imap.login(account["username"], account["password"])
            assert imap.xatom("GETMETADATA", '""', "/private/vendor/alps/settings")[0] == "OK"
            value = imap.response("METADATA")[1][0]
            expected = b'"saved-settings"' if account == first else b"NIL"
            assert expected in value, value
    print("SMTP, HTTP, TLS IMAP, faults, import, METADATA isolation and UIDPLUS passed")
finally:
    for account in accounts:
        api("DELETE", "/api/v1/accounts/" + account["id"])
