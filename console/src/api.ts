// Typed client for the versioned v0 API. The console depends only on this
// public API — never on server internals (ADR 0005).

export type CapabilitySpec = {
  capability: string
  dimensions?: string[]
  metrics?: string[]
  max_range_days?: number
  freshness_lag_days?: number
  supports_cursor: boolean
  quota_hint?: string
}

export type ProviderDescriptor = {
  name: string
  display_name: string
  credential_types: string[]
  capabilities: CapabilitySpec[]
  // Setup deep links come from the server descriptor; the UI never hardcodes
  // third-party console paths.
  setup_url?: string
  docs_url?: string
  oauth_available: boolean
  setup_links?: { kind?: string; label: string; url: string; description?: string }[]
}

export type SiteContext = {
  public_url: string
  sitemap_url: string
  oauth_callback: string
}

export type DiscoveredProperty = {
  reference: string
  display_name: string
}

export type ConnectionState =
  | 'not_configured'
  | 'needs_property'
  | 'reauthorization_required'
  | 'error'
  | 'stale'
  | 'no_data'
  | 'connected'

export type Connection = {
  provider: string
  configured: boolean
  enabled: boolean
  credential_type?: string
  property_reference?: string
  state: ConnectionState
  last_success_at?: string
  data_through_date?: string
  last_error_code?: string
  last_error_message?: string
}

export type SyncRun = {
  id: string
  provider: string
  capability: string
  start_date: string
  end_date: string
  status: 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'PARTIAL' | 'FAILED'
  rows_synced: number
  error_code?: string
  error_message?: string
  started_at: string
  finished_at?: string
}

export type ReportRow = {
  dataset: string
  key: string
  data: Record<string, unknown>
  updated_at: string
}

// AuthClient supplies short-lived access tokens only. It never sees provider
// credentials.
export type AuthClient = {
  getAccessToken(): Promise<string | undefined>
  onUnauthorized?(): void
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
  }
}

export class ApiClient {
  constructor(
    private readonly baseUrl: string,
    private readonly auth?: AuthClient,
  ) {}

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const token = await this.auth?.getAccessToken()
    const headers = new Headers(init?.headers)
    if (token) headers.set('Authorization', `Bearer ${token}`)
    if (init?.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
    const response = await fetch(this.baseUrl.replace(/\/$/, '') + path, {
      ...init,
      credentials: init?.credentials ?? 'same-origin',
      headers,
    })
    if (response.status === 401) {
      this.auth?.onUnauthorized?.()
    }
    if (!response.ok) {
      let message = `request failed (${response.status})`
      try {
        const body = (await response.json()) as { error?: string }
        if (body.error) message = body.error
      } catch {
        // keep generic message
      }
      throw new ApiError(response.status, message)
    }
    return (await response.json()) as T
  }

  listProviders(): Promise<{ providers: ProviderDescriptor[] }> {
    return this.request('/api/v0/providers')
  }

  getSite(): Promise<SiteContext> {
    return this.request('/api/v0/site')
  }

  listConnections(): Promise<{ connections: Connection[] }> {
    return this.request('/api/v0/connections')
  }

  listSyncRuns(provider?: string): Promise<{ sync_runs: SyncRun[] }> {
    const query = provider ? `?provider=${encodeURIComponent(provider)}` : ''
    return this.request(`/api/v0/sync-runs${query}`)
  }

  listReportDatasets(): Promise<{ datasets: string[] }> {
    return this.request('/api/v0/report-datasets')
  }

  listReportRows(dataset: string, limit = 100): Promise<{ rows: ReportRow[] }> {
    const query = new URLSearchParams({ dataset, limit: String(limit) })
    return this.request(`/api/v0/report-rows?${query}`)
  }

  setCredential(
    provider: string,
    credentialType: string,
    material: string,
  ): Promise<{ configured: boolean; properties?: DiscoveredProperty[]; property_discovery_error?: string }> {
    return this.request(`/api/v0/connections/${encodeURIComponent(provider)}/credential`, {
      method: 'PUT',
      body: JSON.stringify({ credential_type: credentialType, material }),
    })
  }

  setProperty(provider: string, propertyReference: string): Promise<{ configured: boolean }> {
    return this.request(`/api/v0/connections/${encodeURIComponent(provider)}/property`, {
      method: 'PUT',
      body: JSON.stringify({ property_reference: propertyReference }),
    })
  }

  listProperties(provider: string): Promise<{ properties: DiscoveredProperty[] }> {
    return this.request(`/api/v0/connections/${encodeURIComponent(provider)}/properties`)
  }

  testConnection(provider: string): Promise<{ ok: boolean; error?: string }> {
    return this.request(`/api/v0/connections/${encodeURIComponent(provider)}/test`, {
      method: 'POST',
      body: '{}',
    })
  }

  revokeConnection(provider: string): Promise<{ configured: boolean }> {
    return this.request(`/api/v0/connections/${encodeURIComponent(provider)}`, {
      method: 'DELETE',
    })
  }

  oauthStart(provider: string, redirectUri: string, returnTo = '/'): Promise<{ authorize_url: string; state: string }> {
    return this.request(`/api/v0/connections/${encodeURIComponent(provider)}/oauth/start`, {
      method: 'POST',
      body: JSON.stringify({ redirect_uri: redirectUri, return_to: returnTo }),
    })
  }

  oauthComplete(
    provider: string,
    state: string,
    code: string,
  ): Promise<{ configured: boolean; properties?: DiscoveredProperty[]; return_to?: string }> {
    return this.request(`/api/v0/connections/${encodeURIComponent(provider)}/oauth/complete`, {
      method: 'POST',
      body: JSON.stringify({ state, code }),
    })
  }

  createSyncRun(input: {
    provider: string
    capability: string
    start_date?: string
    end_date?: string
    idempotency_key?: string
  }): Promise<{ sync_run: SyncRun; already_running: boolean }> {
    return this.request('/api/v0/sync-runs', {
      method: 'POST',
      body: JSON.stringify(input),
    })
  }
}
