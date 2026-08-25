# Developer entry points. CI runs the same commands so local green means CI
# green.
GO ?= GOTOOLCHAIN=go1.26.3 go
TEST_DB_URL ?= postgres://postgres:carshare@127.0.0.1:5434/carshare?sslmode=disable

.PHONY: build test test-sql run fmt vet db db-apply

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o ./bin/carshare ./cmd/carshare

# Unit and handler flow tests, no infrastructure needed.
test:
	$(GO) vet ./...
	$(GO) test -race ./...

# Everything, including the integration tests against a real Postgres. Start
# one with scripts/dev_db.sh first. -p 1 because the store and fleet packages
# both rebuild the same database schema, so they must not run concurrently.
test-sql:
	$(GO) vet ./...
	CARSHARE_TEST_DATABASE_URL="$(TEST_DB_URL)" $(GO) test -race -count=1 -p 1 ./...

run: build
	DATABASE_URL="$(TEST_DB_URL)" INSECURE_COOKIES=1 ./bin/carshare

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

# Show the schema diff against the dev database, then apply it.
db:
	./db/update_schema.sh

db-apply:
	./db/update_schema.sh --apply
