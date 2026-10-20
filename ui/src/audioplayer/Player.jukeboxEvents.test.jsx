/* global globalThis */

import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import React from 'react'
import { act, render, waitFor } from '@testing-library/react'
import { Provider } from 'react-redux'
import { combineReducers, createStore } from 'redux'
import { playerReducer } from '../reducers/playerReducer'
import { setJukeboxMode, updateJukeboxSessionStatus } from '../actions'

vi.mock('react-admin', async (importOriginal) => ({
  ...(await importOriginal()),
  useAuthState: () => ({ authenticated: true }),
  useTranslate: () => (key) => key,
  useDataProvider: () => ({
    getOne: vi.fn(() => Promise.resolve({ data: {} })),
  }),
}))

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
    sessionStatus: vi.fn(() => Promise.resolve({ sessionId: 's1' })),
    detachSession: vi.fn(() => Promise.resolve({})),
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
  Player,
  applyOptimisticUserSkip,
  canControlJukebox,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  resolveControlledJukeboxPlayIndex,
  resolvePlayerUiState,
  runJukeboxHeartbeat,
  shouldConsumePendingRemoteTrackChange,
  startJukeboxHeartbeatLoop,
  syncRemotePositionIfNeeded,
  refreshJukeboxSessionStatus,
  detachJukeboxSession,
} from './Player'
import keyHandlers from './keyHandlers'
import { remoteGainToUiVolume, uiVolumeToRemoteGain } from './volumeProfiles'

const buildJukeboxState = (overrides = {}) => ({
  jukeboxMode: true,
  playIndex: undefined,
  current: { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
  queue: [
    { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
    { trackId: 't2', uuid: 'u2', song: { id: 't2' } },
    { trackId: 't3', uuid: 'u3', song: { id: 't3' } },
  ],
  jukeboxControl: { sessionId: 's1', ownershipState: 'attached' },
  jukeboxRemote: {
    currentIndex: 1,
    trackId: 't2',
    playing: true,
    position: 41,
  },
  ...overrides,
})

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
        jukeboxControl: { ownershipState: 'attached' },
        jukeboxRemote: { playing: true },
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
        jukeboxControl: { ownershipState: 'attached' },
        jukeboxRemote: { currentIndex: 2, trackId: 't3' },
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
        jukeboxControl: { ownershipState: 'attached' },
        jukeboxRemote: { currentIndex: 1, trackId: 't2' },
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
        jukeboxControl: { ownershipState: 'attached' },
        jukeboxRemote: { gain: 0.8 },
      },
    )

    handlers.VOL_DOWN()
    expect(jukeboxClient.volume).toHaveBeenCalledTimes(1)
    expect(jukeboxClient.volume.mock.calls[0][0]).toBeCloseTo(0.7)
  })

  it('uses the Bluetooth profile for remote keyboard volume changes', () => {
    const deviceName = 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink'
    const handlers = keyHandlers(
      { togglePlay: vi.fn(), volume: 0.5 },
      {
        jukeboxMode: true,
        queue: [],
        current: {},
        jukeboxDevice: 'Bluetooth 24:C4:06:FA:00:37',
        jukeboxControl: { ownershipState: 'attached' },
        jukeboxRemote: { gain: 0.8, deviceName },
      },
    )

    handlers.VOL_DOWN()
    expect(jukeboxClient.volume).toHaveBeenCalledTimes(1)
    expect(jukeboxClient.volume.mock.calls[0][0]).toBeCloseTo(
      uiVolumeToRemoteGain(remoteGainToUiVolume(0.8, { deviceName }) - 0.1, {
        deviceName,
      }),
      5,
    )
  })

  it('does not send remote key-handler commands while ownership is recovering', () => {
    const state = buildJukeboxState({
      jukeboxControl: { sessionId: 's1', ownershipState: 'recovering' },
    })

    expect(canControlJukebox(state)).toBe(false)

    const handlers = keyHandlers({ togglePlay: vi.fn(), volume: 0.5 }, state)

    handlers.TOGGLE_PLAY({ preventDefault: vi.fn() })
    handlers.NEXT_SONG({ metaKey: false })
    handlers.VOL_DOWN()

    expect(jukeboxClient.play).not.toHaveBeenCalled()
    expect(jukeboxClient.pause).not.toHaveBeenCalled()
    expect(jukeboxClient.skip).not.toHaveBeenCalled()
    expect(jukeboxClient.volume).not.toHaveBeenCalled()
  })

  it('does not send a stale rewind after control is taken over', async () => {
    const dispatch = vi.fn()
    jukeboxClient.heartbeatSession.mockRejectedValueOnce({ status: 403 })

    await runJukeboxHeartbeat({
      client: jukeboxClient,
      sessionId: 's1',
      clientId: 'tab-1',
      dispatch,
    })

    const state = buildJukeboxState({
      jukeboxControl: { sessionId: 's1', ownershipState: 'taken_over' },
      jukeboxRemote: {
        currentIndex: 2,
        trackId: 't3',
        playing: true,
        position: 41,
      },
    })
    const handlers = keyHandlers({ togglePlay: vi.fn(), volume: 0.5 }, state)

    handlers.PREV_SONG({ metaKey: false })

    expect(jukeboxClient.skip).not.toHaveBeenCalled()
  })
})

