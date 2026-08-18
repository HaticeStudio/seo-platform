import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ApiClient,
  type AuthClient,
  type Connection,
  type DiscoveredProperty,
  type ProviderDescriptor,
  type ReportRow,
  type SiteContext,
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

export function SeoConsole({ apiBaseUrl, auth, theme, locale = 'en' }: SeoConsoleOptions) {
  const client = useMemo(() => new ApiClient(apiBaseUrl, auth), [apiBaseUrl, auth])
  const [providers, setProviders] = useState<ProviderDescriptor[]>([])
  const [site, setSite] = useState<SiteContext | null>(null)
  const [connections, setConnections] = useState<Connection[]>([])
  const [runs, setRuns] = useState<SyncRun[]>([])
  const [datasets, setDatasets] = useState<string[]>([])
  const [selectedDataset, setSelectedDataset] = useState('')
  const [reportRows, setReportRows] = useState<ReportRow[]>([])
  const [error, setError] = useState('')
  const [busyProvider, setBusyProvider] = useState('')

  const refresh = useCallback(async () => {
    try {
      const [providerData, siteData, connectionData, runData, datasetData] = await Promise.all([
        client.listProviders(),
        client.getSite(),
        client.listConnections(),
        client.listSyncRuns(),
        client.listReportDatasets(),
      ])
      setProviders(providerData.providers)
      setSite(siteData)
      setConnections(connectionData.connections)
      setRuns(runData.sync_runs)
      setDatasets(datasetData.datasets)
      setSelectedDataset((current) =>
        current && datasetData.datasets.includes(current) ? current : (datasetData.datasets[0] ?? ''),
      )
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

  useEffect(() => {
    if (!selectedDataset) {
      setReportRows([])
      return
    }
    let active = true
    client
      .listReportRows(selectedDataset)
      .then((result) => {
        if (active) setReportRows(result.rows)
      })
      .catch((cause) => {
        if (active) setError(cause instanceof Error ? cause.message : 'loading reports failed')
      })
    return () => {
      active = false
    }
  }, [client, selectedDataset, runs])

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
    <div className="seo-console" style={style} lang={locale} role="region" aria-label="SEO platform console">
      {error && <div className="seo-console__error" role="alert">{error}</div>}
      {site && <SetupValues site={site} />}
      <section>
        <h2>Providers</h2>
        <div className="seo-console__cards">
          {providers.map((provider) => (
            <ProviderCard
              key={provider.name}
              client={client}
              provider={provider}
              connection={connections.find((item) => item.provider === provider.name)}
              oauthCallback={site?.oauth_callback}
              busy={busyProvider === provider.name}
              onSync={triggerSync}
              onChanged={refresh}
            />
          ))}
          {providers.length === 0 && !error && <p>No providers are installed.</p>}
        </div>
      </section>
      <section>
        <h2>Sync runs</h2>
        <SyncRunTable runs={runs} />
      </section>
      <section>
        <h2>Reports</h2>
        {datasets.length > 0 ? (
          <>
            <label>
              Dataset
              <select
                value={selectedDataset}
                onChange={(event) => setSelectedDataset(event.target.value)}
              >
                {datasets.map((dataset) => (
                  <option key={dataset} value={dataset}>
                    {dataset}
                  </option>
                ))}
              </select>
            </label>
            <ReportTable rows={reportRows} />
          </>
        ) : (
          <p>No report data yet. Connect a provider and run a sync.</p>
        )}
      </section>
    </div>
  )
}

export const OAUTH_PROVIDER_KEY = 'seo-console.oauth-provider'

function ProviderCard({
  client,
  provider,
  connection,
  oauthCallback,
  busy,
  onSync,
  onChanged,
}: {
  client: ApiClient
  provider: ProviderDescriptor
  connection?: Connection
  oauthCallback?: string
  busy: boolean
  onSync: (provider: string, capability: string) => void
  onChanged: () => Promise<void>
}) {
  const state = connection?.state ?? 'not_configured'
  const [connecting, setConnecting] = useState(false)
  const [message, setMessage] = useState('')
  const [properties, setProperties] = useState<DiscoveredProperty[]>([])

  const saveCredential = async (credentialType: string, material: string) => {
    setMessage('')
    try {
      const result = await client.setCredential(provider.name, credentialType, material)
      setProperties(result.properties ?? [])
      if (result.property_discovery_error) {
        setMessage(result.property_discovery_error)
      }
      await onChanged()
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'saving credential failed')
    }
  }

  const chooseProperty = async (reference: string) => {
    setMessage('')
    try {
      await client.setProperty(provider.name, reference)
      setConnecting(false)
      setProperties([])
      await onChanged()
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'saving property failed')
    }
  }

  const startOAuth = async () => {
    setMessage('')
    try {
      const redirectUri = oauthCallback ?? `${window.location.origin}/oauth/callback`
      const started = await client.oauthStart(provider.name, redirectUri)
      sessionStorage.setItem(OAUTH_PROVIDER_KEY, provider.name)
      window.location.assign(started.authorize_url)
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'starting authorization failed')
    }
  }

  const test = async () => {
    setMessage('')
    try {
      const result = await client.testConnection(provider.name)
      setMessage(result.ok ? 'Connection test passed.' : (result.error ?? 'test failed'))
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'test failed')
    }
  }

  const revoke = async () => {
    setMessage('')
    try {
      await client.revokeConnection(provider.name)
      setConnecting(false)
      await onChanged()
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'revoke failed')
    }
  }

  const manualTypes = provider.credential_types.filter((type) => type !== 'oauth2')

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
      {state === 'error' && connection?.last_error_message && (
        // Provider errors are plain text, never markup (ADR 0005).
        <p className="seo-console__hint seo-console__hint--error">
          {connection.last_error_code}: {connection.last_error_message}
        </p>
      )}
      {connection?.data_through_date && (
        <p className="seo-console__hint">Data through {connection.data_through_date}</p>
      )}
      {message && <p className="seo-console__hint">{message}</p>}
      {connecting && (
        <ConnectPanel
          manualTypes={manualTypes}
          oauthAvailable={provider.oauth_available}
          setupUrl={provider.setup_url}
          setupLinks={provider.setup_links}
          properties={properties}
          allowManualProperty={Boolean(connection?.configured)}
          onCredential={saveCredential}
          onProperty={chooseProperty}
          onOAuth={startOAuth}
          onClose={() => setConnecting(false)}
        />
      )}
      <footer>
        {!connecting && (
          <button onClick={() => setConnecting(true)}>
            {state === 'not_configured' ? 'Connect' : 'Reconfigure'}
          </button>
        )}
        {state !== 'not_configured' && (
          <>
            <button onClick={() => void test()}>Test</button>
            <button className="seo-console__danger" onClick={() => void revoke()}>
              Revoke
            </button>
          </>
        )}
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

