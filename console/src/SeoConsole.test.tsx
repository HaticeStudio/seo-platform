// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SeoConsole } from './SeoConsole'

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
        setup_links: [{ label: 'Provider settings', url: 'https://provider.example/settings' }],
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
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'https://console.example')
        const body = responses[url.pathname + url.search]
        return new Response(JSON.stringify(body ?? { error: 'not found' }), {
          status: body ? 200 : 404,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
  })

  afterEach(() => {
    cleanup()
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
})
