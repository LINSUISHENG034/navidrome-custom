const selectPlayerState = (state) => state.player || {}

const selectEffectiveJukeboxRemoteSession = (state) =>
  selectPlayerState(state).jukeboxRemote || null

const selectEffectiveCurrentTrack = (state) => {
  const player = selectPlayerState(state)
  const remote = selectEffectiveJukeboxRemoteSession(state)
  if (player.jukeboxMode && remote) {
    const { currentIndex, trackId } = remote
    if (
      Number.isInteger(currentIndex) &&
      player.queue?.[currentIndex] &&
      (!trackId || player.queue[currentIndex].trackId === trackId)
    ) {
      return player.queue[currentIndex]
    }
    if (trackId) {
      return player.queue?.find((track) => track.trackId === trackId) || null
    }
  }
  return player.current || null
}

const selectEffectiveJukeboxPlaying = (state) => {
  return selectEffectiveJukeboxRemoteSession(state)?.playing ?? false
}

const selectEffectiveJukeboxGain = (state) => {
  return selectEffectiveJukeboxRemoteSession(state)?.gain ?? 0.5
}

const selectEffectiveJukeboxPosition = (state) => {
  return selectEffectiveJukeboxRemoteSession(state)?.position ?? 0
}

const selectEffectiveJukeboxCurrentIndex = (state) => {
  const player = selectPlayerState(state)
  const remote = selectEffectiveJukeboxRemoteSession(state)
  if (Number.isInteger(remote?.currentIndex)) {
    return remote.currentIndex
  }
  return player.queue.findIndex((item) => item.uuid === player.current?.uuid)
}

export {
  selectEffectiveCurrentTrack,
  selectEffectiveJukeboxCurrentIndex,
  selectEffectiveJukeboxGain,
  selectEffectiveJukeboxPlaying,
  selectEffectiveJukeboxPosition,
  selectEffectiveJukeboxRemoteSession,
}
