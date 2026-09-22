# Contributing

Thanks for taking a look. This is a small, focused program; the bar is
"idiomatic Go that a reviewer can check quickly".

## Build and test

The Go version is pinned in `go.mod`; nothing else is required.

```
make build      # bin/agentpulse, built the same way a release binary is
make test       # the suite twice: as released, and with -tags dev
make lint       # gofmt, go vet, golangci-lint — each plain and with -tags dev
make fixtures   # replays the scenario corpus through the built binary
```

`make build`'s binary is not byte-for-byte what a tagged release ships: the
release build additionally sets `CGO_ENABLED=0`, passes `-trimpath` and
`-ldflags "-s -w"`, and stamps in a version string. What stays true either
way, and is the actual guarantee: no dev tag, and no `record` command.

Schema-validation tests in `internal/classify` are skipped unless
`AGENTPULSE_EVENT_SCHEMA` points at a local copy of the `event.v1` JSON
Schema (`AGENTPULSE_EVENT_SCHEMA=/path/to/event.v1.json go test
./internal/classify`); without it, those assertions are skipped and the
rest of the suite still runs.

## What a pull request needs

- `gofmt`-clean, and passing `go vet` and `golangci-lint run` both plain and
  with `--build-tags dev`. CI runs all of it on Linux.
- Tests. Table-driven where there is more than one case.
- No new dependency unless it genuinely earns its place; say why in the
  description.
- The `hook` subcommand must keep exiting `0` on every path and must stay
  silent unless `AGENTPULSE_DEBUG=1` is set. A hook that prints or fails
  interrupts someone's coding agent.

## The event schema is closed

The set of fields the bridge sends (`event.v1`) is fixed and additive-only,
and no field may carry a file path, command text, tool output, or prompt
text. A pull request that adds a field to the event schema will be declined
without an approved design record behind it. This is the project's central
promise to its users, so please raise the idea in an issue first.

## SPEC section and requirement-ID references

Some comments and tests in this codebase cite identifiers like `SPEC 7.2`
or `BR-17`. These are stable requirement identifiers from AgentPulse's
internal specification, retained because the code and its tests are
written against them. A contributor does not need that document to work
on this repository; the identifiers are provenance, not a dependency.

## Scope

Issues and pull requests here are about the `agentpulse` CLI, its release
artifacts, and its install script. Questions about the AgentPulse iPhone app
or the hosted relay belong in AgentPulse support channels, not in this
repository's tracker.

## Licence

Contributions are accepted under the Apache License 2.0 — the same licence
the project ships under (inbound equals outbound). There is no CLA. By
opening a pull request you agree your contribution is licensed that way.
