# Contributing

Bug reports, questions, and pull requests are welcome. For larger changes, such
as new fault actions or IMAP extensions, open an issue first so the design can
be discussed before you spend time on it.

Report security problems privately as described in [SECURITY.md](SECURITY.md).

## Build and test

You need the Go version in [`go.mod`](go.mod). The smoke test also needs
Docker Compose and Python 3.

```sh
make test                         # race-enabled Go tests, then go vet
go build -o mailarky ./cmd/mailarky
make run                          # run locally with the bundled test certificate
docker compose up -d --build --wait
python3 scripts/smoke.py          # independent SMTP/IMAP clients against Docker
```

Go tests use real SMTP and IMAP sockets. Add a socket-level test for protocol
behavior changes, and a fault test for new fault triggers or actions.

## Pull requests

- Keep each change focused, and describe the behavior it changes.
- Update the documentation in `docs/`, and `openapi.yaml` for HTTP API changes.
- Run `make test` and the Docker smoke test before submitting.
- Run `make diagrams` after editing `docs/architecture/*.reladraw`.

## Protocol forks

SMTP and IMAP protocol hooks live in the public
[go-smtp](https://github.com/johlo/go-smtp/tree/smtp-protocol-hooks) and
[go-imap](https://github.com/johlo/go-imap/tree/imap-v2-protocol-hooks) forks.
Changes to the hooks go to those repositories first. Then update the pin:

```sh
go get github.com/johlo/go-imap/v2@COMMIT   # or github.com/johlo/go-smtp@COMMIT
go mod tidy
make test
```

## Releases

Maintainers publish releases by pushing a version tag. See
[publish a release](docs/how-to/publish-release.md).

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
