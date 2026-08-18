# Changelog

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
