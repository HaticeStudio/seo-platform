# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately via GitHub Security Advisories
("Report a vulnerability" on this repository) — not in public issues.

We aim to acknowledge reports within 7 days and to coordinate disclosure
within 90 days of the initial report.

## Supported versions

Only the latest minor release line receives security fixes while the project
is in `v0.x`. From `v1.0` on, the two most recent minor lines are supported.

## Secret handling expectations

- Credential material (OAuth tokens, service-account JSON, API keys) lives
  only in the configured secret store. It is never written to business
  tables, logs, API responses, or Git.
- If you believe a secret has leaked through this software, rotate the
  affected provider credentials immediately and report the path it leaked
  through as a vulnerability.
- API keys for this server are stored as SHA-256 hashes; the plaintext is
  shown once at creation and cannot be recovered.

## Hardening checklist for operators

- Run behind TLS; keep the default loopback bind unless a reverse proxy or
  network policy fronts the server.
- Use a production secret store (cloud secret manager, Vault, or
  envelope-encrypted DB) instead of the local file store.
- Keep the secret-store master key out of the same backup set as the
  encrypted payloads.
- Grant API keys the narrowest scopes that work.
