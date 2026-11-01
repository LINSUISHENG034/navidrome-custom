import httpClient from '../dataProvider/httpClient'
import {
  normalizeQueueProtocol,
  resolveQueueProtocol,
  serializeMigrationContext,
} from './playbackControlModule'

// Public re-export kept for compatibility; the single pure normalizer lives
// in playbackControlModule alongside the module capability gate.
export {
  normalizeQueueProtocol,
  SUPPORTED_QUEUE_PROTOCOL_VERSION,
} from './playbackControlModule'

let attachedScope = null
let attachedLease = null
let attachmentVersion = 0
let confirmedAttachmentVersion = 0
const sessionTransitions = new Map()

// The queue protocol capability is bound to the currently confirmed
// attachment (session/client identity). It is replaced only by a newer
// confirmed attach and cleared on detach, so a stale channel can never
// reinstate capabilities over a newer binding.
let boundQueueProtocol = null

// Canonical shared queue protocol contract. Version is an integer; clients
// must check exact version and boolean values, never field presence.
// normalizeQueueProtocol (re-exported above) returns a validated capability
// copy or null. Anything that is not the exact supported shape with enforced
// boolean values is unsupported, including future versions and mere field
// presence.

// normalizeJukeboxHttpError maps coded jukebox error bodies onto rejected
// command errors, carrying the fresh authoritative snapshot when the server
// included one (409 version conflicts, 404 item/anchor misses).
function normalizeJukeboxHttpError(err) {
  const body = err?.body
  if (body && typeof body === 'object' && typeof body.code === 'string') {
    return {
      code: body.code,
      message: body.error || err.message,
      status: err.status,
      snapshot: body.status,
      queueItemId: body.queueItemId,
      expectedQueueVersion: body.expectedQueueVersion,
      queueVersion: body.queueVersion,
      requiresNewGeneration: body.requiresNewGeneration,
    }
  }
  return err
}

const transitionSession = (sessionId, clientId, operation) => {
  const key = JSON.stringify([sessionId, clientId])
  const previous = sessionTransitions.get(key)
  const pending = (previous || Promise.resolve())
    .catch(() => {})
    .then(operation)
  sessionTransitions.set(key, pending)
  pending
    .finally(() => {
      if (sessionTransitions.get(key) === pending)
        sessionTransitions.delete(key)
    })
    .catch(() => {})
  return pending
}

const post = (path, payload = {}, options = {}) =>
  httpClient(`/api/jukebox/${path}`, {
    ...options,
    method: 'POST',
    ...(Object.keys(payload).length ? { body: JSON.stringify(payload) } : {}),
  }).then(({ json }) => json)

const command = (path, payload = {}, scope = attachedScope) =>
  post(path, { ...payload, ...scope })

const migrationError = (code, message) => ({ code, message })

const migrationProtocol = (raw) =>
  resolveQueueProtocol({ getQueueProtocol: () => raw }, [
    'identityMigration',
    'operationResults',
  ])

const requireMigrationCapture = (capture, current = false) => {
  const scope = capture?.scope
  if (!migrationProtocol(capture?.queueProtocol)) {
    throw migrationError(
      'QUEUE_PROTOCOL_UNSUPPORTED',
      'Identity migration requires queue protocol v1 and operation results',
    )
  }
  if (
    !scope ||
    typeof scope.sessionId !== 'string' ||
    !scope.sessionId.trim() ||
    typeof scope.clientId !== 'string' ||
    !scope.clientId.trim() ||
    !Number.isSafeInteger(scope.bindingGeneration) ||
    scope.bindingGeneration <= 0
  ) {
    throw migrationError(
      'MIGRATION_SCOPE_INVALID',
      'Invalid captured migration scope',
    )
  }
  if (
    current &&
    (capture.attachment !== attachedLease ||
      capture.attachmentVersion !== confirmedAttachmentVersion ||
      attachedScope?.sessionId !== scope.sessionId ||
      attachedScope?.clientId !== scope.clientId ||
      attachedLease?.sessionId !== scope.sessionId ||
      attachedLease?.ownerClientId !== scope.clientId ||
      attachedLease?.ownershipState !== 'attached' ||
      attachedLease?.bindingGeneration !== scope.bindingGeneration ||
      !migrationProtocol(boundQueueProtocol))
  ) {
    throw migrationError(
      'MIGRATION_SCOPE_STALE',
      'Captured migration attachment changed',
    )
  }
  return scope
}

