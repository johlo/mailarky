.PHONY: test run docker diagrams

test:
	go test -race ./...
	go vet ./...

run:
	MAIL_SANDBOX_IMAP_CERT=testdata/tls/server.crt \
	MAIL_SANDBOX_IMAP_KEY=testdata/tls/server.key go run ./cmd/mail-sandbox

docker:
	docker compose up -d --build

# Render the C4 architecture diagrams from their reladraw sources, in a light
# and a dark variant. Requires Node.js.
RELADRAW ?= npx --yes reladraw@0.9.0
DIAGRAMS := context containers components

diagrams:
	for d in $(DIAGRAMS); do \
		$(RELADRAW) docs/architecture/$$d.reladraw -o docs/architecture/$$d.svg --theme light && \
		$(RELADRAW) docs/architecture/$$d.reladraw -o docs/architecture/$$d-dark.svg --theme dark || exit 1; \
	done
