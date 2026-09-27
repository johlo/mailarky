# Troubleshoot

Run `docker compose ps -a` and `docker compose logs imap-emulator` from the
checkout with the same project and port variables used at startup. For a
direct Go run, read the terminal output.

| Symptom | Check and resolution |
| --- | --- |
| Compose reports a port is already allocated | Choose unused host ports as described in [run and test](run-and-test.md#choose-host-ports). Separate Compose projects still need distinct published ports. |
| The container exits with a certificate file error | Verify the configured paths and file permissions. The Docker process runs as UID/GID 65532. For source runs, `make run` sets the bundled certificate paths. |
| TLS reports an unknown authority | Add this checkout's `testdata/tls/server.crt` to the client's trusted roots. A regenerated certificate must also be updated in the client. |
| TLS reports a hostname mismatch | Connect using `localhost`, `127.0.0.1`, or `imap-emulator`, or use a custom certificate covering your chosen name. |
| The client hangs or disconnects during connection setup | Use implicit TLS on the IMAP port. Port 8026 is plain HTTP for fixtures; it is not an IMAP endpoint. |
| A container cannot connect to `localhost:1993` | Inside that container, `localhost` identifies the container itself. Use the Compose service name `imap-emulator` and its internal port 1993. |
| Authentication fails | Use exactly `clinic@example.test` and `local-imap-only`. There is no configurable account or password setting. |
| `POST /messages` returns 400 | Read the plain-text error body. Check the exact folder name, addresses, RFC 3339 dates, recipient list, supported fields, and JSON size. See [HTTP errors](../reference/http-api.md#errors). |
| The API returns 201 but the application shows nothing | Confirm the folder your application polls, its sync schedule, and any address or date filters. The emulator does not trigger application imports. |
| A message appears multiple times | Each POST appends a message, including repeated requests with the same Message-ID. Application deduplication is a separate concern. |
| Mail disappeared after restart | Storage is in memory. Reseed the fixtures, reconnect, and handle the new UIDVALIDITY in your importer. |
| Changing flags, copying, or deleting mail fails | These operations are unsupported. Set flags at creation and seed another copy through HTTP. Reset an isolated instance by restarting it. |

To inspect a 400 response without curl suppressing its body, use `curl -i`
with your existing fixture request. For a TLS connection example that checks
the certificate, use the [tutorial client](../tutorials/first-mailbox.md#3-read-the-message-over-tls).

[Documentation index](../README.md)
