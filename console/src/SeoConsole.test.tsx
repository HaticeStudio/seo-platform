// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiClient } from './api'
import { completeOAuthCallbackWithResult, OAUTH_PROVIDER_KEY, SeoConsole } from './SeoConsole'

const responses: Record<string, unknown> = {
  '/api/v0/site': {
    public_url: 'https://www.example.test',
    sitemap_url: 'https://www.example.test/sitemap.xml',
    oauth_callback: 'https://console.example/oauth/callback',
  },
  '/api/v0/providers': {
    providers: [
      {
        name: 'fake-search',
        display_name: 'Fake Search',
        credential_types: ['api_key'],
        capabilities: [{ capability: 'search.performance', supports_cursor: false }],
        setup_url: 'https://provider.example/settings',
        setup_links: [{ kind: 'credentials', label: 'Provider settings', url: 'https://provider.example/settings', description: 'Create an API key.' }],
        oauth_available: false,
      },
    ],
  },
  '/api/v0/connections': {
    connections: [{ provider: 'fake-search', configured: true, enabled: true, state: 'connected' }],
  },
  '/api/v0/sync-runs': { sync_runs: [] },
  '/api/v0/report-datasets': { datasets: ['fake/search_daily'] },
  '/api/v0/report-rows?dataset=fake%2Fsearch_daily&limit=100': {
    rows: [
      {
        dataset: 'fake/search_daily',
        key: '2026-08-18',
        data: { _key: '2026-08-18', date: '2026-08-18', clicks: 7 },
        updated_at: '2026-08-18T00:00:00Z',
      },
    ],
  },
}

