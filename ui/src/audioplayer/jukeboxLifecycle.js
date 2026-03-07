let suppressUntil = 0
let pendingRemoteSeek = null

export const suppressJukeboxMediaEvents = (ms = 500) => {
  suppressUntil = Date.now() + ms
}

export const markPendingRemoteSeek = ({ position, ttlMs = 1000 }) => {
  pendingRemoteSeek = {
    position: Math.max(0, Number(position) || 0),
    expiresAt: Date.now() + ttlMs,
  }
}

export const shouldSuppressRemoteSeekEcho = (currentTime) => {
  if (!pendingRemoteSeek) return false
  if (Date.now() > pendingRemoteSeek.expiresAt) {
    pendingRemoteSeek = null
    return false
  }

  const current = Math.max(0, Number(currentTime) || 0)
  if (Math.abs(current - pendingRemoteSeek.position) <= 1) {
    pendingRemoteSeek = null
    return true
  }
  return false
}

export const shouldForwardJukeboxMediaEvent = ({ jukeboxMode, hidden }) => {
  if (!jukeboxMode) return false
  if (hidden) return false
  return Date.now() > suppressUntil
}

export const resetJukeboxMediaEventSuppression = () => {
  suppressUntil = 0
  pendingRemoteSeek = null
}
