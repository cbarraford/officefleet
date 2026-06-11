import type { StreamMsg } from './types'

// The SSE feed is advisory (the server drops messages for slow consumers and
// the run table is truth), so every reconnect fires onReconnect as a
// "refetch your data" signal.

export interface StreamHandlers {
  onMessage?: (msg: StreamMsg) => void
  onStatus?: (connected: boolean) => void
  onReconnect?: () => void
}

export interface EventSourceLike {
  onopen: (() => void) | null
  onmessage: ((ev: { data: string }) => void) | null
  onerror: (() => void) | null
  close(): void
}

export type EventSourceFactory = (url: string) => EventSourceLike

const INITIAL_DELAY_MS = 1000
const MAX_DELAY_MS = 30000

export function connectStream(
  handlers: StreamHandlers,
  // EventSource's native handler signatures are wider (MessageEvent, this-
  // typed) than the minimal shape we drive, so a direct assignment fails
  // under strictFunctionTypes — the double assertion is deliberate.
  makeSource: EventSourceFactory = (url) => new EventSource(url) as unknown as EventSourceLike,
): () => void {
  let source: EventSourceLike | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let delay = INITIAL_DELAY_MS
  let everOpened = false
  let stopped = false

  const open = () => {
    const es = makeSource('/api/v1/stream')
    source = es
    es.onopen = () => {
      delay = INITIAL_DELAY_MS
      handlers.onStatus?.(true)
      if (everOpened) handlers.onReconnect?.()
      everOpened = true
    }
    es.onmessage = (ev) => {
      try {
        handlers.onMessage?.(JSON.parse(ev.data) as StreamMsg)
      } catch {
        // malformed payload: ignore (feed is advisory)
      }
    }
    es.onerror = () => {
      if (stopped || source !== es) return // stale/duplicate error from an old source
      source = null
      handlers.onStatus?.(false)
      es.close()
      timer = setTimeout(open, delay)
      delay = Math.min(delay * 2, MAX_DELAY_MS)
    }
  }

  open()
  return () => {
    stopped = true
    if (timer) clearTimeout(timer)
    source?.close()
  }
}
