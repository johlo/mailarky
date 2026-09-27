# Local TLS certificate

These are public test fixtures. The certificate covers `mail-sandbox`,
`localhost` and `127.0.0.1`; clients trust `server.crt` explicitly. The Docker
image contains both the certificate and test private key.

Regenerate the pair from the repository root when it expires, then update the
certificate trusted by your test clients:

```sh
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 3650 \
  -keyout testdata/tls/server.key -out testdata/tls/server.crt \
  -subj '/CN=Local mail sandbox test server' \
  -addext 'subjectAltName=DNS:mail-sandbox,DNS:localhost,IP:127.0.0.1' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'extendedKeyUsage=serverAuth'
```

For custom certificates and client trust, see the
[TLS configuration reference](../../docs/reference/configuration.md#tls-and-authentication).
