# Security

## Intended use

Mailarky is a test double for local development and CI. It is not a mail
server for real users, and its defaults favor convenience over protection:

- The HTTP control API is unauthenticated unless `MAILARKY_HTTP_AUTH` or
  `MAILARKY_HTTP_AUTH_FILE` is set. Anyone who can reach it can read every
  account's mail, create accounts, and inject faults.
- The default account password, `local-imap-only`, is public.
- The certificate and private key in `testdata/tls` and the Docker image are
  public fixtures. They provide no confidentiality.
- The standalone binary listens on all interfaces by default. The Docker
  examples and Compose file publish ports on loopback only.
- `GET /messages/{id}/view` serves captured HTML under a restrictive
  `Content-Security-Policy: sandbox` header, on the same origin as the API.

Bind listeners to `127.0.0.1` or an isolated CI network, and do not expose
Mailarky to untrusted networks. If it must be reachable more widely, set HTTP
authentication and supply your own certificate.

## Supported versions

Security fixes go into the latest release only.

## Reporting a vulnerability

Report vulnerabilities privately through
[GitHub security advisories](https://github.com/johlo/mailarky/security/advisories/new).
Do not open a public issue. Include the Mailarky version (`mailarky --version`),
your configuration, and steps to reproduce.

The documented behavior above, such as an unauthenticated API in the default
configuration, is not a vulnerability. Bypassing configured HTTP
authentication, escaping the HTML view sandbox, or reading another account's
mail over SMTP or IMAP is.
