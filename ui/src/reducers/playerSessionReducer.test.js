import { describe, it, expect } from 'vitest'
import { playerReducer } from './playerReducer'
import {
  PLAYER_JUKEBOX_SESSION_STATUS,
  PLAYER_SET_JUKEBOX_MODE,
} from '../actions'
import { audioVolumeToUiVolume } from '../audioplayer/volumeMapping'

describe('playerReducer jukebox session state', () => {
  it('stores authoritative remote jukebox session status', () => {
    const next = playerReducer(undefined, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        currentIndex: 2,
        trackId: 't3',
        position: 41,
        playing: true,
      },
    })

    expect(next.jukeboxSession.sessionId).toBe('s1')
    expect(next.jukeboxSession.currentIndex).toBe(2)
  })

  it('syncs volume from session gain in jukebox mode', () => {
    const state = playerReducer(undefined, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: true, device: 'pulse/test' },
    })

    const next = playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        gain: 0.81,
      },
    })

    expect(next.volume).toBe(audioVolumeToUiVolume(0.81))
  })

  it('does not overwrite volume when not in jukebox mode', () => {
    const initial = {
      ...playerReducer(undefined, { type: '@@INIT' }),
      volume: 0.33,
      jukeboxMode: false,
    }

    const next = playerReducer(initial, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        gain: 0.81,
      },
    })

    expect(next.volume).toBe(0.33)
  })

  it('clears jukeboxSession when jukebox mode is disabled', () => {
    const initial = {
      ...playerReducer(undefined, { type: '@@INIT' }),
      jukeboxMode: true,
      jukeboxSession: { sessionId: 's1', currentIndex: 2 },
    }

    const next = playerReducer(initial, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: false, device: null },
    })

    expect(next.jukeboxMode).toBe(false)
    expect(next.jukeboxSession).toBeNull()
  })
})
