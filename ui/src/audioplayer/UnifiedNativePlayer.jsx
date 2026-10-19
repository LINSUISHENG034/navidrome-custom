/**
 * Unified Native Player Component
 *
 * Core responsibility:
 * Native player UI component extending ReactJkMusicPlayer to support external engine mode.
 * Preserves the exact native controller DOM structure, CSS theme classes, progress/volume sliders,
 * and sortable playlist drawer.
 *
 * In remote mode, it excludes the <audio> element from the portal render tree, achieving
 * true zero-engine remote execution while routing user intents through PlaybackControlModule.
 *
 * Input Path Inventory (Issue 01 -> 02/03/05 Mapping):
 * - Primary Control Buttons (Play/Pause, Next, Prev, Volume, Seek): Handled in 02 by delegating
 *   to PlaybackControlModule intents (play, pause, seek, setVolume, skipNext, skipPrev).
 * - Queue Drawer Actions (Reorder, Delete Item, Clear): Handled in 03 via Unified Queue Contract.
 * - Global Hotkeys (Space, Arrow keys): Delegated in 05 to PlaybackControlModule.
 * - System Media Keys & MediaSession: Delegated in 05 to PlaybackControlModule.
 */

import React from 'react'
import ReactDOM from 'react-dom'
import ReactJkMusicPlayer from 'navidrome-music-player/es/index.js'
import {
  PlaybackStatus,
  PlaybackMode,
  normalizePlaybackMode,
} from './playbackControlModule'

/**
 * Shared helper to resolve stable queue item identity across UI and drawer representations.
 */
function resolveQueueItemId(identifier, lists = []) {
  if (!identifier) return null
  const item = lists.find(
    (a) =>
      a.queueItemId === identifier ||
      a.uuid === identifier ||
      a.__PLAYER_KEY__ === identifier,
  )
  return item?.queueItemId || item?.uuid || identifier
}

export class UnifiedNativePlayer extends ReactJkMusicPlayer {
  constructor(props) {
    super(props)

    const initialMode = props.controller
      ? props.controller.getState().playMode
      : props.playMode || props.defaultPlayMode

    this.state = {
      ...this.state,
      engineMode: props.engineMode || 'local',
      remoteDuration: props.duration || 0,
      ...(initialMode ? { playMode: initialMode } : {}),
    }

    if (props.engineMode === 'remote') {
      this.setupRemoteModeOverrides()
    }
    if (props.controller) {
      this.setupControllerOverrides()
    }
  }

  setupControllerOverrides() {
    // Intercept native play mode toggle to route through unified controller
    this.togglePlayMode = () => {
      const { controller } = this.props
      if (!controller) return
      const currentMode = controller.getState().playMode || PlaybackMode.ORDER
      const modeCycle = [
        PlaybackMode.ORDER,
        PlaybackMode.REPEAT_LIST,
        PlaybackMode.REPEAT_TRACK,
        PlaybackMode.SHUFFLE,
      ]
      const curIdx = modeCycle.indexOf(currentMode)
      const nextMode =
        modeCycle[(curIdx === -1 ? 0 : curIdx + 1) % modeCycle.length]
      controller.setPlayMode(nextMode).catch(() => {})
    }

    // Intercept native prev and next to route through unified controller
    this.onPlayNextAudio = () => {
      const { controller } = this.props
      if (controller) {
        controller.skipNext().catch(() => {})
      }
    }

    this.onPlayPrevAudio = () => {
      const { controller } = this.props
      if (controller) {
        controller.skipPrev().catch(() => {})
      }
    }

    // In controlled local/remote mode, natural end is handled authoritatively by the adapter
    this.onAudioEnd = () => {}

    // Intercept native queue drawer actions to route through unified controller
    this.onDeleteAudioLists = (audioId) => (e) => {
      if (e && typeof e.stopPropagation === 'function') {
        e.stopPropagation()
      }
      const { controller } = this.props
      if (!controller) return

      if (!audioId) {
        controller.clearQueue().catch(() => {})
        return
      }

      const currentLists = this.state.audioLists || []
      const targetQueueItemId = resolveQueueItemId(audioId, currentLists)
      controller.removeQueueItem(targetQueueItemId).catch(() => {})
    }

    this.onAudioListsSortEnd = (oldIndex, newIndex) => {
      const { controller } = this.props
      if (!controller) return
      if (oldIndex === newIndex) return

      const currentLists = this.state.audioLists || []
      const item = currentLists[oldIndex]
      if (!item) return
      const queueItemId = resolveQueueItemId(
        item.queueItemId || item.uuid || item.__PLAYER_KEY__,
        currentLists,
      )
      controller.moveQueueItem(queueItemId, newIndex).catch(() => {})
    }

    this.audioListsPlay = (playId, ignore) => {
      const { controller } = this.props
      if (!controller) return

      const currentLists = this.state.audioLists || []
      const targetQueueItemId = resolveQueueItemId(playId, currentLists)
      controller.skipToQueueItem(targetQueueItemId).catch(() => {})
    }
  }

