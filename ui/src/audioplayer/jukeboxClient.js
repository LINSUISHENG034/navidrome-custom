import httpClient from '../dataProvider/httpClient'

let attachedScope = null
let attachmentVersion = 0

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
    return post('session/attach', {
      sessionId,
      clientId,
      ...(deviceName ? { deviceName } : {}),
      ...(options.takeover === true ? { takeover: true } : {}),
    }).then((status) => {
      if (
        version === attachmentVersion &&
        status.sessionId === sessionId &&
        status.ownerClientId === clientId &&
        status.ownershipState === 'attached'
      ) {
        attachedScope = { sessionId, clientId }
      }
      return status
    })
  },

  heartbeatSession: (sessionId, clientId) =>
    post('session/heartbeat', { sessionId, clientId }),

  detachSession: (sessionId, clientId, options = {}) => {
    const retiringScope = attachedScope
    return post('session/detach', { sessionId, clientId }, options).then(
      (status) => {
        if (
          attachedScope === retiringScope &&
          attachedScope?.sessionId === sessionId &&
          attachedScope?.clientId === clientId
        ) {
          attachedScope = null
        }
        return status
      },
    )
  },

  set: (ids, scope) => command('set', { ids }, scope),
  start: (scope) => command('start', {}, scope),
  play: (scope) => command('play', {}, scope),
  stop: (scope) => command('stop', {}, scope),
  pause: (scope) => command('pause', {}, scope),
  skip: (index, offset = 0, scope) => command('skip', { index, offset }, scope),
  seek: (position, scope) => command('seek', { position }, scope),
  volume: (gain, scope) => command('volume', { gain }, scope),
  add: (ids, index = null, scope) =>
    command('add', index === null ? { ids } : { ids, index }, scope),
  remove: (index, scope) => command('remove', { index }, scope),
  move: (from, to, scope) => command('move', { from, to }, scope),
  switchDevice: (deviceName, scope) =>
    command('devices/switch', { deviceName }, scope),
}

export default jukeboxClient
