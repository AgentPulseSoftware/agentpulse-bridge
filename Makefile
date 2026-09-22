BINARY := agentpulse
PKG := ./...

.PHONY: build build-dev test lint fixtures

# build produces the release binary: no "record" command, since
# fixture recording writes raw, unscrubbed session data to disk.
build:
	go build -o bin/$(BINARY) ./cmd/agentpulse

# build-dev produces a development binary with the extra "record"
# subcommand used to capture fixture recordings. Never ship this binary.
build-dev:
	go build -tags dev -o bin/$(BINARY)-dev ./cmd/agentpulse

# test runs the suite twice: once as the release build sees it, once with
# -tags dev so the "record" command's own code is exercised too. -timeout
# bounds a single run's wall clock (the workflow rule every "go test"
# invocation in this repo follows) rather than relying on the default.
test:
	go test -timeout 120s $(PKG)
	go test -timeout 120s -tags dev $(PKG)

lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "The following files are not gofmt-formatted:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet $(PKG)
	go vet -tags dev $(PKG)
	golangci-lint run
	golangci-lint run --build-tags dev

# fixtures replays every scenario under testdata/fixtures/ through the real
# release binary (SPEC 9.4) and compares the resulting spool to
# each scenario's expected.json. Depends on "build" so it always checks the
# binary as it would actually ship, not a stale one.
fixtures: build
	go run ./tools/fixturecheck -bin bin/$(BINARY)