describe('SeoConsole', () => {
  beforeEach(() => {
	Object.defineProperty(navigator, 'clipboard', {
		configurable: true,
		value: { writeText: vi.fn(async () => undefined) },
	})
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'https://console.example')
		if (url.pathname.endsWith('/credential') && init?.method === 'PUT') {
			return new Response(JSON.stringify({ configured: true, properties: [{ reference: 'site:example.test', display_name: 'Example site' }] }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' },
			})
		}
		if (url.pathname.endsWith('/property') && init?.method === 'PUT') {
			return new Response(JSON.stringify({ configured: true, property_reference: 'site:example.test' }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' },
			})
		}
        let responsePath = url.pathname + url.search
        if (responsePath.startsWith('/admin/seo/')) {
          responsePath = responsePath.slice('/admin/seo'.length)
        }
        const body = responses[responsePath]
        return new Response(JSON.stringify(body ?? { error: 'not found' }), {
          status: body ? 200 : 404,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
  })

  afterEach(() => {
    cleanup()
	sessionStorage.clear()
	window.history.replaceState({}, '', '/')
    vi.unstubAllGlobals()
  })

  it('renders provider state and normalized report data through the public API', async () => {
    const { container } = render(
      <SeoConsole
        apiBaseUrl="https://console.example"
        auth={{ getAccessToken: async () => 'short-lived-auth-token' }}
      />,
    )

    expect(await screen.findByText('Fake Search')).toBeTruthy()
    expect(screen.getByDisplayValue('https://www.example.test/sitemap.xml')).toBeTruthy()
    expect(screen.getByText('Connected')).toBeTruthy()
    expect(await screen.findByText('7')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Reconfigure' }))
    expect(screen.getByRole('link', { name: /Provider settings/ }).getAttribute('href')).toBe(
      'https://provider.example/settings',
    )
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    for (const call of vi.mocked(fetch).mock.calls) {
      const headers = new Headers(call[1]?.headers)
      expect(headers.get('Authorization')).toBe('Bearer short-lived-auth-token')
    }

    // jsdom has no layout/canvas implementation, so contrast remains a
    // browser-level check; all structural WCAG rules run here.
    const accessibility = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false } },
    })
    expect(accessibility.violations).toEqual([])
  })

  it('uses the host same-origin session when no auth client is provided', async () => {
    render(<SeoConsole apiBaseUrl="/admin/seo" />)

    expect(await screen.findByText('Fake Search')).toBeTruthy()
    for (const call of vi.mocked(fetch).mock.calls) {
      expect(call[1]?.credentials).toBe('same-origin')
      const headers = new Headers(call[1]?.headers)
      expect(headers.has('Authorization')).toBe(false)
    }
  })

	it('guides a Traditional Chinese administrator through secure credential and property setup', async () => {
		render(
			<SeoConsole
				apiBaseUrl="https://console.example"
				auth={{ getAccessToken: async () => 'short-lived-auth-token' }}
				locale="zh-TW"
			/>,
		)

		expect(await screen.findByText('Fake Search')).toBeTruthy()
		fireEvent.click(screen.getByRole('button', { name: '調整設定' }))
		expect(screen.getByRole('heading', { name: '設定引導' })).toBeTruthy()
		expect(screen.getByRole('link', { name: /建立／管理憑證/ }).getAttribute('href')).toBe(
			'https://provider.example/settings',
		)
		expect(screen.getByText(/SecretStore/)).toBeTruthy()

		fireEvent.change(screen.getByRole('textbox', { name: '憑證' }), { target: { value: 'one-time-secret' } })
		fireEvent.click(screen.getByRole('button', { name: '安全儲存憑證' }))
		expect(await screen.findByRole('option', { name: 'Example site' })).toBeTruthy()
		fireEvent.change(screen.getByRole('combobox', { name: 'Property' }), { target: { value: 'site:example.test' } })
		fireEvent.click(screen.getByRole('button', { name: '測試並使用這個 Property' }))

		await waitFor(() => {
			const credentialCall = vi.mocked(fetch).mock.calls.find(([input, init]) =>
				String(input).endsWith('/api/v0/connections/fake-search/credential') && init?.method === 'PUT',
			)
			expect(credentialCall).toBeTruthy()
			expect(credentialCall?.[1]?.body).toBe(JSON.stringify({ credential_type: 'api_key', material: 'one-time-secret' }))
		})
		expect(sessionStorage.length).toBe(0)
	})

	it('keeps a staged GA4 credential actionable without showing Bing instructions', async () => {
		const originalProviders = responses['/api/v0/providers']
		const originalConnections = responses['/api/v0/connections']
		responses['/api/v0/providers'] = {
			providers: [{
				name: 'google-analytics',
				display_name: 'Google Analytics 4',
				credential_types: ['oauth2', 'service_account_json'],
				capabilities: [{ capability: 'analytics.acquisition', supports_cursor: false }],
				setup_url: 'https://analytics.google.com/analytics/web/',
				setup_links: [
					{ kind: 'web_property', label: 'Create a website GA4 property', url: 'https://support.google.com/analytics/answer/9304153' },
					{ kind: 'permissions', label: 'Google Analytics Admin', url: 'https://analytics.google.com/analytics/web/#/a/admin' },
				],
				oauth_available: false,
			}],
		}
		responses['/api/v0/connections'] = {
			connections: [{ provider: 'google-analytics', configured: true, enabled: false, state: 'needs_property' }],
		}

		try {
			render(<SeoConsole apiBaseUrl="https://console.example" locale="zh-TW" />)
			expect(await screen.findByText('Google Analytics 4')).toBeTruthy()
			expect(screen.getByText('等待選擇 Property')).toBeTruthy()
			expect(screen.getByText(/既有憑證已在後端加密保存/)).toBeTruthy()
			fireEvent.click(screen.getByRole('button', { name: '繼續設定' }))
			expect(screen.getByRole('link', { name: /建立網站 GA4 Property/ })).toBeTruthy()
			expect(screen.getByText(/不要使用 Firebase／Android App Property/)).toBeTruthy()
			expect(screen.getByRole('button', { name: '重新取得 Property' })).toBeTruthy()
			expect(screen.getByText(/無法使用 OAuth/)).toBeTruthy()
			expect(screen.queryByText(/Bing Webmaster/)).toBeNull()
		} finally {
			responses['/api/v0/providers'] = originalProviders
			responses['/api/v0/connections'] = originalConnections
		}
	})

	it('completes OAuth once and restores the server-bound local return path', async () => {
		window.history.replaceState({}, '', '/oauth/callback?state=state-1&code=temporary-code')
		sessionStorage.setItem(OAUTH_PROVIDER_KEY, 'google-search-console')
		const client = new ApiClient('https://console.example', { getAccessToken: async () => 'short-lived-auth-token' })
		vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
			configured: true,
			return_to: '/admin/seo?tab=connections',
			properties: [{ reference: 'sc-domain:example.test', display_name: 'example.test' }],
		}), { status: 200, headers: { 'Content-Type': 'application/json' } }))

		const result = await completeOAuthCallbackWithResult(client)
		expect(result).toEqual({
			provider: 'google-search-console',
			returnTo: '/admin/seo?tab=connections',
			properties: [{ reference: 'sc-domain:example.test', display_name: 'example.test' }],
		})
		expect(sessionStorage.getItem(OAUTH_PROVIDER_KEY)).toBeNull()
		const calls = vi.mocked(fetch).mock.calls
		const [, init] = calls[calls.length - 1]
		expect(init?.body).toBe(JSON.stringify({ state: 'state-1', code: 'temporary-code' }))
	})
})
