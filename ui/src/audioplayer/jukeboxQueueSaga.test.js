import React from 'react'
import { Provider } from 'react-redux'
import { ThemeProvider, createTheme } from '@material-ui/core/styles'
import { applyMiddleware, combineReducers, createStore } from 'redux'
import createSagaMiddleware, { runSaga } from 'redux-saga'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import jukeboxQueueSaga, {
  handlePlayTracks,
  handlePlayNext,
  handleAddTracks,
  handleClearQueue,
  handleSetTrack,
  extractTrackIds,
} from './jukeboxQueueSaga'
import jukeboxClient from './jukeboxClient'
import { BatchPlayButton } from '../common/BatchPlayButton'
import {
  addTracks,
  beginJukeboxEpisode,
  clearQueue,
  currentPlaying,
  endJukeboxEpisode,
  playNext,
  playTracks,
  setTrack,
  setJukeboxMode,
  syncQueue,
  updateJukeboxSessionStatus,
} from '../actions'
import { playerReducer } from '../reducers/playerReducer'
import {
  createPlaybackControlModule,
  PlaybackStatus,
} from './playbackControlModule'

import { createMockRemoteAudioAdapter } from './mockRemoteAudioAdapter'
import { createBrowserAudioAdapter } from './browserAudioAdapter'

const listApi = vi.hoisted(() => ({
  getMany: vi.fn(),
  unselectAll: vi.fn(),
  notify: vi.fn(),
}))

vi.mock('react-admin', async (importOriginal) => ({
  ...(await importOriginal()),
  useDataProvider: () => ({ getMany: listApi.getMany }),
  useTranslate: () => (key) => key,
  useUnselectAll: () => listApi.unselectAll,
  useNotify: () => listApi.notify,
}))

vi.mock('./jukeboxSession', async (importOriginal) => ({
  ...(await importOriginal()),
  getJukeboxSessionId: () => 'jukebox-session:admin',
  getOrCreateJukeboxClientId: () => 'tab-a',
}))

