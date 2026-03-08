import { describe, expect, it } from 'vitest'
import { addTracks, syncQueue } from '../actions/player'
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
})