function ConnectPanel({
  manualTypes,
  oauthAvailable,
  setupUrl,
  setupLinks,
  properties,
  allowManualProperty,
  onCredential,
  onProperty,
  onOAuth,
  onClose,
}: {
  manualTypes: string[]
  oauthAvailable: boolean
  setupUrl?: string
  setupLinks?: { label: string; url: string }[]
  properties: DiscoveredProperty[]
  allowManualProperty: boolean
  onCredential: (credentialType: string, material: string) => Promise<void>
  onProperty: (reference: string) => Promise<void>
  onOAuth: () => Promise<void>
  onClose: () => void
}) {
  const [credentialType, setCredentialType] = useState(manualTypes[0] ?? '')
  const [material, setMaterial] = useState('')
  const [property, setProperty] = useState('')

  if (properties.length > 0) {
    return (
      <div className="seo-console__connect">
        <label>
          Choose a property
          <select value={property} onChange={(event) => setProperty(event.target.value)}>
            <option value="">—</option>
            {properties.map((item) => (
              <option key={item.reference} value={item.reference}>
                {item.display_name || item.reference}
              </option>
            ))}
          </select>
        </label>
        <button disabled={!property} onClick={() => void onProperty(property)}>
          Use this property
        </button>
      </div>
    )
  }

  return (
    <div className="seo-console__connect">
      {(setupLinks?.length || setupUrl) && (
        <div className="seo-console__links" aria-label="Official setup links">
          {(setupLinks?.length ? setupLinks : [{ label: 'Provider console', url: setupUrl! }]).map((link) => (
            <a key={link.url} href={link.url} target="_blank" rel="noreferrer">
              {link.label} <span aria-hidden="true">↗</span>
            </a>
          ))}
        </div>
      )}
      {oauthAvailable && (
        <button onClick={() => void onOAuth()}>Authorize with the provider</button>
      )}
      {manualTypes.length > 0 && (
        <>
          {manualTypes.length > 1 && (
            <label>
              Credential type
              <select
                value={credentialType}
                onChange={(event) => setCredentialType(event.target.value)}
              >
                {manualTypes.map((type) => (
                  <option key={type} value={type}>
                    {type}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            Credential
            <textarea
              rows={3}
              value={material}
              onChange={(event) => setMaterial(event.target.value)}
              placeholder={
                credentialType === 'service_account_json'
                  ? 'Paste the service-account JSON'
                  : 'Paste the API key'
              }
            />
          </label>
          <button
            disabled={!material.trim()}
            onClick={() => {
              const value = material
              setMaterial('')
              void onCredential(credentialType, value)
            }}
          >
            Save credential
          </button>
        </>
      )}
      {allowManualProperty && (
        <label>
          Property reference
          <input
            value={property}
            onChange={(event) => setProperty(event.target.value)}
            placeholder="Enter a property ID or site URL"
          />
          <button disabled={!property.trim()} onClick={() => void onProperty(property.trim())}>
            Test and use this property
          </button>
        </label>
      )}
      <button className="seo-console__ghost" onClick={onClose}>
        Cancel
      </button>
    </div>
  )
}

function SetupValues({ site }: { site: SiteContext }) {
  const values = [
    ['Public site URL', site.public_url],
    ['Sitemap URL', site.sitemap_url],
    ['OAuth callback URL', site.oauth_callback],
  ]
  return (
    <section aria-labelledby="seo-setup-values">
      <h2 id="seo-setup-values">Setup values</h2>
      <div className="seo-console__setup-values">
        {values.map(([label, value]) => (
          <label key={label}>
            {label}
            <span>
              <input readOnly value={value} aria-label={label} />
              <button type="button" onClick={() => void navigator.clipboard?.writeText(value)}>
                Copy
              </button>
            </span>
          </label>
        ))}
      </div>
    </section>
  )
}

// completeOAuthCallback finishes an authorization round-trip. Shells call it
// on their callback route with the query parameters the provider sent back.
export async function completeOAuthCallback(client: ApiClient): Promise<string> {
  const params = new URLSearchParams(window.location.search)
  const state = params.get('state') ?? ''
  const code = params.get('code') ?? ''
  const provider = sessionStorage.getItem(OAUTH_PROVIDER_KEY) ?? ''
  sessionStorage.removeItem(OAUTH_PROVIDER_KEY)
  if (!state || !code || !provider) {
    throw new Error('authorization response is incomplete; start again')
  }
  await client.oauthComplete(provider, state, code)
  return provider
}

function SyncRunTable({ runs }: { runs: SyncRun[] }) {
  if (runs.length === 0) {
    return <p>No sync runs yet.</p>
  }
  return (
    <div className="seo-console__table-wrap">
      <table>
        <caption className="seo-console__sr-only">Recent synchronization runs</caption>
        <thead>
          <tr>
            <th scope="col">Provider</th>
            <th scope="col">Capability</th>
            <th scope="col">Range</th>
            <th scope="col">Status</th>
            <th scope="col">Rows</th>
            <th scope="col">Error</th>
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

function ReportTable({ rows }: { rows: ReportRow[] }) {
  if (rows.length === 0) return <p>This dataset has no rows.</p>
  const columns = Array.from(
    new Set(rows.flatMap((row) => Object.keys(row.data).filter((key) => key !== '_key'))),
  ).slice(0, 12)
  return (
    <div className="seo-console__table-wrap">
      <table>
        <caption className="seo-console__sr-only">Normalized report rows</caption>
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column} scope="col">
                {column}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.key}>
              {columns.map((column) => (
                <td key={column}>{formatCell(row.data[column])}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function formatCell(value: unknown): string {
  if (value == null) return '—'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}
