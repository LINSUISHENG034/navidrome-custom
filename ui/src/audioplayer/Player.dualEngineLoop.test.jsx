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
 */

import React from 'react'
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, act, waitFor } from '@testing-library/react'

vi.mock('sortablejs', async () => {
  function Swap() {}
  function Sortable() {
    this.destroy = vi.fn()
  }
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
} from './playbackControlModule'
import { createBrowserAudioAdapter } from './browserAudioAdapter'
import { createMockRemoteAudioAdapter } from './mockRemoteAudioAdapter'
import { UnifiedNativePlayer } from './UnifiedNativePlayer'

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
      .mockImplementation(() => Promise.resolve())
    pauseSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, 'pause')
      .mockImplementation(() => {})
    vi.spyOn(window.HTMLMediaElement.prototype, 'load').mockImplementation(
      () => {},
    )

    originalCreateElement = document.createElement.bind(document)
    createElementSpy = vi.spyOn(document, 'createElement')
  })

  afterEach(() => {
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
      expect(playerRef.state.soundValue).toBe(60)

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
      expect(playerRef.state.soundValue).toBe(40)

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
})
