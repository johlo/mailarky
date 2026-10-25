#!/usr/bin/env python3
"""Write mailbox import fixtures with Python's mailbox module.

Python's mbox and Maildir writers are independent of Mailarky's reader, so
these files catch format assumptions that hand-written samples would share
with the code. Run from the repository root; the output is deterministic.
"""
from email.message import EmailMessage
import mailbox
from pathlib import Path
import shutil

out = Path("testdata/import")
when = (2024, 1, 2, 3, 4, 5, 1, 2, 0)  # struct_time for "From " lines


def message(number, subject, body, attachment=None):
    m = EmailMessage()
    m["From"] = "Alice Ångström <alice@customer.test>"
    m["To"] = "app@example.test"
    m["Subject"] = subject
    m["Message-ID"] = f"<python-{number}@example.test>"
    m["Date"] = "Tue, 02 Jan 2024 03:04:05 +0000"
    m.set_content(body)
    if attachment:
        m.add_attachment(attachment, maintype="application", subtype="octet-stream", filename="report.bin")
        m.set_boundary(f"fixture-{number}")
    return m


shutil.rmtree(out, ignore_errors=True)
out.mkdir(parents=True)

box = mailbox.mbox(out / "python.mbox")
for number, subject, body, flags, attachment in [
    (1, "Read", "Plain text\n", "RO", None),
    # mbox writers quote body lines that start with "From ".
    (2, "Quoted", "From the start\n>From already quoted\nend\n", "", None),
    (3, "Flagged reply", "Answered and flagged\n", "RFA", None),
    (4, "Grüße", "Non-ASCII subject and attachment\n", "R", bytes(range(256))),
]:
    m = mailbox.mboxMessage(message(number, subject, body, attachment))
    m.set_from("alice@customer.test", when)
    m.set_flags(flags)
    box.add(m)
box.close()

# Maildir file names normally include time, PID and hostname; use fixed ones.
root = mailbox.Maildir(out / "python-maildir", create=True)
work = root.add_folder("Work.Projects")
for folder, name, subdir, flags, number, subject in [
    (root, "1704164645.M1P1.fixture", "cur", "SF", 10, "Seen and flagged"),
    (root, "1704164646.M2P1.fixture", "new", "", 11, "Unread"),
    (work, "1704164647.M3P1.fixture", "cur", "RS", 12, "Replied in a subfolder"),
    (work, "1704164648.M4P1.fixture", "cur", "T", 13, "Trashed"),
]:
    m = mailbox.MaildirMessage(message(number, subject, f"Maildir message {number}\n"))
    m.set_subdir(subdir)
    m.set_flags(flags)
    key = folder.add(m)
    current = Path(folder._path) / folder._lookup(key)
    suffix = ":2," + flags if subdir == "cur" else ""
    current.rename(current.with_name(name + suffix))
for tmp in out.glob("python-maildir/**/tmp"):
    tmp.rmdir()
