# Design choices

Mailarky is a test double for applications that send and read email. Its core
is account isolation, shared SMTP/IMAP state, and controllable protocol faults.

## Ownership and delivery

An account represents the application's mail identity. SMTP AUTH and IMAP
LOGIN use the same credentials. Authenticated submissions are captured in that
account even when recipients are arbitrary customer addresses. This lets
parallel tests reuse the same addresses and Message-IDs.

Anonymous SMTP has no submitting identity. It routes registered recipient
addresses to accounts, with unclaimed addresses going to `default`. One
transaction can create one copy in each target account; repeated addresses
within an account do not create duplicate copies. Deleted accounts' registered
addresses remain retired until reassigned, preventing accidental fallback.
Retired addresses have no expiry, so provisioning and deleting accounts with
new recipient addresses grows this routing metadata over the instance's lifetime.

SMTP provides one DATA result, so a recipient account's content rejection
rejects the transaction before copies are stored. Account rules are evaluated
separately. After-phase faults model the uncertain outcome of losing a reply
following successful storage. Separate bbolt databases do not provide an atomic
commit across accounts if persistence itself fails partway through fan-out.
Earlier copies remain stored when a later account fails and SMTP returns 451;
retrying the transaction can duplicate those earlier deliveries.

## State and responsibilities

`Service` owns global configuration, account routing and provisioning, HTTP
credentials, and the server fault registry. `Account` owns credentials, its
fault registry, notification workers, and protocol-facing identity. `Store`
owns raw messages, folders, UIDs, flags, and persistence. Accounts do not carry
copies of listener or authentication configuration.

Store mutations copy state, validate the proposed change, and commit bbolt
before publishing the new snapshot. Readers see consistent snapshots. Raw MIME
is immutable. Parsed text and metadata are derived caches; attachment payloads
are decoded on demand. SMTP parses once before fault matching and reuses that
representation for each delivery.

Retention is opt-in. A count or age limit applies separately to each account
and spans its folders, so explicitly configured retention can remove seeded
fixtures. With default settings, mail remains until the test removes it.

## IMAP sequence numbers and notifications

A selected connection keeps a view of message identities. Changes from HTTP,
retention, or another IMAP session do not silently renumber that view. At safe
command boundaries and during IDLE, the connection emits EXPUNGE, EXISTS, and
flag updates and advances its view. Non-UID FETCH, STORE, and SEARCH defer
EXPUNGE as required by IMAP. Operations resolve the client's sequence numbers
against that view and mutate live records by immutable ID.

Updates and fault delays run without holding the store lock. A slow client or
fault in one connection does not block a different test's mutations. Removing
an account, deleting a selected folder, or resetting its UIDVALIDITY disconnects
affected selected clients when they next poll or issue a command.

HTTP inspection is observational. IMAP BODY fetches set `\Seen`; BODY.PEEK,
metadata-only fetches, and EXAMINE preserve it. Explicit HTTP PATCH and IMAP
STORE are available when a test intends to change flags.

## Faults and dependency tradeoffs

A fault is a trigger plus filters and an action. Registry location determines
server or account scope; a message filter narrows either scope. `before`,
`content`, and `after` identify protocol boundaries without a separate message
stage vocabulary. Typed action decoding rejects unrelated options. Registries
claim hits atomically, then execute actions after releasing their lock.

Public forks of go-smtp and go-imap expose protocol hooks. Their own module
paths allow normal Go dependency resolution without `replace` directives,
which [versioned go install disallows](https://go.dev/ref/mod#go-install).
The IMAP backend implements go-imap v2's session API. Each FETCH response is
written and closed synchronously; completion faults run after those writers
finish. V2 handles IDLE's continuation and DONE parsing, while Mailarky supplies
account updates through its session API.

The [v2 fork](https://github.com/johlo/go-imap/tree/imap-v2-protocol-hooks) adds optional
command, response, greeting, and capability hooks. Its module path is
`github.com/johlo/go-imap/v2`. Upstream v2 remains in development, so exact
revision pins and independent socket tests still matter. Mailarky advertises
IMAP4rev1 and only the extensions its backend implements.

TCP latency, bandwidth, and connection-level failures belong in Toxiproxy.
Mailarky's hooks target SMTP/IMAP semantics while HTTP remains available for
assertions and cleanup.
