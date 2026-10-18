import { describe, expect, it, vi } from 'vitest'
import {
  handlePlayTracks,
  handlePlayNext,
  handleAddTracks,
  handleClearQueue,
  handleSetTrack,
  extractTrackIds,
} from './jukeboxQueueSaga'
import jukeboxClient from './jukeboxClient'

vi.mock('./jukeboxSession', () => ({
  getJukeboxSessionId: () => 'jukebox-session:admin',
  getOrCreateJukeboxClientId: () => 'tab-a',
}))

describe('extractTrackIds', () => {
  it('extracts track IDs from single song object', () => {
    expect(extractTrackIds({ id: 's1', title: 'Song 1' })).toEqual(['s1'])
    expect(extractTrackIds({ mediaFileId: 'mf1', title: 'Song 1' })).toEqual([
      'mf1',
    ])
  })

  it('extracts track IDs from song map object', () => {
    const data = {
      s1: { id: 's1', title: 'Song 1' },
      s2: { id: 's2', title: 'Song 2' },
    }
    expect(extractTrackIds(data)).toEqual(['s1', 's2'])
  })

  it('handles shuffled keys with underscore prefix', () => {
    const data = {
      _s1: { id: 's1', title: 'Song 1' },
      _s2: { id: 's2', title: 'Song 2' },
    }
    expect(extractTrackIds(data)).toEqual(['s1', 's2'])
  })
})

describe('jukeboxQueueSaga handlers', () => {
  const scope = {
    sessionId: 'jukebox-session:admin',
    clientId: 'tab-a',
  }

  it('handlePlayTracks calls set, skip, and play when in jukeboxMode', async () => {
    const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})

    const state = { player: { jukeboxMode: true } }
    const action = {
      type: 'PLAYER_PLAY_TRACKS',
      id: 's2',
      data: {
        s1: { id: 's1', title: 'Song 1' },
        s2: { id: 's2', title: 'Song 2' },
      },
    }

    const gen = handlePlayTracks(action)
    // First: select state
    expect(gen.next().value.type).toBe('SELECT')
    // Next: call set
    const setCall = gen.next(state).value
    expect(setCall.type).toBe('CALL')
    expect(setCall.payload.fn).toBe(jukeboxClient.set)
    expect(setCall.payload.args).toEqual([['s1', 's2'], scope])

    // Next: call skip
    const skipCall = gen.next().value
    expect(skipCall.type).toBe('CALL')
    expect(skipCall.payload.fn).toBe(jukeboxClient.skip)
    expect(skipCall.payload.args).toEqual([1, 0, scope])

    // Next: call play
    const playCall = gen.next().value
    expect(playCall.type).toBe('CALL')
    expect(playCall.payload.fn).toBe(jukeboxClient.play)
    expect(playCall.payload.args).toEqual([scope])

    expect(gen.next().done).toBe(true)
  })

  it('handlePlayTracks does nothing when jukeboxMode is false', async () => {
    const state = { player: { jukeboxMode: false } }
    const action = {
      type: 'PLAYER_PLAY_TRACKS',
      id: 's1',
      data: { s1: { id: 's1' } },
    }
    const gen = handlePlayTracks(action)
    expect(gen.next().value.type).toBe('SELECT')
    expect(gen.next(state).done).toBe(true)
  })

  it('handlePlayNext calls add at currentIndex + 1 when in jukeboxMode', async () => {
    const state = {
      player: {
        jukeboxMode: true,
        jukeboxRemote: { currentIndex: 2 },
      },
    }
    const action = {
      type: 'PLAYER_PLAY_NEXT',
      data: { s3: { id: 's3' } },
    }
    const gen = handlePlayNext(action)
    expect(gen.next().value.type).toBe('SELECT')
    const addCall = gen.next(state).value
    expect(addCall.type).toBe('CALL')
    expect(addCall.payload.fn).toBe(jukeboxClient.add)
    expect(addCall.payload.args).toEqual([['s3'], 3, scope])
    expect(gen.next().done).toBe(true)
  })

  it('handleAddTracks calls add with -1 index (append) when in jukeboxMode', async () => {
    const state = { player: { jukeboxMode: true } }
    const action = {
      type: 'PLAYER_ADD_TRACKS',
      data: { s4: { id: 's4' } },
    }
    const gen = handleAddTracks(action)
    expect(gen.next().value.type).toBe('SELECT')
    const addCall = gen.next(state).value
    expect(addCall.type).toBe('CALL')
    expect(addCall.payload.fn).toBe(jukeboxClient.add)
    expect(addCall.payload.args).toEqual([['s4'], -1, scope])
    expect(gen.next().done).toBe(true)
  })

  it('handleSetTrack calls set, skip(0, 0), and play when in jukeboxMode', async () => {
    const state = { player: { jukeboxMode: true } }
    const action = {
      type: 'PLAYER_SET_TRACK',
      data: { id: 'single1' },
    }
    const gen = handleSetTrack(action)
    expect(gen.next().value.type).toBe('SELECT')
    const setCall = gen.next(state).value
    expect(setCall.type).toBe('CALL')
    expect(setCall.payload.fn).toBe(jukeboxClient.set)
    expect(setCall.payload.args).toEqual([['single1'], scope])

    const skipCall = gen.next().value
    expect(skipCall.type).toBe('CALL')
    expect(skipCall.payload.fn).toBe(jukeboxClient.skip)
    expect(skipCall.payload.args).toEqual([0, 0, scope])

    const playCall = gen.next().value
    expect(playCall.type).toBe('CALL')
    expect(playCall.payload.fn).toBe(jukeboxClient.play)
    expect(playCall.payload.args).toEqual([scope])

    expect(gen.next().done).toBe(true)
  })

  it('handleClearQueue calls stop and set([]) when in jukeboxMode', async () => {
    const state = { player: { jukeboxMode: true } }
    const action = { type: 'PLAYER_CLEAR_QUEUE' }
    const gen = handleClearQueue(action)
    expect(gen.next().value.type).toBe('SELECT')
    const stopCall = gen.next(state).value
    expect(stopCall.type).toBe('CALL')
    expect(stopCall.payload.fn).toBe(jukeboxClient.stop)
    expect(stopCall.payload.args).toEqual([scope])

    const setCall = gen.next().value
    expect(setCall.type).toBe('CALL')
    expect(setCall.payload.fn).toBe(jukeboxClient.set)
    expect(setCall.payload.args).toEqual([[], scope])

    expect(gen.next().done).toBe(true)
  })
})
