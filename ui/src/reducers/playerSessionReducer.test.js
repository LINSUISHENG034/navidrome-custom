import { describe, it, expect } from 'vitest'
import { playerReducer } from './playerReducer'
import { PLAYER_JUKEBOX_SESSION_STATUS } from '../actions'

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
})
