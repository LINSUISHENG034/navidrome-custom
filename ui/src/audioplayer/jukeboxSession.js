import {
  setJukeboxMode,
  syncQueue,
  updateJukeboxSessionStatus,
} from '../actions'

const JUKEBOX_SESSION_ID_PREFIX = 'jukebox-session:'
const JUKEBOX_CLIENT_ID_KEY = 'jukebox-client-id'
const JUKEBOX_HEARTBEAT_INTERVAL_MS = 15000
let documentClientId = null

const startJukeboxHeartbeatLoop = ({
  jukeboxMode,
  sessionId,
  clientId,
  onHeartbeat,
}) => {
  if (!jukeboxMode || !sessionId || !clientId || !onHeartbeat) return () => {}
  const interval = setInterval(onHeartbeat, JUKEBOX_HEARTBEAT_INTERVAL_MS)
  return () => clearInterval(interval)
}

const getJukeboxSessionId = () => {
  const username = localStorage.getItem('username')
  return username ? `${JUKEBOX_SESSION_ID_PREFIX}${username}` : null
}

const getOrCreateJukeboxClientId = () => {
  if (!documentClientId) {
    documentClientId =
      window.crypto?.randomUUID?.() ||
      `${Date.now()}-${Math.random().toString(16).slice(2)}`
  }
  // Storage is diagnostic only: a cloned document must not inherit control.
  sessionStorage.setItem(JUKEBOX_CLIENT_ID_KEY, documentClientId)
  return documentClientId
}

const dispatchJukeboxSessionStatus = (dispatch, status) => {
  if (dispatch) {
    dispatch(updateJukeboxSessionStatus(status))
  }
  return status
}

// --- Caller-scoped control eligibility ---
//
// One predicate drives every egress boundary (controller render+commands,
// Player callbacks, keyboard, legacy saga). Eligibility requires the strict
// reducer-admitted shared grant, the admitted owner to BE this caller's
// actual session/client identity, and a live enabled episode — registered
// and reconciled by the reducer — whose reconciled binding matches the
// admitted scene binding. An observer's snapshot may therefore admit shared
// facts without ever making the observer eligible, and never revokes a
// genuine same-binding owner. The returned capture is immutable: callers
// recheck it against fresh state after every await, never recapturing a
// replacement token/owner/generation mid-intent.

const captureEligibleJukeboxScope = (playerState, scope = {}) => {
  const control = playerState?.jukeboxControl
  if (!control) return null
  const sessionId = scope.sessionId ?? getJukeboxSessionId()
  const clientId = scope.clientId ?? getOrCreateJukeboxClientId()
  if (!sessionId || !clientId) return null
  if (control.controlGranted !== true) return null
  if (control.ownershipState !== 'attached') return null
  if (control.sessionId !== sessionId || control.ownerClientId !== clientId)
    return null
  const remote = playerState?.jukeboxRemote
  const bindingGeneration = remote?.bindingGeneration ?? null
  const device = remote?.deviceName ?? null
  if (bindingGeneration == null) return null
  const episodes = playerState?.jukeboxEpisodes || {}
  const token = Object.keys(episodes)
    .filter((candidate) => {
      if (scope.token && candidate !== scope.token) return false
      const episode = episodes[candidate]
      return !!(
        episode &&
        episode.disabled === false &&
        episode.sessionId === sessionId &&
        episode.clientId === clientId &&
        episode.reconciledBinding &&
        episode.reconciledBinding.bindingGeneration === bindingGeneration &&
        (episode.reconciledBinding.canonicalDevice ?? null) === device
      )
    })
    .sort()[0]
  if (!token) return null
  return {
    token,
    revision: episodes[token].revision ?? 0,
    sessionId,
    clientId,
    bindingGeneration,
    device,
  }
}

// The default control check resolves THIS document's actual session/client
// identity — never an attached-only fallback.
const canControlJukebox = (playerState) =>
  captureEligibleJukeboxScope(playerState) !== null

