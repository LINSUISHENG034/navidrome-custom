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
import { SPACE_BAR_KEYCODE } from 'navidrome-music-player/es/config/keycode.js'
import Sortable from 'sortablejs'
import { PlaybackStatus, PlaybackMode } from './playbackControlModule'
import { audioVolumeToUiVolume, uiVolumeToAudioVolume } from './volumeMapping'

const MEDIA_SESSION_ACTIONS = [
  'play',
  'pause',
  'previoustrack',
  'nexttrack',
  'seekto',
  'seekbackward',
  'seekforward',
]

const DEFAULT_MEDIA_SESSION_SKIP_S = 10

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
        this.refreshAuthoritativeState()
      }
    }

    this.audioListsPlay = (playId, ignore) => {
      const { controller } = this.props
      if (!controller) return

      const currentLists = this.state.audioLists || []
      const targetQueueItemId = resolveQueueItemId(playId, currentLists)
      controller.skipToQueueItem(targetQueueItemId).catch(() => {})
    }

    // Space keydown: issue the toggle once. preventDefault stops a focused
    // button's click activation; stopPropagation keeps a document-level host
    // hotkey (Player.jsx GlobalHotKeys -> audioInstance.togglePlay) from
    // re-issuing the same intent for the same key press.
    this.onKeyDown = (e) => {
      if (this.props.spaceBar && e.keyCode === SPACE_BAR_KEYCODE) {
        e.preventDefault()
        e.stopPropagation()
        this.onTogglePlay()
      }
    }

    // Media session position state reads the authoritative scene, never a
    // media element (there is none in remote mode).
    this.updateMediaSessionPositionState = () => {
      if (
        typeof navigator === 'undefined' ||
        !('mediaSession' in navigator) ||
        !('setPositionState' in navigator.mediaSession)
      ) {
        return
      }
      const authority = this.props.controller?.getState()
      try {
        navigator.mediaSession.setPositionState({
          duration:
            typeof authority?.duration === 'number' && authority.duration > 0
              ? authority.duration
              : this.audioDuration,
          playbackRate: 1,
          position:
            typeof authority?.position === 'number' ? authority.position : 0,
        })
      } catch (error) {
        // Same tolerance as the native class: invalid ranges are ignored.
      }
    }
  }

  syncControlledMediaSession() {
    const supported =
      typeof navigator !== 'undefined' && 'mediaSession' in navigator
    const { controller, showMediaSession } = this.props
    const shouldBind = Boolean(controller && showMediaSession && supported)
    const binding = this.controlledMediaSession
    if (binding && (!shouldBind || binding.controller !== controller)) {
      binding.release()
      this.controlledMediaSession = null
    }
    if (!shouldBind || this.controlledMediaSession?.controller === controller) {
      return
    }
    const setHandler = (action, handler) => {
      try {
        navigator.mediaSession.setActionHandler(action, handler)
      } catch (error) {
        // The browser does not support this action; skip it like the native class.
      }
    }
    const intent =
      (dispatch) =>
      (...args) => {
        if (this.props.controller !== controller) return
        try {
          const result = dispatch(...args)
          if (result && typeof result.catch === 'function') {
            result.catch(() => {})
          }
        } catch (error) {
          // Handler errors must not escape into the UA media session dispatch.
        }
      }
    // Keep native parity: the play/pause actions toggle through the same
    // controlled entry the native onAddMediaSession would have captured.
    setHandler(
      'play',
      intent(() => this.onTogglePlay()),
    )
    setHandler(
      'pause',
      intent(() => this.onTogglePlay()),
    )
    setHandler(
      'previoustrack',
      intent(() => controller.skipPrev()),
    )
    setHandler(
      'nexttrack',
      intent(() => controller.skipNext()),
    )
    setHandler(
      'seekto',
      intent((details) =>
        typeof details?.seekTime === 'number'
          ? controller.seek(details.seekTime)
          : undefined,
      ),
    )
    const seekBy = (details, direction) => {
      const authority = controller.getState()
      const offset = details?.seekOffset || DEFAULT_MEDIA_SESSION_SKIP_S
      const position = authority.position || 0
      const max =
        typeof authority.duration === 'number' && authority.duration > 0
          ? authority.duration
          : Infinity
      return controller.seek(
        Math.min(Math.max(position + direction * offset, 0), max),
      )
    }
    setHandler(
      'seekbackward',
      intent((details) => seekBy(details, -1)),
    )
    setHandler(
      'seekforward',
      intent((details) => seekBy(details, 1)),
    )
    this.controlledMediaSession = {
      controller,
      release: () => {
        MEDIA_SESSION_ACTIONS.forEach((action) => {
          try {
            navigator.mediaSession.setActionHandler(action, null)
          } catch (error) {
            // Ignore: releasing an unsupported action.
          }
        })
      },
    }
  }

  releaseControlledMediaSession() {
    const binding = this.controlledMediaSession
    this.controlledMediaSession = null
    binding?.release()
  }

  updateLyricPosition(timeMs) {
    if (!this.lyric) return
    if (typeof this.lyric.update === 'function') {
      this.lyric.update(timeMs)
      return
    }
    const lines = this.lyric.lines || []
    if (!lines.length) return
    let lo = 0
    let hi = lines.length - 1
    let active = -1
    while (lo <= hi) {
      const mid = Math.floor((lo + hi) / 2)
      if (lines[mid].time <= timeMs) {
        active = mid
        lo = mid + 1
      } else {
        hi = mid - 1
      }
    }
    if (active === this.lyric._lastActive) return
    this.lyric._lastActive = active
    const handler =
      typeof this.lyric.handler === 'function'
        ? this.lyric.handler
        : this.onLyricChange
    if (active < 0) {
      handler?.call(this.lyric, { txt: '', lineNum: -1 })
    } else {
      handler?.call(this.lyric, {
        txt: lines[active].txt ?? lines[active].text ?? '',
        lineNum: active,
      })
    }
  }

  // Derived display projections that follow the authoritative scene:
  // lyric line highlighting and media session metadata/position. Neither
  // issues intents; lyric sync runs even below the seekbar's 0.5s dead-band.
  // Runs in the setState callback, so this.state is the post-projection view.
  syncAuthoritativeDerivedState(update, prevSnapshot) {
    if (this.isUnmounted || !this.props.controller) return
    const lyricChanged = this.state.lyric !== prevSnapshot.lyric
    if (lyricChanged) {
      this.initLyricParser()
      const position =
        typeof update.position === 'number'
          ? update.position
          : this.props.controller.getState().position
      if (typeof position === 'number') {
        this.updateLyricPosition(position * 1000)
      }
    } else if (typeof update.position === 'number') {
      this.updateLyricPosition(update.position * 1000)
    }
    if (
      this.props.showMediaSession &&
      typeof navigator !== 'undefined' &&
      'mediaSession' in navigator
    ) {
      const metaChanged = ['name', 'singer', 'cover'].some(
        (field) => this.state[field] !== prevSnapshot[field],
      )
      if (metaChanged) {
        this.updateMediaSessionMetadata()
      } else if (
        typeof update.position === 'number' ||
        typeof update.status === 'string'
      ) {
        this.updateMediaSessionPositionState()
      }
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
    const binding = {}
    this.sortableBinding = binding
    binding.instance = new Sortable(container, {
      ...defaults,
      ...options,
      // The controlled contract is remove-and-insert, not the native Swap plugin.
      swap: false,
      onStart: (event) => {
        if (this.sortableBinding !== binding) return
        this.beginQueueDrag(event)
        try {
          options?.onStart?.call(binding.instance, event)
        } catch (error) {
          this.cancelQueueDrag()
          this.refreshAuthoritativeState()
          throw error
        }
      },
      onEnd: (event) => {
        if (this.sortableBinding !== binding) return
        const result = this.onAudioListsSortEnd(event)
        options?.onEnd?.call(binding.instance, event)
        return result
      },
    })
    this.sortable = binding.instance
  }

  destroyQueueSortable() {
    const sortable = this.sortable
    this.sortableBinding = null
    this.sortable = null
    this.boundSortableOptions = null
    if (!sortable) return
    sortable.destroy()
    // Sortable's destroy() sets its own `el` to null before the internal drop
    // teardown finishes; a late document-level drop/dragend that still targets
    // the instance would read a null el. Neutralize the stale instance's own
    // handleEvent so control-loss unmounts stay error-free.
    sortable.handleEvent = () => {}
  }

  cancelQueueDrag() {
    this.releaseQueueDrag()
    this.destroyQueueSortable()
  }

  refreshAuthoritativeState() {
    if (!this.isUnmounted && this.props.controller) {
      this.applyAuthoritativeState(this.props.controller.getState())
    }
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
    this.syncControlledMediaSession()
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
      prevProps.controller !== this.props.controller ||
      prevProps.showMediaSession !== this.props.showMediaSession
    ) {
      this.syncControlledMediaSession()
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
    this.cancelQueueDrag()
    this.releaseControlledMediaSession()
    this.controllerSubscription = null
    if (this.unsubscribeController) {
      this.unsubscribeController()
      this.unsubscribeController = null
    }
    this.detachBrowserElement()
    super.componentWillUnmount()
  }

  applyAuthoritativeState(authoritativeState) {
    const controller = this.props.controller
    const epoch = controller?.getEpoch()
    if (
      this.queueDrag &&
      (authoritativeState.error ||
        this.queueDrag.controller !== controller ||
        this.queueDrag.epoch !== epoch)
    ) {
      this.cancelQueueDrag()
    }
    const isPlaying = authoritativeState.status === PlaybackStatus.PLAYING
    const isLoading = authoritativeState.status === PlaybackStatus.PREPARING
    if (authoritativeState.volume > 0) {
      this.restoreVolume = authoritativeState.volume
    }

    // Snapshot of the pre-update view fields for the derived-state diff.
    const prevSnapshot = {
      lyric: this.state.lyric,
      name: this.state.name,
      singer: this.state.singer,
      cover: this.state.cover,
    }

    // Use functional setState to ensure consecutive synchronous updates (e.g. preparing -> playing)
    // resolve cleanly against up-to-date state rather than stale closures.
    this.setState(
      (prevState) => {
        if (
          controller !== this.props.controller ||
          epoch !== controller?.getEpoch()
        ) {
          return null
        }
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
          nextState.soundValue = audioVolumeToUiVolume(
            authoritativeState.volume,
          )
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
          nextState.displayedQueueEpoch = epoch
          nextState.displayedQueueVersion = authoritativeState.queueVersion
        }
        if (authoritativeState.currentQueueItemId !== undefined) {
          nextState.playId = authoritativeState.currentQueueItemId
          const selected = authoritativeState.queue?.find(
            (item) =>
              item.queueItemId === authoritativeState.currentQueueItemId,
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
      },
      () => {
        this.syncAuthoritativeDerivedState(authoritativeState, prevSnapshot)
      },
    )
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
