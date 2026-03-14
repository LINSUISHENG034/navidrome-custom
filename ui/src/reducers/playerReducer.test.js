import { describe, expect, it } from 'vitest'
import { addTracks, syncQueue } from '../actions/player'
import { PLAYER_JUKEBOX_SESSION_STATUS } from '../actions'
import { playerReducer } from './playerReducer'

describe('playerReducer queue isolation', () => {
  it('does not mutate the previous queue when adding tracks', () => {
    const sharedQueue = [
      { trackId: 'a', uuid: 'ua' },
      { trackId: 'b', uuid: 'ub' },
    ]
    const state = playerReducer(undefined, syncQueue({}, sharedQueue))

    const next = playerReducer(
      state,
      addTracks({ c: { id: 'c', title: 'Song C', artist: 'Artist C' } }),
    )

    expect(state.queue).toHaveLength(2)
    expect(next.queue).toHaveLength(3)
    expect(next.queue).not.toBe(state.queue)
  })

  it('preserves remote authority when a recovering control update omits playback fields', () => {
    const active = playerReducer(undefined, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'attached',
        currentIndex: 2,
        trackId: 't3',
        playing: true,
      },
    })

    const recovering = playerReducer(active, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'recovering',
      },
    })

    expect(recovering.jukeboxControl.ownershipState).toBe('recovering')
    expect(recovering.jukeboxRemote.currentIndex).toBe(2)
    expect(recovering.jukeboxRemote.trackId).toBe('t3')
    expect(recovering.jukeboxRemote.playing).toBe(true)
  })

  it('does not keep stale termination metadata in the compatibility mirror', () => {
    const terminated = playerReducer(undefined, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'detached',
        terminationReason: 'stale_expired',
        currentIndex: 2,
        trackId: 't3',
      },
    })

    const recovered = playerReducer(terminated, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'attached',
        currentIndex: 2,
        trackId: 't3',
      },
    })

    expect(recovered.jukeboxControl.terminationReason).toBeNull()
    expect(recovered.jukeboxSession.terminationReason ?? null).toBeNull()
  })
})
