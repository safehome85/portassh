# Contributing

Thanks for your interest in the project!

## How to propose a change

1. Open an issue describing the scenario (templates: "Bug report" /
   "Feature request") — let's agree on the approach before writing code
2. Fork the repository, create a branch from `main`
3. Make your changes following the rules below
4. Open a PR using the template

## Rules

- `gofmt -w .` and `go vet ./...` are mandatory, `go test ./...` must be green
- Keys, IPs and machine names must never appear in commits: `baked.go`, logs
  and working documents are gitignored — do not disable .gitignore
- Comments and messages: English or Russian, but do not mix languages within
  a single sentence
- Behavioural changes go into CHANGELOG.md (Keep a Changelog format)

## Testing

Interactive scenarios are covered by e2e tests (`TestEndToEndShell` — a real
PTY through gliderlabs). Changes to exec/pty/tunnel coverage will likely get
the question "how was this verified on a live tunnel".

## Local build

```sh
./scripts/gen-baked.sh relay.example.com   # keys + config
./build.sh                                 # build (requires a filled baked.go)
```
