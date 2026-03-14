import { v4 as uuidv4 } from 'uuid'
import subsonic from '../subsonic'
import {
  PLAYER_ADD_TRACKS,
  PLAYER_CLEAR_QUEUE,
  PLAYER_CURRENT,
  PLAYER_PLAY_NEXT,
  PLAYER_PLAY_TRACKS,
  PLAYER_SET_TRACK,
  PLAYER_SET_VOLUME,
  PLAYER_SYNC_QUEUE,
  PLAYER_SET_MODE,
  PLAYER_SET_JUKEBOX_MODE,
  PLAYER_JUKEBOX_SESSION_STATUS,
  PLAYER_SET_AUDIO_INSTANCE,
} from '../actions'
import config from '../config'
import { audioVolumeToUiVolume } from '../audioplayer/volumeMapping'

const initialState = {
  queue: [],
  current: {},
  clear: false,
  volume: config.defaultUIVolume / 100,
  savedPlayIndex: 0,
  jukeboxMode: false,
  jukeboxDevice: null,
  jukeboxControl: null,
  jukeboxRemote: null,
  audioInstance: null,
}

const pad = (value) => {
  const str = value.toString()
  if (str.length === 1) {
    return `0${str}`
  } else {
    return str
  }
}

const mapToAudioLists = (item) => {
  // If item comes from a playlist, trackId is mediaFileId
  const trackId = item.mediaFileId || item.id

  if (item.isRadio) {
    return {
      trackId,
      uuid: uuidv4(),
      name: item.name,
      song: item,
      musicSrc: item.streamUrl,
      cover: item.cover,
      isRadio: true,
    }
  }

  const { lyrics } = item
  let lyricText = ''

  if (lyrics) {
    const structured = JSON.parse(lyrics)
    for (const structuredLyric of structured) {
      if (structuredLyric.synced) {
        for (const line of structuredLyric.line) {
          let time = Math.floor(line.start / 10)
          const ms = time % 100
          time = Math.floor(time / 100)
          const sec = time % 60
          time = Math.floor(time / 60)
          const min = time % 60

          ms.toString()
          lyricText += `[${pad(min)}:${pad(sec)}.${pad(ms)}] ${line.value}\n`
        }
      }
    }
  }

  return {
    trackId,
    uuid: uuidv4(),
    song: item,
    name: item.title,
    lyric: lyricText,
    singer: item.artist,
    duration: item.duration,
    musicSrc: subsonic.streamUrl(trackId),
    cover: subsonic.getCoverArtUrl(
      {
        id: trackId,
        updatedAt: item.updatedAt,
        album: item.album,
      },
      300,
    ),
  }
}

const reduceClearQueue = () => ({ ...initialState, clear: true })

const reducePlayTracks = (state, { data, id }) => {
  let playIndex = 0
  const queue = Object.keys(data).map((key, idx) => {
    if (key === id) {
      playIndex = idx
    }
    return mapToAudioLists(data[key])
  })
  return {
    ...state,
    queue,
    playIndex,
    clear: true,
  }
}

const reduceSetTrack = (state, { data }) => {
  return {
    ...state,
    queue: [mapToAudioLists(data)],
    playIndex: 0,
    clear: true,
  }
}

const reduceAddTracks = (state, { data }) => {
  const appended = Object.keys(data).map((id) => mapToAudioLists(data[id]))
  return {
    ...state,
    queue: [...state.queue, ...appended],
    clear: false,
  }
}

const reducePlayNext = (state, { data }) => {
  const newQueue = []
  const current = state.current || {}
  let foundPos = false
  let currentIndex = 0
  state.queue.forEach((item) => {
    newQueue.push(item)
    if (item.uuid === current.uuid) {
      foundPos = true
      currentIndex = newQueue.length - 1
      Object.keys(data).forEach((id) => {
        newQueue.push(mapToAudioLists(data[id]))
      })
    }
  })
  if (!foundPos) {
    Object.keys(data).forEach((id) => {
      newQueue.push(mapToAudioLists(data[id]))
    })
  }

  return {
    ...state,
    queue: newQueue,
    playIndex: foundPos ? currentIndex : undefined,
    clear: true,
  }
}

const reduceSetVolume = (state, { data: { volume } }) => {
  return {
    ...state,
    volume,
  }
}

