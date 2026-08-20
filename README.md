# seo-platform

Embeddable SEO integration module: connect Google Search Console, Bing
Webmaster, and GA4 inside an existing service backend and admin UI.

- **Embed first** — import the Go runtime, mount it on an existing admin path,
  and render the React package inside the host UI. There is no second service,
  platform URL, platform API key, or data-copy job.
- **Host-owned access** — the host's existing login and RBAC map directly to
  generic SEO scopes through a small authenticator adapter.
- **No hidden platform dependencies** — the core knows no specific customer,
  domain, identity product, or deployment platform.
- **Honest capabilities** — providers declare what they actually support;
  missing data is reported as missing, never faked with zeros.
- **Secrets stay out of reach** — credential material lives only in a
  pluggable secret store; business tables, API responses, and logs carry an
  opaque reference at most.

Design and security model: see [`docs/architecture.md`](docs/architecture.md).

## Quick Start (embedded host)

```go
seo, err := platform.New(ctx, platform.Config{
    Site: core.Site{
        ID: "main",
        PublicURL: "https://www.example.com",
        SitemapURL: "https://www.example.com/sitemap.xml",
        Timezone: "Asia/Taipei",
    },
    StorePath: "data/seo.db",
    Secrets: hostSecretStore,
    Authenticator: platform.AuthenticateFunc(func(r *http.Request) (core.Subject, error) {
        return hostSEOUser(r) // Validate the existing host session/RBAC.
    }),
    Providers: []core.Provider{searchconsole.New(), bing.New(), ga4.New()},
    OAuthCallbackURL: "https://www.example.com/admin/seo/oauth/callback",
})
if err != nil { return err }
defer seo.Close()
go seo.Start(ctx)

router.Handle("/admin/seo/", http.StripPrefix("/admin/seo", seo.Handler()))
```

```tsx
// Same-origin host login: no seo-platform API key or AuthClient is needed.
<SeoConsole apiBaseUrl="/admin/seo" locale="zh-TW" />
```

Hosts upgrading from an existing integration can import a credential directly
from their server process. This path is create-only, stores the material in the
configured `SecretStore`, discovers visible properties, and never sends the
credential through a browser:

```go
result, err := seo.ImportConnection(ctx, platform.ImportConnectionRequest{
    Provider: "google-search-console",
    Credential: core.SecretMaterial{
        Type: "service_account_json",
        Bytes: existingServerCredential,
    },
    PropertyReference: "https://www.example.com/", // optional
    Actor: "host-migration",
})
// With no PropertyReference, result.Properties is rendered for selection in
// the embedded Console. Repeated imports never overwrite an existing setup.
```

The host may implement `core.SecretStore` with its existing KMS/Vault/encrypted
database. `secretstore.NewEncryptedFiles` is included for single-machine hosts.

## Optional standalone app

The CLI and Docker image are a runnable example for sites that explicitly want
a separate process. They are not required for module integration.

```bash
SEO_PUBLIC_URL=https://www.example.com go run ./cmd/seo-platform
```

### Standalone-only configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `SEO_PUBLIC_URL` | (required) | The site's public base URL |
| `SEO_BASE_URL` | `SEO_PUBLIC_URL` | Optional standalone Console origin used for OAuth callbacks; embedded hosts set `OAuthCallbackURL` in Go |
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

### Guided provider setup

Open the Console after startup. Its **Setup values** section shows copyable
public URL, sitemap URL, and OAuth callback URL values. Each provider card then
walks an administrator through its official console links, authorization or
one-time credential entry, property discovery/selection, a live connection
test, and the first sync.

For Google OAuth, create a Web application client in Google Cloud and register
the exact callback value shown by the Console. Configure its client ID and
secret on the server with `SEO_GOOGLE_OAUTH_CLIENT_ID` and
`SEO_GOOGLE_OAUTH_CLIENT_SECRET`; the secret is never sent to the browser.
Search Console and GA4 use the same Google OAuth client. Service-account JSON
remains an optional unattended fallback. Bing uses a Webmaster API key.

Provider credentials are write-only. Manual values and OAuth refresh tokens
are encrypted immediately by `SecretStore`, API responses expose only
connection state, and the Console does not persist provider credentials,
authorization codes, or refresh tokens in browser storage.

## Public module surfaces

- Embedded Go runtime: `github.com/HaticeStudio/seo-platform/platform`
- Secret-store helpers: `github.com/HaticeStudio/seo-platform/secretstore`
- Provider implementations: `github.com/HaticeStudio/seo-platform/providers/*`
- HTTP contract: [`api/openapi.yaml`](api/openapi.yaml)
- Go client: `github.com/HaticeStudio/seo-platform/sdk/go/seo`
- React package: `@haticestudio/seo-console` (build with `npm run build:lib`)
- Embedded-console example: [`examples/embedded-console`](examples/embedded-console/README.md)

The usual embedded path uses the host's same-origin session. `AuthClient`
remains optional for hosts whose existing API convention uses short-lived
bearer tokens.

## Repository layout

```
platform/           public in-process runtime for host backends
secretstore/        optional public secret-store helpers
cmd/seo-platform/   optional standalone example/server
core/               public contracts: Provider, SecretStore, resolvers, models
providers/          provider implementations (Search Console, Bing, GA4)
providertest/       public contract-test kit + fake provider
sdk/go/seo/         public Go client for the versioned API
console/            React console: standalone app + embeddable package
api/                machine-readable HTTP API contract
internal/           implementation details behind the public runtime
migrations/         forward-only SQL migrations
deploy/             Docker Compose example
```

## Status

`v0.x` — Google Search Console, Bing Webmaster, GA4, the embeddable runtime and
Console, optional standalone app, HTTP API, and Go SDK are available. APIs may
still change between minor versions; every breaking change ships with a
migration note (see [VERSIONING.md](VERSIONING.md)).

## License

[Apache-2.0](LICENSE). Security reports: see [SECURITY.md](SECURITY.md).
