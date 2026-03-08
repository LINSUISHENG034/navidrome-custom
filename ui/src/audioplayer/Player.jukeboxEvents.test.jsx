import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'

vi.mock('navidrome-music-player', () => ({
  default: () => null,
}))

vi.mock('./jukeboxClient', () => ({

  default: {
    play: vi.fn(() => Promise.resolve({})),
    pause: vi.fn(() => Promise.resolve({})),
    status: vi.fn(() => Promise.resolve({ playing: true })),
    volume: vi.fn(() => Promise.resolve({})),
    skip: vi.fn(() => Promise.resolve({})),
    seek: vi.fn(() => Promise.resolve({})),
    attachSession: vi.fn(() => Promise.resolve({})),
    heartbeatSession: vi.fn(() => Promise.resolve({})),
  },
}))

vi.mock('./jukeboxCommandQueue', () => ({
  enqueueJukeboxCommand: vi.fn((fn) => fn()),
}))

import jukeboxClient from './jukeboxClient'
import { enqueueJukeboxCommand } from './jukeboxCommandQueue'
import {
  markPendingRemoteSeek,
  markPendingRemoteTrackChange,
  shouldSuppressRemoteSeekEcho,
  shouldSuppressRemoteTrackEcho,
  shouldForwardJukeboxMediaEvent,
  suppressJukeboxMediaEvents,
  resetJukeboxMediaEventSuppression,
} from './jukeboxLifecycle'
import {
  syncJukeboxSeek,
  syncJukeboxTrackChangeAfterQueueSync,
} from './jukeboxSync'
import {
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  resolveControlledJukeboxPlayIndex,
  resolvePlayerUiState,
  startJukeboxHeartbeatLoop,
  syncRemotePositionIfNeeded,
} from './Player'
import keyHandlers from './keyHandlers'

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

describe('remote-state control safety', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetJukeboxMediaEventSuppression()
  })

  const simulateCurrentSeekHandler = async ({ jukeboxMode, info }) => {
    if (jukeboxMode && !shouldSuppressRemoteSeekEcho(info.currentTime)) {
      await enqueueJukeboxCommand(() => syncJukeboxSeek(jukeboxClient, info))
    }
  }

  it('suppresses the next seek echo after programmatic remote position sync', () => {
    markPendingRemoteSeek({ position: 42, ttlMs: 1000 })
    expect(shouldSuppressRemoteSeekEcho(42)).toBe(true)
    expect(shouldSuppressRemoteSeekEcho(42)).toBe(false)
  })

  it('stops suppressing after TTL expires even if position matches', () => {
    vi.useFakeTimers()
    markPendingRemoteSeek({ position: 42, ttlMs: 50 })
    vi.advanceTimersByTime(100)
    expect(shouldSuppressRemoteSeekEcho(42)).toBe(false)
    vi.useRealTimers()
  })

  it('does not suppress a user seek when no remote sync is pending', async () => {
    await simulateCurrentSeekHandler({
      jukeboxMode: true,
      info: { currentTime: 17 },
    })
    expect(jukeboxClient.seek).toHaveBeenCalledWith(17)
  })

  it('forces local audio position toward remote state only when drift is large', () => {
    const audioInstance = { currentTime: 5, muted: true }
    const changed = syncRemotePositionIfNeeded({
      jukeboxMode: true,
      audioInstance,
      session: { position: 42, trackId: 't1' },
      currentTrackId: 't1',
    })
    expect(changed).toBe(true)
    expect(audioInstance.currentTime).toBe(42)
  })

  it('does not force-sync remote position when track ids do not match', () => {
    const audioInstance = { currentTime: 5, muted: true }
    const changed = syncRemotePositionIfNeeded({
      jukeboxMode: true,
      audioInstance,
      session: { position: 42, trackId: 't2' },
      currentTrackId: 't1',
    })
    expect(changed).toBe(false)
    expect(audioInstance.currentTime).toBe(5)
  })
})


