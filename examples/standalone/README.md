# Standalone single-site example

Runs the whole platform — server, scheduler, and console — as one container
with SQLite and the encrypted local secret store. No external services.

```bash
cd examples/standalone

# 1. Configure the site and start
printf 'SEO_PUBLIC_URL=https://www.example.com\n' > .env
docker compose up -d

# 2. Retrieve the generated admin API key exactly once and save it
docker compose exec seo-platform seo-platform admin bootstrap

# 3. Open http://127.0.0.1:8080 and sign in with that API key
```

Connect providers from the console: paste a Bing Webmaster API key or a
Google service-account JSON, pick the property, test, sync, and inspect report
rows. For
interactive Google OAuth, also set `SEO_GOOGLE_OAUTH_CLIENT_ID` and
`SEO_GOOGLE_OAUTH_CLIENT_SECRET` in `.env`.
