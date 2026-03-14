import { updateJukeboxSessionStatus } from '../actions'

const JUKEBOX_SESSION_ID_PREFIX = 'jukebox-session:'
const JUKEBOX_CLIENT_ID_KEY = 'jukebox-client-id'

const getJukeboxSessionId = () => {
  const username = localStorage.getItem('username')
  return username ? `${JUKEBOX_SESSION_ID_PREFIX}${username}` : null
}

const getOrCreateJukeboxClientId = () => {
  const existing = sessionStorage.getItem(JUKEBOX_CLIENT_ID_KEY)
  if (existing) return existing
  const next =
    globalThis.crypto?.randomUUID?.() ||
    `${Date.now()}-${Math.random().toString(16).slice(2)}`
  sessionStorage.setItem(JUKEBOX_CLIENT_ID_KEY, next)
  return next
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
  dispatch,
}) => {
  if (!client?.attachSession || !sessionId || !clientId) return null
  const status = await client.attachSession(sessionId, clientId, deviceName)
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

const detachJukeboxSession = async ({ client, sessionId, clientId }) => {
  if (!client?.detachSession || !sessionId || !clientId) return null
  return client.detachSession(sessionId, clientId)
}

const runJukeboxHeartbeat = async ({
  client,
  sessionId,
  clientId,
  deviceName = null,
  dispatch,
}) => {
  if (!client?.heartbeatSession || !sessionId || !clientId) return null

  try {
    const status = await client.heartbeatSession(sessionId, clientId)
    return dispatchJukeboxSessionStatus(dispatch, status)
  } catch (err) {
    if (err?.status === 404 && client?.attachSession) {
      try {
        const status = await client.attachSession(sessionId, clientId, deviceName)
        return dispatchJukeboxSessionStatus(dispatch, status)
      } catch (attachErr) {
        err = attachErr
      }
    }

    if (err?.status === 403) {
      return dispatchJukeboxSessionStatus(dispatch, {
        sessionId,
        ownerClientId: clientId,
        ownershipState: 'taken_over',
        terminationReason: 'taken_over',
      })
    }

    if (err?.status === 404) {
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

export {
  attachJukeboxSession,
  canControlJukebox,
  detachJukeboxSession,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  refreshJukeboxSessionStatus,
  runJukeboxHeartbeat,
}