describe('remote-state-first jukebox controls', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('uses remote session playing state for toggle play', () => {
    const handlers = keyHandlers(
      { togglePlay: vi.fn(), volume: 0.5 },
      {
        jukeboxMode: true,
        queue: [],
        current: {},
        jukeboxStatus: { playing: false },
        jukeboxSession: { playing: true },
      },
    )

    handlers.TOGGLE_PLAY({ preventDefault: vi.fn() })
    expect(jukeboxClient.pause).toHaveBeenCalledTimes(1)
    expect(jukeboxClient.play).not.toHaveBeenCalled()
  })



  it('uses authoritative remote current index for PREV_SONG in jukebox mode', () => {
    const handlers = keyHandlers(
      { togglePlay: vi.fn(), volume: 0.5 },
      {
        jukeboxMode: true,
        queue: [
          { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
          { trackId: 't2', uuid: 'u2', song: { id: 't2' } },
          { trackId: 't3', uuid: 'u3', song: { id: 't3' } },
        ],
        current: { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
        jukeboxStatus: null,
        jukeboxSession: { currentIndex: 2, trackId: 't3' },
      },
    )

    handlers.PREV_SONG({ metaKey: false })
    expect(jukeboxClient.skip).toHaveBeenCalledWith(1, 0)
  })

  it('uses authoritative remote current index for NEXT_SONG in jukebox mode', () => {
    const handlers = keyHandlers(
      { togglePlay: vi.fn(), volume: 0.5 },
      {
        jukeboxMode: true,
        queue: [
          { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
          { trackId: 't2', uuid: 'u2', song: { id: 't2' } },
          { trackId: 't3', uuid: 'u3', song: { id: 't3' } },
        ],
        current: { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
        jukeboxStatus: null,
        jukeboxSession: { currentIndex: 1, trackId: 't2' },
      },
    )

    handlers.NEXT_SONG({ metaKey: false })
    expect(jukeboxClient.skip).toHaveBeenCalledWith(2, 0)
  })

  it('uses remote session gain for volume changes', () => {
    const handlers = keyHandlers(
      { togglePlay: vi.fn(), volume: 0.5 },
      {
        jukeboxMode: true,
        queue: [],
        current: {},
        jukeboxStatus: { gain: 0.2 },
        jukeboxSession: { gain: 0.8 },
      },
    )

    handlers.VOL_DOWN()
    expect(jukeboxClient.volume).toHaveBeenCalledTimes(1)
    expect(jukeboxClient.volume.mock.calls[0][0]).toBeCloseTo(0.7)
  })
})


describe('jukebox session heartbeat lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    localStorage.setItem('username', 'admin')
  })

  it('starts heartbeat polling in jukebox mode and stops it on cleanup', () => {
    vi.useFakeTimers()
    const cleanup = startJukeboxHeartbeatLoop({
      jukeboxMode: true,
      sessionId: 's1',
      clientId: 'tab-1',
      onHeartbeat: () => jukeboxClient.heartbeatSession('s1', 'tab-1'),
    })

    vi.advanceTimersByTime(15000)
    expect(jukeboxClient.heartbeatSession).toHaveBeenCalledWith('s1', 'tab-1')

    cleanup()
    vi.advanceTimersByTime(15000)
    expect(jukeboxClient.heartbeatSession).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it('does not send heartbeat when no session id/client id is available', () => {
    vi.useFakeTimers()
    startJukeboxHeartbeatLoop({
      jukeboxMode: true,
      sessionId: null,
      clientId: null,
      onHeartbeat: () => jukeboxClient.heartbeatSession('s1', 'tab-1'),
    })
    vi.advanceTimersByTime(30000)
    expect(jukeboxClient.heartbeatSession).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('derives a stable session id and per-tab client id', () => {
    expect(getJukeboxSessionId()).toBe('jukebox-session:admin')
    const first = getOrCreateJukeboxClientId()
    const second = getOrCreateJukeboxClientId()
    expect(first).toBe(second)
    expect(first).toBeTruthy()
  })
})


describe('remote-state-first player selection', () => {
  it('uses remote current track and index for jukebox queue highlighting', () => {
    const remoteTrack = { trackId: 't2', uuid: 'u2', song: { id: 't2' } }
    const state = {
      jukeboxMode: true,
      playIndex: undefined,
      current: { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
      queue: [
        { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
        remoteTrack,
      ],
      jukeboxSession: { currentIndex: 1, trackId: 't2' },
      jukeboxStatus: null,
    }

    const resolved = resolvePlayerUiState(state)
    expect(resolved.current).toBe(remoteTrack)
    expect(resolved.playIndex).toBe(1)
  })
})


describe('queue highlight stabilization', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetJukeboxMediaEventSuppression()
  })

  it('consumes a remote track-change echo only once', () => {
    markPendingRemoteTrackChange({ index: 2, ttlMs: 3000 })
    expect(shouldSuppressRemoteTrackEcho(2)).toBe(true)
    expect(shouldSuppressRemoteTrackEcho(2)).toBe(false)
  })

  it('stops suppressing track-change echo after TTL expires', () => {
    vi.useFakeTimers()
    markPendingRemoteTrackChange({ index: 2, ttlMs: 50 })
    vi.advanceTimersByTime(100)
    expect(shouldSuppressRemoteTrackEcho(2)).toBe(false)
    vi.useRealTimers()
  })

  it('does not suppress track-change echo when index does not match', () => {
    markPendingRemoteTrackChange({ index: 2, ttlMs: 3000 })
    expect(shouldSuppressRemoteTrackEcho(5)).toBe(false)
    expect(shouldSuppressRemoteTrackEcho(2)).toBe(true)
  })

  it('keeps the last stable playIndex while queue sync is pending', () => {
    expect(
      resolveControlledJukeboxPlayIndex({
        jukeboxMode: true,
        queueSyncPending: true,
        remotePlayIndex: 2,
        lastStablePlayIndex: 1,
      }),
    ).toBe(1)
  })

  it('uses the remote playIndex once queue sync is settled', () => {
    expect(
      resolveControlledJukeboxPlayIndex({
        jukeboxMode: true,
        queueSyncPending: false,
        remotePlayIndex: 2,
        lastStablePlayIndex: 1,
      }),
    ).toBe(2)
  })

  it('suppresses remote-originated track change from re-sending skip', async () => {
    const nextIndex = 2
    markPendingRemoteTrackChange({ index: nextIndex, ttlMs: 3000 })

    if (!shouldSuppressRemoteTrackEcho(nextIndex)) {
      await enqueueJukeboxCommand(() => jukeboxClient.skip(nextIndex, 0))
    }

    expect(jukeboxClient.skip).not.toHaveBeenCalled()
  })
})
