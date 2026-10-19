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

import ReactJkMusicPlayer from 'navidrome-music-player/es/index.js'

describe('Native Player (ReactJkMusicPlayer) Baseline Behavioral Contract', () => {
  let playSpy
  let pauseSpy
  let loadSpy

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
    loadSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, 'load')
      .mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

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
    {
      name: 'Song 2',
      singer: 'Artist 2',
      musicSrc: 'http://localhost/song2.mp3',
      cover: 'http://localhost/cover2.jpg',
      id: 's-2',
    },
  ]

  it('TC-01: mounts ReactJkMusicPlayer with real audio element and provides audioInstance', async () => {
    let capturedAudio = null
    render(
      <ReactJkMusicPlayer
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
    expect(typeof capturedAudio.playNext).toBe('function')
    expect(typeof capturedAudio.playPrev).toBe('function')
  })

  it('TC-02: executes local play, pause, seek, and volume on audioInstance', async () => {
    let capturedAudio = null
    render(
      <ReactJkMusicPlayer
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

    // Play & Pause
    act(() => {
      capturedAudio.play()
    })
    expect(playSpy).toHaveBeenCalled()

    act(() => {
      capturedAudio.pause()
    })
    expect(pauseSpy).toHaveBeenCalled()

    // Seek via currentTime
    act(() => {
      capturedAudio.currentTime = 42
    })
    expect(capturedAudio.currentTime).toBe(42)

    // Volume
    act(() => {
      capturedAudio.volume = 0.75
    })
    expect(capturedAudio.volume).toBe(0.75)
  })

  it('TC-07: documents baseline anomaly - deleting currently playing track jumps to index 0 instead of successor', async () => {
    let playerRef = null
    let listChangeArgs = null

    // Track instance using ref or test props
    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={sampleTracks}
        playIndex={1}
        mode="full"
        autoPlay={false}
        onAudioListsChange={(playId, audioLists, audioInfo) => {
          listChangeArgs = { playId, audioLists, audioInfo }
        }}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    // Initial playId corresponds to index 1 (Song 1)
    const initialPlayId = playerRef.state.playId
    expect(playerRef.getCurrentPlayIndex()).toBe(1)

    // Trigger onDeleteAudioLists for the current track (Song 1)
    act(() => {
      playerRef.onDeleteAudioLists(initialPlayId)({
        stopPropagation: () => {},
      })
    })

    // onAudioListsChange should have fired
    expect(listChangeArgs).not.toBeNull()
    expect(listChangeArgs.audioLists).toHaveLength(2)

    // Advance setTimeout(..., AUDIO_LIST_REMOVE_ANIMATE_TIME)
    await waitFor(
      () => {
        // Because of the baseline defect, current index becomes 0 (Song 0), NOT index 1 (Song 2)
        expect(playerRef.getCurrentPlayIndex()).toBe(0)
      },
      { timeout: 1000 },
    )
  })

  it('TC-08: documents baseline anomaly - deleting current track while paused forcibly initiates playback', async () => {
    let playerRef = null

    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={sampleTracks}
        playIndex={1}
        mode="full"
        autoPlay={false}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    // Ensure player is paused
    playSpy.mockClear()
    loadSpy.mockClear()
    expect(playerRef.state.playing).toBe(false)

    const currentPlayId = playerRef.state.playId

    // Delete current track while paused
    act(() => {
      playerRef.onDeleteAudioLists(currentPlayId)({
        stopPropagation: () => {},
      })
    })

    // After removal timeout, handlePlay(orderLoop) calls audioListsPlay
    // which unconditionally sets isAutoPlayWhenUserClicked = true and calls audio.load()
    await waitFor(
      () => {
        expect(loadSpy).toHaveBeenCalled()
        expect(playerRef.state.isAutoPlayWhenUserClicked).toBe(true)
      },
      { timeout: 1000 },
    )

    // When media load completes (canplay), onAudioCanPlay triggers playAudio(true) which calls audio.play()
    Object.defineProperty(playerRef.audio, 'networkState', {
      value: 1, // NETWORK_IDLE
      writable: true,
      configurable: true,
    })
    Object.defineProperty(playerRef.audio, 'readyState', {
      value: 4, // HAVE_ENOUGH_DATA
      writable: true,
      configurable: true,
    })
    act(() => {
      playerRef.audio.dispatchEvent(new Event('canplay'))
    })

    await waitFor(() => {
      expect(playSpy).toHaveBeenCalled()
    })
  })

  it('TC-09: documents baseline behavior - deleting the sole track resets audio status and stops', async () => {
    let playerRef = null
    const singleTrack = [sampleTracks[0]]

    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={singleTrack}
        playIndex={0}
        mode="full"
        autoPlay={false}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    const onlyTrackId = playerRef.state.playId

    pauseSpy.mockClear()
    act(() => {
      playerRef.onDeleteAudioLists(onlyTrackId)({
        stopPropagation: () => {},
      })
    })

    await waitFor(
      () => {
        expect(playerRef.state.audioLists).toHaveLength(0)
        expect(pauseSpy).toHaveBeenCalled()
        expect(playerRef.state.playing).toBe(false)
      },
      { timeout: 1000 },
    )
  })

  it('TC-10: documents baseline behavior - deleting the last track in multi-item queue loops to index 0', async () => {
    let playerRef = null

    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={sampleTracks}
        playIndex={2}
        mode="full"
        autoPlay={false}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    const lastTrackId = playerRef.state.playId
    expect(playerRef.getCurrentPlayIndex()).toBe(2)

    act(() => {
      playerRef.onDeleteAudioLists(lastTrackId)({
        stopPropagation: () => {},
      })
    })

    await waitFor(
      () => {
        // Remaining list has length 2. The baseline evaluates -1 + 1 = 0, looping back to index 0
        expect(playerRef.state.audioLists).toHaveLength(2)
        expect(playerRef.getCurrentPlayIndex()).toBe(0)
      },
      { timeout: 1000 },
    )
  })

  it('TC-11: documents baseline behavior - shuffle mode selects random index without history cursor', async () => {
    let playerRef = null

    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={sampleTracks}
        playIndex={0}
        mode="full"
        autoPlay={false}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    // Setting playMode to shufflePlay ('shufflePlay')
    act(() => {
      playerRef.handlePlay('shufflePlay')
    })

    // In shufflePlay, playId is updated to a randomly selected track
    expect(playerRef.state.playId).toBeDefined()
    expect(playerRef.getCurrentPlayIndex()).toBeGreaterThanOrEqual(0)
    expect(playerRef.getCurrentPlayIndex()).toBeLessThan(sampleTracks.length)
  })

  it('TC-03: manual skip next wraps around at end of queue regardless of order mode', async () => {
    let playerRef = null

    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={sampleTracks}
        playIndex={2} // Last song
        defaultPlayMode="order"
        mode="full"
        autoPlay={false}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    expect(playerRef.getCurrentPlayIndex()).toBe(2)

    // Manual next calls onPlayNextAudio -> audioPrevAndNextBasePlayHandle -> orderLoop
    act(() => {
      playerRef.onPlayNextAudio()
    })

    // Wraps around to song 0
    expect(playerRef.getCurrentPlayIndex()).toBe(0)
  })

  it('TC-04: manual skip prev rewinds to 0s if currentTime > 1s, else moves to previous song', async () => {
    let playerRef = null

    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={sampleTracks}
        playIndex={1}
        mode="full"
        autoPlay={false}
        restartCurrentOnPrev={true}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    // Case 1: currentTime > 1s -> rewind
    playerRef.audio.currentTime = 15
    act(() => {
      playerRef.onPlayPrevAudio()
    })
    expect(playerRef.audio.currentTime).toBe(0)
    expect(playerRef.getCurrentPlayIndex()).toBe(1) // Still song 1

    // Case 2: currentTime <= 1s -> skips to previous song
    playerRef.audio.currentTime = 0.5
    act(() => {
      playerRef.onPlayPrevAudio()
    })
    expect(playerRef.getCurrentPlayIndex()).toBe(0) // Moved to song 0
  })

  it('TC-05: natural end (onAudioEnd) stops at end of queue in order mode and replays in singleLoop mode', async () => {
    let playerRef = null

    render(
      <ReactJkMusicPlayer
        ref={(r) => {
          playerRef = r
        }}
        audioLists={sampleTracks}
        playIndex={2} // Last song
        defaultPlayMode="order"
        mode="full"
        autoPlay={false}
      />,
    )

    await waitFor(() => {
      expect(playerRef).not.toBeNull()
    })

    pauseSpy.mockClear()

    // Simulate natural end at queue end in order mode
    act(() => {
      playerRef.onAudioEnd()
    })

    // In order mode, last song ending stops playback
    expect(pauseSpy).toHaveBeenCalled()

    // Switch mode to singleLoop
    act(() => {
      playerRef.setState({ playMode: 'singleLoop' })
    })
    expect(playerRef.state.playMode).toBe('singleLoop')

    const currentPlayId = playerRef.state.playId
    act(() => {
      playerRef.onAudioEnd()
    })
    // In singleLoop mode, playId remains the same track
    expect(playerRef.state.playId).toBe(currentPlayId)
  })
})
