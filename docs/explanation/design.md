# Shared storage and test isolation

SMTP, IMAP and HTTP are interfaces to one store. SMTP DATA parses the MIME,
checks matching toxics and commits a message to the configured folder. HTTP
fixtures, raw imports, JSON sending and IMAP APPEND use the same append path.
HTTP and IMAP therefore observe one identity, one raw message and one flag/tag
state. Incoming historical fixtures and outgoing app deliveries coexist.

Raw MIME is preserved. Parsed headers, decoded bodies and attachment metadata
are derived for HTTP inspection and reconstructed from raw MIME on database
startup. HTTP IDs are UUIDs; IMAP UIDs are monotonically allocated per folder;
Message-ID is user-supplied message metadata. These identities serve different
purposes and must not be substituted for one another.

Writes build a new state under a mutex. With persistence enabled, a bbolt
transaction commits changes before the in-memory state becomes visible.
Readers take snapshots and perform protocol work after releasing the mutex.
Deletion and retention preserve remaining UIDs. One process owns a database;
this is a test mail service, not a clustered mail server.

A toxic combines a name, enabled state, exact conjunctive selector, type and
attributes. Selecting matching toxics, applying probability and incrementing
hit counts happens atomically under a short registry lock. Waiting and protocol
responses happen after releasing that lock. A slow selected message therefore
cannot block another test's independent connection through a shared lock.

Isolation still requires good ownership: a test must select its own UUID or
unique address. The service cannot distinguish two tests that deliberately use
the same selector. No global failure mode is offered. A shared SMTP transaction
or IMAP command is an indivisible protocol operation; avoid combining unrelated
tests' messages in one command when testing delays/rejections.

IMAP toxics change a returned snapshot, never the persisted MIME. HTTP remains
available for inspection and cleanup while a toxic is active. Searches exclude
noncandidate messages before applying faults. SMTP pre-DATA faults only know
envelope addresses; the API rejects selectors requiring unavailable headers.

WebSocket updates and webhooks are best-effort notifications. Slow WebSocket
clients are disconnected when their bounded queue fills. Webhooks use a bounded
queue, configurable timing and three attempts; they are not a durable event log.
Polling the HTTP API remains the authoritative way to assert stored state.

Relay and forwarding are opt-in outbound operations, separate from capture.
Delivery failures can be logged or propagated as a temporary SMTP failure;
when propagated, the captured message remains available to diagnose the failure.
Retries can therefore create duplicates unless duplicate suppression is enabled.

The API's common Mailpit contracts are reimplemented independently. There is no
Mailpit runtime/source dependency. HTML compatibility uses MIT-licensed Can I
Email data with heuristic feature detection and equal client/version weighting;
its scores are advisory and are not a rendering-engine result or an assertion
of exact Mailpit scoring equivalence.
