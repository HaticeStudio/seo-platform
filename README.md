# seo-platform

Standalone-first SEO integration platform: connect Google Search Console,
Bing Webmaster, and GA4 to any website, sync search/index/acquisition data on
a schedule, and inspect it through a versioned API and React console.

- **Standalone-first** — a single-site deployment needs only Docker Compose,
  a public URL, and provider credentials. No tenant concept, no external IAM.
- **No hidden platform dependencies** — the core knows no specific customer,
  domain, identity product, or deployment platform.
- **Honest capabilities** — providers declare what they actually support;
  missing data is reported as missing, never faked with zeros.
- **Secrets stay out of reach** — credential material lives only in a
  pluggable secret store; business tables, API responses, and logs carry an
  opaque reference at most.

Design and security model: see [`docs/architecture.md`](docs/architecture.md).

## Quick Start (single site)

```bash
# 1. Start the service. Encryption and bootstrap credentials are generated
#    into local data files with owner-only permissions on first start.
SEO_PUBLIC_URL=https://www.example.com go run ./cmd/seo-platform

# 2. Print the bootstrap API key once, then delete its one-time file.
go run ./cmd/seo-platform admin bootstrap
```

Or with Docker Compose: see [`deploy/docker-compose.yml`](deploy/docker-compose.yml).

```bash
API_KEY=$(docker compose -f deploy/docker-compose.yml exec -T seo-platform seo-platform admin bootstrap)
curl -H "Authorization: Bearer $API_KEY" http://127.0.0.1:8080/api/v0/providers
```

### Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `SEO_PUBLIC_URL` | (required) | The site's public base URL |
| `SEO_BASE_URL` | `SEO_PUBLIC_URL` | Public seo-platform Console/API origin used for OAuth callbacks |
| `SEO_SITEMAP_URL` | `<public_url>/sitemap.xml` | Sitemap location |
| `SEO_TIMEZONE` | `UTC` | Site reporting timezone |
| `SEO_LISTEN` | `127.0.0.1:8080` | Bind address |
| `SEO_DB_PATH` | `data/seo.db` | SQLite database path |
| `SEO_SECRETS_MASTER_KEY` | auto-generated | Optional 64-hex-char key override for the encrypted local secret store |
| `SEO_SECRETS_KEY_FILE` | `data/keys/master-key` | Owner-only generated key file when the env override is unset |
| `SEO_SECRETS_DIR` | `data/secrets` | Encrypted secret files directory |
| `SEO_API_KEYS` | auto-generated | Optional `sha256hex=scope1,scope2;...` override — hashes only, never plaintext |
| `SEO_API_KEY_HASH_FILE` | `data/auth/api-key-hash` | Owner-only hash used by automatic bootstrap auth |
| `SEO_BOOTSTRAP_TOKEN_FILE` | `data/bootstrap/admin-api-key` | One-time plaintext bootstrap token; removed after `admin bootstrap` |
| `SEO_DEV_AUTH` | `false` | Loopback-only no-credential auth for local development |
| `SEO_GA4_CONVERSION_EVENTS` | (unset) | Comma-separated GA4 event names this deployment counts as conversions |
| `SEO_CONSOLE_DIR` | (container: `/app/console`) | Serve the built React console from this directory |
| `SEO_GOOGLE_OAUTH_CLIENT_ID` | (unset) | Google OAuth client for interactive authorization |
| `SEO_GOOGLE_OAUTH_CLIENT_SECRET` | (unset) | Its secret; used server-side only, never sent to the browser |
| `SEO_SYNC_LOOKBACK_DAYS` | `30` | Default sync range |
| `SEO_SYNC_TIMEOUT` | `10m` | Per-run timeout |
| `SEO_SYNC_INTERVAL` | `24h` | Scheduler interval |

Auth is mandatory. On a fresh installation the server generates one bootstrap
key; retrieve it once with `seo-platform admin bootstrap`. Explicit
`SEO_API_KEYS` disables this bootstrap path. Development auth is opt-in and
refuses non-loopback binds.

## Public integration surfaces

- HTTP contract: [`api/openapi.yaml`](api/openapi.yaml)
- Go client: `github.com/HaticeStudio/seo-platform/sdk/go/seo`
- React package: `@haticestudio/seo-console` (build with `npm run build:lib`)
- Embedded-console example: [`examples/embedded-console`](examples/embedded-console/README.md)

## Repository layout

```
cmd/seo-platform/   standalone server entry point
core/               public contracts: Provider, SecretStore, resolvers, models
providers/          provider implementations (Search Console, Bing, GA4)
providertest/       public contract-test kit + fake provider
sdk/go/seo/         public Go client for the versioned API
console/            React console: standalone app + embeddable package
api/                machine-readable HTTP API contract
internal/           server runtime (not public Go API)
migrations/         forward-only SQL migrations
deploy/             Docker Compose example
```

## Status

`v0.x` — Google Search Console, Bing Webmaster, GA4, the standalone Console,
HTTP API, and Go SDK are available. APIs may still change between minor
versions; every breaking change ships with a migration note (see
[VERSIONING.md](VERSIONING.md)).

## License

[Apache-2.0](LICENSE). Security reports: see [SECURITY.md](SECURITY.md).
