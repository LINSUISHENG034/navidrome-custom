import jukeboxClient from './jukeboxClient'
import { canControlJukebox } from './jukeboxSession'
import { clamp01 } from './volumeMapping'
import {
  selectEffectiveCurrentTrack,
  selectEffectiveJukeboxCurrentIndex,
  selectEffectiveJukeboxGain,
  selectEffectiveJukeboxPlaying,
} from '../selectors/playerSelectors'

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
  const canControl = canControlJukebox(playerState)
  const state = { player: playerState }
  const effectiveCurrentTrack = selectEffectiveCurrentTrack(state)
  const effectiveCurrentIndex = selectEffectiveJukeboxCurrentIndex(state)
  const effectiveJukeboxPlaying = selectEffectiveJukeboxPlaying(state)
  const effectiveJukeboxGain = selectEffectiveJukeboxGain(state)

  return {
    TOGGLE_PLAY: (e) => {
      e.preventDefault()
      if (isJukebox) {
        if (!canControl) return
        if (effectiveJukeboxPlaying) {
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
        if (!canControl) return
        jukeboxClient.volume(clamp01(effectiveJukeboxGain + 0.1)).catch(() => {})
      } else {
        audioInstance.volume = Math.min(1, audioInstance.volume + 0.1)
      }
    },
    VOL_DOWN: () => {
      if (isJukebox) {
        if (!canControl) return
        jukeboxClient.volume(clamp01(effectiveJukeboxGain - 0.1)).catch(() => {})
      } else {
        audioInstance.volume = Math.max(0, audioInstance.volume - 0.1)
      }
    },
    PREV_SONG: (e) => {
      if (isJukebox) {
        if (!canControl) return
        if (effectiveCurrentIndex > 0) {
          jukeboxClient.skip(effectiveCurrentIndex - 1, 0).catch(() => {})
        }
      } else {
        if (!e.metaKey && prevSong()) audioInstance && audioInstance.playPrev()
      }
    },
    CURRENT_SONG: () => {
      window.location.href = `#/album/${effectiveCurrentTrack?.song.albumId}/show`
    },
    NEXT_SONG: (e) => {
      if (isJukebox) {
        if (!canControl) return
        if (
          effectiveCurrentIndex >= 0 &&
          effectiveCurrentIndex < playerState.queue.length - 1
        ) {
          jukeboxClient.skip(effectiveCurrentIndex + 1, 0).catch(() => {})
        }
      } else {
        if (!e.metaKey && nextSong()) audioInstance && audioInstance.playNext()
      }
    },
  }
}

export default keyHandlers
