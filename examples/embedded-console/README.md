# Embed SEO into an existing admin

The normal integration runs inside the host application. It does not require a
seo-platform service URL, API key, reverse proxy, or extra deployment.

## Backend

Import `github.com/HaticeStudio/seo-platform/platform`, provide the host's
existing authentication/RBAC adapter and `core.SecretStore`, then mount the
handler under the existing admin path:

```go
seo, err := platform.New(ctx, platform.Config{
    Site: core.Site{
        ID: "main",
        PublicURL: "https://example.com",
        SitemapURL: "https://example.com/sitemap.xml",
    },
    StorePath: "data/seo.db",
    Secrets: hostSecretStore,
    Authenticator: platform.AuthenticateFunc(func(r *http.Request) (core.Subject, error) {
        user, err := hostSession(r)
        if err != nil || !user.CanManageSEO { return core.Subject{}, errUnauthorized }
        return core.Subject{
            ID: user.ID,
            Scopes: []string{core.ScopeRead, core.ScopeSync, core.ScopeConnectionsManage},
        }, nil
    }),
    Providers: []core.Provider{searchconsole.New(), bing.New(), ga4.New()},
    OAuthCallbackURL: "https://example.com/admin/seo/oauth/callback",
})
if err != nil { return err }
defer seo.Close()
go seo.Start(ctx)

router.Handle("/admin/seo/", http.StripPrefix("/admin/seo", seo.Handler()))
```

## Frontend

The Console uses the host's same-origin cookie/session by default:

```tsx
import { SeoConsole } from '@haticestudio/seo-console'
import '@haticestudio/seo-console/style.css'

export function SeoAdminPage() {
  return <SeoConsole apiBaseUrl="/admin/seo" locale="zh-TW" />
}
```

On the existing `/admin/seo/oauth/callback` page, finish the provider return
through the same mounted API and navigate to the server-bound local path:

```tsx
const result = await completeOAuthCallbackWithResult(new ApiClient('/admin/seo'))
window.location.assign(result.returnTo)
```

If the host already authenticates every API call with a short-lived bearer
token, it may optionally pass `auth={{ getAccessToken }}`. Provider credentials
remain write-only and are stored by the server-side `SecretStore` in both
modes.
