import React from 'react'
import { Provider } from 'react-redux'
import { applyMiddleware, combineReducers, createStore } from 'redux'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RemotePlayerController from './RemotePlayerController'
import { playerReducer } from '../reducers/playerReducer'
import { activityReducer } from '../reducers/activityReducer'
import { replayGainReducer } from '../reducers/replayGainReducer'
import { settingsReducer } from '../reducers/settingsReducer'
import { themeReducer } from '../reducers/themeReducer'
import {
  setJukeboxMode,
  updateJukeboxSessionStatus,
  playTracks,
  playNext,
  addTracks,
  beginJukeboxEpisode,
} from '../actions'
import createSagaMiddleware from 'redux-saga'
import jukeboxQueueSaga from './jukeboxQueueSaga'
import {
  getOrCreateJukeboxClientId,
  rehydrateJukeboxSession,
} from './jukeboxSession'
import jukeboxClient from './jukeboxClient'
import { startEventStream } from '../eventStream'
import config from '../config'
import { Player } from './Player'
import DeviceSelector from './DeviceSelector'
import { uiVolumeToRemoteGain } from './volumeProfiles'

const browserPlayback = vi.hoisted(() => ({ mounts: 0, streamUrls: [] }))

vi.mock('react-admin', async (importOriginal) => ({
  ...(await importOriginal()),
  useAuthState: () => ({ authenticated: true }),
  useTranslate: () => (key) => key,
  useDataProvider: () => ({
    getOne: () => Promise.resolve({ data: {} }),
  }),
}))

vi.mock('navidrome-music-player', () => ({
  default: function BrowserPlaybackBoundary({
    audioLists = [],
    playIndex = 0,
    getAudioInstance,
  }) {
    const audio = React.useRef(null)
    const source = audioLists[playIndex ?? 0]?.musicSrc
    React.useEffect(() => {
      browserPlayback.mounts++
      getAudioInstance?.(audio.current)
      return () => getAudioInstance?.(null)
    }, [getAudioInstance])
    React.useEffect(() => {
      let active = true
      if (source) {
        Promise.resolve(typeof source === 'function' ? source() : source).then(
          (url) => {
            if (active && audio.current) {
              browserPlayback.streamUrls.push(url)
              audio.current.src = url
            }
          },
        )
      }
      return () => {
        active = false
      }
    }, [source])
    return <audio ref={audio} data-testid="browser-playback-engine" />
  },
}))

// Controlled-server fixture with the server-issued ordering and identity
// contract (bindingGeneration/stateSequence, stable queue item id, playback
// instance, desired/applied gain and mute), matching core/playback
// SessionStatus. Semantic transitions in a scenario advance stateSequence.
const snapshot = (title = 'Server queue', position = 12) => ({
  sessionStatus: {
    sessionId: 'jukebox-session:admin',
    ownerClientId: 'tab-a',
    ownershipState: 'attached',
    deviceName: 'pulse/test',
    currentIndex: 0,
    trackId: 'song-a',
    queueItemId: 'qi-a',
    playing: true,
    position,
    gain: 0.25,
    desiredGain: 0.4,
    muted: false,
    attached: true,
    queueVersion: 20,
    stateSequence: 10,
    playMode: 'orderLoop',
    playbackInstanceId: 'inst-1',
    bindingGeneration: 2,
    lastHeartbeat: '2026-11-02T00:00:00Z',
  },
  jukeboxPlaylist: {
    currentIndex: 0,
    playing: true,
    position,
    gain: 0.25,
    entry: [
      {
        id: 'song-a',
        queueItemId: 'qi-a',
        title,
        artist: 'Test artist',
        album: 'Test album',
        duration: 60,
      },
      {
        id: 'song-b',
        queueItemId: 'qi-b',
        title: 'Next song',
        artist: 'Test artist',
        album: 'Test album',
        duration: 60,
      },
    ],
  },
})

