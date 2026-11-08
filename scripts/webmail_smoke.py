#!/usr/bin/env python3
"""Exercise the optional Alps profile through its HTTP interface and Mailarky."""
from contextlib import ExitStack
from email import policy
from email.message import EmailMessage
from email.parser import BytesParser
from email.utils import formatdate
import http.cookiejar
import json
import os
import urllib.error
import urllib.parse
import urllib.request
import uuid


class Client:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, method, path, body=None, content_type="application/json"):
        if body is not None and content_type == "application/json":
            body = json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, body, method=method,
            headers={"Content-Type": content_type, "Origin": self.base})
        try:
            response = self.opener.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            raise AssertionError(f"{method} {path}: {error.code} {error.read().decode()}") from error
        with response:
            raw = response.read()
            if not raw:
                return None
            return json.loads(raw) if "application/json" in response.headers.get("Content-Type", "") else raw

    def form(self, method, path, fields):
        return self.request(method, path, urllib.parse.urlencode(fields).encode(),
                            "application/x-www-form-urlencoded")

    def login(self, account):
        assert self.request("POST", "/session", {
            "username": account["username"], "password": account["password"],
        })["ok"]


def main():
    api = Client(os.environ.get("MAILARKY_HTTP_URL", "http://localhost:8026"))
    webmail = os.environ.get("MAILARKY_WEBMAIL_URL", "http://localhost:8027")
    unique = uuid.uuid4().hex
    attachment = b"Attachment inspected through Alps.\n"
    with ExitStack() as cleanup:
        accounts, clients = [], []
        # Cover generated bare usernames and case-sensitive email usernames.
        for request in [{}, {"username": f"AlpsCase-{unique}@example.test"}]:
            account = api.request("POST", "/api/v1/accounts", request)
            cleanup.callback(api.request, "DELETE", account["api_base"])
            accounts.append(account)
            client = Client(webmail)
            cleanup.callback(client.request, "DELETE", "/session")
            clients.append(client)
            client.login(account)
            assert client.request("GET", "/session")["EnabledPlugins"] == ["base"]
        first, second = accounts
        client, other = clients

        assert b"Open an account" in client.request("GET", "/")
        assert b"<app-root>" in client.request("GET", "/alps")
        listed = client.request("GET", "/mailarky/accounts")
        assert {"default", first["id"], second["id"]} <= {a["id"] for a in listed}
        assert all("password" not in a for a in listed)
        client.request("DELETE", "/session")
        assert client.request("POST", f"/mailarky/accounts/{first['id']}/login")["ok"]

        incoming = EmailMessage(policy=policy.SMTP)
        incoming["From"] = "Sender <sender@example.test>"
        incoming["To"] = "reader@example.test"
        incoming["Subject"] = "Alps incoming " + unique
        incoming["Date"] = formatdate(usegmt=True)
        incoming["Message-ID"] = f"<alps-incoming-{unique}@example.test>"
        incoming.set_content("Incoming message body for Alps.")
        incoming.add_alternative("<p>Incoming <b>HTML</b> for Alps.</p>", subtype="html")
        incoming.add_attachment(attachment, maintype="application", subtype="octet-stream", filename="fixture.txt")
        api.request("POST", first["api_base"] + "/messages?folder=INBOX",
                    incoming.as_bytes(), "message/rfc822")

        inbox = client.request("GET", "/mailboxes/INBOX?refresh=1")
        assert inbox["Address"] == first["username"] + "@example.test"
        assert inbox["Total"] == 1, inbox
        assert inbox["Messages"][0]["Envelope"]["Subject"] == incoming["Subject"]
        uid = inbox["Messages"][0]["UID"]
        raw = client.request("GET", f"/mailboxes/INBOX/messages/{uid}/raw")
        parsed = BytesParser(policy=policy.default).parsebytes(raw)
        assert list(parsed.iter_attachments())[0].get_payload(decode=True) == attachment
        assert other.request("GET", "/mailboxes/INBOX")["Total"] == 0

        client.request("PUT", "/mailboxes/INBOX/messages/flag", {
            "uids": [uid], "flags": ["\\Seen"], "action": "add",
        })
        assert api.request("GET", first["api_base"] + "/messages")["messages"][0]["read"]
        client.form("POST", "/mailboxes", {"name": "SmokeFolder"})
        client.request("PUT", "/mailboxes/INBOX/messages/move", {"uids": [uid], "to": "SmokeFolder"})
        assert client.request("GET", "/mailboxes/INBOX?refresh=1")["Total"] == 0
        moved = client.request("GET", "/mailboxes/SmokeFolder")
        assert moved["Total"] == 1
        assert moved["Messages"][0]["Envelope"]["Subject"] == incoming["Subject"]

        client.request("PUT", "/settings", {"from": "Alps Smoke", "signature": "Saved via METADATA"})
        client.request("DELETE", "/session")
        assert client.request("POST", f"/mailarky/accounts/{first['id']}/login")["ok"]
        settings = client.request("GET", "/settings")["Settings"]
        assert settings["signature"] == "Saved via METADATA", settings
        assert other.request("GET", "/settings")["Settings"].get("signature", "") == ""

        boundary = "alps-" + unique
        upload = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"attachments\"; filename=\"sent.txt\"\r\n"
                  "Content-Type: text/plain\r\n\r\n").encode() + attachment + f"\r\n--{boundary}--\r\n".encode()
        uploaded = client.request("POST", "/attachments", upload, "multipart/form-data; boundary=" + boundary)
        assert len(uploaded) == 1, uploaded
        outgoing = {"to": "recipient@example.test", "subject": "Alps sent " + unique,
                    "text": "Sent through SMTP STARTTLS.", "attachment-uuids": uploaded[0]}
        draft = client.form("POST", "/messages", dict(outgoing, save_as_draft="1"))
        assert draft["ok"] and draft["draft_uid"], draft
        assert len(draft["attachments"]) == 1, draft
        # Saving consumes the upload; send the attachment from the stored draft.
        del outgoing["attachment-uuids"]
        outgoing["prev_attachments"] = draft["attachments"][0]["partPath"]
        sent = client.form("POST", "/messages", dict(outgoing,
            draft_mailbox=draft["draft_mailbox"], draft_uid=draft["draft_uid"]))
        assert sent["ok"], sent
        assert client.request("GET", "/mailboxes/Drafts?refresh=1")["Total"] == 0
        assert client.request("GET", "/mailboxes/Sent?refresh=1")["Total"] == 1
        listing = api.request("GET", first["api_base"] + "/messages?query=folder:Sent")
        assert listing["matched"] == 1, listing  # No duplicate IMAP Sent copy.
        captured = listing["messages"][0]
        assert captured["subject"] == outgoing["subject"]
        captured_raw = api.request("GET", first["api_base"] + "/messages/" + captured["id"] + "/raw")
        parsed = BytesParser(policy=policy.default).parsebytes(captured_raw)
        assert parsed["From"].addresses[0].addr_spec == inbox["Address"]
        assert list(parsed.iter_attachments())[0].get_payload(decode=True) == attachment
        assert api.request("GET", second["api_base"] + "/messages")["total"] == 0

        # Email-shaped usernames retain their address for both login and sending.
        other.form("POST", "/messages", {"to": "recipient@example.test", "subject": "Case preserved", "text": "Hello"})
        listing = api.request("GET", second["api_base"] + "/messages")
        assert listing["total"] == 1, listing
        assert listing["messages"][0]["from"]["address"] == second["username"]

        history = api.request("GET", first["api_base"] + "/history?limit=1000")
        assert history["dropped"] == 0 and not history["has_more"]
        assert any(e["protocol"] == "smtp" and e["command"] == "AUTH" and e["response"].startswith("235") for e in history["commands"])
        assert any(e["command"] == "SETMETADATA" and e["response"] == "OK" for e in history["commands"])
        assert any(e["command"] == "UID COPY" and e["response"].startswith("OK") for e in history["commands"])

        # The same browser session can choose a different account without using
        # either account's original password or seeing the previous account's mail.
        client.request("DELETE", "/session")
        assert client.request("POST", f"/mailarky/accounts/{second['id']}/login")["ok"]
        assert client.request("GET", "/mailboxes/INBOX?refresh=1")["Total"] == 0
        switched = client.request("GET", "/mailboxes/Sent?refresh=1")
        assert switched["Total"] == 1
        assert switched["Messages"][0]["Envelope"]["Subject"] == "Case preserved"
    print("Alps account picker, login, account isolation, message/attachment inspection, folders, settings, drafts and SMTP submission passed")


if __name__ == "__main__":
    main()
