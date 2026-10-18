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

const canControlJukebox = (playerState) =>
  !!(
    playerState?.jukeboxMode &&
    playerState?.jukeboxControl?.ownershipState === 'attached'
  )

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
  } catch (err) {
    if (err?.status === 403) {
      if (dispatch) {
        dispatch(setJukeboxMode(true, null))
        dispatch(
          updateJukeboxSessionStatus({
            sessionId,
            ownershipState: 'taken_over',
            terminationReason: 'taken_over',
          }),
        )
      }
      return { outcome: 'conflict', error: err }
    }
    // Session not found or network error: stay in local mode
  }
  return null
}

export {
  attachJukeboxSession,
  canControlJukebox,
  createJukeboxSessionEpisode,
  detachJukeboxSession,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  refreshJukeboxSessionStatus,
  rehydrateJukeboxSession,
  runJukeboxHeartbeat,
  startJukeboxHeartbeatLoop,
}