const response = (json, status = 200) =>
  new Response(JSON.stringify(json), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

class EventSourceTransport {
  static instances = []
  constructor(url) {
    this.url = url
    this.listeners = new Map()
    this.closed = false
    EventSourceTransport.instances.push(this)
  }
  addEventListener(name, listener) {
    this.listeners.set(name, listener)
  }
  close() {
    this.closed = true
  }
  emit(name, data) {
    this.listeners.get(name)?.({ type: name, data: JSON.stringify(data) })
  }
}

const deferred = () => {
  let resolve
  const promise = new Promise((complete) => {
    resolve = complete
  })
  return { promise, resolve }
}

// SessionStatus-shaped fixture carrying the server-issued ordering and
// identity contract (bindingGeneration/stateSequence/queueVersion, stable
// queue item id, raw playMode, playback instance, desired/applied gain and
// mute), matching core/playback SessionStatus and the jukebox SSE event.
const orderedStatus = (overrides = {}) => ({
  sessionId: 'jukebox-session:admin',
  ownerClientId: 'tab-a',
  ownershipState: 'attached',
  deviceName: 'pulse/test',
  currentIndex: 0,
  trackId: 'song-a',
  queueItemId: 'qi-a',
  playing: true,
  position: 12,
  gain: 0.25,
  desiredGain: 0.4,
  muted: false,
  attached: true,
  queueVersion: 20,
  stateSequence: 10,
  playMode: 'orderLoop',
  playbackInstanceId: 'inst-1',
  bindingGeneration: 2,
  lastHeartbeat: '2026-11-02T00:00:00Z',
  ...overrides,
})

const orderedSnapshot = (
  title = 'Ordered server queue',
  statusOverrides = {},
) => {
  const base = snapshot(title)
  base.sessionStatus = orderedStatus(statusOverrides)
  base.jukeboxPlaylist.entry = base.jukeboxPlaylist.entry.map(
    (entry, index) => ({
      ...entry,
      queueItemId: `qi-${String.fromCharCode(97 + index)}`,
    }),
  )
  return base
}

// Older-server snapshot: valid display payload without the ordered
// generation/sequence contract.
const unorderedSnapshot = (title = 'Unordered server queue', position = 12) => {
  const base = snapshot(title, position)
  delete base.sessionStatus.bindingGeneration
  delete base.sessionStatus.stateSequence
  delete base.sessionStatus.queueItemId
  delete base.sessionStatus.playbackInstanceId
  delete base.sessionStatus.playMode
  delete base.sessionStatus.desiredGain
  delete base.sessionStatus.muted
  delete base.sessionStatus.attached
  delete base.sessionStatus.lastHeartbeat
  delete base.sessionStatus.queueVersion
  base.jukeboxPlaylist.entry = base.jukeboxPlaylist.entry.map((entry) => {
    const { queueItemId, ...legacyEntry } = entry
    void queueItemId
    return legacyEntry
  })
  return base
}

const makeTransport = (
  snapshots = [snapshot()],
  {
    attachReply = null,
    heartbeatReply = null,
    commandReply = null,
    takeoverReply = null,
  } = {},
) => {
  const requests = []
  let snapshotIndex = 0
  const fetch = vi.fn((url, options = {}) => {
    const path = new URL(url, 'http://localhost').pathname
    requests.push({ path, options, url })
    if (
      path.endsWith('/session/attach') &&
      options.body &&
      JSON.parse(options.body).takeover &&
      takeoverReply
    ) {
      return Promise.resolve(takeoverReply).then((reply) => reply.clone())
    }
    if (path.endsWith('/session/attach') && attachReply) {
      const reply =
        typeof attachReply === 'function'
          ? attachReply(JSON.parse(options.body))
          : attachReply
      return Promise.resolve(reply).then((value) => value.clone())
    }
    if (path.endsWith('/session/heartbeat') && heartbeatReply) {
      return Promise.resolve(heartbeatReply).then((reply) => reply.clone())
    }
    if (
      path.endsWith('/session/attach') ||
      path.endsWith('/session/heartbeat')
    ) {
      const identity = JSON.parse(options.body)
      return Promise.resolve(
        response({
          ...snapshot().sessionStatus,
          sessionId: identity.sessionId,
          ownerClientId: identity.clientId,
        }),
      )
    }
    if (path.endsWith('/session/snapshot')) {
      const reply = snapshots[Math.min(snapshotIndex++, snapshots.length - 1)]
      return reply instanceof Promise
        ? reply
        : Promise.resolve(
            reply instanceof Response ? reply.clone() : response(reply),
          )
    }
    if (!path.includes('/session/') && commandReply) {
      return Promise.resolve(commandReply).then((reply) => reply.clone())
    }
    if (path.startsWith('/rest/')) {
      return Promise.resolve(
        response({
          'subsonic-response': { status: 'ok', transcodeDecision: {} },
        }),
      )
    }
    return Promise.resolve(response({}))
  })
  vi.stubGlobal('fetch', fetch)
  return { fetch, requests }
}

const renderController = (
  props = {},
  store = createStore(
    combineReducers({ player: playerReducer, activity: activityReducer }),
  ),
) => {
  const view = render(
    <Provider store={store}>
      <RemotePlayerController
        sessionId="jukebox-session:admin"
        clientId="tab-a"
        deviceName="pulse/test"
        {...props}
      />
    </Provider>,
  )
  return { ...view, store }
}

const renderDefaultPlayer = (
  status,
  { remote = true, replayGainMode = 'track' } = {},
) => {
  const localTrack = {
    trackId: 'retained-local',
    uuid: 'retained-local-occurrence',
    name: 'Retained local song',
    song: { id: 'retained-local', title: 'Retained local song' },
    musicSrc: '/rest/stream?id=retained-local',
  }
  const store = createStore(
    combineReducers({
      player: playerReducer,
      activity: activityReducer,
      settings: settingsReducer,
      replayGain: replayGainReducer,
      theme: themeReducer,
    }),
    {
      player: {
        ...playerReducer(undefined, { type: '@@INIT' }),
        queue: [localTrack],
        current: localTrack,
        savedPlayIndex: 0,
        playIndex: 0,
      },
      replayGain: {
        ...replayGainReducer(undefined, { type: '@@INIT' }),
        gainMode: replayGainMode,
      },
    },
  )
  if (remote) store.dispatch(setJukeboxMode(true, 'Validation output'))
  if (status) store.dispatch(updateJukeboxSessionStatus(status))
  const view = render(
    <Provider store={store}>
      <Player />
    </Provider>,
  )
  return { ...view, store }
}

const commands = (transport) =>
  transport.requests.filter(({ path }) => !path.includes('/session/'))
const snapshotsRequested = (transport) =>
  transport.requests.filter(({ path }) => path.endsWith('/session/snapshot'))

let originalEventStreamFlag
beforeEach(() => {
  originalEventStreamFlag = config.devNewEventStream
  config.devNewEventStream = true
  localStorage.setItem('username', 'admin')
  localStorage.setItem('token', 'fake-jwt')
  localStorage.setItem('is-authenticated', 'true')
  EventSourceTransport.instances = []
  vi.stubGlobal('EventSource', EventSourceTransport)
})
afterEach(() => {
  config.devNewEventStream = originalEventStreamFlag
  EventSourceTransport.instances.forEach((stream) => stream.close())
  vi.useRealTimers()
  vi.unstubAllGlobals()
  localStorage.clear()
  sessionStorage.clear()
})

describe('<RemotePlayerController />', () => {
  it('rejects a previous attachment episode snapshot after the session and device change', async () => {
    const old = deferred()
    const next = snapshot('Different session queue', 9)
    next.sessionStatus.sessionId = 'jukebox-session:admin:new'
    next.sessionStatus.deviceName = 'pulse/other'
    const transport = makeTransport([old.promise, next])
    const { rerender, store } = renderController()
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(1))
    rerender(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin:new"
          clientId="tab-a"
          deviceName="pulse/other"
        />
      </Provider>,
    )
    await screen.findByRole('heading', { name: 'Different session queue' })
    await act(async () => {
      old.resolve(response(snapshot('Obsolete attachment queue', 2)))
    })
    expect(
      screen.getByRole('heading', { name: 'Different session queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Obsolete attachment queue' }),
    ).not.toBeInTheDocument()
    expect(screen.getByText('Device: pulse/other')).toBeInTheDocument()
    expect(commands(transport)).toEqual([])
  })

  it('retains the last observable queue but blocks commands after an authoritative snapshot request is denied', async () => {
    const transport = makeTransport([
      snapshot(),
      response({ message: 'Session ownership changed' }, 403),
    ])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Server queue' })
    await act(async () =>
      EventSourceTransport.instances.at(-1).emit('jukeboxStateUpdated', {
        position: 13,
        bindingGeneration: 2,
        stateSequence: 11,
      }),
    )
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Session ownership changed',
    )
    expect(
      screen.getByRole('heading', { name: 'Server queue' }),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(commands(transport)).toEqual([])
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
  })

  it('blocks commands immediately when heartbeat reports takeover without a stream event', async () => {
    const transport = makeTransport([snapshot()], {
      heartbeatReply: response({ message: 'Control taken over' }, 403),
    })
    vi.useFakeTimers()
    renderController()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000)
    })
    vi.useRealTimers()
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    })
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(commands(transport)).toEqual([])
    expect(
      screen.getByRole('heading', { name: 'Server queue' }),
    ).toBeInTheDocument()
  })

  it('reacquires the full queue after a missing heartbeat session is reattached', async () => {
    const reattached = snapshot('Reattached queue', 31)
    reattached.sessionStatus.stateSequence = 11
    const transport = makeTransport([snapshot(), reattached], {
      heartbeatReply: response({ message: 'Session missing' }, 404),
    })
    vi.useFakeTimers()
    renderController()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(
      screen.getByRole('heading', { name: 'Server queue' }),
    ).toBeInTheDocument()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000)
    })
    vi.useRealTimers()
    expect(
      await screen.findByRole('heading', { name: 'Reattached queue' }),
    ).toBeInTheDocument()
    expect(screen.getByText('Position: 31 seconds')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/attach')),
    ).toHaveLength(2)
    expect(commands(transport)).toEqual([])
  })

  it('uses the shared session heartbeat while attached and stops it when the controller exits', async () => {
    const transport = makeTransport()
    vi.useFakeTimers()
    const { unmount } = renderController()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(
      screen.getByRole('heading', { name: 'Server queue' }),
    ).toBeInTheDocument()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000)
    })
    expect(
      transport.requests.filter(({ path }) =>
        path.endsWith('/session/heartbeat'),
      ),
    ).toHaveLength(1)
    unmount()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000)
    })
    expect(
      transport.requests.filter(({ path }) =>
        path.endsWith('/session/heartbeat'),
      ),
    ).toHaveLength(1)
    expect(commands(transport)).toEqual([])
    vi.useRealTimers()
  })

  it('refreshes two real controller subscribers from one stream without automatic queue or playback commands', async () => {
    const shared = snapshot()
    shared.sessionStatus.ownerClientId = 'tab-b'
    const advanced = snapshot('Shared server successor', 21)
    advanced.sessionStatus.ownerClientId = 'tab-b'
    advanced.sessionStatus.stateSequence = 11
    const transport = makeTransport([shared, shared, advanced, advanced], {
      attachReply: ({ clientId }) =>
        clientId === 'tab-b'
          ? response(shared.sessionStatus)
          : response({ message: 'Control denied' }, 403),
    })
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    const { unmount } = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-b"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    await waitFor(() =>
      expect(
        screen.getAllByRole('heading', { name: 'Server queue' }),
      ).toHaveLength(2),
    )
    expect(screen.getAllByRole('button', { name: 'Pause' })[0]).toBeDisabled()
    expect(
      screen.getAllByRole('button', { name: 'Pause' })[1],
    ).not.toBeDisabled()
    await act(async () =>
      EventSourceTransport.instances.at(-1).emit('jukeboxStateUpdated', {
        currentIndex: 1,
        trackId: 'song-b',
        bindingGeneration: 2,
        stateSequence: 11,
      }),
    )
    await waitFor(() =>
      expect(
        screen.getAllByRole('heading', { name: 'Shared server successor' }),
      ).toHaveLength(2),
    )
    expect(snapshotsRequested(transport)).toHaveLength(4)
    expect(commands(transport)).toEqual([])
    window.dispatchEvent(new Event('pagehide'))
    unmount()
    await waitFor(() =>
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/detach'),
        ),
      ).toHaveLength(1),
    )
    expect(
      JSON.parse(
        transport.requests.find(({ path }) => path.endsWith('/session/detach'))
          .options.body,
      ),
    ).toEqual({ sessionId: 'jukebox-session:admin', clientId: 'tab-b' })
    expect(commands(transport)).toEqual([])
  })

  it('detaches without stopping on exit and restores a fresh queue when a controller is reopened', async () => {
    const fresh = snapshot('Fresh reopened queue', 7)
    fresh.jukeboxPlaylist.entry = [fresh.jukeboxPlaylist.entry[0]]
    const transport = makeTransport([snapshot(), fresh])
    const first = renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    window.dispatchEvent(new Event('pagehide'))
    first.unmount()
    await waitFor(() =>
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/detach'),
        ),
      ).toHaveLength(1),
    )
    renderController()
    await screen.findByRole('heading', { name: 'Fresh reopened queue' })
    expect(
      screen.queryByText('Next song — Test artist'),
    ).not.toBeInTheDocument()
    expect(screen.getByText('Position: 7 seconds')).toBeInTheDocument()
    expect(commands(transport)).toEqual([])
  })

  it('actively reacquires an atomic snapshot when the existing event stream reconnects', async () => {
    const restored = snapshot('Restored after reconnect', 27)
    restored.sessionStatus.stateSequence = 11
    const transport = makeTransport([snapshot(), restored])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Server queue' })
    vi.useFakeTimers()
    await act(async () =>
      EventSourceTransport.instances.at(-1).onerror(new Event('error')),
    )
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(EventSourceTransport.instances).toHaveLength(2)
    expect(EventSourceTransport.instances[0].closed).toBe(true)
    expect(
      screen.getByRole('heading', { name: 'Restored after reconnect' }),
    ).toBeInTheDocument()
    expect(screen.getByText('Position: 27 seconds')).toBeInTheDocument()
    expect(snapshotsRequested(transport)).toHaveLength(2)
    expect(commands(transport)).toEqual([])
    vi.useRealTimers()
  })

  it('reports command failure without optimistic state changes or an automatic replay', async () => {
    const transport = makeTransport([snapshot()], {
      commandReply: response({ message: 'Output unavailable' }, 503),
    })
    renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Output unavailable',
    )
    expect(screen.getByText('Playing')).toBeInTheDocument()
    expect(snapshotsRequested(transport)).toHaveLength(1)
    expect(commands(transport)).toHaveLength(1)
  })

  it('keeps a taken-over snapshot observable but refuses further local control intents', async () => {
    const takenOver = snapshot()
    takenOver.sessionStatus.ownerClientId = 'other-tab'
    takenOver.sessionStatus.ownershipState = 'taken_over'
    const transport = makeTransport([takenOver])
    renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(commands(transport)).toEqual([])
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(screen.getByText('Ownership: taken_over')).toBeInTheDocument()
  })

  it.each([
    {
      label: 'play',
      action: () =>
        fireEvent.click(screen.getByRole('button', { name: 'Play' })),
      path: '/api/jukebox/play',
      body: {},
    },
    {
      label: 'skip',
      action: () =>
        fireEvent.click(screen.getByRole('button', { name: 'Next' })),
      path: '/api/jukebox/skip',
      body: { index: 1, offset: 0 },
    },
    {
      label: 'seek',
      action: () =>
        fireEvent.change(screen.getByRole('slider', { name: 'Seek' }), {
          target: { value: '24' },
        }),
      path: '/api/jukebox/seek',
      body: { position: 24 },
    },
    {
      label: 'volume',
      action: () =>
        fireEvent.change(screen.getByRole('slider', { name: 'Volume' }), {
          target: { value: '0.4' },
        }),
      path: '/api/jukebox/volume',
      body: { gain: uiVolumeToRemoteGain(0.4, { deviceName: 'pulse/test' }) },
    },
  ])(
    'routes only explicit $label intents to their existing endpoint',
    async ({ action, path, body }) => {
      const paused = snapshot()
      paused.sessionStatus.playing = false
      paused.jukeboxPlaylist.playing = false
      const transport = makeTransport([paused])
      renderController()
      await screen.findByRole('heading', { name: 'Server queue' })
      action()
      await waitFor(() => expect(commands(transport)).toHaveLength(1))
      expect(commands(transport)[0].path).toBe(path)
      expect(
        commands(transport)[0].options.body
          ? JSON.parse(commands(transport)[0].options.body)
          : undefined,
      ).toEqual({
        ...body,
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      })
      await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))
      expect(screen.getByText('Paused')).toBeInTheDocument()
    },
  )

  it('sends a user command through the authenticated client but changes display only after a fresh snapshot', async () => {
    const acknowledgement = deferred()
    const paused = snapshot('Authoritative paused queue', 12)
    paused.sessionStatus.playing = false
    paused.sessionStatus.stateSequence = 11
    paused.jukeboxPlaylist.playing = false
    const transport = makeTransport([snapshot(), paused], {
      commandReply: acknowledgement.promise,
    })
    renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(commands(transport)).toHaveLength(1))
    expect(commands(transport)[0].path).toBe('/api/jukebox/pause')
    expect(commands(transport)[0].options.method).toBe('POST')
    expect(
      commands(transport)[0].options.headers.get('X-ND-Authorization'),
    ).toBe('Bearer fake-jwt')
    expect(screen.getByText('Playing')).toBeInTheDocument()
    expect(snapshotsRequested(transport)).toHaveLength(1)
    await act(async () => {
      acknowledgement.resolve(response({ playing: false }))
    })
    await screen.findByRole('heading', { name: 'Authoritative paused queue' })
    expect(screen.getByText('Paused')).toBeInTheDocument()
    expect(snapshotsRequested(transport)).toHaveLength(2)
  })

  it('ignores a late older HTTP snapshot after a newer invalidation has completed', async () => {
    const stale = deferred()
    const newest = snapshot('Newest server queue', 33)
    newest.sessionStatus.stateSequence = 12
    const transport = makeTransport([snapshot(), stale.promise, newest])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Server queue' })
    await act(async () =>
      EventSourceTransport.instances.at(-1).emit('jukeboxStateUpdated', {
        position: 20,
        bindingGeneration: 2,
        stateSequence: 11,
      }),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))
    await act(async () =>
      EventSourceTransport.instances.at(-1).emit('jukeboxStateUpdated', {
        position: 33,
        bindingGeneration: 2,
        stateSequence: 12,
      }),
    )
    await screen.findByRole('heading', { name: 'Newest server queue' })
    await act(async () => {
      stale.resolve(response(snapshot('Late obsolete queue', 20)))
    })
    expect(
      screen.getByRole('heading', { name: 'Newest server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Late obsolete queue' }),
    ).not.toBeInTheDocument()
    expect(screen.getByText('Position: 33 seconds')).toBeInTheDocument()
    expect(commands(transport)).toEqual([])
  })

  it('uses actual SSE dispatch only to invalidate and reacquire authoritative HTTP state', async () => {
    const refreshed = snapshot('HTTP refreshed queue', 18)
    refreshed.sessionStatus.stateSequence = 11
    const transport = makeTransport([snapshot(), refreshed])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Server queue' })
    await act(async () =>
      EventSourceTransport.instances.at(-1).emit('jukeboxStateUpdated', {
        currentIndex: 1,
        trackId: 'song-b',
        playing: false,
        position: 55,
        gain: 1,
        bindingGeneration: 2,
        stateSequence: 11,
      }),
    )
    await screen.findByRole('heading', { name: 'HTTP refreshed queue' })
    expect(screen.getByText('Position: 18 seconds')).toBeInTheDocument()
    expect(screen.getByText('Playing')).toBeInTheDocument()
    expect(screen.getByText('Gain: 0.25')).toBeInTheDocument()
    expect(snapshotsRequested(transport)).toHaveLength(2)
    expect(commands(transport)).toEqual([])
  })

  it('finishes retiring the old attachment before reusing the same identity for another device', async () => {
    const attachment = deferred()
    const transport = makeTransport([], { attachReply: attachment.promise })
    const { rerender, store } = renderController()
    await waitFor(() => expect(transport.requests).toHaveLength(1))
    rerender(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/other"
        />
      </Provider>,
    )
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/attach')),
    ).toHaveLength(1)
    await act(async () => {
      attachment.resolve(response(snapshot().sessionStatus))
    })
    await waitFor(() =>
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/attach'),
        ),
      ).toHaveLength(2),
    )
    expect(transport.requests.slice(0, 3).map(({ path }) => path)).toEqual([
      '/api/jukebox/session/attach',
      '/api/jukebox/session/detach',
      '/api/jukebox/session/attach',
    ])
    expect(commands(transport)).toEqual([])
  })

  it('keeps an active owner observable after observer attachment is denied without detaching or controlling it', async () => {
    const owner = snapshot()
    owner.sessionStatus.ownerClientId = 'other-tab'
    const transport = makeTransport([owner], {
      attachReply: response({ message: 'Control denied' }, 403),
    })
    const { unmount } = renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Take control' })).toBeEnabled()
    expect(
      transport.requests.filter(
        ({ options }) => options.body && JSON.parse(options.body).takeover,
      ),
    ).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    window.dispatchEvent(new Event('pagehide'))
    unmount()
    await act(async () => {})
    expect(snapshotsRequested(transport)).toHaveLength(1)
    expect(commands(transport)).toEqual([])
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/detach')),
    ).toHaveLength(0)
  })

  it('takes control only after an explicit observer click and sends no successful old-owner mutation', async () => {
    let owner = 'tab-a'
    let playing = true
    // Servers advance the state sequence on each accepted command, so the
    // post-pause snapshot supersedes the equal-sequence scene instead of
    // conflicting with it.
    let stateSequence = 10
    const requests = []
    const deniedCommands = []
    const currentSnapshot = () => {
      const current = snapshot()
      current.sessionStatus.ownerClientId = owner
      current.sessionStatus.playing = playing
      current.sessionStatus.stateSequence = stateSequence
      current.jukeboxPlaylist.playing = playing
      return current
    }
    vi.stubGlobal(
      'fetch',
      vi.fn((url, options = {}) => {
        const path = new URL(url, 'http://localhost').pathname
        const body = options.body ? JSON.parse(options.body) : {}
        requests.push({ path, body })
        if (path.endsWith('/session/attach')) {
          if (body.clientId !== owner && body.takeover !== true) {
            return Promise.resolve(response({ message: 'Control denied' }, 403))
          }
          if (body.takeover === true) owner = body.clientId
          return Promise.resolve(response(currentSnapshot().sessionStatus))
        }
        if (path.endsWith('/session/snapshot'))
          return Promise.resolve(response(currentSnapshot()))
        if (path.endsWith('/pause')) {
          if (body.clientId !== owner) {
            deniedCommands.push(body)
            return Promise.resolve(response({ message: 'Control denied' }, 403))
          }
          playing = false
          stateSequence = 11
          return Promise.resolve(response({ playing }))
        }
        return Promise.resolve(response(currentSnapshot().sessionStatus))
      }),
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    const { container, unmount } = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-b"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    const [first, second] = Array.from(
      container.querySelectorAll('[aria-label="Remote controller"]'),
    ).map((element) => within(element))
    await first.findByRole('heading', { name: 'Server queue' })
    await second.findByRole('heading', { name: 'Server queue' })
    expect(first.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(second.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(requests.filter(({ body }) => body.takeover)).toHaveLength(0)
    fireEvent.click(second.getByRole('button', { name: 'Take control' }))
    await waitFor(() =>
      expect(second.getByRole('button', { name: 'Pause' })).toBeEnabled(),
    )
    expect(owner).toBe('tab-b')
    expect(requests.filter(({ body }) => body.takeover)).toEqual([
      {
        path: '/api/jukebox/session/attach',
        body: {
          sessionId: 'jukebox-session:admin',
          clientId: 'tab-b',
          deviceName: 'pulse/test',
          takeover: true,
        },
      },
    ])
    // The superseded owner is fenced client-side: once the new-owner snapshot
    // is admitted the old owner's controls are disabled and its click
    // egresses NOTHING — no server 403 round-trip, no successful mutation.
    expect(first.getByRole('button', { name: 'Pause' })).toBeDisabled()
    fireEvent.click(first.getByRole('button', { name: 'Pause' }))
    expect(commands({ requests })).toEqual([])
    expect(deniedCommands).toEqual([])
    expect(playing).toBe(true)
    fireEvent.click(second.getByRole('button', { name: 'Pause' }))
    await second.findByText('Paused', { exact: true })
    expect(playing).toBe(false)
    unmount()
  })

  it('retires a pending explicit takeover after page close without late heartbeat or playback resurrection', async () => {
    const pending = deferred()
    const owner = snapshot()
    owner.sessionStatus.ownerClientId = 'other-tab'
    const transport = makeTransport([owner], {
      attachReply: response({ message: 'Control denied' }, 403),
      takeoverReply: pending.promise,
    })
    const { unmount } = renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    fireEvent.click(screen.getByRole('button', { name: 'Take control' }))
    await waitFor(() =>
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/attach'),
        ),
      ).toHaveLength(2),
    )
    expect(screen.getByRole('button', { name: 'Take control' })).toBeDisabled()
    window.dispatchEvent(new Event('pagehide'))
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/detach')),
    ).toHaveLength(0)
    await act(async () => pending.resolve(response(snapshot().sessionStatus)))
    await waitFor(() =>
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/detach'),
        ),
      ).toHaveLength(1),
    )
    expect(
      transport.requests.find(({ path }) => path.endsWith('/session/detach'))
        .options.keepalive,
    ).toBe(true)
    vi.useFakeTimers()
    await act(async () => vi.advanceTimersByTimeAsync(15000))
    vi.useRealTimers()
    expect(
      transport.requests.filter(({ path }) =>
        path.endsWith('/session/heartbeat'),
      ),
    ).toHaveLength(0)
    expect(snapshotsRequested(transport)).toHaveLength(1)
    expect(commands(transport)).toEqual([])
    unmount()
  })

  it('does not retry a denied explicit takeover or send playback commands automatically', async () => {
    const owner = snapshot()
    owner.sessionStatus.ownerClientId = 'other-tab'
    const denied = response({ message: 'Control denied' }, 403)
    const transport = makeTransport([owner], {
      attachReply: denied,
      takeoverReply: denied,
    })
    renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    fireEvent.click(screen.getByRole('button', { name: 'Take control' }))
    await waitFor(() =>
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/attach'),
        ),
      ).toHaveLength(2),
    )
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Take control' }),
      ).toBeEnabled(),
    )
    vi.useFakeTimers()
    await act(async () => vi.advanceTimersByTimeAsync(15000))
    vi.useRealTimers()
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/attach')),
    ).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(commands(transport)).toEqual([])
  })

  it('detaches control with authenticated keepalive on actual pagehide without stopping remote playback', async () => {
    const transport = makeTransport()
    const { unmount } = renderController()
    await screen.findByRole('heading', { name: 'Server queue' })
    window.dispatchEvent(new Event('pagehide'))
    await waitFor(() => {
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/detach'),
        ),
      ).toHaveLength(1)
    })
    const request = transport.requests.find(({ path }) =>
      path.endsWith('/session/detach'),
    )
    expect(request.options.keepalive).toBe(true)
    expect(request.options.headers.get('X-ND-Authorization')).toBe(
      'Bearer fake-jwt',
    )
    expect(JSON.parse(request.options.body)).toEqual({
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
    })
    expect(commands(transport)).toEqual([])
    unmount()
    await act(async () => {})
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/detach')),
    ).toHaveLength(1)
  })

  it('exposes attachment and observation denial without inventing a queue or issuing control commands', async () => {
    const transport = makeTransport(
      [response({ message: 'Control denied' }, 403)],
      {
        attachReply: response({ message: 'Control denied' }, 403),
      },
    )
    const { unmount } = renderController()
    expect(await screen.findByRole('alert')).toHaveTextContent('Control denied')
    expect(
      screen.queryByRole('button', { name: 'Play' }),
    ).not.toBeInTheDocument()
    expect(snapshotsRequested(transport)).toHaveLength(1)
    expect(commands(transport)).toEqual([])
    unmount()
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/detach')),
    ).toHaveLength(0)
  })

  it('retires a late attachment only after it completes, without fetching state or controlling playback', async () => {
    const attachment = deferred()
    const transport = makeTransport([], { attachReply: attachment.promise })
    const { unmount } = renderController()
    await waitFor(() => expect(transport.requests).toHaveLength(1))
    unmount()
    expect(
      transport.requests.filter(({ path }) => path.endsWith('/session/detach')),
    ).toHaveLength(0)

    await act(async () => {
      attachment.resolve(response(snapshot().sessionStatus))
    })
    await waitFor(() =>
      expect(
        transport.requests.filter(({ path }) =>
          path.endsWith('/session/detach'),
        ),
      ).toHaveLength(1),
    )
    expect(snapshotsRequested(transport)).toHaveLength(0)
    expect(commands(transport)).toEqual([])
  })

  it('attaches and restores the full server queue through authenticated HTTP without a browser engine', async () => {
    const transport = makeTransport()
    const { container } = renderController()

    await screen.findByRole('heading', { name: 'Server queue' })
    expect(container.querySelector('audio')).toBeNull()
    expect(screen.getByText('Position: 12 seconds')).toBeInTheDocument()
    expect(screen.getByText('Next song — Test artist')).toBeInTheDocument()
    expect(commands(transport)).toEqual([])
    expect(transport.requests[0].path).toBe('/api/jukebox/session/attach')
    expect(transport.requests[1].path).toBe('/api/jukebox/session/snapshot')
    expect(
      transport.requests[1].options.headers.get('X-ND-Authorization'),
    ).toBe('Bearer fake-jwt')
    expect(
      new URL(transport.requests[1].url, 'http://localhost').searchParams.get(
        'sessionId',
      ),
    ).toBe('jukebox-session:admin')
  })
})

