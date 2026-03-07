const selectPlayerState = (state) => state.player || {}

const selectEffectiveCurrentTrack = (state) => {
  const player = selectPlayerState(state)
  if (player.jukeboxMode && player.jukeboxSession) {
    const { currentIndex, trackId } = player.jukeboxSession
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
  const player = selectPlayerState(state)
  return player.jukeboxSession?.playing ?? player.jukeboxStatus?.playing ?? false
}

const selectEffectiveJukeboxGain = (state) => {
  const player = selectPlayerState(state)
  return player.jukeboxSession?.gain ?? player.jukeboxStatus?.gain ?? 0.5
}

const selectEffectiveJukeboxPosition = (state) => {
  const player = selectPlayerState(state)
  return player.jukeboxSession?.position ?? player.jukeboxStatus?.position ?? 0
}

const selectEffectiveJukeboxCurrentIndex = (state) => {
  const player = selectPlayerState(state)
  if (Number.isInteger(player.jukeboxSession?.currentIndex)) {
    return player.jukeboxSession.currentIndex
  }
  return player.queue.findIndex((item) => item.uuid === player.current?.uuid)
}

export {
  selectEffectiveCurrentTrack,
  selectEffectiveJukeboxCurrentIndex,
  selectEffectiveJukeboxGain,
  selectEffectiveJukeboxPlaying,
  selectEffectiveJukeboxPosition,
}
