.PHONY: test run docker

test:
	go test -race ./...
	go vet ./...

run:
	IMAP_EMULATOR_CERT=testdata/tls/server.crt \
	IMAP_EMULATOR_KEY=testdata/tls/server.key go run .

docker:
	docker compose up -d --build
