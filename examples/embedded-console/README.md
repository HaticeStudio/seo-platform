# Embedding the console in a host admin

The console ships as an ES module (`@haticestudio/seo-console`). A host
renders it inside its own admin shell and supplies a generic `AuthClient` —
typically forwarding the host's short-lived session token to a
host-authenticated backend-for-frontend. That server-side adapter checks the
host permission and adds its seo-platform API key while proxying `/api/v0`.
The browser and host business database never receive provider credentials.

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
      apiBaseUrl="/admin-api/seo-platform"
      auth={auth}
			locale="zh-TW"
      theme={{ accent: '#0f766e' }}
    />
  )
}
```

The proxy must preserve the versioned path and response unchanged. Keep the
seo-platform service and its API key on a private network; do not put the key
in JavaScript, HTML, local storage, or a public environment variable.

For OAuth-based provider authorization, set `SEO_BASE_URL` to the public host
origin, register the exact callback shown in **Setup values**, and proxy the
callback API requests to seo-platform. On the callback page call
`completeOAuthCallbackWithResult(client)` and navigate only to its returned
`returnTo`. The server binds that local return path to the initiating subject,
site, provider, PKCE verifier, and single-use OAuth state.

Theme, locale, and routing are presentation settings only; scopes and data
access are always decided server-side from the authenticated subject.
