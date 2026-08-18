import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ApiClient,
  type AuthClient,
  type Connection,
  type ProviderDescriptor,
  type SyncRun,
} from './api'
import './console.css'

// Options mirror the ADR 0005 console contract: presentation settings only.
// Nothing here may influence scopes, site resolution, or query semantics.
export type SeoConsoleOptions = {
  apiBaseUrl: string
  auth: AuthClient
  locale?: string
  theme?: Partial<Record<'accent' | 'background' | 'surface' | 'text', string>>
}

const STATE_LABELS: Record<Connection['state'], string> = {
  not_configured: 'Not configured',
  error: 'Error',
  stale: 'Stale',
  no_data: 'No data yet',
  connected: 'Connected',
}

const REFRESH_MS = 15000

export function SeoConsole({ apiBaseUrl, auth, theme }: SeoConsoleOptions) {
  const client = useMemo(() => new ApiClient(apiBaseUrl, auth), [apiBaseUrl, auth])
  const [providers, setProviders] = useState<ProviderDescriptor[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [runs, setRuns] = useState<SyncRun[]>([])
  const [error, setError] = useState('')
  const [busyProvider, setBusyProvider] = useState('')

  const refresh = useCallback(async () => {
    try {
      const [providerData, connectionData, runData] = await Promise.all([
        client.listProviders(),
        client.listConnections(),
        client.listSyncRuns(),
      ])
      setProviders(providerData.providers)
      setConnections(connectionData.connections)
      setRuns(runData.sync_runs)
      setError('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'request failed')
    }
  }, [client])

  useEffect(() => {
    void refresh()
    const timer = setInterval(() => void refresh(), REFRESH_MS)
    return () => clearInterval(timer)
  }, [refresh])

  const triggerSync = useCallback(
    async (provider: string, capability: string) => {
      setBusyProvider(provider)
      try {
        await client.createSyncRun({ provider, capability })
        await refresh()
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : 'sync failed')
      } finally {
        setBusyProvider('')
      }
    },
    [client, refresh],
  )

  const style = theme
    ? ({
        '--seo-accent': theme.accent,
        '--seo-background': theme.background,
        '--seo-surface': theme.surface,
        '--seo-text': theme.text,
      } as React.CSSProperties)
    : undefined

  return (
    <div className="seo-console" style={style}>
      {error && <div className="seo-console__error">{error}</div>}
      <section>
        <h2>Providers</h2>
        <div className="seo-console__cards">
          {providers.map((provider) => (
            <ProviderCard
              key={provider.name}
              provider={provider}
              connection={connections.find((item) => item.provider === provider.name)}
              busy={busyProvider === provider.name}
              onSync={triggerSync}
            />
          ))}
          {providers.length === 0 && !error && <p>No providers are installed.</p>}
        </div>
      </section>
      <section>
        <h2>Sync runs</h2>
        <SyncRunTable runs={runs} />
      </section>
    </div>
  )
}

function ProviderCard({
  provider,
  connection,
  busy,
  onSync,
}: {
  provider: ProviderDescriptor
  connection?: Connection
  busy: boolean
  onSync: (provider: string, capability: string) => void
}) {
  const state = connection?.state ?? 'not_configured'
  return (
    <article className="seo-console__card">
      <header>
        <h3>{provider.display_name}</h3>
        <span className={`seo-console__state seo-console__state--${state}`}>
          {STATE_LABELS[state]}
        </span>
      </header>
      {connection?.property_reference && (
        <p className="seo-console__property">{connection.property_reference}</p>
      )}
      {state === 'not_configured' && (
        <p className="seo-console__hint">
          Connect this provider with a {provider.credential_types.join(' or ')}{' '}
          credential.
          {provider.setup_url && (
            <>
              {' '}
              <a href={provider.setup_url} target="_blank" rel="noreferrer">
                Open provider console
              </a>
            </>
          )}
        </p>
      )}
      {state === 'error' && connection?.last_error_message && (
        // Provider errors are plain text, never markup (ADR 0005).
        <p className="seo-console__hint seo-console__hint--error">
          {connection.last_error_code}: {connection.last_error_message}
        </p>
      )}
      {connection?.data_through_date && (
        <p className="seo-console__hint">Data through {connection.data_through_date}</p>
      )}
      <footer>
        {provider.capabilities.map((capability) => (
          <button
            key={capability.capability}
            disabled={busy || state === 'not_configured'}
            onClick={() => onSync(provider.name, capability.capability)}
          >
            Sync {capability.capability}
          </button>
        ))}
      </footer>
    </article>
  )
}

function SyncRunTable({ runs }: { runs: SyncRun[] }) {
  if (runs.length === 0) {
    return <p>No sync runs yet.</p>
  }
  return (
    <div className="seo-console__table-wrap">
      <table>
        <thead>
          <tr>
            <th>Provider</th>
            <th>Capability</th>
            <th>Range</th>
            <th>Status</th>
            <th>Rows</th>
            <th>Error</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id}>
              <td>{run.provider}</td>
              <td>{run.capability}</td>
              <td>
                {run.start_date} → {run.end_date}
              </td>
              <td className={`seo-console__status--${run.status.toLowerCase()}`}>
                {run.status}
              </td>
              <td>{run.rows_synced}</td>
              <td>{run.error_code ? `${run.error_code}: ${run.error_message ?? ''}` : ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