const reduceSyncQueue = (state, { data: { audioInfo, audioLists } }) => {
  return {
    ...state,
    queue: audioLists.map((item) => ({ ...item })),
    clear: false,
    playIndex: undefined,
  }
}

const reduceCurrent = (state, { data }) => {
  const current = data.ended ? {} : data
  const savedPlayIndex = state.queue.findIndex(
    (item) => item.uuid === current.uuid,
  )
  return {
    ...state,
    current,
    playIndex: undefined,
    savedPlayIndex,
    volume: data.volume,
  }
}

const reduceMode = (state, { data: { mode } }) => {
  return {
    ...state,
    mode,
  }
}

const buildJukeboxControlState = (previousControl, nextStatus) => ({
  sessionId: nextStatus.sessionId ?? previousControl.sessionId ?? null,
  ownerClientId:
    nextStatus.ownerClientId ?? previousControl.ownerClientId ?? null,
  ownershipState:
    nextStatus.ownershipState ?? previousControl.ownershipState ?? 'attached',
  terminationReason:
    nextStatus.terminationReason !== undefined
      ? nextStatus.terminationReason
      : nextStatus.ownershipState === 'attached'
        ? null
        : previousControl.terminationReason ?? null,
  lastHeartbeat: nextStatus.lastHeartbeat ?? previousControl.lastHeartbeat ?? null,
  staleSince: nextStatus.staleSince ?? previousControl.staleSince ?? null,
})

const buildJukeboxRemoteState = (previousRemote, nextStatus) => ({
  currentIndex: nextStatus.currentIndex ?? previousRemote.currentIndex ?? null,
  trackId: nextStatus.trackId ?? previousRemote.trackId ?? null,
  playing: nextStatus.playing ?? previousRemote.playing ?? false,
  position: nextStatus.position ?? previousRemote.position ?? 0,
  gain: nextStatus.gain ?? previousRemote.gain ?? 0.5,
  queueVersion: nextStatus.queueVersion ?? previousRemote.queueVersion ?? null,
  attached: nextStatus.attached ?? previousRemote.attached ?? false,
  deviceName: nextStatus.deviceName ?? previousRemote.deviceName ?? null,
})

const reduceJukeboxSessionStatus = (previousState, payload) => {
  const previousControl = previousState.jukeboxControl || {}
  const previousRemote = previousState.jukeboxRemote || {}
  const nextStatus = payload.data || {}
  const nextControl = buildJukeboxControlState(previousControl, nextStatus)
  const nextRemote = buildJukeboxRemoteState(previousRemote, nextStatus)

  return {
    ...previousState,
    jukeboxControl: nextControl,
    jukeboxRemote: nextRemote,
    volume:
      previousState.jukeboxMode && typeof nextStatus?.gain === 'number'
        ? audioVolumeToUiVolume(nextStatus.gain)
        : previousState.volume,
  }
}

export const playerReducer = (previousState = initialState, payload) => {
  const { type } = payload
  switch (type) {
    case PLAYER_CLEAR_QUEUE:
      return reduceClearQueue()
    case PLAYER_PLAY_TRACKS:
      return reducePlayTracks(previousState, payload)
    case PLAYER_SET_TRACK:
      return reduceSetTrack(previousState, payload)
    case PLAYER_ADD_TRACKS:
      return reduceAddTracks(previousState, payload)
    case PLAYER_PLAY_NEXT:
      return reducePlayNext(previousState, payload)
    case PLAYER_SET_VOLUME:
      return reduceSetVolume(previousState, payload)
    case PLAYER_SYNC_QUEUE:
      return reduceSyncQueue(previousState, payload)
    case PLAYER_CURRENT:
      return reduceCurrent(previousState, payload)
    case PLAYER_SET_MODE:
      return reduceMode(previousState, payload)
    case PLAYER_SET_JUKEBOX_MODE:
      return {
        ...previousState,
        jukeboxMode: payload.data.enabled,
        jukeboxDevice: payload.data.device,
        jukeboxControl: payload.data.enabled
          ? previousState.jukeboxControl
          : null,
        jukeboxRemote: payload.data.enabled ? previousState.jukeboxRemote : null,
      }
    case PLAYER_JUKEBOX_SESSION_STATUS:
      return reduceJukeboxSessionStatus(previousState, payload)
    case PLAYER_SET_AUDIO_INSTANCE:
      return {
        ...previousState,
        audioInstance: payload.data,
      }
    default:
      return previousState
  }
}
