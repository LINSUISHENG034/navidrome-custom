import { all, call, select, takeEvery } from 'redux-saga/effects'
import {
  PLAYER_ADD_TRACKS,
  PLAYER_CLEAR_QUEUE,
  PLAYER_PLAY_NEXT,
  PLAYER_PLAY_TRACKS,
  PLAYER_SET_TRACK,
} from '../actions'
import jukeboxClient from './jukeboxClient'
import {
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
} from './jukeboxSession'

export const extractTrackIds = (data) => {
  if (!data) return []
  if (Array.isArray(data)) {
    return data.map((item) => item?.mediaFileId || item?.id).filter(Boolean)
  }
  if (data.id || data.mediaFileId) {
    return [data.mediaFileId || data.id]
  }
  if (typeof data === 'object') {
    return Object.entries(data)
      .map(
        ([key, item]) => item?.mediaFileId || item?.id || key.replace(/^_/, ''),
      )
      .filter(Boolean)
  }
  return []
}

const getScope = () => ({
  sessionId: getJukeboxSessionId(),
  clientId: getOrCreateJukeboxClientId(),
})

export function* handlePlayTracks(action) {
  const state = yield select()
  if (!state?.player?.jukeboxMode) return

  const trackIds = extractTrackIds(action.data)
  if (trackIds.length === 0) return

  const cleanSelectedId = (action.id || '').toString().replace(/^_/, '')
  const selectedIndex = trackIds.findIndex((id) => id === cleanSelectedId)
  const playIndex = selectedIndex >= 0 ? selectedIndex : 0
  const scope = getScope()

  try {
    yield call(jukeboxClient.set, trackIds, scope)
    yield call(jukeboxClient.skip, playIndex, 0, scope)
    yield call(jukeboxClient.play, scope)
  } catch (err) {
    // Handled
  }
}

export function* handleSetTrack(action) {
  const state = yield select()
  if (!state?.player?.jukeboxMode) return

  const trackIds = extractTrackIds(action.data)
  if (trackIds.length === 0) return

  const scope = getScope()
  try {
    yield call(jukeboxClient.set, trackIds, scope)
    yield call(jukeboxClient.skip, 0, 0, scope)
    yield call(jukeboxClient.play, scope)
  } catch (err) {
    // Handled
  }
}

export function* handlePlayNext(action) {
  const state = yield select()
  if (!state?.player?.jukeboxMode) return

  const trackIds = extractTrackIds(action.data)
  if (trackIds.length === 0) return

  const currentIndex =
    state.player?.jukeboxRemote?.currentIndex ??
    state.player?.savedPlayIndex ??
    0
  const scope = getScope()

  try {
    yield call(jukeboxClient.add, trackIds, currentIndex + 1, scope)
  } catch (err) {
    // Handled
  }
}

export function* handleAddTracks(action) {
  const state = yield select()
  if (!state?.player?.jukeboxMode) return

  const trackIds = extractTrackIds(action.data)
  if (trackIds.length === 0) return

  const scope = getScope()
  try {
    yield call(jukeboxClient.add, trackIds, -1, scope)
  } catch (err) {
    // Handled
  }
}

export function* handleClearQueue() {
  const state = yield select()
  if (!state?.player?.jukeboxMode) return

  const scope = getScope()
  try {
    yield call(jukeboxClient.stop, scope)
    yield call(jukeboxClient.set, [], scope)
  } catch (err) {
    // Handled
  }
}

export default function* jukeboxQueueSaga() {
  yield all([
    takeEvery(PLAYER_PLAY_TRACKS, handlePlayTracks),
    takeEvery(PLAYER_SET_TRACK, handleSetTrack),
    takeEvery(PLAYER_PLAY_NEXT, handlePlayNext),
    takeEvery(PLAYER_ADD_TRACKS, handleAddTracks),
    takeEvery(PLAYER_CLEAR_QUEUE, handleClearQueue),
  ])
}
