import jukeboxClient from './jukeboxClient'
import { canControlJukebox } from './jukeboxSession'
import { PlaybackStatus } from './playbackControlModule'
import { stepRemoteGainByUiDelta } from './volumeProfiles'
import {
  selectEffectiveCurrentTrack,
  selectEffectiveJukeboxCurrentIndex,
  selectEffectiveJukeboxGain,
  selectEffectiveJukeboxPlaying,
} from '../selectors/playerSelectors'

const VOLUME_KEY_STEP = 0.1

const keyHandlers = (audioInstance, playerState, playbackController = null) => {
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

  // Unified candidate path (Issue 05): when a playback controller is supplied,
  // every playback hotkey enters the Module exactly once; the audio element and
  // the jukebox client are no longer command targets. Production callers pass
  // no controller and keep the existing behavior below.
  if (playbackController) {
    return {
      TOGGLE_PLAY: (e) => {
        e.preventDefault()
        const { status } = playbackController.getState()
        const intent =
          status === PlaybackStatus.PLAYING
            ? playbackController.pause
            : playbackController.play
        intent.call(playbackController).catch(() => {})
      },
      VOL_UP: () => {
        const { volume } = playbackController.getState()
        playbackController
          .setVolume(Math.min(1, volume + VOLUME_KEY_STEP))
          .catch(() => {})
      },
      VOL_DOWN: () => {
        const { volume } = playbackController.getState()
        playbackController
          .setVolume(Math.max(0, volume - VOLUME_KEY_STEP))
          .catch(() => {})
      },
      PREV_SONG: (e) => {
        if (!e.metaKey) playbackController.skipPrev().catch(() => {})
      },
      CURRENT_SONG: () => {
        window.location.href = `#/album/${effectiveCurrentTrack?.song.albumId}/show`
      },
      NEXT_SONG: (e) => {
        if (!e.metaKey) playbackController.skipNext().catch(() => {})
      },
    }
  }

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
        jukeboxClient
          .volume(
            stepRemoteGainByUiDelta(effectiveJukeboxGain, 0.1, playerState),
          )
          .catch(() => {})
      } else {
        audioInstance.volume = Math.min(1, audioInstance.volume + 0.1)
      }
    },
    VOL_DOWN: () => {
      if (isJukebox) {
        if (!canControl) return
        jukeboxClient
          .volume(
            stepRemoteGainByUiDelta(effectiveJukeboxGain, -0.1, playerState),
          )
          .catch(() => {})
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
