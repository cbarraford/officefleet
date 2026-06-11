import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { connectStream, type EventSourceLike } from './sse'

class FakeEventSource implements EventSourceLike {
  static instances: FakeEventSource[] = []
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  closed = false

  constructor(public url: string) {
    FakeEventSource.instances.push(this)
  }

  close(): void {
    this.closed = true
  }
}

const factory = (url: string) => new FakeEventSource(url)
const latest = () => FakeEventSource.instances[FakeEventSource.instances.length - 1]

describe('connectStream', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    FakeEventSource.instances = []
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('connects to /api/v1/stream and reports status on open', () => {
    const onStatus = vi.fn()
    connectStream({ onStatus }, factory)
    expect(latest().url).toBe('/api/v1/stream')
    latest().onopen?.()
    expect(onStatus).toHaveBeenCalledWith(true)
  })

  it('parses messages and dispatches them', () => {
    const onMessage = vi.fn()
    connectStream({ onMessage }, factory)
    latest().onopen?.()
    latest().onmessage?.({ data: '{"event":"run_started","id":"r1","status":"running"}' })
    expect(onMessage).toHaveBeenCalledWith(expect.objectContaining({ event: 'run_started', id: 'r1' }))
  })

  it('ignores malformed payloads without dying', () => {
    const onMessage = vi.fn()
    connectStream({ onMessage }, factory)
    latest().onopen?.()
    latest().onmessage?.({ data: 'not-json{' })
    expect(onMessage).not.toHaveBeenCalled()
  })

  it('reconnects with doubling backoff and fires onReconnect on re-open', () => {
    const onStatus = vi.fn()
    const onReconnect = vi.fn()
    connectStream({ onStatus, onReconnect }, factory)

    latest().onopen?.() // first open: no onReconnect
    expect(onReconnect).not.toHaveBeenCalled()

    latest().onerror?.() // drop
    expect(onStatus).toHaveBeenLastCalledWith(false)
    expect(FakeEventSource.instances).toHaveLength(1)

    vi.advanceTimersByTime(1000) // first retry after 1s
    expect(FakeEventSource.instances).toHaveLength(2)

    latest().onerror?.() // still down: next delay doubles to 2s
    vi.advanceTimersByTime(1999)
    expect(FakeEventSource.instances).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(FakeEventSource.instances).toHaveLength(3)

    latest().onopen?.() // back: refetch signal fires, delay resets
    expect(onReconnect).toHaveBeenCalledOnce()
    expect(onStatus).toHaveBeenLastCalledWith(true)

    latest().onerror?.()
    vi.advanceTimersByTime(1000) // reset delay: 1s again
    expect(FakeEventSource.instances).toHaveLength(4)
  })

  it('caps the backoff at 30s', () => {
    connectStream({}, factory)
    latest().onopen?.()
    // 1, 2, 4, 8, 16, 32→30 …
    for (let i = 0; i < 6; i++) {
      latest().onerror?.()
      vi.advanceTimersByTime(30000)
    }
    const count = FakeEventSource.instances.length
    latest().onerror?.()
    vi.advanceTimersByTime(29999)
    expect(FakeEventSource.instances).toHaveLength(count)
    vi.advanceTimersByTime(1)
    expect(FakeEventSource.instances).toHaveLength(count + 1)
  })

  it('stop() closes the source and cancels pending retries', () => {
    const stop = connectStream({}, factory)
    latest().onopen?.()
    latest().onerror?.()
    stop()
    vi.advanceTimersByTime(60000)
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(latest().closed).toBe(true)
  })
})