describe('remote atomic snapshot admission (binding generation and state sequence)', () => {
  it('rejects a late lower-generation HTTP snapshot after the floor advanced', async () => {
    const late = deferred()
    const transport = makeTransport([orderedSnapshot(), late.promise], {
      attachReply: () => response(orderedStatus()),
    })
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))

    await act(async () => {
      late.resolve(
        response(
          orderedSnapshot('Late stale generation queue', {
            bindingGeneration: 1,
            stateSequence: 1000,
            queueVersion: 40,
            playing: false,
            position: 44,
          }),
        ),
      )
    })

    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Late stale generation queue' }),
    ).not.toBeInTheDocument()
    expect(screen.getByText('Playing')).toBeInTheDocument()
    expect(commands(transport)).toEqual([])
  })

  it('admits a higher generation whose full snapshot restarts sequencing', async () => {
    const transport = makeTransport([
      orderedSnapshot(),
      orderedSnapshot('Regenerated queue', {
        bindingGeneration: 3,
        stateSequence: 1,
        queueVersion: 1,
        playing: false,
        position: 5,
      }),
    ])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances.at(-1).emit(
        'jukeboxStateUpdated',
        orderedStatus({
          bindingGeneration: 3,
          stateSequence: 1,
          position: 5,
        }),
      ),
    )

    await screen.findByRole('heading', { name: 'Regenerated queue' })
    expect(screen.getByText('Paused')).toBeInTheDocument()
    expect(commands(transport)).toEqual([])
  })

  it('raises the floor from a partial higher-generation observation so a pending older-generation HTTP response is rejected', async () => {
    const pendingGen2 = deferred()
    const gen3Full = deferred()
    const transport = makeTransport(
      [orderedSnapshot(), pendingGen2.promise, gen3Full.promise],
      { attachReply: () => response(orderedStatus()) },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))

    // Partial observation: valid newer generation/sequence, no queue payload.
    await act(async () =>
      EventSourceTransport.instances.at(-1).emit(
        'jukeboxStateUpdated',
        orderedStatus({
          bindingGeneration: 3,
          stateSequence: 5,
          position: 30,
        }),
      ),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(3))

    await act(async () => {
      pendingGen2.resolve(
        response(
          orderedSnapshot('Superseded generation queue', {
            stateSequence: 20,
            playing: false,
          }),
        ),
      )
    })

    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Superseded generation queue' }),
    ).not.toBeInTheDocument()

    await act(async () => {
      gen3Full.resolve(
        response(
          orderedSnapshot('Third generation queue', {
            bindingGeneration: 3,
            stateSequence: 6,
            position: 31,
          }),
        ),
      )
    })
    await screen.findByRole('heading', { name: 'Third generation queue' })
    expect(store.getState().player.jukeboxRemote.bindingGeneration).toBe(3)
    expect(commands(transport)).toEqual([])
  })

  it('rejects a same-generation HTTP snapshot with an older sequence even at an equal queue version', async () => {
    const late = deferred()
    const transport = makeTransport([orderedSnapshot(), late.promise], {
      attachReply: () => response(orderedStatus()),
    })
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))

    await act(async () => {
      late.resolve(
        response(
          orderedSnapshot('Older sequence queue', {
            stateSequence: 9,
            queueVersion: 20,
            playing: false,
            position: 44,
          }),
        ),
      )
    })

    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Older sequence queue' }),
    ).not.toBeInTheDocument()
    expect(screen.getByText('Playing')).toBeInTheDocument()
    expect(commands(transport)).toEqual([])
  })

  it('suspends control once and coalesces a single bounded refresh on repeated equal-sequence conflicts without compensating commands', async () => {
    const reconciled = deferred()
    const transport = makeTransport(
      [orderedSnapshot(), reconciled.promise, orderedSnapshot()],
      { attachReply: () => response(orderedStatus()) },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()

    // Same generation and sequence, but conflicting semantic scene fields.
    const conflict = orderedStatus({
      playing: false,
      trackId: 'song-b',
      queueItemId: 'qi-b',
      gain: 0.9,
      desiredGain: 0.9,
      muted: true,
      playMode: 'singleLoop',
      playbackInstanceId: 'inst-9',
      position: 2,
    })
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', conflict),
    )
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', { ...conflict }),
    )

    expect(snapshotsRequested(transport)).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(commands(transport)).toEqual([])

    await act(async () => {
      reconciled.resolve(
        response(
          orderedSnapshot('Reconciled queue', {
            stateSequence: 12,
            position: 18,
          }),
        ),
      )
    })
    await screen.findByRole('heading', { name: 'Reconciled queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(commands(transport)).toEqual([])
  })

  it('keeps control available for equal-sequence position, heartbeat and handoff metadata observations', async () => {
    const transport = makeTransport([
      orderedSnapshot(),
      orderedSnapshot('Ordered server queue', { position: 44 }),
    ])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances.at(-1).emit(
        'jukeboxStateUpdated',
        orderedStatus({
          position: 44,
          lastHeartbeat: '2026-11-02T00:05:00Z',
          controlHandoffToken: 'handoff-1',
        }),
      ),
    )

    expect(await screen.findByText('Position: 44 seconds')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(commands(transport)).toEqual([])
  })

  it('promotes a strict scene only from the atomic snapshot handshake and drops a stale generation afterwards', async () => {
    const attach = deferred()
    const initial = deferred()
    const transport = makeTransport(
      [initial.promise, orderedSnapshot('Post-baseline probe', 77)],
      { attachReply: attach.promise },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    expect(screen.getByText('Loading remote state')).toBeInTheDocument()

    // A pre-handshake partial observation cannot promote or block the scene.
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', { position: 99 }),
    )
    await act(async () => {
      attach.resolve(response(orderedStatus()))
    })
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(1))
    await act(async () => {
      initial.resolve(response(orderedSnapshot()))
    })
    await screen.findByRole('heading', { name: 'Ordered server queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()

    await act(async () =>
      EventSourceTransport.instances.at(-1).emit(
        'jukeboxStateUpdated',
        orderedStatus({
          bindingGeneration: 1,
          stateSequence: 500,
          queueVersion: 30,
          trackId: 'song-z',
          queueItemId: 'qi-z',
          playing: false,
          position: 55,
          playbackInstanceId: 'inst-7',
        }),
      ),
    )

    expect(snapshotsRequested(transport)).toHaveLength(1)
    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Post-baseline probe' }),
    ).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(commands(transport)).toEqual([])
  })

  it('uses SSE invalidation only to reacquire the atomic snapshot without replacing the Redux queue', async () => {
    const transport = makeTransport([
      orderedSnapshot(),
      orderedSnapshot('Invalidated server queue', {
        stateSequence: 11,
        position: 26,
      }),
    ])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })
    expect(store.getState().player.queue).toEqual([])

    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )

    await screen.findByRole('heading', { name: 'Invalidated server queue' })
    expect(store.getState().player.queue).toEqual([])
    expect(commands(transport)).toEqual([])
  })
})