describe('unified controller hotkey routing (Issue 05, TC-IN-03)', () => {
  const playerState = {
    jukeboxMode: false,
    queue: [],
    current: {},
  }

  function makeController(status = 'paused', volume = 0.5) {
    return {
      getState: vi.fn(() => ({ status, volume, position: 0, duration: 0 })),
      play: vi.fn(() => Promise.resolve()),
      pause: vi.fn(() => Promise.resolve()),
      setVolume: vi.fn(() => Promise.resolve()),
      skipNext: vi.fn(() => Promise.resolve()),
      skipPrev: vi.fn(() => Promise.resolve()),
    }
  }

  it('TC-IN-03: TOGGLE_PLAY routes through the controller instead of the audio instance', () => {
    const audioInstance = { togglePlay: vi.fn(), volume: 0.5 }
    const controller = makeController('paused')
    const handlers = keyHandlers(audioInstance, playerState, controller)

    handlers.TOGGLE_PLAY({ preventDefault: vi.fn() })
    expect(controller.play).toHaveBeenCalledTimes(1)
    expect(controller.pause).not.toHaveBeenCalled()
    expect(audioInstance.togglePlay).not.toHaveBeenCalled()

    const playingController = makeController('playing')
    keyHandlers(audioInstance, playerState, playingController).TOGGLE_PLAY({
      preventDefault: vi.fn(),
    })
    expect(playingController.pause).toHaveBeenCalledTimes(1)
    expect(playingController.play).not.toHaveBeenCalled()
    expect(audioInstance.togglePlay).not.toHaveBeenCalled()
  })

  it('TC-IN-03: VOL_UP and VOL_DOWN step the authoritative volume instead of mutating the element', () => {
    const audioInstance = { togglePlay: vi.fn(), volume: 0.5 }
    const controller = makeController('playing', 0.5)
    const handlers = keyHandlers(audioInstance, playerState, controller)

    handlers.VOL_UP()
    expect(controller.setVolume).toHaveBeenLastCalledWith(0.6)
    handlers.VOL_DOWN()
    handlers.VOL_DOWN()
    expect(controller.setVolume).toHaveBeenLastCalledWith(0.4)
    expect(controller.setVolume).toHaveBeenCalledTimes(3)
    expect(audioInstance.volume).toBe(0.5)

    const clamped = makeController('playing', 0.97)
    keyHandlers(audioInstance, playerState, clamped).VOL_UP()
    expect(clamped.setVolume).toHaveBeenLastCalledWith(1)
  })

  it('TC-IN-03: NEXT_SONG and PREV_SONG route skips through the controller, keeping the metaKey guard', () => {
    const audioInstance = {
      togglePlay: vi.fn(),
      playNext: vi.fn(),
      playPrev: vi.fn(),
      volume: 0.5,
    }
    const controller = makeController()
    const handlers = keyHandlers(audioInstance, playerState, controller)

    handlers.NEXT_SONG({ metaKey: false })
    handlers.PREV_SONG({ metaKey: false })
    expect(controller.skipNext).toHaveBeenCalledTimes(1)
    expect(controller.skipPrev).toHaveBeenCalledTimes(1)
    expect(audioInstance.playNext).not.toHaveBeenCalled()
    expect(audioInstance.playPrev).not.toHaveBeenCalled()

    handlers.NEXT_SONG({ metaKey: true })
    handlers.PREV_SONG({ metaKey: true })
    expect(controller.skipNext).toHaveBeenCalledTimes(1)
    expect(controller.skipPrev).toHaveBeenCalledTimes(1)
  })

  it('TC-IN-03: without a controller the two-argument call keeps the existing host behavior', () => {
    const audioInstance = { togglePlay: vi.fn(), volume: 0.5 }
    const handlers = keyHandlers(audioInstance, playerState)

    handlers.TOGGLE_PLAY({ preventDefault: vi.fn() })
    expect(audioInstance.togglePlay).toHaveBeenCalledTimes(1)

    handlers.VOL_UP()
    expect(audioInstance.volume).toBeCloseTo(0.6)
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

  it('moves control state to recovering on heartbeat transport failure', async () => {
    const dispatch = vi.fn()
    jukeboxClient.heartbeatSession.mockRejectedValueOnce(new Error('offline'))

    await runJukeboxHeartbeat({
      client: jukeboxClient,
      sessionId: 's1',
      clientId: 'tab-1',
      dispatch,
    })

    expect(dispatch).toHaveBeenCalledWith({
      type: 'jukeboxStateUpdated',
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'recovering',
        terminationReason: null,
      },
    })
  })

  it('transitions to taken_over on heartbeat ownership conflict', async () => {
    const dispatch = vi.fn()
    jukeboxClient.heartbeatSession.mockRejectedValueOnce({ status: 403 })

    await runJukeboxHeartbeat({
      client: jukeboxClient,
      sessionId: 's1',
      clientId: 'tab-1',
      dispatch,
    })

    expect(dispatch).toHaveBeenCalledWith({
      type: 'jukeboxStateUpdated',
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'taken_over',
        terminationReason: 'taken_over',
      },
    })
  })

  it('re-attaches on heartbeat 404 using the last known remote device', async () => {
    const dispatch = vi.fn()
    jukeboxClient.heartbeatSession.mockRejectedValueOnce({ status: 404 })
    jukeboxClient.attachSession.mockResolvedValueOnce({
      sessionId: 's1',
      ownerClientId: 'tab-1',
      ownershipState: 'attached',
      deviceName: 'pulse/test',
    })

    await runJukeboxHeartbeat({
      client: jukeboxClient,
      sessionId: 's1',
      clientId: 'tab-1',
      deviceName: 'pulse/test',
      dispatch,
    })

    expect(jukeboxClient.attachSession).toHaveBeenCalledWith(
      's1',
      'tab-1',
      'pulse/test',
    )
    expect(dispatch).toHaveBeenCalledWith({
      type: 'jukeboxStateUpdated',
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'attached',
        deviceName: 'pulse/test',
      },
    })
  })

  it('returns to attached when a heartbeat succeeds after recovery', async () => {
    const dispatch = vi.fn()
    jukeboxClient.heartbeatSession.mockResolvedValueOnce({
      sessionId: 's1',
      ownerClientId: 'tab-1',
      ownershipState: 'attached',
      currentIndex: 2,
      trackId: 't3',
    })

    await runJukeboxHeartbeat({
      client: jukeboxClient,
      sessionId: 's1',
      clientId: 'tab-1',
      dispatch,
    })

    expect(dispatch).toHaveBeenCalledWith({
      type: 'jukeboxStateUpdated',
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'attached',
        currentIndex: 2,
        trackId: 't3',
      },
    })
  })
})

