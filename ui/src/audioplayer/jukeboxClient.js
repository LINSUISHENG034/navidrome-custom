import httpClient from '../dataProvider/httpClient'
import { normalizeQueueProtocol } from './playbackControlModule'

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

const jukeboxClient = {
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
