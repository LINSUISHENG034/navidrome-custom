import { beforeEach, describe, expect, it, vi } from 'vitest'
import httpClient from '../dataProvider/httpClient'
import jukeboxClient from './jukeboxClient'

vi.mock('../dataProvider/httpClient', () => ({
  default: vi.fn(),
}))

describe('jukeboxClient.sessionSnapshot', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('loads the session-bound snapshot through the authenticated HTTP client', async () => {
    const payload = {
      sessionStatus: { sessionId: 'session-a' },
      jukeboxPlaylist: { currentIndex: 1, entry: [{ id: 'song-a' }] },
    }
    httpClient.mockResolvedValue({ json: payload })

    await expect(jukeboxClient.sessionSnapshot('session / a')).resolves.toEqual(
      payload,
    )
    expect(httpClient).toHaveBeenCalledWith(
      '/api/jukebox/session/snapshot?sessionId=session%20%2F%20a',
    )
  })
})

describe('jukeboxClient command authority', () => {
  const scope = { sessionId: 'session-a', clientId: 'tab-a' }
  const attached = (identity = scope) => ({
    ...identity,
    ownerClientId: identity.clientId,
    ownershipState: 'attached',
  })
  const deferred = () => {
    let resolve
    const promise = new Promise((complete) => {
      resolve = complete
    })
    return { promise, resolve }
  }
  const mutations = [
    ['set', [['song-a']], { ids: ['song-a'] }],
    ['start', [], {}],
    ['play', [], {}],
    ['pause', [], {}],
    ['stop', [], {}],
    ['skip', [1, 3], { index: 1, offset: 3 }],
    ['seek', [12], { position: 12 }],
    ['volume', [0.25], { gain: 0.25 }],
    ['add', [['song-b'], 1], { ids: ['song-b'], index: 1 }],
    ['remove', [1], { index: 1 }],
    ['move', [1, 0], { from: 1, to: 0 }],
    ['switchDevice', ['pulse/test'], { deviceName: 'pulse/test' }],
  ]
  let client

  beforeEach(async () => {
    vi.resetModules()
    vi.clearAllMocks()
    client = (await import('./jukeboxClient')).default
    httpClient.mockResolvedValue({ json: {} })
  })

  it.each(mutations)(
    'carries explicit identity for %s without changing the command payload',
    async (method, args, payload) => {
      await client[method](...args, scope)
      const [path, options] = httpClient.mock.calls.at(-1)
      expect(path).toBe(
        `/api/jukebox/${method === 'switchDevice' ? 'devices/switch' : method}`,
      )
      expect(options.method).toBe('POST')
      expect(JSON.parse(options.body)).toEqual({ ...payload, ...scope })
    },
  )

  it.each(mutations)(
    'uses only a confirmed attached identity as the default for %s',
    async (method, args, payload) => {
      httpClient.mockResolvedValueOnce({ json: attached() })
      await client.attachSession(scope.sessionId, scope.clientId)
      await client[method](...args)
      expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual({
        ...payload,
        ...scope,
      })
    },
  )

  it('leaves legacy requests unscoped before attachment and after matching detach', async () => {
    await client.volume(0.25)
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual({
      gain: 0.25,
    })
    httpClient.mockResolvedValueOnce({ json: attached() })
    await client.attachSession(scope.sessionId, scope.clientId)
    await client.detachSession(scope.sessionId, scope.clientId)
    await client.play()
    expect(httpClient.mock.calls.at(-1)).toEqual([
      '/api/jukebox/play',
      { method: 'POST' },
    ])
  })

  it('sends takeover only when it is explicitly requested', async () => {
    httpClient.mockResolvedValue({ json: attached() })
    await client.attachSession(scope.sessionId, scope.clientId)
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual(scope)
    await client.attachSession(scope.sessionId, scope.clientId, 'pulse/test', {
      takeover: true,
    })
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual({
      ...scope,
      deviceName: 'pulse/test',
      takeover: true,
    })
  })

  it('does not claim identity after a denied attachment', async () => {
    httpClient.mockRejectedValueOnce(
      Object.assign(new Error('Denied'), { status: 403 }),
    )
    await expect(
      client.attachSession(scope.sessionId, scope.clientId),
    ).rejects.toThrow('Denied')
    await client.volume(0.25)
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual({
      gain: 0.25,
    })
  })

  it('does not claim another owner returned by an observer attachment', async () => {
    httpClient.mockResolvedValueOnce({
      json: { ...attached(), ownerClientId: 'other-tab' },
    })
    await client.attachSession(scope.sessionId, scope.clientId)
    await client.volume(0.25)
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual({
      gain: 0.25,
    })
  })

  it('does not let a late detach clear a newer attachment with the same identity', async () => {
    httpClient.mockResolvedValueOnce({ json: attached() })
    await client.attachSession(scope.sessionId, scope.clientId)
    const retirement = deferred()
    httpClient.mockReturnValueOnce(retirement.promise)
    const oldDetach = client.detachSession(scope.sessionId, scope.clientId)
    httpClient.mockResolvedValueOnce({ json: attached() })
    await client.attachSession(scope.sessionId, scope.clientId)
    retirement.resolve({ json: {} })
    await oldDetach
    await client.pause()
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual(scope)
  })

  it('does not replace a newer confirmed scope when an old attach completes late', async () => {
    const old = deferred()
    const next = { sessionId: 'session-b', clientId: 'tab-b' }
    httpClient.mockReturnValueOnce(old.promise)
    const pending = client.attachSession(scope.sessionId, scope.clientId)
    httpClient.mockResolvedValueOnce({ json: attached(next) })
    await client.attachSession(next.sessionId, next.clientId)
    old.resolve({ json: attached() })
    await pending
    await client.stop()
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual(next)
  })
})
