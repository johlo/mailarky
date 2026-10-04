# Local TLS certificate

Public test fixtures, also bundled in the Docker image. The certificate covers
`mailarky`, `localhost` and `127.0.0.1`. Clients should trust `server.crt`.

To regenerate the pair when it expires, run this from the repository root,
then update the certificate your clients trust:

```sh
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 3650 \
  -keyout testdata/tls/server.key -out testdata/tls/server.crt \
  -subj '/CN=Mailarky test server' \
  -addext 'subjectAltName=DNS:mailarky,DNS:localhost,IP:127.0.0.1' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'extendedKeyUsage=serverAuth'
```

To use your own certificate, see [configuration](../../docs/reference/configuration.md).
