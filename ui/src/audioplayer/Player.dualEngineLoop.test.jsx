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
  PlaybackMode,
  normalizePlaybackMode,
  getNextTrack,
  getPrevTrack,
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

    afterEach(() => {
      vi.restoreAllMocks()
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

      let playerInstance = null
      render(
        <UnifiedNativePlayer
          ref={(ref) => {
            playerInstance = ref
          }}
          controller={controller}
          engineMode="remote"
          mode="full"
          autoPlay={false}
        />,
      )

      await controller.addQueueItems(duplicateQueueTracks)
      remoteAdapter.clearWireCommands()

      // 1. Delete item via onDeleteAudioLists
      await act(async () => {
        playerInstance.onDeleteAudioLists('q-dup-3')({
          stopPropagation: vi.fn(),
        })
      })
      await waitFor(() => {
        const cmd = remoteAdapter
          .getWireCommands()
          .find((c) => c.action === 'remove')
        expect(cmd).toBeDefined()
        expect(cmd.queueItemId).toBe('q-dup-3')
      })

      // 2. Reorder item via onAudioListsSortEnd
      await act(async () => {
        playerInstance.onAudioListsSortEnd(0, 1)
      })
      await waitFor(() => {
        const cmd = remoteAdapter
          .getWireCommands()
          .find((c) => c.action === 'move')
        expect(cmd).toBeDefined()
        expect(cmd.queueItemId).toBe('q-dup-1')
        expect(cmd.toIndex).toBe(1)
      })

      // 3. Skip item via audioListsPlay
      await act(async () => {
        playerInstance.audioListsPlay('q-mid-2')
      })
      await waitFor(() => {
        const cmd = remoteAdapter
          .getWireCommands()
          .find((c) => c.action === 'skip')
        expect(cmd).toBeDefined()
        expect(cmd.queueItemId).toBe('q-mid-2')
      })

      // 4. Clear queue via onDeleteAudioLists(null / undefined)
      await act(async () => {
        playerInstance.onDeleteAudioLists()({
          stopPropagation: vi.fn(),
        })
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
    it('TC-QC-10: host list actions dispatch unified queue intents with single-intent guarantee', async () => {
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

      await testManualNext(createBrowserAudioAdapter(), false)
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
      const localAdapter = createBrowserAudioAdapter()
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.setPlayMode(PlaybackMode.ORDER)

      const audioEl = localAdapter.getAudioElement()

      // Natural end on mode-1 -> advances to mode-2
      act(() => {
        audioEl.dispatchEvent(new Event('ended'))
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-2')
      expect(localController.getState().status).toBe(PlaybackStatus.PLAYING)

      // Natural end on mode-2 -> advances to mode-3
      act(() => {
        audioEl.dispatchEvent(new Event('ended'))
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-3')
      expect(localController.getState().status).toBe(PlaybackStatus.PLAYING)

      // Natural end on mode-3 (last) -> stops! Does NOT wrap around
      act(() => {
        audioEl.dispatchEvent(new Event('ended'))
      })
      expect(localController.getState().currentQueueItemId).toBe('mode-3')
      expect(localController.getState().status).toBe(PlaybackStatus.PAUSED)

      // 2. Mock Remote Audio Adapter: autonomous server natural end
      const remoteAdapter = createMockRemoteAudioAdapter()
      const remoteController = createPlaybackControlModule()
      remoteController.setAdapter(remoteAdapter)
      await remoteController.addQueueItems(modeTracks)
      await remoteController.setPlayMode(PlaybackMode.ORDER)

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
      const localAdapter = createBrowserAudioAdapter()
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.setPlayMode(PlaybackMode.REPEAT_LIST)
      await localController.skipToQueueItem('mode-3')

      const audioEl = localAdapter.getAudioElement()
      act(() => {
        audioEl.dispatchEvent(new Event('ended'))
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
      const localAdapter = createBrowserAudioAdapter()
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.setPlayMode(PlaybackMode.REPEAT_TRACK)
      await localController.skipToQueueItem('mode-2')

      const audioEl = localAdapter.getAudioElement()
      act(() => {
        audioEl.dispatchEvent(new Event('ended'))
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
      const localAdapter = createBrowserAudioAdapter()
      const localController = createPlaybackControlModule()
      localController.setAdapter(localAdapter)
      await localController.addQueueItems(modeTracks)
      await localController.skipToQueueItem('mode-2')

      const audioEl = localAdapter.getAudioElement()

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

      const localRes = await runModeSequence(
        createBrowserAudioAdapter({ randomFn: deterministicRandom }),
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
})