// Mid-intent recheck: the SAME captured scope must still be capturable from
// the CURRENT state — same token, revision, binding. Any takeover, rebind,
// device move, disable, or token replacement breaks it.
const isCapturedJukeboxScopeCurrent = (playerState, captured) => {
  if (!captured) return false
  const again = captureEligibleJukeboxScope(playerState, {
    sessionId: captured.sessionId,
    clientId: captured.clientId,
    token: captured.token,
  })
  return !!(
    again &&
    again.revision === captured.revision &&
    again.bindingGeneration === captured.bindingGeneration &&
    again.device === captured.device
  )
}

// --- Ordered remote admission (binding generation / state sequence) ---
//
// One shared pure classifier for the reducer (single authority for the
// accepted scene and the observed ordering floor) and the controller
// (snapshot admission at the atomic HTTP boundary). Trust is always local
// action.meta provenance ({source: 'atomic-session-snapshot', sessionId,
// deviceName, clientId}); server payload fields such as a baseline flag
// never grant trust. Attach/status/SSE observations are untrusted: they may
// raise the generation/sequence floor and invalidate, but they can never
// establish or replace the authoritative scene or enable control.

const ATOMIC_SNAPSHOT_SOURCE = 'atomic-session-snapshot'

// Generation must be a positive safe integer; sequence a non-negative safe
// integer. An explicit 0 stateSequence/queueVersion is valid history.
const isSafeOrderedInteger = (value, minimum) =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum

const readOrderedFloor = (status) => {
  if (!isSafeOrderedInteger(status?.bindingGeneration, 1)) return null
  if (!isSafeOrderedInteger(status?.stateSequence, 0)) return null
  return {
    bindingGeneration: status.bindingGeneration,
    stateSequence: status.stateSequence,
  }
}

const isTrustedSnapshotMeta = (meta) =>
  meta?.source === ATOMIC_SNAPSHOT_SOURCE &&
  typeof meta.sessionId === 'string' &&
  meta.sessionId !== '' &&
  typeof meta.clientId === 'string' &&
  meta.clientId !== ''

// Semantic scene fields. An equal-generation/equal-sequence observation that
// supplies different values for any of these conflicts with the accepted
// scene; position, heartbeat and handoff metadata are harmless.
const SCENE_SEMANTIC_FIELDS = [
  'currentIndex',
  'trackId',
  'queueItemId',
  'playing',
  'gain',
  'desiredGain',
  'muted',
  'playMode',
  'playbackInstanceId',
  'queueVersion',
]

const canonicalSceneValue = (value) => (value === undefined ? null : value)
const conflictsWithScene = (status, scene) =>
  SCENE_SEMANTIC_FIELDS.some(
    (field) =>
      status[field] !== undefined &&
      canonicalSceneValue(status[field]) !==
        canonicalSceneValue(scene?.[field]),
  )

const isSameOrdering = (a, b) =>
  !!a &&
  !!b &&
  a.bindingGeneration === b.bindingGeneration &&
  a.stateSequence === b.stateSequence

const isOlderOrdering = (ordering, floor) =>
  !!ordering &&
  !!floor &&
  (ordering.bindingGeneration < floor.bindingGeneration ||
    (ordering.bindingGeneration === floor.bindingGeneration &&
      ordering.stateSequence < floor.stateSequence))

// How one SessionStatus-shaped payload relates to the current authority.
// `remote` is the reducer's jukeboxRemote (observed floor + accepted scene).
// Callers decide trust; this classifies ordering only, so there is a single
// shared gate instead of per-source duplicates.
const ORDERED_OBSERVATION = {
  BESTEFFORT: 'besteffort', // no valid ordered fields, no floor: legacy merge
  READONLY: 'readonly', // no valid ordered fields, floor exists: no scene change
  ESTABLISH: 'establish', // valid ordered fields, no floor yet
  STALE: 'stale', // lower generation, or same generation lower sequence
  EQUAL: 'equal', // same generation and sequence as the floor
  ADVANCED: 'advanced', // higher generation, or same generation higher sequence
}

