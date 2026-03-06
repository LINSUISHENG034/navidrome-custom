let suppressUntil = 0

export const suppressJukeboxMediaEvents = (ms = 500) => {
  suppressUntil = Date.now() + ms
}

export const shouldForwardJukeboxMediaEvent = ({ jukeboxMode, hidden }) => {
  if (!jukeboxMode) return false
  if (hidden) return false
  return Date.now() > suppressUntil
}

export const resetJukeboxMediaEventSuppression = () => {
  suppressUntil = 0
}
