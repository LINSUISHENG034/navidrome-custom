/**
 * Dual-Engine Unified Native Player Control Loop Test Suite
 *
 * Implements verification for Issue 02 / Spec §共同案例与证据记录
 *
 * Case Index:
 * - TC-DE-01 (US-1, US-2): Zero Browser Audio Engine Lifecycle Detection (Remote Mode)
 * - TC-DE-02 (US-3, US-20): Common Operations on BrowserAudioAdapter (Local Mode)
 * - TC-DE-03 (US-3, US-20): Common Operations on MockRemoteAudioAdapter (Remote Mode)
 * - TC-DE-04 (US-4, US-21): Single-Intent Guarantee and Zero-Command State Backfill
 * - TC-DE-05 (US-15, US-23): Engine Switching, Resource Cleanup, and Epoch Guard Invalidation
 * - TC-DE-06 (US-21, US-22): Remote Rejection, Permission Loss, and Timeout Failure Surfacing
 * - TC-DE-07 (US-1, US-24): Dynamic UI-Level Engine Switching (Local -> Remote)
 * - TC-DE-08 (US-1, US-3): Native DOM Button Interactions via Unified Control Loop
 * - TC-DE-09 (US-24, US-25): Backward Compatibility with Default Native Baseline
 * - TC-IN-01 (US-1, US-4, Issue 05): Shortcut Input Consistency and Single-Issue Guarantee
 * - TC-IN-02 (US-1, US-4, Issue 05): Media Session Action Delegation, Authority-Driven Metadata
 * - TC-DSP-01 (US-3, Issue 05): Authoritative State Drives Lyric Time Sync Without Audio Events
 */

import React from 'react'
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import {
  render,
  act,
  waitFor,
  fireEvent,
  cleanup,
} from '@testing-library/react'

vi.mock('sortablejs', async () => {
  function Swap() {}
  const Sortable = vi.fn(function (element, options) {
    this.el = element
    this.options = options
    this.destroy = vi.fn()
    this.option = vi.fn((name, value) => {
      if (value !== undefined) this.options[name] = value
      return this.options[name]
    })
  })
  Sortable.mount = vi.fn()
  Sortable.Swap = Swap
  return {
    default: Sortable,
    Swap,
  }
})

import {
  createPlaybackControlModule,
  PlaybackStatus,
  PlaybackMode,
  CommandResultStatus,
  normalizePlaybackMode,
  getNextTrack,
  getPrevTrack,
} from './playbackControlModule'
import { createBrowserAudioAdapter } from './browserAudioAdapter'
import { createMockRemoteAudioAdapter } from './mockRemoteAudioAdapter'
import { UnifiedNativePlayer } from './UnifiedNativePlayer'
import {
  audioVolumeToRemoteGain,
  remoteGainToAudioVolume,
  HARDWARE_QUANTIZATION_TOLERANCE,
} from './volumeProfiles'
import { audioVolumeToUiVolume } from './volumeMapping'
import Sortable from 'sortablejs'

function getNativeVolumeBarValue() {
  return Number(
    document
      .querySelector('.sound-operation .rc-slider-handle')
      .getAttribute('aria-valuenow'),
  )
}

function setMediaFlags(audio, flags) {
  Object.entries(flags).forEach(([name, value]) => {
    Object.defineProperty(audio, name, { configurable: true, value })
  })
}

function finishMedia(audio) {
  setMediaFlags(audio, { paused: true, ended: true })
  audio.dispatchEvent(new Event('pause'))
  audio.dispatchEvent(new Event('ended'))
}

function getDrawerLabels() {
  return Array.from(document.querySelectorAll('.audio-item .player-name')).map(
    (node) => node.textContent,
  )
}

function startNativeDrag(oldIndex, newIndex) {
  const list = document.querySelector('.audio-lists-panel-content-wrap')
  const sortable = Sortable.mock.instances.findLast(
    (instance) => instance.el === list && !instance.destroy.mock.calls.length,
  )
  expect(sortable).toBeDefined()
  const nodes = Array.from(list.children)
  const event = {
    from: list,
    to: list,
    item: nodes[oldIndex],
    oldIndex,
    newIndex,
  }
  act(() => {
    sortable.options.onStart(event)
    const anchor =
      oldIndex < newIndex ? nodes[newIndex].nextSibling : nodes[newIndex]
    list.insertBefore(event.item, anchor)
  })
  return {
    event,
    sortable,
    end: (overrides = {}) =>
      act(async () => sortable.options.onEnd({ ...event, ...overrides })),
  }
}

