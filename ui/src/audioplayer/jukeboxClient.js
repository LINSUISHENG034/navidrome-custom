import httpClient from '../dataProvider/httpClient'

const jukeboxClient = {
  status: () => httpClient('/api/jukebox/status').then(({ json }) => json),


  attachSession: (sessionId, clientId, deviceName = null) =>
    httpClient('/api/jukebox/session/attach', {
      method: 'POST',
      body: JSON.stringify(
        deviceName ? { sessionId, clientId, deviceName } : { sessionId, clientId },
      ),
    }).then(({ json }) => json),

  heartbeatSession: (sessionId, clientId) =>
    httpClient('/api/jukebox/session/heartbeat', {
      method: 'POST',
      body: JSON.stringify({ sessionId, clientId }),
    }).then(({ json }) => json),

  detachSession: (sessionId, clientId) =>
    httpClient('/api/jukebox/session/detach', {
      method: 'POST',
      body: JSON.stringify({ sessionId, clientId }),
    }).then(({ json }) => json),

  set: (ids) =>
    httpClient('/api/jukebox/set', {
      method: 'POST',
      body: JSON.stringify({ ids }),
    }).then(({ json }) => json),

  start: () =>
    httpClient('/api/jukebox/start', { method: 'POST' }).then(
      ({ json }) => json,
    ),

  play: () =>
    httpClient('/api/jukebox/play', { method: 'POST' }).then(
      ({ json }) => json,
    ),

  stop: () =>
    httpClient('/api/jukebox/stop', { method: 'POST' }).then(
      ({ json }) => json,
    ),

  pause: () =>
    httpClient('/api/jukebox/pause', { method: 'POST' }).then(
      ({ json }) => json,
    ),

  skip: (index, offset = 0) =>
    httpClient('/api/jukebox/skip', {
      method: 'POST',
      body: JSON.stringify({ index, offset }),
    }).then(({ json }) => json),

  seek: (position) =>
    httpClient('/api/jukebox/seek', {
      method: 'POST',
      body: JSON.stringify({ position }),
    }).then(({ json }) => json),

  volume: (gain) =>
    httpClient('/api/jukebox/volume', {
      method: 'POST',
      body: JSON.stringify({ gain }),
    }).then(({ json }) => json),

  add: (ids, index = null) =>
    httpClient('/api/jukebox/add', {
      method: 'POST',
      body: JSON.stringify(index === null ? { ids } : { ids, index }),
    }).then(({ json }) => json),

  remove: (index) =>
    httpClient('/api/jukebox/remove', {
      method: 'POST',
      body: JSON.stringify({ index }),
    }).then(({ json }) => json),

  move: (from, to) =>
    httpClient('/api/jukebox/move', {
      method: 'POST',
      body: JSON.stringify({ from, to }),
    }).then(({ json }) => json),
}

export default jukeboxClient
