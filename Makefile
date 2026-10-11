GO ?= go
VERSION ?= $(shell date -u +%Y%m%d%H%M%S)
BINARY := bin/verdale-judge
CORE_CONTRACTS ?= ../Verdale-core-service/contracts

PROBLEM ?= examples/aplusb
SOURCE ?= examples/aplusb/solutions/ac.cpp
KIND ?= submission

.PHONY: check lint fmt test test-integration test-resilience build run generate contracts-sync contracts-check up down logs submit submit-all clean

## check: everything CI runs, in one command (lint, unit tests, contract check)
check: lint test contracts-check

## lint: gofmt and go vet
lint:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...
	$(GO) vet -tags integration ./integration/...

fmt:
	gofmt -w .

## test: unit tests (no Linux, Docker or Redis needed)
test:
	$(GO) test -race -count=1 ./...

## test-integration: real isolate sandbox and real Redis, in a privileged container
test-integration:
	docker compose -f docker-compose.test.yml build integration
	docker compose -f docker-compose.test.yml run --rm integration; status=$$?; \
	docker compose -f docker-compose.test.yml down -v >/dev/null 2>&1; exit $$status

## test-resilience: kill Judge mid-job and restart Redis with jobs queued (needs `make up`)
test-resilience:
	scripts/test-resilience.sh

build:
	CGO_ENABLED=0 $(GO) build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

run: build
	./$(BINARY) -config config.json

## generate: regenerate pkg/contract from the pinned contracts/judge.openapi.yaml
generate:
	$(GO) generate ./pkg/contract/...

## contracts-sync: pin the contracts from a Verdale-core-service checkout
contracts-sync:
	scripts/contracts.sh sync $(CORE_CONTRACTS)
	$(MAKE) generate

## contracts-check: vendored contracts match the lock, generated code matches the spec
contracts-check:
	scripts/contracts.sh verify
	@cp pkg/contract/types.gen.go /tmp/verdale-types.gen.go.before
	@$(GO) generate ./pkg/contract/...
	@if ! cmp -s pkg/contract/types.gen.go /tmp/verdale-types.gen.go.before; then \
		echo "pkg/contract/types.gen.go is out of date: run make generate"; exit 1; fi
	@echo "Generated contract types are up to date"

## up / down / logs: local stack (Redis, Judge, mock Core)
up:
	@test -f .env || (echo "copy .env.example to .env first" && exit 1)
	mkdir -p dev/storage/tests tmp/results
	docker compose up --build -d
	@echo "Judge: http://localhost:8081/ready  mock Core: http://localhost:8090"

down:
	docker compose down

logs:
	docker compose logs -f judge mockcore

## submit: send one solution, e.g. make submit SOURCE=examples/aplusb/solutions/tle.c KIND=verify
submit:
	@set -a; . ./.env; set +a; $(GO) run ./cmd/submit -problem $(PROBLEM) -source $(SOURCE) -kind $(KIND) -storage dev/storage/tests

## submit-all: send every example solution
submit-all:
	@for source in $(PROBLEM)/solutions/*; do $(MAKE) --no-print-directory submit SOURCE=$$source; done

clean:
	rm -rf bin tmp coverage.out
