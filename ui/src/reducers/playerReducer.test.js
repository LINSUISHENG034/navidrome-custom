import { describe, expect, it } from 'vitest'
import { addTracks, syncQueue } from '../actions/player'
import {
  PLAYER_CURRENT,
  PLAYER_JUKEBOX_SESSION_STATUS,
  PLAYER_REFRESH_QUEUE,
  PLAYER_SYNC_QUEUE,
} from '../actions'
import { playerReducer } from './playerReducer'

describe('playerReducer', () => {
  describe('queue isolation', () => {
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

  describe('pending track selection survives SYNC_QUEUE and premature CURRENT', () => {
    const stateAfterPlayTracks = {
      queue: [
        { trackId: 's1', uuid: 'aaa', name: 'Song 1' },
        { trackId: 's2', uuid: 'bbb', name: 'Song 2' },
        { trackId: 's3', uuid: 'ccc', name: 'Song 3' },
      ],
      current: { uuid: 'ccc', name: 'Song 3' },
      playIndex: 0,
      savedPlayIndex: 2,
      clear: true,
      volume: 1,
    }

    it('SYNC_QUEUE preserves pending playIndex and clear', () => {
      const newQueue = [
        { trackId: 's1', uuid: 'xxx', name: 'Song 1' },
        { trackId: 's2', uuid: 'yyy', name: 'Song 2' },
        { trackId: 's3', uuid: 'zzz', name: 'Song 3' },
      ]
      const action = {
        type: PLAYER_SYNC_QUEUE,
        data: { audioInfo: {}, audioLists: newQueue },
      }
      const result = playerReducer(stateAfterPlayTracks, action)
      expect(result.playIndex).toBe(0)
      expect(result.clear).toBe(true)
      expect(result.queue).toEqual(newQueue)
      expect(result.queue).not.toBe(newQueue)
    })

    it('SYNC_QUEUE clears playIndex when no pending selection', () => {
      const stateNoPending = { ...stateAfterPlayTracks, playIndex: undefined }
      const action = {
        type: PLAYER_SYNC_QUEUE,
        data: { audioInfo: {}, audioLists: stateNoPending.queue },
      }
      const result = playerReducer(stateNoPending, action)
      expect(result.playIndex).toBeUndefined()
      expect(result.clear).toBe(false)
    })

    it('CURRENT for old track preserves pending playIndex', () => {
      const stateAfterSync = {
        ...stateAfterPlayTracks,
        queue: [
          { trackId: 's1', uuid: 'xxx', name: 'Song 1' },
          { trackId: 's2', uuid: 'yyy', name: 'Song 2' },
          { trackId: 's3', uuid: 'zzz', name: 'Song 3' },
        ],
      }
      const action = {
        type: PLAYER_CURRENT,
        data: { uuid: 'zzz', name: 'Song 3', volume: 1 },
      }
      const result = playerReducer(stateAfterSync, action)
      expect(result.playIndex).toBe(0)
      expect(result.clear).toBe(true)
      expect(result.savedPlayIndex).toBe(2)
    })

    it('CURRENT for correct track consumes pending playIndex', () => {
      const stateAfterSync = {
        ...stateAfterPlayTracks,
        queue: [
          { trackId: 's1', uuid: 'xxx', name: 'Song 1' },
          { trackId: 's2', uuid: 'yyy', name: 'Song 2' },
          { trackId: 's3', uuid: 'zzz', name: 'Song 3' },
        ],
      }
      const action = {
        type: PLAYER_CURRENT,
        data: { uuid: 'xxx', name: 'Song 1', volume: 1 },
      }
      const result = playerReducer(stateAfterSync, action)
      expect(result.playIndex).toBeUndefined()
      expect(result.clear).toBe(false)
      expect(result.savedPlayIndex).toBe(0)
      expect(result.current.name).toBe('Song 1')
    })
  })

  describe('play new album after closing player (issue #5440)', () => {
    it('SYNC_QUEUE preserves pending playIndex=0 after clearQueue', () => {
      // Scenario: user plays album A, advances to track 3, closes player,
      // then plays album B. After clearQueue, savedPlayIndex=0.
      // PLAYER_PLAY_TRACKS sets playIndex=0. SYNC_QUEUE must NOT clear it.
      const stateAfterClearThenPlay = {
        queue: [
          { trackId: 'b1', uuid: 'u1', name: 'B Song 1' },
          { trackId: 'b2', uuid: 'u2', name: 'B Song 2' },
          { trackId: 'b3', uuid: 'u3', name: 'B Song 3' },
        ],
        current: {},
        playIndex: 0,
        savedPlayIndex: 0, // reset by clearQueue
        clear: true,
        volume: 1,
      }

      const action = {
        type: PLAYER_SYNC_QUEUE,
        data: {
          audioInfo: {},
          audioLists: stateAfterClearThenPlay.queue,
        },
      }
      const result = playerReducer(stateAfterClearThenPlay, action)
      expect(result.playIndex).toBe(0)
      expect(result.clear).toBe(true)
    })

    it('CURRENT for wrong track preserves pending playIndex=0 after clearQueue', () => {
      // The music player fires onAudioPlay for the old track (at index 3)
      // before switching to the new track at index 0.
      const stateAfterClearThenPlay = {
        queue: [
          { trackId: 'b1', uuid: 'u1', name: 'B Song 1' },
          { trackId: 'b2', uuid: 'u2', name: 'B Song 2' },
          { trackId: 'b3', uuid: 'u3', name: 'B Song 3' },
          { trackId: 'b4', uuid: 'u4', name: 'B Song 4' },
        ],
        current: {},
        playIndex: 0,
        savedPlayIndex: 0,
        clear: true,
        volume: 1,
      }

      // Player reports track at index 3 as current (stale callback)
      const action = {
        type: PLAYER_CURRENT,
        data: { uuid: 'u4', name: 'B Song 4', volume: 1 },
      }
      const result = playerReducer(stateAfterClearThenPlay, action)
      expect(result.playIndex).toBe(0)
      expect(result.clear).toBe(true)
    })

    it('CURRENT for correct track consumes pending playIndex=0', () => {
      const stateAfterClearThenPlay = {
        queue: [
          { trackId: 'b1', uuid: 'u1', name: 'B Song 1' },
          { trackId: 'b2', uuid: 'u2', name: 'B Song 2' },
        ],
        current: {},
        playIndex: 0,
        savedPlayIndex: 0,
        clear: true,
        volume: 1,
      }

      // Player confirms it switched to track at index 0
      const action = {
        type: PLAYER_CURRENT,
        data: { uuid: 'u1', name: 'B Song 1', volume: 1 },
      }
      const result = playerReducer(stateAfterClearThenPlay, action)
      expect(result.playIndex).toBeUndefined()
      expect(result.clear).toBe(false)
      expect(result.savedPlayIndex).toBe(0)
    })
  })

  describe('PLAYER_REFRESH_QUEUE', () => {
    it('clamps negative savedPlayIndex to 0', () => {
      const state = {
        queue: [
          { trackId: 'song-1', musicSrc: 'old-url', uuid: 'a' },
          { trackId: 'song-2', musicSrc: 'old-url', uuid: 'b' },
        ],
        savedPlayIndex: -1,
        current: {},
        clear: false,
        volume: 1,
      }
      const action = { type: PLAYER_REFRESH_QUEUE, data: {} }
      const result = playerReducer(state, action)
      expect(result.playIndex).toBe(0)
    })

    it('preserves valid savedPlayIndex', () => {
      const state = {
        queue: [
          { trackId: 'song-1', musicSrc: 'old-url', uuid: 'a' },
          { trackId: 'song-2', musicSrc: 'old-url', uuid: 'b' },
        ],
        savedPlayIndex: 1,
        current: {},
        clear: false,
        volume: 1,
      }
      const action = { type: PLAYER_REFRESH_QUEUE, data: {} }
      const result = playerReducer(state, action)
      expect(result.playIndex).toBe(1)
    })

    it('uses savedPlayIndex of 0 correctly', () => {
      const state = {
        queue: [{ trackId: 'song-1', musicSrc: 'old-url', uuid: 'a' }],
        savedPlayIndex: 0,
        current: {},
        clear: false,
        volume: 1,
      }
      const action = { type: PLAYER_REFRESH_QUEUE, data: {} }
      const result = playerReducer(state, action)
      expect(result.playIndex).toBe(0)
    })
  })

  describe('jukebox session state', () => {
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

    it('clears stale termination metadata without keeping a legacy mirror', () => {
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
      expect(recovered).not.toHaveProperty('jukeboxSession')
    })
  })
})
