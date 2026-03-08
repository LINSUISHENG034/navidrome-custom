import { describe, it, beforeEach, vi, expect, afterEach } from 'vitest'
import { startEventStream } from './eventStream'
import { serverDown } from './actions'
import config from './config'

class MockEventSource {
  constructor(url) {
    this.url = url
    this.readyState = 1
    this.listeners = {}
    this.onerror = null
  }
  addEventListener(type, handler) {
    this.listeners[type] = handler
  }
  close() {
    this.readyState = 2
  }
}

describe('startEventStream', () => {
  vi.useFakeTimers()
  let dispatch
  let instance

  beforeEach(() => {
    dispatch = vi.fn()
    global.EventSource = vi.fn().mockImplementation(function (url) {
      instance = new MockEventSource(url)
      return instance
    })
    localStorage.setItem('is-authenticated', 'true')
    localStorage.setItem('token', 'abc')
    config.devNewEventStream = true
    vi.spyOn(console, 'log').mockImplementation(() => {})
  })

  afterEach(() => {
    config.devNewEventStream = false
  })

  it('reconnects after an error', async () => {
    await startEventStream(dispatch)
    expect(global.EventSource).toHaveBeenCalledTimes(1)
    instance.onerror(new Event('error'))
    expect(dispatch).toHaveBeenCalledWith(serverDown())
    vi.advanceTimersByTime(5000)
    expect(global.EventSource).toHaveBeenCalledTimes(2)
  })

  it('subscribes to jukebox state updates', async () => {
    await startEventStream(dispatch)
    expect(instance.listeners.jukeboxStateUpdated).toBeTypeOf('function')

    dispatch.mockClear()
    instance.listeners.jukeboxStateUpdated({
      type: 'jukeboxStateUpdated',
      data: JSON.stringify({ sessionId: 's1', currentIndex: 1, trackId: 't2' }),
    })

    expect(dispatch).toHaveBeenCalledWith({
      type: 'jukeboxStateUpdated',
      data: { sessionId: 's1', currentIndex: 1, trackId: 't2' },
    })
  })
})