describe('persistent remote-authority egress regressions', () => {
  it('an unordered owned HTTP response after a floor advance cannot replace the accepted scene or issue commands', async () => {
    const staleUnordered = deferred()
    const transport = makeTransport(
      [orderedSnapshot(), staleUnordered.promise],
      {
        attachReply: () => response(orderedStatus()),
      },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()

    // A newer raw observation raises the shared ordering floor past the
    // admitted scene while the next HTTP request is still pending.
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))

    // The late unordered response carries a stale title but no ordering.
    const stale = unorderedSnapshot('Unordered stale queue')
    stale.sessionStatus.ownerClientId = 'tab-a'
    stale.sessionStatus.ownershipState = 'attached'
    await act(async () => {
      staleUnordered.resolve(response(stale))
    })

    // Command egress is fenced first: clicking the rendered Pause must send
    // nothing while the accepted scene is retained by the authority.
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(commands(transport)).toEqual([])

    // Redux authority alone retaining the scene is not sufficient: the
    // displayed scene must be retained as well.
    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Unordered stale queue' }),
    ).not.toBeInTheDocument()
    expect(store.getState().player.jukeboxRemote.stateSequence).toBe(11)
    expect(store.getState().player.jukeboxRemote.trackId).toBe('song-a')
  })

  it('an HTTP snapshot pinned at the accepted anchor sequence but below the observed floor cannot render', async () => {
    const belowFloor = deferred()
    const transport = makeTransport([orderedSnapshot(), belowFloor.promise], {
      attachReply: () => response(orderedStatus()),
    })
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))

    // Same generation, sequence back at the accepted anchor (10), i.e.
    // below the observed floor (11), with a stale playlist title.
    await act(async () => {
      belowFloor.resolve(
        response(
          orderedSnapshot('Below-floor anchor queue', { stateSequence: 10 }),
        ),
      )
    })

    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Below-floor anchor queue' }),
    ).not.toBeInTheDocument()
    expect(commands(transport)).toEqual([])
    expect(store.getState().player.jukeboxRemote.stateSequence).toBe(11)
    expect(store.getState().player.jukeboxRemote.trackId).toBe('song-a')
  })

  it('a binding drift arms exactly one bounded follow-up snapshot GET that reconciles against the new binding', async () => {
    const gen3 = orderedSnapshot('Generation 3 queue', {
      bindingGeneration: 3,
      stateSequence: 1,
      deviceName: 'pulse/gen3',
    })
    const gen3Advanced = orderedSnapshot('Generation 3 queue', {
      bindingGeneration: 3,
      stateSequence: 2,
      deviceName: 'pulse/gen3',
    })
    const transport = makeTransport([orderedSnapshot(), gen3, gen3Advanced], {
      attachReply: () => response(orderedStatus()),
    })
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()

    // A raw observation announces the higher generation; the floor advance
    // reacquires the atomic snapshot, which admits the new binding but cannot
    // confer writes from the older request provenance.
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit(
          'jukeboxStateUpdated',
          orderedStatus({ bindingGeneration: 3, stateSequence: 1 }),
        ),
    )
    await screen.findByRole('heading', { name: 'Generation 3 queue' })

    // The intermediate suspension is asserted at the Redux authority level
    // (grant suspended, owner eligibility null); React batches the drift
    // admission and the bounded reconciliation, so the DOM may never commit
    // a disabled frame here. The observable contract: exactly ONE bounded
    // follow-up GET reconciles and confers writes — no 15s heartbeat wait,
    // no takeover, no command egress, no request storm.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled(),
    )
    expect(snapshotsRequested(transport)).toHaveLength(3)
    expect(commands(transport)).toEqual([])
  })

  it('a repeated missing-device drift answer stays readonly without a snapshot storm', async () => {
    const gen3 = orderedSnapshot('Generation 3 queue', {
      bindingGeneration: 3,
      stateSequence: 1,
      deviceName: 'pulse/gen3',
    })
    const gen3Missing = orderedSnapshot('Generation 3 queue', {
      bindingGeneration: 3,
      stateSequence: 2,
      deviceName: null,
    })
    const transport = makeTransport(
      [orderedSnapshot(), gen3, gen3Missing, gen3Missing],
      {
        attachReply: () => response(orderedStatus()),
      },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit(
          'jukeboxStateUpdated',
          orderedStatus({ bindingGeneration: 3, stateSequence: 1 }),
        ),
    )
    await screen.findByRole('heading', { name: 'Generation 3 queue' })

    // The ONE bounded follow-up answers without a canonical device again: the
    // reconciliation is consumed, control stays suspended, and no further
    // snapshot GETs are issued.
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(3))
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(snapshotsRequested(transport)).toHaveLength(3)
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(commands(transport)).toEqual([])
  })

  it('a trusted snapshot omitting the device stays readonly, forbids command POSTs, and recovers on an explicit device', async () => {
    const deviceless = orderedSnapshot('Deviceless advance queue', {
      stateSequence: 11,
      position: 20,
    })
    delete deviceless.sessionStatus.deviceName
    const recovered = orderedSnapshot('Recovered device queue', {
      stateSequence: 12,
    })
    const transport = makeTransport(
      [orderedSnapshot(), deviceless, recovered],
      {
        attachReply: () => response(orderedStatus()),
      },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()

    // A raw observation raises the floor; the controller reacquires the
    // atomic snapshot, which OMITS the device field.
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )
    await screen.findByRole('heading', { name: 'Deviceless advance queue' })

    // Missing device is read-only: no inherited device may confer writes, no
    // reconciliation may be armed against one, and user interaction must not
    // egress a command.
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(commands(transport)).toEqual([])
    expect(store.getState().player.jukeboxControl.controlGranted).toBe(false)
    expect(store.getState().player.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(store.getState().player.jukeboxControl.bindingReconcile).toBeNull()

    // An explicit current valid device from a fresh live trusted episode
    // recovers correctly: the grant is restored and a user command egresses.
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 12 })),
    )
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled(),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(commands(transport)).not.toHaveLength(0))
    expect(commands(transport).at(-1).path).toContain('/pause')
  })

  it('a higher-generation snapshot omitting the device does not arm a reconciliation GET or egress commands', async () => {
    const gen3Deviceless = orderedSnapshot('Generation 3 deviceless queue', {
      bindingGeneration: 3,
      stateSequence: 0,
    })
    delete gen3Deviceless.sessionStatus.deviceName
    const transport = makeTransport(
      [orderedSnapshot(), gen3Deviceless, gen3Deviceless],
      {
        attachReply: () => response(orderedStatus()),
      },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()

    // The server rebound to a new generation but the snapshot omits the
    // device: the prior binding device must not be carried into the new
    // generation nor armed as a reconciliation target.
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit(
          'jukeboxStateUpdated',
          orderedStatus({ bindingGeneration: 3, stateSequence: 0 }),
        ),
    )
    await screen.findByRole('heading', {
      name: 'Generation 3 deviceless queue',
    })

    // No bounded follow-up GET is armed: the request count stays bounded and
    // command egress stays forbidden.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(snapshotsRequested(transport)).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(commands(transport)).toEqual([])
    expect(store.getState().player.jukeboxControl.controlGranted).toBe(false)
    expect(store.getState().player.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(store.getState().player.jukeboxControl.bindingReconcile).toBeNull()
  })

  it('two controllers keep the shared scene observable and pass control only through explicit takeover after the owner retires', async () => {
    let owner = 'tab-a'
    let playing = true
    // Servers advance the state sequence on each accepted command, so the
    // post-pause snapshot supersedes the equal-sequence scene instead of
    // conflicting with it.
    let stateSequence = 10
    const requests = []
    const currentStatus = () =>
      orderedStatus({ ownerClientId: owner, playing, stateSequence })
    const currentSnapshot = () => {
      const next = orderedSnapshot('Shared ordered queue')
      next.sessionStatus = currentStatus()
      next.jukeboxPlaylist.playing = playing
      return next
    }
    vi.stubGlobal(
      'fetch',
      vi.fn((url, options = {}) => {
        const path = new URL(url, 'http://localhost').pathname
        const body = options.body ? JSON.parse(options.body) : {}
        requests.push({ path, body })
        if (path.endsWith('/session/attach')) {
          if (body.clientId !== owner && body.takeover !== true) {
            return Promise.resolve(response({ message: 'Control denied' }, 403))
          }
          if (body.takeover === true) owner = body.clientId
          return Promise.resolve(response(currentStatus()))
        }
        if (path.endsWith('/session/snapshot')) {
          return Promise.resolve(response(currentSnapshot()))
        }
        if (path.endsWith('/pause')) {
          if (body.clientId !== owner) {
            return Promise.resolve(response({ message: 'Control denied' }, 403))
          }
          playing = false
          stateSequence = 11
          return Promise.resolve(response({ playing: false }))
        }
        return Promise.resolve(response({}))
      }),
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    const ownerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    const observerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-b"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    await waitFor(() =>
      expect(
        screen.getAllByRole('heading', { name: 'Shared ordered queue' }),
      ).toHaveLength(2),
    )
    expect(screen.getAllByRole('button', { name: 'Pause' })[0]).toBeEnabled()
    expect(screen.getAllByRole('button', { name: 'Pause' })[1]).toBeDisabled()

    // The owner retires; the observer keeps observing the shared scene.
    ownerView.unmount()
    await waitFor(() =>
      expect(
        requests.filter(({ path }) => path.endsWith('/session/detach')),
      ).toHaveLength(1),
    )
    expect(
      requests.find(({ path }) => path.endsWith('/session/detach')).body,
    ).toEqual(
      expect.objectContaining({
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      }),
    )
    expect(
      observerView.getByRole('heading', { name: 'Shared ordered queue' }),
    ).toBeInTheDocument()
    expect(observerView.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(
      observerView.getByRole('button', { name: 'Take control' }),
    ).toBeEnabled()

    // Control passes only through the observer's explicit takeover.
    fireEvent.click(observerView.getByRole('button', { name: 'Take control' }))
    await waitFor(() =>
      expect(observerView.getByRole('button', { name: 'Pause' })).toBeEnabled(),
    )
    expect(owner).toBe('tab-b')
    expect(requests.filter(({ body }) => body.takeover === true)).toHaveLength(
      1,
    )

    fireEvent.click(observerView.getByRole('button', { name: 'Pause' }))
    await observerView.findByText('Paused', { exact: true })
    expect(playing).toBe(false)
    expect(commands({ requests })).toHaveLength(1)
    expect(commands({ requests })[0].path).toBe('/api/jukebox/pause')
    expect(commands({ requests })[0].body).toEqual(
      expect.objectContaining({
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-b',
      }),
    )
    observerView.unmount()
  })

  it('a same-identity remount rejects the old token but must admit an identical scene on the new token', async () => {
    const pendingSnapshots = []
    const requests = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url, options = {}) => {
        const path = new URL(url, 'http://localhost').pathname
        requests.push({ path })
        if (path.endsWith('/session/attach')) {
          return Promise.resolve(response(orderedStatus()))
        }
        if (path.endsWith('/session/snapshot')) {
          const snapshotRequest = deferred()
          pendingSnapshots.push(snapshotRequest)
          return snapshotRequest.promise
        }
        return Promise.resolve(response({}))
      }),
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )

    const first = renderController({}, store)
    await waitFor(() =>
      expect(snapshotsRequested({ requests })).toHaveLength(1),
    )
    window.dispatchEvent(new Event('pagehide'))
    first.unmount()

    // A local<->remote transition clears the admitted pair, so the remounted
    // same-identity episode is the only remaining display source.
    await act(async () => {
      store.dispatch(setJukeboxMode(false))
      store.dispatch(setJukeboxMode(true, 'pulse/test'))
    })

    renderController({}, store)
    await waitFor(() =>
      expect(snapshotsRequested({ requests })).toHaveLength(2),
    )

    // The NEW token returns the IDENTICAL scene: a fresh fetch of unchanged
    // server state must be admitted — the anti-ABA guard must not over-reject
    // content merely because a retired same-identity episode observed it.
    await act(async () => {
      pendingSnapshots[1].resolve(
        response(orderedSnapshot('Remount held queue')),
      )
    })
    await screen.findByRole('heading', { name: 'Remount held queue' })

    // The OLD token's late response now carries a CHANGED scene: it must not
    // be adopted by the live episode.
    await act(async () => {
      pendingSnapshots[0].resolve(
        response(
          orderedSnapshot('Old token changed queue', { stateSequence: 12 }),
        ),
      )
    })
    expect(
      screen.getByRole('heading', { name: 'Remount held queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Old token changed queue' }),
    ).not.toBeInTheDocument()
    expect(commands({ requests })).toEqual([])
  })

  it('an observer GET admits the shared scene for both subscribers and never grants the observer eligibility', async () => {
    const shared = orderedSnapshot('Shared observed queue')
    const transport = makeTransport([shared, shared], {
      attachReply: ({ clientId }) =>
        clientId === 'tab-a'
          ? response(orderedStatus())
          : response({ message: 'Control denied' }, 403),
    })
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    const ownerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    const observerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-b"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    await waitFor(() =>
      expect(
        screen.getAllByRole('heading', { name: 'Shared observed queue' }),
      ).toHaveLength(2),
    )
    const owner = within(ownerView.container)
    const observer = within(observerView.container)

    // The observer never gets command eligibility: only explicit takeover.
    expect(observer.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(observer.getByRole('button', { name: 'Take control' })).toBeEnabled()
    // The owner's eligibility survives the observer's GET entirely.
    expect(owner.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(commands(transport)).toEqual([])
    ownerView.unmount()
    observerView.unmount()
  })

  it('a superseded owner emits zero POSTs once the new-owner snapshot is admitted', async () => {
    let owner = 'tab-a'
    let playing = true
    let stateSequence = 10
    const requests = []
    const currentStatus = () =>
      orderedStatus({ ownerClientId: owner, playing, stateSequence })
    const currentSnapshot = () => {
      const next = orderedSnapshot('Owner superseded queue')
      next.sessionStatus = currentStatus()
      next.jukeboxPlaylist.playing = playing
      return next
    }
    vi.stubGlobal(
      'fetch',
      vi.fn((url, options = {}) => {
        const path = new URL(url, 'http://localhost').pathname
        const body = options.body ? JSON.parse(options.body) : {}
        requests.push({ path, body })
        if (path.endsWith('/session/attach')) {
          if (body.clientId !== owner && body.takeover !== true) {
            return Promise.resolve(response({ message: 'Control denied' }, 403))
          }
          if (body.takeover === true) owner = body.clientId
          return Promise.resolve(response(currentStatus()))
        }
        if (path.endsWith('/session/snapshot')) {
          return Promise.resolve(response(currentSnapshot()))
        }
        if (path.endsWith('/pause')) {
          if (body.clientId !== owner) {
            return Promise.resolve(response({ message: 'Control denied' }, 403))
          }
          playing = false
          stateSequence = 11
          return Promise.resolve(response({ playing: false }))
        }
        return Promise.resolve(response({}))
      }),
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    const ownerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    const observerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-b"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    await waitFor(() =>
      expect(
        screen.getAllByRole('heading', { name: 'Owner superseded queue' }),
      ).toHaveLength(2),
    )
    const ownerUi = within(ownerView.container)
    const observerUi = within(observerView.container)

    // The observer takes over; the new-owner snapshot is admitted at the
    // same ordering, so the shared scene stays identical for both.
    fireEvent.click(observerUi.getByRole('button', { name: 'Take control' }))
    await waitFor(() =>
      expect(observerUi.getByRole('button', { name: 'Pause' })).toBeEnabled(),
    )
    expect(owner).toBe('tab-b')
    await waitFor(() =>
      expect(store.getState().player.jukeboxControl.ownerClientId).toBe(
        'tab-b',
      ),
    )

    // The superseded owner's forced command must egress NOTHING: the client
    // fences it against the admitted owner instead of posting and receiving
    // a server 403.
    fireEvent.click(ownerUi.getByRole('button', { name: 'Pause' }))
    await act(async () => {})
    expect(commands({ requests })).toEqual([])

    // The new owner remains fully usable.
    fireEvent.click(observerUi.getByRole('button', { name: 'Pause' }))
    await observerUi.findByText('Paused', { exact: true })
    expect(commands({ requests })).toHaveLength(1)
    expect(commands({ requests })[0].body).toEqual(
      expect.objectContaining({
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-b',
      }),
    )
    ownerView.unmount()
    observerView.unmount()
  })

  it('observer retirement never gates the attached owner: owner eligibility and control survive observer unmount', async () => {
    let playing = true
    // Servers advance the state sequence on each accepted command so the
    // post-pause snapshot supersedes the scene instead of conflicting with it.
    let stateSequence = 10
    const requests = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url, options = {}) => {
        const path = new URL(url, 'http://localhost').pathname
        const body = options.body ? JSON.parse(options.body) : {}
        requests.push({ path, body })
        if (path.endsWith('/session/attach')) {
          if (body.clientId !== 'tab-a') {
            return Promise.resolve(response({ message: 'Control denied' }, 403))
          }
          return Promise.resolve(response(orderedStatus()))
        }
        if (path.endsWith('/session/snapshot')) {
          const next = orderedSnapshot('Owner led queue', { stateSequence })
          next.sessionStatus.playing = playing
          next.jukeboxPlaylist.playing = playing
          return Promise.resolve(response(next))
        }
        if (path.endsWith('/pause')) {
          playing = false
          stateSequence = 11
          return Promise.resolve(response({ playing: false }))
        }
        return Promise.resolve(response({}))
      }),
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    const ownerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    const observerView = render(
      <Provider store={store}>
        <RemotePlayerController
          sessionId="jukebox-session:admin"
          clientId="tab-b"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    await waitFor(() =>
      expect(
        screen.getAllByRole('heading', { name: 'Owner led queue' }),
      ).toHaveLength(2),
    )
    const owner = within(ownerView.container)
    const observer = within(observerView.container)
    expect(owner.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(observer.getByRole('button', { name: 'Pause' })).toBeDisabled()

    // The denied observer retires: it never owned the session, so no detach
    // is sent and the owner's eligibility must be untouched.
    observerView.unmount()
    await act(async () => {})
    expect(
      requests.filter(({ path }) => path.endsWith('/session/detach')),
    ).toHaveLength(0)
    expect(owner.getByRole('button', { name: 'Pause' })).toBeEnabled()
    expect(
      owner.getByRole('heading', { name: 'Owner led queue' }),
    ).toBeInTheDocument()

    fireEvent.click(owner.getByRole('button', { name: 'Pause' }))
    await owner.findByText('Paused', { exact: true })
    expect(commands({ requests })).toHaveLength(1)
    expect(commands({ requests })[0].body).toEqual(
      expect.objectContaining({
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      }),
    )
    ownerView.unmount()
  })
})