const requireMigrationOperation = (operation, current = false) => {
  const scope = requireMigrationCapture(operation?.capture, current)
  if (
    typeof operation.operationId !== 'string' ||
    !operation.operationId.startsWith(`g${scope.bindingGeneration}:`) ||
    !operation.operationId.slice(operation.operationId.indexOf(':') + 1).trim()
  ) {
    throw migrationError(
      'MIGRATION_OPERATION_INVALID',
      'Invalid captured operation ID',
    )
  }
  return scope
}

const migrationFields = (request, required, optional = []) => {
  const result = {}
  for (const key of required) {
    if (typeof request?.[key] !== 'string' || !request[key].trim()) {
      throw migrationError('MIGRATION_REQUEST_INVALID', `${key} is required`)
    }
    result[key] = request[key]
  }
  for (const key of optional) {
    if (request?.[key] !== undefined) result[key] = request[key]
  }
  return result
}

const migrationRequestOptions = (requestOptions) =>
  requestOptions?.signal === undefined ? {} : { signal: requestOptions.signal }

const migrationPost = async (path, payload, operation, requestOptions) => {
  const scope = requireMigrationOperation(operation, true)
  try {
    return await post(
      path,
      { ...payload, ...scope, operationId: operation.operationId },
      migrationRequestOptions(requestOptions),
    )
  } catch (err) {
    throw normalizeJukeboxHttpError(err)
  }
}

const migrationGet = async (path, query, requestOptions) => {
  const encoded = Object.entries(query)
    .map(([key, value]) => `${key}=${encodeURIComponent(value)}`)
    .join('&')
  const url = `/api/jukebox/${path}?${encoded}`
  const options = migrationRequestOptions(requestOptions)
  try {
    const response =
      options.signal === undefined
        ? await httpClient(url)
        : await httpClient(url, options)
    return response.json
  } catch (err) {
    throw normalizeJukeboxHttpError(err)
  }
}