  setupRemoteModeOverrides() {
    // 1. User Intent Interceptors: forward to unified controller without optimistic drift
    this.onTogglePlay = () => {
      const { controller } = this.props
      if (!controller) return

      const currentState = controller.getState()
      if (currentState.status === PlaybackStatus.PLAYING) {
        controller.pause().catch(() => {})
      } else {
        controller.play().catch(() => {})
      }
    }

    this.onPlayNextAudio = () => {
      const { controller } = this.props
      if (controller) {
        controller.skipNext().catch(() => {})
      }
    }

    this.onPlayPrevAudio = () => {
      const { controller } = this.props
      if (controller) {
        controller.skipPrev().catch(() => {})
      }
    }

    // Spec compliance: forward intent to controller without optimistic UI mutation;
    // UI updates when authoritative state is confirmed/backfilled.
    this.onAudioSoundChange = (value) => {
      const { controller } = this.props
      if (controller) {
        controller.setVolume(value / 100).catch(() => {})
      }
    }

    this.onProgressChange = (progress) => {
      const { controller } = this.props
      if (controller) {
        controller.seek(progress).catch(() => {})
      }
    }

    // 2. Safe overrides for methods that access this.audio in native class
    this.setAudioVolume = (volumeBarValue) => {
      this.setState({
        currentAudioVolume: volumeBarValue,
        soundValue: volumeBarValue,
      })
    }

    this.setDefaultAudioVolume = () => {
      const defaultVolumeValue = this.props.defaultVolume ?? 1
      this.setAudioVolume(defaultVolumeValue * 100)
    }

    this.onResetVolume = () => {
      const restoreValue = this.state.currentAudioVolume || 100
      const { controller } = this.props
      if (controller) {
        controller.setVolume(restoreValue / 100).catch(() => {})
      }
    }

    this.onAudioMute = () => {
      const { controller } = this.props
      if (controller) {
        controller.setVolume(0).catch(() => {})
      }
    }

    this.loadAndPlayAudio = () => {}
    this.playAudio = () => {}
    this.resetAudioStatus = () => {}
    if (!this.props.controller) {
      this.audioListsPlay = () => {}
    }
    this.stopAll = () => {}

    // Zero facade: remote mode does not return a fake HTMLAudioElement
    this.getEnhanceAudio = () => null
  }

  onGetAudioInstance() {
    super.onGetAudioInstance()
    // In local mode, attach the native player's real media element to the browser adapter
    if (
      this.props.engineMode === 'local' &&
      this.props.browserAdapter &&
      this.audio
    ) {
      this.props.browserAdapter.attachElement(this.audio)
    }
  }

  componentDidMount() {
    super.componentDidMount()

    if (
      this.props.engineMode === 'local' &&
      this.props.browserAdapter &&
      this.audio
    ) {
      this.props.browserAdapter.attachElement(this.audio)
    }

    if (this.props.engineMode === 'remote') {
      // In remote mode, clear loading state since there is no browser audio waiting to load
      this.setState({ loading: false })
    }

    const { controller } = this.props
    if (controller && typeof controller.subscribe === 'function') {
      this.applyAuthoritativeState(controller.getState())
      this.unsubscribeController = controller.subscribe(
        (authoritativeState) => {
          this.applyAuthoritativeState(authoritativeState)
        },
      )
    }
  }

