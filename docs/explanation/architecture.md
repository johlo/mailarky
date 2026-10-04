# Architecture

Mailarky runs SMTP, TLS IMAP, and HTTP in one Go process. Each account owns a
separate Store and fault registry. An optional Roundcube container uses the
same mail protocols as the application under test.

## System context

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="../architecture/context-dark.svg">
  <img alt="Tests control Mailarky over HTTP; applications use SMTP and IMAP" src="../architecture/context.svg">
</picture>

The test harness controls accounts, fixtures, and failures over HTTP. The
application sends through SMTP and reads through IMAP. Mailarky captures mail
locally; it has no relay or forwarding path. Optional webhook receivers consume
notifications. Toxiproxy may sit in front of either mail listener for TCP faults.

## Containers

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="../architecture/containers-dark.svg">
  <img alt="Mailarky protocol listeners, optional bbolt storage and Roundcube" src="../architecture/containers.svg">
</picture>

The Go process defaults to memory storage. With persistence, each account uses
its own bbolt file. Roundcube is an optional Compose profile with verified TLS
IMAP and STARTTLS SMTP. It authenticates both using the logged-in account.

## Components

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="../architecture/components-dark.svg">
  <img alt="Service owns routing and configuration; accounts own faults and notifications; Store owns persisted state" src="../architecture/components.svg">
</picture>

`Service` owns configuration, the account catalog, and server faults.
`Account` owns credentials, account faults, notification workers, and its
`Store`. `Store` owns immutable raw MIME, copy-on-write state, folder identity,
and persistence. Protocol adapters use these objects without copying global
configuration into each account.

The SMTP adapter selects an account by AUTH first, otherwise by recipient.
Anonymous fan-out creates one copy per account. The IMAP adapter keeps selected
views so concurrent changes cannot silently shift sequence numbers. The HTTP
API has one route tree and applies shared control authentication once.

See [design choices](design.md) for the consistency and dependency tradeoffs.
Diagram sources are in `docs/architecture/`; regenerate both themes with
`make diagrams`.
