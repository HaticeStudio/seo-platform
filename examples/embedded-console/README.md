# Embedding the console in a host admin

The console ships as an ES module (`@haticestudio/seo-console`). A host
renders it inside its own admin shell and supplies a generic `AuthClient` —
typically minting a short-lived JWT the seo-platform server is configured to
trust. The host never touches provider credentials.

```tsx
import { SeoConsole, type AuthClient } from '@haticestudio/seo-console'
import '@haticestudio/seo-console/style.css'

const auth: AuthClient = {
  // Exchange the host session for a short-lived seo-platform token.
  async getAccessToken() {
    const response = await fetch('/internal/seo-token', { method: 'POST' })
    const { token } = await response.json()
    return token
  },
  onUnauthorized() {
    window.location.assign('/login')
  },
}

export function SeoAdminPage() {
  return (
    <SeoConsole
      apiBaseUrl="https://seo.internal.example.com"
      auth={auth}
      theme={{ accent: '#0f766e' }}
    />
  )
}
```

For OAuth-based provider authorization inside an embedded console, register a
host route as the redirect URI and call `completeOAuthCallback(client)` on it
— the same helper the standalone shell uses.

Theme, locale, and routing are presentation settings only; scopes and data
access are always decided server-side from the authenticated subject.
