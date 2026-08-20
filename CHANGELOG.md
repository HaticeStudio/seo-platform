# Changelog

## v0.3.1 — 2026-08-21

- Added the public, create-only `Runtime.ImportConnection` API so embedding
  hosts can move existing provider credentials into the configured SecretStore
  without a browser or a second platform service.
- Existing configured connections are never overwritten by host startup
  reconciliation; imports validate provider access and return discovered
  properties for final selection in the Console.

Migration notes: no database or HTTP migration. The Go API addition is
backward-compatible. Rollback to v0.3.0 keeps already imported connections and
encrypted credentials usable.

## v0.3.0 — 2026-08-19

- Corrected the integration model from standalone-first to embed-first.
- Added the public `platform` Go runtime so a host can mount SEO APIs in its
  existing process and reuse its session/RBAC without a platform API key.
- Made the React Console use same-origin host authentication by default while
  retaining optional short-lived bearer-token support.
- Added exact host-path OAuth callbacks and public secret-store helpers.
- Kept the standalone binary and container as optional examples.

## v0.2.0 — 2026-08-19

- Added a responsive English and Traditional Chinese guided setup Console with
  copyable site values, semantic official-console links, one-time credential
  entry, property selection, live testing, sync state, and actionable errors.
- Bound the post-OAuth navigation path to single-use, subject/site/provider
  scoped PKCE state and exposed return-aware helpers in the React and Go SDKs.
- Added a distinct reauthorization state for expired, revoked, or unauthorized
  Google OAuth credentials without exposing upstream response bodies.
- Expanded provider setup-link metadata while preserving existing fields and
  manual service-account/API-key flows.

Migration notes: startup applies the additive `0004_oauth_return_to.sql`
migration to existing SQLite databases. HTTP and SDK changes are additive.
Rollback to v0.1.1 leaves the unused column in place and does not delete data.

## v0.1.1 — 2026-08-18

- Added cursor pagination to normalized report rows so host applications can
  migrate complete datasets instead of silently stopping at the first page.
- Resume the last partial provider checkpoint on the next sync run, allowing
  batched URL Inspection jobs to advance through the complete sitemap.

Migration notes: existing clients remain compatible; integrations that need
more than one page should continue with `next_cursor` until it is empty.

## v0.1.0 — 2026-08-18

Initial public release.

- Standalone single-site server with generated owner-only encryption and
  one-time bootstrap credentials.
- Google Search Console search performance, sitemap, and batched URL
  inspection providers with OAuth or service-account authentication.
- Bing Webmaster search performance, crawl, and sitemap provider.
- GA4 acquisition and configurable conversion-event provider.
- Versioned HTTP API, Go SDK, standalone and embeddable React Console.
- Site-scoped connection, OAuth state, sync-run, report, and secret storage.
- Docker Compose Quick Start, release archives, checksums, multi-architecture
  container provenance/SBOM, secret scanning, dependency updates, race tests,
  vulnerability checks, API lint, and standalone smoke tests.

Migration notes: none; this is the first public version. Publishing this tag
does not deploy or mutate downstream installations.