describe('remote-state-first player selection', () => {
  it('uses remote current track and index for jukebox queue highlighting', () => {
    const remoteTrack = { trackId: 't2', uuid: 'u2', song: { id: 't2' } }
    const state = buildJukeboxState({
      queue: [{ trackId: 't1', uuid: 'u1', song: { id: 't1' } }, remoteTrack],
      jukeboxControl: { sessionId: 's1', ownershipState: 'recovering' },
      jukeboxRemote: {
        currentIndex: 1,
        trackId: 't2',
        playing: true,
        position: 41,
      },
    })

    const resolved = resolvePlayerUiState(state)
    expect(resolved.current).toBe(remoteTrack)
    expect(resolved.playIndex).toBe(1)
  })

  it('keeps remote authority after a reconnect gap while control is recovering', () => {
    const state = buildJukeboxState({
      current: { trackId: 't2', uuid: 'u2', song: { id: 't2' } },
      jukeboxControl: { sessionId: 's1', ownershipState: 'recovering' },
      jukeboxRemote: {
        currentIndex: 2,
        trackId: 't3',
        playing: true,
        position: 3,
      },
    })

    const resolved = resolvePlayerUiState(state)
    expect(resolved.current?.trackId).toBe('t3')
    expect(resolved.playIndex).toBe(2)
  })

  it('does not send next-song skip after remote advance during a reconnect gap', () => {
    const state = buildJukeboxState({
      current: { trackId: 't2', uuid: 'u2', song: { id: 't2' } },
      jukeboxControl: { sessionId: 's1', ownershipState: 'recovering' },
      jukeboxRemote: {
        currentIndex: 2,
        trackId: 't3',
        playing: true,
        position: 3,
      },
    })
    const handlers = keyHandlers({ togglePlay: vi.fn(), volume: 0.5 }, state)

    handlers.NEXT_SONG({ metaKey: false })

    expect(jukeboxClient.skip).not.toHaveBeenCalled()
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

describe('optimistic user skip', () => {
  it('holds the user-selected index while remote state is stale', () => {
    const pendingUserSkipRef = {
      current: { index: 2, expiresAt: Date.now() + 5000 },
    }

    const controlledPlayIndex = resolveControlledJukeboxPlayIndex({
      jukeboxMode: true,
      queueSyncPending: false,
      remotePlayIndex: 0,
      lastStablePlayIndex: 2,
    })

    expect(
      applyOptimisticUserSkip({
        controlledPlayIndex,
        pendingUserSkipRef,
        remoteCurrentIndex: 0,
      }),
    ).toBe(2)
    expect(pendingUserSkipRef.current).not.toBeNull()
  })

  it('clears the optimistic ref once the remote state confirms it', () => {
    const pendingUserSkipRef = {
      current: { index: 2, expiresAt: Date.now() + 5000 },
    }

    expect(
      applyOptimisticUserSkip({
        controlledPlayIndex: 2,
        pendingUserSkipRef,
        remoteCurrentIndex: 2,
      }),
    ).toBe(2)
    expect(pendingUserSkipRef.current).toBeNull()
  })

  it('expires the optimistic ref after its TTL', () => {
    vi.useFakeTimers()

    const pendingUserSkipRef = {
      current: { index: 2, expiresAt: Date.now() + 50 },
    }

    vi.advanceTimersByTime(100)

    expect(
      applyOptimisticUserSkip({
        controlledPlayIndex: 0,
        pendingUserSkipRef,
        remoteCurrentIndex: 0,
      }),
    ).toBe(0)
    expect(pendingUserSkipRef.current).toBeNull()

    vi.useRealTimers()
  })

  it('covers user click to remote confirmation without reverting', () => {
    const pendingUserSkipRef = { current: null }
    const lastStablePlayIndexRef = { current: 0 }
    const pendingRemoteTrackChangeRef = { current: null }
    const nextIndex = 2

    expect(
      shouldConsumePendingRemoteTrackChange({
        nextIndex,
        pendingRemoteTrackChangeRef,
      }),
    ).toBe(false)

    pendingUserSkipRef.current = {
      index: nextIndex,
      expiresAt: Date.now() + 5000,
    }
    lastStablePlayIndexRef.current = nextIndex

    const staleControlledPlayIndex = resolveControlledJukeboxPlayIndex({
      jukeboxMode: true,
      queueSyncPending: false,
      remotePlayIndex: 0,
      lastStablePlayIndex: lastStablePlayIndexRef.current,
    })

    expect(
      applyOptimisticUserSkip({
        controlledPlayIndex: staleControlledPlayIndex,
        pendingUserSkipRef,
        remoteCurrentIndex: 0,
      }),
    ).toBe(2)

    const confirmedPlayIndex = resolveControlledJukeboxPlayIndex({
      jukeboxMode: true,
      queueSyncPending: false,
      remotePlayIndex: 2,
      lastStablePlayIndex: lastStablePlayIndexRef.current,
    })

    expect(
      applyOptimisticUserSkip({
        controlledPlayIndex: confirmedPlayIndex,
        pendingUserSkipRef,
        remoteCurrentIndex: 2,
      }),
    ).toBe(2)
    expect(pendingUserSkipRef.current).toBeNull()
  })
})

describe('jukebox session authority refresh helpers', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('refreshes session status after reconnect', async () => {
    const dispatch = vi.fn()

    await refreshJukeboxSessionStatus({
      jukeboxMode: true,
      sessionId: 's1',
      client: jukeboxClient,
      dispatch,
    })

    expect(jukeboxClient.sessionStatus).toHaveBeenCalledWith('s1')
    expect(dispatch).toHaveBeenCalledWith({
      type: 'jukeboxStateUpdated',
      data: { sessionId: 's1' },
    })
  })

  it('detaches the session on jukebox exit cleanup', async () => {
    await detachJukeboxSession({
      sessionId: 's1',
      clientId: 'tab-1',
      client: jukeboxClient,
    })

    expect(jukeboxClient.detachSession).toHaveBeenCalledWith('s1', 'tab-1')
  })
})

describe('fresh-document control identity', () => {
  it('does not adopt a copied storage identity and creates a fresh identity for a new document module', async () => {
    sessionStorage.setItem('jukebox-client-id', 'copied-owner-from-opener')
    vi.resetModules()
    const firstDocument = await import('./jukeboxSession')
    const first = firstDocument.getOrCreateJukeboxClientId()
    expect(first).not.toBe('copied-owner-from-opener')
    expect(firstDocument.getOrCreateJukeboxClientId()).toBe(first)
    expect(sessionStorage.getItem('jukebox-client-id')).toBe(first)
    vi.resetModules()
    const nextDocument = await import('./jukeboxSession')
    const next = nextDocument.getOrCreateJukeboxClientId()
    expect(next).not.toBe(first)
    expect(nextDocument.getOrCreateJukeboxClientId()).toBe(next)
    expect(sessionStorage.getItem('jukebox-client-id')).toBe(next)
    sessionStorage.clear()
  })
})

describe('mounted Player control-session episodes', () => {
  const originalDevice = 'pulse/original'
  const deferred = () => {
    let resolve
    let reject
    const promise = new Promise((complete, fail) => {
      resolve = complete
      reject = fail
    })
    return { promise, resolve, reject }
  }
  const status = (deviceName = originalDevice) => ({
    sessionId: 'jukebox-session:admin',
    ownerClientId: getOrCreateJukeboxClientId(),
    ownershipState: 'attached',
    deviceName,
    playing: false,
    currentIndex: -1,
    position: 0,
    gain: 0.25,
  })
  const renderPlayer = () => {
    const store = createStore(
      combineReducers({
        player: playerReducer,
        activity: () => ({ streamReconnected: false }),
        settings: () => ({ notifications: false }),
        replayGain: () => ({ gainMode: 'none' }),
        theme: () => 'DarkTheme',
      }),
    )
    store.dispatch(setJukeboxMode(true, 'Validation output'))
    store.dispatch(updateJukeboxSessionStatus(status()))
    vi.spyOn(store, 'dispatch')
    const view = render(
      <Provider store={store}>
        <Player />
      </Provider>,
    )
    return { ...view, store }
  }

  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.setItem('username', 'admin')
    sessionStorage.clear()
    jukeboxClient.attachSession.mockImplementation(() =>
      Promise.resolve(status()),
    )
    jukeboxClient.heartbeatSession.mockImplementation(() =>
      Promise.resolve(status()),
    )
    jukeboxClient.detachSession.mockImplementation(() => Promise.resolve({}))
  })
  afterEach(() => {
    vi.useRealTimers()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('does not detach or attach when only the authoritative output device migrates', async () => {
    const { store, unmount } = renderPlayer()
    await waitFor(() =>
      expect(jukeboxClient.attachSession).toHaveBeenCalledTimes(1),
    )
    await act(async () =>
      store.dispatch(updateJukeboxSessionStatus(status('auto'))),
    )
    expect(jukeboxClient.detachSession).not.toHaveBeenCalled()
    expect(jukeboxClient.attachSession).toHaveBeenCalledTimes(1)
    await act(async () => store.dispatch(setJukeboxMode(false)))
    await waitFor(() =>
      expect(jukeboxClient.detachSession).toHaveBeenCalledTimes(1),
    )
    unmount()
  })

  it('retires a canceled pending attachment only after it settles without dispatching stale state', async () => {
    const pending = deferred()
    jukeboxClient.attachSession.mockReturnValueOnce(pending.promise)
    const { store, unmount } = renderPlayer()
    await waitFor(() =>
      expect(jukeboxClient.attachSession).toHaveBeenCalledTimes(1),
    )
    const dispatch = store.dispatch
    dispatch.mockClear()
    unmount()
    try {
      expect(jukeboxClient.detachSession).not.toHaveBeenCalled()
    } finally {
      await act(async () => pending.resolve(status()))
    }
    await waitFor(() =>
      expect(jukeboxClient.detachSession).toHaveBeenCalledTimes(1),
    )
    expect(dispatch).not.toHaveBeenCalled()
  })

  it('waits for an already-started heartbeat reattachment and retires its late result without stale state', async () => {
    const pending = deferred()
    vi.useFakeTimers()
    const { store, unmount } = renderPlayer()
    await act(async () => vi.advanceTimersByTimeAsync(0))
    jukeboxClient.heartbeatSession.mockRejectedValueOnce(
      Object.assign(new Error('Missing'), { status: 404 }),
    )
    jukeboxClient.attachSession.mockReturnValueOnce(pending.promise)
    await act(async () => vi.advanceTimersByTimeAsync(15000))
    expect(jukeboxClient.attachSession).toHaveBeenCalledTimes(2)
    await act(async () => store.dispatch(setJukeboxMode(false)))
    expect(jukeboxClient.detachSession).not.toHaveBeenCalled()
    store.dispatch.mockClear()
    const late = status()
    await act(async () => pending.resolve(late))
    expect(jukeboxClient.detachSession).toHaveBeenCalledTimes(1)
    expect(jukeboxClient.detachSession).toHaveBeenCalledWith(
      late.sessionId,
      late.ownerClientId,
      { attachment: late },
    )
    expect(store.dispatch).not.toHaveBeenCalled()
    vi.useRealTimers()
    unmount()
  })

  it('does not reattach after a canceled in-flight heartbeat reports a missing session', async () => {
    const pending = deferred()
    vi.useFakeTimers()
    const { store, unmount } = renderPlayer()
    await act(async () => vi.advanceTimersByTimeAsync(0))
    expect(jukeboxClient.attachSession).toHaveBeenCalledTimes(1)
    jukeboxClient.heartbeatSession.mockReturnValueOnce(pending.promise)
    await act(async () => vi.advanceTimersByTimeAsync(15000))
    expect(jukeboxClient.heartbeatSession).toHaveBeenCalledTimes(1)
    await act(async () => store.dispatch(setJukeboxMode(false)))
    await act(async () =>
      pending.reject(Object.assign(new Error('Missing'), { status: 404 })),
    )
    expect(jukeboxClient.attachSession).toHaveBeenCalledTimes(1)
    expect(store.getState().player.jukeboxMode).toBe(false)
    vi.useRealTimers()
    unmount()
  })
})
