import { v4 as uuidv4 } from 'uuid'
import subsonic from '../subsonic'
import { decisionService } from '../transcode'
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
  PLAYER_REFRESH_QUEUE,
  PLAYER_SET_JUKEBOX_MODE,
  PLAYER_JUKEBOX_SESSION_STATUS,
  PLAYER_SET_AUDIO_INSTANCE,
  PLAYER_JUKEBOX_EPISODE_BEGIN,
  PLAYER_JUKEBOX_EPISODE_END,
  PLAYER_JUKEBOX_EPISODE_DISABLE,
} from '../actions'
import config from '../config'
import { audioVolumeToUiVolume } from '../audioplayer/volumeMapping'
import { remoteGainToUiVolume } from '../audioplayer/volumeProfiles'
import {
  classifyJukeboxObservation,
  conflictsWithScene,
  isSameOrdering,
  isTrustedSnapshotMeta,
  ORDERED_OBSERVATION,
  readOrderedFloor,
} from '../audioplayer/jukeboxSession'

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
  jukeboxPlaylist: null,
  jukeboxEpisodes: {},
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

const makeMusicSrc = (trackId) =>
  decisionService.getProfile()
    ? () =>
        decisionService
          .resolveStreamUrl(trackId)
          .catch(() => subsonic.streamUrl(trackId))
    : subsonic.streamUrl(trackId)

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
    musicSrc: makeMusicSrc(trackId),
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
  const newTracks = Object.keys(data).map((id) => mapToAudioLists(data[id]))
  const newQueue = []
  const current = state.current || {}
  let foundPos = false
  state.queue.forEach((item) => {
    newQueue.push(item)
    if (item.uuid === current.uuid) {
      foundPos = true
      newQueue.push(...newTracks)
    }
  })
  if (!foundPos) {
    newQueue.push(...newTracks)
  }

  return {
    ...state,
    queue: newQueue,
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
  // Keep clear and playIndex alive when there is a pending track switch.
  // A switch is pending when playIndex is set AND either:
  //   - playIndex differs from savedPlayIndex, OR
  //   - clear is true (a new queue was loaded, e.g. after clearQueue + playTracks)
  // The clear check handles the edge case where both playIndex and
  // savedPlayIndex are 0 (close player then play a new album from track 1).
  const hasPendingSwitch =
    state.playIndex != null &&
    (state.clear || state.playIndex !== state.savedPlayIndex)
  return {
    ...state,
    queue: audioLists.map((item) => ({ ...item })),
    clear: hasPendingSwitch ? state.clear : false,
    playIndex: hasPendingSwitch ? state.playIndex : undefined,
  }
}

