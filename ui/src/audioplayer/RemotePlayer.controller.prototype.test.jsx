import React from 'react'
import { Provider } from 'react-redux'
import { combineReducers, createStore } from 'redux'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RemotePlayerControllerPrototype from './RemotePlayer.controller.prototype'
import { playerReducer } from '../reducers/playerReducer'
import { activityReducer } from '../reducers/activityReducer'
import { startEventStream } from '../eventStream'
import config from '../config'

const snapshot = (title = 'Server queue', position = 12) => ({
  sessionStatus: {
    sessionId: 'jukebox-session:admin',
    ownerClientId: 'tab-a',
    ownershipState: 'attached',
    deviceName: 'pulse/test',
    currentIndex: 0,
    trackId: 'song-a',
    playing: true,
    position,
    gain: 0.25,
    queueVersion: 1,
  },
  jukeboxPlaylist: {
    currentIndex: 0,
    playing: true,
    position,
    gain: 0.25,
    entry: [
      {
        id: 'song-a',
        title,
        artist: 'Test artist',
        album: 'Test album',
        duration: 60,
      },
      {
        id: 'song-b',
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
        : Promise.resolve(reply instanceof Response ? reply : response(reply))
    }
    if (!path.includes('/session/') && commandReply) {
      return Promise.resolve(commandReply).then((reply) => reply.clone())
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
      <RemotePlayerControllerPrototype
        sessionId="jukebox-session:admin"
        clientId="tab-a"
        deviceName="pulse/test"
        {...props}
      />
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

describe('<RemotePlayerControllerPrototype />', () => {
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
        <RemotePlayerControllerPrototype
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
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', { position: 13 }),
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
    const transport = makeTransport(
      [snapshot(), snapshot('Reattached queue', 31)],
      { heartbeatReply: response({ message: 'Session missing' }, 404) },
    )
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
        <RemotePlayerControllerPrototype
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
        <RemotePlayerControllerPrototype
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
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', { currentIndex: 1, trackId: 'song-b' }),
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
    const transport = makeTransport([
      snapshot(),
      snapshot('Restored after reconnect', 27),
    ])
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
      body: { gain: 0.4 },
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
    const transport = makeTransport([
      snapshot(),
      stale.promise,
      snapshot('Newest server queue', 33),
    ])
    const store = createStore(
      combineReducers({ player: playerReducer, activity: activityReducer }),
    )
    await startEventStream(store.dispatch)
    renderController({}, store)
    await screen.findByRole('heading', { name: 'Server queue' })
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', { position: 20 }),
    )
    await waitFor(() => expect(snapshotsRequested(transport)).toHaveLength(2))
    await act(async () =>
      EventSourceTransport.instances
        .at(-1)
        .emit('jukeboxStateUpdated', { position: 33 }),
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
    const transport = makeTransport([
      snapshot(),
      snapshot('HTTP refreshed queue', 18),
    ])
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
        <RemotePlayerControllerPrototype
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
    const requests = []
    const deniedCommands = []
    const currentSnapshot = () => {
      const current = snapshot()
      current.sessionStatus.ownerClientId = owner
      current.sessionStatus.playing = playing
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
        <RemotePlayerControllerPrototype
          sessionId="jukebox-session:admin"
          clientId="tab-a"
          deviceName="pulse/test"
        />
        <RemotePlayerControllerPrototype
          sessionId="jukebox-session:admin"
          clientId="tab-b"
          deviceName="pulse/test"
        />
      </Provider>,
    )
    const [first, second] = Array.from(
      container.querySelectorAll('[aria-label="Remote controller prototype"]'),
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
    fireEvent.click(first.getByRole('button', { name: 'Pause' }))
    await first.findByRole('alert')
    await waitFor(() =>
      expect(first.getByRole('button', { name: 'Pause' })).toBeDisabled(),
    )
    expect(deniedCommands).toEqual([
      { sessionId: 'jukebox-session:admin', clientId: 'tab-a' },
    ])
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
