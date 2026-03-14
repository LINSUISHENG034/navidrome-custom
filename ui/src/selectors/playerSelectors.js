const selectPlayerState = (state) => state.player || {}

const selectJukeboxAuthority = (player) =>
  player.jukeboxRemote || player.jukeboxSession || null

const selectEffectiveCurrentTrack = (state) => {
  const player = selectPlayerState(state)
  const authority = selectJukeboxAuthority(player)
  if (player.jukeboxMode && authority) {
    const { currentIndex, trackId } = authority
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
  return selectJukeboxAuthority(player)?.playing ?? player.jukeboxStatus?.playing ?? false
}

const selectEffectiveJukeboxGain = (state) => {
  const player = selectPlayerState(state)
  return selectJukeboxAuthority(player)?.gain ?? player.jukeboxStatus?.gain ?? 0.5
}

const selectEffectiveJukeboxPosition = (state) => {
  const player = selectPlayerState(state)
  return selectJukeboxAuthority(player)?.position ?? player.jukeboxStatus?.position ?? 0
}

const selectEffectiveJukeboxCurrentIndex = (state) => {
  const player = selectPlayerState(state)
  const authority = selectJukeboxAuthority(player)
  if (Number.isInteger(authority?.currentIndex)) {
    return authority.currentIndex
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
