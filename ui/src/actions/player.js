export const PLAYER_ADD_TRACKS = 'PLAYER_ADD_TRACKS'
export const PLAYER_PLAY_NEXT = 'PLAYER_PLAY_NEXT'
export const PLAYER_SET_TRACK = 'PLAYER_SET_TRACK'
export const PLAYER_SYNC_QUEUE = 'PLAYER_SYNC_QUEUE'
export const PLAYER_CLEAR_QUEUE = 'PLAYER_CLEAR_QUEUE'
export const PLAYER_PLAY_TRACKS = 'PLAYER_PLAY_TRACKS'
export const PLAYER_CURRENT = 'PLAYER_CURRENT'
export const PLAYER_SET_VOLUME = 'PLAYER_SET_VOLUME'
export const PLAYER_SET_MODE = 'PLAYER_SET_MODE'
export const TRANSCODING_SET_PROFILE = 'TRANSCODING_SET_PROFILE'
export const PLAYER_REFRESH_QUEUE = 'PLAYER_REFRESH_QUEUE'
export const PLAYER_SET_JUKEBOX_MODE = 'PLAYER_SET_JUKEBOX_MODE'
export const PLAYER_JUKEBOX_SESSION_STATUS = 'jukeboxStateUpdated'
export const PLAYER_SET_AUDIO_INSTANCE = 'PLAYER_SET_AUDIO_INSTANCE'
export const PLAYER_JUKEBOX_EPISODE_BEGIN = 'PLAYER_JUKEBOX_EPISODE_BEGIN'
export const PLAYER_JUKEBOX_EPISODE_END = 'PLAYER_JUKEBOX_EPISODE_END'
export const PLAYER_JUKEBOX_EPISODE_DISABLE = 'PLAYER_JUKEBOX_EPISODE_DISABLE'

export const setTrack = (data) => ({
  type: PLAYER_SET_TRACK,
  data,
})

export const filterSongs = (data, ids) => {
  const filteredData = Object.fromEntries(
    Object.entries(data).filter(([_, song]) => !song.missing),
  )
  return !ids
    ? filteredData
    : ids.reduce((acc, id) => {
        if (filteredData[id]) {
          return { ...acc, [id]: filteredData[id] }
        }
        return acc
      }, {})
}

export const addTracks = (data, ids) => {
  const songs = filterSongs(data, ids)
  return {
    type: PLAYER_ADD_TRACKS,
    data: songs,
  }
}

export const playNext = (data, ids) => {
  const songs = filterSongs(data, ids)
  return {
    type: PLAYER_PLAY_NEXT,
    data: songs,
  }
}

export const shuffle = (data) => {
  const ids = Object.keys(data)
  for (let i = ids.length - 1; i > 0; i--) {
    let j = Math.floor(Math.random() * (i + 1))
    ;[ids[i], ids[j]] = [ids[j], ids[i]]
  }
  const shuffled = {}
  // The "_" is to force the object key to be a string, so it keeps the order when adding to object
  // or else the keys will always be in the same (numerically) order
  ids.forEach((id) => (shuffled['_' + id] = data[id]))
  return shuffled
}

export const shuffleTracks = (data, ids) => {
  const songs = filterSongs(data, ids)
  const shuffled = shuffle(songs)
  const firstId = Object.keys(shuffled)[0]
  return {
    type: PLAYER_PLAY_TRACKS,
    id: firstId,
    data: shuffled,
  }
}

export const playTracks = (data, ids, selectedId) => {
  const songs = filterSongs(data, ids)
  return {
    type: PLAYER_PLAY_TRACKS,
    id: selectedId || Object.keys(songs)[0],
    data: songs,
  }
}

export const syncQueue = (audioInfo, audioLists) => ({
  type: PLAYER_SYNC_QUEUE,
  data: {
    audioInfo,
    audioLists,
  },
})

export const clearQueue = () => ({
  type: PLAYER_CLEAR_QUEUE,
})

export const currentPlaying = (audioInfo) => ({
  type: PLAYER_CURRENT,
  data: audioInfo,
})

export const setVolume = (volume) => ({
  type: PLAYER_SET_VOLUME,
  data: { volume },
})

export const setPlayMode = (mode) => ({
  type: PLAYER_SET_MODE,
  data: { mode },
})

export const setTranscodingProfile = (profile) => ({
  type: TRANSCODING_SET_PROFILE,
  data: profile,
})

export const refreshQueue = (resolvedUrls) => ({
  type: PLAYER_REFRESH_QUEUE,
  data: resolvedUrls,
})

export const setJukeboxMode = (enabled, device = null) => ({
  type: PLAYER_SET_JUKEBOX_MODE,
  data: { enabled, device },
})

export const setAudioInstance = (instance) => ({
  type: PLAYER_SET_AUDIO_INSTANCE,
  data: instance,
})

// Provenance is local action metadata, never a server payload field: only
// the atomic session-snapshot boundary inside the trusted controller
// episode passes the atomic-session-snapshot meta.
export const updateJukeboxSessionStatus = (status, meta) => ({
  type: PLAYER_JUKEBOX_SESSION_STATUS,
  data: status,
  ...(meta ? { meta } : {}),
})

// Episode lifecycle, reducer-owned: the reducer is the single registry of
// live control episodes. BEGIN registers a fresh token (disabled until its
// first reconcile); END retires only the matching token synchronously before
// async attachment retirement; DISABLE invalidates only the matching
// reconciliation and bumps its revision so in-flight responses captured at
// an older revision can never regrant.
export const beginJukeboxEpisode = ({
  token,
  sessionId,
  clientId,
  deviceName = null,
}) => ({
  type: PLAYER_JUKEBOX_EPISODE_BEGIN,
  data: { token, sessionId, clientId, deviceName },
})

export const endJukeboxEpisode = ({ token }) => ({
  type: PLAYER_JUKEBOX_EPISODE_END,
  data: { token },
})

export const disableJukeboxEpisode = ({ token }) => ({
  type: PLAYER_JUKEBOX_EPISODE_DISABLE,
  data: { token },
})
