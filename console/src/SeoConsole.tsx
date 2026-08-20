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
  // Omit auth when the API is mounted in the same host application and uses
  // its existing cookie/session. Provide it only when the host needs to add a
  // short-lived bearer token.
  auth?: AuthClient
  locale?: string
  theme?: Partial<Record<'accent' | 'background' | 'surface' | 'text', string>>
}

type DisplayState = Connection['state'] | 'authorizing' | 'syncing'

const COPY = {
  en: {
    setupValues: 'Setup values', providers: 'Providers', syncRuns: 'Sync runs', reports: 'Reports',
    publicURL: 'Public site URL', sitemapURL: 'Sitemap URL', callbackURL: 'OAuth callback URL', copy: 'Copy', copied: 'Copied',
    states: { not_configured: 'Not configured', needs_property: 'Choose a property', reauthorization_required: 'Reauthorization required', error: 'Error', stale: 'Stale', no_data: 'No data yet', connected: 'Connected', authorizing: 'Authorizing', syncing: 'Syncing' } as Record<DisplayState, string>,
    connect: 'Connect', continueSetup: 'Continue setup', reconfigure: 'Reconfigure', test: 'Test connection', revoke: 'Disconnect', authorize: 'Authorize with Google',
    credentialType: 'Credential type', credential: 'Credential', saveCredential: 'Save credential securely', property: 'Property', chooseProperty: 'Choose a property', useProperty: 'Test and use this property', refreshProperties: 'Refresh properties', cancel: 'Close setup',
    setupGuide: 'Setup guide', officialSteps: 'Open the official consoles below and complete the required steps.', secretNotice: 'Credentials are sent once to the server-side encrypted secret store. They are never returned by the API or saved in browser storage.',
    oauthPreferred: 'Recommended: authorize with Google OAuth. A service-account JSON remains available for unattended installations.',
    apiKeyHelp: 'Create a Bing Webmaster API key, then paste it here.', serviceAccountHelp: 'Use a Google service-account JSON only when OAuth is unavailable or for an unattended installation.', existingCredentialHelp: 'An existing credential is already encrypted on the server. Grant it provider access, then refresh the property list; you do not need to paste it again.', connectedHelp: 'Credential and property are configured. Test the connection, then start the first sync.',
    selectPropertyHelp: 'Select a property visible to this credential. Property selection also performs a live permission test.',
    testPassed: 'Connection test passed.', openingProvider: 'Opening provider authorization…', disconnected: 'Provider disconnected.',
    noProviders: 'No providers are installed.', noRuns: 'No sync runs yet.', noData: 'No report data yet. Connect a provider and run a sync.',
    dataset: 'Dataset', dataThrough: 'Data through', sync: 'Sync', emptyDataset: 'This dataset has no rows.',
    recentRuns: 'Recent synchronization runs', normalizedRows: 'Normalized report rows',
    table: { provider: 'Provider', capability: 'Capability', range: 'Range', status: 'Status', rows: 'Rows', error: 'Error' },
  },
  zh: {
    setupValues: '本網站設定值', providers: '資料來源設定', syncRuns: '同步紀錄', reports: '資料預覽',
    publicURL: '公開網站網址', sitemapURL: 'Sitemap 網址', callbackURL: 'OAuth 回呼網址', copy: '複製', copied: '已複製',
    states: { not_configured: '尚未設定', needs_property: '等待選擇 Property', reauthorization_required: '需要重新授權', error: '連線錯誤', stale: '資料過期', no_data: '尚無資料', connected: '已連線', authorizing: '授權中', syncing: '同步中' } as Record<DisplayState, string>,
    connect: '開始設定', continueSetup: '繼續設定', reconfigure: '調整設定', test: '測試連線', revoke: '中斷連線', authorize: '使用 Google 帳號授權',
    credentialType: '憑證類型', credential: '憑證', saveCredential: '安全儲存憑證', property: 'Property', chooseProperty: '選擇網站／Property', useProperty: '測試並使用這個 Property', refreshProperties: '重新取得 Property', cancel: '關閉設定',
    setupGuide: '設定引導', officialSteps: '依序開啟下列官方頁面完成設定，完成後回到這裡授權或輸入憑證。', secretNotice: '憑證只會送往後端加密 SecretStore；API 不會回傳，也不會保存在瀏覽器儲存空間。',
    oauthPreferred: '建議使用 Google OAuth 授權；只有無人值守環境才需要匯入 service-account JSON。',
    apiKeyHelp: '先到 Bing Webmaster 建立 API Key，再貼到這裡。', serviceAccountHelp: '只有無法使用 OAuth 或無人值守的環境才需要匯入 Google service-account JSON。', existingCredentialHelp: '既有憑證已在後端加密保存；授予資料來源權限後重新取得 Property 即可，不需要再次貼上憑證。', connectedHelp: '憑證與 Property 已設定。請測試連線，再執行第一次同步。',
    selectPropertyHelp: '請選擇這組憑證能存取的 Property；儲存時會同步驗證權限。',
    testPassed: '連線測試成功。', openingProvider: '正在前往官方授權頁面…', disconnected: '已中斷資料來源連線。',
    noProviders: '沒有可用的資料來源。', noRuns: '尚無同步紀錄。', noData: '尚無資料；請先完成連線並執行同步。',
    dataset: '資料集', dataThrough: '資料截至', sync: '同步', emptyDataset: '這個資料集尚無資料。',
    recentRuns: '最近同步紀錄', normalizedRows: '標準化報表資料',
    table: { provider: '資料來源', capability: '功能', range: '日期範圍', status: '狀態', rows: '筆數', error: '錯誤' },
  },
}