describe('Dual-Engine Unified Native Player Control Loop (Issue 02)', () => {
  let playSpy
  let pauseSpy
  let createElementSpy
  let originalCreateElement

  const sampleTracks = [
    {
      name: 'Song 0',
      singer: 'Artist 0',
      musicSrc: 'http://localhost/song0.mp3',
      cover: 'http://localhost/cover0.jpg',
      id: 's-0',
    },
    {
      name: 'Song 1',
      singer: 'Artist 1',
      musicSrc: 'http://localhost/song1.mp3',
      cover: 'http://localhost/cover1.jpg',
      id: 's-1',
    },
  ]

  beforeEach(() => {
    window.matchMedia = vi.fn().mockImplementation((query) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }))

    playSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, 'play')
      .mockImplementation(function () {
        setMediaFlags(this, { paused: false, ended: false })
        return Promise.resolve()
      })
    pauseSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, 'pause')
      .mockImplementation(function () {
        setMediaFlags(this, { paused: true })
      })
    vi.spyOn(window.HTMLMediaElement.prototype, 'load').mockImplementation(
      () => {},
    )

    originalCreateElement = document.createElement.bind(document)
    createElementSpy = vi.spyOn(document, 'createElement')
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  describe('Zero Browser Audio Engine Lifecycle Detection (Remote Mode)', () => {
    /**
     * Case: TC-DE-01
     * Requirement: US-1, US-2, Issue 02 line 15
     * Initial Scene: Remote mode requested with 2 tracks
     * Input Intent: Mount player, play, seek, volume, pause, unmount
     * Expected State: Controller and native UI reflect changes
     * Forbidden Side-Effect: Exactly 0 <audio> tags, 0 AudioContext, 0 stream fetches
     */
    it('TC-DE-01: remote mount, interaction, and unmount create zero <audio> elements and zero AudioContext', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      const createdAudioTags = []
      createElementSpy.mockImplementation((tagName, options) => {
        const el = originalCreateElement(tagName, options)
        if (tagName.toLowerCase() === 'audio') {
          createdAudioTags.push(el)
        }
        return el
      })

      const audioConstructorSpy = vi.fn()
      window.Audio = class extends window.HTMLMediaElement {
        constructor() {
          super()
          audioConstructorSpy()
        }
      }

      window.AudioContext = vi.fn()
      window.webkitAudioContext = vi.fn()

      const { unmount } = render(
        <UnifiedNativePlayer
          controller={controller}
          engineMode="remote"
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
          duration={180}
        />,
      )

      await waitFor(() => {
        expect(
          document.querySelector('.react-jinke-music-player-main'),
        ).not.toBeNull()
      })

      // 1. Verify that during mount, ZERO <audio> elements were created
      expect(createdAudioTags).toHaveLength(0)
      expect(audioConstructorSpy).not.toHaveBeenCalled()
      expect(window.AudioContext).not.toHaveBeenCalled()
      expect(document.querySelector('audio')).toBeNull()

      // 2. Perform interactive operations (Play, Pause, Seek, Volume)
      await act(async () => {
        await controller.play()
      })
      await act(async () => {
        await controller.seek(45)
      })
      await act(async () => {
        await controller.setVolume(0.7)
      })
      await act(async () => {
        await controller.pause()
      })

      // Verify that throughout interactions, STILL ZERO <audio> elements were created
      expect(createdAudioTags).toHaveLength(0)
      expect(audioConstructorSpy).not.toHaveBeenCalled()
      expect(document.querySelector('audio')).toBeNull()

      // 3. Unmount and verify no cleanup errors or late engine instantiation
      unmount()
      expect(createdAudioTags).toHaveLength(0)
      expect(document.querySelector('audio')).toBeNull()
    })
  })

  describe('Common Operations across Both Engines', () => {
    /**
     * Case: TC-DE-02
     * Requirement: US-3, US-20, Issue 02 line 14
     * Initial Scene: Local mode with real audio element
     * Input Intent: play(), seek(60), setVolume(0.85), pause()
     * Expected State: State confirmed via DOM media events
     * Forbidden Side-Effect: State change without media confirmation
     */
    it('TC-DE-02: executes play, pause, seek, and volume on BrowserAudioAdapter (Local)', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)

      render(
        <UnifiedNativePlayer
          controller={controller}
          browserAdapter={browserAdapter}
          engineMode="local"
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
        />,
      )

      const audioEl = browserAdapter.getAudioElement()
      expect(audioEl).not.toBeNull()

      // Initial state
      expect(controller.getState().status).toBe(PlaybackStatus.IDLE)

      // 1. Play
      await act(async () => {
        const playPromise = controller.play()
        expect(controller.getState().status).toBe(PlaybackStatus.PREPARING)
        // Simulate browser audio firing play/playing event
        audioEl.dispatchEvent(new Event('playing'))
        await playPromise
      })
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(playSpy).toHaveBeenCalled()

      // 2. Seek
      await act(async () => {
        await controller.seek(60)
        audioEl.currentTime = 60
        audioEl.dispatchEvent(new Event('timeupdate'))
      })
      expect(controller.getState().position).toBe(60)

      // 3. Volume
      await act(async () => {
        await controller.setVolume(0.85)
        audioEl.volume = 0.85
        audioEl.dispatchEvent(new Event('volumechange'))
      })
      expect(controller.getState().volume).toBe(0.85)

      // 4. Pause
      await act(async () => {
        const pausePromise = controller.pause()
        audioEl.dispatchEvent(new Event('pause'))
        await pausePromise
      })
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(pauseSpy).toHaveBeenCalled()
    })

    /**
     * Case: TC-DE-03
     * Requirement: US-3, US-20, Issue 02 line 14
     * Initial Scene: Remote mode with deterministic mock adapter
     * Input Intent: play(), seek(75), setVolume(0.6), pause()
     * Expected State: Confirmed by authoritative state, native UI updated
     * Forbidden Side-Effect: Any audio element or facade created
     */
    it('TC-DE-03: executes play, pause, seek, and volume on MockRemoteAudioAdapter (Remote)', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter({
        initialDuration: 200,
      })
      controller.setAdapter(remoteAdapter)

      let playerRef = null
      render(
        <UnifiedNativePlayer
          ref={(r) => {
            playerRef = r
          }}
          controller={controller}
          engineMode="remote"
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
          duration={200}
        />,
      )

      await waitFor(() => {
        expect(playerRef).not.toBeNull()
      })

      // 1. Play intent
      await act(async () => {
        await controller.play()
      })
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(playerRef.state.playing).toBe(true)

      // 2. Seek intent
      await act(async () => {
        await controller.seek(75)
      })
      expect(controller.getState().position).toBe(75)
      expect(playerRef.state.currentTime).toBe(75)

      // 3. Volume intent
      await act(async () => {
        await controller.setVolume(0.6)
      })
      expect(controller.getState().volume).toBe(0.6)
      expect(getNativeVolumeBarValue()).toBeCloseTo(0.7745966692)

      // 4. Pause intent
      await act(async () => {
        await controller.pause()
      })
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(playerRef.state.playing).toBe(false)
    })
  })

  describe('Single-Intent Guarantee and Zero-Command State Backfill', () => {
    /**
     * Case: TC-DE-04
     * Requirement: US-4, US-21, Issue 02 line 17
     * Initial Scene: Remote player mounted, server pushes state update
     * Input Intent: onTogglePlay clicked once; server pushes position & volume
     * Expected State: UI updates; commandsSent counts remain unchanged
     * Forbidden Side-Effect: Re-issuing intent from state backfill (command echo)
     */
    it('TC-DE-04: user interaction issues intent exactly once; server state push updates UI with 0 commands sent', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      let playerRef = null
      render(
        <UnifiedNativePlayer
          ref={(r) => {
            playerRef = r
          }}
          controller={controller}
          engineMode="remote"
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
          duration={180}
        />,
      )

      await waitFor(() => {
        expect(playerRef).not.toBeNull()
      })

      // Trigger UI play click
      act(() => {
        playerRef.onTogglePlay()
      })

      const metricsBefore = controller.getMetrics()
      expect(metricsBefore.intentsIssued).toBe(1)
      expect(metricsBefore.commandsSent.play).toBe(1)
      expect(playerRef.state.playing).toBe(true)

      // Simulate asynchronous server push (state backfill) with natural progress and volume change
      act(() => {
        remoteAdapter.pushServerSnapshot({
          position: 92,
          volume: 0.4,
        })
      })

      // UI state should be updated via backfill
      expect(playerRef.state.currentTime).toBe(92)
      expect(getNativeVolumeBarValue()).toBeCloseTo(0.632455532)

      // CRITICAL ASSERTION: No new commands or intents issued due to state backfill!
      const metricsAfter = controller.getMetrics()
      expect(metricsAfter.intentsIssued).toBe(1)
      expect(metricsAfter.commandsSent.play).toBe(1)
      expect(metricsAfter.commandsSent.pause).toBe(0)
      expect(metricsAfter.commandsSent.seek).toBe(0)
      expect(metricsAfter.commandsSent.volume).toBe(0)
      expect(metricsAfter.stateBackfills).toBeGreaterThan(0)
    })
  })

  describe('Engine Switching and Late Callback Invalidation', () => {
    /**
     * Case: TC-DE-05
     * Requirement: US-15, US-23, Issue 02 line 15
     * Initial Scene: Local playback active; switch to remote adapter
     * Input Intent: setAdapter(remoteAdapter); late callback invoked with old epoch
     * Expected State: Local paused, resources cleared; late callback dropped
     * Forbidden Side-Effect: Local audio leaking into remote or stale state overriding new state
     */
    it('TC-DE-05: switching from local to remote stops local audio, releases resources, and drops stale callbacks', async () => {
      const controller = createPlaybackControlModule()
      let lateCallbackCaptor = null
      const mockLocalAdapter = {
        activate: vi.fn((epoch, cb) => {
          lateCallbackCaptor = { epoch, cb }
        }),
        deactivate: vi.fn(),
        play: vi.fn(),
        pause: vi.fn(),
      }

      controller.setAdapter(mockLocalAdapter)
      expect(mockLocalAdapter.activate).toHaveBeenCalled()

      // Switch to remote adapter -> increments epoch
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      // 1. Verify local adapter was deactivated with previous epoch
      expect(mockLocalAdapter.deactivate).toHaveBeenCalled()

      // 2. Simulate late asynchronous callback from old epoch
      act(() => {
        lateCallbackCaptor.cb(lateCallbackCaptor.epoch, {
          status: PlaybackStatus.PLAYING,
          position: 999,
        })
      })

      // The controller state must drop the stale epoch update
      expect(controller.getState().position).not.toBe(999)
    })
  })

  describe('Failure Surfacing, Timeouts & Non-swallowed Errors', () => {
    /**
     * Case: TC-DE-06
     * Requirement: US-21, US-22, Issue 02 line 18
     * Initial Scene: Remote engine with permission rejection or network timeout
     * Input Intent: play()
     * Expected State: Status transitions to ERROR, error surfaces to caller
     * Forbidden Side-Effect: Silently swallowing failure or reporting fake success
     */
    it('TC-DE-06: remote rejection/timeout sets error status and does not swallow failure', async () => {
      const controller = createPlaybackControlModule({ commandTimeoutMs: 150 })
      const remoteAdapter = createMockRemoteAudioAdapter()

      // Part A: Explicit Rejection / Permission Denied
      remoteAdapter.setFailure({
        action: 'play',
        code: 'PERMISSION_DENIED',
        message: 'Jukebox lease expired or active owner conflict',
      })
      controller.setAdapter(remoteAdapter)

      let caughtError = null
      await act(async () => {
        try {
          await controller.play()
        } catch (err) {
          caughtError = err
        }
      })

      expect(caughtError).not.toBeNull()
      expect(caughtError.code).toBe('PERMISSION_DENIED')
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)

      // Part B: Command Timeout Handling
      remoteAdapter.setFailure({
        action: 'pause',
        timeout: true,
        timeoutMs: 300,
      })

      let timeoutError = null
      await act(async () => {
        try {
          await controller.pause()
        } catch (err) {
          timeoutError = err
        }
      })

      expect(timeoutError).not.toBeNull()
      expect(timeoutError.code).toBe('COMMAND_TIMEOUT')
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
    })
  })

  describe('Dynamic UI-Level Engine Switching', () => {
    /**
     * Case: TC-DE-07
     * Requirement: US-1, US-14, US-15
     * Initial Scene: UnifiedNativePlayer rendered in local mode
     * Input Intent: Re-render with engineMode="remote"
     * Expected State: <audio> element is removed from DOM in remote mode
     * Forbidden Side-Effect: Lingering <audio> node or audio element recreated
     */
    it('TC-DE-07: dynamically switching component from local to remote strips <audio> element', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)

      const { rerender } = render(
        <UnifiedNativePlayer
          controller={controller}
          browserAdapter={browserAdapter}
          engineMode="local"
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
        />,
      )

      await waitFor(() => {
        expect(document.querySelector('audio')).not.toBeNull()
      })

      // Switch to remote mode
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      rerender(
        <UnifiedNativePlayer
          controller={controller}
          engineMode="remote"
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
          duration={180}
        />,
      )

      await waitFor(() => {
        expect(document.querySelector('audio')).toBeNull()
      })

      // Verify remote interaction works cleanly without audio tag
      await act(async () => {
        await controller.play()
      })
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(document.querySelector('audio')).toBeNull()
    })
  })

  async function renderControlledPlayer(engineMode = 'local', extraProps = {}) {
    const tracks = (extraProps.audioLists || sampleTracks).map(
      (track, index) => ({
        ...track,
        queueItemId: track.queueItemId || `controlled-${index}`,
      }),
    )
    const controller = createPlaybackControlModule()
    const browserAdapter =
      engineMode === 'local' ? createBrowserAudioAdapter() : undefined
    const adapter = browserAdapter || createMockRemoteAudioAdapter()
    controller.setAdapter(adapter)
    await controller.addQueueItems(tracks)
    const props = {
      controller,
      browserAdapter,
      engineMode,
      audioLists: tracks,
      mode: 'full',
      autoPlay: false,
      ...extraProps,
    }
    const view = render(<UnifiedNativePlayer {...props} />)
    return {
      controller,
      browserAdapter,
      adapter,
      tracks,
      ...view,
      rerenderPlayer: (overrides) =>
        view.rerender(<UnifiedNativePlayer {...props} {...overrides} />),
    }
  }

  describe('Controlled native inputs and lifecycle', () => {
    it('TC-QC-09: a failing custom drag-start hook cannot leave structural projection locked', async () => {
      const { controller } = await renderControlledPlayer('remote', {
        sortableOptions: {
          onStart: () => {
            throw new Error('Drag hook failed')
          },
        },
      })
      expect(() => startNativeDrag(0, 1)).toThrow('Drag hook failed')
      const initialIntents = controller.getMetrics().intentsIssued
      await act(async () => {
        await controller.addQueueItems([
          { queueItemId: 'after-hook', name: 'After hook' },
        ])
      })
      expect(getDrawerLabels()).toEqual(['Song 0', 'Song 1', 'After hook'])
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
      expect(controller.getMetrics().commandsSent.moveQueueItem).toBe(0)
    })

    it('TC-QC-09: nonadjacent dragging uses move semantics and composes native Sortable hooks', async () => {
      const onStart = vi.fn()
      const onEnd = vi.fn()
      const { controller } = await renderControlledPlayer('remote', {
        audioLists: [
          ...sampleTracks,
          { ...sampleTracks[0], id: 's-2', name: 'Song 2' },
        ],
        sortableOptions: { onStart, onEnd, swap: true },
      })
      const initialIntents = controller.getMetrics().intentsIssued
      const drag = startNativeDrag(0, 2)
      expect(drag.sortable.options.swap).toBe(false)
      await drag.end()

      expect(getDrawerLabels()).toEqual(['Song 1', 'Song 2', 'Song 0'])
      expect(
        controller.getState().queue.map((item) => item.queueItemId),
      ).toEqual(['controlled-1', 'controlled-2', 'controlled-0'])
      expect(controller.getState().currentQueueItemId).toBe('controlled-0')
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
      expect(onStart).toHaveBeenCalledTimes(1)
      expect(onEnd).toHaveBeenCalledTimes(1)
      expect(onStart.mock.contexts[0]).toBe(drag.sortable)
      expect(onEnd.mock.contexts[0]).toBe(drag.sortable)
    })

    it('TC-QC-09: unmounting during drag cleans the binding and ignores its late drop', async () => {
      const { controller, unmount } = await renderControlledPlayer('remote')
      const drag = startNativeDrag(0, 1)
      const initialIntents = controller.getMetrics().intentsIssued
      unmount()
      await drag.end()

      expect(drag.sortable.destroy).toHaveBeenCalledTimes(1)
      expect(
        document.querySelector('.audio-lists-panel-content-wrap'),
      ).toBeNull()
      expect(document.querySelector('audio')).toBeNull()
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents)
    })

    it.each([
      { reason: 'unchanged position', newIndex: 0 },
      { reason: 'missing target', newIndex: undefined },
    ])(
      'TC-QC-09: a canceled drag ($reason) releases its view without an intent',
      async ({ newIndex }) => {
        const { controller } = await renderControlledPlayer('remote')
        const initialIntents = controller.getMetrics().intentsIssued
        const canceled = startNativeDrag(0, 1)
        await canceled.end({ newIndex })
        expect(getDrawerLabels()).toEqual(['Song 0', 'Song 1'])
        expect(controller.getMetrics().intentsIssued).toBe(initialIntents)

        const next = startNativeDrag(0, 1)
        await next.end()
        expect(getDrawerLabels()).toEqual(['Song 1', 'Song 0'])
        expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
      },
    )

    it('TC-QC-09: a rejected move restores DOM and authority before the next gesture', async () => {
      const { controller, adapter, getByRole } =
        await renderControlledPlayer('remote')
      const initialQueue = controller.getState().queue
      const initialIntents = controller.getMetrics().intentsIssued
      adapter.setFailure({
        action: 'moveQueueItem',
        code: 'MOVE_REJECTED',
        message: 'Move rejected',
      })
      const rejected = startNativeDrag(0, 1)
      await rejected.end()

      expect(getByRole('alert').textContent).toBe('Move rejected')
      expect(getDrawerLabels()).toEqual(['Song 0', 'Song 1'])
      expect(controller.getState().queue).toEqual(initialQueue)
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)

      adapter.setFailure(null)
      const accepted = startNativeDrag(0, 1)
      await accepted.end()
      expect(getDrawerLabels()).toEqual(['Song 1', 'Song 0'])
      expect(controller.getState().error).toBeNull()
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 2)
    })

    it('TC-QC-09: a destroyed Sortable drop cannot finish a newer drag', async () => {
      const { controller, tracks } = await renderControlledPlayer('remote')
      const previous = startNativeDrag(0, 1)
      await act(async () => {
        controller.setAdapter(createMockRemoteAudioAdapter())
        await controller.addQueueItems(tracks)
      })
      const current = startNativeDrag(0, 1)
      const initialIntents = controller.getMetrics().intentsIssued

      await previous.end()
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents)
      expect(getDrawerLabels()).toEqual(['Song 1', 'Song 0'])

      await current.end()
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
      expect(getDrawerLabels()).toEqual(['Song 1', 'Song 0'])
    })

    it.each(['adapter', 'controller'])(
      'TC-QC-09: changing the %s during drag immediately projects the new scene',
      async (change) => {
        const { controller: original, rerenderPlayer } =
          await renderControlledPlayer('remote')
        const drag = startNativeDrag(0, 1)
        const controller =
          change === 'controller' ? createPlaybackControlModule() : original
        await act(async () => {
          controller.setAdapter(createMockRemoteAudioAdapter())
          await controller.addQueueItems([
            { queueItemId: 'new-scene', name: 'New scene' },
          ])
        })
        if (change === 'controller') rerenderPlayer({ controller })

        expect(getDrawerLabels()).toEqual(['New scene'])
        const initialIntents = controller.getMetrics().intentsIssued
        await drag.end()
        expect(getDrawerLabels()).toEqual(['New scene'])
        expect(controller.getMetrics().intentsIssued).toBe(initialIntents)
        expect(controller.getMetrics().commandsSent.moveQueueItem).toBe(0)
      },
    )

    it('TC-QC-09: losing control during drag cancels the view and ignores its later drop', async () => {
      const { controller, adapter, getByRole } =
        await renderControlledPlayer('remote')
      const drag = startNativeDrag(0, 1)
      const initialIntents = controller.getMetrics().intentsIssued
      await act(async () => {
        adapter.pushServerSnapshot({
          status: PlaybackStatus.ERROR,
          error: { code: 'PERMISSION_DENIED', message: 'Control lost' },
        })
      })

      expect(getByRole('alert').textContent).toBe('Control lost')
      expect(getDrawerLabels()).toEqual(['Song 0', 'Song 1'])
      await drag.end()
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents)
      expect(controller.getMetrics().commandsSent.moveQueueItem).toBe(0)

      await act(async () => {
        adapter.pushServerSnapshot({
          status: PlaybackStatus.PAUSED,
          error: null,
        })
      })
      const recovered = startNativeDrag(0, 1)
      await recovered.end()
      expect(getDrawerLabels()).toEqual(['Song 1', 'Song 0'])
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
    })

    it('TC-QC-09: structural updates during drag keep scalar authority live and cancel the stale move', async () => {
      const { controller, tracks } = await renderControlledPlayer('remote')
      const drag = startNativeDrag(0, 1)
      const initialIntents = controller.getMetrics().intentsIssued
      await act(async () => {
        await controller.removeQueueItem(tracks[0].queueItemId)
        await controller.setVolume(0.25)
        await controller.seek(45)
      })

      expect(
        controller.getState().queue.map((item) => item.queueItemId),
      ).toEqual([tracks[1].queueItemId])
      expect(getDrawerLabels()).toEqual(['Song 1', 'Song 0'])
      expect(getNativeVolumeBarValue()).toBeCloseTo(0.5)
      expect(document.querySelector('.current-time').textContent).toBe('00:45')

      await drag.end()
      expect(getDrawerLabels()).toEqual(['Song 1'])
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 3)
      expect(controller.getMetrics().commandsSent.moveQueueItem).toBe(0)
      await act(async () => {
        await controller.addQueueItems([
          { queueItemId: 'later', name: 'Later song' },
        ])
      })
      expect(getDrawerLabels()).toEqual(['Song 1', 'Later song'])
    })

    it('TC-DE-04: native track labels and highlighting follow queue authority without extra intents', async () => {
      const { controller } = await renderControlledPlayer('remote')
      expect(document.querySelector('.audio-title').textContent).toContain(
        'Song 0',
      )
      const initialIntents = controller.getMetrics().intentsIssued
      await act(async () => document.querySelectorAll('.audio-item')[1].click())
      expect(document.querySelector('.audio-title').textContent).toContain(
        'Song 1',
      )
      expect(
        document.querySelector('.audio-item.playing .player-name').textContent,
      ).toBe('Song 1')
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
    })

    it('TC-DE-07: local play rejection after confirmed remote pause is visible without fallback or retry', async () => {
      const { controller, tracks, rerenderPlayer, getByRole } =
        await renderControlledPlayer('remote')
      await act(async () => document.querySelector('.play-btn').click())
      await act(async () => document.querySelector('.play-btn').click())
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      const browserAdapter = createBrowserAudioAdapter()
      await act(async () => controller.setAdapter(browserAdapter))
      rerenderPlayer({ engineMode: 'local', browserAdapter })
      await act(async () => controller.addQueueItems(tracks))
      const epoch = controller.getEpoch()
      const initialIntents = controller.getMetrics().intentsIssued
      playSpy.mockRejectedValue(
        new DOMException('User gesture required', 'NotAllowedError'),
      )

      await act(async () => document.querySelector('.play-btn').click())

      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(getByRole('alert').textContent).toContain('User gesture required')
      expect(controller.getEpoch()).toBe(epoch)
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
      expect(playSpy).toHaveBeenCalledTimes(1)
    })

    it.each(['local', 'remote'])(
      'TC-DE-08: controlled %s mounting never resolves or loads media outside its Adapter',
      async (engineMode) => {
        const musicSrc = vi.fn(() =>
          Promise.resolve('http://localhost/resolved.mp3'),
        )
        const { browserAdapter } = await renderControlledPlayer(engineMode, {
          audioLists: [{ ...sampleTracks[0], musicSrc }],
        })
        expect(musicSrc).not.toHaveBeenCalled()
        if (browserAdapter) {
          expect(
            browserAdapter.getAudioElement().getAttribute('src'),
          ).toBeNull()
        }

        await act(async () => document.querySelector('.play-btn').click())
        if (browserAdapter) {
          expect(musicSrc).toHaveBeenCalledTimes(1)
          expect(browserAdapter.getAudioElement().src).toBe(
            'http://localhost/resolved.mp3',
          )
        } else {
          expect(musicSrc).not.toHaveBeenCalled()
          expect(document.querySelector('audio')).toBeNull()
        }
      },
    )

    it('TC-DE-07: the same native player restores its real audio interface after two engine round trips', async () => {
      const ref = React.createRef()
      const instances = []
      const { controller, browserAdapter, tracks, rerenderPlayer, unmount } =
        await renderControlledPlayer('local', {
          ref,
          getAudioInstance: (audio) => instances.push(audio),
        })
      const player = ref.current
      let previousBrowser = browserAdapter

      for (let round = 0; round < 2; round += 1) {
        const remoteAdapter = createMockRemoteAudioAdapter()
        await act(async () => controller.setAdapter(remoteAdapter))
        rerenderPlayer({ engineMode: 'remote', browserAdapter: undefined })
        expect(document.querySelector('audio')).toBeNull()
        expect(previousBrowser.getAudioElement()).toBeNull()

        const nextBrowser = createBrowserAudioAdapter()
        await act(async () => controller.setAdapter(nextBrowser))
        rerenderPlayer({ engineMode: 'local', browserAdapter: nextBrowser })
        await act(async () => controller.addQueueItems(tracks))
        const audio = document.querySelector('audio')
        expect(ref.current).toBe(player)
        expect(nextBrowser.getAudioElement()).toBe(audio)
        expect(instances.at(-1)).toBe(audio)
        const initialIntents = controller.getMetrics().intentsIssued
        await act(async () => instances.at(-1).togglePlay())
        expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
        await act(async () => instances.at(-1).togglePlay())
        expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
        expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 2)
        previousBrowser = nextBrowser
      }

      const finalIntents = controller.getMetrics().intentsIssued
      unmount()
      expect(previousBrowser.getAudioElement()).toBeNull()
      expect(controller.getMetrics().intentsIssued).toBe(finalIntents)
    })

    it('TC-DE-07: replacing the controller subscribes only to the new authority', async () => {
      const { controller: oldController, rerenderPlayer } =
        await renderControlledPlayer('remote')
      const controller = createPlaybackControlModule()
      controller.setAdapter(createMockRemoteAudioAdapter())
      await controller.addQueueItems([
        { queueItemId: 'replacement', name: 'Replacement song' },
      ])
      rerenderPlayer({ controller })

      await act(async () => controller.play())
      expect(document.querySelector('.play-btn').getAttribute('title')).toMatch(
        /pause/i,
      )
      await act(async () => oldController.pause())
      expect(document.querySelector('.play-btn').getAttribute('title')).toMatch(
        /pause/i,
      )
      expect(document.querySelector('.player-name').textContent).toBe(
        'Replacement song',
      )
      expect(document.querySelector('audio')).toBeNull()
    })

    it.each(['local', 'remote'])(
      'TC-DE-08: the %s native mute control restores volume through the Module',
      async (engineMode) => {
        const { controller } = await renderControlledPlayer(engineMode)
        await act(async () => controller.setVolume(0.25))
        const initialIntents = controller.getMetrics().intentsIssued

        await act(async () => document.querySelector('.sounds-icon').click())
        expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
        expect(controller.getState().isMuted).toBe(true)
        expect(controller.getState().volume).toBe(0.25)

        await act(async () => document.querySelector('.sounds-icon').click())
        expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 2)
        expect(controller.getState().isMuted).toBe(false)
        expect(controller.getState().volume).toBe(0.25)
      },
    )

    it.each(['local', 'remote'])(
      'TC-DE-08: the %s native seek slider commits one Module intent',
      async (engineMode) => {
        const { controller, browserAdapter } =
          await renderControlledPlayer(engineMode)
        await act(async () => {
          if (browserAdapter) {
            Object.defineProperty(
              browserAdapter.getAudioElement(),
              'duration',
              {
                configurable: true,
                value: 180,
              },
            )
          }
          controller.backfillState({ duration: 180 })
        })
        const handle = document.querySelector('.progress-bar .rc-slider-handle')
        const initialIntents = controller.getMetrics().intentsIssued

        await act(async () => {
          fireEvent.keyDown(handle, { key: 'ArrowRight', keyCode: 39 })
          fireEvent.keyUp(handle, { key: 'ArrowRight', keyCode: 39 })
        })

        expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
        if (browserAdapter) {
          expect(browserAdapter.getAudioElement().currentTime).toBe(1)
          await act(async () => {
            browserAdapter.getAudioElement().dispatchEvent(new Event('seeked'))
          })
        } else {
          expect(document.querySelector('audio')).toBeNull()
        }
        expect(controller.getState().position).toBe(1)
      },
    )

    it.each(['local', 'remote'])(
      'TC-DE-08: the %s native volume slider uses the shared normalized volume curve once',
      async (engineMode) => {
        const { controller, browserAdapter } =
          await renderControlledPlayer(engineMode)
        await act(async () => controller.setVolume(0.25))
        const handle = document.querySelector(
          '.sound-operation .rc-slider-handle',
        )
        expect(getNativeVolumeBarValue()).toBeCloseTo(0.5)
        const initialIntents = controller.getMetrics().intentsIssued

        await act(async () => {
          fireEvent.keyDown(handle, { key: 'ArrowRight', keyCode: 39 })
          fireEvent.keyUp(handle, { key: 'ArrowRight', keyCode: 39 })
        })

        expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
        expect(controller.getState().volume).toBeCloseTo(0.2601)
        if (browserAdapter) {
          expect(browserAdapter.getAudioElement().volume).toBeCloseTo(0.2601)
        } else {
          expect(document.querySelector('audio')).toBeNull()
        }
      },
    )

    it('TC-DE-08: a native play event cannot hide pending local playback or confirm success', async () => {
      let rejectPlay
      playSpy.mockImplementation(
        () => new Promise((_, reject) => (rejectPlay = reject)),
      )
      const { controller, browserAdapter } = await renderControlledPlayer()
      await act(async () => document.querySelector('.play-btn').click())
      await act(async () => {
        browserAdapter.getAudioElement().dispatchEvent(new Event('play'))
      })

      try {
        expect(controller.getState().status).toBe(PlaybackStatus.PREPARING)
        expect(document.querySelector('.loading-icon')).not.toBeNull()
      } finally {
        await act(async () => rejectPlay(new Error('Playback not allowed')))
      }
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
    })

    it('TC-DE-08: local native play and pause each enter the Module exactly once', async () => {
      playSpy.mockImplementation(function () {
        this.dispatchEvent(new Event('playing'))
        return Promise.resolve()
      })
      pauseSpy.mockImplementation(function () {
        this.dispatchEvent(new Event('pause'))
      })
      const { controller } = await renderControlledPlayer()
      const initialIntents = controller.getMetrics().intentsIssued

      await act(async () => document.querySelector('.play-btn').click())

      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)

      await act(async () => document.querySelector('.play-btn').click())

      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 2)
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
    })

    it.each(['local', 'remote'])(
      'TC-IN-01: the %s space-bar shortcut enters the Module exactly once per keydown',
      async (engineMode) => {
        const ref = React.createRef()
        const { controller, browserAdapter } = await renderControlledPlayer(
          engineMode,
          { ref, spaceBar: true },
        )
        const playBefore = controller.getMetrics().commandsSent.play

        await act(async () => {
          fireEvent.keyDown(ref.current.player.current, {
            key: ' ',
            keyCode: 32,
          })
        })

        expect(controller.getMetrics().commandsSent.play).toBe(playBefore + 1)
        if (browserAdapter) {
          await act(async () => {
            browserAdapter.getAudioElement().dispatchEvent(new Event('playing'))
          })
        }
        expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)

        const pauseBefore = controller.getMetrics().commandsSent.pause
        await act(async () => {
          fireEvent.keyDown(ref.current.player.current, {
            key: ' ',
            keyCode: 32,
          })
        })
        expect(controller.getMetrics().commandsSent.pause).toBe(pauseBefore + 1)
        if (browserAdapter) {
          await act(async () => {
            browserAdapter.getAudioElement().dispatchEvent(new Event('pause'))
          })
        }
        expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      },
    )

    it.each(['local', 'remote'])(
      'TC-IN-01: %s visibility and focus changes issue no playback commands',
      async (engineMode) => {
        const { controller } = await renderControlledPlayer(engineMode)
        const before = controller.getMetrics()
        const statusBefore = controller.getState().status
        await act(async () => {
          document.dispatchEvent(new Event('visibilitychange'))
          window.dispatchEvent(new Event('blur'))
          window.dispatchEvent(new Event('focus'))
          document.dispatchEvent(new Event('visibilitychange'))
        })
        const after = controller.getMetrics()
        expect(after.intentsIssued).toBe(before.intentsIssued)
        expect(after.commandsSent).toEqual(before.commandsSent)
        expect(controller.getState().status).toBe(statusBefore)
      },
    )

    it('TC-IN-01: a focused player and a document-level host handler do not double-issue one space press', async () => {
      const ref = React.createRef()
      let hostAudio = null
      const { controller } = await renderControlledPlayer('local', {
        ref,
        spaceBar: true,
        getAudioInstance: (audio) => {
          hostAudio = audio
        },
      })
      // Mirror the production host wiring (Player.jsx GlobalHotKeys ->
      // keyHandlers -> audioInstance.togglePlay) with a plain document listener.
      const hostListener = (event) => {
        if (event.keyCode === 32 && hostAudio) hostAudio.togglePlay()
      }
      document.addEventListener('keydown', hostListener)
      try {
        const before = controller.getMetrics().intentsIssued
        await act(async () => {
          fireEvent.keyDown(ref.current.player.current, {
            key: ' ',
            keyCode: 32,
          })
        })
        expect(controller.getMetrics().intentsIssued).toBe(before + 1)
      } finally {
        document.removeEventListener('keydown', hostListener)
      }
    })

    it('TC-IN-01: without a controller the native space-bar binding keeps its default behavior', async () => {
      const ref = React.createRef()
      render(
        <UnifiedNativePlayer
          ref={ref}
          audioLists={sampleTracks}
          mode="full"
          autoPlay={false}
          spaceBar
        />,
      )
      await waitFor(() => {
        expect(document.querySelector('.play-btn')).not.toBeNull()
      })
      await act(async () => {
        fireEvent.keyDown(ref.current.player.current, {
          key: ' ',
          keyCode: 32,
        })
      })
      // The native player starts playback after the media canplay event; jsdom
      // reports NETWORK_EMPTY, so mirror the baseline's loaded-media flags.
      const audio = document.querySelector('audio')
      setMediaFlags(audio, { networkState: 1, readyState: 4 })
      await act(async () => {
        audio.dispatchEvent(new Event('canplay'))
      })
      await waitFor(() => {
        expect(playSpy).toHaveBeenCalled()
      })
    })
  })

  describe('Shortcut and media key consistency (Issue 05)', () => {
    function installMediaSessionStub() {
      const handlers = {}
      const stub = {
        metadata: null,
        setActionHandler: vi.fn((action, handler) => {
          handlers[action] = handler
        }),
        setPositionState: vi.fn(),
      }
      Object.defineProperty(window.navigator, 'mediaSession', {
        configurable: true,
        writable: true,
        value: stub,
      })
      window.MediaMetadata = vi.fn(function MediaMetadata(config) {
        Object.assign(this, config)
      })
      return { stub, handlers }
    }

    afterEach(() => {
      delete window.navigator.mediaSession
      delete window.MediaMetadata
    })

    it.each(['local', 'remote'])(
      'TC-IN-02: %s media session transport actions enter the same Module intents',
      async (engineMode) => {
        const { stub, handlers } = installMediaSessionStub()
        const { controller } = await renderControlledPlayer(engineMode, {
          showMediaSession: true,
        })
        for (const action of [
          'play',
          'pause',
          'previoustrack',
          'nexttrack',
          'seekto',
          'seekbackward',
          'seekforward',
        ]) {
          expect(typeof handlers[action]).toBe('function')
        }

        const sent = () => controller.getMetrics().commandsSent
        const before = { ...sent() }
        await act(async () => handlers.nexttrack())
        await act(async () => handlers.previoustrack())
        await act(async () => handlers.play())
        await act(async () => handlers.pause())
        expect(sent().skipNext).toBe(before.skipNext + 1)
        expect(sent().skipPrev).toBe(before.skipPrev + 1)
        expect(sent().play).toBe(before.play + 1)
        expect(sent().pause).toBe(before.pause + 1)
      },
    )

    it.each(['local', 'remote'])(
      'TC-IN-02: %s media session seeks commit against the authoritative position, not a media element',
      async (engineMode) => {
        const { handlers } = installMediaSessionStub()
        const { controller, browserAdapter } = await renderControlledPlayer(
          engineMode,
          { showMediaSession: true, duration: 180 },
        )
        await act(async () => controller.play())
        if (browserAdapter) {
          await act(async () => {
            browserAdapter.getAudioElement().dispatchEvent(new Event('playing'))
          })
        }
        await act(async () =>
          controller.backfillState({ position: 40, duration: 180 }),
        )
        expect(controller.getState().position).toBe(40)

        const sent = () => controller.getMetrics().commandsSent
        const seekBefore = sent().seek

        await act(async () => handlers.seekbackward({ seekOffset: 10 }))
        if (browserAdapter) {
          expect(browserAdapter.getAudioElement().currentTime).toBe(30)
          await act(async () => {
            browserAdapter.getAudioElement().dispatchEvent(new Event('seeked'))
          })
        }
        expect(controller.getState().position).toBe(30)

        await act(async () => handlers.seekforward({}))
        if (browserAdapter) {
          expect(browserAdapter.getAudioElement().currentTime).toBe(40)
          await act(async () => {
            browserAdapter.getAudioElement().dispatchEvent(new Event('seeked'))
          })
        }
        expect(controller.getState().position).toBe(40)

        await act(async () => handlers.seekto({ seekTime: 12 }))
        if (browserAdapter) {
          expect(browserAdapter.getAudioElement().currentTime).toBe(12)
          await act(async () => {
            browserAdapter.getAudioElement().dispatchEvent(new Event('seeked'))
          })
        }
        expect(controller.getState().position).toBe(12)
        expect(sent().seek).toBe(seekBefore + 3)
      },
    )

    it.each(['local', 'remote'])(
      'TC-IN-02: %s media session metadata and position state come from the authority',
      async (engineMode) => {
        const { stub } = installMediaSessionStub()
        const { controller } = await renderControlledPlayer(engineMode, {
          showMediaSession: true,
          duration: 180,
        })
        await waitFor(() => expect(window.MediaMetadata).toHaveBeenCalled())
        expect(stub.metadata.title).toBe('Song 0')
        expect(stub.metadata.artist).toBe('Artist 0')

        await act(async () =>
          controller.backfillState({ position: 42, duration: 180 }),
        )
        expect(stub.setPositionState).toHaveBeenCalled()
        const last = stub.setPositionState.mock.calls.at(-1)[0]
        expect(last.position).toBe(42)
        expect(last.duration).toBe(180)
      },
    )

    it('TC-IN-02: unmounting the controlled player releases every media session action', async () => {
      const { handlers } = installMediaSessionStub()
      const { unmount } = await renderControlledPlayer('remote', {
        showMediaSession: true,
      })
      expect(typeof handlers.play).toBe('function')
      unmount()
      for (const action of [
        'play',
        'pause',
        'previoustrack',
        'nexttrack',
        'seekto',
        'seekbackward',
        'seekforward',
      ]) {
        expect(handlers[action]).toBeNull()
      }
    })

    it('TC-IN-02: controlled mode without showMediaSession registers no media session handlers', async () => {
      const { stub } = installMediaSessionStub()
      await renderControlledPlayer('remote')
      expect(stub.setActionHandler).not.toHaveBeenCalled()
    })
  })

  describe('Authoritative display state (Issue 05)', () => {
    const lyricTracks = [
      {
        name: 'Lyric Song 1',
        singer: 'Artist L1',
        musicSrc: 'http://localhost/lyric1.mp3',
        id: 's-l1',
        queueItemId: 'q-l1',
        lyric: '[00:01.00]line one\n[00:03.00]line two\n[00:05.00]line three',
      },
      {
        name: 'Lyric Song 2',
        singer: 'Artist L2',
        musicSrc: 'http://localhost/lyric2.mp3',
        id: 's-l2',
        queueItemId: 'q-l2',
        lyric: '[00:01.00]other one\n[00:03.00]other two',
      },
    ]

    const currentLyricLine = () =>
      document.querySelector('.music-player-lyric')?.textContent

    async function openLyricView() {
      await act(async () => document.querySelector('.lyric-btn').click())
      expect(document.querySelector('.music-player-lyric')).not.toBeNull()
    }

    it('TC-DSP-01: remote lyric highlighting follows the authoritative position without any audio element', async () => {
      const { controller } = await renderControlledPlayer('remote', {
        audioLists: lyricTracks,
        showLyric: true,
      })
      expect(document.querySelector('audio')).toBeNull()
      await act(async () => controller.play())
      await openLyricView()
      const intentsBefore = controller.getMetrics().intentsIssued

      await act(async () => controller.backfillState({ position: 3.2 }))
      expect(currentLyricLine()).toContain('line two')

      // An earlier snapshot moves the highlight back; nothing replays forward.
      await act(async () => controller.backfillState({ position: 0.2 }))
      expect(currentLyricLine()).not.toContain('line')

      await act(async () => controller.backfillState({ position: 5.2 }))
      expect(currentLyricLine()).toContain('line three')
      expect(controller.getMetrics().intentsIssued).toBe(intentsBefore)
    })

    it('TC-DSP-01: a remote track change re-seeds lyric highlighting from the new authoritative track', async () => {
      const { controller } = await renderControlledPlayer('remote', {
        audioLists: lyricTracks,
        showLyric: true,
      })
      await act(async () => controller.play())
      await openLyricView()
      await act(async () => controller.backfillState({ position: 3.2 }))
      expect(currentLyricLine()).toContain('line two')

      await act(async () => controller.skipNext())
      await act(async () => controller.backfillState({ position: 1.2 }))
      expect(currentLyricLine()).toContain('other one')
    })

    it('TC-DSP-01: local lyric highlighting follows the same authoritative position', async () => {
      const { controller, browserAdapter } = await renderControlledPlayer(
        'local',
        { audioLists: lyricTracks, showLyric: true },
      )
      await act(async () => controller.play())
      await act(async () => {
        browserAdapter.getAudioElement().dispatchEvent(new Event('playing'))
      })
      await openLyricView()
      const audio = browserAdapter.getAudioElement()
      await act(async () => {
        Object.defineProperty(audio, 'currentTime', {
          configurable: true,
          value: 3.2,
        })
        audio.dispatchEvent(new Event('timeupdate'))
      })
      expect(currentLyricLine()).toContain('line two')
    })
  })

  describe('Native DOM Controller Interactions', () => {
    /**
     * Case: TC-DE-08
     * Requirement: US-1, US-3, Issue 02 line 11
     * Initial Scene: Remote player mounted with real native DOM
     * Input Intent: User clicks .play-btn in DOM
     * Expected State: Dispatches play/pause through controller, updates icon
     * Forbidden Side-Effect: Any <audio> tag creation
     */
    it('TC-DE-08: real DOM button click (.play-btn) dispatches through unified controller in remote mode', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      render(
        <UnifiedNativePlayer
          controller={controller}
          engineMode="remote"
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
          duration={180}
        />,
      )

      await waitFor(() => {
        expect(document.querySelector('.play-btn')).not.toBeNull()
      })

      const playBtn = document.querySelector('.play-btn')

      // Click the real native play button DOM element
      await act(async () => {
        playBtn.click()
      })

      // Verify controller processed the intent and confirmed playing
      await waitFor(() => {
        expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      })
      expect(document.querySelector('audio')).toBeNull()

      // Click again to pause
      await waitFor(() => {
        expect(document.querySelector('.play-btn')).not.toBeNull()
      })
      const pauseBtn = document.querySelector('.play-btn')
      await act(async () => {
        pauseBtn.click()
      })
      await waitFor(() => {
        expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      })
      expect(document.querySelector('audio')).toBeNull()
    })
  })

  describe('Backward Compatibility with Default Baseline', () => {
    /**
     * Case: TC-DE-09
     * Requirement: US-24, US-25, Issue 02 line 19
     * Initial Scene: UnifiedNativePlayer used without unified controller
     * Input Intent: Normal mount as standard ReactJkMusicPlayer
     * Expected State: Mounts real audio, getAudioInstance returns enhanced instance
     */
    it('TC-DE-09: UnifiedNativePlayer without controller falls back cleanly to default ReactJkMusicPlayer behavior', async () => {
      let capturedAudio = null
      render(
        <UnifiedNativePlayer
          audioLists={sampleTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
          getAudioInstance={(instance) => {
            capturedAudio = instance
          }}
        />,
      )

      await waitFor(() => {
        expect(capturedAudio).not.toBeNull()
      })

      const audioEl = document.querySelector('audio')
      expect(audioEl).not.toBeNull()
      expect(capturedAudio).toBe(audioEl)
      expect(typeof capturedAudio.togglePlay).toBe('function')
    })
  })

  describe('Async authority and media-result regressions', () => {
    it('TC-DE-05: a previous engine rejection cannot replace the new engine state', async () => {
      vi.useFakeTimers()
      try {
        const controller = createPlaybackControlModule()
        const oldAdapter = {
          activate: () => {},
          deactivate: () => {},
          play: () =>
            new Promise((_, reject) => {
              setTimeout(() => reject({ code: 'OLD_ENGINE_REJECTED' }), 20)
            }),
        }
        controller.setAdapter(oldAdapter)
        const oldResult = controller.play().catch((error) => error)

        controller.setAdapter(createMockRemoteAudioAdapter())
        await controller.play()
        await controller.pause()
        const newState = controller.getState()

        await vi.advanceTimersByTimeAsync(20)
        expect((await oldResult).code).toBe('OLD_ENGINE_REJECTED')
        expect(controller.getState()).toEqual(newState)
        expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
        expect(controller.getState().error).toBeNull()
      } finally {
        vi.useRealTimers()
      }
    })

    it.each([
      ['clear', 0],
      ['add', 20],
    ])(
      'TC-QC-10: switching during Play All %s cannot write to the new engine',
      async (_phase, elapsed) => {
        vi.useFakeTimers()
        try {
          const controller = createPlaybackControlModule()
          controller.setAdapter(createMockRemoteAudioAdapter({ delayMs: 20 }))
          const oldResult = controller
            .playAll([{ queueItemId: 'old-item', name: 'Old scene' }])
            .catch((error) => error)
          if (elapsed) await vi.advanceTimersByTimeAsync(elapsed)

          const newAdapter = createMockRemoteAudioAdapter()
          controller.setAdapter(newAdapter)
          await controller.addQueueItems([
            { queueItemId: 'new-item', name: 'New scene' },
          ])
          const newState = controller.getState()
          const newCommands = newAdapter.getWireCommands()

          await vi.advanceTimersByTimeAsync(20)
          expect((await oldResult).code).toBe('COMMAND_SUPERSEDED')
          expect(controller.getState()).toEqual(newState)
          expect(newAdapter.getWireCommands()).toEqual(newCommands)
          expect(newAdapter.getQueue().map((item) => item.queueItemId)).toEqual(
            ['new-item'],
          )
        } finally {
          vi.useRealTimers()
        }
      },
    )

    it('TC-QC-11: a fresh adapter accepts its lower queue version without retaining the old scene', async () => {
      const controller = createPlaybackControlModule()
      controller.setAdapter(createMockRemoteAudioAdapter())
      await controller.addQueueItems([{ queueItemId: 'old-one' }])
      await controller.addQueueItems([{ queueItemId: 'old-two' }])
      await controller.play()
      expect(controller.getState().queueVersion).toBe(2)

      const newAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(newAdapter)
      await controller.addQueueItems([{ queueItemId: 'new-one' }])

      expect(controller.getState().queueVersion).toBe(1)
      expect(
        controller.getState().queue.map((item) => item.queueItemId),
      ).toEqual(['new-one'])
      expect(controller.getState().currentQueueItemId).toBe('new-one')
      expect(controller.getState().status).toBe(PlaybackStatus.IDLE)
      expect(controller.getState().error).toBeNull()
      expect(newAdapter.getQueue()).toEqual(controller.getState().queue)
    })

    it('TC-QC-11: playback ordering rejects an older state at the same queue version before remote mutation', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)
      await controller.addQueueItems([{ queueItemId: 'one' }])
      remoteAdapter.pushServerSnapshot({
        queueVersion: 1,
        stateSequence: 10,
        status: PlaybackStatus.PAUSED,
        position: 12,
        error: null,
      })
      const accepted = controller.getState()
      const intents = controller.getMetrics().intentsIssued
      remoteAdapter.pushServerSnapshot({
        queueVersion: 1,
        stateSequence: 9,
        status: PlaybackStatus.PLAYING,
        position: 3,
      })

      expect(controller.getState()).toEqual(accepted)
      expect(remoteAdapter.getRemoteState().status).toBe(PlaybackStatus.PAUSED)
      expect(remoteAdapter.getRemoteState().position).toBe(12)
      expect(controller.getMetrics().intentsIssued).toBe(intents)
      await controller.setVolume(0.4)
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(controller.getState().position).toBe(12)
    })

    it('TC-PM-01: a local next waits for media confirmation and exposes a rejected play', async () => {
      let rejectPlayback
      playSpy.mockImplementation(
        () =>
          new Promise((_, reject) => {
            rejectPlayback = reject
          }),
      )
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems([
        { queueItemId: 'one', musicSrc: '/one.mp3' },
        { queueItemId: 'two', musicSrc: '/two.mp3' },
      ])
      const result = controller.skipNext().catch((error) => error)
      try {
        expect(controller.getState().currentQueueItemId).toBe('two')
        expect(controller.getState().status).toBe(PlaybackStatus.PREPARING)
        browserAdapter.getAudioElement().dispatchEvent(new Event('play'))
        expect(controller.getState().status).toBe(PlaybackStatus.PREPARING)
      } finally {
        rejectPlayback(
          new DOMException('Playback permission denied', 'NotAllowedError'),
        )
        await result
      }
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(controller.getState().error.message).toContain(
        'Playback permission denied',
      )
      expect((await result).code).toBe('BROWSER_PLAY_REQUEST_FAILED')
    })

    it.each(['resolve', 'reject'])(
      'TC-DE-05: an earlier track %s cannot overwrite a newer playback attempt',
      async (completion) => {
        let finishOldPlayback
        playSpy.mockImplementationOnce(
          () =>
            new Promise((resolve, reject) => {
              finishOldPlayback =
                completion === 'resolve'
                  ? resolve
                  : () =>
                      reject(
                        new DOMException(
                          'Old playback rejected',
                          'NotAllowedError',
                        ),
                      )
            }),
        )
        const controller = createPlaybackControlModule()
        const browserAdapter = createBrowserAudioAdapter()
        controller.setAdapter(browserAdapter)
        await controller.addQueueItems([
          { queueItemId: 'old-track', musicSrc: '/old.mp3' },
          { queueItemId: 'new-track', musicSrc: '/new.mp3' },
        ])
        const oldResult = controller.play().catch((error) => error)
        await controller.pause()
        await controller.skipNext()
        const accepted = controller.getState()
        finishOldPlayback()

        expect((await oldResult).code).toBe('COMMAND_SUPERSEDED')
        expect(controller.getState()).toEqual(accepted)
        expect(controller.getState().currentQueueItemId).toBe('new-track')
        expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
        expect(controller.getState().error).toBeNull()
      },
    )

    it('TC-DE-05: a delayed remote write cannot execute after the same adapter is reactivated', async () => {
      vi.useFakeTimers()
      try {
        const controller = createPlaybackControlModule()
        const remoteAdapter = createMockRemoteAudioAdapter({ delayMs: 20 })
        controller.setAdapter(remoteAdapter)
        const oldResult = controller
          .addQueueItems([{ queueItemId: 'obsolete' }])
          .catch((error) => error)
        controller.setAdapter(createMockRemoteAudioAdapter())
        controller.setAdapter(remoteAdapter)
        const newResult = controller.addQueueItems([{ queueItemId: 'current' }])
        await vi.advanceTimersByTimeAsync(20)
        await newResult

        expect((await oldResult).code).toBe('COMMAND_SUPERSEDED')
        expect(
          remoteAdapter.getQueue().map((item) => item.queueItemId),
        ).toEqual(['current'])
        expect(controller.getState().queue).toEqual(remoteAdapter.getQueue())
        expect(controller.getState().error).toBeNull()
      } finally {
        vi.useRealTimers()
      }
    })

    it('TC-QC-01: a removed skip target is a no-op on both adapters', async () => {
      for (const adapter of [
        createBrowserAudioAdapter(),
        createMockRemoteAudioAdapter(),
      ]) {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)
        await controller.addQueueItems([
          { queueItemId: 'keep', musicSrc: '/keep.mp3' },
          { queueItemId: 'removed', musicSrc: '/removed.mp3' },
        ])
        await controller.removeQueueItem('removed')
        const before = controller.getState()
        const plays = playSpy.mock.calls.length
        await controller.skipToQueueItem('removed')

        const after = controller.getState()
        expect(after.currentQueueItemId).toBe('keep')
        expect(after.queue).toEqual(before.queue)
        expect(after.status).toBe(before.status)
        expect(after.position).toBe(before.position)
        expect(after.error).toBeNull()
        expect(playSpy.mock.calls.length).toBe(plays)
        controller.setAdapter(null)
      }
    })

    it('TC-QC-11: an explicitly captured old backfill epoch cannot enter the current scene', async () => {
      const controller = createPlaybackControlModule()
      controller.setAdapter(createMockRemoteAudioAdapter())
      await controller.addQueueItems([{ queueItemId: 'obsolete' }])
      const oldEpoch = controller.getEpoch()
      controller.setAdapter(createMockRemoteAudioAdapter())
      await controller.addQueueItems([{ queueItemId: 'current' }])
      const accepted = controller.getState()
      const intents = controller.getMetrics().intentsIssued
      controller.backfillState(
        {
          queue: [{ queueItemId: 'obsolete' }],
          currentQueueItemId: 'obsolete',
          queueVersion: 100,
          stateSequence: 100,
          status: PlaybackStatus.ERROR,
          error: { code: 'OLD_SCENE_ERROR' },
        },
        oldEpoch,
      )

      expect(controller.getState()).toEqual(accepted)
      expect(controller.getMetrics().intentsIssued).toBe(intents)
    })

    it('TC-QC-11: a remote snapshot from an earlier activation cannot mutate its reactivated adapter', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)
      const oldEpoch = controller.getEpoch()
      controller.setAdapter(createMockRemoteAudioAdapter())
      controller.setAdapter(remoteAdapter)
      await controller.addQueueItems([{ queueItemId: 'current' }])
      const accepted = controller.getState()
      remoteAdapter.pushServerSnapshot(
        {
          queue: [{ queueItemId: 'obsolete' }],
          currentQueueItemId: 'obsolete',
          queueVersion: 100,
          stateSequence: 100,
          status: PlaybackStatus.PLAYING,
        },
        oldEpoch,
      )

      expect(remoteAdapter.getQueue().map((item) => item.queueItemId)).toEqual([
        'current',
      ])
      expect(controller.getState()).toEqual(accepted)
    })
    it.each(['resolve', 'reject'])(
      'TC-DE-05: an obsolete async media source %s never loads or plays over a new track',
      async (completion) => {
        let finishSource
        const source = new Promise((resolve, reject) => {
          finishSource =
            completion === 'resolve'
              ? () => resolve('/obsolete.mp3')
              : () => reject(new Error('Obsolete URL lookup failed'))
        })
        const controller = createPlaybackControlModule()
        const browserAdapter = createBrowserAudioAdapter()
        controller.setAdapter(browserAdapter)
        await controller.addQueueItems([
          { queueItemId: 'obsolete', musicSrc: () => source },
          { queueItemId: 'current', musicSrc: '/current.mp3' },
        ])
        const oldResult = controller.play().catch((error) => error)
        await controller.skipNext()
        const accepted = controller.getState()
        const plays = playSpy.mock.calls.length
        finishSource()

        expect((await oldResult).code).toBe('COMMAND_SUPERSEDED')
        expect(browserAdapter.getAudioElement().getAttribute('src')).toBe(
          '/current.mp3',
        )
        expect(playSpy.mock.calls.length).toBe(plays)
        expect(controller.getState()).toEqual(accepted)
      },
    )
    it('TC-DE-06: an old command timeout cannot replace a new paused scene', async () => {
      vi.useFakeTimers()
      try {
        const controller = createPlaybackControlModule({ commandTimeoutMs: 30 })
        controller.setAdapter({
          activate: () => {},
          deactivate: () => {},
          play: () => new Promise(() => {}),
        })
        const oldResult = controller.play().catch((error) => error)
        controller.setAdapter(createMockRemoteAudioAdapter())
        await controller.play()
        await controller.pause()
        const accepted = controller.getState()
        const intents = controller.getMetrics().intentsIssued
        await vi.advanceTimersByTimeAsync(30)

        expect((await oldResult).code).toBe('COMMAND_TIMEOUT')
        expect(controller.getState()).toEqual(accepted)
        expect(controller.getMetrics().intentsIssued).toBe(intents)
      } finally {
        vi.useRealTimers()
      }
    })
    it('TC-DE-08: a legacy non-Promise play() on an already-playing element confirms without waiting for a playing event', async () => {
      // Legacy engines return undefined from play(); when the element already
      // reports playing, no fresh 'playing' event fires to confirm the attempt.
      playSpy.mockImplementation(function () {
        setMediaFlags(this, { paused: false, ended: false })
        return undefined
      })
      const controller = createPlaybackControlModule({ commandTimeoutMs: 300 })
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems([
        { queueItemId: 'legacy', musicSrc: '/legacy.mp3' },
      ])

      await controller.play()

      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
    })
    it('TC-DE-05: real native mounting and repeated attachment advance once and detach without an orphan engine', async () => {
      const tracks = [
        { ...sampleTracks[0], queueItemId: 'first' },
        { ...sampleTracks[1], queueItemId: 'second' },
        { ...sampleTracks[0], queueItemId: 'third' },
      ]
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems(tracks)
      const { unmount } = render(
        <UnifiedNativePlayer
          controller={controller}
          browserAdapter={browserAdapter}
          engineMode="local"
          audioLists={tracks}
          mode="full"
          autoPlay={false}
        />,
      )
      const audio = document.querySelector('audio')
      expect(browserAdapter.getAudioElement()).toBe(audio)
      browserAdapter.attachElement(audio)
      browserAdapter.attachElement(audio)
      await act(async () => {
        await controller.play()
      })
      const intents = controller.getMetrics().intentsIssued
      const plays = playSpy.mock.calls.length
      await act(async () => {
        finishMedia(audio)
      })

      expect(controller.getState().currentQueueItemId).toBe('second')
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(playSpy.mock.calls.length).toBe(plays + 1)
      expect(controller.getMetrics().intentsIssued).toBe(intents)
      unmount()
      const detached = controller.getState()
      audio.dispatchEvent(new Event('ended'))
      expect(controller.getState()).toEqual(detached)
      expect(browserAdapter.getAudioElement()).toBeNull()
      const created = createElementSpy.mock.calls.filter(
        ([tag]) => tag === 'audio',
      ).length
      const playsAfterDetach = playSpy.mock.calls.length
      const error = await controller.play().catch((failure) => failure)
      expect(error.code).toBe('NO_AUDIO_ELEMENT')
      expect(
        createElementSpy.mock.calls.filter(([tag]) => tag === 'audio'),
      ).toHaveLength(created)
      expect(playSpy.mock.calls.length).toBe(playsAfterDetach)
      controller.setAdapter(null)
    })
    it.each(['next', 'single repeat', 'remove current', 'select item'])(
      'TC-PM-04: %s reports media rejection without automatic replay',
      async (action) => {
        const source = vi.fn().mockResolvedValue('/resolved-first.mp3')
        const controller = createPlaybackControlModule()
        const browserAdapter = createBrowserAudioAdapter()
        controller.setAdapter(browserAdapter)
        await controller.addQueueItems([
          { queueItemId: 'first', musicSrc: source },
          { queueItemId: 'second', musicSrc: '/second.mp3' },
        ])
        await controller.play()
        const audio = browserAdapter.getAudioElement()
        expect(audio.getAttribute('src')).toBe('/resolved-first.mp3')
        if (action === 'single repeat')
          await controller.setPlayMode(PlaybackMode.REPEAT_TRACK)
        const sourceWrites = vi.spyOn(audio, 'src', 'set')
        const intents = controller.getMetrics().intentsIssued
        const plays = playSpy.mock.calls.length
        playSpy.mockRejectedValueOnce(
          new DOMException('Playback blocked', 'NotAllowedError'),
        )
        let result
        if (action === 'single repeat') {
          await act(async () => {
            finishMedia(audio)
          })
        } else if (action === 'next') {
          result = await controller.skipNext().catch((error) => error)
        } else if (action === 'remove current') {
          result = await controller
            .removeQueueItem('first')
            .catch((error) => error)
        } else {
          result = await controller
            .skipToQueueItem('second')
            .catch((error) => error)
        }

        expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
        expect(controller.getState().error.message).toBe('Playback blocked')
        expect(playSpy.mock.calls.length).toBe(plays + 1)
        expect(controller.getMetrics().intentsIssued).toBe(
          intents + (action === 'single repeat' ? 0 : 1),
        )
        if (action === 'single repeat') {
          expect(controller.getState().currentQueueItemId).toBe('first')
          expect(sourceWrites).not.toHaveBeenCalled()
          expect(source).toHaveBeenCalledTimes(1)
        } else {
          expect(result.code).toBe('BROWSER_PLAY_REQUEST_FAILED')
          expect(controller.getState().currentQueueItemId).toBe('second')
        }
        controller.setAdapter(null)
      },
    )
    it('TC-DE-05: a subscriber switching during PREPARING cannot redirect the issued play', async () => {
      const controller = createPlaybackControlModule()
      const oldAdapter = createMockRemoteAudioAdapter()
      const newAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(oldAdapter)
      const unsubscribe = controller.subscribe((state) => {
        if (state.status === PlaybackStatus.PREPARING) {
          unsubscribe()
          controller.setAdapter(newAdapter)
        }
      })
      const result = await controller.play().catch((error) => error)

      expect(result.code).toBe('COMMAND_SUPERSEDED')
      expect(controller.getState().status).toBe(PlaybackStatus.IDLE)
      expect(controller.getState().error).toBeNull()
      expect(newAdapter.getRemoteState().status).toBe(PlaybackStatus.IDLE)
      expect(controller.getMetrics().commandsSent.play).toBe(0)
      expect(controller.getMetrics().intentsIssued).toBe(1)
    })
    it('TC-DE-05: an earlier activation callback cannot impersonate the current adapter epoch', () => {
      const callbacks = []
      const adapter = {
        activate: (_epoch, callback) => callbacks.push(callback),
        deactivate: () => {},
      }
      const controller = createPlaybackControlModule()
      controller.setAdapter(adapter)
      controller.setAdapter(createMockRemoteAudioAdapter())
      controller.setAdapter(adapter)
      const epoch = controller.getEpoch()
      callbacks[1](epoch, { status: PlaybackStatus.PAUSED, stateSequence: 2 })
      const accepted = controller.getState()
      callbacks[0](epoch, {
        status: PlaybackStatus.ERROR,
        error: { code: 'OLD_ACTIVATION' },
        queueVersion: 100,
        stateSequence: 100,
      })

      expect(controller.getState()).toEqual(accepted)
      expect(controller.getMetrics().intentsIssued).toBe(0)
    })
    it('TC-DE-06: a missing resolved media URL cannot play the previous track as a successful selection', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems([
        { queueItemId: 'first', musicSrc: '/first.mp3' },
        { queueItemId: 'invalid', musicSrc: async () => undefined },
      ])
      await controller.play()
      const plays = playSpy.mock.calls.length
      const result = await controller.skipNext().catch((error) => error)

      expect(result.code).toBe('MEDIA_SOURCE_UNAVAILABLE')
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(playSpy.mock.calls.length).toBe(plays)
    })
    it('TC-DE-05: old-epoch play and pause perform no work on either reactivated adapter', async () => {
      for (const adapter of [
        createBrowserAudioAdapter(),
        createMockRemoteAudioAdapter(),
      ]) {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)
        const oldEpoch = controller.getEpoch()
        controller.setAdapter(createMockRemoteAudioAdapter())
        controller.setAdapter(adapter)
        const accepted = controller.getState()
        const plays = playSpy.mock.calls.length
        const pauses = pauseSpy.mock.calls.length
        await adapter.play(oldEpoch)
        await adapter.pause(oldEpoch)

        expect(controller.getState()).toEqual(accepted)
        expect(playSpy.mock.calls.length).toBe(plays)
        expect(pauseSpy.mock.calls.length).toBe(pauses)
        expect(controller.getMetrics().intentsIssued).toBe(0)
        controller.setAdapter(null)
      }
    })
    it('TC-PM-02: an old ended event cannot advance a new resource that has not ended', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems([
        { queueItemId: 'first', musicSrc: '/first.mp3' },
        { queueItemId: 'second', musicSrc: '/second.mp3' },
        { queueItemId: 'third', musicSrc: '/third.mp3' },
      ])
      await controller.play()
      await controller.skipNext()
      const audio = browserAdapter.getAudioElement()
      expect(audio.ended).toBe(false)
      const accepted = controller.getState()
      const plays = playSpy.mock.calls.length
      const intents = controller.getMetrics().intentsIssued
      await act(async () => {
        audio.dispatchEvent(new Event('ended'))
      })

      expect(controller.getState()).toEqual(accepted)
      expect(controller.getState().currentQueueItemId).toBe('second')
      expect(playSpy.mock.calls.length).toBe(plays)
      expect(controller.getMetrics().intentsIssued).toBe(intents)
    })
    it.each(['same item', 'different item'])(
      'TC-PM-04: late single-repeat completion cannot confirm a newer %s attempt',
      async (selection) => {
        const controller = createPlaybackControlModule()
        const browserAdapter = createBrowserAudioAdapter()
        controller.setAdapter(browserAdapter)
        await controller.addQueueItems([
          { queueItemId: 'first', musicSrc: '/first.mp3' },
          { queueItemId: 'second', musicSrc: '/second.mp3' },
        ])
        await controller.play()
        await controller.setPlayMode(PlaybackMode.REPEAT_TRACK)
        let finishRepeat
        playSpy.mockImplementationOnce(function () {
          setMediaFlags(this, { paused: false, ended: false })
          return new Promise((resolve) => {
            finishRepeat = resolve
          })
        })
        const audio = browserAdapter.getAudioElement()
        await act(async () => {
          finishMedia(audio)
        })
        expect(controller.getState().status).toBe(PlaybackStatus.PREPARING)
        await controller.pause()
        if (selection === 'same item') await controller.play()
        else await controller.skipNext()
        const accepted = controller.getState()
        const intents = controller.getMetrics().intentsIssued
        const plays = playSpy.mock.calls.length
        await act(async () => {
          finishRepeat()
        })

        expect(controller.getState()).toEqual(accepted)
        expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
        expect(controller.getState().currentQueueItemId).toBe(
          selection === 'same item' ? 'first' : 'second',
        )
        expect(controller.getMetrics().intentsIssued).toBe(intents)
        expect(playSpy.mock.calls.length).toBe(plays)
      },
    )
    it('TC-DE-05: stale pause and error events cannot replace a healthy current resource', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems([
        { queueItemId: 'first', musicSrc: '/first.mp3' },
        { queueItemId: 'second', musicSrc: '/second.mp3' },
      ])
      await controller.play()
      await controller.skipNext()
      const audio = browserAdapter.getAudioElement()
      setMediaFlags(audio, { error: null })
      expect(audio.paused).toBe(false)
      expect(audio.error).toBeNull()
      const accepted = controller.getState()
      const intents = controller.getMetrics().intentsIssued
      await act(async () => {
        audio.dispatchEvent(new Event('pause'))
        audio.dispatchEvent(new Event('error'))
      })

      expect(controller.getState()).toEqual(accepted)
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controller.getMetrics().intentsIssued).toBe(intents)
    })
    it('TC-QC-07: a delayed move rejects an intervening authoritative queue edit', async () => {
      vi.useFakeTimers()
      try {
        const controller = createPlaybackControlModule()
        const remoteAdapter = createMockRemoteAudioAdapter({ delayMs: 20 })
        controller.setAdapter(remoteAdapter)
        const items = [
          { queueItemId: 'first' },
          { queueItemId: 'second' },
          { queueItemId: 'third' },
        ]
        const initial = controller.addQueueItems(items)
        await vi.advanceTimersByTimeAsync(20)
        await initial
        const move = controller
          .moveQueueItem('third', 0)
          .catch((error) => error)
        const authoritative = [
          items[0],
          { queueItemId: 'inserted' },
          items[1],
          items[2],
        ]
        remoteAdapter.pushServerSnapshot({
          queue: authoritative,
          currentQueueItemId: 'first',
          queueVersion: 2,
        })
        const intents = controller.getMetrics().intentsIssued
        await vi.advanceTimersByTimeAsync(20)
        const result = await move

        expect(result?.code).toBe('QUEUE_VERSION_CONFLICT')
        expect(remoteAdapter.getQueue()).toEqual(authoritative)
        expect(controller.getState().queue).toEqual(authoritative)
        expect(controller.getState().currentQueueItemId).toBe('first')
        expect(controller.getState().error.code).toBe('QUEUE_VERSION_CONFLICT')
        expect(controller.getMetrics().intentsIssued).toBe(intents)
      } finally {
        vi.useRealTimers()
      }
    })
    it('TC-DE-06: a current media error stays visible after its pending play settles', async () => {
      let finishPlayback
      playSpy.mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finishPlayback = resolve
          }),
      )
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems([
        { queueItemId: 'current', musicSrc: '/current.mp3' },
      ])
      const result = controller.play().catch((error) => error)
      const audio = browserAdapter.getAudioElement()
      setMediaFlags(audio, {
        paused: true,
        error: { code: 3, message: 'Current decoder failed' },
      })
      audio.dispatchEvent(new Event('error'))
      const failed = controller.getState()
      finishPlayback()

      expect((await result).code).toBe('COMMAND_SUPERSEDED')
      expect(controller.getState()).toEqual(failed)
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(controller.getState().error).toEqual({
        code: 'MEDIA_ELEMENT_ERROR',
        message: 'Current decoder failed',
      })
    })
    it('TC-QC-07: both adapters reject an explicit outdated move baseline without changing the queue', async () => {
      for (const adapter of [
        createBrowserAudioAdapter(),
        createMockRemoteAudioAdapter(),
      ]) {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)
        await controller.addQueueItems([
          { queueItemId: 'first', musicSrc: '/first.mp3' },
          { queueItemId: 'second', musicSrc: '/second.mp3' },
        ])
        const before = controller.getState()
        const result = await controller
          .moveQueueItem('second', 0, 0)
          .catch((error) => error)

        expect(result.code).toBe('QUEUE_VERSION_CONFLICT')
        expect(controller.getState().queue).toEqual(before.queue)
        expect(controller.getState().queueVersion).toBe(before.queueVersion)
        expect(controller.getState().currentQueueItemId).toBe('first')
        expect(adapter.getQueue()).toEqual(before.queue)
        controller.setAdapter(null)
      }
    })
    it('TC-DE-05: an old resource error cannot cancel the next track while its URL is pending', async () => {
      let finishSource
      const source = new Promise((resolve) => {
        finishSource = resolve
      })
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      await controller.addQueueItems([
        { queueItemId: 'first', musicSrc: '/first.mp3' },
        { queueItemId: 'second', musicSrc: () => source },
      ])
      await controller.play()
      const next = controller.skipNext().catch((error) => error)
      const audio = browserAdapter.getAudioElement()
      setMediaFlags(audio, {
        error: { code: 3, message: 'Old resource failed' },
      })
      try {
        audio.dispatchEvent(new Event('error'))
        expect(controller.getState().status).toBe(PlaybackStatus.PREPARING)
        expect(controller.getState().error).toBeNull()
      } finally {
        setMediaFlags(audio, { error: null })
        finishSource('/second.mp3')
        await next
      }
      expect(controller.getState().currentQueueItemId).toBe('second')
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controller.getState().error).toBeNull()
    })
    it('TC-QC-11: an old remote queue snapshot cannot corrupt a subsequent valid edit', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)
      await controller.addQueueItems([{ queueItemId: 'first' }])
      await controller.addQueueItems([{ queueItemId: 'second' }])
      const accepted = controller.getState()
      const intents = controller.getMetrics().intentsIssued
      remoteAdapter.pushServerSnapshot({
        queue: [{ queueItemId: 'obsolete' }],
        currentQueueItemId: 'obsolete',
        queueVersion: 1,
        stateSequence: 100,
      })
      expect(controller.getState()).toEqual(accepted)
      expect(controller.getMetrics().intentsIssued).toBe(intents)
      await controller.addQueueItems([{ queueItemId: 'third' }])

      expect(controller.getState().queueVersion).toBe(3)
      expect(
        controller.getState().queue.map((item) => item.queueItemId),
      ).toEqual(['first', 'second', 'third'])
      expect(remoteAdapter.getQueue()).toEqual(controller.getState().queue)
      expect(controller.getState().currentQueueItemId).toBe('first')
    })
    it.each(['clear', 'remove last'])(
      'TC-QC-08: %s ignores released-resource event tails while volume stays live',
      async (action) => {
        const controller = createPlaybackControlModule()
        const browserAdapter = createBrowserAudioAdapter()
        controller.setAdapter(browserAdapter)
        try {
          await controller.addQueueItems([
            { queueItemId: 'only', musicSrc: '/only.mp3' },
          ])
          await controller.play()
          await controller.seek(27)
          const audio = browserAdapter.getAudioElement()
          if (action === 'clear') await controller.clearQueue()
          else await controller.removeQueueItem('only')
          const released = controller.getState()
          expect(released.status).toBe(PlaybackStatus.IDLE)
          expect(released.currentQueueItemId).toBeNull()
          expect(released.position).toBe(0)
          expect(released.error).toBeNull()
          expect(audio.getAttribute('src')).toBeNull()
          const intents = controller.getMetrics().intentsIssued
          const plays = playSpy.mock.calls.length
          setMediaFlags(audio, {
            paused: true,
            ended: true,
            duration: 99,
            error: { code: 2, message: 'Released stream failed' },
          })
          audio.currentTime = 27
          await act(async () => {
            for (const name of [
              'pause',
              'timeupdate',
              'seeked',
              'loadedmetadata',
              'durationchange',
              'error',
              'ended',
              'playing',
            ]) {
              audio.dispatchEvent(new Event(name))
            }
          })

          expect(controller.getState()).toEqual(released)
          expect(controller.getMetrics().intentsIssued).toBe(intents)
          expect(playSpy.mock.calls.length).toBe(plays)
          await act(async () => {
            audio.volume = 0.4
            audio.dispatchEvent(new Event('volumechange'))
          })
          expect(controller.getState().volume).toBe(0.4)
          expect(controller.getState().status).toBe(PlaybackStatus.IDLE)
          expect(controller.getState().currentQueueItemId).toBeNull()
          expect(controller.getState().position).toBe(0)
          expect(controller.getState().error).toBeNull()
          expect(controller.getMetrics().intentsIssued).toBe(intents)
        } finally {
          controller.setAdapter(null)
        }
      },
    )
    it('TC-DE-02: legacy no-queue play and seek reopen media admission after release', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)
      try {
        await controller.clearQueue()
        const audio = browserAdapter.getAudioElement()
        audio.src = '/legacy-media.mp3'
        const intents = controller.getMetrics().intentsIssued
        await controller.play()
        await controller.seek(12)

        expect(controller.getState().queue).toEqual([])
        expect(controller.getState().currentQueueItemId).toBeNull()
        expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
        expect(controller.getState().position).toBe(12)
        expect(controller.getState().error).toBeNull()
        expect(controller.getMetrics().intentsIssued).toBe(intents + 2)
        audio.currentTime = 13
        audio.dispatchEvent(new Event('timeupdate'))
        expect(controller.getState().position).toBe(13)
        await controller.pause()
        expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      } finally {
        controller.setAdapter(null)
      }
    })
  })

  describe('Native Queue Editing Contract and Stable Instance Identity (Issue 03)', () => {
    let playSpy
    let pauseSpy
    let loadSpy

    const duplicateQueueTracks = [
      {
        queueItemId: 'q-dup-1',
        trackId: 'track-dup',
        name: 'Duplicate Song (Instance 1)',
        singer: 'Artist Alpha',
        musicSrc: 'http://localhost/dup.mp3',
        id: 'track-dup',
      },
      {
        queueItemId: 'q-mid-2',
        trackId: 'track-unique',
        name: 'Middle Unique Song',
        singer: 'Artist Beta',
        musicSrc: 'http://localhost/unique.mp3',
        id: 'track-unique',
      },
      {
        queueItemId: 'q-dup-3',
        trackId: 'track-dup',
        name: 'Duplicate Song (Instance 2)',
        singer: 'Artist Alpha',
        musicSrc: 'http://localhost/dup.mp3',
        id: 'track-dup',
      },
    ]

    beforeEach(() => {
      playSpy = vi
        .spyOn(window.HTMLMediaElement.prototype, 'play')
        .mockImplementation(() => Promise.resolve())
      pauseSpy = vi
        .spyOn(window.HTMLMediaElement.prototype, 'pause')
        .mockImplementation(() => {})
      loadSpy = vi
        .spyOn(window.HTMLMediaElement.prototype, 'load')
        .mockImplementation(() => {})
    })

    /**
     * Case: TC-QC-01
     * Requirement: US-5, Issue 03 lines 11-12
     * Initial Scene: Remote mode with duplicate track IDs in queue
     * Input Intent: Skip to second duplicate instance ('q-dup-3')
     * Expected State: Disambiguated by queueItemId across UI, Adapter, and wire payload
     */
    it('TC-QC-01: separates track identity from queue instance identity across UI, Adapter, and wire payload', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      let playerRef = null
      render(
        <UnifiedNativePlayer
          ref={(ref) => {
            playerRef = ref
          }}
          controller={controller}
          engineMode="remote"
          mode="full"
          autoPlay={false}
        />,
      )

      await controller.addQueueItems(duplicateQueueTracks)
      expect(controller.getState().queue).toHaveLength(3)
      expect(controller.getState().queue[0].trackId).toBe('track-dup')
      expect(controller.getState().queue[2].trackId).toBe('track-dup')
      expect(controller.getState().queue[0].queueItemId).toBe('q-dup-1')
      expect(controller.getState().queue[2].queueItemId).toBe('q-dup-3')

      // Skip specifically to duplicate instance 2 (q-dup-3)
      await controller.skipToQueueItem('q-dup-3')

      // Assert wire payload recorded queueItemId
      const wireCommands = remoteAdapter.getWireCommands()
      const skipCommand = wireCommands.find((c) => c.action === 'skip')
      expect(skipCommand).toBeDefined()
      expect(skipCommand.queueItemId).toBe('q-dup-3')

      // Assert authoritative state and UI playId
      expect(controller.getState().currentQueueItemId).toBe('q-dup-3')
      await waitFor(() => {
        expect(playerRef.state.playId).toBe('q-dup-3')
      })
    })

    /**
     * Case: TC-QC-02
     * Requirement: US-5, US-7, Issue 03 lines 11, 19
     * Initial Scene: Queue with duplicate tracks [q-dup-1, q-mid-2, q-dup-3]
     * Input Intent: Remove duplicate instance 2 ('q-dup-3')
     * Expected State: 'q-dup-3' removed, 'q-dup-1' untouched at index 0
     */
    it('TC-QC-02: deleting specific duplicate instance removes only that instance, leaving other instances untouched', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      await controller.addQueueItems(duplicateQueueTracks)
      await controller.removeQueueItem('q-dup-3')

      const wireCommands = remoteAdapter.getWireCommands()
      const removeCommand = wireCommands.find((c) => c.action === 'remove')
      expect(removeCommand).toBeDefined()
      expect(removeCommand.queueItemId).toBe('q-dup-3')

      const currentQueue = controller.getState().queue
      expect(currentQueue).toHaveLength(2)
      expect(currentQueue[0].queueItemId).toBe('q-dup-1')
      expect(currentQueue[0].trackId).toBe('track-dup')
      expect(currentQueue[1].queueItemId).toBe('q-mid-2')
      expect(currentQueue.some((item) => item.queueItemId === 'q-dup-3')).toBe(
        false,
      )
    })

    /**
     * Case: TC-QC-03
     * Requirement: US-7, Issue 03 line 16
     * Initial Scene: 3 items in queue, playing middle item 'q-mid-2'
     * Input Intent: Delete currently playing item
     * Expected State: Successor item 'q-dup-3' is selected across both engines
     */
    it('TC-QC-03: deleting current item selects its successor across both engines', async () => {
      for (const adapter of [
        createMockRemoteAudioAdapter(),
        createBrowserAudioAdapter(),
      ]) {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)

        await controller.addQueueItems(duplicateQueueTracks)
        await controller.skipToQueueItem('q-mid-2')
        expect(controller.getState().currentQueueItemId).toBe('q-mid-2')

        await controller.removeQueueItem('q-mid-2')
        expect(controller.getState().queue).toHaveLength(2)
        // Successor was index 2 ('q-dup-3'), now at index 1
        expect(controller.getState().currentQueueItemId).toBe('q-dup-3')
      }
    })

    /**
     * Case: TC-QC-04
     * Requirement: US-7, Issue 03 line 16
     * Initial Scene: 3 items in queue, playing end item 'q-dup-3'
     * Input Intent: Delete currently playing end item
     * Expected State: Selects remaining last item 'q-mid-2', does NOT loop to index 0 across both engines
     */
    it('TC-QC-04: deleting last item selects remaining last item without triggering loop across both engines', async () => {
      for (const adapter of [
        createMockRemoteAudioAdapter(),
        createBrowserAudioAdapter(),
      ]) {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)

        await controller.addQueueItems(duplicateQueueTracks)
        await controller.skipToQueueItem('q-dup-3')
        expect(controller.getState().currentQueueItemId).toBe('q-dup-3')

        await controller.removeQueueItem('q-dup-3')
        expect(controller.getState().queue).toHaveLength(2)
        // Must select remaining last item 'q-mid-2', not jump back to 'q-dup-1'
        expect(controller.getState().currentQueueItemId).toBe('q-mid-2')
      }
    })

    /**
     * Case: TC-QC-05
     * Requirement: US-8, Issue 03 lines 17, 18
     * Initial Scene: Local mode paused on 'q-mid-2'
     * Input Intent: Delete currently playing item while paused
     * Expected State: Status remains PAUSED throughout; audio.play() called 0 times
     */
    it('TC-QC-05: deleting current item while paused remains paused throughout with 0 play calls', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)

      await controller.addQueueItems(duplicateQueueTracks)
      await controller.skipToQueueItem('q-mid-2')
      await controller.pause()
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)

      playSpy.mockClear()
      await controller.removeQueueItem('q-mid-2')

      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(controller.getState().currentQueueItemId).toBe('q-dup-3')
      expect(playSpy).not.toHaveBeenCalled()
    })

    /**
     * Case: TC-QC-06
     * Requirement: US-6, Issue 03 lines 15, 18
     * Initial Scene: Local mode playing 'q-dup-1' at position 45s
     * Input Intent: Move current playing item from index 0 to index 2
     * Expected State: Playback identity continuous, audio.load() called 0 times, currentTime preserved
     */
    it('TC-QC-06: moving currently playing item keeps playing identity and does not reload audio', async () => {
      const controller = createPlaybackControlModule()
      const browserAdapter = createBrowserAudioAdapter()
      controller.setAdapter(browserAdapter)

      await controller.addQueueItems(duplicateQueueTracks)
      const audioEl = browserAdapter.getAudioElement()
      audioEl.currentTime = 45

      loadSpy.mockClear()
      await controller.moveQueueItem('q-dup-1', 2)

      const queue = controller.getState().queue
      expect(queue[2].queueItemId).toBe('q-dup-1')
      expect(controller.getState().currentQueueItemId).toBe('q-dup-1')
      expect(loadSpy).not.toHaveBeenCalled()
      expect(audioEl.currentTime).toBe(45)
    })

    /**
     * Case: TC-QC-07
     * Requirement: US-6, Issue 03 line 15
     * Initial Scene: Remote mode with 3 tracks
     * Input Intent: Move track 'q-dup-1' to index 2
     * Expected State: Emits wire payload with action 'move' and queueItemId, NOT full replace 'set'
     */
    it('TC-QC-07: remote move emits distinct move command with queueItemId, not full queue replacement', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      await controller.addQueueItems(duplicateQueueTracks)
      remoteAdapter.clearWireCommands()

      await controller.moveQueueItem('q-dup-1', 2)

      const wireCommands = remoteAdapter.getWireCommands()
      expect(wireCommands).toHaveLength(1)
      expect(wireCommands[0]).toMatchObject({
        action: 'move',
        queueItemId: 'q-dup-1',
        toIndex: 2,
      })
      expect(wireCommands.some((c) => c.action === 'set')).toBe(false)
    })

    /**
     * Case: TC-QC-08
     * Requirement: US-9, Issue 03 line 16
     * Initial Scene: Queue with tracks playing
     * Input Intent: Clear queue
     * Expected State: Queue empty, currentQueueItemId null, status IDLE across both engines
     */
    it('TC-QC-08: clearing queue stops playback, clears current item, and sets status to IDLE across both engines', async () => {
      for (const adapter of [
        createBrowserAudioAdapter(),
        createMockRemoteAudioAdapter(),
      ]) {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)

        await controller.addQueueItems(duplicateQueueTracks)
        expect(controller.getState().queue).toHaveLength(3)

        await controller.clearQueue()
        expect(controller.getState().queue).toHaveLength(0)
        expect(controller.getState().currentQueueItemId).toBeNull()
        expect(controller.getState().status).toBe(PlaybackStatus.IDLE)
      }
    })

    /**
     * Case: TC-QC-09
     * Requirement: US-2, Issue 03 lines 13, 14
     * Initial Scene: UnifiedNativePlayer rendered with controller in remote mode
     * Input Intent: User interacts through native queue drawer callbacks
     * Expected State: Intercepted and routed to controller with stable queueItemId
     */
    it('TC-QC-09: native drawer interactions route delete, move, skip, and clear through unified controller', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      render(
        <UnifiedNativePlayer
          controller={controller}
          engineMode="remote"
          mode="full"
          autoPlay={false}
        />,
      )

      await act(async () => controller.addQueueItems(duplicateQueueTracks))
      remoteAdapter.clearWireCommands()
      await act(async () => document.querySelector('.audio-lists-btn').click())

      // Delete through the real native row action.
      await act(async () => {
        document.querySelectorAll('.audio-item .player-delete')[2].click()
      })
      await waitFor(() => {
        const cmd = remoteAdapter
          .getWireCommands()
          .find((c) => c.action === 'remove')
        expect(cmd).toBeDefined()
        expect(cmd.queueItemId).toBe('q-dup-3')
      })

      // Sortable physically moves DOM and delivers one event object, not two indexes.
      const drag = startNativeDrag(0, 1)
      await drag.end()
      await waitFor(() => {
        const cmd = remoteAdapter
          .getWireCommands()
          .find((c) => c.action === 'move')
        expect(cmd).toBeDefined()
        expect(cmd.queueItemId).toBe('q-dup-1')
        expect(cmd.toIndex).toBe(1)
      })

      await act(async () => {
        document.querySelectorAll('.audio-item')[0].click()
      })
      await waitFor(() => {
        const cmd = remoteAdapter
          .getWireCommands()
          .find((c) => c.action === 'skip')
        expect(cmd).toBeDefined()
        expect(cmd.queueItemId).toBe('q-mid-2')
      })

      await act(async () => {
        document.querySelector('.audio-lists-panel-header-delete-btn').click()
      })
      await waitFor(() => {
        const cmd = remoteAdapter
          .getWireCommands()
          .find((c) => c.action === 'clear')
        expect(cmd).toBeDefined()
        expect(controller.getState().queue).toHaveLength(0)
      })
    })

    /**
     * Case: TC-QC-10
     * Requirement: US-4, Issue 03 lines 13, 14
     * Initial Scene: Host playlist actions (Play Next, Add to Queue, Play All)
     * Input Intent: Dispatch list operations
     * Expected State: Single intent issued per operation, correct insertion positions
     */
    it('TC-QC-10: Module list intents preserve single-intent and compound-command semantics', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      await controller.addQueueItems([
        duplicateQueueTracks[0],
        duplicateQueueTracks[1],
      ])
      const initialIntents = controller.getMetrics().intentsIssued

      // 1. Play Next
      await controller.playNext([
        {
          queueItemId: 'q-next',
          trackId: 'track-next',
          name: 'Play Next Song',
        },
      ])
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 1)
      expect(controller.getState().queue[1].queueItemId).toBe('q-next')

      // 2. Add to Queue
      await controller.addQueueItems([
        {
          queueItemId: 'q-end',
          trackId: 'track-end',
          name: 'Add To Queue Song',
        },
      ])
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 2)
      const q = controller.getState().queue
      expect(q[q.length - 1].queueItemId).toBe('q-end')

      // 3. Play All (compound intent)
      await controller.playAll(
        [
          { queueItemId: 'pa-1', trackId: 'pa-1', name: 'PA 1' },
          { queueItemId: 'pa-2', trackId: 'pa-2', name: 'PA 2' },
        ],
        0,
      )
      // Compound intent counted once
      expect(controller.getMetrics().intentsIssued).toBe(initialIntents + 3)
      expect(controller.getState().queue).toHaveLength(2)
      expect(controller.getState().currentQueueItemId).toBe('pa-1')
    })

    /**
     * Case: TC-QC-11
     * Requirement: US-23, Issue 03 lines 19, 20
     * Initial Scene: Controller active, server pushes authoritative snapshot, reconnect and stale snapshot replay
     * Input Intent: State backfill of queue, stale snapshot rejection, and reconnect replay
     * Expected State: UI and controller update; 0 commands sent to wire, 0 intents issued, stale snapshots rejected
     */
    it('TC-QC-11: external snapshot backfill, reconnect replay, and stale snapshot rejection produce zero echo', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      await controller.addQueueItems([duplicateQueueTracks[0]])
      remoteAdapter.clearWireCommands()
      const baselineIntents = controller.getMetrics().intentsIssued

      // 1. Server pushes authoritative snapshot with 3 items (including duplicate songs)
      controller.backfillState({
        queue: duplicateQueueTracks,
        currentQueueItemId: 'q-dup-3',
        queueVersion: 10,
      })

      expect(controller.getState().queue).toHaveLength(3)
      expect(controller.getState().currentQueueItemId).toBe('q-dup-3')
      expect(controller.getState().queueVersion).toBe(10)

      // Zero echo verification
      expect(controller.getMetrics().intentsIssued).toBe(baselineIntents)
      expect(remoteAdapter.getWireCommands()).toHaveLength(0)

      // 2. Stale snapshot rejection: an older snapshot (queueVersion: 5) is rejected
      controller.backfillState({
        queue: [duplicateQueueTracks[0]],
        currentQueueItemId: 'q-dup-1',
        queueVersion: 5,
      })
      // State remains at version 10 with 3 items
      expect(controller.getState().queue).toHaveLength(3)
      expect(controller.getState().currentQueueItemId).toBe('q-dup-3')
      expect(controller.getState().queueVersion).toBe(10)

      // 3. Reconnect replay: replayed snapshot accurately restores target duplicate item
      controller.backfillState({
        queue: duplicateQueueTracks,
        currentQueueItemId: 'q-dup-3',
        queueVersion: 11,
      })
      expect(controller.getState().currentQueueItemId).toBe('q-dup-3')
      expect(controller.getMetrics().intentsIssued).toBe(baselineIntents)
      expect(remoteAdapter.getWireCommands()).toHaveLength(0)
    })

    /**
     * Case: TC-QC-12
     * Requirement: US-24, Issue 03 line 21
     * Initial Scene: Common sequence executed on both BrowserAudioAdapter and MockRemoteAudioAdapter
     * Input Intent: add -> skip -> move -> remove
     * Expected State: Identical resulting queue and currentQueueItemId on both adapters
     */
    it('TC-QC-12: common behavioral contract produces identical state on both adapters', async () => {
      const runSequence = async (adapter) => {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)

        await controller.addQueueItems(duplicateQueueTracks)
        await controller.skipToQueueItem('q-mid-2')
        await controller.moveQueueItem('q-mid-2', 0)
        await controller.removeQueueItem('q-mid-2')

        return {
          queueIds: controller.getState().queue.map((item) => item.queueItemId),
          currentId: controller.getState().currentQueueItemId,
        }
      }

      const localResult = await runSequence(createBrowserAudioAdapter())
      const remoteResult = await runSequence(createMockRemoteAudioAdapter())

      expect(localResult.queueIds).toEqual(['q-dup-1', 'q-dup-3'])
      expect(localResult.currentId).toBe('q-dup-1') // Successor at index 0
      expect(remoteResult.queueIds).toEqual(localResult.queueIds)
      expect(remoteResult.currentId).toEqual(localResult.currentId)
    })
  })

  describe('Playback Mode Contract and Natural End Progression (Issue 04)', () => {
    const modeTracks = [
      {
        queueItemId: 'mode-1',
        trackId: 'track-1',
        name: 'First Mode Song',
        singer: 'Artist 1',
        musicSrc: 'http://localhost/song1.mp3',
        id: 'track-1',
      },
      {
        queueItemId: 'mode-2',
        trackId: 'track-2',
        name: 'Second Mode Song',
        singer: 'Artist 2',
        musicSrc: 'http://localhost/song2.mp3',
        id: 'track-2',
      },
      {
        queueItemId: 'mode-3',
        trackId: 'track-3',
        name: 'Third Mode Song',
        singer: 'Artist 3',
        musicSrc: 'http://localhost/song3.mp3',
        id: 'track-3',
      },
    ]

    /**
     * Case: TC-PM-01
     * Requirement: US-10, Issue 04 lines 11, 12
     * Scenario: Manual Next under order, orderLoop, and singleLoop modes across both engines
     * Expected State: All advance to next track; at queue end, all wrap around to index 0;
     *                 singleLoop does NOT prevent active song skipping.
     */
    it('TC-PM-01: manual Next advances and wraps around under order, orderLoop, and singleLoop on both engines', async () => {
      const testManualNext = async (adapter, isRemote = false) => {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)

        await controller.addQueueItems(modeTracks)
        expect(controller.getState().currentQueueItemId).toBe('mode-1')

        // 1. ORDER mode: advance, then wrap at end
        await controller.setPlayMode(PlaybackMode.ORDER)
        await controller.skipNext()
        expect(controller.getState().currentQueueItemId).toBe('mode-2')

        await controller.skipNext()
        expect(controller.getState().currentQueueItemId).toBe('mode-3')

        // Manual Next at last item wraps around to first item
        await controller.skipNext()
        expect(controller.getState().currentQueueItemId).toBe('mode-1')

        // 2. ORDER_LOOP mode: wrap at end
        await controller.setPlayMode(PlaybackMode.REPEAT_LIST)
        await controller.skipToQueueItem('mode-3')
        await controller.skipNext()
        expect(controller.getState().currentQueueItemId).toBe('mode-1')

        // 3. SINGLE_LOOP mode: does NOT block active skip!
        await controller.setPlayMode(PlaybackMode.REPEAT_TRACK)
        await controller.skipToQueueItem('mode-2')
        await controller.skipNext()
        expect(controller.getState().currentQueueItemId).toBe('mode-3')

        await controller.skipNext()
        expect(controller.getState().currentQueueItemId).toBe('mode-1')

        if (isRemote) {
          const wireCmds = adapter.getWireCommands()
          const nextCmds = wireCmds.filter((c) => c.action === 'skipNext')
          expect(nextCmds.length).toBe(6)
        }
      }

      const localAudio = document.createElement('audio')
      await testManualNext(
        createBrowserAudioAdapter({ audioElement: localAudio }),
        false,
      )
      await testManualNext(createMockRemoteAudioAdapter(), true)
    })

    /**
     * Case: TC-PM-02
     * Requirement: US-10, Issue 04 line 13
     * Scenario: Natural End in order mode across both engines
     * Expected State: Natural end advances before queue end, stops at queue end (does not loop)
     */
    it('TC-PM-02: natural end in order mode advances before end and stops at end of queue across both engines', async () => {
      // 1. Local Browser Audio Adapter: real DOM ended event
      const audioEl = document.createElement('audio')
      const localAdapter = createBrowserAudioAdapter({ audioElement: audioEl })
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.setPlayMode(PlaybackMode.ORDER)
      await localController.play()

      // Natural end on mode-1 -> advances to mode-2
      await act(async () => {
        finishMedia(audioEl)
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-2')
      expect(localController.getState().status).toBe(PlaybackStatus.PLAYING)

      // Natural end on mode-2 -> advances to mode-3
      await act(async () => {
        finishMedia(audioEl)
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-3')
      expect(localController.getState().status).toBe(PlaybackStatus.PLAYING)

      // Natural end on mode-3 (last) -> stops! Does NOT wrap around
      await act(async () => {
        finishMedia(audioEl)
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-3')
      expect(localController.getState().status).toBe(PlaybackStatus.PAUSED)

      // 2. Mock Remote Audio Adapter: autonomous server natural end
      const remoteAdapter = createMockRemoteAudioAdapter()
      const remoteController = createPlaybackControlModule()
      remoteController.setAdapter(remoteAdapter)
      await remoteController.addQueueItems(modeTracks)
      await remoteController.setPlayMode(PlaybackMode.ORDER)
      await remoteController.play()

      remoteAdapter.triggerNaturalEnd()
      expect(remoteController.getState().currentQueueItemId).toBe('mode-2')
      expect(remoteController.getState().status).toBe(PlaybackStatus.PLAYING)

      remoteAdapter.triggerNaturalEnd()
      expect(remoteController.getState().currentQueueItemId).toBe('mode-3')
      expect(remoteController.getState().status).toBe(PlaybackStatus.PLAYING)

      const stopResult = remoteAdapter.triggerNaturalEnd()
      expect(stopResult.stopped).toBe(true)
      expect(remoteController.getState().currentQueueItemId).toBe('mode-3')
      expect(remoteController.getState().status).toBe(PlaybackStatus.PAUSED)
    })

    /**
     * Case: TC-PM-03
     * Requirement: US-10, Issue 04 line 13
     * Scenario: Natural End in orderLoop mode across both engines
     * Expected State: Natural end on last item wraps around to first item and continues playing
     */
    it('TC-PM-03: natural end in orderLoop mode wraps from last item to first item across both engines', async () => {
      // 1. Local Browser Audio Adapter
      const audioEl = document.createElement('audio')
      const localAdapter = createBrowserAudioAdapter({ audioElement: audioEl })
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.setPlayMode(PlaybackMode.REPEAT_LIST)
      await localController.skipToQueueItem('mode-3')
      await localController.play()
      await act(async () => {
        finishMedia(audioEl)
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-1')
      expect(localController.getState().status).toBe(PlaybackStatus.PLAYING)

      // 2. Mock Remote Audio Adapter
      const remoteAdapter = createMockRemoteAudioAdapter()
      const remoteController = createPlaybackControlModule()
      remoteController.setAdapter(remoteAdapter)
      await remoteController.addQueueItems(modeTracks)
      await remoteController.setPlayMode(PlaybackMode.REPEAT_LIST)
      await remoteController.skipToQueueItem('mode-3')
      await remoteController.play()

      const res = remoteAdapter.triggerNaturalEnd()
      expect(res.stopped).toBe(false)
      expect(remoteController.getState().currentQueueItemId).toBe('mode-1')
      expect(remoteController.getState().status).toBe(PlaybackStatus.PLAYING)
    })

    /**
     * Case: TC-PM-04
     * Requirement: US-10, Issue 04 line 13
     * Scenario: Natural End in singleLoop mode across both engines
     * Expected State: Replays current item from 0s, remains PLAYING
     */
    it('TC-PM-04: natural end in singleLoop mode replays current item across both engines', async () => {
      // 1. Local Browser Audio Adapter
      const audioEl = document.createElement('audio')
      const localAdapter = createBrowserAudioAdapter({ audioElement: audioEl })
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.setPlayMode(PlaybackMode.REPEAT_TRACK)
      await localController.skipToQueueItem('mode-2')
      await localController.play()
      await act(async () => {
        finishMedia(audioEl)
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-2')
      expect(audioEl.currentTime).toBe(0)
      expect(localController.getState().status).toBe(PlaybackStatus.PLAYING)

      // 2. Mock Remote Audio Adapter
      const remoteAdapter = createMockRemoteAudioAdapter()
      const remoteController = createPlaybackControlModule()
      remoteController.setAdapter(remoteAdapter)
      await remoteController.addQueueItems(modeTracks)
      await remoteController.setPlayMode(PlaybackMode.REPEAT_TRACK)
      await remoteController.skipToQueueItem('mode-2')
      await remoteController.play()

      const res = remoteAdapter.triggerNaturalEnd()
      expect(res.stopped).toBe(false)
      expect(remoteController.getState().currentQueueItemId).toBe('mode-2')
      expect(remoteController.getState().position).toBe(0)
      expect(remoteController.getState().status).toBe(PlaybackStatus.PLAYING)
    })

    /**
     * Case: TC-PM-05
     * Requirement: US-3, US-10, Issue 04 line 15
     * Scenario: Manual Prev threshold (>1.0s rewinds to 0s; <=1.0s moves to prev with wrap-around)
     * Expected State: Threshold behavior honored on both engines
     */
    it('TC-PM-05: manual Prev rewinds when currentTime > 1.0s, otherwise moves to previous with wrap-around', async () => {
      // 1. Local Browser Audio Adapter
      const audioEl = document.createElement('audio')
      const localAdapter = createBrowserAudioAdapter({ audioElement: audioEl })
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.skipToQueueItem('mode-2')

      // Progress > 1s -> rewind only
      audioEl.currentTime = 15.0
      await localController.skipPrev()
      expect(audioEl.currentTime).toBe(0)
      expect(localController.getState().currentQueueItemId).toBe('mode-2')

      // Progress <= 1s -> switches to previous track (mode-1)
      audioEl.currentTime = 0.5
      await localController.skipPrev()
      expect(localController.getState().currentQueueItemId).toBe('mode-1')

      // At mode-1, progress <= 1s -> wraps around to last item (mode-3)
      audioEl.currentTime = 0.0
      await localController.skipPrev()
      expect(localController.getState().currentQueueItemId).toBe('mode-3')

      // 2. Mock Remote Audio Adapter
      const remoteAdapter = createMockRemoteAudioAdapter({
        initialPosition: 20.0,
      })
      const remoteController = createPlaybackControlModule()
      remoteController.setAdapter(remoteAdapter)
      await remoteController.addQueueItems(modeTracks)
      await remoteController.skipToQueueItem('mode-2')

      // Position > 1s -> rewind
      remoteAdapter.pushServerSnapshot({ position: 20.0 })
      await remoteController.skipPrev()
      expect(remoteController.getState().position).toBe(0)
      expect(remoteController.getState().currentQueueItemId).toBe('mode-2')

      // Position <= 1s -> previous track
      remoteAdapter.pushServerSnapshot({ position: 0.5 })
      await remoteController.skipPrev()
      expect(remoteController.getState().currentQueueItemId).toBe('mode-1')

      // Wrap around at first track
      remoteAdapter.pushServerSnapshot({ position: 0.0 })
      await remoteController.skipPrev()
      expect(remoteController.getState().currentQueueItemId).toBe('mode-3')
    })

    /**
     * Case: TC-PM-06
     * Requirement: US-11, US-12, Issue 04 lines 16, 17
     * Scenario: Shuffle mode selection without reordering visible queue, excluding current item
     * Expected State: Deterministic selection via PRNG, zero visible queue mutation, no history stack
     */
    it('TC-PM-06: random mode does not mutate visible queue order, excludes current item, and uses deterministic PRNG', async () => {
      // Deterministic PRNG: returns 0 (picks first candidate)
      const mockRandom = vi.fn().mockReturnValue(0)

      const adapter = createMockRemoteAudioAdapter({ randomFn: mockRandom })
      const controller = createPlaybackControlModule()
      controller.setAdapter(adapter)

      await controller.addQueueItems(modeTracks)
      await controller.setPlayMode(PlaybackMode.SHUFFLE)
      await controller.skipToQueueItem('mode-2')

      // Current is mode-2. Candidates are [mode-1, mode-3].
      // mockRandom returns 0 -> picks candidates[0] = mode-1.
      await controller.skipNext()
      expect(controller.getState().currentQueueItemId).toBe('mode-1')

      // Crucial: visible queue sequence and length must NOT be mutated!
      const currentQueueIds = controller
        .getState()
        .queue.map((item) => item.queueItemId)
      expect(currentQueueIds).toEqual(['mode-1', 'mode-2', 'mode-3'])

      // Next pick: current is mode-1. Candidates are [mode-2, mode-3].
      // Return 0.99 -> picks candidates[1] = mode-3.
      mockRandom.mockReturnValue(0.99)
      await controller.skipNext()
      expect(controller.getState().currentQueueItemId).toBe('mode-3')
      expect(
        controller.getState().queue.map((item) => item.queueItemId),
      ).toEqual(['mode-1', 'mode-2', 'mode-3'])

      // Prev threshold in shuffle mode: > 1s rewinds, does not pick random
      adapter.pushServerSnapshot({ position: 5.0 })
      await controller.skipPrev()
      expect(controller.getState().position).toBe(0)
      expect(controller.getState().currentQueueItemId).toBe('mode-3')

      // Prev threshold <= 1s: picks random from candidates [mode-1, mode-2]
      mockRandom.mockReturnValue(0)
      adapter.pushServerSnapshot({ position: 0.2 })
      await controller.skipPrev()
      expect(controller.getState().currentQueueItemId).toBe('mode-1')
    })

    /**
     * Case: TC-PM-07
     * Requirement: US-10, US-18, Issue 04 line 18
     * Scenario: Four mutually exclusive modes and native UI mode toggle cycling
     * Expected State: Cycles order -> orderLoop -> singleLoop -> shufflePlay -> order;
     *                 backfill updates UI without echoing commands.
     */
    it('TC-PM-07: four mutually exclusive modes and native UI mode toggle cycling', async () => {
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      let playerRef = null
      render(
        <UnifiedNativePlayer
          ref={(r) => {
            playerRef = r
          }}
          controller={controller}
          engineMode="remote"
          audioLists={modeTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
        />,
      )

      await waitFor(() => {
        expect(playerRef).not.toBeNull()
      })

      expect(controller.getState().playMode).toBe(PlaybackMode.ORDER)
      expect(playerRef.state.playMode).toBe(PlaybackMode.ORDER)

      // Cycle 1: order -> orderLoop
      await act(async () => {
        playerRef.togglePlayMode()
      })
      await waitFor(() => {
        expect(controller.getState().playMode).toBe(PlaybackMode.REPEAT_LIST)
      })
      expect(playerRef.state.playMode).toBe(PlaybackMode.REPEAT_LIST)

      // Cycle 2: orderLoop -> singleLoop
      await act(async () => {
        playerRef.togglePlayMode()
      })
      await waitFor(() => {
        expect(controller.getState().playMode).toBe(PlaybackMode.REPEAT_TRACK)
      })
      expect(playerRef.state.playMode).toBe(PlaybackMode.REPEAT_TRACK)

      // Cycle 3: singleLoop -> shufflePlay
      await act(async () => {
        playerRef.togglePlayMode()
      })
      await waitFor(() => {
        expect(controller.getState().playMode).toBe(PlaybackMode.SHUFFLE)
      })
      expect(playerRef.state.playMode).toBe(PlaybackMode.SHUFFLE)

      // Cycle 4: shufflePlay -> order (full wrap-around)
      await act(async () => {
        playerRef.togglePlayMode()
      })
      await waitFor(() => {
        expect(controller.getState().playMode).toBe(PlaybackMode.ORDER)
      })
      expect(playerRef.state.playMode).toBe(PlaybackMode.ORDER)

      // External snapshot backfill: updates playerRef state without echoing commands
      const commandsBefore = controller.getMetrics().intentsIssued
      act(() => {
        controller.backfillState({ playMode: PlaybackMode.REPEAT_TRACK })
      })
      expect(playerRef.state.playMode).toBe(PlaybackMode.REPEAT_TRACK)
      expect(controller.getMetrics().intentsIssued).toBe(commandsBefore)
    })

    /**
     * Case: TC-PM-08
     * Requirement: US-13, Issue 04 line 19
     * Scenario: Autonomous remote progression continues when UI disconnects; reconnects cleanly
     * Expected State: Server advances autonomously; reconnected UI recovers state; zero browser ended compensation
     */
    it('TC-PM-08: autonomous remote progression continues when UI disconnects; reconnects cleanly with zero ended compensation commands', async () => {
      const remoteAdapter = createMockRemoteAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(remoteAdapter)

      await controller.addQueueItems(modeTracks)
      await controller.setPlayMode(PlaybackMode.REPEAT_LIST)
      expect(controller.getState().currentQueueItemId).toBe('mode-1')

      // Step 1: UI mounts and binds to controller
      const { unmount } = render(
        <UnifiedNativePlayer
          controller={controller}
          engineMode="remote"
          audioLists={modeTracks}
          playIndex={0}
          mode="full"
          autoPlay={false}
        />,
      )

      await waitFor(() => {
        expect(document.querySelector('.play-btn')).not.toBeNull()
      })

      // Step 2: User closes tab / disconnects UI
      unmount()

      // Step 3: Remote server finishes songs and advances autonomously without browser being online
      remoteAdapter.triggerNaturalEnd() // mode-1 -> mode-2
      remoteAdapter.triggerNaturalEnd() // mode-2 -> mode-3
      expect(remoteAdapter.getCurrentQueueItemId()).toBe('mode-3')

      // Step 4: UI reconnects
      remoteAdapter.clearWireCommands()
      let reconnectedPlayer = null
      render(
        <UnifiedNativePlayer
          ref={(r) => {
            reconnectedPlayer = r
          }}
          controller={controller}
          engineMode="remote"
          audioLists={modeTracks}
          mode="full"
          autoPlay={false}
        />,
      )

      await waitFor(() => {
        expect(reconnectedPlayer).not.toBeNull()
      })

      // UI immediately displays remote authority's current track (mode-3)
      expect(reconnectedPlayer.state.playId).toBe('mode-3')

      // Guarantee: zero <audio> elements in DOM, zero ended compensation commands sent
      expect(document.querySelector('audio')).toBeNull()
      const endedCommands = remoteAdapter
        .getWireCommands()
        .filter((c) => c.action === 'ended')
      expect(endedCommands.length).toBe(0)
    })

    /**
     * Case: TC-PM-09
     * Requirement: US-10, Issue 04 line 21
     * Scenario: Boundary conditions for empty queue and single-item queue
     * Expected State: Safe no-ops on empty queue; single-item replay/stop rules respected
     */
    it('TC-PM-09: handles boundary cases cleanly (empty queue & single-item queue) across both engines', async () => {
      // 1. Empty queue boundary
      const controller = createPlaybackControlModule()
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      await controller.clearQueue()
      await expect(controller.skipNext()).resolves.toBeUndefined()
      await expect(controller.skipPrev()).resolves.toBeUndefined()
      expect(controller.getState().status).toBe(PlaybackStatus.IDLE)

      // 2. Single-item queue boundary
      await controller.addQueueItems([modeTracks[0]])
      expect(controller.getState().currentQueueItemId).toBe('mode-1')

      // Manual skip wraps/stays on single item
      await controller.skipNext()
      expect(controller.getState().currentQueueItemId).toBe('mode-1')
      await controller.skipPrev()
      expect(controller.getState().currentQueueItemId).toBe('mode-1')

      // Natural end on single item:
      // In ORDER mode -> stops
      await controller.setPlayMode(PlaybackMode.ORDER)
      const stopRes = remoteAdapter.triggerNaturalEnd()
      expect(stopRes.stopped).toBe(true)

      // In REPEAT_LIST mode -> replays
      await controller.setPlayMode(PlaybackMode.REPEAT_LIST)
      const listRes = remoteAdapter.triggerNaturalEnd()
      expect(listRes.stopped).toBe(false)
      expect(controller.getState().currentQueueItemId).toBe('mode-1')

      // In SHUFFLE mode -> replays (single item allows replay)
      await controller.setPlayMode(PlaybackMode.SHUFFLE)
      const shuffleRes = remoteAdapter.triggerNaturalEnd()
      expect(shuffleRes.stopped).toBe(false)
      expect(controller.getState().currentQueueItemId).toBe('mode-1')
    })

    /**
     * Case: TC-PM-10
     * Requirement: US-24, Issue 04 lines 11, 23
     * Scenario: Common behavioral mode contract sequence executed across both engines
     * Expected State: Both engines produce 100% identical state transitions and final track ID
     */
    it('TC-PM-10: common behavioral mode contract produces identical state on both adapters', async () => {
      const deterministicRandom = () => 0

      const runModeSequence = async (adapter) => {
        const controller = createPlaybackControlModule()
        controller.setAdapter(adapter)

        await controller.addQueueItems(modeTracks)

        // 1. Order mode next
        await controller.setPlayMode(PlaybackMode.ORDER)
        await controller.skipNext() // mode-2
        const s1 = controller.getState().currentQueueItemId

        // 2. Order loop next past end
        await controller.setPlayMode(PlaybackMode.REPEAT_LIST)
        await controller.skipNext() // mode-3
        await controller.skipNext() // mode-1 (wrapped)
        const s2 = controller.getState().currentQueueItemId

        // 3. Single loop manual skip (must not be blocked)
        await controller.setPlayMode(PlaybackMode.REPEAT_TRACK)
        await controller.skipNext() // mode-2
        const s3 = controller.getState().currentQueueItemId

        // 4. Skip prev > 1s threshold (rewind only)
        if (adapter.getAudioElement) {
          adapter.getAudioElement().currentTime = 10.0
        } else {
          adapter.pushServerSnapshot({ position: 10.0 })
        }
        await controller.skipPrev()
        const s4 = controller.getState().currentQueueItemId
        const pos4 = controller.getState().position

        // 5. Skip prev <= 1s threshold (moves to mode-1)
        if (adapter.getAudioElement) {
          adapter.getAudioElement().currentTime = 0.2
        } else {
          adapter.pushServerSnapshot({ position: 0.2 })
        }
        await controller.skipPrev()
        const s5 = controller.getState().currentQueueItemId

        return {
          steps: [s1, s2, s3, s4, s5],
          pos4,
          finalMode: controller.getState().playMode,
        }
      }

      const localAudio = document.createElement('audio')
      const localRes = await runModeSequence(
        createBrowserAudioAdapter({
          audioElement: localAudio,
          randomFn: deterministicRandom,
        }),
      )
      const remoteRes = await runModeSequence(
        createMockRemoteAudioAdapter({ randomFn: deterministicRandom }),
      )

      expect(localRes.steps).toEqual([
        'mode-2',
        'mode-1',
        'mode-2',
        'mode-2',
        'mode-1',
      ])
      expect(localRes.pos4).toBe(0)
      expect(localRes.finalMode).toBe(PlaybackMode.REPEAT_TRACK)

      expect(remoteRes.steps).toEqual(localRes.steps)
      expect(remoteRes.pos4).toBe(localRes.pos4)
      expect(remoteRes.finalMode).toBe(localRes.finalMode)
    })
  })

  describe('Safe Output Migration and Takeover Protocol (Issue 06)', () => {
    it('TC-MIG-01: minimal sequential playback scene migration (local -> remote -> local) preserves queue, current item, position, and playing state', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      const migrationTracks = [
        {
          queueItemId: 'mig-1',
          trackId: 't1',
          name: 'Song 1',
          musicSrc: 'http://example.com/1.mp3',
        },
        {
          queueItemId: 'mig-2',
          trackId: 't2',
          name: 'Song 2',
          musicSrc: 'http://example.com/2.mp3',
        },
        {
          queueItemId: 'mig-3',
          trackId: 't3',
          name: 'Song 3',
          musicSrc: 'http://example.com/3.mp3',
        },
      ]

      await controller.addQueueItems(migrationTracks)
      await controller.skipToQueueItem('mig-2')
      await controller.setVolume(0.8)
      await controller.seek(17)
      await controller.play()

      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controller.getState().currentQueueItemId).toBe('mig-2')
      expect(controller.getState().position).toBe(17)
      expect(controller.getState().volume).toBe(0.8)

      // Migrate from Local to Remote
      const remoteAdapter = createMockRemoteAudioAdapter()
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Remote Speaker' },
      })

      // Invariants:
      // 1. Local audio is paused
      expect(audio.paused).toBe(true)
      // 2. Remote adapter preserves scene: queue, current item, position, volume, and playing state
      const remoteState = controller.getState()
      expect(remoteState.status).toBe(PlaybackStatus.PLAYING)
      expect(remoteState.currentQueueItemId).toBe('mig-2')
      expect(remoteState.position).toBe(17)
      expect(remoteState.volume).toBe(0.8)
      expect(remoteState.queue).toHaveLength(3)

      // Remote advances naturally to 35s
      remoteAdapter.pushServerSnapshot({ position: 35 })
      expect(controller.getState().position).toBe(35)

      // Migrate from Remote back to Local
      const nextBrowserAdapter = createBrowserAudioAdapter()
      const nextAudio = document.createElement('audio')
      nextBrowserAdapter.attachElement(nextAudio)
      setMediaFlags(nextAudio, { paused: false, ended: false })

      await controller.migrateOutput({
        targetAdapter: nextBrowserAdapter,
        targetDevice: { name: 'Local' },
      })

      // Invariants:
      // 1. Local adapter received migrated scene
      const localState = controller.getState()
      expect(localState.status).toBe(PlaybackStatus.PLAYING)
      expect(localState.currentQueueItemId).toBe('mig-2')
      expect(localState.position).toBe(35)
      expect(localState.volume).toBe(0.8)
      expect(localState.queue).toHaveLength(3)

      // 2. Old remote adapter was deactivated
      expect(remoteAdapter.getRemoteState().status).toBe(PlaybackStatus.IDLE)
    })

    it('TC-MIG-02: paused playback scene migration stays PAUSED throughout with 0 play calls', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: true, ended: false })

      const migrationTracks = [
        {
          queueItemId: 'mig-1',
          trackId: 't1',
          name: 'Song 1',
          musicSrc: 'http://example.com/1.mp3',
        },
        {
          queueItemId: 'mig-2',
          trackId: 't2',
          name: 'Song 2',
          musicSrc: 'http://example.com/2.mp3',
        },
      ]

      await controller.addQueueItems(migrationTracks)
      await controller.skipToQueueItem('mig-2')
      await controller.seek(12)
      await controller.pause()

      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(controller.getState().position).toBe(12)

      const remoteAdapter = createMockRemoteAudioAdapter()
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Remote Speaker' },
      })

      // Invariants:
      // 1. Target remains PAUSED
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(controller.getState().position).toBe(12)
      expect(controller.getState().currentQueueItemId).toBe('mig-2')
      // 2. Target play() was called 0 times
      expect(remoteAdapter.getPlayCount()).toBe(0)
    })

    it('TC-MIG-03: target device occupied requires prompt and explicit confirmation before any overwrite action', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      await controller.addQueueItems([
        {
          queueItemId: 'loc-1',
          trackId: 't-loc',
          name: 'Local Track',
          musicSrc: 'http://example.com/l.mp3',
        },
      ])
      await controller.seek(5)
      await controller.play()

      // Target remote device has an active session from another client
      const remoteAdapter = createMockRemoteAudioAdapter({
        hasActiveSession: true,
        activeSessionOwner: 'other-user',
        initialPosition: 40,
      })
      remoteAdapter.pushServerSnapshot({
        queue: [
          { queueItemId: 'rem-1', trackId: 't-rem', name: 'Remote Track' },
        ],
        currentQueueItemId: 'rem-1',
        position: 40,
        status: PlaybackStatus.PLAYING,
      })

      // Sub-case A: Confirmation callback returns false (user cancels/rejects)
      const requestConfirmationCancel = vi.fn(async () => false)
      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: { name: 'Living Room TV', hasActiveSession: true },
          requestConfirmation: requestConfirmationCancel,
        }),
      ).rejects.toMatchObject({
        code: 'TARGET_CONFIRMATION_REJECTED',
      })

      expect(requestConfirmationCancel).toHaveBeenCalledTimes(1)
      // Remote scene is 100% untouched: still playing remote track at 40s
      expect(remoteAdapter.getQueue()).toHaveLength(1)
      expect(remoteAdapter.getCurrentQueueItemId()).toBe('rem-1')
      expect(remoteAdapter.getRemoteState().position).toBe(40)
      expect(remoteAdapter.getStopCount()).toBe(0)

      // Local player continues playing undisturbed
      expect(controller.getState().currentQueueItemId).toBe('loc-1')
      expect(audio.paused).toBe(false)

      // Sub-case B: No confirmation callback and not forceTakeover -> TARGET_OCCUPIED
      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: { name: 'Living Room TV', hasActiveSession: true },
        }),
      ).rejects.toMatchObject({
        code: 'TARGET_OCCUPIED',
      })
      expect(remoteAdapter.getQueue()).toHaveLength(1)

      // Sub-case C: User confirms explicit takeover
      const requestConfirmationConfirm = vi.fn(async () => true)
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Living Room TV', hasActiveSession: true },
        requestConfirmation: requestConfirmationConfirm,
      })

      expect(requestConfirmationConfirm).toHaveBeenCalledTimes(1)
      expect(controller.getState().currentQueueItemId).toBe('loc-1')
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
    })

    it('TC-MIG-04: old device stop confirmation failure prevents target startup (no parallel output)', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      await controller.addQueueItems([
        {
          queueItemId: 'loc-1',
          trackId: 't1',
          name: 'Local Track',
          musicSrc: 'http://example.com/1.mp3',
        },
      ])
      await controller.play()

      // Inject stop failure on old adapter
      localAdapter.stop = vi.fn(async () => {
        throw {
          code: 'AUDIO_DEVICE_LOCKED',
          message: 'Hardware audio device locked',
        }
      })

      const remoteAdapter = createMockRemoteAudioAdapter()

      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: { name: 'Remote Speaker' },
        }),
      ).rejects.toMatchObject({
        code: 'AUDIO_DEVICE_LOCKED',
      })

      // Invariants:
      // 1. Target remote adapter play was NEVER called (zero sound from target)
      expect(remoteAdapter.getPlayCount()).toBe(0)
      // 2. Error is recorded on controller state
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(controller.getState().error.code).toBe('AUDIO_DEVICE_LOCKED')

      // Sub-case B: Stop timeout prevents target startup
      const remoteAdapterTimeout = createMockRemoteAudioAdapter()
      localAdapter.stop = vi.fn(
        () =>
          new Promise((_, reject) =>
            setTimeout(() => reject({ code: 'COMMAND_TIMEOUT' }), 10),
          ),
      )
      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapterTimeout,
          targetDevice: { name: 'Remote Speaker' },
        }),
      ).rejects.toMatchObject({
        code: 'COMMAND_TIMEOUT',
      })
      expect(remoteAdapterTimeout.getPlayCount()).toBe(0)
    })

    it('TC-MIG-05: target volume setting failure prevents target startup (preserves unstarted scene, no local fallback)', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      await controller.addQueueItems([
        {
          queueItemId: 'loc-1',
          trackId: 't1',
          name: 'Track 1',
          musicSrc: 'http://example.com/1.mp3',
        },
      ])
      await controller.seek(20)
      await controller.play()

      const remoteAdapter = createMockRemoteAudioAdapter()
      remoteAdapter.setFailure({
        action: 'volume',
        code: 'MPV_SET_VOLUME_IPC_ERROR',
        message: 'IPC error: set_property volume failed',
      })

      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: { name: 'Remote Speaker' },
        }),
      ).rejects.toMatchObject({
        code: 'MPV_SET_VOLUME_IPC_ERROR',
      })

      // Invariants:
      // 1. Target play() was NEVER called
      expect(remoteAdapter.getPlayCount()).toBe(0)
      // 2. Failed scene is preserved and visible on target
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(controller.getState().currentQueueItemId).toBe('loc-1')
      expect(controller.getState().position).toBe(20)
      expect(controller.getState().queue).toHaveLength(1)
      // 3. Local audio is not resurrected / no local fallback
      expect(audio.paused).toBe(true)
    })

    it('TC-MIG-06: three volume paths (no active track, prepared paused track, running track) with target device profile mapping', async () => {
      const controller = createPlaybackControlModule()
      const localAdapter = createBrowserAudioAdapter()
      controller.setAdapter(localAdapter)

      // Path 1: No active track - saves desired volume
      expect(controller.getState().queue).toHaveLength(0)
      await controller.setVolume(0.75)
      expect(controller.getState().volume).toBe(0.75)

      // Add track and prepare
      await controller.addQueueItems([
        {
          queueItemId: 'track-1',
          trackId: 't1',
          name: 'Song 1',
          musicSrc: 'http://example.com/1.mp3',
        },
      ])

      // Path 2: Prepared track during migration to Bluetooth device
      // Maps volume 0.64 through Bluetooth profile exponent mapping
      const remoteAdapter = createMockRemoteAudioAdapter()
      await controller.setVolume(0.64)
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: {
          name: 'BT Headset',
          deviceName: 'pulse/bluez_sink',
          isBluetooth: true,
        },
      })

      // Applied volume on remote adapter was mapped for Bluetooth
      const expectedBtGain = audioVolumeToRemoteGain(0.64, {
        deviceName: 'pulse/bluez_sink',
        isBluetooth: true,
      })
      expect(remoteAdapter.getAppliedVolume()).toBeCloseTo(expectedBtGain, 3)

      // Path 3: Running track modification
      await controller.play()
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      await controller.setVolume(0.5)
      expect(controller.getState().volume).toBe(0.5)
    })

    it('TC-MIG-07: same-owner late command across migration cannot mutate new scene or inject error', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      await controller.addQueueItems([
        {
          queueItemId: 'old-1',
          trackId: 't1',
          name: 'Song 1',
          musicSrc: 'http://example.com/1.mp3',
        },
      ])
      await controller.play()

      // Start a slow command on localAdapter that will resolve late
      let completeOldSeek
      const oldSeekPromise = new Promise((resolve) => {
        completeOldSeek = resolve
      })
      const originalSeek = localAdapter.seek
      localAdapter.seek = vi.fn(async (...args) => {
        await oldSeekPromise
        return originalSeek.apply(localAdapter, args)
      })

      // Issue seek on old adapter
      const seekPromise = controller.seek(99)

      // Migrate to new remote adapter while seek is still in-flight
      const remoteAdapter = createMockRemoteAudioAdapter()
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Remote Speaker' },
      })

      expect(controller.getState().position).not.toBe(99)

      // Release the late old seek (which rejects or finishes with COMMAND_SUPERSEDED)
      completeOldSeek()
      try {
        await seekPromise
      } catch {
        // Expected superseded rejection
      }

      // New remote scene remains completely unaffected
      expect(controller.getState().position).not.toBe(99)
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controller.getState().error).toBeNull()
    })

    it('TC-MIG-08: compound intent (playAll) interrupted by migration stops cleanly without writing to new adapter', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      let finishClear
      localAdapter.clearQueue = vi.fn(
        () =>
          new Promise((resolve) => {
            finishClear = resolve
          }),
      )

      // Start playAll which begins with clearQueue
      const playAllPromise = controller.playAll([
        { queueItemId: 'item-1', trackId: 't1', name: 'Item 1' },
        { queueItemId: 'item-2', trackId: 't2', name: 'Item 2' },
      ])

      // Switch to new adapter while playAll is waiting on clearQueue
      const remoteAdapter = createMockRemoteAudioAdapter()
      controller.setAdapter(remoteAdapter)

      // Finish old clearQueue
      finishClear()
      await expect(playAllPromise).rejects.toMatchObject({
        code: 'COMMAND_SUPERSEDED',
      })

      // Remote adapter did NOT receive the addQueueItems or play from the old playAll
      expect(remoteAdapter.getQueue()).toHaveLength(0)
      expect(remoteAdapter.getPlayCount()).toBe(0)
    })

    it('TC-MIG-09: target adapter lower queueVersion is legally accepted as new baseline', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      // Advance old adapter's queueVersion to 5
      await controller.addQueueItems([{ queueItemId: 'q1', trackId: 't1' }])
      await controller.addQueueItems([{ queueItemId: 'q2', trackId: 't2' }])
      await controller.addQueueItems([{ queueItemId: 'q3', trackId: 't3' }])
      expect(controller.getState().queueVersion).toBeGreaterThanOrEqual(3)
      await controller.clearQueue()
      expect(controller.getState().queue).toHaveLength(0)

      // Migrate to fresh remote adapter which starts with queueVersion = 0
      const remoteAdapter = createMockRemoteAudioAdapter()
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
      })

      // Push authoritative update with queueVersion 1 from the new adapter
      remoteAdapter.pushServerSnapshot({
        queue: [{ queueItemId: 'fresh-1', trackId: 'f1', name: 'Fresh 1' }],
        currentQueueItemId: 'fresh-1',
        queueVersion: 1,
      })

      // Module accepts the new adapter's lower queueVersion as authoritative
      expect(controller.getState().queueVersion).toBe(1)
      expect(controller.getState().currentQueueItemId).toBe('fresh-1')
      expect(controller.getState().queue[0].queueItemId).toBe('fresh-1')
    })

    it('TC-MIG-10: shared remote output single-controller rule & explicit takeover vs recovering owner re-attachment', async () => {
      // Remote adapter owned by 'client-A'
      const remoteAdapter = createMockRemoteAudioAdapter({
        hasActiveSession: true,
        activeSessionOwner: 'client-A',
        initialPosition: 25,
      })
      remoteAdapter.pushServerSnapshot({
        queue: [
          { queueItemId: 'q-rem', trackId: 't-rem', name: 'Remote Track' },
        ],
        currentQueueItemId: 'q-rem',
        position: 25,
        status: PlaybackStatus.PLAYING,
      })

      // 1. Observer client-B attempts normal attach without takeover -> rejected
      const observerController = createPlaybackControlModule()
      await expect(
        observerController.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: {
            hasActiveSession: true,
            activeController: 'client-A',
            clientId: 'client-B',
          },
        }),
      ).rejects.toMatchObject({
        code: 'TARGET_OCCUPIED',
      })

      // 2. Recovering owner client-A reattaches after disconnect gap -> succeeds without takeover
      const ownerController = createPlaybackControlModule()
      await ownerController.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: {
          hasActiveSession: true,
          activeController: 'client-A',
          clientId: 'client-A',
        },
      })
      // Remote authoritative scene continues at 25s without rewinding
      expect(ownerController.getState().position).toBe(25)
      expect(ownerController.getState().currentQueueItemId).toBe('q-rem')

      // 3. Client-B explicitly takes over -> succeeds and continues without rewinding
      await observerController.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: {
          hasActiveSession: true,
          activeController: 'client-A',
          clientId: 'client-B',
        },
        forceTakeover: true,
      })
      expect(observerController.getState().position).toBe(25)
      expect(remoteAdapter.getActiveSessionOwner()).toBe('client-B')

      // 4. Revoked old owner (client-A) subsequent normal migrations are rejected
      await expect(
        ownerController.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: {
            hasActiveSession: true,
            activeController: 'client-B',
            clientId: 'client-A',
          },
        }),
      ).rejects.toMatchObject({
        code: 'TARGET_OCCUPIED',
      })
      expect(remoteAdapter.getActiveSessionOwner()).toBe('client-B')

      // 5. Lease/heartbeat gap does not stop remote playback
      remoteAdapter.pushServerSnapshot({ position: 28 })
      expect(observerController.getState().position).toBe(28)
      expect(observerController.getState().status).toBe(PlaybackStatus.PLAYING)
    })

    it('TC-MIG-12: empty queue paused migration sets PAUSED status (not stuck in PREPARING)', async () => {
      const controller = createPlaybackControlModule()
      const localAdapter = createBrowserAudioAdapter()
      controller.setAdapter(localAdapter)
      await controller.pause()
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)

      const remoteAdapter = createMockRemoteAudioAdapter()
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Remote Speaker' },
      })
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(remoteAdapter.getPlayCount()).toBe(0)
    })

    it('TC-MIG-11: full round trips (local -> remote -> local -> remote -> local) on UnifiedNativePlayer with visible play rejection', async () => {
      const ref = React.createRef()
      const { controller, rerenderPlayer, getByRole, unmount } =
        await renderControlledPlayer('local', { ref })

      const player = ref.current
      expect(document.querySelector('audio')).not.toBeNull()

      // Initial local play
      await act(async () => document.querySelector('.play-btn').click())
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)

      // Round trip 1: local -> remote
      const remoteAdapter1 = createMockRemoteAudioAdapter()
      await act(async () =>
        controller.migrateOutput({ targetAdapter: remoteAdapter1 }),
      )
      rerenderPlayer({ engineMode: 'remote', browserAdapter: undefined })
      expect(document.querySelector('audio')).toBeNull()
      expect(ref.current).toBe(player)
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)

      // Remote toggle play -> pause
      await act(async () => document.querySelector('.play-btn').click())
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)

      // Round trip 1: remote -> local
      const nextBrowser1 = createBrowserAudioAdapter()
      await act(async () =>
        controller.migrateOutput({ targetAdapter: nextBrowser1 }),
      )
      rerenderPlayer({ engineMode: 'local', browserAdapter: nextBrowser1 })
      expect(document.querySelector('audio')).not.toBeNull()
      expect(ref.current).toBe(player)
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)

      // Round trip 2: local -> remote
      const remoteAdapter2 = createMockRemoteAudioAdapter()
      await act(async () =>
        controller.migrateOutput({ targetAdapter: remoteAdapter2 }),
      )
      rerenderPlayer({ engineMode: 'remote', browserAdapter: undefined })
      expect(document.querySelector('audio')).toBeNull()

      // Round trip 2: remote -> local with play rejection (e.g. autoplay gesture blocked)
      const nextBrowser2 = createBrowserAudioAdapter()
      await act(async () =>
        controller.migrateOutput({ targetAdapter: nextBrowser2 }),
      )
      rerenderPlayer({ engineMode: 'local', browserAdapter: nextBrowser2 })
      expect(document.querySelector('audio')).not.toBeNull()

      playSpy.mockRejectedValueOnce(
        new DOMException('User gesture required', 'NotAllowedError'),
      )
      await act(async () => document.querySelector('.play-btn').click())

      // Visible rejection: state is ERROR, alert displayed, no auto-fallback
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(getByRole('alert').textContent).toContain('User gesture required')

      unmount()
    })

    it('TC-MIG-13: PREPARING play intent survives across output migration, starting target automatically', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      await controller.addQueueItems([
        {
          queueItemId: 'prep-1',
          trackId: 't1',
          name: 'Track 1',
          musicSrc: 'http://example.com/1.mp3',
        },
      ])

      // Emulate starting playback that is currently in PREPARING
      // (play intent accepted and sequence incremented, but media is still loading)
      const playPromise = controller.play()
      expect(controller.getState().status).toBe(PlaybackStatus.PREPARING)
      expect(controller.getState().acceptedIntent).toBe('play')
      expect(controller.getState().intentSequence).toBeGreaterThan(0)

      const remoteAdapter = createMockRemoteAudioAdapter()
      await controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Remote Speaker' },
      })

      // Invariants:
      // 1. Accepted play intent survived PREPARING across output migration
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controller.getState().acceptedIntent).toBe('play')
      // 2. Target adapter started automatically
      expect(remoteAdapter.getPlayCount()).toBe(1)
      await playPromise.catch(() => {})
    })

    it('TC-MIG-14: explicit pause during migration neutralizes automatic startup (zero target play calls); later play intent takes effect through newer intent sequence', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      await controller.addQueueItems([
        {
          queueItemId: 'mig-14',
          trackId: 't14',
          name: 'Track 14',
          musicSrc: 'http://example.com/14.mp3',
        },
      ])
      await controller.play()
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controller.getState().acceptedIntent).toBe('play')

      // Subcase A: user pauses during migration
      let finishOldStopA
      const stopPromiseA = new Promise((resolve) => {
        finishOldStopA = resolve
      })
      localAdapter.stop = vi.fn(() => stopPromiseA)

      const remoteAdapterA = createMockRemoteAudioAdapter()
      const migrationPromiseA = controller.migrateOutput({
        targetAdapter: remoteAdapterA,
        targetDevice: { name: 'Remote Speaker' },
      })

      // User pauses while migration is in progress
      await controller.pause()
      expect(controller.getState().acceptedIntent).toBe('pause')

      finishOldStopA()
      await migrationPromiseA

      // Invariants:
      // 1. Target output stays PAUSED
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(controller.getState().acceptedIntent).toBe('pause')
      // 2. Target play() was called 0 times
      expect(remoteAdapterA.getPlayCount()).toBe(0)

      // Subcase B: user pauses, then plays again with newer sequence during migration
      const localAdapterB = createBrowserAudioAdapter()
      const controllerB = createPlaybackControlModule()
      controllerB.setAdapter(localAdapterB)

      const audioB = document.createElement('audio')
      localAdapterB.attachElement(audioB)
      setMediaFlags(audioB, { paused: false, ended: false })

      await controllerB.addQueueItems([
        {
          queueItemId: 'mig-14b',
          trackId: 't14b',
          name: 'Track 14B',
          musicSrc: 'http://example.com/14b.mp3',
        },
      ])
      await controllerB.play()
      expect(controllerB.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controllerB.getState().acceptedIntent).toBe('play')

      let finishOldStopB
      const stopPromiseB = new Promise((resolve) => {
        finishOldStopB = resolve
      })
      localAdapterB.stop = vi.fn(() => stopPromiseB)

      const remoteAdapterB = createMockRemoteAudioAdapter()
      const migrationPromiseB = controllerB.migrateOutput({
        targetAdapter: remoteAdapterB,
        targetDevice: { name: 'Remote Speaker' },
      })

      await controllerB.pause()
      const seqPause = controllerB.getState().intentSequence
      expect(controllerB.getState().acceptedIntent).toBe('pause')

      await controllerB.play()
      const seqPlay = controllerB.getState().intentSequence
      expect(seqPlay).toBeGreaterThan(seqPause)
      expect(controllerB.getState().acceptedIntent).toBe('play')

      finishOldStopB()
      await migrationPromiseB

      // Invariants:
      // 1. Newer play intent won over earlier pause
      expect(controllerB.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controllerB.getState().acceptedIntent).toBe('play')
      expect(remoteAdapterB.getPlayCount()).toBe(1)
    })

    it('TC-MIG-15: concurrent migration attempts are rejected by explicit migration lock; second migration cannot implicitly supersede running migration', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      let finishOldStop
      const stopPromise = new Promise((resolve) => {
        finishOldStop = resolve
      })
      localAdapter.stop = vi.fn(() => stopPromise)

      const remoteAdapter1 = createMockRemoteAudioAdapter()
      const remoteAdapter2 = createMockRemoteAudioAdapter()

      const migration1 = controller.migrateOutput({
        targetAdapter: remoteAdapter1,
        targetDevice: { name: 'Speaker 1' },
      })

      expect(controller.isMigrating()).toBe(true)

      // Second migration attempted concurrently must be rejected immediately
      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter2,
          targetDevice: { name: 'Speaker 2' },
        }),
      ).rejects.toMatchObject({
        code: 'MIGRATION_IN_PROGRESS',
      })

      // Finish first migration
      finishOldStop()
      await migration1

      expect(controller.isMigrating()).toBe(false)
      // Second adapter was untouched
      expect(remoteAdapter2.getPlayCount()).toBe(0)
    })

    it('TC-MIG-16: commands submitted during migration are isolated; stale seek/queue commands are discarded with COMMAND_SUPERSEDED', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      await controller.addQueueItems([
        {
          queueItemId: 'item-1',
          trackId: 't1',
          name: 'Track 1',
          musicSrc: 'http://example.com/1.mp3',
        },
      ])
      await controller.play()

      let finishStop
      const stopPromise = new Promise((resolve) => {
        finishStop = resolve
      })
      localAdapter.stop = vi.fn(() => stopPromise)

      const remoteAdapter = createMockRemoteAudioAdapter()
      const migrationPromise = controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Remote Speaker' },
      })

      // Issue seek while migration is in progress
      const isolatedSeek = controller.seek(42)

      // Target adapter migrates with fresh queue/version
      finishStop()
      await migrationPromise

      // Target authoritative snapshot was established with version updated
      remoteAdapter.pushServerSnapshot({
        queue: [
          { queueItemId: 'new-item-1', trackId: 't-new', name: 'New Track' },
        ],
        currentQueueItemId: 'new-item-1',
        queueVersion: 99,
      })

      // Stale seek command submitted against old context rejects with COMMAND_SUPERSEDED
      await expect(isolatedSeek).rejects.toMatchObject({
        code: 'COMMAND_SUPERSEDED',
      })
    })

    it('TC-MIG-17: UNKNOWN reconciliation via queryOperation: timeout/lost response queries original operation identity rather than blind replay', async () => {
      const controller = createPlaybackControlModule()
      const localAdapter = createBrowserAudioAdapter()
      controller.setAdapter(localAdapter)

      // Subcase A: Response lost on remote volume, but queryOperation confirms APPLIED -> succeeds without retry
      const remoteAdapterA = createMockRemoteAudioAdapter()
      remoteAdapterA.setFailure({
        action: 'volume',
        responseLost: true,
      })

      await controller.migrateOutput({
        targetAdapter: remoteAdapterA,
        targetDevice: { name: 'Speaker A' },
      })

      // Migration succeeded because queryOperation reconciled APPLIED
      expect(controller.getState().status).not.toBe(PlaybackStatus.ERROR)

      // Subcase B: queryOperation returns NOT_EXECUTED -> fails closed with zero play calls
      const remoteAdapterB = createMockRemoteAudioAdapter()
      remoteAdapterB.setFailure({
        action: 'volume',
        timeout: true,
        timeoutMs: 5000,
      })
      remoteAdapterB.queryOperation = vi.fn(async (opId) => ({
        status: CommandResultStatus.NOT_EXECUTED,
        operationId: opId,
      }))

      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapterB,
          targetDevice: { name: 'Speaker B' },
          phaseTimeoutMs: 10,
        }),
      ).rejects.toMatchObject({
        code: 'COMMAND_NOT_EXECUTED',
        status: CommandResultStatus.NOT_EXECUTED,
      })
      expect(remoteAdapterB.getPlayCount()).toBe(0)

      // Subcase C: queryOperation returns FAILED -> fails closed
      const remoteAdapterC = createMockRemoteAudioAdapter()
      remoteAdapterC.setFailure({
        action: 'volume',
        timeout: true,
        timeoutMs: 5000,
      })
      remoteAdapterC.queryOperation = vi.fn(async (opId) => ({
        status: CommandResultStatus.FAILED,
        error: { code: 'HARDWARE_SINK_DEAD' },
        operationId: opId,
      }))

      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapterC,
          targetDevice: { name: 'Speaker C' },
          phaseTimeoutMs: 10,
        }),
      ).rejects.toMatchObject({
        code: 'HARDWARE_SINK_DEAD',
        status: CommandResultStatus.FAILED,
      })
      expect(remoteAdapterC.getPlayCount()).toBe(0)

      // Subcase D: queryOperation returns UNKNOWN -> fails closed
      const remoteAdapterD = createMockRemoteAudioAdapter()
      remoteAdapterD.setFailure({
        action: 'volume',
        timeout: true,
        timeoutMs: 5000,
      })
      remoteAdapterD.queryOperation = vi.fn(async (opId) => ({
        status: CommandResultStatus.UNKNOWN,
        operationId: opId,
      }))

      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapterD,
          targetDevice: { name: 'Speaker D' },
          phaseTimeoutMs: 10,
        }),
      ).rejects.toMatchObject({
        status: CommandResultStatus.UNKNOWN,
      })
      expect(remoteAdapterD.getPlayCount()).toBe(0)
    })

    it('TC-MIG-18: reservation cleanup only releases reservation owned by current migration; source stop failure leaves target owner & context unchanged and does not resume source output', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      const audio = document.createElement('audio')
      localAdapter.attachElement(audio)
      setMediaFlags(audio, { paused: false, ended: false })

      // Target remote device owned by 'initial-owner'
      const remoteAdapter = createMockRemoteAudioAdapter({
        hasActiveSession: true,
        activeSessionOwner: 'initial-owner',
        initialPosition: 50,
      })
      remoteAdapter.pushServerSnapshot({
        queue: [{ queueItemId: 'rem-orig', trackId: 't-orig' }],
        currentQueueItemId: 'rem-orig',
        position: 50,
      })

      // Inject stop failure on source adapter
      localAdapter.stop = vi.fn(async () => {
        throw {
          code: 'SOURCE_STOP_REJECTED',
          message: 'Failed to stop local output',
        }
      })

      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: {
            name: 'Living Room TV',
            hasActiveSession: true,
            activeController: 'initial-owner',
            clientId: 'migrating-client',
          },
          forceTakeover: true,
        }),
      ).rejects.toMatchObject({
        code: 'SOURCE_STOP_REJECTED',
      })

      // Invariants:
      // 1. Target reservation cleanup was executed
      const reservations = remoteAdapter.getReservations()
      for (const res of reservations.values()) {
        expect(res.status).toBe('RELEASED')
      }
      expect(remoteAdapter.getActiveSessionOwner()).toBe('initial-owner')
      // 2. Target playback context remains intact
      expect(remoteAdapter.getCurrentQueueItemId()).toBe('rem-orig')
      expect(remoteAdapter.getRemoteState().position).toBe(50)
      // 3. Target play was never called
      expect(remoteAdapter.getPlayCount()).toBe(0)
      // 4. Source audio is not resumed
      expect(audio.paused).toBe(false)
    })

    it('TC-MIG-19: rejected/timed-out final pause during paused migration produces bounded visible error instead of false success', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      await controller.pause()
      expect(controller.getState().status).toBe(PlaybackStatus.PAUSED)
      expect(controller.getState().acceptedIntent).toBe('pause')

      // Target adapter where pause fails
      const remoteAdapter = createMockRemoteAudioAdapter()
      remoteAdapter.setFailure({
        action: 'pause',
        code: 'TARGET_PAUSE_HARDWARE_ERROR',
        message: 'Could not pause target hardware sink',
      })

      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: { name: 'Remote Speaker' },
        }),
      ).rejects.toMatchObject({
        code: 'TARGET_PAUSE_HARDWARE_ERROR',
      })

      // Visible terminal error: NOT false success
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(controller.getState().error.code).toBe(
        'TARGET_PAUSE_HARDWARE_ERROR',
      )
    })

    it('TC-MIG-20: migration cancellation via AbortSignal propagates cancellation and boundedly releases resources', async () => {
      const localAdapter = createBrowserAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      let finishOldStop
      const stopPromise = new Promise((resolve) => {
        finishOldStop = resolve
      })
      localAdapter.stop = vi.fn(() => stopPromise)

      const abortController = new AbortController()
      const remoteAdapter = createMockRemoteAudioAdapter()

      const migrationPromise = controller.migrateOutput({
        targetAdapter: remoteAdapter,
        targetDevice: { name: 'Remote Speaker' },
        signal: abortController.signal,
      })

      expect(controller.isMigrating()).toBe(true)

      // Cancel migration while in-flight
      abortController.abort()
      finishOldStop()

      await expect(migrationPromise).rejects.toMatchObject({
        code: 'MIGRATION_CANCELLED',
      })

      // Lock is released
      expect(controller.isMigrating()).toBe(false)
      // Target play was never called
      expect(remoteAdapter.getPlayCount()).toBe(0)
    })

    it('TC-MIG-21: pendingOutputTarget is published before waiting for browser readiness, enabling host/UnifiedNativePlayer to mount <audio> element and satisfy readiness immediately', async () => {
      const ref = React.createRef()
      const { controller, rerenderPlayer, unmount } =
        await renderControlledPlayer('remote', { ref })

      expect(document.querySelector('audio')).toBeNull()

      const unattachedBrowser = createBrowserAudioAdapter({ unattached: true })
      expect(unattachedBrowser.getAudioElement()).toBeNull()

      let observedPending = null
      const unsubscribe = controller.subscribe((state) => {
        if (state.pendingOutputTarget) {
          observedPending = state.pendingOutputTarget
        }
      })

      const migrationPromise = controller.migrateOutput({
        targetAdapter: unattachedBrowser,
        targetDevice: { name: 'Browser Local', type: 'browser' },
        readinessTimeoutMs: 1000,
      })

      // Invariant: pendingOutputTarget published before readiness wait completes
      await waitFor(() => {
        expect(observedPending).toMatchObject({
          name: 'Browser Local',
          type: 'browser',
        })
      })

      // UnifiedNativePlayer mounts <audio> branch when target is browser
      rerenderPlayer({
        engineMode: 'local',
        browserAdapter: unattachedBrowser,
      })
      await waitFor(() => {
        expect(document.querySelector('audio')).not.toBeNull()
      })

      await migrationPromise
      unsubscribe()

      // Post-migration: pendingOutputTarget is cleared, real audio element remains
      expect(controller.getState().pendingOutputTarget).toBeNull()
      expect(unattachedBrowser.getAudioElement()).not.toBeNull()
      expect(document.querySelector('audio')).not.toBeNull()

      unmount()
    })

    it('TC-MIG-22: valid attached element satisfies migration preparation immediately, while unattached target waits and produces TARGET_NOT_READY on readiness watchdog timeout', async () => {
      // Subcase A: Valid attached element satisfies immediately
      const attachedAudio = document.createElement('audio')
      const attachedAdapter = createBrowserAudioAdapter({
        audioElement: attachedAudio,
      })
      const opIdA = 'mig_imm:phase1:admission'
      const resA = await attachedAdapter.reserveMigration(opIdA, {
        migrationId: 'mig_imm',
        readinessTimeoutMs: 50,
      })
      expect(resA.status).toBe(CommandResultStatus.APPLIED)

      // Subcase B: Unattached target waits; times out with TARGET_NOT_READY
      const unattachedAdapter = createBrowserAudioAdapter({ unattached: true })
      const opIdB = 'mig_timeout:phase1:admission'

      await expect(
        unattachedAdapter.reserveMigration(opIdB, {
          migrationId: 'mig_timeout',
          readinessTimeoutMs: 30,
        }),
      ).rejects.toMatchObject({
        code: 'TARGET_NOT_READY',
        status: CommandResultStatus.FAILED,
      })

      // Target was never played
      expect(unattachedAdapter.getAudioElement()).toBeNull()
    })

    it('TC-MIG-23: unattached readiness wait honors AbortSignal cancellation and cleans up timers', async () => {
      const unattachedAdapter = createBrowserAudioAdapter({ unattached: true })
      const abortController = new AbortController()

      const waitPromise = unattachedAdapter.reserveMigration(
        'mig_cancel:phase1:admission',
        {
          migrationId: 'mig_cancel',
          signal: abortController.signal,
          readinessTimeoutMs: 5000,
        },
      )

      // Cancel while waiting for attachment
      abortController.abort()

      await expect(waitPromise).rejects.toMatchObject({
        code: 'MIGRATION_CANCELLED',
      })
      expect(
        unattachedAdapter.getReservations().get('mig_cancel')?.status,
      ).toBe('CANCELLED')
    })

    it('TC-MIG-24: no detached Audio object is created to start playback early; detachment invalidates local reservation', async () => {
      const initialCreated = createElementSpy.mock.calls.filter(
        ([tag]) => tag === 'audio',
      ).length

      const unattachedAdapter = createBrowserAudioAdapter({ unattached: true })
      expect(unattachedAdapter.getAudioElement()).toBeNull()

      // 1. Calling reserveMigration on unattached target does NOT create an Audio element
      const waitPromise = unattachedAdapter
        .reserveMigration('mig_early:phase1:admission', {
          migrationId: 'mig_early',
          readinessTimeoutMs: 50,
        })
        .catch((e) => e)

      expect(
        createElementSpy.mock.calls.filter(([tag]) => tag === 'audio'),
      ).toHaveLength(initialCreated)

      // 2. Detach invalidates local reservation
      unattachedAdapter.attachElement(null)
      const resAfterDetach = unattachedAdapter
        .getReservations()
        .get('mig_early')
      expect(resAfterDetach?.status === 'INVALIDATED' || !resAfterDetach).toBe(
        true,
      )

      await waitPromise

      // 3. Attempting play on unattached adapter throws NO_AUDIO_ELEMENT without creating audio
      unattachedAdapter.activate(1, () => {})
      await expect(unattachedAdapter.play(1)).rejects.toMatchObject({
        code: 'NO_AUDIO_ELEMENT',
      })
      expect(
        createElementSpy.mock.calls.filter(([tag]) => tag === 'audio'),
      ).toHaveLength(initialCreated)
    })

    it('TC-MIG-25: late attachment after migration cannot clear, rewind, or replace already migrated Playback Context', async () => {
      const localAdapter = createBrowserAudioAdapter({ unattached: true })
      const controller = createPlaybackControlModule()
      controller.setAdapter(localAdapter)

      await controller.addQueueItems([
        { queueItemId: 'mig-1', musicSrc: '/1.mp3' },
        { queueItemId: 'mig-2', musicSrc: '/2.mp3' },
        { queueItemId: 'mig-3', musicSrc: '/3.mp3' },
      ])
      await controller.skipToQueueItem('mig-2')
      await controller.seek(42)

      const migratedContext = controller.getState()
      expect(migratedContext.currentQueueItemId).toBe('mig-2')
      expect(migratedContext.position).toBe(42)
      expect(migratedContext.queue).toHaveLength(3)

      // Mount element late
      const lateAudio = document.createElement('audio')
      localAdapter.attachElement(lateAudio)

      // Invariants: late attachment must NOT clear, rewind, or replace the migrated scene
      const currentContext = controller.getState()
      expect(currentContext.currentQueueItemId).toBe('mig-2')
      expect(currentContext.position).toBe(42)
      expect(currentContext.queue).toHaveLength(3)
      expect(lateAudio.currentTime).toBe(42)
      expect(localAdapter.getAudioElement()).toBe(lateAudio)
    })

    it('TC-MIG-26: media listener binding is reused: same-element/same-epoch is idempotent, cross-epoch removes duplicates, and deactivation unloads media', () => {
      const audio = document.createElement('audio')
      const addSpy = vi.spyOn(audio, 'addEventListener')
      const removeSpy = vi.spyOn(audio, 'removeEventListener')

      const adapter = createBrowserAudioAdapter({ unattached: true })
      adapter.activate(1, () => {})

      // 1. Initial attachment binds listeners
      adapter.attachElement(audio)
      const initialAddCalls = addSpy.mock.calls.length
      expect(initialAddCalls).toBeGreaterThan(0)

      // 2. Same element, same epoch attachment is idempotent (0 additional addEventListener calls)
      adapter.attachElement(audio)
      expect(addSpy.mock.calls.length).toBe(initialAddCalls)

      // 3. Cross-epoch attachment removes previous listeners and re-binds for new epoch
      adapter.activate(2, () => {})
      expect(removeSpy.mock.calls.length).toBe(initialAddCalls)
      expect(addSpy.mock.calls.length).toBe(initialAddCalls * 2)

      // 4. Deactivation unbinds listeners and unloads media resources
      const testLoadSpy = vi.spyOn(audio, 'load')
      audio.setAttribute('src', '/test.mp3')
      adapter.deactivate()

      expect(removeSpy.mock.calls.length).toBe(initialAddCalls * 2)
      expect(audio.getAttribute('src')).toBeNull()
      expect(testLoadSpy).toHaveBeenCalled()
    })

    it('TC-MIG-27: tail error, pause, ended, and timeupdate events emitted during cleanup are rejected by epoch and resource-generation guards', async () => {
      const audio = document.createElement('audio')
      const adapter = createBrowserAudioAdapter({ audioElement: audio })
      const controller = createPlaybackControlModule()
      controller.setAdapter(adapter)

      await controller.addQueueItems([
        { queueItemId: 'track-1', musicSrc: '/1.mp3' },
        { queueItemId: 'track-2', musicSrc: '/2.mp3' },
      ])
      await controller.play()

      // Deactivate / cleanup output via controller
      controller.setAdapter(null)

      // Emit tail events that occur as browser unloads media
      await act(async () => {
        audio.dispatchEvent(new Event('timeupdate'))
        audio.dispatchEvent(new Event('pause'))
        audio.dispatchEvent(new Event('error'))
        audio.dispatchEvent(new Event('ended'))
      })

      // Invariants: tail events do NOT advance track, do NOT corrupt state with error, do NOT update progress
      expect(controller.getState().status).toBe(PlaybackStatus.IDLE)
      expect(controller.getState().error).toBeNull()
      expect(controller.getState().currentQueueItemId).toBeNull()
    })

    it('TC-MIG-28: real media promise rejections (NotAllowedError) propagate through shared failure envelope without fabricating PLAYING; subsequent user play recovers', async () => {
      const ref = React.createRef()
      const { controller, rerenderPlayer, getByRole, unmount } =
        await renderControlledPlayer('remote', { ref })

      // Initial remote playback
      await act(async () => {
        await controller.play()
      })
      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)

      const nextBrowser = createBrowserAudioAdapter({ unattached: true })

      // Start migration from remote to browser with playing intent
      const migrationPromise = controller.migrateOutput({
        targetAdapter: nextBrowser,
        targetDevice: { name: 'Browser Local', type: 'browser' },
      })

      // Host switches to local branch
      rerenderPlayer({
        engineMode: 'local',
        browserAdapter: nextBrowser,
      })

      // Simulate browser autoplay rejection (NotAllowedError)
      playSpy.mockRejectedValueOnce(
        new DOMException(
          'Autoplay blocked: gesture required',
          'NotAllowedError',
        ),
      )

      await expect(migrationPromise).rejects.toMatchObject({
        message: 'Autoplay blocked: gesture required',
      })

      // Invariants:
      // 1. Status is ERROR, never fabricates PLAYING
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)
      expect(controller.getState().error.message).toContain('Autoplay blocked')
      // 2. Alert visible in UI
      expect(getByRole('alert').textContent).toContain('Autoplay blocked')
      // 3. No audible sound restored automatically
      expect(nextBrowser.getAudioElement().paused).toBe(true)

      // Recovery: Subsequent explicit user play action succeeds with user gesture
      playSpy.mockResolvedValueOnce(undefined)
      await act(async () => {
        await controller.play()
      })

      expect(controller.getState().status).toBe(PlaybackStatus.PLAYING)
      expect(controller.getState().error).toBeNull()

      unmount()
    })

    it('TC-MIG-29: canonical volume is the only module volume; invalid inputs rejected at module boundary; V=0 does not overwrite lastNonZeroVolume', async () => {
      const controller = createPlaybackControlModule()
      const adapter = createBrowserAudioAdapter()
      controller.setAdapter(adapter)

      // Initial canonical volume
      expect(controller.getState().volume).toBe(1)
      expect(controller.getState().lastNonZeroVolume).toBe(1)

      // Invalid numeric inputs rejected at Module boundary
      await expect(controller.setVolume(NaN)).rejects.toMatchObject({
        code: 'INVALID_VOLUME',
      })
      await expect(controller.setVolume(Infinity)).rejects.toMatchObject({
        code: 'INVALID_VOLUME',
      })
      await expect(controller.setVolume(-0.01)).rejects.toMatchObject({
        code: 'INVALID_VOLUME',
      })
      await expect(controller.setVolume(1.01)).rejects.toMatchObject({
        code: 'INVALID_VOLUME',
      })
      await expect(controller.setVolume('0.5')).rejects.toMatchObject({
        code: 'INVALID_VOLUME',
      })

      // Valid volume updates volume and lastNonZeroVolume
      await controller.setVolume(0.64)
      expect(controller.getState().volume).toBe(0.64)
      expect(controller.getState().lastNonZeroVolume).toBe(0.64)

      // V = 0 is valid, but does NOT overwrite lastNonZeroVolume
      await controller.setVolume(0)
      expect(controller.getState().volume).toBe(0)
      expect(controller.getState().lastNonZeroVolume).toBe(0.64)

      // V = 1 is valid
      await controller.setVolume(1)
      expect(controller.getState().volume).toBe(1)
      expect(controller.getState().lastNonZeroVolume).toBe(1)
    })

    it('TC-MIG-30: mute is an independent authoritative boolean; adjusting volume while muted updates desired value without unmuting; unmute restores output with current volume', async () => {
      const audio = document.createElement('audio')
      const adapter = createBrowserAudioAdapter({ audioElement: audio })
      const controller = createPlaybackControlModule()
      controller.setAdapter(adapter)

      await controller.setVolume(0.8)
      expect(controller.getState().volume).toBe(0.8)
      expect(controller.getState().isMuted).toBe(false)
      expect(audio.muted).toBe(false)

      // Mute independently
      await controller.setMute(true)
      expect(controller.getState().isMuted).toBe(true)
      expect(controller.getState().volume).toBe(0.8)
      expect(controller.getState().lastNonZeroVolume).toBe(0.8)
      expect(audio.muted).toBe(true)

      // Adjusting volume while muted updates V and lastNonZeroVolume without unmuting
      await controller.setVolume(0.4)
      expect(controller.getState().volume).toBe(0.4)
      expect(controller.getState().lastNonZeroVolume).toBe(0.4)
      expect(controller.getState().isMuted).toBe(true)
      expect(audio.muted).toBe(true)
      expect(audio.volume).toBeCloseTo(0.4)

      // Unmute restores sound with current V
      await controller.setMute(false)
      expect(controller.getState().isMuted).toBe(false)
      expect(controller.getState().volume).toBe(0.4)
      expect(audio.muted).toBe(false)
      expect(audio.volume).toBeCloseTo(0.4)

      // toggleMute convenience method
      await controller.toggleMute()
      expect(controller.getState().isMuted).toBe(true)
      await controller.toggleMute()
      expect(controller.getState().isMuted).toBe(false)
    })

    it('TC-MIG-31: explicit volume profile takes precedence over device hints; unknown profile fails before target startup with VOLUME_PROFILE_UNKNOWN and zero play calls', async () => {
      const controller = createPlaybackControlModule()
      const localAdapter = createBrowserAudioAdapter()
      controller.setAdapter(localAdapter)

      await controller.addQueueItems([
        { queueItemId: 'item-1', musicSrc: '/test.mp3' },
      ])
      await controller.setVolume(0.64)
      await controller.play()

      const remoteAdapter = createMockRemoteAudioAdapter()

      // Subcase A: Unknown profile fails before target startup with VOLUME_PROFILE_UNKNOWN
      await expect(
        controller.migrateOutput({
          targetAdapter: remoteAdapter,
          targetDevice: {
            name: 'Device with bad profile',
            volumeProfile: 'unsupported-mystery-curve',
          },
        }),
      ).rejects.toMatchObject({
        code: 'VOLUME_PROFILE_UNKNOWN',
      })

      expect(remoteAdapter.getPlayCount()).toBe(0)
      expect(controller.getState().status).toBe(PlaybackStatus.ERROR)

      // Subcase B: Explicit 'default' overrides Bluetooth device hint
      await controller.play()
      const remoteAdapterB = createMockRemoteAudioAdapter()
      const resB = await controller.migrateOutput({
        targetAdapter: remoteAdapterB,
        targetDevice: {
          name: 'BT speaker forced to default',
          deviceName: 'pulse/bluez_output.sink',
          isBluetooth: true,
          volumeProfile: 'default',
        },
      })
      expect(resB.status).toBe(PlaybackStatus.PLAYING)
      expect(remoteAdapterB.getAppliedVolume()).toBeCloseTo(0.64) // Linear, not 0.9352

      // Subcase C: Explicit 'bluetooth-jukebox' overrides non-Bluetooth device hint
      const remoteAdapterC = createMockRemoteAudioAdapter()
      const resC = await controller.migrateOutput({
        targetAdapter: remoteAdapterC,
        targetDevice: {
          name: 'ALSA card forced to bluetooth curve',
          deviceName: 'pulse/alsa_output.pci',
          isBluetooth: false,
          volumeProfile: 'bluetooth-jukebox',
        },
      })
      expect(resC.status).toBe(PlaybackStatus.PLAYING)
      expect(remoteAdapterC.getAppliedVolume()).toBeCloseTo(
        Math.pow(0.64, 0.15),
      )
    })

    it('TC-MIG-32: repeated equivalent authoritative gain snapshots do not cumulatively remap V; external change outside quantization tolerance inverts once', async () => {
      const controller = createPlaybackControlModule()
      const bluetoothAdapter = createMockRemoteAudioAdapter({
        device: {
          deviceName: 'pulse/bluez_output.sink',
          isBluetooth: true,
        },
      })
      controller.setAdapter(bluetoothAdapter)

      await controller.setVolume(0.64)
      expect(controller.getState().volume).toBe(0.64)
      const expectedBtGain = Math.pow(0.64, 0.15) // ~0.935235
      expect(bluetoothAdapter.getAppliedVolume()).toBeCloseTo(expectedBtGain, 5)

      // Repeated snapshots within 0.01 tolerance (e.g. 0.94 due to rounding) reuse confirmed V
      bluetoothAdapter.pushServerSnapshot({ gain: 0.94 })
      expect(controller.getState().volume).toBe(0.64)

      bluetoothAdapter.pushServerSnapshot({ gain: expectedBtGain })
      expect(controller.getState().volume).toBe(0.64)

      bluetoothAdapter.pushServerSnapshot({ gain: 0.93 })
      expect(controller.getState().volume).toBe(0.64)

      // External change outside tolerance (e.g. 0.5) inverts once
      bluetoothAdapter.pushServerSnapshot({ gain: 0.5 })
      const expectedInvertedV = Math.pow(0.5, 1 / 0.15)
      expect(controller.getState().volume).toBeCloseTo(expectedInvertedV, 6)

      // Subsequent identical feedback at 0.5 does not cumulatively remap
      bluetoothAdapter.pushServerSnapshot({ gain: 0.5 })
      expect(controller.getState().volume).toBeCloseTo(expectedInvertedV, 6)
    })

    it('TC-MIG-33: ordinary -> Bluetooth -> ordinary preserves canonical V = 0.64 and U = 0.8 across runtime adjustments, repeated snapshots, and five reconnects', async () => {
      const controller = createPlaybackControlModule()
      const ordinaryAdapter = createMockRemoteAudioAdapter({
        device: { deviceName: 'pulse/alsa_output.pci' },
      })
      controller.setAdapter(ordinaryAdapter)

      await controller.setVolume(0.64)
      expect(controller.getState().volume).toBe(0.64)
      expect(audioVolumeToUiVolume(controller.getState().volume)).toBeCloseTo(
        0.8,
      )

      const bluetoothAdapter = createMockRemoteAudioAdapter({
        device: {
          deviceName: 'pulse/bluez_output.sink',
          isBluetooth: true,
        },
      })

      // Migrate to Bluetooth
      await controller.migrateOutput({
        targetAdapter: bluetoothAdapter,
        targetDevice: {
          clientId: 'test-client',
          deviceName: 'pulse/bluez_output.sink',
          isBluetooth: true,
        },
      })

      expect(controller.getState().volume).toBe(0.64)
      expect(audioVolumeToUiVolume(controller.getState().volume)).toBeCloseTo(
        0.8,
      )
      expect(bluetoothAdapter.getAppliedVolume()).toBeCloseTo(
        Math.pow(0.64, 0.15),
        5,
      )

      // 5 reconnects/re-migrations to same Bluetooth target
      for (let i = 1; i <= 5; i++) {
        await controller.migrateOutput({
          targetAdapter: bluetoothAdapter,
          targetDevice: {
            clientId: 'test-client',
            deviceName: 'pulse/bluez_output.sink',
            isBluetooth: true,
          },
        })
        expect(controller.getState().volume).toBe(0.64)
        expect(audioVolumeToUiVolume(controller.getState().volume)).toBeCloseTo(
          0.8,
        )
        expect(bluetoothAdapter.getAppliedVolume()).toBeCloseTo(
          Math.pow(0.64, 0.15),
          5,
        )
      }

      // Migrate back to Ordinary output
      const returnOrdinary = createMockRemoteAudioAdapter({
        device: { deviceName: 'pulse/alsa_output.pci' },
      })
      await controller.migrateOutput({
        targetAdapter: returnOrdinary,
        targetDevice: {
          clientId: 'test-client',
          deviceName: 'pulse/alsa_output.pci',
        },
      })

      expect(controller.getState().volume).toBe(0.64)
      expect(audioVolumeToUiVolume(controller.getState().volume)).toBeCloseTo(
        0.8,
      )
      expect(returnOrdinary.getAppliedVolume()).toBeCloseTo(0.64)
    })

    it('TC-MIG-34: desired gain and confirmed gain remain distinct on remote adapter; apply failure retains desired value without presenting it as applied', async () => {
      const remoteAdapter = createMockRemoteAudioAdapter()
      const controller = createPlaybackControlModule()
      controller.setAdapter(remoteAdapter)

      // With no active track, setting volume updates desired gain
      await controller.setVolume(0.7)
      expect(remoteAdapter.getDesiredGain()).toBeCloseTo(0.7)
      expect(remoteAdapter.getAppliedVolume()).toBeCloseTo(0.7)

      // Inject failure on volume
      remoteAdapter.setFailure({
        action: 'volume',
        code: 'MPV_SET_VOLUME_FAILED',
        message: 'Volume IPC failed',
      })

      await expect(controller.setVolume(0.3)).rejects.toMatchObject({
        code: 'MPV_SET_VOLUME_FAILED',
      })

      // Desired gain recorded the requested value, but applied gain retains previous confirmed value
      expect(remoteAdapter.getDesiredGain()).toBeCloseTo(0.3)
      expect(remoteAdapter.getAppliedVolume()).toBeCloseTo(0.7)
    })
  })
})