  componentDidUpdate(prevProps) {
    if (
      prevProps.engineMode !== this.props.engineMode &&
      this.props.engineMode === 'remote'
    ) {
      this.setupRemoteModeOverrides()
      this.setState({ loading: false })
    }

    if (
      (prevProps.engineMode !== this.props.engineMode ||
        prevProps.browserAdapter !== this.props.browserAdapter) &&
      this.props.engineMode === 'local' &&
      this.props.browserAdapter &&
      this.audio
    ) {
      this.props.browserAdapter.attachElement(this.audio)
    }

    if (
      (prevProps.engineMode !== this.props.engineMode ||
        prevProps.controller !== this.props.controller) &&
      this.props.controller
    ) {
      this.setupControllerOverrides()
      this.applyAuthoritativeState(this.props.controller.getState())
    }
  }

  componentWillUnmount() {
    if (this.unsubscribeController) {
      this.unsubscribeController()
      this.unsubscribeController = null
    }
    super.componentWillUnmount()
  }

  applyAuthoritativeState(authoritativeState) {
    const isPlaying = authoritativeState.status === PlaybackStatus.PLAYING
    const isLoading = authoritativeState.status === PlaybackStatus.PREPARING

    // Use functional setState to ensure consecutive synchronous updates (e.g. preparing -> playing)
    // resolve cleanly against up-to-date state rather than stale closures.
    this.setState((prevState) => {
      const nextState = {}
      if (prevState.playing !== isPlaying) {
        nextState.playing = isPlaying
      }
      if (prevState.loading !== isLoading) {
        nextState.loading = isLoading
      }
      if (
        typeof authoritativeState.volume === 'number' &&
        Math.round(authoritativeState.volume * 100) !== prevState.soundValue
      ) {
        nextState.soundValue = Math.round(authoritativeState.volume * 100)
      }
      if (
        typeof authoritativeState.position === 'number' &&
        Math.abs(authoritativeState.position - (prevState.currentTime || 0)) >
          0.5
      ) {
        nextState.currentTime = authoritativeState.position
      }
      if (
        typeof authoritativeState.duration === 'number' &&
        authoritativeState.duration !== prevState.remoteDuration
      ) {
        nextState.remoteDuration = authoritativeState.duration
      }
      if (Array.isArray(authoritativeState.queue)) {
        const mappedLists = authoritativeState.queue.map((item) => ({
          ...item,
          __PLAYER_KEY__: item.queueItemId,
          uuid: item.queueItemId,
        }))
        nextState.audioLists = mappedLists
      }
      if (authoritativeState.currentQueueItemId !== undefined) {
        nextState.playId = authoritativeState.currentQueueItemId
      }
      if (
        authoritativeState.playMode !== undefined &&
        authoritativeState.playMode !== prevState.playMode
      ) {
        nextState.playMode = authoritativeState.playMode
      }

      return Object.keys(nextState).length > 0 ? nextState : null
    })
  }

  get audioDuration() {
    if (this.props.engineMode === 'remote') {
      return this.state.remoteDuration || this.props.duration || 0
    }
    return super.audioDuration
  }

  render() {
    const portal = super.render()

    if (this.props.engineMode === 'remote' && portal && portal.children) {
      const rootDiv = portal.children
      if (rootDiv && rootDiv.props && Array.isArray(rootDiv.props.children)) {
        // Strip out the <audio> element to enforce zero audio engine creation
        const filteredChildren = rootDiv.props.children.filter(
          (child) => child && child.type !== 'audio',
        )
        const newRootDiv = React.cloneElement(rootDiv, {}, filteredChildren)
        return ReactDOM.createPortal(newRootDiv, portal.containerInfo)
      }
    }

    return portal
  }
}

export default UnifiedNativePlayer
