export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

interface ClientConfig {
  onUnauthorized: () => void
}

// Default 401 behavior: bounce to /login preserving the current location —
// except on /login itself, where the error must surface in the form.
let cfg: ClientConfig = {
  onUnauthorized: () => {
    if (!window.location.pathname.startsWith('/login')) {
      const next = encodeURIComponent(window.location.pathname + window.location.search)
      window.location.assign(`/login?next=${next}`)
    }
  },
}

export function configureClient(overrides: Partial<ClientConfig>): void {
  cfg = { ...cfg, ...overrides }
}

async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let msg = `request failed (${res.status})`
    try {
      const data: unknown = await res.json()
      if (data && typeof data === 'object' && typeof (data as { error?: unknown }).error === 'string') {
        msg = (data as { error: string }).error
      }
    } catch {
      // non-JSON error body: keep the generic message
    }
    if (res.status === 401) {
      cfg.onUnauthorized()
      throw new ApiError(401, msg === `request failed (401)` ? 'authentication required' : msg)
    }
    throw new ApiError(res.status, msg)
  }
  return res.json() as Promise<T>
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return handle<T>(await fetch(path, init))
}

async function requestRaw<T>(method: string, path: string, body: BodyInit, contentType: string): Promise<T> {
  return handle<T>(
    await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: { 'Content-Type': contentType },
      body,
    }),
  )
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
  putRaw: <T>(path: string, body: BodyInit, contentType: string) => requestRaw<T>('PUT', path, body, contentType),
}
