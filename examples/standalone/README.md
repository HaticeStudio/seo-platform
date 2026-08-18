# Standalone single-site example

Runs the whole platform — server, scheduler, and console — as one container
with SQLite and the encrypted local secret store. No external services.

```bash
cd examples/standalone

# 1. Create .env (never commit it)
cat > .env <<EOF
SEO_PUBLIC_URL=https://www.example.com
SEO_SECRETS_MASTER_KEY=$(openssl rand -hex 32)
EOF

# 2. Generate an admin API key and add its hash
API_KEY=$(openssl rand -hex 24)
echo "SEO_API_KEYS=$(printf '%s' "$API_KEY" | shasum -a 256 | cut -d' ' -f1)=read,sync,connections.manage" >> .env
echo "Console API key (save it, it is not stored): $API_KEY"

# 3. Start
docker compose up -d

# 4. Open http://127.0.0.1:8080 and sign in with the API key
```

Connect providers from the console: paste a Bing Webmaster API key or a
Google service-account JSON, pick the property, test, and sync. For
interactive Google OAuth, also set `SEO_GOOGLE_OAUTH_CLIENT_ID` and
`SEO_GOOGLE_OAUTH_CLIENT_SECRET` in `.env`.
