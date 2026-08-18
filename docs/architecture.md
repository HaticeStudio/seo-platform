# Architecture and trust boundaries

seo-platform is standalone-first. Its default deployment represents one
public site and requires no tenant system, identity vendor, private gateway,
or host-specific domain model.

## Public boundaries

- Providers implement `core.Provider` and declare only capabilities their
  upstream API actually supports.
- The server exposes `/api/v0`; its contract is `api/openapi.yaml`.
- Go hosts use `sdk/go/seo`. React hosts use the Console package and provide an
  `AuthClient` which returns a short-lived platform access token.
- A host-specific identity or routing adapter belongs in the host repository,
  never in this repository.

## Credential boundary

Provider material enters through a credential or OAuth completion endpoint and
is immediately written to `SecretStore`. Connections persist opaque references
only. Every secret operation is scoped by site and provider; a reference from a
different scope is rejected. Rotation validates the replacement before an
atomic connection swap, and only then revokes the old material.

OAuth state is short-lived, single-use, PKCE protected, and bound to provider,
site, initiating subject, and a same-origin (or loopback) redirect. Tokens,
authorization codes, private keys, and client secrets are excluded from API
responses, browser storage, audit events, and structured logs.

## Data isolation and failure semantics

Connections, OAuth state, sync runs, and report rows are site-scoped. Optional
multi-site resolvers may select a site, but single-site installations need no
tenant configuration. Sync results distinguish unsupported, not configured,
unauthorized, quota, timeout, partial, and provider failures. Missing or not-yet
synced data is never represented as invented zero-valued data.

## Release boundary

Tagged releases publish source archives, platform binaries, Console assets,
checksums, and a multi-architecture container image with provenance and SBOM.
Publishing a release does not deploy or modify any downstream environment.
