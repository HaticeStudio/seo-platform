# seo-platform

Standalone-first SEO integration platform: connect Google Search Console,
Bing Webmaster, and GA4 to any website, sync search/index/acquisition data on
a schedule, and inspect it through a versioned API and (soon) a React console.

- **Standalone-first** — a single-site deployment needs only Docker Compose,
  a public URL, and provider credentials. No tenant concept, no external IAM.
- **No hidden platform dependencies** — the core knows no specific customer,
  domain, identity product, or deployment platform.
- **Honest capabilities** — providers declare what they actually support;
  missing data is reported as missing, never faked with zeros.
- **Secrets stay out of reach** — credential material lives only in a
  pluggable secret store; business tables, API responses, and logs carry an
  opaque reference at most.

Design and security model: see [`docs/adr`](docs/) and the parent design ADR
(“public SEO platform boundary”).

## Quick Start (single site)

```bash
# 1. Generate keys
SECRETS_KEY=$(openssl rand -hex 32)
API_KEY=$(openssl rand -hex 24)
API_KEY_HASH=$(printf '%s' "$API_KEY" | shasum -a 256 | cut -d' ' -f1)

# 2. Run
SEO_PUBLIC_URL=https://www.example.com \
SEO_SECRETS_MASTER_KEY=$SECRETS_KEY \
SEO_API_KEYS="$API_KEY_HASH=read,sync,connections.manage" \
go run ./cmd/seo-platform
```

Or with Docker Compose: see [`deploy/docker-compose.yml`](deploy/docker-compose.yml).

```bash
curl -H "Authorization: Bearer $API_KEY" http://127.0.0.1:8080/api/v0/providers
```

### Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `SEO_PUBLIC_URL` | (required) | The site's public base URL |
| `SEO_SITEMAP_URL` | `<public_url>/sitemap.xml` | Sitemap location |
| `SEO_TIMEZONE` | `UTC` | Site reporting timezone |
| `SEO_LISTEN` | `127.0.0.1:8080` | Bind address |
| `SEO_DB_PATH` | `data/seo.db` | SQLite database path |
| `SEO_SECRETS_MASTER_KEY` | (unset) | 64-hex-char key for the encrypted local secret store |
| `SEO_SECRETS_DIR` | `data/secrets` | Encrypted secret files directory |
| `SEO_API_KEYS` | (unset) | `sha256hex=scope1,scope2;...` — hashes only, never plaintext |
| `SEO_DEV_AUTH` | `false` | Loopback-only no-credential auth for local development |
| `SEO_GA4_CONVERSION_EVENTS` | (unset) | Comma-separated GA4 event names this deployment counts as conversions |
| `SEO_CONSOLE_DIR` | (container: `/app/console`) | Serve the built React console from this directory |
| `SEO_GOOGLE_OAUTH_CLIENT_ID` | (unset) | Google OAuth client for interactive authorization |
| `SEO_GOOGLE_OAUTH_CLIENT_SECRET` | (unset) | Its secret; used server-side only, never sent to the browser |
| `SEO_SYNC_LOOKBACK_DAYS` | `30` | Default sync range |
| `SEO_SYNC_TIMEOUT` | `10m` | Per-run timeout |
| `SEO_SYNC_INTERVAL` | `24h` | Scheduler interval |

Auth is mandatory: the server refuses to start without `SEO_API_KEYS` unless
`SEO_DEV_AUTH=true`, and development auth refuses non-loopback binds.

## Repository layout

```
cmd/seo-platform/   standalone server entry point
core/               public contracts: Provider, SecretStore, resolvers, models
providers/          provider implementations (Search Console, Bing, GA4)
providertest/       public contract-test kit + fake provider
console/            React console: standalone app + embeddable package
internal/           server runtime (not public Go API)
migrations/         forward-only SQL migrations
deploy/             Docker Compose example
```

## Status

`v0.x` — APIs may still change between minor versions; every breaking change
ships with a migration note (see [VERSIONING.md](VERSIONING.md)). Provider
packages for Google Search Console, Bing Webmaster, and GA4 are landing next.

## License

[Apache-2.0](LICENSE). Security reports: see [SECURITY.md](SECURITY.md).
