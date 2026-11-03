import jukeboxClient from './jukeboxClient'
import { canControlJukebox } from './jukeboxSession'
import { PlaybackStatus } from './playbackControlModule'
import { stepRemoteGainByUiDelta } from './volumeProfiles'
import { stepUiVolume } from './volumeMapping'
import {
  selectEffectiveCurrentTrack,
  selectEffectiveJukeboxCurrentIndex,
  selectEffectiveJukeboxGain,
  selectEffectiveJukeboxPlaying,
} from '../selectors/playerSelectors'

const VOLUME_KEY_STEP = 0.1

const keyHandlers = (
  audioInstance,
  playerState,
  playbackController = null,
  getLiveState = null,
) => {
  // Remote-authority reads must happen at invocation time: a takeover or
  // downgrade can land after these handlers were created. Callers that can
  // supply the current state (fourth argument) get invocation-time reads;
  // legacy callers keep the creation-time snapshot.
  const liveState = () => getLiveState?.() || playerState
  const readRemoteAuthority = () => {
    const current = liveState()
    const state = { player: current }
    return {
      current,
      isJukebox: current?.jukeboxMode,
      canControl: canControlJukebox(current),
      effectiveCurrentTrack: selectEffectiveCurrentTrack(state),
      effectiveCurrentIndex: selectEffectiveJukeboxCurrentIndex(state),
      effectiveJukeboxPlaying: selectEffectiveJukeboxPlaying(state),
      effectiveJukeboxGain: selectEffectiveJukeboxGain(state),
    }
  }

  const nextSong = () => {
    const current = liveState()
    const idx = current.queue.findIndex(
      (item) => item.uuid === current.current.uuid,
    )
    return idx !== null ? current.queue[idx + 1] : null
  }

  const prevSong = () => {
    const current = liveState()
    const idx = current.queue.findIndex(
      (item) => item.uuid === current.current.uuid,
    )
    return idx !== null ? current.queue[idx - 1] : null
  }

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
          .setVolume(stepUiVolume(volume, VOLUME_KEY_STEP))
          .catch(() => {})
      },
      VOL_DOWN: () => {
        const { volume } = playbackController.getState()
        playbackController
          .setVolume(stepUiVolume(volume, -VOLUME_KEY_STEP))
          .catch(() => {})
      },
      PREV_SONG: (e) => {
        if (!e.metaKey) playbackController.skipPrev().catch(() => {})
      },
      CURRENT_SONG: () => {
        const { effectiveCurrentTrack } = readRemoteAuthority()
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
      const { isJukebox, canControl, effectiveJukeboxPlaying } =
        readRemoteAuthority()
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
      const { isJukebox, canControl, effectiveJukeboxGain } =
        readRemoteAuthority()
      if (isJukebox) {
        if (!canControl) return
        jukeboxClient
          .volume(
            stepRemoteGainByUiDelta(effectiveJukeboxGain, 0.1, liveState()),
          )
          .catch(() => {})
      } else {
        audioInstance.volume = stepUiVolume(
          audioInstance.volume,
          VOLUME_KEY_STEP,
        )
      }
    },
    VOL_DOWN: () => {
      const { isJukebox, canControl, effectiveJukeboxGain } =
        readRemoteAuthority()
      if (isJukebox) {
        if (!canControl) return
        jukeboxClient
          .volume(
            stepRemoteGainByUiDelta(effectiveJukeboxGain, -0.1, liveState()),
          )
          .catch(() => {})
      } else {
        audioInstance.volume = stepUiVolume(
          audioInstance.volume,
          -VOLUME_KEY_STEP,
        )
      }
    },
    PREV_SONG: (e) => {
      const { isJukebox, canControl, effectiveCurrentIndex } =
        readRemoteAuthority()
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
      const { effectiveCurrentTrack } = readRemoteAuthority()
      window.location.href = `#/album/${effectiveCurrentTrack?.song.albumId}/show`
    },
    NEXT_SONG: (e) => {
      const { current, isJukebox, canControl, effectiveCurrentIndex } =
        readRemoteAuthority()
      if (isJukebox) {
        if (!canControl) return
        // The boundary is the ACCEPTED remote playlist, never the browser
        // queue: server-side queue length is the only authoritative count.
        const remotePlaylistLength =
          current?.jukeboxPlaylist?.entry?.length ?? 0
        if (
          effectiveCurrentIndex >= 0 &&
          effectiveCurrentIndex < remotePlaylistLength - 1
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
