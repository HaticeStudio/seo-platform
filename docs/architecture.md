# Architecture and trust boundaries

seo-platform is embed-first. Its default integration is a Go package mounted
inside an existing backend plus a React package rendered inside the existing
admin shell. It requires no second runtime service, domain, API key, gateway,
or data replication layer.

## Public boundaries

- Providers implement `core.Provider` and declare only capabilities their
  upstream API actually supports.
- Go hosts import `platform`, provide their host authenticator and secret
  store, and mount its `/api/v0` handler under an existing path.
- React hosts use the Console package with the same-origin host session.
  `AuthClient` is optional when the host already uses short-lived bearer auth.
- The SDK is for optional remote/standalone integrations, not the normal
  in-process path.
- A host-specific identity or routing adapter belongs in the host repository,
  never in this repository.

## Credential boundary

Provider material enters through a credential or OAuth completion endpoint and
is immediately written to `SecretStore`. Connections persist opaque references
only. Every secret operation is scoped by site and provider; a reference from a
different scope is rejected. Rotation validates the replacement before an
atomic connection swap, and only then revokes the old material.

OAuth state is short-lived, single-use, PKCE protected, and bound to provider,
site, initiating subject, a same-origin (or loopback) redirect, and a local
post-authorization return path. Tokens,
authorization codes, private keys, and client secrets are excluded from API
responses, browser storage, audit events, and structured logs.

Embedded hosts pass their existing authenticated session through a public
`platform.Authenticator`, which maps the host user to generic SEO scopes. There
is no platform API key and no BFF. The standalone example alone uses scoped API
keys because it has no host login system.

## Data isolation and failure semantics

Connections, OAuth state, sync runs, and report rows are site-scoped. Optional
multi-site resolvers may select a site, but single-site installations need no
tenant configuration. Sync results distinguish unsupported, not configured,
unauthorized, quota, timeout, partial, and provider failures. Missing or not-yet
synced data is never represented as invented zero-valued data.

## Release boundary

Tagged releases publish the Go module, Console package, optional standalone
binaries, checksums, and an optional multi-architecture container image.
Publishing a release does not deploy or modify any downstream environment.
