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
})
