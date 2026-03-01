import httpClient from '../dataProvider/httpClient'

const jukeboxClient = {
  status: () => httpClient('/api/jukebox/status').then(({ json }) => json),

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
}

export default jukeboxClient
