// Standalone shell: same component, an API-key AuthClient. The key is asked
// for at runtime and kept in sessionStorage only — never embedded in the
// build or persisted to disk.
import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { completeOAuthCallback, SeoConsole } from './SeoConsole'
import { ApiClient, type AuthClient } from './api'

const API_BASE_KEY = 'seo-console.api-base'
const TOKEN_KEY = 'seo-console.token'

function Shell() {
  const [apiBase, setApiBase] = useState(
    sessionStorage.getItem(API_BASE_KEY) ?? window.location.origin,
  )
  const [token, setToken] = useState(sessionStorage.getItem(TOKEN_KEY) ?? '')
  const [entered, setEntered] = useState(Boolean(token))

  if (!entered) {
    return (
      <form
        className="seo-console-login"
        onSubmit={(event) => {
          event.preventDefault()
          sessionStorage.setItem(API_BASE_KEY, apiBase)
          sessionStorage.setItem(TOKEN_KEY, token)
          setEntered(true)
        }}
      >
        <h1>SEO Platform Console</h1>
        <label>
          Server URL
          <input value={apiBase} onChange={(event) => setApiBase(event.target.value)} />
        </label>
        <label>
          API key
          <input
            type="password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            autoComplete="off"
          />
        </label>
        <button type="submit">Open console</button>
      </form>
    )
  }

  const auth: AuthClient = {
    getAccessToken: () => Promise.resolve(token),
    onUnauthorized: () => {
      sessionStorage.removeItem(TOKEN_KEY)
      setEntered(false)
    },
  }
  return <AuthedShell apiBase={apiBase} auth={auth} />
}

function AuthedShell({ apiBase, auth }: { apiBase: string; auth: AuthClient }) {
  const [oauthMessage, setOauthMessage] = useState(
    window.location.pathname === '/oauth/callback' ? 'Completing authorization…' : '',
  )

  useEffect(() => {
    if (window.location.pathname !== '/oauth/callback') return
    completeOAuthCallback(new ApiClient(apiBase, auth))
      .then((provider) => setOauthMessage(`${provider} authorized.`))
      .catch((cause) =>
        setOauthMessage(cause instanceof Error ? cause.message : 'authorization failed'),
      )
      .finally(() => window.history.replaceState(null, '', '/'))
    // The callback query is consumed exactly once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <>
      {oauthMessage && <p style={{ margin: '0.75rem 1.5rem' }}>{oauthMessage}</p>}
      <SeoConsole apiBaseUrl={apiBase} auth={auth} />
    </>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Shell />
  </StrictMode>,
)
