import { describe, it, expect } from 'vitest'
import {
  selectEffectiveCurrentTrack,
  selectEffectiveJukeboxGain,
  selectEffectiveJukeboxPlaying,
  selectEffectiveJukeboxPosition,
} from './playerSelectors'

describe('playerSelectors', () => {
  it('selects remote current track in jukebox mode', () => {
    const state = {
      player: {
        jukeboxMode: true,
        queue: [{ trackId: 't1' }, { trackId: 't3' }],
        current: { trackId: 't1' },
        jukeboxControl: { ownershipState: 'attached' },
        jukeboxRemote: { currentIndex: 1, trackId: 't3', position: 41 },
      },
    }

    expect(selectEffectiveCurrentTrack(state)?.trackId).toBe('t3')
  })

  it('falls back to local current in non-jukebox mode', () => {
    const current = { trackId: 'local-track' }
    const state = {
      player: {
        jukeboxMode: false,
        queue: [{ trackId: 't1' }, { trackId: 't3' }],
        current,
        jukeboxControl: { ownershipState: 'attached' },
        jukeboxRemote: { currentIndex: 1, trackId: 't3', position: 41 },
      },
    }

    expect(selectEffectiveCurrentTrack(state)).toBe(current)
  })

  it('falls back to trackId lookup when index mismatches', () => {
    const state = {
      player: {
        jukeboxMode: true,
        queue: [{ trackId: 't1' }, { trackId: 't3' }],
        current: { trackId: 't1' },
        jukeboxControl: { ownershipState: 'recovering' },
        jukeboxRemote: { currentIndex: 0, trackId: 't3', position: 41 },
      },
    }

    expect(selectEffectiveCurrentTrack(state)?.trackId).toBe('t3')
  })

  it('returns null when queue is empty in jukebox mode', () => {
    const state = {
      player: {
        jukeboxMode: true,
        queue: [],
        current: { trackId: 't1' },
        jukeboxControl: { ownershipState: 'recovering' },
        jukeboxRemote: { currentIndex: 1, trackId: 't3', position: 41 },
      },
    }

    expect(selectEffectiveCurrentTrack(state)).toBeNull()
  })

  it('returns current when jukeboxRemote is null', () => {
    const current = { trackId: 'local-track' }
    const state = {
      player: {
        jukeboxMode: true,
        queue: [{ trackId: 't1' }, { trackId: 't3' }],
        current,
        jukeboxControl: { ownershipState: 'recovering' },
        jukeboxRemote: null,
      },
    }

    expect(selectEffectiveCurrentTrack(state)).toBe(current)
  })

  it('falls back to current when index is out of range and trackId is missing', () => {
    const current = { trackId: 'local-track' }
    const state = {
      player: {
        jukeboxMode: true,
        queue: [{ trackId: 't1' }],
        current,
        jukeboxControl: { ownershipState: 'recovering' },
        jukeboxRemote: { currentIndex: 9 },
      },
    }

    expect(selectEffectiveCurrentTrack(state)).toBe(current)
  })

  it('prefers jukeboxRemote playing/gain/position', () => {
    const state = {
      player: {
        jukeboxMode: true,
        jukeboxControl: { ownershipState: 'recovering' },
        jukeboxRemote: { playing: true, gain: 0.8, position: 41 },
      },
    }

    expect(selectEffectiveJukeboxPlaying(state)).toBe(true)
    expect(selectEffectiveJukeboxGain(state)).toBe(0.8)
    expect(selectEffectiveJukeboxPosition(state)).toBe(41)
  })

  it('returns defaults when jukeboxRemote is unavailable', () => {
    const state = {
      player: {
        jukeboxMode: true,
        jukeboxControl: { ownershipState: 'recovering' },
        jukeboxRemote: null,
      },
    }

    expect(selectEffectiveJukeboxPlaying(state)).toBe(false)
    expect(selectEffectiveJukeboxGain(state)).toBe(0.5)
    expect(selectEffectiveJukeboxPosition(state)).toBe(0)
  })
})