describe('opt-in unified host list entry', () => {
  const song = { id: 'song-a', title: 'Song A', artist: 'Artist' }
  const hosts = []

  beforeEach(() => {
    listApi.getMany.mockReset()
    listApi.unselectAll.mockClear()
    listApi.notify.mockClear()
  })

  afterEach(() => {
    hosts.splice(0).forEach(({ task, controller }) => {
      task.cancel()
      controller.setAdapter(null)
    })
    vi.restoreAllMocks()
  })

  function createCandidateHost({
    adapterOptions = {},
    withController = true,
    engineMode = 'remote',
  } = {}) {
    const controller = createPlaybackControlModule()
    let adapter
    if (engineMode === 'local') {
      const audioElement = document.createElement('audio')
      let paused = true
      Object.defineProperty(audioElement, 'paused', { get: () => paused })
      vi.spyOn(audioElement, 'play').mockImplementation(() => {
        paused = false
        audioElement.dispatchEvent(new Event('playing'))
        return Promise.resolve()
      })
      vi.spyOn(audioElement, 'pause').mockImplementation(() => {
        if (!paused) {
          paused = true
          audioElement.dispatchEvent(new Event('pause'))
        }
      })
      vi.spyOn(audioElement, 'load').mockImplementation(() => {})
      adapter = createBrowserAudioAdapter({ ...adapterOptions, audioElement })
    } else {
      adapter = createMockRemoteAudioAdapter(adapterOptions)
    }
    controller.setAdapter(adapter)
    const sagaMiddleware = createSagaMiddleware({
      context: withController ? { playbackController: controller } : {},
    })
    const store = createStore(
      combineReducers({ player: playerReducer }),
      applyMiddleware(sagaMiddleware),
    )
    hosts.push({ controller, task: sagaMiddleware.run(jukeboxQueueSaga) })
    return { controller, adapter, store, sagaMiddleware }
  }

  function renderListAction(host, action, selectedIds = [song.id]) {
    return render(
      React.createElement(
        Provider,
        { store: host.store },
        React.createElement(
          ThemeProvider,
          { theme: createTheme() },
          React.createElement(BatchPlayButton, {
            resource: 'song',
            selectedIds,
            action,
            label: 'Queue selected songs',
            icon: React.createElement('span'),
          }),
        ),
      ),
    )
  }

  it.each(['local', 'remote'])(
    'TC-QC-10: actual list Add uses the normalized occurrence once with the %s adapter',
    async (engineMode) => {
      listApi.getMany.mockResolvedValue({ data: [song] })
      const legacyAdd = vi.spyOn(jukeboxClient, 'add').mockResolvedValue({})
      const host = createCandidateHost({ engineMode })
      renderListAction(host, addTracks)

      fireEvent.click(
        screen.getByRole('button', { name: 'Queue selected songs' }),
      )

      await waitFor(() =>
        expect(host.controller.getState().queue).toHaveLength(1),
      )
      const queued = host.controller.getState().queue[0]
      expect(queued.trackId).toBe('song-a')
      expect(queued.queueItemId).toBe(
        host.store.getState().player.queue[0].uuid,
      )
      expect(host.controller.getMetrics().intentsIssued).toBe(1)
      expect(host.store.getState().player.jukeboxMode).toBe(false)
      expect(legacyAdd).not.toHaveBeenCalled()
    },
  )

  it.each(['local', 'remote'])(
    'TC-QC-10: actual list Play Next inserts its own occurrence with the %s adapter',
    async (engineMode) => {
      const second = { id: 'song-b', title: 'Song B' }
      const next = { id: 'song-next', title: 'Play next song' }
      listApi.getMany
        .mockResolvedValueOnce({ data: [song, second] })
        .mockResolvedValueOnce({ data: [next] })
      const host = createCandidateHost({ engineMode })
      const firstAction = renderListAction(host, addTracks, [
        song.id,
        second.id,
      ])
      fireEvent.click(
        screen.getByRole('button', { name: 'Queue selected songs' }),
      )
      await waitFor(() =>
        expect(host.controller.getState().queue).toHaveLength(2),
      )
      host.store.dispatch(currentPlaying(host.store.getState().player.queue[0]))
      firstAction.unmount()
      renderListAction(host, playNext, [next.id])

      fireEvent.click(
        screen.getByRole('button', { name: 'Queue selected songs' }),
      )

      await waitFor(() =>
        expect(host.controller.getState().queue).toHaveLength(3),
      )
      expect(
        host.controller.getState().queue.map((item) => item.trackId),
      ).toEqual(['song-a', 'song-next', 'song-b'])
      expect(host.controller.getState().queue[1].queueItemId).toBe(
        host.store.getState().player.queue[1].uuid,
      )
      expect(host.controller.getMetrics().intentsIssued).toBe(2)
    },
  )

  it.each(['local', 'remote'])(
    'TC-QC-10: actual list Play All starts distinct same-song occurrences with the %s adapter',
    async (engineMode) => {
      const first = {
        id: 'entry-1',
        mediaFileId: 'same-song',
        title: 'Same song',
      }
      const second = { ...first, id: 'entry-2' }
      listApi.getMany.mockResolvedValue({ data: [second, first] })
      const host = createCandidateHost({ engineMode })
      renderListAction(host, playTracks, [first.id, second.id])

      fireEvent.click(
        screen.getByRole('button', { name: 'Queue selected songs' }),
      )

      await waitFor(() =>
        expect(host.controller.getState().status).toBe(PlaybackStatus.PLAYING),
      )
      const queue = host.controller.getState().queue
      expect(queue.map((item) => item.trackId)).toEqual([
        'same-song',
        'same-song',
      ])
      expect(queue.map((item) => item.song.id)).toEqual(['entry-1', 'entry-2'])
      expect(queue.map((item) => item.queueItemId)).toEqual(
        host.store.getState().player.queue.map((item) => item.uuid),
      )
      expect(new Set(queue.map((item) => item.queueItemId)).size).toBe(2)
      expect(host.controller.getState().currentQueueItemId).toBe(
        queue[0].queueItemId,
      )
      expect(host.controller.getMetrics().intentsIssued).toBe(1)
    },
  )

  it.each(['local', 'remote'])(
    'TC-QC-10: candidate single-track selection starts once with the %s adapter',
    async (engineMode) => {
      const host = createCandidateHost({ engineMode })

      host.store.dispatch(setTrack(song))

      await waitFor(() =>
        expect(host.controller.getState().status).toBe(PlaybackStatus.PLAYING),
      )
      expect(
        host.controller.getState().queue.map((item) => item.trackId),
      ).toEqual(['song-a'])
      expect(host.controller.getState().currentQueueItemId).toBe(
        host.store.getState().player.queue[0].uuid,
      )
      expect(host.controller.getMetrics().intentsIssued).toBe(1)
    },
  )

  it.each(['local', 'remote'])(
    'TC-QC-10: candidate Clear stops the %s adapter without a legacy engine command',
    async (engineMode) => {
      listApi.getMany.mockResolvedValue({ data: [song] })
      const legacyStop = vi.spyOn(jukeboxClient, 'stop').mockResolvedValue({})
      const legacySet = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})
      const host = createCandidateHost({ engineMode })
      renderListAction(host, playTracks)
      fireEvent.click(
        screen.getByRole('button', { name: 'Queue selected songs' }),
      )
      await waitFor(() =>
        expect(host.controller.getState().status).toBe(PlaybackStatus.PLAYING),
      )

      host.store.dispatch(clearQueue())

      await waitFor(() => expect(host.controller.getState().queue).toEqual([]))
      expect(host.controller.getState().currentQueueItemId).toBeNull()
      expect(host.controller.getState().status).toBe(PlaybackStatus.IDLE)
      expect(host.controller.getState().position).toBe(0)
      expect(host.controller.getMetrics().intentsIssued).toBe(2)
      expect(legacyStop).not.toHaveBeenCalled()
      expect(legacySet).not.toHaveBeenCalled()
    },
  )

  it('TC-QC-10: rapid list Adds retain distinct normalized instances while the first engine command is pending', async () => {
    listApi.getMany.mockResolvedValue({ data: [song] })
    const host = createCandidateHost({ adapterOptions: { delayMs: 40 } })
    renderListAction(host, addTracks)
    const button = screen.getByRole('button', { name: 'Queue selected songs' })

    fireEvent.click(button)
    fireEvent.click(button)

    await waitFor(() =>
      expect(host.controller.getState().queue).toHaveLength(2),
    )
    const queue = host.controller.getState().queue
    expect(queue.map((item) => item.trackId)).toEqual(['song-a', 'song-a'])
    expect(queue.map((item) => item.queueItemId)).toEqual(
      host.store.getState().player.queue.map((item) => item.uuid),
    )
    expect(new Set(queue.map((item) => item.queueItemId)).size).toBe(2)
    expect(host.controller.getMetrics().intentsIssued).toBe(2)
  })

  it('TC-QC-10: rejected list Add is visible without legacy fallback or replay of its optimistic row', async () => {
    const nextSong = { id: 'song-b', title: 'Song B' }
    listApi.getMany
      .mockResolvedValueOnce({ data: [song] })
      .mockResolvedValueOnce({ data: [nextSong] })
    const legacyAdd = vi.spyOn(jukeboxClient, 'add').mockResolvedValue({})
    const host = createCandidateHost({
      adapterOptions: {
        failure: { action: 'addQueueItems', code: 'PERMISSION_DENIED' },
      },
    })
    host.store.dispatch(setJukeboxMode(true))
    const rejectedAction = renderListAction(host, addTracks)

    fireEvent.click(
      screen.getByRole('button', { name: 'Queue selected songs' }),
    )

    await waitFor(() =>
      expect(host.controller.getState().error?.code).toBe('PERMISSION_DENIED'),
    )
    expect(host.controller.getState().status).toBe(PlaybackStatus.ERROR)
    expect(host.controller.getState().queue).toEqual([])
    expect(legacyAdd).not.toHaveBeenCalled()
    rejectedAction.unmount()
    host.adapter.setFailure(null)
    renderListAction(host, addTracks, [nextSong.id])

    fireEvent.click(
      screen.getByRole('button', { name: 'Queue selected songs' }),
    )

    await waitFor(() =>
      expect(host.controller.getState().queue).toHaveLength(1),
    )
    expect(host.controller.getState().queue[0].trackId).toBe('song-b')
    expect(host.controller.getState().queue[0].queueItemId).toBe(
      host.store.getState().player.queue[1].uuid,
    )
    expect(host.controller.getMetrics().intentsIssued).toBe(2)
    expect(legacyAdd).not.toHaveBeenCalled()
  })

  it('TC-QC-10: missing candidate context preserves the existing host jukebox entry', async () => {
    listApi.getMany.mockResolvedValue({ data: [song] })
    const legacyAdd = vi.spyOn(jukeboxClient, 'add').mockResolvedValue({})
    const host = createCandidateHost({ withController: false })
    host.store.dispatch(setJukeboxMode(true))
    // Reducer-admitted authority: legacy handlers egress only under grant.
    host.store.dispatch(
      beginJukeboxEpisode({
        token: 'host-token',
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      }),
    )
    host.store.dispatch(
      updateJukeboxSessionStatus(
        {
          sessionId: 'jukebox-session:admin',
          ownerClientId: 'tab-a',
          ownershipState: 'attached',
          deviceName: 'pulse/test',
          bindingGeneration: 2,
          stateSequence: 10,
        },
        {
          source: 'atomic-session-snapshot',
          sessionId: 'jukebox-session:admin',
          clientId: 'tab-a',
          token: 'host-token',
          revision: 0,
          requestBinding: {
            bindingGeneration: null,
            deviceName: null,
          },
        },
      ),
    )
    renderListAction(host, addTracks)

    fireEvent.click(
      screen.getByRole('button', { name: 'Queue selected songs' }),
    )

    await waitFor(() => expect(legacyAdd).toHaveBeenCalledTimes(1))
    expect(legacyAdd).toHaveBeenCalledWith(['song-a'], -1, {
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
    })
    expect(host.controller.getMetrics().intentsIssued).toBe(0)
    expect(host.controller.getState().queue).toEqual([])
  })

  it('TC-QC-11: restored normalized queue IDs and current-item projections do not replay list intents', async () => {
    listApi.getMany.mockResolvedValue({ data: [song] })
    const host = createCandidateHost()
    renderListAction(host, addTracks)
    fireEvent.click(
      screen.getByRole('button', { name: 'Queue selected songs' }),
    )
    await waitFor(() =>
      expect(host.controller.getState().queue).toHaveLength(1),
    )
    const restored = JSON.parse(
      JSON.stringify(host.store.getState().player.queue),
    )
    const before = host.controller.getMetrics()

    host.store.dispatch(syncQueue(restored[0], restored))
    host.store.dispatch(currentPlaying(restored[0]))

    expect(host.store.getState().player.queue[0].uuid).toBe(restored[0].uuid)
    expect(host.controller.getState().queue[0].queueItemId).toBe(
      restored[0].uuid,
    )
    expect(host.controller.getMetrics()).toEqual(before)
  })
})

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
  // Reducer-admitted authority fixture: the legacy handlers egress only while
  // the shared authority grants this document's captured scope.
  const admittedState = (playerOverrides = {}) => ({
    player: {
      jukeboxMode: true,
      jukeboxControl: {
        sessionId: 'jukebox-session:admin',
        ownerClientId: 'tab-a',
        ownershipState: 'attached',
        controlGranted: true,
      },
      jukeboxRemote: {
        bindingGeneration: 2,
        stateSequence: 10,
        deviceName: 'pulse/test',
      },
      jukeboxEpisodes: {
        'fixture-token': {
          sessionId: 'jukebox-session:admin',
          clientId: 'tab-a',
          requestedDeviceName: 'pulse/test',
          revision: 0,
          reconciledBinding: {
            bindingGeneration: 2,
            canonicalDevice: 'pulse/test',
          },
          disabled: false,
        },
      },
      ...playerOverrides,
    },
  })

  it('handlePlayTracks calls set, skip, and play when in jukeboxMode', async () => {
    const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})

    const state = admittedState()
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
    // Eligibility re-check against the current authority
    expect(gen.next(state).value.type).toBe('SELECT')
    // Next: call set
    const setCall = gen.next(state).value
    expect(setCall.type).toBe('CALL')
    expect(setCall.payload.fn).toBe(jukeboxClient.set)
    expect(setCall.payload.args).toEqual([['s1', 's2'], scope])

    // Re-check after the await, then call skip
    expect(gen.next().value.type).toBe('SELECT')
    const skipCall = gen.next(state).value
    expect(skipCall.type).toBe('CALL')
    expect(skipCall.payload.fn).toBe(jukeboxClient.skip)
    expect(skipCall.payload.args).toEqual([1, 0, scope])

    // Re-check after the await, then call play
    expect(gen.next().value.type).toBe('SELECT')
    const playCall = gen.next(state).value
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
    const state = admittedState({
      jukeboxRemote: {
        bindingGeneration: 2,
        stateSequence: 10,
        deviceName: 'pulse/test',
        currentIndex: 2,
      },
    })
    const action = {
      type: 'PLAYER_PLAY_NEXT',
      data: { s3: { id: 's3' } },
    }
    const gen = handlePlayNext(action)
    expect(gen.next().value.type).toBe('SELECT')
    expect(gen.next(state).value.type).toBe('SELECT')
    const addCall = gen.next(state).value
    expect(addCall.type).toBe('CALL')
    expect(addCall.payload.fn).toBe(jukeboxClient.add)
    expect(addCall.payload.args).toEqual([['s3'], 3, scope])
    expect(gen.next().done).toBe(true)
  })

  it('handleAddTracks calls add with -1 index (append) when in jukeboxMode', async () => {
    const state = admittedState()
    const action = {
      type: 'PLAYER_ADD_TRACKS',
      data: { s4: { id: 's4' } },
    }
    const gen = handleAddTracks(action)
    expect(gen.next().value.type).toBe('SELECT')
    expect(gen.next(state).value.type).toBe('SELECT')
    const addCall = gen.next(state).value
    expect(addCall.type).toBe('CALL')
    expect(addCall.payload.fn).toBe(jukeboxClient.add)
    expect(addCall.payload.args).toEqual([['s4'], -1, scope])
    expect(gen.next().done).toBe(true)
  })

  it('handleSetTrack calls set, skip(0, 0), and play when in jukeboxMode', async () => {
    const state = admittedState()
    const action = {
      type: 'PLAYER_SET_TRACK',
      data: { id: 'single1' },
    }
    const gen = handleSetTrack(action)
    expect(gen.next().value.type).toBe('SELECT')
    expect(gen.next(state).value.type).toBe('SELECT')
    const setCall = gen.next(state).value
    expect(setCall.type).toBe('CALL')
    expect(setCall.payload.fn).toBe(jukeboxClient.set)
    expect(setCall.payload.args).toEqual([['single1'], scope])

    expect(gen.next().value.type).toBe('SELECT')
    const skipCall = gen.next(state).value
    expect(skipCall.type).toBe('CALL')
    expect(skipCall.payload.fn).toBe(jukeboxClient.skip)
    expect(skipCall.payload.args).toEqual([0, 0, scope])

    expect(gen.next().value.type).toBe('SELECT')
    const playCall = gen.next(state).value
    expect(playCall.type).toBe('CALL')
    expect(playCall.payload.fn).toBe(jukeboxClient.play)
    expect(playCall.payload.args).toEqual([scope])

    expect(gen.next().done).toBe(true)
  })

  it('handleClearQueue calls stop and set([]) when in jukeboxMode', async () => {
    const state = admittedState()
    const action = { type: 'PLAYER_CLEAR_QUEUE' }
    const gen = handleClearQueue(action)
    expect(gen.next().value.type).toBe('SELECT')
    expect(gen.next(state).value.type).toBe('SELECT')
    const stopCall = gen.next(state).value
    expect(stopCall.type).toBe('CALL')
    expect(stopCall.payload.fn).toBe(jukeboxClient.stop)
    expect(stopCall.payload.args).toEqual([scope])

    expect(gen.next().value.type).toBe('SELECT')
    const setCall = gen.next(state).value
    expect(setCall.type).toBe('CALL')
    expect(setCall.payload.fn).toBe(jukeboxClient.set)
    expect(setCall.payload.args).toEqual([[], scope])

    expect(gen.next().done).toBe(true)
  })
})