const classifyJukeboxObservation = (status, remote) => {
  const ordered = readOrderedFloor(status)
  const floor = readOrderedFloor(remote)
  if (!ordered)
    return floor ? ORDERED_OBSERVATION.READONLY : ORDERED_OBSERVATION.BESTEFFORT
  if (!floor) return ORDERED_OBSERVATION.ESTABLISH
  if (isOlderOrdering(ordered, floor)) return ORDERED_OBSERVATION.STALE
  if (isSameOrdering(ordered, floor)) return ORDERED_OBSERVATION.EQUAL
  return ORDERED_OBSERVATION.ADVANCED
}

const buildAtomicSnapshotMeta = ({
  sessionId,
  clientId,
  deviceName,
  token,
  revision,
  requestBinding,
}) => ({
  source: ATOMIC_SNAPSHOT_SOURCE,
  sessionId,
  clientId,
  deviceName: deviceName ?? null,
  token,
  revision,
  requestBinding: {
    bindingGeneration: requestBinding?.bindingGeneration ?? null,
    deviceName: requestBinding?.deviceName ?? null,
  },
})

// Local episode tokens are reducer-owned lifecycle state; this only mints
// them. Anti-ABA is inherent: a retired token is unregistered, so its late
// responses are rejected whole — no body fingerprints.
const createJukeboxEpisodeToken = () =>
  window.crypto?.randomUUID?.() ||
  `${Date.now()}-${Math.random().toString(16).slice(2)}`

const attachJukeboxSession = async ({
  client,
  sessionId,
  clientId,
  deviceName = null,
  takeover = false,
  dispatch,
}) => {
  if (!client?.attachSession || !sessionId || !clientId) return null
  const status = takeover
    ? await client.attachSession(sessionId, clientId, deviceName, {
        takeover: true,
      })
    : await client.attachSession(sessionId, clientId, deviceName)
  return dispatchJukeboxSessionStatus(dispatch, status)
}

const refreshJukeboxSessionStatus = async ({
  jukeboxMode,
  client,
  sessionId,
  dispatch,
}) => {
  if (!jukeboxMode || !client?.sessionStatus || !sessionId) return null
  const status = await client.sessionStatus(sessionId)
  return dispatchJukeboxSessionStatus(dispatch, status)
}

const detachJukeboxSession = async ({
  client,
  sessionId,
  clientId,
  keepalive = false,
  attachment = null,
}) => {
  if (!client?.detachSession || !sessionId || !clientId) return null
  const options = {
    ...(keepalive ? { keepalive: true } : {}),
    ...(attachment ? { attachment } : {}),
  }
  return Object.keys(options).length
    ? client.detachSession(sessionId, clientId, options)
    : client.detachSession(sessionId, clientId)
}

const runJukeboxHeartbeat = async ({
  client,
  sessionId,
  clientId,
  deviceName = null,
  dispatch,
  isActive = () => true,
  onAttachment,
}) => {
  if (!client?.heartbeatSession || !sessionId || !clientId || !isActive())
    return null

  try {
    const status = await client.heartbeatSession(sessionId, clientId)
    return isActive() ? dispatchJukeboxSessionStatus(dispatch, status) : null
  } catch (heartbeatError) {
    if (!isActive()) return null
    let error = heartbeatError
    if (error?.status === 404 && client?.attachSession) {
      try {
        const status = await client.attachSession(
          sessionId,
          clientId,
          deviceName,
        )
        onAttachment?.(status)
        return isActive()
          ? dispatchJukeboxSessionStatus(dispatch, status)
          : status
      } catch (attachError) {
        error = attachError
      }
    }
    if (!isActive()) return null

    if (error?.status === 403) {
      return dispatchJukeboxSessionStatus(dispatch, {
        sessionId,
        ownerClientId: clientId,
        ownershipState: 'taken_over',
        terminationReason: 'taken_over',
      })
    }

    if (error?.status === 404) {
      return dispatchJukeboxSessionStatus(dispatch, {
        sessionId,
        ownerClientId: clientId,
        ownershipState: 'detached',
        terminationReason: 'session_missing',
      })
    }

    return dispatchJukeboxSessionStatus(dispatch, {
      sessionId,
      ownerClientId: clientId,
      ownershipState: 'recovering',
      terminationReason: null,
    })
  }
}

