import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, configureClient } from './client'

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('api client', () => {
  const onUnauthorized = vi.fn()

  beforeEach(() => {
    configureClient({ onUnauthorized })
  })

  afterEach(() => {
    vi.unstubAllGlobals() // restoreAllMocks does NOT undo stubGlobal
    vi.restoreAllMocks()
    onUnauthorized.mockReset()
  })

  it('returns parsed JSON on success', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { role: 'admin' }))
    vi.stubGlobal('fetch', fetchMock)

    const out = await api.get<{ role: string }>('/api/v1/me')
    expect(out.role).toBe('admin')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/me',
      expect.objectContaining({ method: 'GET', credentials: 'same-origin' }),
    )
  })

  it('sends JSON bodies with content-type', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, { ok: true }))
    vi.stubGlobal('fetch', fetchMock)

    await api.post('/api/v1/users', { username: 'x' })
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('POST')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(init.body).toBe(JSON.stringify({ username: 'x' }))
  })

  it('throws ApiError with the server error envelope message', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(409, { error: 'duplicate' })))

    const err = await api.post('/api/v1/duties', {}).then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).message).toBe('duplicate')
  })

  it('falls back to a generic message when the error body is not JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('boom', { status: 500 })),
    )

    const err = await api.get('/api/v1/agents').then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(500)
    expect((err as ApiError).message).toBe('request failed (500)')
  })

  it('invokes onUnauthorized and throws with the server message on 401', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(401, { error: 'invalid credentials' })),
    )

    const err = await api.get('/api/v1/me').then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).message).toBe('invalid credentials')
    expect(onUnauthorized).toHaveBeenCalledOnce()
  })
})
