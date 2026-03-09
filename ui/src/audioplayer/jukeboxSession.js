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

const attachJukeboxSession = async ({
  client,
  sessionId,
  clientId,
  deviceName = null,
  dispatch,
}) => {
  if (!client?.attachSession || !sessionId || !clientId) return null
  const status = await client.attachSession(sessionId, clientId, deviceName)
  if (dispatch) {
    dispatch(updateJukeboxSessionStatus(status))
  }
  return status
}

const refreshJukeboxSessionStatus = async ({
  jukeboxMode,
  client,
  sessionId,
  dispatch,
}) => {
  if (!jukeboxMode || !client?.sessionStatus || !sessionId) return null
  const status = await client.sessionStatus(sessionId)
  if (dispatch) {
    dispatch(updateJukeboxSessionStatus(status))
  }
  return status
}

const detachJukeboxSession = async ({ client, sessionId, clientId }) => {
  if (!client?.detachSession || !sessionId || !clientId) return null
  return client.detachSession(sessionId, clientId)
}

export {
  attachJukeboxSession,
  detachJukeboxSession,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  refreshJukeboxSessionStatus,
}