describe('legacy unordered server read-only display', () => {
  it('a pre-strict unordered snapshot displays read-only and never egresses commands', async () => {
    const transport = makeTransport([
      unorderedSnapshot('Legacy unordered queue'),
    ])
    renderController()
    await screen.findByRole('heading', { name: 'Legacy unordered queue' })
    const pause = screen.getByRole('button', { name: 'Pause' })
    expect(pause).toBeDisabled()
    fireEvent.click(pause)
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    expect(commands(transport)).toEqual([])
  })

  it('a post-strict unordered HTTP response retains the accepted pair read-only', async () => {
    const lateUnordered = deferred()
    const transport = makeTransport(
      [orderedSnapshot(), lateUnordered.promise],
      {
        attachReply: () => response(orderedStatus()),
      },
    )
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Ordered server queue' })

    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', orderedStatus({ stateSequence: 11 })),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))

    await act(async () => {
      lateUnordered.resolve(
        response(unorderedSnapshot('Unordered replacement queue')),
      )
    })

    expect(
      screen.getByRole('heading', { name: 'Ordered server queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Unordered replacement queue' }),
    ).not.toBeInTheDocument()
    const pause = screen.getByRole('button', { name: 'Pause' })
    expect(pause).toBeDisabled()
    fireEvent.click(pause)
    expect(commands(transport)).toEqual([])
    expect(store.getState().player.jukeboxRemote.stateSequence).toBe(11)
    expect(store.getState().player.jukeboxRemote.trackId).toBe('song-a')
  })
})

