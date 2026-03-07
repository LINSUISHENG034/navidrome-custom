import { describe, it, expect } from 'vitest'
import { selectEffectiveCurrentTrack } from './playerSelectors'

describe('playerSelectors', () => {
  it('selects remote current track in jukebox mode', () => {
    const state = {
      player: {
        jukeboxMode: true,
        queue: [{ trackId: 't1' }, { trackId: 't3' }],
        current: { trackId: 't1' },
        jukeboxSession: { currentIndex: 1, trackId: 't3', position: 41 },
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
        jukeboxSession: { currentIndex: 1, trackId: 't3', position: 41 },
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
        jukeboxSession: { currentIndex: 0, trackId: 't3', position: 41 },
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
        jukeboxSession: { currentIndex: 1, trackId: 't3', position: 41 },
      },
    }

    expect(selectEffectiveCurrentTrack(state)).toBeNull()
  })

  it('returns current when jukeboxSession is null', () => {
    const current = { trackId: 'local-track' }
    const state = {
      player: {
        jukeboxMode: true,
        queue: [{ trackId: 't1' }, { trackId: 't3' }],
        current,
        jukeboxSession: null,
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
        jukeboxSession: { currentIndex: 9 },
      },
    }

    expect(selectEffectiveCurrentTrack(state)).toBe(current)
  })
})
