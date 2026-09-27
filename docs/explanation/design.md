# Design

## One store per account

```
SMTP ──┐
HTTP ──┼──▶ account store (raw MIME + flags + tags) ──▶ IMAP / HTTP reads
IMAP ──┘      APPEND
```

Every way of adding mail goes through the same append path into the account's
store. HTTP and IMAP therefore see the same message, the same raw bytes and the
same flags. Nothing is copied or synced between protocols.

- **Raw MIME is the source of truth.** Parsed headers, bodies and attachments
  are derived from it for the HTTP API.
- **Three identifiers, three purposes:** the HTTP `ID` (a UUID), the IMAP `UID`
  (increasing per folder) and the `Message-ID` header (whatever the sender set).
- **Accounts are fully separate.** A small manager holds the account list and
  which SMTP recipient belongs to which account. Everything else is per account.

## Concurrency

Writes happen under a mutex. With persistence on, the bbolt transaction commits
before the change becomes visible. Reads take a snapshot and do protocol work
after releasing the lock, so a slow IMAP client never blocks writers.

## Toxics and isolation

A toxic is chosen and counted atomically under a short lock. Its delay or error
happens **after** the lock is released. A delayed message in one test therefore
never holds up another test's connection.

The sandbox can only isolate tests that use distinct selectors. Two tests
matching the same address or header will affect each other. That's why there
is no global failure mode, and why selectors must be specific.

One SMTP transaction or IMAP command is indivisible. If it includes messages
from two tests, a fault for one affects both.

IMAP toxics change the response, never the stored message.

## What it is not

A single-process test double. There is no clustering. Webhooks and WebSocket
events are best-effort, so poll the API when a test needs a definite answer.
Captured mail is never delivered onward, so a test stack can't send real email.
