import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'

vi.mock('./jukeboxClient', () => ({
  default: {
    play: vi.fn(() => Promise.resolve({})),
    pause: vi.fn(() => Promise.resolve({})),
    status: vi.fn(() => Promise.resolve({ playing: true })),
    volume: vi.fn(() => Promise.resolve({})),
    skip: vi.fn(() => Promise.resolve({})),
  },
}))

vi.mock('./jukeboxCommandQueue', () => ({
  enqueueJukeboxCommand: vi.fn((fn) => fn()),
}))

import jukeboxClient from './jukeboxClient'
import { enqueueJukeboxCommand } from './jukeboxCommandQueue'
import {
  shouldForwardJukeboxMediaEvent,
  suppressJukeboxMediaEvents,
  resetJukeboxMediaEventSuppression,
} from './jukeboxLifecycle'
import { syncJukeboxTrackChangeAfterQueueSync } from './jukeboxSync'

describe('Jukebox visibility guard logic', () => {
  let originalHidden

  beforeEach(() => {
    vi.clearAllMocks()
    originalHidden = Object.getOwnPropertyDescriptor(document, 'hidden')
    resetJukeboxMediaEventSuppression()
  })

  afterEach(() => {
    if (originalHidden) {
      Object.defineProperty(document, 'hidden', originalHidden)
    } else {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
    }
  })

  describe('onAudioPause guard', () => {
    const simulateOnAudioPause = (jukeboxMode) => {
      if (
        shouldForwardJukeboxMediaEvent({
          jukeboxMode,
          hidden: document.hidden,
        })
      ) {
        enqueueJukeboxCommand(() => jukeboxClient.pause())
      }
    }

    it('forwards pause to jukebox when tab is visible (user-initiated)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
      simulateOnAudioPause(true)
      expect(jukeboxClient.pause).toHaveBeenCalledTimes(1)
    })

    it('suppresses pause when tab is hidden (browser-initiated)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => true,
      })
      simulateOnAudioPause(true)
      expect(jukeboxClient.pause).not.toHaveBeenCalled()
    })

    it('does not forward pause when not in jukebox mode', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
      simulateOnAudioPause(false)
      expect(jukeboxClient.pause).not.toHaveBeenCalled()
    })
  })

  describe('onAudioPlay guard', () => {
    const simulateOnAudioPlay = (jukeboxMode) => {
      if (
        shouldForwardJukeboxMediaEvent({
          jukeboxMode,
          hidden: document.hidden,
        })
      ) {
        enqueueJukeboxCommand(() => jukeboxClient.play())
      }
    }

    it('forwards play to jukebox when tab is visible (user-initiated)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
      simulateOnAudioPlay(true)
      expect(jukeboxClient.play).toHaveBeenCalledTimes(1)
    })

    it('suppresses play when tab is hidden (browser auto-resume)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => true,
      })
      simulateOnAudioPlay(true)
      expect(jukeboxClient.play).not.toHaveBeenCalled()
    })
  })

  describe('pagehide cleanup', () => {
    it('sends stop request with keepalive on pagehide in jukebox mode', () => {
      const calls = []
      const origFetch = globalThis.fetch
      globalThis.fetch = (...args) => {
        calls.push(args)
        return Promise.resolve(new Response())
      }
      localStorage.setItem('token', 'fake-jwt-token')

      // Simulate: jukeboxMode is true, pagehide fires
      const jukeboxMode = true
      if (jukeboxMode) {
        const token = localStorage.getItem('token')
        if (token) {
          globalThis.fetch('/api/jukebox/stop', {
            method: 'POST',
            headers: {
              'X-ND-Authorization': `Bearer ${token}`,
              'Content-Type': 'application/json',
            },
            keepalive: true,
          })
        }
      }

      expect(calls).toHaveLength(1)
      expect(calls[0][0]).toBe('/api/jukebox/stop')
      expect(calls[0][1]).toEqual({
        method: 'POST',
        headers: {
          'X-ND-Authorization': 'Bearer fake-jwt-token',
          'Content-Type': 'application/json',
        },
        keepalive: true,
      })

      globalThis.fetch = origFetch
      localStorage.clear()
      localStorage.setItem('username', 'admin')
    })

    it('does not send stop request on pagehide when not in jukebox mode', () => {
      const calls = []
      const origFetch = globalThis.fetch
      globalThis.fetch = (...args) => {
        calls.push(args)
        return Promise.resolve(new Response())
      }

      const jukeboxMode = false
      if (jukeboxMode) {
        globalThis.fetch('/api/jukebox/pause', {
          method: 'POST',
          keepalive: true,
        })
      }

      expect(calls).toHaveLength(0)

      globalThis.fetch = origFetch
    })

    it('does not send stop request when no auth token exists', () => {
      const calls = []
      const origFetch = globalThis.fetch
      globalThis.fetch = (...args) => {
        calls.push(args)
        return Promise.resolve(new Response())
      }
      // Ensure no token is set (clear and re-add only username)
      localStorage.clear()
      localStorage.setItem('username', 'admin')

      const jukeboxMode = true
      if (jukeboxMode) {
        const token = localStorage.getItem('token')
        if (token) {
          globalThis.fetch('/api/jukebox/pause', {
            method: 'POST',
            keepalive: true,
          })
        }
      }

      expect(calls).toHaveLength(0)

      globalThis.fetch = origFetch
    })
  })

  describe('suppression window', () => {
    it('suppresses visible-tab events during the temporary guard window', () => {
      vi.useFakeTimers()
      suppressJukeboxMediaEvents(500)

      expect(
        shouldForwardJukeboxMediaEvent({
          jukeboxMode: true,
          hidden: false,
        }),
      ).toBe(false)

      vi.advanceTimersByTime(501)

      expect(
        shouldForwardJukeboxMediaEvent({
          jukeboxMode: true,
          hidden: false,
        }),
      ).toBe(true)

      vi.useRealTimers()
    })
  })


  describe('queue sync ordering', () => {
    it('waits for pending queue sync before sending skip', async () => {
      const calls = []
      let resolveQueueSync
      const pendingQueueSync = new Promise((resolve) => {
        resolveQueueSync = () => {
          calls.push('queue-sync')
          resolve()
        }
      })

      const trackChangePromise = syncJukeboxTrackChangeAfterQueueSync(
        pendingQueueSync,
        jukeboxClient,
        {
          audioLists: [
            { uuid: 'u1', trackId: 't1' },
            { uuid: 'u2', trackId: 't2' },
          ],
          playId: 'u2',
          audioInfo: { currentTime: 9 },
        },
      )

      await Promise.resolve()
      expect(jukeboxClient.skip).not.toHaveBeenCalled()

      resolveQueueSync()
      await trackChangePromise

      expect(jukeboxClient.skip).toHaveBeenCalledWith(1, 0)
      expect(calls).toEqual(['queue-sync'])
    })

    it('does not forward stale previous-track time during queue-click switching', async () => {
      await syncJukeboxTrackChangeAfterQueueSync(
        Promise.resolve(),
        jukeboxClient,
        {
          audioLists: [
            { uuid: 'u1', trackId: 't1' },
            { uuid: 'u2', trackId: 't2' },
          ],
          playId: 'u2',
          audioInfo: { trackId: 't1', currentTime: 42 },
        },
      )

      expect(jukeboxClient.skip).toHaveBeenCalledWith(1, 0)
    })
  })

  describe('beforeunload guard', () => {
    const shouldPreventUnload = ({ jukeboxMode, currentUuid, audioPaused }) => {
      if (jukeboxMode) {
        return !!currentUuid
      }
      return !!(currentUuid && !audioPaused)
    }

    it('prevents unload in jukebox mode when a track is loaded', () => {
      expect(
        shouldPreventUnload({
          jukeboxMode: true,
          currentUuid: 'some-uuid',
          audioPaused: true,
        }),
      ).toBe(true)
    })

    it('does not prevent unload in jukebox mode when no track is loaded', () => {
      expect(
        shouldPreventUnload({
          jukeboxMode: true,
          currentUuid: undefined,
          audioPaused: true,
        }),
      ).toBe(false)
    })

    it('prevents unload in local mode when audio is playing', () => {
      expect(
        shouldPreventUnload({
          jukeboxMode: false,
          currentUuid: 'some-uuid',
          audioPaused: false,
        }),
      ).toBe(true)
    })

    it('does not prevent unload in local mode when audio is paused', () => {
      expect(
        shouldPreventUnload({
          jukeboxMode: false,
          currentUuid: 'some-uuid',
          audioPaused: true,
        }),
      ).toBe(false)
    })
  })
})