function copyFor(locale: string) {
  return locale.toLowerCase().startsWith('zh') ? COPY.zh : COPY.en
}

type ConsoleCopy = ReturnType<typeof copyFor>

function setupLinkLabel(kind: string | undefined, fallback: string, text: ConsoleCopy): string {
  if (text !== COPY.zh) return fallback
  return ({
    web_property: '建立網站 GA4 Property',
    console: '開啟官方管理平台',
    enable_api: '啟用必要 API',
    credentials: '建立／管理憑證',
    permissions: '設定資源與帳號權限',
    sitemaps: '提交 Sitemap',
  } as Record<string, string>)[kind ?? ''] ?? fallback
}

function setupLinkDescription(kind: string | undefined, fallback: string | undefined, text: ConsoleCopy): string | undefined {
  if (text !== COPY.zh) return fallback
  return ({
    web_property: '建立或選擇本網站專用的 GA4 Property，並新增「網站」資料串流；不要使用 Firebase／Android App Property。',
    console: '開啟官方平台，確認資源與資料狀態。',
    enable_api: '在持有 OAuth 用戶端的 Google Cloud 專案啟用必要 API。',
    credentials: '建立或管理 OAuth 用戶端、service account 或 API Key。',
    permissions: '授予登入帳號或 service account 讀取 Property 的權限。',
    sitemaps: '提交並檢查公開 Sitemap。',
  } as Record<string, string>)[kind ?? ''] ?? fallback
}