// Persistent regressions for the verified legacy queue-saga authority
// bypasses: without trusted remote ownership the handlers must egress
// nothing, and an in-flight set/stop must abort its remaining continuation
// after a takeover instead of redirecting commands at the latest owner.
describe('legacy queue saga remote-authority gating', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  const sagaEpisodeToken = 'saga-token'
  const sagaIdentity = {
    token: sagaEpisodeToken,
    sessionId: 'jukebox-session:admin',
    clientId: 'tab-a',
  }
  // Reducer-admitted authority fixture: a live enabled episode reconciled to
  // the admitted scene binding (the shared grant's caller-scoped basis).
  const admittedEpisodeFixtures = {
    [sagaEpisodeToken]: {
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
      requestedDeviceName: 'pulse/test',
      revision: 0,
      reconciledBinding: {
        bindingGeneration: 2,
        canonicalDevice: 'pulse/test',
      },
      disabled: false,
    },
  }
  const beginSagaEpisode = (store) =>
    store.dispatch(beginJukeboxEpisode(sagaIdentity))
  // Trusted meta captured at GET start: live token, current revision, and the
  // binding the shared authority had admitted when the GET started.
  const trustedMeta = (store, overrides = {}) => {
    const player = store.getState().player
    return {
      source: 'atomic-session-snapshot',
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
      token: sagaEpisodeToken,
      revision: player.jukeboxEpisodes?.[sagaEpisodeToken]?.revision ?? 0,
      requestBinding: {
        bindingGeneration: player.jukeboxRemote?.bindingGeneration ?? null,
        deviceName: player.jukeboxRemote?.deviceName ?? null,
      },
      ...overrides,
    }
  }

  // Local action.meta provenance for a trusted-shaped action WITHOUT the
  // lifecycle binding: under the approved contract it is rejected whole.
  const atomicMeta = {
    source: 'atomic-session-snapshot',
    sessionId: 'jukebox-session:admin',
    clientId: 'tab-a',
  }
  const trustedAttachedStatus = {
    sessionId: 'jukebox-session:admin',
    ownerClientId: 'tab-a',
    ownershipState: 'attached',
    deviceName: 'pulse/test',
    bindingGeneration: 2,
    stateSequence: 10,
  }
  const takenOverStatus = {
    sessionId: 'jukebox-session:admin',
    ownerClientId: 'tab-b',
    ownershipState: 'taken_over',
    terminationReason: 'taken_over',
  }
  // The same takeover, but the server also rebased the binding generation:
  // the captured continuation must abort on either authority change.
  const rebasedTakenOverStatus = {
    ...takenOverStatus,
    bindingGeneration: 3,
    stateSequence: 0,
  }

  const createLegacyStore = () => {
    const sagaMiddleware = createSagaMiddleware()
    const store = createStore(
      combineReducers({ player: playerReducer }),
      applyMiddleware(sagaMiddleware),
    )
    sagaMiddleware.run(jukeboxQueueSaga)
    return store
  }

  const flush = () => new Promise((resolve) => setTimeout(resolve, 20))

  const allowedState = {
    player: {
      jukeboxMode: true,
      jukeboxControl: {
        sessionId: 'jukebox-session:admin',
        ownerClientId: 'tab-a',
        ownershipState: 'attached',
        controlGranted: true,
      },
      jukeboxRemote: {
        bindingGeneration: 2,
        stateSequence: 10,
        deviceName: 'pulse/test',
      },
      jukeboxEpisodes: admittedEpisodeFixtures,
    },
  }
  const readonlyStateFor = (authorityChange) => ({
    player: {
      jukeboxMode: true,
      jukeboxControl: {
        sessionId: 'jukebox-session:admin',
        ownerClientId: authorityChange.ownerClientId,
        ownershipState: authorityChange.ownershipState,
      },
      jukeboxRemote: {
        bindingGeneration: authorityChange.bindingGeneration ?? 2,
        stateSequence: authorityChange.stateSequence ?? 10,
        deviceName: 'pulse/test',
      },
      jukeboxEpisodes: admittedEpisodeFixtures,
    },
  })

  it('a readonly session with no trusted owner causes zero legacy POSTs from every handler', async () => {
    const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
    const addSpy = vi.spyOn(jukeboxClient, 'add').mockResolvedValue({})
    const stopSpy = vi.spyOn(jukeboxClient, 'stop').mockResolvedValue({})
    const store = createLegacyStore()

    store.dispatch(setJukeboxMode(true, 'pulse/test'))
    // Raw observation only: taken over by another owner, no trusted admission.
    store.dispatch(updateJukeboxSessionStatus(takenOverStatus))
    expect(store.getState().player.jukeboxControl.ownershipState).toBe(
      'taken_over',
    )

    store.dispatch(
      playTracks({ s1: { id: 's1' }, s2: { id: 's2' } }, ['s1', 's2'], 's1'),
    )
    store.dispatch(setTrack({ id: 's3' }))
    store.dispatch(playNext({ s4: { id: 's4' } }))
    store.dispatch(addTracks({ s5: { id: 's5' } }))
    store.dispatch(clearQueue())

    await flush()
    expect(setSpy).not.toHaveBeenCalled()
    expect(skipSpy).not.toHaveBeenCalled()
    expect(playSpy).not.toHaveBeenCalled()
    expect(addSpy).not.toHaveBeenCalled()
    expect(stopSpy).not.toHaveBeenCalled()
  })

  it.each([
    ['a takeover', takenOverStatus],
    ['a takeover that rebased the binding generation', rebasedTakenOverStatus],
  ])(
    'set->await->skip/play aborts after %s with the original captured session and client',
    async (_, authorityChange) => {
      let resolveSet
      const setSpy = vi
        .spyOn(jukeboxClient, 'set')
        .mockImplementation(
          () => new Promise((resolve) => (resolveSet = resolve)),
        )
      const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
      const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
      const store = createLegacyStore()

      store.dispatch(setJukeboxMode(true, 'pulse/test'))
      beginSagaEpisode(store)
      store.dispatch(
        updateJukeboxSessionStatus(trustedAttachedStatus, trustedMeta(store)),
      )

      store.dispatch(
        playTracks({ s1: { id: 's1' }, s2: { id: 's2' } }, ['s1', 's2'], 's2'),
      )
      await waitFor(() => expect(setSpy).toHaveBeenCalledTimes(1))
      expect(setSpy).toHaveBeenCalledWith(
        ['s1', 's2'],
        expect.objectContaining({
          sessionId: 'jukebox-session:admin',
          clientId: 'tab-a',
        }),
      )

      // The authority change lands while set is still in flight.
      store.dispatch(updateJukeboxSessionStatus(authorityChange))
      resolveSet({})
      await flush()

      // The captured continuation must not redirect skip/play at the new owner.
      expect(setSpy).toHaveBeenCalledTimes(1)
      expect(skipSpy).not.toHaveBeenCalled()
      expect(playSpy).not.toHaveBeenCalled()
    },
  )

  const deviceChangedState = {
    player: {
      jukeboxMode: true,
      jukeboxControl: {
        sessionId: 'jukebox-session:admin',
        ownerClientId: 'tab-a',
        ownershipState: 'attached',
        controlGranted: true,
      },
      jukeboxRemote: {
        bindingGeneration: 2,
        stateSequence: 11,
        deviceName: 'pulse/other-sink',
      },
      // The captured episode is still reconciled to the OLD device: the
      // binding no longer matches the admitted scene.
      jukeboxEpisodes: admittedEpisodeFixtures,
    },
  }
  const tokenReplacedState = {
    player: {
      jukeboxMode: true,
      jukeboxControl: {
        sessionId: 'jukebox-session:admin',
        ownerClientId: 'tab-a',
        ownershipState: 'attached',
        controlGranted: true,
      },
      jukeboxRemote: {
        bindingGeneration: 2,
        stateSequence: 11,
        deviceName: 'pulse/test',
      },
      // Same identity and binding, but the captured token was retired and
      // replaced: the old intent must not recapture the replacement token.
      jukeboxEpisodes: {
        'saga-token-2': {
          sessionId: 'jukebox-session:admin',
          clientId: 'tab-a',
          requestedDeviceName: 'pulse/test',
          revision: 0,
          reconciledBinding: {
            bindingGeneration: 2,
            canonicalDevice: 'pulse/test',
          },
          disabled: false,
        },
      },
    },
  }

  it.each([
    ['a takeover', readonlyStateFor(takenOverStatus)],
    [
      'a takeover that rebased the binding generation',
      readonlyStateFor(rebasedTakenOverStatus),
    ],
    ['a reconciled output device change', deviceChangedState],
    ['an episode token replacement', tokenReplacedState],
  ])(
    'stop->set aborts after %s for clearQueue with the original captured session and client',
    async (_, readonlyState) => {
      let resolveStop
      const stopSpy = vi
        .spyOn(jukeboxClient, 'stop')
        .mockImplementation(
          () => new Promise((resolve) => (resolveStop = resolve)),
        )
      const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})

      // Drive the real handler through the saga runtime with a live state
      // snapshot: PLAYER_CLEAR_QUEUE wipes jukeboxMode in the reducer before
      // the saga would run, so the mid-flight abort is exercised here directly.
      let currentState = allowedState
      const task = runSaga(
        { dispatch: () => {}, getState: () => currentState },
        handleClearQueue,
        { type: 'PLAYER_CLEAR_QUEUE' },
      )

      await waitFor(() => expect(stopSpy).toHaveBeenCalledTimes(1))
      expect(stopSpy).toHaveBeenCalledWith({
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      })

      // The authority change lands while stop is still in flight.
      currentState = readonlyState
      resolveStop({})
      await task.toPromise()

      // The captured continuation must not redirect set at the new owner.
      expect(stopSpy).toHaveBeenCalledTimes(1)
      expect(setSpy).not.toHaveBeenCalled()
    },
  )

  it('set->await->skip/play aborts when the same session/client rebinds to a new generation mid-flight', async () => {
    let resolveSet
    const setSpy = vi
      .spyOn(jukeboxClient, 'set')
      .mockImplementation(
        () => new Promise((resolve) => (resolveSet = resolve)),
      )
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
    const store = createLegacyStore()

    store.dispatch(setJukeboxMode(true, 'pulse/test'))
    beginSagaEpisode(store)
    store.dispatch(
      updateJukeboxSessionStatus(trustedAttachedStatus, trustedMeta(store)),
    )
    store.dispatch(
      playTracks({ s1: { id: 's1' }, s2: { id: 's2' } }, ['s1', 's2'], 's2'),
    )
    await waitFor(() => expect(setSpy).toHaveBeenCalledTimes(1))

    // Same session, same client, still attached — but the server rebound the
    // session to a new binding generation while set was in flight.
    store.dispatch(
      updateJukeboxSessionStatus(
        { ...trustedAttachedStatus, bindingGeneration: 3, stateSequence: 0 },
        trustedMeta(store),
      ),
    )
    resolveSet({})
    await flush()

    expect(setSpy).toHaveBeenCalledTimes(1)
    expect(skipSpy).not.toHaveBeenCalled()
    expect(playSpy).not.toHaveBeenCalled()
  })

  it('set->await->skip/play completes through an ordinary same-binding state update', async () => {
    const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
    const store = createLegacyStore()

    store.dispatch(setJukeboxMode(true, 'pulse/test'))
    beginSagaEpisode(store)
    store.dispatch(
      updateJukeboxSessionStatus(trustedAttachedStatus, trustedMeta(store)),
    )
    store.dispatch(
      playTracks({ s1: { id: 's1' }, s2: { id: 's2' } }, ['s1', 's2'], 's2'),
    )
    await waitFor(() => expect(setSpy).toHaveBeenCalledTimes(1))

    // A harmless position observation on the SAME binding keeps the captured
    // continuation eligible: remaining writes must still be issued.
    store.dispatch(
      updateJukeboxSessionStatus({
        ...trustedAttachedStatus,
        position: 13,
      }),
    )
    await flush()

    expect(skipSpy).toHaveBeenCalledWith(
      1,
      0,
      expect.objectContaining({
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      }),
    )
    expect(playSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      }),
    )
  })

  it('set->await->skip/play aborts when the reconciled output device changes mid-flight', async () => {
    let resolveSet
    const setSpy = vi
      .spyOn(jukeboxClient, 'set')
      .mockImplementation(
        () => new Promise((resolve) => (resolveSet = resolve)),
      )
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
    const store = createLegacyStore()

    store.dispatch(setJukeboxMode(true, 'pulse/test'))
    beginSagaEpisode(store)
    store.dispatch(
      updateJukeboxSessionStatus(
        { ...trustedAttachedStatus, deviceName: 'pulse/test' },
        trustedMeta(store),
      ),
    )
    store.dispatch(
      playTracks({ s1: { id: 's1' }, s2: { id: 's2' } }, ['s1', 's2'], 's2'),
    )
    await waitFor(() => expect(setSpy).toHaveBeenCalledTimes(1))

    // The session moves to a different output device at the same binding
    // while set is in flight: the captured device-bound intent must abort.
    store.dispatch(
      updateJukeboxSessionStatus(
        {
          ...trustedAttachedStatus,
          deviceName: 'pulse/other-sink',
          stateSequence: 11,
        },
        trustedMeta(store),
      ),
    )
    resolveSet({})
    await flush()

    expect(setSpy).toHaveBeenCalledTimes(1)
    expect(skipSpy).not.toHaveBeenCalled()
    expect(playSpy).not.toHaveBeenCalled()
  })

  it('stop->set completes through a same-identity replacement state (same binding, advanced sequence)', async () => {
    let resolveStop
    const stopSpy = vi
      .spyOn(jukeboxClient, 'stop')
      .mockImplementation(
        () => new Promise((resolve) => (resolveStop = resolve)),
      )
    const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})

    // The authority is replaced by an equivalent same-identity admission
    // while stop is in flight: same session/client, same binding device,
    // ordinary sequence advance. The captured continuation must complete.
    const replacementState = {
      player: {
        jukeboxMode: true,
        jukeboxControl: {
          sessionId: 'jukebox-session:admin',
          ownerClientId: 'tab-a',
          ownershipState: 'attached',
          controlGranted: true,
        },
        jukeboxRemote: {
          bindingGeneration: 2,
          stateSequence: 11,
          deviceName: 'pulse/test',
        },
        jukeboxEpisodes: admittedEpisodeFixtures,
      },
    }
    let currentState = allowedState
    const task = runSaga(
      { dispatch: () => {}, getState: () => currentState },
      handleClearQueue,
      { type: 'PLAYER_CLEAR_QUEUE' },
    )

    await waitFor(() => expect(stopSpy).toHaveBeenCalledTimes(1))
    expect(stopSpy).toHaveBeenCalledWith({
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
    })

    currentState = replacementState
    resolveStop({})
    await task.toPromise()

    expect(stopSpy).toHaveBeenCalledTimes(1)
    expect(setSpy).toHaveBeenCalledWith([], {
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
    })
  })

  it('set->await->skip/play aborts when a raw observation moves the authority to a new episode mid-flight', async () => {
    let resolveSet
    const setSpy = vi
      .spyOn(jukeboxClient, 'set')
      .mockImplementation(
        () => new Promise((resolve) => (resolveSet = resolve)),
      )
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
    const store = createLegacyStore()

    store.dispatch(setJukeboxMode(true, 'pulse/test'))
    beginSagaEpisode(store)
    store.dispatch(
      updateJukeboxSessionStatus(trustedAttachedStatus, trustedMeta(store)),
    )
    store.dispatch(
      playTracks({ s1: { id: 's1' }, s2: { id: 's2' } }, ['s1', 's2'], 's2'),
    )
    await waitFor(() => expect(setSpy).toHaveBeenCalledTimes(1))

    // The session itself moved (raw observation, no trusted provenance).
    store.dispatch(
      updateJukeboxSessionStatus({
        sessionId: 'jukebox-session:OTHER',
        ownerClientId: 'tab-c',
        ownershipState: 'attached',
        bindingGeneration: 2,
        stateSequence: 10,
      }),
    )
    resolveSet({})
    await flush()

    expect(setSpy).toHaveBeenCalledTimes(1)
    expect(skipSpy).not.toHaveBeenCalled()
    expect(playSpy).not.toHaveBeenCalled()
  })

  it('set->await->skip/play aborts when the same identity replaces its token mid-flight', async () => {
    let resolveSet
    const setSpy = vi
      .spyOn(jukeboxClient, 'set')
      .mockImplementation(
        () => new Promise((resolve) => (resolveSet = resolve)),
      )
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
    const store = createLegacyStore()

    store.dispatch(setJukeboxMode(true, 'pulse/test'))
    beginSagaEpisode(store)
    store.dispatch(
      updateJukeboxSessionStatus(trustedAttachedStatus, trustedMeta(store)),
    )
    store.dispatch(
      playTracks({ s1: { id: 's1' }, s2: { id: 's2' } }, ['s1', 's2'], 's2'),
    )
    await waitFor(() => expect(setSpy).toHaveBeenCalledTimes(1))

    // Same identity, same binding, ordinary advance — but the episode token
    // was retired and replaced: the captured old intent must not recapture
    // the replacement token and continue.
    store.dispatch(endJukeboxEpisode(sagaIdentity))
    store.dispatch(
      beginJukeboxEpisode({ ...sagaIdentity, token: 'saga-token-2' }),
    )
    store.dispatch(
      updateJukeboxSessionStatus(
        { ...trustedAttachedStatus, stateSequence: 11 },
        trustedMeta(store),
      ),
    )
    resolveSet({})
    await flush()

    expect(setSpy).toHaveBeenCalledTimes(1)
    expect(skipSpy).not.toHaveBeenCalled()
    expect(playSpy).not.toHaveBeenCalled()
  })
})
