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
}

export type ConnectionState =
  | 'not_configured'
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

// AuthClient supplies short-lived access tokens only. It never sees provider
// credentials.
export type AuthClient = {
  getAccessToken(): Promise<string>
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
    private readonly auth: AuthClient,
  ) {}

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const token = await this.auth.getAccessToken()
    const response = await fetch(this.baseUrl.replace(/\/$/, '') + path, {
      ...init,
      headers: {
        ...init?.headers,
        Authorization: `Bearer ${token}`,
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      },
    })
    if (response.status === 401) {
      this.auth.onUnauthorized?.()
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

  listConnections(): Promise<{ connections: Connection[] }> {
    return this.request('/api/v0/connections')
  }

  listSyncRuns(provider?: string): Promise<{ sync_runs: SyncRun[] }> {
    const query = provider ? `?provider=${encodeURIComponent(provider)}` : ''
    return this.request(`/api/v0/sync-runs${query}`)
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