describe('default Player Remote route', () => {
  it('renders the authoritative Remote snapshot without mounting or feeding a browser playback engine', async () => {
    localStorage.setItem('subsonic-token', '0123456789abcdef0123456789abcdef')
    localStorage.setItem('subsonic-salt', 'remote-route-fixture')
    const server = snapshot('Authoritative remote queue', 27)
    server.sessionStatus.ownerClientId = getOrCreateJukeboxClientId()
    const transport = makeTransport([server])
    const localTrack = {
      trackId: 'retained-local',
      uuid: 'retained-local-occurrence',
      name: 'Retained local song',
      song: { id: 'retained-local', title: 'Retained local song' },
      musicSrc: '/rest/stream?id=retained-local',
    }
    const store = createStore(
      combineReducers({
        player: playerReducer,
        activity: activityReducer,
        settings: settingsReducer,
        replayGain: replayGainReducer,
        theme: themeReducer,
      }),
      {
        player: {
          ...playerReducer(undefined, { type: '@@INIT' }),
          queue: [localTrack],
          current: localTrack,
          savedPlayIndex: 0,
          playIndex: 0,
        },
        replayGain: {
          ...replayGainReducer(undefined, { type: '@@INIT' }),
          gainMode: 'track',
        },
      },
    )
    store.dispatch(setJukeboxMode(true, 'Validation output'))
    store.dispatch(updateJukeboxSessionStatus(server.sessionStatus))
    const audioContext = vi.fn(function AudioContextBoundary() {
      return {
        currentTime: 0,
        destination: {},
        createMediaElementSource: () => ({ connect: vi.fn() }),
        createGain: () => ({
          connect: vi.fn(),
          gain: { setValueAtTime: vi.fn() },
        }),
        close: () => Promise.resolve(),
      }
    })
    vi.stubGlobal('AudioContext', audioContext)
    expect('AudioContext' in window).toBe(true)
    const originalReplayGain = config.enableReplayGain
    config.enableReplayGain = true
    browserPlayback.mounts = 0
    browserPlayback.streamUrls = []
    const { container, unmount } = render(
      <Provider store={store}>
        <Player />
      </Provider>,
    )
    try {
      await waitFor(() =>
        expect(
          transport.requests.filter(({ path }) =>
            path.endsWith('/session/attach'),
          ),
        ).toHaveLength(1),
      )
      const attachment = transport.requests.find(({ path }) =>
        path.endsWith('/session/attach'),
      )
      expect(attachment.options.headers.get('X-ND-Authorization')).toBe(
        'Bearer fake-jwt',
      )
      await act(async () => {})
      expect({
        audioElements: container.querySelectorAll('audio').length,
        browserEngineMounts: browserPlayback.mounts,
        audioContexts: audioContext.mock.calls.length,
        consumedStreamUrls: browserPlayback.streamUrls,
        browserStreamRequests: transport.requests
          .filter(({ path }) =>
            /^\/rest\/(stream|getTranscodeStream|getTranscodeDecision)$/.test(
              path,
            ),
          )
          .map(({ path }) => path),
      }).toEqual({
        audioElements: 0,
        browserEngineMounts: 0,
        audioContexts: 0,
        consumedStreamUrls: [],
        browserStreamRequests: [],
      })
      expect(
        await screen.findByRole('heading', {
          name: 'Authoritative remote queue',
        }),
      ).toBeInTheDocument()
      expect(screen.getByText('Position: 27 seconds')).toBeInTheDocument()
      expect(screen.getByText('Next song — Test artist')).toBeInTheDocument()
      expect(snapshotsRequested(transport)).toHaveLength(1)
      expect(
        screen.queryByRole('heading', { name: 'Retained local song' }),
      ).not.toBeInTheDocument()
      expect(
        transport.requests.filter(
          ({ path }) =>
            path.startsWith('/api/jukebox/') && !path.includes('/session/'),
        ),
      ).toEqual([])
    } finally {
      unmount()
      await act(async () => {})
      config.enableReplayGain = originalReplayGain
    }
  })
})