function connectionDiagnostic(connection: Connection | undefined, text: ConsoleCopy): string {
  if (!connection || connection.state === 'not_configured') return ''
  if (connection.state === 'needs_property') return text.existingCredentialHelp
  if (connection.state === 'reauthorization_required') {
    return text === COPY.zh ? '授權已失效或帳號沒有 Property 權限，請重新授權並再次選擇 Property。' : 'Authorization expired or lost property access. Reauthorize and choose the property again.'
  }
  if (connection.last_error_code === 'RATE_LIMITED') {
    return text === COPY.zh ? '已達資料來源配額，系統稍後可重試；不需要重新建立憑證。' : 'The provider quota was reached. Retry later; the credential does not need replacing.'
  }
  if (connection.last_error_code === 'TRANSIENT') {
    return text === COPY.zh ? '資料來源暫時無法使用，請稍後測試連線或重新同步。' : 'The provider is temporarily unavailable. Test or sync again later.'
  }
  if (connection.state === 'no_data') return text.connectedHelp
  if (connection.state === 'stale') {
    return text === COPY.zh ? '連線仍存在，但資料已過期，請執行同步並檢查最近一次失敗原因。' : 'The connection exists but data is stale. Run a sync and inspect the latest failure.'
  }
  return ''
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
  const text = copyFor(locale)

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
      {site && <SetupValues site={site} text={text} />}
      <section>
        <h2>{text.providers}</h2>
        <div className="seo-console__cards">
          {providers.map((provider) => (
            <ProviderCard
              key={provider.name}
              client={client}
              provider={provider}
              connection={connections.find((item) => item.provider === provider.name)}
              oauthCallback={site?.oauth_callback}
              runs={runs.filter((run) => run.provider === provider.name)}
              text={text}
              busy={busyProvider === provider.name}
              onSync={triggerSync}
              onChanged={refresh}
            />
          ))}
          {providers.length === 0 && !error && <p>{text.noProviders}</p>}
        </div>
      </section>
      <section>
        <h2>{text.syncRuns}</h2>
        <SyncRunTable runs={runs} emptyText={text.noRuns} text={text} />
      </section>
      <section>
        <h2>{text.reports}</h2>
        {datasets.length > 0 ? (
          <>
            <label>
              {text.dataset}
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
            <ReportTable rows={reportRows} text={text} />
          </>
        ) : (
          <p>{text.noData}</p>
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
  runs,
  text,
  busy,
  onSync,
  onChanged,
}: {
  client: ApiClient
  provider: ProviderDescriptor
  connection?: Connection
  oauthCallback?: string
  runs: SyncRun[]
  text: ConsoleCopy
  busy: boolean
  onSync: (provider: string, capability: string) => void
  onChanged: () => Promise<void>
}) {
  const state = connection?.state ?? 'not_configured'
  const [connecting, setConnecting] = useState(false)
  const [authorizing, setAuthorizing] = useState(false)
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
    setAuthorizing(true)
    try {
      const redirectUri = oauthCallback ?? `${window.location.origin}/oauth/callback`
      const returnTo = window.location.pathname + window.location.search
      const started = await client.oauthStart(provider.name, redirectUri, returnTo)
      sessionStorage.setItem(OAUTH_PROVIDER_KEY, provider.name)
      setMessage(text.openingProvider)
      window.location.assign(started.authorize_url)
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'starting authorization failed')
      setAuthorizing(false)
    }
  }

  const test = async () => {
    setMessage('')
    try {
      const result = await client.testConnection(provider.name)
      setMessage(result.ok ? text.testPassed : (result.error ?? 'test failed'))
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'test failed')
    }
  }

  const revoke = async () => {
    setMessage('')
    try {
      await client.revokeConnection(provider.name)
      setConnecting(false)
      setMessage(text.disconnected)
      await onChanged()
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : 'revoke failed')
    }
  }

  const manualTypes = provider.credential_types.filter((type) => type !== 'oauth2')
  const syncing = runs.some((run) => run.status === 'QUEUED' || run.status === 'RUNNING')
  const displayState: DisplayState = authorizing ? 'authorizing' : syncing ? 'syncing' : state
  const diagnostic = connectionDiagnostic(connection, text)

  return (
    <article className="seo-console__card">
      <header>
        <h3>{provider.display_name}</h3>
        <span className={`seo-console__state seo-console__state--${displayState}`} aria-live="polite">
          {text.states[displayState]}
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
      {state === 'reauthorization_required' && connection?.last_error_message && (
        <p className="seo-console__hint seo-console__hint--error">
          {connection.last_error_message}
        </p>
      )}
      {diagnostic && <p className="seo-console__hint">{diagnostic}</p>}
      {connection?.data_through_date && (
        <p className="seo-console__hint">{text.dataThrough} {connection.data_through_date}</p>
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
          text={text}
          onCredential={saveCredential}
          onProperty={chooseProperty}
          onOAuth={startOAuth}
          onDiscoverProperties={async () => {
            setMessage('')
            try {
              const result = await client.listProperties(provider.name)
              setProperties(result.properties)
            } catch (cause) {
              setMessage(cause instanceof Error ? cause.message : 'loading properties failed')
            }
          }}
          onClose={() => setConnecting(false)}
        />
      )}
      <footer>
        {!connecting && (
          <button onClick={() => setConnecting(true)}>
            {state === 'not_configured' ? text.connect : state === 'needs_property' ? text.continueSetup : text.reconfigure}
          </button>
        )}
        {connection?.enabled && (
          <button onClick={() => void test()}>{text.test}</button>
        )}
        {connection?.configured && (
          <>
            <button className="seo-console__danger" onClick={() => void revoke()}>
              {text.revoke}
            </button>
          </>
        )}
        {provider.capabilities.map((capability) => (
          <button
            key={capability.capability}
            disabled={busy || !connection?.enabled}
            onClick={() => onSync(provider.name, capability.capability)}
          >
            {text.sync} {capability.capability}
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
  text,
  onCredential,
  onProperty,
  onOAuth,
  onDiscoverProperties,
  onClose,
}: {
  manualTypes: string[]
  oauthAvailable: boolean
  setupUrl?: string
  setupLinks?: { kind?: string; label: string; url: string; description?: string }[]
  properties: DiscoveredProperty[]
  allowManualProperty: boolean
  text: ConsoleCopy
  onCredential: (credentialType: string, material: string) => Promise<void>
  onProperty: (reference: string) => Promise<void>
  onOAuth: () => Promise<void>
  onDiscoverProperties: () => Promise<void>
  onClose: () => void
}) {
  const [credentialType, setCredentialType] = useState(manualTypes[0] ?? '')
  const [material, setMaterial] = useState('')
  const [property, setProperty] = useState('')

  if (properties.length > 0) {
    return (
      <div className="seo-console__connect">
        <h4>{text.chooseProperty}</h4>
        <p className="seo-console__hint">{text.selectPropertyHelp}</p>
        <label>
          {text.property}
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
          {text.useProperty}
        </button>
      </div>
    )
  }

  return (
    <div className="seo-console__connect">
      <h4>{text.setupGuide}</h4>
      <p className="seo-console__hint">{text.officialSteps}</p>
      {(setupLinks?.length || setupUrl) && (
        <ol className="seo-console__links" aria-label="Official setup links">
          {(setupLinks?.length ? setupLinks : [{ label: 'Provider console', url: setupUrl! }]).map((link) => {
            const kind = 'kind' in link ? link.kind : undefined
            const description = setupLinkDescription(
              kind,
              'description' in link ? link.description : undefined,
              text,
            )
            return (
              <li key={link.url}>
                <a href={link.url} target="_blank" rel="noreferrer">
                  {setupLinkLabel(kind, link.label, text)} <span aria-hidden="true">↗</span>
                </a>
                {description && <span>{description}</span>}
              </li>
            )
          })}
        </ol>
      )}
      {oauthAvailable && (
        <>
          <p className="seo-console__hint">{text.oauthPreferred}</p>
          <button onClick={() => void onOAuth()}>{text.authorize}</button>
        </>
      )}
      {allowManualProperty && (
        <div className="seo-console__property-actions">
          <p className="seo-console__hint">{text.existingCredentialHelp}</p>
          <button type="button" onClick={() => void onDiscoverProperties()}>{text.refreshProperties}</button>
          <label>
            {text.property}
            <input
              value={property}
              onChange={(event) => setProperty(event.target.value)}
              placeholder="Property ID or site URL"
            />
          </label>
          <button disabled={!property.trim()} onClick={() => void onProperty(property.trim())}>
            {text.useProperty}
          </button>
        </div>
      )}
      {manualTypes.length > 0 && (
        <>
          {!oauthAvailable && (
            <p className="seo-console__hint">
              {credentialType === 'api_key' ? text.apiKeyHelp : text.serviceAccountHelp}
            </p>
          )}
          {manualTypes.length > 1 && (
            <label>
              {text.credentialType}
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
            {text.credential}
            <textarea
              rows={3}
              value={material}
              onChange={(event) => setMaterial(event.target.value)}
              autoComplete="off"
              spellCheck={false}
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
            {text.saveCredential}
          </button>
          <p className="seo-console__hint">{text.secretNotice}</p>
        </>
      )}
      <button className="seo-console__ghost" onClick={onClose}>
        {text.cancel}
      </button>
    </div>
  )
}

function SetupValues({ site, text }: { site: SiteContext; text: ConsoleCopy }) {
  const values = [
    [text.publicURL, site.public_url],
    [text.sitemapURL, site.sitemap_url],
    [text.callbackURL, site.oauth_callback],
  ]
  const [copied, setCopied] = useState('')
  return (
    <section aria-labelledby="seo-setup-values">
      <h2 id="seo-setup-values">{text.setupValues}</h2>
      <div className="seo-console__setup-values">
        {values.map(([label, value]) => (
          <label key={label}>
            {label}
            <span>
              <input readOnly value={value} aria-label={label} />
              <button type="button" aria-label={`${text.copy} ${label}`} onClick={() => {
                void navigator.clipboard?.writeText(value).then(() => setCopied(label))
              }}>
                {copied === label ? text.copied : text.copy}
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
export type OAuthCallbackResult = { provider: string; returnTo: string; properties: DiscoveredProperty[] }

export async function completeOAuthCallbackWithResult(client: ApiClient): Promise<OAuthCallbackResult> {
  const params = new URLSearchParams(window.location.search)
  const state = params.get('state') ?? ''
  const code = params.get('code') ?? ''
  const provider = sessionStorage.getItem(OAUTH_PROVIDER_KEY) ?? ''
  sessionStorage.removeItem(OAUTH_PROVIDER_KEY)
  if (!state || !code || !provider) {
    throw new Error('authorization response is incomplete; start again')
  }
  const result = await client.oauthComplete(provider, state, code)
  return { provider, returnTo: result.return_to || '/', properties: result.properties ?? [] }
}

export async function completeOAuthCallback(client: ApiClient): Promise<string> {
  return (await completeOAuthCallbackWithResult(client)).provider
}

function SyncRunTable({ runs, emptyText, text }: { runs: SyncRun[]; emptyText: string; text: ConsoleCopy }) {
  if (runs.length === 0) {
    return <p>{emptyText}</p>
  }
  return (
    <div className="seo-console__table-wrap">
      <table>
        <caption className="seo-console__sr-only">{text.recentRuns}</caption>
        <thead>
          <tr>
            <th scope="col">{text.table.provider}</th>
            <th scope="col">{text.table.capability}</th>
            <th scope="col">{text.table.range}</th>
            <th scope="col">{text.table.status}</th>
            <th scope="col">{text.table.rows}</th>
            <th scope="col">{text.table.error}</th>
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

function ReportTable({ rows, text }: { rows: ReportRow[]; text: ConsoleCopy }) {
  if (rows.length === 0) return <p>{text.emptyDataset}</p>
  const columns = Array.from(
    new Set(rows.flatMap((row) => Object.keys(row.data).filter((key) => key !== '_key'))),
  ).slice(0, 12)
  return (
    <div className="seo-console__table-wrap">
      <table>
        <caption className="seo-console__sr-only">{text.normalizedRows}</caption>
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
