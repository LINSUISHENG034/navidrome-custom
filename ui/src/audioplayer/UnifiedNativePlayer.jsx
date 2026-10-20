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
import sortableConfig from 'navidrome-music-player/es/config/sortable.js'
import Sortable from 'sortablejs'
import { PlaybackStatus, PlaybackMode } from './playbackControlModule'
import { audioVolumeToUiVolume, uiVolumeToAudioVolume } from './volumeMapping'

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

    const nativeInitPlayer = this.initPlayer
    this.initPlayer = (...args) => {
      if (!this.props.controller && this.props.engineMode !== 'remote') {
        return nativeInitPlayer(...args)
      }
    }
    const nativeInitSortable = this.initSortableAudioLists
    this.initSortableAudioLists = () => {
      if (this.props.controller) {
        this.syncQueueSortable()
      } else if (this.props.engineMode !== 'remote') {
        nativeInitSortable()
      }
    }
    const nativeGetEnhanceAudio = this.getEnhanceAudio
    this.getEnhanceAudio = (...args) => {
      if (this.props.engineMode === 'remote') return null
      if (this.props.controller && this.enhancedAudioElement === this.audio) {
        return this.audio
      }
      const audio = nativeGetEnhanceAudio(...args)
      if (this.props.controller) this.enhancedAudioElement = audio
      return audio
    }
    const nativeOnGetAudioInstance = this.onGetAudioInstance
    this.onGetAudioInstance = () => {
      this.syncBrowserElement()
      return nativeOnGetAudioInstance()
    }

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
    if (this.controllerOverridesInstalled) return
    this.controllerOverridesInstalled = true

    this.onAudioMute = () => {
      this.props.controller?.setVolume(0).catch(() => {})
    }
    this.onResetVolume = () => {
      this.props.controller
        ?.setVolume(this.restoreVolume ?? this.props.defaultVolume ?? 1)
        .catch(() => {})
    }

    this.onProgressChange = (position) => {
      this.setState({ currentTime: position })
    }
    this.onAudioSeeked = (position) => {
      this.props.controller?.seek(position).catch(() => {})
    }

    this.onAudioSoundChange = (value) => {
      this.props.controller
        ?.setVolume(uiVolumeToAudioVolume(value))
        .catch(() => {})
    }

    this.onTogglePlay = () => {
      const { controller } = this.props
      if (!controller) return
      if (controller.getState().status === PlaybackStatus.PLAYING) {
        controller.pause().catch(() => {})
      } else {
        controller.play().catch(() => {})
      }
    }

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

    this.onAudioListsSortEnd = async (event) => {
      const drag = this.releaseQueueDrag()
      const { controller } = this.props
      if (!drag || !controller || this.isUnmounted) return
      const snapshot = controller.getState()
      const { oldIndex, newIndex } = event
      const sameView =
        drag.controller === controller &&
        drag.epoch === controller.getEpoch() &&
        drag.queueVersion === snapshot.queueVersion &&
        drag.ids.length === snapshot.queue.length &&
        drag.ids.every((id, index) => snapshot.queue[index].queueItemId === id)
      const validMove =
        sameView &&
        event.from === drag.container &&
        event.to === drag.container &&
        Number.isInteger(oldIndex) &&
        Number.isInteger(newIndex) &&
        oldIndex >= 0 &&
        newIndex >= 0 &&
        oldIndex < drag.ids.length &&
        newIndex < drag.ids.length &&
        oldIndex !== newIndex

      try {
        if (validMove) {
          await controller.moveQueueItem(
            drag.ids[oldIndex],
            newIndex,
            drag.queueVersion,
          )
        }
      } catch {
        // The Module owns the error; the view always returns to current authority.
      } finally {
        if (!this.isUnmounted && this.props.controller) {
          this.applyAuthoritativeState(this.props.controller.getState())
        }
      }
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
    // Remote-only execution guards; controlled inputs are shared by both engines.
    if (!this.props.controller) {
      this.onTogglePlay = () => {}
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

    this.loadAndPlayAudio = () => {}
    this.playAudio = () => {}
    this.resetAudioStatus = () => {}
    if (!this.props.controller) {
      this.audioListsPlay = () => {}
    }
    this.stopAll = () => {}
  }

  syncQueueSortable() {
    if (this.queueDrag || this.isUnmounted) return
    const { selector, ...defaults } = sortableConfig
    const container = this.player.current?.querySelector(`.${selector}`)
    const options = this.props.sortableOptions
    if (
      this.sortable?.el === container &&
      this.boundSortableOptions === options
    ) {
      return
    }
    this.destroyQueueSortable()
    if (!container) return

    this.boundSortableOptions = options
    this.sortable = new Sortable(container, {
      ...defaults,
      ...options,
      // The controlled contract is remove-and-insert, not the native Swap plugin.
      swap: false,
      onStart: (event) => {
        this.beginQueueDrag(event)
        options?.onStart?.(event)
      },
      onEnd: (event) => {
        const result = this.onAudioListsSortEnd(event)
        options?.onEnd?.(event)
        return result
      },
    })
  }

  destroyQueueSortable() {
    this.sortable?.destroy()
    this.sortable = null
    this.boundSortableOptions = null
  }

  beginQueueDrag(event) {
    this.releaseQueueDrag()
    if (!this.props.controller) return
    const container = event.from
    this.queueDrag = {
      controller: this.props.controller,
      epoch: this.state.displayedQueueEpoch,
      queueVersion: this.state.displayedQueueVersion,
      ids: this.state.audioLists.map((item) => item.queueItemId),
      container,
      nodes: Array.from(container.children),
    }
  }

  releaseQueueDrag() {
    const drag = this.queueDrag
    this.queueDrag = null
    if (drag?.container.isConnected) {
      // Restore React's last committed DOM before allowing keyed reconciliation.
      drag.nodes.forEach((node) => {
        if (node.parentNode) drag.container.appendChild(node)
      })
    }
    return drag
  }

  detachBrowserElement() {
    const binding = this.browserBinding
    this.browserBinding = null
    if (binding && binding.adapter.getAudioElement() === binding.element) {
      binding.adapter.attachElement(null)
    }
  }

  syncBrowserElement() {
    const adapter =
      this.props.engineMode !== 'remote' ? this.props.browserAdapter : null
    const element = adapter ? this.audio : null
    if (
      this.browserBinding?.adapter === adapter &&
      this.browserBinding?.element === element
    ) {
      return
    }
    this.detachBrowserElement()
    if (adapter && element) {
      adapter.attachElement(element)
      this.browserBinding = { adapter, element }
    }
  }

  UNSAFE_componentWillMount() {
    if (!this.props.controller && this.props.engineMode !== 'remote') {
      super.UNSAFE_componentWillMount()
    }
  }

  UNSAFE_componentWillReceiveProps(nextProps) {
    if (
      this.props.controller ||
      nextProps.controller ||
      this.props.engineMode === 'remote' ||
      nextProps.engineMode === 'remote'
    ) {
      this.updateTheme(nextProps.theme)
      this.updateMode(nextProps.mode)
      return
    }
    super.UNSAFE_componentWillReceiveProps(nextProps)
  }

  componentDidMount() {
    super.componentDidMount()

    if (this.props.engineMode === 'remote') {
      // In remote mode, clear loading state since there is no browser audio waiting to load
      this.setState({ loading: false })
    }

    if (this.props.controller) this.bindKeyDownEvents()
    this.subscribeToController()
  }

  subscribeToController() {
    this.controllerSubscription = null
    this.unsubscribeController?.()
    this.unsubscribeController = null
    const { controller } = this.props
    if (!controller) return

    const subscription = { controller }
    this.controllerSubscription = subscription
    this.unsubscribeController = controller.subscribe((state) => {
      if (this.controllerSubscription === subscription) {
        this.applyAuthoritativeState(state)
      }
    })
    this.applyAuthoritativeState(controller.getState())
  }

  componentDidUpdate(prevProps) {
    if (prevProps.controller !== this.props.controller) {
      this.subscribeToController()
    }
    if (
      prevProps.engineMode !== this.props.engineMode &&
      this.props.engineMode === 'remote'
    ) {
      this.setupRemoteModeOverrides()
      this.setState({ loading: false })
    }

    if (
      prevProps.engineMode !== this.props.engineMode ||
      prevProps.browserAdapter !== this.props.browserAdapter
    ) {
      this.onGetAudioInstance()
    }

    if (
      (prevProps.engineMode !== this.props.engineMode ||
        prevProps.controller !== this.props.controller) &&
      this.props.controller
    ) {
      this.setupControllerOverrides()
      this.applyAuthoritativeState(this.props.controller.getState())
    }
    if (this.props.controller) this.initSortableAudioLists()
  }

  componentWillUnmount() {
    this.isUnmounted = true
    this.releaseQueueDrag()
    this.destroyQueueSortable()
    this.controllerSubscription = null
    if (this.unsubscribeController) {
      this.unsubscribeController()
      this.unsubscribeController = null
    }
    this.detachBrowserElement()
    super.componentWillUnmount()
  }

  applyAuthoritativeState(authoritativeState) {
    const isPlaying = authoritativeState.status === PlaybackStatus.PLAYING
    const isLoading = authoritativeState.status === PlaybackStatus.PREPARING
    if (authoritativeState.volume > 0) {
      this.restoreVolume = authoritativeState.volume
    }

    // Use functional setState to ensure consecutive synchronous updates (e.g. preparing -> playing)
    // resolve cleanly against up-to-date state rather than stale closures.
    this.setState((prevState) => {
      const nextState = {}
      if (prevState.playbackError !== authoritativeState.error) {
        nextState.playbackError = authoritativeState.error
      }
      if (prevState.playing !== isPlaying) {
        nextState.playing = isPlaying
      }
      if (prevState.loading !== isLoading) {
        nextState.loading = isLoading
      }
      if (
        typeof authoritativeState.volume === 'number' &&
        audioVolumeToUiVolume(authoritativeState.volume) !==
          prevState.soundValue
      ) {
        nextState.soundValue = audioVolumeToUiVolume(authoritativeState.volume)
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
      if (Array.isArray(authoritativeState.queue) && !this.queueDrag) {
        const mappedLists = authoritativeState.queue.map((item) => ({
          ...item,
          __PLAYER_KEY__: item.queueItemId,
          uuid: item.queueItemId,
        }))
        nextState.audioLists = mappedLists
        nextState.displayedQueueEpoch = this.props.controller?.getEpoch()
        nextState.displayedQueueVersion = authoritativeState.queueVersion
      }
      if (authoritativeState.currentQueueItemId !== undefined) {
        nextState.playId = authoritativeState.currentQueueItemId
        const selected = authoritativeState.queue?.find(
          (item) => item.queueItemId === authoritativeState.currentQueueItemId,
        )
        for (const field of ['name', 'singer', 'cover', 'lyric']) {
          const value = selected?.[field] || ''
          if (prevState[field] !== value) nextState[field] = value
        }
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

    if (
      (this.props.controller || this.props.engineMode === 'remote') &&
      portal?.children
    ) {
      const rootDiv = portal.children
      const children = React.Children.map(rootDiv.props.children, (child) => {
        if (child?.type !== 'audio') return child
        if (this.props.engineMode === 'remote') return null
        // Preserve the native element/ref; only the Adapter may assign its source.
        return React.cloneElement(child, { src: undefined })
      })
      return ReactDOM.createPortal(
        React.cloneElement(
          rootDiv,
          {},
          children,
          this.state.playbackError ? (
            <div className="music-player-error" role="alert">
              {this.state.playbackError.message}
            </div>
          ) : null,
        ),
        portal.containerInfo,
      )
    }

    return portal
  }
}

export default UnifiedNativePlayer
