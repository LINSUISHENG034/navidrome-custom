import jukeboxClient from './jukeboxClient'
import { clamp01 } from './volumeMapping'

const keyHandlers = (audioInstance, playerState) => {
  const nextSong = () => {
    const idx = playerState.queue.findIndex(
      (item) => item.uuid === playerState.current.uuid,
    )
    return idx !== null ? playerState.queue[idx + 1] : null
  }

  const prevSong = () => {
    const idx = playerState.queue.findIndex(
      (item) => item.uuid === playerState.current.uuid,
    )
    return idx !== null ? playerState.queue[idx - 1] : null
  }

  const isJukebox = playerState.jukeboxMode

  return {
    TOGGLE_PLAY: (e) => {
      e.preventDefault()
      if (isJukebox) {
        const status = playerState.jukeboxStatus
        if (status?.playing) {
          jukeboxClient.pause().catch(() => {})
        } else {
          jukeboxClient.play().catch(() => {})
        }
      } else {
        audioInstance && audioInstance.togglePlay()
      }
    },
    VOL_UP: () => {
      if (isJukebox) {
        const current = playerState.jukeboxStatus?.gain ?? 0.5
        jukeboxClient.volume(clamp01(current + 0.1)).catch(() => {})
      } else {
        audioInstance.volume = Math.min(1, audioInstance.volume + 0.1)
      }
    },
    VOL_DOWN: () => {
      if (isJukebox) {
        const current = playerState.jukeboxStatus?.gain ?? 0.5
        jukeboxClient.volume(clamp01(current - 0.1)).catch(() => {})
      } else {
        audioInstance.volume = Math.max(0, audioInstance.volume - 0.1)
      }
    },
    PREV_SONG: (e) => {
      if (isJukebox) {
        const idx = playerState.queue.findIndex(
          (item) => item.uuid === playerState.current?.uuid,
        )
        if (idx > 0) {
          jukeboxClient.skip(idx - 1, 0).catch(() => {})
        }
      } else {
        if (!e.metaKey && prevSong()) audioInstance && audioInstance.playPrev()
      }
    },
    CURRENT_SONG: () => {
      window.location.href = `#/album/${playerState.current?.song.albumId}/show`
    },
    NEXT_SONG: (e) => {
      if (isJukebox) {
        const idx = playerState.queue.findIndex(
          (item) => item.uuid === playerState.current?.uuid,
        )
        if (idx >= 0 && idx < playerState.queue.length - 1) {
          jukeboxClient.skip(idx + 1, 0).catch(() => {})
        }
      } else {
        if (!e.metaKey && nextSong()) audioInstance && audioInstance.playNext()
      }
    },
  }
}

export default keyHandlers
