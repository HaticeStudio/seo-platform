# Contributing

Thanks for considering a contribution!

## Ground rules

- **Host-neutral core.** Nothing under `core/`, `internal/`, or
  `providers/` may reference a specific customer, domain, IAM product, or
  deployment platform. Host-specific glue belongs in the host's repository,
  wired through the public extension points (`SiteResolver`,
  `WorkspaceResolver`, `ProjectAdapter`, `SecretStore`).
- **No secrets anywhere.** No credentials in code, tests, fixtures, or Git
  history. Business tables and API responses carry opaque refs only.
- **Honest data.** Providers report missing capabilities as `unsupported`
  and missing data as missing — never as fabricated zeros.

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

Provider implementations must pass the contract kit:

```go
func TestContract(t *testing.T) { providertest.RunContract(t, New(...)) }
```

## Pull requests

- Keep changes focused; describe behavior changes and migration impact.
- Add or update tests for anything user-visible.
- Breaking changes need a migration note in the PR description
  (see VERSIONING.md).
