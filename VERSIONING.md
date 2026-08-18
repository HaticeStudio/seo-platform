# Versioning Policy

## Scheme

- `v0.x` (current): APIs may change between minor versions, but every
  breaking change must be listed in the release notes with a migration note.
  Silent breakage is a bug.
- `v1.0` onward: Semantic Versioning. Breaking changes to the HTTP API, Go
  packages under `core/` and `providertest/`, provider capability semantics,
  or migration behavior require a major version.

## What is public API

- The HTTP API under `/api/v0` (later `/api/v1`).
- Go packages `core/`, `providertest/`, and `sdk/go/seo`. Everything under
  `internal/` is explicitly not public API.
- The React package exported from `console/src/index.ts`.
- Provider capability names (`search.performance`, `index.sitemaps`, …).
  New capabilities may be added at any time; a published name is never
  re-interpreted to mean something else.
- Migration files: forward-only and transactionally recorded exactly once.
  Released migrations are never edited; fixes ship as new migrations.

## Compatibility rules

- Additive JSON fields are not breaking; removals and semantic changes are.
- Database migrations never run destructive changes automatically at startup.
- Release notes must list API, migration, and provider behavior changes plus
  rollback caveats.
- Rollback: deploy the previous compatible image. Down-migrations that would
  delete synced provider data are not provided; forward-fix instead.