const jukeboxClient = {
  // Captures are local preflight evidence, not proof of server lease authority.
  captureMigrationScope: (attachment) => {
    if (!attachment || attachment !== attachedLease) {
      throw migrationError(
        'MIGRATION_SCOPE_STALE',
        'A current confirmed attachment is required',
      )
    }
    const protocol = migrationProtocol(boundQueueProtocol)
    const capture = Object.freeze({
      attachment,
      attachmentVersion: confirmedAttachmentVersion,
      scope: Object.freeze({
        sessionId: attachedScope?.sessionId,
        clientId: attachedScope?.clientId,
        bindingGeneration: attachment.bindingGeneration,
      }),
      queueProtocol: protocol ? Object.freeze(protocol) : null,
    })
    requireMigrationCapture(capture, true)
    return capture
  },
  captureMigrationOperation: (capture, stableId) => {
    const scope = requireMigrationCapture(capture, true)
    if (typeof stableId !== 'string' || !stableId.trim()) {
      throw migrationError(
        'MIGRATION_OPERATION_INVALID',
        'A stable operation ID is required',
      )
    }
    const prefix = `g${scope.bindingGeneration}:`
    const prefixed = /^g[0-9]+:/.test(stableId)
    if (prefixed && !stableId.startsWith(prefix)) {
      throw migrationError(
        'MIGRATION_OPERATION_INVALID',
        'Operation generation does not match captured scope',
      )
    }
    const operation = Object.freeze({
      capture,
      operationId: prefixed ? stableId : `${prefix}${stableId}`,
    })
    requireMigrationOperation(operation, true)
    return operation
  },
  reserveMigration: async (request, operation, requestOptions) =>
    migrationPost(
      'migration/reserve',
      migrationFields(
        request,
        ['migrationId', 'deviceName'],
        ['forceTakeover', 'expectedFingerprint'],
      ),
      operation,
      requestOptions,
    ),
  releaseMigrationReservation: async (request, operation, requestOptions) =>
    migrationPost(
      'migration/release',
      migrationFields(request, ['migrationId']),
      operation,
      requestOptions,
    ),
  commitMigration: async (snapshot, request, operation, requestOptions) =>
    migrationPost(
      'migration/commit',
      {
        ...migrationFields(
          request,
          ['migrationId', 'targetDeviceName'],
          ['expectedFingerprint'],
        ),
        ...serializeMigrationContext(snapshot),
      },
      operation,
      requestOptions,
    ),
  queryReservation: async (migrationId, capture, requestOptions) => {
    requireMigrationCapture(capture)
    const query = migrationFields({ migrationId }, ['migrationId'])
    return migrationGet('migration/reservation', query, requestOptions)
  },
  queryOperation: async (operation, requestOptions) => {
    const scope = requireMigrationOperation(operation)
    return migrationGet(
      'operation',
      {
        sessionId: scope.sessionId,
        operationId: operation.operationId,
      },
      requestOptions,
    )
  },

  sessionStatus: (sessionId) =>
    httpClient(
      `/api/jukebox/session/status?sessionId=${encodeURIComponent(sessionId)}`,
    ).then(({ json }) => json),

  sessionSnapshot: (sessionId) =>
    httpClient(
      `/api/jukebox/session/snapshot?sessionId=${encodeURIComponent(sessionId)}`,
    ).then(({ json }) => json),

  attachSession: (sessionId, clientId, deviceName = null, options = {}) => {
    const version = ++attachmentVersion
    return transitionSession(sessionId, clientId, () =>
      post('session/attach', {
        sessionId,
        clientId,
        ...(deviceName ? { deviceName } : {}),
        ...(options.takeover === true ? { takeover: true } : {}),
      }).then((status) => {
        if (
          version >= confirmedAttachmentVersion &&
          status.sessionId === sessionId &&
          status.ownerClientId === clientId &&
          status.ownershipState === 'attached'
        ) {
          confirmedAttachmentVersion = version
          attachedScope = { sessionId, clientId }
          attachedLease = status
          boundQueueProtocol = normalizeQueueProtocol(status.queueProtocol)
        }
        return status
      }),
    )
  },

  heartbeatSession: (sessionId, clientId) =>
    post('session/heartbeat', { sessionId, clientId }),

  detachSession: (sessionId, clientId, options = {}) =>
    transitionSession(sessionId, clientId, () => {
      const { attachment, ...requestOptions } = options
      if (attachment && attachment !== attachedLease) return null
      const retiringScope = attachedScope
      return post(
        'session/detach',
        { sessionId, clientId },
        requestOptions,
      ).then((status) => {
        if (
          attachedScope === retiringScope &&
          attachedScope?.sessionId === sessionId &&
          attachedScope?.clientId === clientId
        ) {
          attachedScope = null
          attachedLease = null
          boundQueueProtocol = null
        }
        return status
      })
    }),

  set: (ids, scope) => command('set', { ids }, scope),
  start: (scope) => command('start', {}, scope),
  play: (scope) => command('play', {}, scope),
  stop: (scope) => command('stop', {}, scope),
  pause: (scope) => command('pause', {}, scope),
  skip: (index, offset = 0, scope) => command('skip', { index, offset }, scope),
  seek: (position, scope) => command('seek', { position }, scope),
  volume: (gain, scope) => command('volume', { gain }, scope),
  add: (ids, index = null, scope) =>
    command(
      'add',
      index === null || index === -1 ? { ids } : { ids, index },
      scope,
    ),
  remove: (index, scope) => command('remove', { index }, scope),
  move: (from, to, scope) => command('move', { from, to }, scope),
  // Identity/anchor queue transports (T13). expectedQueueVersion is omitted
  // when the caller captured none; legacy index fields are never sent.
  moveAnchor: (
    { queueItemId, anchorQueueItemId, placement, expectedQueueVersion } = {},
    scope,
  ) =>
    command(
      'move',
      {
        queueItemId,
        anchorQueueItemId,
        placement,
        ...(expectedQueueVersion !== undefined ? { expectedQueueVersion } : {}),
      },
      scope,
    ).catch((err) => {
      throw normalizeJukeboxHttpError(err)
    }),
  removeItem: (queueItemId, scope) =>
    command('remove', { queueItemId }, scope).catch((err) => {
      throw normalizeJukeboxHttpError(err)
    }),
  skipToItem: (queueItemId, scope) =>
    command('skip', { queueItemId }, scope).catch((err) => {
      throw normalizeJukeboxHttpError(err)
    }),
  getQueueProtocol: () =>
    boundQueueProtocol ? { ...boundQueueProtocol } : null,
  switchDevice: (deviceName, scope) =>
    command('devices/switch', { deviceName }, scope),
}

export default jukeboxClient
