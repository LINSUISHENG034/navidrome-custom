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

  it('finishes an older detach before enrolling the same identity again', async () => {
    httpClient.mockResolvedValueOnce({ json: attached() })
    await client.attachSession(scope.sessionId, scope.clientId)
    const retirement = deferred()
    httpClient.mockReturnValueOnce(retirement.promise)
    const oldDetach = client.detachSession(scope.sessionId, scope.clientId)
    httpClient.mockResolvedValueOnce({ json: attached() })
    const replacement = client.attachSession(scope.sessionId, scope.clientId)
    await Promise.resolve()
    try {
      expect(
        httpClient.mock.calls.filter(([url]) =>
          url.endsWith('/session/attach'),
        ),
      ).toHaveLength(1)
    } finally {
      retirement.resolve({ json: {} })
    }
    await Promise.all([oldDetach, replacement])
    await client.pause()
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual(scope)
  })

  it('does not send old cleanup after a newer attachment of the same identity', async () => {
    httpClient.mockResolvedValueOnce({ json: attached() })
    const oldAttachment = await client.attachSession(
      scope.sessionId,
      scope.clientId,
    )
    httpClient.mockResolvedValueOnce({ json: attached() })
    await client.attachSession(scope.sessionId, scope.clientId)
    httpClient.mockClear()
    await client.detachSession(scope.sessionId, scope.clientId, {
      attachment: oldAttachment,
      keepalive: true,
    })
    expect(httpClient).not.toHaveBeenCalled()
    await client.pause()
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual(scope)
  })

  it('rechecks old cleanup after an in-flight newer attachment completes', async () => {
    httpClient.mockResolvedValueOnce({ json: attached() })
    const oldAttachment = await client.attachSession(
      scope.sessionId,
      scope.clientId,
    )
    const next = deferred()
    httpClient.mockReturnValueOnce(next.promise)
    const replacement = client.attachSession(scope.sessionId, scope.clientId)
    const oldCleanup = client.detachSession(scope.sessionId, scope.clientId, {
      attachment: oldAttachment,
    })
    next.resolve({ json: attached() })
    await Promise.all([replacement, oldCleanup])
    expect(
      httpClient.mock.calls.filter(([url]) => url.endsWith('/session/detach')),
    ).toHaveLength(0)
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

describe('jukeboxClient queue protocol capability lifecycle', () => {
  const scope = { sessionId: 'session-a', clientId: 'tab-a' }
  const fullProtocol = {
    version: 1,
    relativeMove: true,
    identitySelect: true,
    identityRemove: true,
    identityMigration: true,
    anchorVersionEnforcement: true,
    operationResults: true,
  }
  const attached = (extra = {}) => ({
    ...scope,
    ownerClientId: scope.clientId,
    ownershipState: 'attached',
    ...extra,
  })
  const deferred = () => {
    let resolve
    const promise = new Promise((complete) => {
      resolve = complete
    })
    return { promise, resolve }
  }
  let client

  beforeEach(async () => {
    vi.resetModules()
    vi.clearAllMocks()
    client = (await import('./jukeboxClient')).default
    httpClient.mockResolvedValue({ json: {} })
  })

  it('exposes no capability before any confirmed attachment', () => {
    expect(client.getQueueProtocol()).toBeNull()
  })

  it('binds the normalized capability of the confirmed attach lease', async () => {
    httpClient.mockResolvedValueOnce({
      json: attached({ queueProtocol: fullProtocol }),
    })
    await client.attachSession(scope.sessionId, scope.clientId)
    expect(client.getQueueProtocol()).toEqual(fullProtocol)
  })

  it('resets the capability when the binding detaches', async () => {
    httpClient.mockResolvedValueOnce({
      json: attached({ queueProtocol: fullProtocol }),
    })
    await client.attachSession(scope.sessionId, scope.clientId)
    await client.detachSession(scope.sessionId, scope.clientId)
    expect(client.getQueueProtocol()).toBeNull()
  })

  it('rebinds the capability to a newer attachment instead of keeping the stale one', async () => {
    httpClient.mockResolvedValueOnce({
      json: attached({ queueProtocol: fullProtocol }),
    })
    await client.attachSession(scope.sessionId, scope.clientId)
    httpClient.mockResolvedValueOnce({
      json: attached({ queueProtocol: null }),
    })
    await client.attachSession(scope.sessionId, scope.clientId)
    expect(client.getQueueProtocol()).toBeNull()
  })

  it('never lets a late old attach reinstall a capability over a newer binding', async () => {
    const old = deferred()
    httpClient.mockReturnValueOnce(old.promise)
    const pending = client.attachSession(scope.sessionId, scope.clientId)
    httpClient.mockResolvedValueOnce({
      json: attached({
        queueProtocol: { ...fullProtocol, relativeMove: false },
      }),
    })
    const replacement = client.attachSession(scope.sessionId, scope.clientId)
    old.resolve({ json: attached({ queueProtocol: fullProtocol }) })
    await Promise.all([pending, replacement])
    expect(client.getQueueProtocol()?.relativeMove).toBe(false)
  })

  it.each([
    ['field presence without enforced boolean values', { version: 1 }],
    ['an unsupported future version', { ...fullProtocol, version: 2 }],
    ['a non-object advertisement', 'v1'],
  ])(
    'treats %s as unsupported instead of inferring capability',
    async (_title, raw) => {
      httpClient.mockResolvedValueOnce({
        json: attached({ queueProtocol: raw }),
      })
      await client.attachSession(scope.sessionId, scope.clientId)
      expect(client.getQueueProtocol()).toBeNull()
    },
  )
})

describe('jukeboxClient anchor queue transport', () => {
  const scope = { sessionId: 'session-a', clientId: 'tab-a' }
  let client

  beforeEach(async () => {
    vi.resetModules()
    vi.clearAllMocks()
    client = (await import('./jukeboxClient')).default
    httpClient.mockResolvedValue({ json: {} })
  })

  it('sends an exact anchor move payload with no legacy index fields', async () => {
    await client.moveAnchor(
      {
        queueItemId: 'q-dup-3',
        anchorQueueItemId: 'q-dup-1',
        placement: 'before',
        expectedQueueVersion: 0,
      },
      scope,
    )
    const [path, options] = httpClient.mock.calls.at(-1)
    expect(path).toBe('/api/jukebox/move')
    expect(options.method).toBe('POST')
    expect(JSON.parse(options.body)).toEqual({
      queueItemId: 'q-dup-3',
      anchorQueueItemId: 'q-dup-1',
      placement: 'before',
      expectedQueueVersion: 0,
      ...scope,
    })
  })

  it('omits expectedQueueVersion when the caller captured none', async () => {
    await client.moveAnchor(
      {
        queueItemId: 'q-dup-3',
        anchorQueueItemId: 'q-dup-1',
        placement: 'before',
      },
      scope,
    )
    const body = JSON.parse(httpClient.mock.calls.at(-1)[1].body)
    expect(body).not.toHaveProperty('expectedQueueVersion')
    expect(body).not.toHaveProperty('index')
    expect(body).not.toHaveProperty('from')
    expect(body).not.toHaveProperty('to')
  })

  it('addresses identity remove and select transports by queueItemId only', async () => {
    await client.removeItem('q-dup-3', scope)
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual({
      queueItemId: 'q-dup-3',
      ...scope,
    })
    expect(httpClient.mock.calls.at(-1)[0]).toBe('/api/jukebox/remove')

    await client.skipToItem('q-dup-3', scope)
    expect(JSON.parse(httpClient.mock.calls.at(-1)[1].body)).toEqual({
      queueItemId: 'q-dup-3',
      ...scope,
    })
    expect(httpClient.mock.calls.at(-1)[0]).toBe('/api/jukebox/skip')
  })

  it('normalizes a 409 version conflict into a coded error with the fresh snapshot', async () => {
    const conflict = Object.assign(new Error('queue version conflict'), {
      status: 409,
      body: {
        code: 'QUEUE_VERSION_CONFLICT',
        error: 'queue version conflict',
        expectedQueueVersion: 7,
        queueVersion: 2,
        status: { queueVersion: 2, playing: true },
      },
    })
    httpClient.mockRejectedValueOnce(conflict)
    await expect(
      client.moveAnchor(
        {
          queueItemId: 'q-dup-3',
          anchorQueueItemId: 'q-dup-1',
          placement: 'before',
          expectedQueueVersion: 7,
        },
        scope,
      ),
    ).rejects.toMatchObject({
      code: 'QUEUE_VERSION_CONFLICT',
      status: 409,
      expectedQueueVersion: 7,
      queueVersion: 2,
      snapshot: { queueVersion: 2, playing: true },
    })
  })

  it('normalizes a 404 anchor miss into a coded error with the fresh snapshot', async () => {
    const missing = Object.assign(new Error('anchor not found'), {
      status: 404,
      body: {
        code: 'QUEUE_ANCHOR_NOT_FOUND',
        error: 'anchor not found',
        queueItemId: 'q-dup-1',
        status: { queueVersion: 2 },
      },
    })
    httpClient.mockRejectedValueOnce(missing)
    await expect(
      client.moveAnchor(
        {
          queueItemId: 'q-dup-3',
          anchorQueueItemId: 'q-dup-1',
          placement: 'before',
        },
        scope,
      ),
    ).rejects.toMatchObject({
      code: 'QUEUE_ANCHOR_NOT_FOUND',
      status: 404,
      queueItemId: 'q-dup-1',
      snapshot: { queueVersion: 2 },
    })
  })
})

describe('jukeboxClient captured identity migration', () => {
  const protocol = {
    version: 1,
    relativeMove: true,
    identitySelect: true,
    identityRemove: true,
    identityMigration: true,
    anchorVersionEnforcement: true,
    operationResults: true,
  }
  const identity = { sessionId: 'session / a', clientId: 'tab-a' }
  const snapshot = () => ({
    queue: [
      { queueItemId: 'q-first', id: 'same-media' },
      { queueItemId: 'q-middle', id: 'fallback', mediaFileId: 'preferred' },
      { queueItemId: 'q-last', id: 'same-media' },
    ],
    currentQueueItemId: 'q-last',
    position: 12.5,
    acceptedIntent: 'pause',
    playMode: 'orderLoop',
    volume: 0.2,
  })
  let client
  let serialize
  let createModule
  let createAdapter

  beforeEach(async () => {
    vi.resetModules()
    httpClient.mockReset()
    client = (await import('./jukeboxClient')).default
    const module = await import('./playbackControlModule')
    serialize = module.serializeMigrationContext
    createModule = module.createPlaybackControlModule
    createAdapter = (await import('./mockRemoteAudioAdapter'))
      .createMockRemoteAudioAdapter
    httpClient.mockResolvedValue({ json: {} })
  })

  const attach = async (extra = {}) => {
    const response = {
      ...identity,
      ownerClientId: identity.clientId,
      ownershipState: 'attached',
      bindingGeneration: 7,
      queueProtocol: protocol,
      ...extra,
    }
    httpClient.mockResolvedValueOnce({ json: response })
    return client.attachSession(response.sessionId, identity.clientId)
  }
  const captured = async () => {
    const lease = await attach()
    const scope = client.captureMigrationScope(lease)
    const operation = client.captureMigrationOperation(scope, 'mig:commit')
    httpClient.mockClear()
    return { lease, scope, operation }
  }
  const expectedScope = { ...identity, bindingGeneration: 7 }
  const request = {
    migrationId: 'migration / one',
    deviceName: 'target-device',
    targetDeviceName: 'target-device',
    expectedFingerprint: 'reserved-target-fingerprint',
    forceTakeover: true,
    sessionId: 'injected-session',
    clientId: 'injected-client',
    bindingGeneration: 99,
    expectedGeneration: 99,
    operationId: 'g99:injected',
    queue: ['legacy'],
    currentIndex: 0,
    queueItems: [],
    gain: 1,
    muted: false,
    user: 'injected-user',
  }

  const transportArguments = (method, { operation, scope }) => {
    if (method === 'commitMigration') return [snapshot(), request, operation]
    if (method === 'queryOperation') return [operation]
    if (method === 'queryReservation') return [request.migrationId, scope]
    return [request, operation]
  }

  it('serializes ordered duplicate occurrences and actual media metadata only', () => {
    expect(serialize(snapshot())).toEqual({
      queueItems: [
        { queueItemId: 'q-first', mediaFileId: 'same-media' },
        { queueItemId: 'q-middle', mediaFileId: 'preferred' },
        { queueItemId: 'q-last', mediaFileId: 'same-media' },
      ],
      currentQueueItemId: 'q-last',
      position: 13,
    })
  })

  it('serializes a real module addQueueItems snapshot without changing its IDs', async () => {
    const controller = createModule()
    controller.setAdapter(createAdapter())
    try {
      await controller.addQueueItems(snapshot().queue)
      const state = controller.getState()
      expect(serialize(state).queueItems).toEqual([
        { queueItemId: 'q-first', mediaFileId: 'same-media' },
        { queueItemId: 'q-middle', mediaFileId: 'preferred' },
        { queueItemId: 'q-last', mediaFileId: 'same-media' },
      ])
      expect(state.queue.map((item) => item.queueItemId)).toEqual([
        'q-first',
        'q-middle',
        'q-last',
      ])
    } finally {
      controller.setAdapter(null)
    }
  })

  it.each([undefined, null])(
    'omits absent current %s and position for explicit empty queues',
    (currentQueueItemId) => {
      expect(serialize({ queue: [], currentQueueItemId })).toEqual({
        queueItems: [],
      })
    },
  )

  it('preserves nonempty queues without inventing a current item', () => {
    const state = snapshot()
    delete state.currentQueueItemId
    delete state.position
    const wire = serialize(state)
    expect(wire.queueItems).toHaveLength(3)
    expect(wire).not.toHaveProperty('currentQueueItemId')
    expect(wire).not.toHaveProperty('position')
    expect(wire).not.toHaveProperty('queue')
    expect(wire).not.toHaveProperty('currentIndex')
  })

  it.each([
    [0, 0],
    [12.49, 12],
    [12.5, 13],
  ])('rounds position %s to Go-compatible integer %s', (position, expected) => {
    expect(serialize({ queue: [], position }).position).toBe(expected)
  })

  const invalidSnapshots = [
    ['missing array', {}],
    ['null array', { queue: null }],
    ['non-array', { queue: {} }],
    [
      'duplicate IDs',
      {
        queue: [
          { queueItemId: 'q', id: 'a' },
          { queueItemId: 'q', id: 'b' },
        ],
      },
    ],
    ['blank item ID', { queue: [{ queueItemId: ' ', id: 'a' }] }],
    ['missing item ID', { queue: [{ id: 'a' }] }],
    ['numeric item ID', { queue: [{ queueItemId: 1, id: 'a' }] }],
    [
      'missing media ID',
      { queue: [{ queueItemId: 'q', path: '/music/song.mp3' }] },
    ],
    ['blank media ID', { queue: [{ queueItemId: 'q', id: ' ' }] }],
    ['numeric media ID', { queue: [{ queueItemId: 'q', id: 1 }] }],
    ['missing current member', { queue: [], currentQueueItemId: 'q' }],
    ['blank current', { queue: [], currentQueueItemId: ' ' }],
    ['numeric current', { queue: [], currentQueueItemId: 1 }],
    ...[NaN, Infinity, -1, null, '1', Number.MAX_SAFE_INTEGER + 1].map(
      (position) => [`invalid position ${position}`, { queue: [], position }],
    ),
  ]
  it.each(invalidSnapshots)(
    'rejects %s before commit POST',
    async (_name, state) => {
      const { operation } = await captured()
      expect(() => serialize(state)).toThrow(
        expect.objectContaining({ code: 'MIGRATION_CONTEXT_INVALID' }),
      )
      await expect(
        client.commitMigration(state, request, operation),
      ).rejects.toMatchObject({ code: 'MIGRATION_CONTEXT_INVALID' })
      expect(httpClient).not.toHaveBeenCalled()
    },
  )

  it('freezes the capture, scope, capability and once-derived operation ID', async () => {
    const { scope, operation } = await captured()
    expect(Object.isFrozen(scope)).toBe(true)
    expect(Object.isFrozen(scope.scope)).toBe(true)
    expect(Object.isFrozen(scope.queueProtocol)).toBe(true)
    expect(Object.isFrozen(operation)).toBe(true)
    expect(scope.scope).toEqual(expectedScope)
    expect(operation.operationId).toBe('g7:mig:commit')
    expect(
      client.captureMigrationOperation(scope, 'g7:already').operationId,
    ).toBe('g7:already')
    expect(() => client.captureMigrationOperation(scope, 'g8:wrong')).toThrow(
      expect.objectContaining({ code: 'MIGRATION_OPERATION_INVALID' }),
    )
    expect(httpClient).not.toHaveBeenCalled()
  })

  it.each(['', ' ', 'g7:', null])(
    'rejects invalid stable operation ID %s without HTTP',
    async (id) => {
      const { scope } = await captured()
      expect(() => client.captureMigrationOperation(scope, id)).toThrow(
        expect.objectContaining({ code: 'MIGRATION_OPERATION_INVALID' }),
      )
      expect(httpClient).not.toHaveBeenCalled()
    },
  )

  it('requires exact current lease identity, not a copied status', async () => {
    const lease = await attach()
    httpClient.mockClear()
    expect(() => client.captureMigrationScope({ ...lease })).toThrow(
      expect.objectContaining({ code: 'MIGRATION_SCOPE_STALE' }),
    )
    expect(httpClient).not.toHaveBeenCalled()
  })

  it('rejects pre-attachment capture with zero HTTP', () => {
    expect(() => client.captureMigrationScope(null)).toThrow(
      expect.objectContaining({ code: 'MIGRATION_SCOPE_STALE' }),
    )
    expect(httpClient).not.toHaveBeenCalled()
  })

  it.each([
    null,
    { version: 1 },
    { ...protocol, version: 0 },
    { ...protocol, version: 2 },
    { ...protocol, identityMigration: false },
    { ...protocol, operationResults: false },
    { ...protocol, identityMigration: 'true' },
  ])(
    'rejects unsupported advertised capabilities %j with no migration HTTP',
    async (queueProtocol) => {
      const lease = await attach({ queueProtocol })
      httpClient.mockClear()
      expect(() => client.captureMigrationScope(lease)).toThrow(
        expect.objectContaining({ code: 'QUEUE_PROTOCOL_UNSUPPORTED' }),
      )
      await expect(
        client.commitMigration(snapshot(), request, undefined),
      ).rejects.toMatchObject({ code: 'QUEUE_PROTOCOL_UNSUPPORTED' })
      expect(httpClient).not.toHaveBeenCalled()
    },
  )

  it.each([undefined, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    'rejects invalid captured generation %s',
    async (bindingGeneration) => {
      const lease = await attach({ bindingGeneration })
      httpClient.mockClear()
      expect(() => client.captureMigrationScope(lease)).toThrow(
        expect.objectContaining({ code: 'MIGRATION_SCOPE_INVALID' }),
      )
      expect(httpClient).not.toHaveBeenCalled()
    },
  )

  it.each([
    [
      'reserveMigration',
      'migration/reserve',
      {
        migrationId: request.migrationId,
        deviceName: request.deviceName,
        forceTakeover: true,
        expectedFingerprint: request.expectedFingerprint,
      },
    ],
    [
      'releaseMigrationReservation',
      'migration/release',
      { migrationId: request.migrationId },
    ],
    [
      'commitMigration',
      'migration/commit',
      {
        migrationId: request.migrationId,
        targetDeviceName: request.targetDeviceName,
        expectedFingerprint: request.expectedFingerprint,
      },
    ],
  ])(
    'allowlists exact %s payload and returns authoritative JSON unchanged',
    async (method, path, fields) => {
      const { operation, lease } = await captured()
      const raw = {
        status: 'APPLIED',
        bindingGeneration: 8,
        ownerClientId: identity.clientId,
      }
      httpClient.mockResolvedValueOnce({ json: raw })
      const args =
        method === 'commitMigration'
          ? [snapshot(), request, operation]
          : [request, operation]
      expect(await client[method](...args)).toBe(raw)
      expect(httpClient.mock.calls[0][0]).toBe(`/api/jukebox/${path}`)
      expect(httpClient.mock.calls[0][1].method).toBe('POST')
      expect(JSON.parse(httpClient.mock.calls[0][1].body)).toEqual({
        ...fields,
        ...(method === 'commitMigration' ? serialize(snapshot()) : {}),
        ...expectedScope,
        operationId: 'g7:mig:commit',
      })
      expect(client.captureMigrationScope(lease).scope.bindingGeneration).toBe(
        7,
      )
    },
  )

  it('queries exact original wire operation ID after reattach and detach', async () => {
    const { operation, scope } = await captured()
    await attach({ bindingGeneration: 8 })
    await client.detachSession(identity.sessionId, identity.clientId)
    httpClient.mockClear()
    const raw = { status: 'APPLIED', sessionStatus: { bindingGeneration: 8 } }
    httpClient.mockResolvedValueOnce({ json: raw })
    expect(await client.queryOperation(operation)).toBe(raw)
    expect(httpClient.mock.calls[0]).toEqual([
      '/api/jukebox/operation?sessionId=session%20%2F%20a&operationId=g7%3Amig%3Acommit',
    ])
    const reservation = { status: 'ACTIVE', expectedFingerprint: 'target' }
    httpClient.mockResolvedValueOnce({ json: reservation })
    expect(await client.queryReservation('migration / one', scope)).toBe(
      reservation,
    )
    expect(httpClient.mock.calls[1]).toEqual([
      '/api/jukebox/migration/reservation?migrationId=migration%20%2F%20one',
    ])
  })

  it('rejects every stale captured write without dispatch', async () => {
    const { operation } = await captured()
    await attach({ bindingGeneration: 8 })
    httpClient.mockClear()
    for (const action of [
      () => client.reserveMigration(request, operation),
      () => client.releaseMigrationReservation(request, operation),
      () => client.commitMigration(snapshot(), request, operation),
    ]) {
      await expect(action()).rejects.toMatchObject({
        code: 'MIGRATION_SCOPE_STALE',
      })
    }
    expect(httpClient).not.toHaveBeenCalled()
  })

  it('rejects in-place lease generation changes without altering original query identity', async () => {
    const { lease, operation } = await captured()
    lease.bindingGeneration = 8
    await expect(
      client.reserveMigration(request, operation),
    ).rejects.toMatchObject({ code: 'MIGRATION_SCOPE_STALE' })
    expect(httpClient).not.toHaveBeenCalled()
    await client.queryOperation(operation)
    expect(httpClient.mock.calls[0][0]).toContain(
      'operationId=g7%3Amig%3Acommit',
    )
  })

  it('does not let late older attachment capture overwrite a newer confirmed binding', async () => {
    let completeOld
    httpClient.mockReturnValueOnce(
      new Promise((resolve) => {
        completeOld = resolve
      }),
    )
    const pending = client.attachSession('old-session', identity.clientId)
    const current = await attach({ bindingGeneration: 8 })
    completeOld({
      json: {
        ...identity,
        sessionId: 'old-session',
        ownerClientId: identity.clientId,
        ownershipState: 'attached',
        bindingGeneration: 7,
        queueProtocol: protocol,
      },
    })
    const old = await pending
    httpClient.mockClear()
    expect(() => client.captureMigrationScope(old)).toThrow(
      expect.objectContaining({ code: 'MIGRATION_SCOPE_STALE' }),
    )
    expect(client.captureMigrationScope(current).scope.bindingGeneration).toBe(
      8,
    )
    expect(httpClient).not.toHaveBeenCalled()
  })

  const migrationMethods = [
    'reserveMigration',
    'releaseMigrationReservation',
    'commitMigration',
    'queryOperation',
    'queryReservation',
  ]

  it.each(migrationMethods)(
    'forwards only the exact signal for %s outside JSON and captures',
    async (method) => {
      const values = await captured()
      const signal = new AbortController().signal
      const raw = { status: 'APPLIED' }
      httpClient.mockResolvedValueOnce({ json: raw })
      const args = transportArguments(method, values)
      expect(
        await client[method](...args, {
          signal,
          headers: { ignored: 'yes' },
          operationId: 'g99:ignored',
        }),
      ).toBe(raw)
      expect(httpClient).toHaveBeenCalledTimes(1)
      const [, options] = httpClient.mock.calls[0]
      expect(options.signal).toBe(signal)
      expect(options).not.toHaveProperty('headers')
      expect(options).not.toHaveProperty('operationId')
      expect(values.operation).not.toHaveProperty('signal')
      expect(values.scope).not.toHaveProperty('signal')
      expect(values.scope.scope).not.toHaveProperty('signal')
      if (options.body) {
        const body = JSON.parse(options.body)
        expect(body).not.toHaveProperty('signal')
        expect(body.operationId).toBe('g7:mig:commit')
      } else {
        expect(options).toEqual({ signal })
      }
    },
  )

  it.each(migrationMethods)(
    'propagates raw aborted %s rejection without retry or binding promotion',
    async (method) => {
      const values = await captured()
      const abort = new AbortController()
      abort.abort()
      const error = new DOMException('Request aborted', 'AbortError')
      httpClient.mockRejectedValueOnce(error)
      const args = transportArguments(method, values)
      await expect(
        client[method](...args, { signal: abort.signal }),
      ).rejects.toBe(error)
      expect(httpClient).toHaveBeenCalledTimes(1)
      expect(httpClient.mock.calls[0][1].signal).toBe(abort.signal)
      expect(values.operation.operationId).toBe('g7:mig:commit')
      expect(
        client.captureMigrationScope(values.lease).scope.bindingGeneration,
      ).toBe(7)
      expect(error).not.toHaveProperty('status', 'NOT_EXECUTED')
    },
  )

  it.each(migrationMethods)(
    'normalizes coded %s errors without changing binding',
    async (method) => {
      const { lease, operation, scope } = await captured()
      httpClient.mockRejectedValueOnce({
        status: 409,
        body: { code: 'MIGRATION_CONFLICT', error: 'target changed' },
      })
      const args = transportArguments(method, { operation, scope })
      await expect(client[method](...args)).rejects.toMatchObject({
        code: 'MIGRATION_CONFLICT',
        message: 'target changed',
        status: 409,
      })
      expect(client.captureMigrationScope(lease).scope.bindingGeneration).toBe(
        7,
      )
    },
  )
})
