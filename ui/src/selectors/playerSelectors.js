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

export { selectEffectiveCurrentTrack }