const reduceCurrent = (state, { data }) => {
  const current = data.ended ? {} : data
  const savedPlayIndex = state.queue.findIndex(
    (item) => item.uuid === current.uuid,
  )
  // When a track selection is pending (playIndex is set), keep it alive
  // until the music player confirms it actually switched to the requested
  // track. Without this, a premature onAudioPlay callback for the
  // still-playing old track would overwrite the pending selection.
  const pending = state.playIndex != null && savedPlayIndex !== state.playIndex
  return {
    ...state,
    current,
    playIndex: pending ? state.playIndex : undefined,
    clear: pending ? state.clear : false,
    savedPlayIndex: pending ? state.savedPlayIndex : savedPlayIndex,
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
        : (previousControl.terminationReason ?? null),
  lastHeartbeat:
    nextStatus.lastHeartbeat ?? previousControl.lastHeartbeat ?? null,
  staleSince: nextStatus.staleSince ?? previousControl.staleSince ?? null,
  // Bounded-reconciliation target armed when trusted A provenance observed a
  // newer binding without conferring writes; carried through control-only
  // updates, consumed by the next trusted response (grant, contradiction, or
  // an answer without a canonical device).
  bindingReconcile: previousControl.bindingReconcile ?? null,
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

// --- Strict ordered admission ---
//
// The reducer is the single authority for the accepted scene and the
// observed ordering floor. The scene plus the floor live together in
// jukeboxRemote (floor fields bindingGeneration/stateSequence; scene fields
// only once a trusted atomic snapshot establishes them). The accepted-scene
// ordering anchor and the equal-sequence conflict marker live in
// jukeboxControl, the session-authority slice.

const readSceneAnchor = (control) => {
  const bindingGeneration = control?.sceneBindingGeneration
  const stateSequence = control?.sceneStateSequence
  if (
    typeof bindingGeneration === 'number' &&
    Number.isSafeInteger(bindingGeneration) &&
    bindingGeneration >= 1 &&
    typeof stateSequence === 'number' &&
    Number.isSafeInteger(stateSequence) &&
    stateSequence >= 0
  ) {
    return { bindingGeneration, stateSequence }
  }
  return null
}

const carrySceneAuthority = (nextControl, previousControl) => {
  const carried = { ...nextControl }
  if (previousControl?.sceneConflicted !== undefined) {
    carried.sceneConflicted = previousControl.sceneConflicted
  }
  if (previousControl?.controlGranted !== undefined) {
    carried.controlGranted = previousControl.controlGranted
  }
  const anchor = readSceneAnchor(previousControl)
  if (anchor) {
    carried.sceneBindingGeneration = anchor.bindingGeneration
    carried.sceneStateSequence = anchor.stateSequence
  }
  return carried
}

const withSceneAuthority = (nextControl, { conflicted, anchor, granted }) => ({
  ...nextControl,
  sceneConflicted: conflicted,
  sceneBindingGeneration: anchor.bindingGeneration,
  sceneStateSequence: anchor.stateSequence,
  controlGranted: granted,
})

// Trusted atomic admission replaces the scene wholesale. Values are kept
// raw/canonical exactly as issued (mode, instance, current item, queue
// version, gain/desired gain/mute); a trusted full snapshot that omits the
// playback instance clears it to null, while fields the snapshot omits are
// retained from the previous scene for older-server compatibility. The
// canonical device is the exception: a missing device — omitted or explicit
// null — asserts nothing and never inherits the previous scene's (or a
// raw-observation-seeded) device, so it clears to null and stays read-only
// (no grant, no reconcile target) until an explicit current valid device is
// observed through trusted provenance.
const buildStrictScene = (previousRemote, nextStatus) => ({
  attached: nextStatus.attached ?? previousRemote?.attached ?? false,
  bindingGeneration: nextStatus.bindingGeneration,
  currentIndex: nextStatus.currentIndex ?? previousRemote?.currentIndex ?? null,
  desiredGain: nextStatus.desiredGain ?? previousRemote?.desiredGain ?? null,
  deviceName:
    nextStatus.deviceName === undefined ? null : nextStatus.deviceName,
  gain: nextStatus.gain ?? previousRemote?.gain ?? 0.5,
  muted: nextStatus.muted ?? previousRemote?.muted ?? false,
  playMode: nextStatus.playMode ?? previousRemote?.playMode ?? null,
  playing: nextStatus.playing ?? previousRemote?.playing ?? false,
  playbackInstanceId:
    nextStatus.playbackInstanceId === undefined
      ? null
      : nextStatus.playbackInstanceId,
  position: nextStatus.position ?? previousRemote?.position ?? 0,
  queueItemId: nextStatus.queueItemId ?? previousRemote?.queueItemId ?? null,
  queueVersion: nextStatus.queueVersion ?? previousRemote?.queueVersion ?? null,
  stateSequence: nextStatus.stateSequence,
  trackId: nextStatus.trackId ?? previousRemote?.trackId ?? null,
})

// --- Reducer-owned episode lifecycle ---
//
// The reducer is the single registry of live control episodes. A record
// holds only provenance: session/client/requested device, an invalidation
// revision, the reconciled output binding, and a disabled flag. Episodes
// begin disabled; the atomic snapshot action is the ONLY reconcile
// transaction and enables them; DISABLE clears the matching reconciliation
// and bumps the revision so responses captured at an older revision can
// never regrant; END retires only the matching token.

const reduceJukeboxEpisodeBegin = (state, { data }) => {
  if (!data?.token || !data.sessionId || !data.clientId) return state
  return {
    ...state,
    jukeboxEpisodes: {
      ...state.jukeboxEpisodes,
      [data.token]: {
        sessionId: data.sessionId,
        clientId: data.clientId,
        requestedDeviceName: data.deviceName ?? null,
        revision: 0,
        reconciledBinding: null,
        disabled: true,
      },
    },
  }
}

const reduceJukeboxEpisodeEnd = (state, { data }) => {
  if (!data?.token || !state.jukeboxEpisodes?.[data.token]) return state
  const jukeboxEpisodes = { ...state.jukeboxEpisodes }
  delete jukeboxEpisodes[data.token]
  return { ...state, jukeboxEpisodes }
}

const reduceJukeboxEpisodeDisable = (state, { data }) => {
  const episode = state.jukeboxEpisodes?.[data?.token]
  if (!episode) return state
  return {
    ...state,
    jukeboxEpisodes: {
      ...state.jukeboxEpisodes,
      [data.token]: {
        ...episode,
        disabled: true,
        reconciledBinding: null,
        revision: (episode.revision ?? 0) + 1,
      },
    },
  }
}

// An untrusted observation with valid ordering but no floor yet raises the
// floor only: it can never establish scene identity (no playback instance,
// no mode, no current item) or enable control. deviceName is binding
// identity, so a raw observation never seeds it into the floor — raw
// provenance is floor/invalidation only, and there must be nothing here that
// a later trusted omission could inherit.
const buildFloorOnlyRemote = (previousRemote, nextStatus) => {
  const nextRemote = { ...(previousRemote || {}) }
  if (nextStatus.attached !== undefined)
    nextRemote.attached = nextStatus.attached
  nextRemote.bindingGeneration = nextStatus.bindingGeneration
  nextRemote.stateSequence = nextStatus.stateSequence
  return nextRemote
}

// Untrusted observations never mutate semantic scene fields or the canonical
// device: both are binding identity that only trusted atomic provenance may
// establish or replace. A conflicting claim retains the whole scene (only the
// floor advances); a non-conflicting observation may apply harmless position
// metadata.
const buildAdvancedRemote = (previousRemote, nextStatus, ordered) => {
  const nextRemote = {
    ...previousRemote,
    bindingGeneration: ordered.bindingGeneration,
    stateSequence: ordered.stateSequence,
  }
  if (nextStatus.attached !== undefined)
    nextRemote.attached = nextStatus.attached
  if (
    !conflictsWithScene(nextStatus, previousRemote) &&
    nextStatus.position !== undefined
  ) {
    nextRemote.position = nextStatus.position
  }
  return nextRemote
}

// Equal-ordering untrusted observation without a semantic conflict: only
// harmless position and heartbeat/handoff metadata apply; the scene and the
// canonical device are retained.
const applyHarmlessObservation = (previousRemote, nextStatus) => {
  const nextRemote = { ...previousRemote }
  if (nextStatus.position !== undefined)
    nextRemote.position = nextStatus.position
  if (nextStatus.attached !== undefined)
    nextRemote.attached = nextStatus.attached
  return nextRemote
}

const reduceJukeboxSessionStatus = (previousState, payload) => {
  const previousControl = previousState.jukeboxControl || {}
  const previousRemote = previousState.jukeboxRemote
  const rawData = payload.data || {}
  // The atomic snapshot boundary may carry the whole admitted pair; bare
  // status payloads (attach/status/SSE/fixtures) stay status-shaped.
  const nextStatus = rawData.sessionStatus || rawData
  const nextPlaylist = rawData.jukeboxPlaylist
  const kind = classifyJukeboxObservation(nextStatus, previousRemote)
  const anchor = readSceneAnchor(previousControl)

  const baseControl = () =>
    carrySceneAuthority(
      buildJukeboxControlState(previousControl, nextStatus),
      previousControl,
    )
  const withVolume = (nextRemote, nextControl, playlist) => ({
    ...previousState,
    jukeboxControl: nextControl,
    jukeboxRemote: nextRemote,
    jukeboxPlaylist:
      playlist === undefined
        ? (previousState.jukeboxPlaylist ?? null)
        : playlist,
    volume:
      previousState.jukeboxMode && typeof nextRemote?.gain === 'number'
        ? remoteGainToUiVolume(nextRemote.gain, {
            deviceName: nextRemote?.deviceName,
            jukeboxDevice: previousState.jukeboxDevice,
          })
        : previousState.volume,
  })

  // Trusted-shaped provenance must name a live reducer-registered episode.
  // Tokenless, unregistered/retired, revision-stale, or session/client-
  // mismatched actions are rejected WHOLE — never downgraded to a raw
  // observation (raw provenance carries no meta at all).
  let trusted = false
  let episode = null
  if (isTrustedSnapshotMeta(payload.meta)) {
    const candidate = previousState.jukeboxEpisodes?.[payload.meta.token]
    const revision = payload.meta.revision
    const requestBinding = payload.meta.requestBinding
    if (
      typeof payload.meta.token !== 'string' ||
      payload.meta.token === '' ||
      !candidate ||
      candidate.sessionId !== payload.meta.sessionId ||
      candidate.clientId !== payload.meta.clientId ||
      !Number.isSafeInteger(revision) ||
      revision < 0 ||
      candidate.revision !== revision ||
      !requestBinding ||
      typeof requestBinding !== 'object' ||
      (nextStatus.sessionId && nextStatus.sessionId !== payload.meta.sessionId)
    ) {
      return previousState
    }
    trusted = true
    episode = candidate
  }

  // Stale ordering is rejected whole for every provenance: lower generation,
  // or same generation with a lower sequence than the observed floor,
  // regardless of arrival order.
  if (kind === ORDERED_OBSERVATION.STALE) return previousState

  // Trusted atomic snapshot admission. The reducer is the single authority:
  // it admits the whole status+playlist pair or retains it whole. The fence
  // is the observed generation/sequence floor plus the accepted-scene anchor:
  // a full snapshot at/above the floor with no semantic conflict against the
  // accepted scene is admitted, a higher generation may restart (lower)
  // sequencing and the queue version, and anything below is rejected
  // regardless of HTTP arrival order.
  if (trusted) {
    const ordered = readOrderedFloor(nextStatus)
    if (!ordered) {
      // Trusted provenance without usable ordering cannot establish scene
      // identity nor reset the accepted scene (anti-ABA at the boundary).
      return withVolume(previousRemote, {
        ...baseControl(),
        controlGranted: false,
      })
    }
    if (
      anchor &&
      isSameOrdering(ordered, anchor) &&
      conflictsWithScene(nextStatus, previousRemote)
    ) {
      // Equal-ordering semantic conflict: retain the accepted scene and pair,
      // raise the conflict marker once, and suspend control so the controller
      // reacquires with a single bounded refresh.
      if (previousControl.sceneConflicted) return previousState
      return withVolume(
        previousRemote,
        withSceneAuthority(
          {
            ...buildJukeboxControlState(previousControl, nextStatus),
            bindingReconcile: null,
          },
          { conflicted: true, anchor, granted: false },
        ),
      )
    }
    // Device contradiction: trusted provenance at the SAME generation as the
    // accepted scene that asserts a different canonical device (a missing
    // device asserts nothing and is handled by the ordinary admission rules).
    // The admitted binding identity is contradicted, so the authority retains
    // the scene and pair whole, raises the conflict marker once, and suspends
    // writes. The episode is never reconciled to the contradicted device, so
    // no follow-up GET — heartbeat-style or bound to the contradiction — can
    // ever restore the grant on it; recovery requires a genuinely consistent
    // observation of the retained device or a higher ordered generation.
    const sceneDevice = previousRemote?.deviceName ?? null
    const statusDevice =
      nextStatus.deviceName === undefined
        ? sceneDevice
        : (nextStatus.deviceName ?? null)
    const deviceContradiction =
      anchor != null &&
      ordered.bindingGeneration === anchor.bindingGeneration &&
      sceneDevice != null &&
      statusDevice != null &&
      statusDevice !== sceneDevice
    if (deviceContradiction) {
      if (previousControl.sceneConflicted) return previousState
      return withVolume(
        previousRemote,
        withSceneAuthority(
          {
            ...buildJukeboxControlState(previousControl, nextStatus),
            bindingReconcile: null,
          },
          { conflicted: true, anchor, granted: false },
        ),
      )
    }
    const nextControl = buildJukeboxControlState(previousControl, nextStatus)
    // Reconcile the fetching episode against the binding its GET started
    // with. A response whose binding matches the request binding (or the
    // first baseline, which establishes it) confers the shared grant when it
    // carries a canonical device and the session is attached. A known-A
    // request answered by a newer binding B admits the shared facts but does
    // NOT grant writes from A provenance: the authority arms exactly one
    // bounded follow-up GET started against current B (see bindingReconcile).
    // Permission never transfers silently.
    const nextRemote = buildStrictScene(previousRemote, nextStatus)
    const responseGen = ordered.bindingGeneration
    const responseDevice = nextRemote.deviceName ?? null
    const requestGen = episode
      ? (payload.meta.requestBinding.bindingGeneration ?? null)
      : null
    const requestDevice = episode
      ? (payload.meta.requestBinding.deviceName ?? null)
      : null
    const bindingEstablished = requestGen == null
    const bindingMatch =
      !bindingEstablished &&
      requestGen === responseGen &&
      (requestDevice == null || requestDevice === responseDevice)
    const granted =
      nextControl.ownershipState === 'attached' &&
      responseDevice != null &&
      (bindingEstablished || bindingMatch)
    // Arm the next bounded reconciliation only when a live binding with a
    // canonical device was observed that this provenance cannot confer
    // writes on. A repeated answer on the same target, or one without a
    // canonical device, consumes the arm and stays readonly: the snapshot
    // endpoint can never be stormed.
    const prevReconcile = previousControl.bindingReconcile ?? null
    const reconcileConsumed =
      prevReconcile != null &&
      prevReconcile.bindingGeneration === responseGen &&
      (prevReconcile.deviceName ?? null) === responseDevice
    const nextReconcile =
      granted || bindingEstablished || responseDevice == null
        ? null
        : reconcileConsumed
          ? null
          : { bindingGeneration: responseGen, deviceName: responseDevice }
    const admitted = withVolume(
      nextRemote,
      withSceneAuthority(
        { ...nextControl, bindingReconcile: nextReconcile },
        {
          conflicted: false,
          anchor: ordered,
          granted,
        },
      ),
      nextPlaylist,
    )
    if (!episode) return admitted
    return {
      ...admitted,
      jukeboxEpisodes: {
        ...admitted.jukeboxEpisodes,
        [payload.meta.token]: {
          ...episode,
          disabled: false,
          reconciledBinding: {
            bindingGeneration: responseGen,
            canonicalDevice: responseDevice,
          },
        },
      },
    }
  }

  // --- Untrusted observations (attach/status/SSE, expired or replayed
  // trusted actions): they may raise the observed floor, apply harmless
  // metadata, or downgrade permissions, but never establish or replace the
  // admitted scene/pair and never grant control. ---

  if (kind === ORDERED_OBSERVATION.BESTEFFORT) {
    // No ordered fields and no floor yet (older server / local domain):
    // legacy best-effort merge, displayed read-only. A playlist supplied at
    // this pre-strict boundary is displayed but never grants control.
    return withVolume(
      buildJukeboxRemoteState(previousRemote || {}, nextStatus),
      { ...baseControl(), controlGranted: false },
      nextPlaylist,
    )
  }

  if (kind === ORDERED_OBSERVATION.READONLY) {
    // Missing/invalid ordered fields cannot reset an existing strict scene:
    // the observation is control-only (recovering/takenover may downgrade
    // control but never reset scene, ordering, or the admitted pair).
    return withVolume(previousRemote, {
      ...baseControl(),
      controlGranted: false,
    })
  }

  if (kind === ORDERED_OBSERVATION.ESTABLISH) {
    return withVolume(buildFloorOnlyRemote(previousRemote, nextStatus), {
      ...baseControl(),
      controlGranted: false,
    })
  }

  if (kind === ORDERED_OBSERVATION.EQUAL) {
    const ordered = readOrderedFloor(nextStatus)
    // Equal ordering is only a semantic conflict when it targets the
    // accepted scene itself; an observation matching a floor that was
    // raised beyond the scene by a newer partial is not falsely in conflict
    // with the older scene.
    if (
      anchor &&
      isSameOrdering(ordered, anchor) &&
      conflictsWithScene(nextStatus, previousRemote)
    ) {
      if (previousControl.sceneConflicted) return previousState
      return withVolume(
        previousRemote,
        withSceneAuthority(
          buildJukeboxControlState(previousControl, nextStatus),
          {
            conflicted: true,
            anchor,
            granted: false,
          },
        ),
      )
    }
    // Harmless position/heartbeat/handoff metadata keeps the grant; an
    // observation that changes ownership (takeover, foreign claim) can only
    // downgrade — a raw observation must never re-enable control.
    const permissionChanged =
      (nextStatus.ownershipState !== undefined &&
        nextStatus.ownershipState !== previousControl.ownershipState) ||
      (nextStatus.ownerClientId !== undefined &&
        nextStatus.ownerClientId !== previousControl.ownerClientId)
    return withVolume(
      applyHarmlessObservation(previousRemote, nextStatus),
      permissionChanged
        ? { ...baseControl(), controlGranted: false }
        : baseControl(),
    )
  }

  // ORDERED_OBSERVATION.ADVANCED (untrusted): the floor advances past the
  // accepted scene, so control is suspended until the next trusted snapshot.
  const ordered = readOrderedFloor(nextStatus)
  return withVolume(buildAdvancedRemote(previousRemote, nextStatus, ordered), {
    ...baseControl(),
    controlGranted: false,
  })
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
    case PLAYER_REFRESH_QUEUE: {
      const resolvedUrls = payload.data || {}
      return {
        ...previousState,
        queue: previousState.queue.map((item) => ({
          ...item,
          musicSrc: item.isRadio
            ? item.musicSrc
            : resolvedUrls[item.trackId] || subsonic.streamUrl(item.trackId),
        })),
        clear: true,
        autoPlay: false,
        playIndex:
          previousState.savedPlayIndex >= 0 ? previousState.savedPlayIndex : 0,
      }
    }
    case PLAYER_SET_JUKEBOX_MODE:
      return {
        ...previousState,
        jukeboxMode: payload.data.enabled,
        jukeboxDevice: payload.data.device,
        jukeboxControl: payload.data.enabled
          ? previousState.jukeboxControl
          : null,
        jukeboxRemote: payload.data.enabled
          ? previousState.jukeboxRemote
          : null,
        jukeboxPlaylist: payload.data.enabled
          ? previousState.jukeboxPlaylist
          : null,
        jukeboxEpisodes: payload.data.enabled
          ? previousState.jukeboxEpisodes
          : {},
      }
    case PLAYER_JUKEBOX_SESSION_STATUS:
      return reduceJukeboxSessionStatus(previousState, payload)
    case PLAYER_JUKEBOX_EPISODE_BEGIN:
      return reduceJukeboxEpisodeBegin(previousState, payload)
    case PLAYER_JUKEBOX_EPISODE_END:
      return reduceJukeboxEpisodeEnd(previousState, payload)
    case PLAYER_JUKEBOX_EPISODE_DISABLE:
      return reduceJukeboxEpisodeDisable(previousState, payload)
    case PLAYER_SET_AUDIO_INSTANCE:
      return {
        ...previousState,
        audioInstance: payload.data,
      }
    default:
      return previousState
  }
}
