.PHONY: test run docker

test:
	go test -race ./...
	go vet ./...

run:
	MAIL_SANDBOX_IMAP_CERT=testdata/tls/server.crt \
	MAIL_SANDBOX_IMAP_KEY=testdata/tls/server.key go run ./cmd/mail-sandbox

docker:
	docker compose up -d --build