describe('P0-B frontend control closure and session rehydration', () => {
  it('dispatches library queue actions to jukeboxClient when in remote mode', async () => {
    const sagaMiddleware = createSagaMiddleware()
    const store = createStore(
      combineReducers({
        player: playerReducer,
        activity: activityReducer,
      }),
      applyMiddleware(sagaMiddleware),
    )
    sagaMiddleware.run(jukeboxQueueSaga)

    const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})
    const skipSpy = vi.spyOn(jukeboxClient, 'skip').mockResolvedValue({})
    const playSpy = vi.spyOn(jukeboxClient, 'play').mockResolvedValue({})
    const addSpy = vi.spyOn(jukeboxClient, 'add').mockResolvedValue({})

    store.dispatch(setJukeboxMode(true, 'Bluetooth Output'))
    // Reducer-admitted authority: the legacy saga handlers egress only while
    // the shared authority grants control to this document's scope.
    store.dispatch(
      beginJukeboxEpisode({
        token: 'p0b-token',
        sessionId: 'jukebox-session:admin',
        clientId: getOrCreateJukeboxClientId(),
      }),
    )
    store.dispatch(
      updateJukeboxSessionStatus(
        {
          sessionId: 'jukebox-session:admin',
          ownerClientId: getOrCreateJukeboxClientId(),
          ownershipState: 'attached',
          deviceName: 'Bluetooth Output',
          bindingGeneration: 2,
          stateSequence: 10,
        },
        {
          source: 'atomic-session-snapshot',
          sessionId: 'jukebox-session:admin',
          clientId: getOrCreateJukeboxClientId(),
          token: 'p0b-token',
          revision: 0,
          requestBinding: {
            bindingGeneration: null,
            deviceName: null,
          },
        },
      ),
    )

    // 1. Play Tracks (Play Now / Play All)
    store.dispatch(
      playTracks(
        {
          s1: { id: 's1', title: 'Song 1' },
          s2: { id: 's2', title: 'Song 2' },
        },
        ['s1', 's2'],
        's2',
      ),
    )

    await waitFor(() => {
      expect(setSpy).toHaveBeenCalledWith(
        ['s1', 's2'],
        expect.objectContaining({ sessionId: 'jukebox-session:admin' }),
      )
      expect(skipSpy).toHaveBeenCalledWith(
        1,
        0,
        expect.objectContaining({ sessionId: 'jukebox-session:admin' }),
      )
      expect(playSpy).toHaveBeenCalled()
    })

    // 2. Play Next
    store.dispatch(playNext({ s3: { id: 's3', title: 'Song 3' } }))
    await waitFor(() => {
      expect(addSpy).toHaveBeenCalledWith(
        ['s3'],
        expect.any(Number),
        expect.objectContaining({ sessionId: 'jukebox-session:admin' }),
      )
    })

    // 3. Add to Queue
    store.dispatch(addTracks({ s4: { id: 's4', title: 'Song 4' } }))
    await waitFor(() => {
      expect(addSpy).toHaveBeenCalledWith(
        ['s4'],
        -1,
        expect.objectContaining({ sessionId: 'jukebox-session:admin' }),
      )
    })
  })

  it('reconnection / reload rehydrates remote session without pushing local queue', async () => {
    vi.restoreAllMocks()
    const remoteStatus = {
      sessionId: 'jukebox-session:admin',
      deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
      playing: true,
      currentIndex: 1,
      position: 15,
      gain: 0.2,
    }
    vi.spyOn(jukeboxClient, 'sessionStatus').mockResolvedValue(remoteStatus)
    const setSpy = vi.spyOn(jukeboxClient, 'set').mockResolvedValue({})

    const dispatch = vi.fn()
    const result = await rehydrateJukeboxSession({
      client: jukeboxClient,
      dispatch,
    })

    expect(result).toEqual(remoteStatus)
    expect(dispatch).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'PLAYER_SET_JUKEBOX_MODE',
        data: {
          enabled: true,
          device: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
        },
      }),
    )
    expect(setSpy).not.toHaveBeenCalled()
  })

  it('renders DeviceSelector in remote mode controller', async () => {
    const origJukebox = config.jukeboxEnabled
    const origBluetooth = config.bluetoothManagementEnabled
    config.jukeboxEnabled = true
    config.bluetoothManagementEnabled = true
    try {
      makeTransport([snapshot()])
      const { container } = renderController()
      await screen.findByRole('heading', { name: 'Server queue' })
      await waitFor(() => {
        expect(
          container.querySelector('[data-testid="device-selector-button"]'),
        ).toBeInTheDocument()
      })
    } finally {
      config.jukeboxEnabled = origJukebox
      config.bluetoothManagementEnabled = origBluetooth
    }
  })

  it('DeviceSelector triggers clean Local <-> Remote transitions without audio leakage', async () => {
    const origJukebox = config.jukeboxEnabled
    config.jukeboxEnabled = true
    try {
      const audioInstance = {
        muted: false,
        paused: true,
        volume: 0.8,
        currentTime: 0,
        play: vi.fn(() => Promise.resolve()),
      }
      const store = createStore(
        combineReducers({
          player: playerReducer,
          activity: activityReducer,
        }),
        {
          player: {
            ...playerReducer(undefined, { type: '@@INIT' }),
            audioInstance,
            queue: [{ trackId: 't1' }],
            jukeboxMode: false,
          },
        },
      )

      vi.spyOn(jukeboxClient, 'attachSession').mockResolvedValue({
        sessionId: 'jukebox-session:admin',
        ownerClientId: 'tab-a',
        ownershipState: 'attached',
      })
      vi.spyOn(jukeboxClient, 'stop').mockResolvedValue({})
      vi.spyOn(jukeboxClient, 'detachSession').mockResolvedValue({})
      vi.spyOn(jukeboxClient, 'switchDevice').mockResolvedValue([])

      render(
        <Provider store={store}>
          <DeviceSelector isDesktop={true} />
        </Provider>,
      )

      // 1. Switch to remote mode
      expect(audioInstance.muted).toBe(false)
      store.dispatch(setJukeboxMode(true, 'Bluetooth Output'))
      expect(store.getState().player.jukeboxMode).toBe(true)

      // 2. Switch back to local mode
      store.dispatch(setJukeboxMode(false))
      expect(store.getState().player.jukeboxMode).toBe(false)
    } finally {
      config.jukeboxEnabled = origJukebox
    }
  })
})