const createJukeboxSessionEpisode = ({
  client,
  sessionId,
  clientId,
  dispatch,
  getDeviceName = () => null,
  onHeartbeat,
  previousRetirement = Promise.resolve(),
}) => {
  let active = true
  let attachment = null
  let retired
  let cleanupHeartbeat = () => {}
  const pending = new Set()
  const guardDispatch = (callback) => (action) => {
    if (active) callback?.(action)
  }
  const rememberAttachment = (status) => {
    attachment = status
    return status
  }
  const track = (operation) => {
    pending.add(operation)
    operation.finally(() => pending.delete(operation)).catch(() => {})
    return operation
  }
  const heartbeat = () =>
    track(
      runJukeboxHeartbeat({
        client,
        sessionId,
        clientId,
        deviceName: getDeviceName(),
        dispatch: guardDispatch(dispatch),
        isActive: () => active,
        onAttachment: rememberAttachment,
      }).then((status) => {
        if (!active || !status) return status
        if (status.ownershipState === 'taken_over') cleanupHeartbeat()
        return Promise.resolve(onHeartbeat?.(status)).then(() => status)
      }),
    ).catch(() => null)

  return {
    attach: ({
      deviceName = null,
      takeover = false,
      dispatch: attachDispatch = dispatch,
    } = {}) =>
      track(
        previousRetirement
          .catch(() => {})
          .then(() => {
            if (!active) return null
            return attachJukeboxSession({
              client,
              sessionId,
              clientId,
              deviceName,
              takeover,
              dispatch: guardDispatch(attachDispatch),
            }).then((status) => {
              rememberAttachment(status)
              if (active && status) {
                cleanupHeartbeat()
                cleanupHeartbeat = startJukeboxHeartbeatLoop({
                  jukeboxMode: true,
                  sessionId,
                  clientId,
                  onHeartbeat: heartbeat,
                })
              }
              return status
            })
          }),
      ),
    retire: ({ keepalive = false } = {}) => {
      if (!retired) {
        active = false
        cleanupHeartbeat()
        retired = Promise.allSettled([...pending])
          .then(
            () =>
              attachment &&
              detachJukeboxSession({
                client,
                sessionId,
                clientId,
                keepalive,
                attachment,
              }),
          )
          .catch(() => {})
      }
      return retired
    },
  }
}

const rehydrateJukeboxSession = async ({ client, dispatch }) => {
  const sessionId = getJukeboxSessionId()
  if (!client?.sessionStatus || !sessionId) return null

  try {
    const status = await client.sessionStatus(sessionId)
    if (status && status.deviceName && status.deviceName !== 'auto') {
      dispatchJukeboxSessionStatus(dispatch, status)
      if (client.sessionSnapshot) {
        try {
          const snapshot = await client.sessionSnapshot(sessionId)
          if (snapshot?.jukeboxPlaylist?.entry && dispatch) {
            dispatch(syncQueue(status, snapshot.jukeboxPlaylist.entry))
          }
        } catch {
          // Fall back to status-only rehydration
        }
      }
      if (dispatch) {
        dispatch(setJukeboxMode(true, status.deviceName))
      }
      return status
    }
  } catch {
    // Session not found or network error: stay in local mode
  }
  return null
}

export {
  attachJukeboxSession,
  buildAtomicSnapshotMeta,
  canControlJukebox,
  captureEligibleJukeboxScope,
  classifyJukeboxObservation,
  conflictsWithScene,
  createJukeboxEpisodeToken,
  createJukeboxSessionEpisode,
  detachJukeboxSession,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  isCapturedJukeboxScopeCurrent,
  isOlderOrdering,
  isSameOrdering,
  isTrustedSnapshotMeta,
  ORDERED_OBSERVATION,
  readOrderedFloor,
  refreshJukeboxSessionStatus,
  rehydrateJukeboxSession,
  runJukeboxHeartbeat,
  startJukeboxHeartbeatLoop,
}
