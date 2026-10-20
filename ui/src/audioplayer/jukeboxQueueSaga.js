import { all, call, getContext, select, takeEvery } from 'redux-saga/effects'
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

function* routeQueueAction(legacyHandler, action) {
  const controller = yield getContext('playbackController')
  if (!controller) {
    yield call(legacyHandler, action)
    return
  }

  // Saga receives actions after the reducer; capture this action's occurrences
  // before awaiting the engine, rather than diffing against a pending queue.
  const state = yield select()
  const { queue, current, playIndex } = state.player
  const replacesQueue =
    action.type === PLAYER_PLAY_TRACKS || action.type === PLAYER_SET_TRACK
  const clearsQueue = action.type === PLAYER_CLEAR_QUEUE
  const count = replacesQueue
    ? queue.length
    : Object.keys(action.data || {}).length
  if (count === 0 && !replacesQueue && !clearsQueue) return
  let start = queue.length - count
  if (action.type === PLAYER_PLAY_NEXT) {
    const currentIndex = queue.findIndex((item) => item.uuid === current?.uuid)
    if (currentIndex >= 0) start = currentIndex + 1
  }
  const items = queue.slice(start, start + count).map((item) => ({
    ...item,
    queueItemId: item.queueItemId || item.uuid,
  }))
  const method = {
    [PLAYER_PLAY_TRACKS]: controller.playAll,
    [PLAYER_SET_TRACK]: controller.playAll,
    [PLAYER_PLAY_NEXT]: controller.playNext,
    [PLAYER_ADD_TRACKS]: controller.addQueueItems,
    [PLAYER_CLEAR_QUEUE]: controller.clearQueue,
  }[action.type]
  const args = clearsQueue ? [] : [items]
  if (replacesQueue) {
    args.push(playIndex ?? 0)
  }

  try {
    yield call([controller, method], ...args)
  } catch {
    // The Module publishes the failure; never fall back to a second engine.
  }
}

export default function* jukeboxQueueSaga() {
  yield all([
    takeEvery(PLAYER_PLAY_TRACKS, routeQueueAction, handlePlayTracks),
    takeEvery(PLAYER_SET_TRACK, routeQueueAction, handleSetTrack),
    takeEvery(PLAYER_PLAY_NEXT, routeQueueAction, handlePlayNext),
    takeEvery(PLAYER_ADD_TRACKS, routeQueueAction, handleAddTracks),
    takeEvery(PLAYER_CLEAR_QUEUE, routeQueueAction, handleClearQueue),
  ])
}
