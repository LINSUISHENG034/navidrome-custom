import { describe, it, expect } from 'vitest'
import { playerReducer } from './playerReducer'
import {
  PLAYER_JUKEBOX_SESSION_STATUS,
  PLAYER_SET_JUKEBOX_MODE,
} from '../actions'
import { audioVolumeToUiVolume } from '../audioplayer/volumeMapping'

describe('playerReducer jukebox session state', () => {
  it('stores jukebox control and remote state separately', () => {
    const next = playerReducer(undefined, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'attached',
        currentIndex: 2,
        trackId: 't3',
        position: 41,
        playing: true,
      },
    })

    expect(next.jukeboxControl.sessionId).toBe('s1')
    expect(next.jukeboxControl.ownerClientId).toBe('tab-1')
    expect(next.jukeboxControl.ownershipState).toBe('attached')
    expect(next.jukeboxRemote.currentIndex).toBe(2)
    expect(next.jukeboxRemote.trackId).toBe('t3')
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

  it('clears jukebox control and remote state when jukebox mode is disabled', () => {
    const initial = {
      ...playerReducer(undefined, { type: '@@INIT' }),
      jukeboxMode: true,
      jukeboxControl: { sessionId: 's1', ownershipState: 'attached' },
      jukeboxRemote: { currentIndex: 2, trackId: 't3' },
    }

    const next = playerReducer(initial, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: false, device: null },
    })

    expect(next.jukeboxMode).toBe(false)
    expect(next.jukeboxControl).toBeNull()
    expect(next.jukeboxRemote).toBeNull()
  })

  it('preserves remote playback state while control is recovering', () => {
    const next = playerReducer(undefined, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'recovering',
        currentIndex: 1,
        trackId: 't2',
        playing: true,
        position: 33,
      },
    })

    expect(next.jukeboxControl.ownershipState).toBe('recovering')
    expect(next.jukeboxRemote.currentIndex).toBe(1)
    expect(next.jukeboxRemote.trackId).toBe('t2')
  })
})
