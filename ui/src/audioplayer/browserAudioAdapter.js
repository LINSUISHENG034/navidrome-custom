/**
 * Browser Audio Adapter
 *
 * Owns local media execution and translates confirmed media results into the
 * same authoritative state contract as the remote adapter.
 */

import {
  PlaybackStatus,
  PlaybackMode,
  normalizePlaybackMode,
  getNextTrack,
  getPrevTrack,
  applyQueueAdd,
  applyQueueRemove,
  applyQueueMove,
  applyQueueClear,
} from './playbackControlModule'

export function createBrowserAudioAdapter(options = {}) {
  let activeEpoch = 0
  let isActive = false
  let stateCallback = null
  let audioElement = options.audioElement || null
  let ownsElement = false
  let explicitlyDetached = false
  let boundElement = null
  let boundEpoch = null
  const eventListeners = new Map()

  let queue = []
  let currentQueueItemId = null
  let loadedQueueItemId = null
  let resourceReleased = false
  let queueVersion = 0
  let stateSequence = 0
  let playMode = normalizePlaybackMode(
    options.initialPlayMode || PlaybackMode.ORDER,
  )
  const randomFn = options.randomFn || Math.random
  let desiredPlaying = false
  let activeAttempt = null
  let localState = {
    status: PlaybackStatus.IDLE,
    position: 0,
    duration: 0,
    volume: options.initialVolume ?? 1,
    isMuted: false,
    error: null,
  }

  function publishState(patch = {}) {
    localState = { ...localState, ...patch }
    if (!isActive || !stateCallback) return
    stateCallback(activeEpoch, {
      ...localState,
      queue: [...queue],
      currentQueueItemId,
      queueVersion,
      playMode,
      stateSequence: ++stateSequence,
    })
  }

  function mediaState(el) {
    return {
      position: Number.isFinite(el.currentTime) ? el.currentTime : 0,
      duration: Number.isFinite(el.duration) ? el.duration : 0,
      volume: el.volume,
      isMuted: el.muted,
    }
  }

  function ensureAudioElement() {
    if (!audioElement && !explicitlyDetached) {
      audioElement = document.createElement('audio')
      audioElement.className = 'navidrome-browser-audio-engine'
      ownsElement = true
    }
    return audioElement
  }

  function requireAudioElement() {
    const el = ensureAudioElement()
    if (!el) {
      throw {
        code: 'NO_AUDIO_ELEMENT',
        message: 'The local player is not attached to a media element',
      }
    }
    return el
  }

  function supersededError() {
    return {
      code: 'COMMAND_SUPERSEDED',
      message: 'The local playback request was superseded',
    }
  }

  function invalidateAttempt() {
    const previous = activeAttempt
    activeAttempt = null
    if (previous?.rejectPlaying) previous.rejectPlaying(supersededError())
  }

  function isCurrentAttempt(attempt) {
    return (
      isActive &&
      activeEpoch === attempt.epoch &&
      audioElement === attempt.element &&
      currentQueueItemId === attempt.queueItemId &&
      activeAttempt === attempt
    )
  }

  function assertCurrentAttempt(attempt) {
    if (!isCurrentAttempt(attempt)) throw supersededError()
  }

  function observePlaybackFailure(error) {
    // Selection commands report to their caller as well. Natural-end work has
    // no caller, so its rejection is observed here through authoritative state.
    if (error.code !== 'COMMAND_SUPERSEDED' && localState.error !== error) {
      desiredPlaying = false
      invalidateAttempt()
      publishState({ status: PlaybackStatus.ERROR, error })
    }
  }

  async function selectQueueItem(
    epoch,
    targetQueueItemId,
    { play = false, restart = true } = {},
  ) {
    if (!isActive || epoch !== activeEpoch) return
    const el = requireAudioElement()
    resourceReleased = false
    invalidateAttempt()
    currentQueueItemId = targetQueueItemId
    desiredPlaying = play
    const attempt = {
      epoch,
      element: el,
      queueItemId: targetQueueItemId,
      pending: true,
      playingObserved: false,
    }
    activeAttempt = attempt
    publishState({
      status: play ? PlaybackStatus.PREPARING : PlaybackStatus.PAUSED,
      position: restart ? 0 : mediaState(el).position,
      error: null,
    })

    try {
      assertCurrentAttempt(attempt)
      const target = queue.find(
        (item) => item.queueItemId === targetQueueItemId,
      )
      const needsSource =
        target &&
        (loadedQueueItemId !== targetQueueItemId || !el.getAttribute('src'))
      if (needsSource) {
        let source =
          typeof target.musicSrc === 'function'
            ? target.musicSrc()
            : target.musicSrc
        if (source && typeof source.then === 'function') source = await source
        assertCurrentAttempt(attempt)
        if (typeof source !== 'string' || !source) {
          throw {
            code: 'MEDIA_SOURCE_UNAVAILABLE',
            message: 'The selected queue item did not resolve to a media URL',
          }
        }
        el.src = source
        loadedQueueItemId = targetQueueItemId
      }
      if (restart) el.currentTime = 0
      assertCurrentAttempt(attempt)
      if (play) {
        const result = el.play()
        if (result && typeof result.then === 'function') {
          await result
        } else if (!attempt.playingObserved && (el.paused || el.ended)) {
          // Legacy engines return no Promise; only wait for a 'playing'
          // event when the element does not already report playback,
          // otherwise an already-playing element would hang the attempt.
          await new Promise((resolve, reject) => {
            attempt.resolvePlaying = resolve
            attempt.rejectPlaying = reject
          })
        }
        assertCurrentAttempt(attempt)
        attempt.pending = false
        publishState({
          ...mediaState(el),
          status: PlaybackStatus.PLAYING,
          error: null,
        })
      } else {
        el.pause()
        assertCurrentAttempt(attempt)
        attempt.pending = false
        publishState({
          ...mediaState(el),
          status: PlaybackStatus.PAUSED,
          error: null,
        })
      }
    } catch (error) {
      if (!isCurrentAttempt(attempt)) throw supersededError()
      desiredPlaying = false
      invalidateAttempt()
      el.pause()
      const failure = {
        code:
          typeof error?.code === 'string'
            ? error.code
            : 'BROWSER_PLAY_REQUEST_FAILED',
        message: error?.message || 'The browser could not start playback',
      }
      observePlaybackFailure(failure)
      throw failure
    }
  }

  function handleMediaEvent(eventName, el, epoch) {
    if (!isActive || el !== audioElement || epoch !== activeEpoch) return
    if (resourceReleased && eventName !== 'volumechange') return
    if (
      eventName !== 'volumechange' &&
      currentQueueItemId !== null &&
      loadedQueueItemId !== currentQueueItemId
    )
      return
    switch (eventName) {
      case 'play':
        // This event means play was requested, not that playback has started.
        break
      case 'playing':
        if (activeAttempt && desiredPlaying) {
          activeAttempt.playingObserved = true
          activeAttempt.resolvePlaying?.()
          if (!activeAttempt.pending) {
            publishState({ ...mediaState(el), status: PlaybackStatus.PLAYING })
          }
        }
        break
      case 'pause':
        if (!el.paused) break
        // Natural completion may queue pause before ended on the same resource.
        if (
          el.ended &&
          desiredPlaying &&
          activeAttempt &&
          !activeAttempt.pending
        )
          break
        if (
          localState.status !== PlaybackStatus.ERROR &&
          !(desiredPlaying && activeAttempt?.pending)
        ) {
          desiredPlaying = false
          publishState({
            status: PlaybackStatus.PAUSED,
            position: mediaState(el).position,
          })
        }
        break
      case 'loadedmetadata':
      case 'durationchange':
      case 'seeked':
      case 'timeupdate':
        publishState(mediaState(el))
        break
      case 'volumechange':
        publishState({ volume: el.volume, isMuted: el.muted })
        break
      case 'ended': {
        if (
          !el.ended ||
          !desiredPlaying ||
          !activeAttempt ||
          activeAttempt.pending ||
          !isCurrentAttempt(activeAttempt)
        )
          return
        const result = getNextTrack({
          queue,
          currentQueueItemId,
          mode: playMode,
          isManual: false,
          randomFn,
        })
        if (result.shouldStop) {
          desiredPlaying = false
          invalidateAttempt()
          publishState({
            status: PlaybackStatus.PAUSED,
            position: el.duration || el.currentTime || 0,
          })
        } else if (result.queueItemId) {
          selectQueueItem(epoch, result.queueItemId, { play: true }).catch(
            observePlaybackFailure,
          )
        }
        break
      }
      case 'error': {
        if (!el.error) break
        const error = {
          code: 'MEDIA_ELEMENT_ERROR',
          message:
            el.error?.message || 'Audio element encountered playback error',
        }
        observePlaybackFailure(error)
        break
      }
      default:
        break
    }
  }

  function unbindEvents() {
    if (!boundElement) return
    eventListeners.forEach((listener, name) =>
      boundElement.removeEventListener(name, listener),
    )
    eventListeners.clear()
    boundElement = null
    boundEpoch = null
  }

  function bindEvents(el) {
    if (!el || (boundElement === el && boundEpoch === activeEpoch)) return
    unbindEvents()
    boundElement = el
    boundEpoch = activeEpoch
    const epoch = activeEpoch
    const events = [
      'play',
      'playing',
      'pause',
      'loadedmetadata',
      'durationchange',
      'seeked',
      'timeupdate',
      'volumechange',
      'ended',
      'error',
    ]
    events.forEach((name) => {
      const listener = () => handleMediaEvent(name, el, epoch)
      eventListeners.set(name, listener)
      el.addEventListener(name, listener)
    })
  }

  function releaseElement() {
    unbindEvents()
    if (!audioElement) return
    audioElement.pause()
    audioElement.removeAttribute('src')
    if (typeof audioElement.load === 'function') audioElement.load()
    if (ownsElement && audioElement.parentNode) {
      audioElement.parentNode.removeChild(audioElement)
    }
    loadedQueueItemId = null
  }

  function attachElement(el) {
    if (audioElement === el) {
      if (isActive && el) bindEvents(el)
      return
    }
    desiredPlaying = false
    invalidateAttempt()
    releaseElement()
    audioElement = el
    ownsElement = false
    explicitlyDetached = !el
    if (isActive) {
      if (el) bindEvents(el)
      publishState({
        ...(el ? mediaState(el) : { position: 0, duration: 0 }),
        status: currentQueueItemId
          ? PlaybackStatus.PAUSED
          : PlaybackStatus.IDLE,
        error: null,
      })
    }
  }

  function activate(epoch, onStateChange) {
    if (activeEpoch !== epoch) {
      invalidateAttempt()
      unbindEvents()
      stateSequence = 0
    }
    activeEpoch = epoch
    isActive = true
    stateCallback = onStateChange
    const el = ensureAudioElement()
    if (el) bindEvents(el)
    publishState(el ? mediaState(el) : {})
  }

  function deactivate() {
    isActive = false
    activeEpoch = -1
    desiredPlaying = false
    invalidateAttempt()
    releaseElement()
    queue = []
    currentQueueItemId = null
    localState = {
      ...localState,
      status: PlaybackStatus.IDLE,
      position: 0,
      duration: 0,
      error: null,
    }
    stateCallback = null
  }

  async function play(epoch) {
    if (!isActive || epoch !== activeEpoch) return
    return selectQueueItem(epoch, currentQueueItemId, {
      play: true,
      restart: false,
    })
  }

  async function pause(epoch) {
    if (!isActive || epoch !== activeEpoch) return
    desiredPlaying = false
    invalidateAttempt()
    const el = requireAudioElement()
    el.pause()
    publishState({
      status: PlaybackStatus.PAUSED,
      position: mediaState(el).position,
      error: null,
    })
  }

  async function stop(epoch) {
    if (!isActive || epoch !== activeEpoch) return
    desiredPlaying = false
    invalidateAttempt()
    if (audioElement) {
      audioElement.pause()
    }
    publishState({
      status: PlaybackStatus.PAUSED,
      position: audioElement ? mediaState(audioElement).position : 0,
      error: null,
    })
  }

  async function seek(epoch, positionSeconds) {
    if (!isActive || epoch !== activeEpoch) return
    const el = requireAudioElement()
    el.currentTime = Math.max(0, positionSeconds)
    publishState({ position: mediaState(el).position })
  }

  async function setVolume(epoch, volumeFraction) {
    if (!isActive || epoch !== activeEpoch) return
    const el = requireAudioElement()
    el.volume = Math.max(0, Math.min(1, volumeFraction))
    publishState({ volume: el.volume, isMuted: el.muted })
  }

  async function setPlayMode(epoch, mode) {
    if (!isActive || epoch !== activeEpoch) return
    playMode = normalizePlaybackMode(mode)
    publishState()
  }

  async function skipNext(epoch) {
    if (!isActive || epoch !== activeEpoch) return
    const result = getNextTrack({
      queue,
      currentQueueItemId,
      mode: playMode,
      isManual: true,
      randomFn,
    })
    if (result.queueItemId)
      return selectQueueItem(epoch, result.queueItemId, { play: true })
  }

  async function skipPrev(epoch, currentPos) {
    if (!isActive || epoch !== activeEpoch) return
    const el = requireAudioElement()
    const position = Number.isFinite(el.currentTime)
      ? el.currentTime
      : currentPos || 0
    const result = getPrevTrack({
      queue,
      currentQueueItemId,
      mode: playMode,
      currentTime: position,
      randomFn,
    })
    if (result.rewindOnly) {
      el.currentTime = 0
      publishState({ position: 0 })
    } else if (result.queueItemId) {
      return selectQueueItem(epoch, result.queueItemId, { play: true })
    }
  }

  async function addQueueItems(epoch, items = [], index = -1) {
    if (!isActive || epoch !== activeEpoch) return
    const result = applyQueueAdd(queue, currentQueueItemId, items, index)
    queue = result.queue
    currentQueueItemId = result.currentQueueItemId
    queueVersion += 1
    publishState()
  }

  async function removeQueueItem(epoch, queueItemId) {
    if (!isActive || epoch !== activeEpoch) return
    const wasPlaying = desiredPlaying || (audioElement && !audioElement.paused)
    const result = applyQueueRemove(
      queue,
      currentQueueItemId,
      queueItemId,
      wasPlaying,
    )
    if (!result.removed) return
    queue = result.queue
    currentQueueItemId = result.currentQueueItemId
    queueVersion += 1
    if (result.status === PlaybackStatus.IDLE) {
      resourceReleased = true
      desiredPlaying = false
      invalidateAttempt()
      if (audioElement) {
        audioElement.pause()
        audioElement.removeAttribute('src')
      }
      loadedQueueItemId = null
      publishState({ status: PlaybackStatus.IDLE, position: 0, error: null })
    } else if (result.successor) {
      return selectQueueItem(epoch, currentQueueItemId, {
        play: Boolean(wasPlaying),
      })
    } else {
      publishState()
    }
  }

  async function moveQueueItem(
    epoch,
    queueItemId,
    toIndex,
    expectedQueueVersion = queueVersion,
  ) {
    if (!isActive || epoch !== activeEpoch) return
    const result = applyQueueMove(queue, queueItemId, toIndex, {
      expectedQueueVersion,
      queueVersion,
    })
    if (!result.moved) return
    queue = result.queue
    queueVersion += 1
    publishState()
  }

  async function clearQueue(epoch) {
    if (!isActive || epoch !== activeEpoch) return
    const result = applyQueueClear()
    resourceReleased = true
    desiredPlaying = false
    invalidateAttempt()
    queue = result.queue
    currentQueueItemId = result.currentQueueItemId
    queueVersion += 1
    if (audioElement) {
      audioElement.pause()
      audioElement.removeAttribute('src')
    }
    loadedQueueItemId = null
    publishState({ status: PlaybackStatus.IDLE, position: 0, error: null })
  }

  async function skipToQueueItem(epoch, queueItemId) {
    if (!isActive || epoch !== activeEpoch) return
    if (!queue.some((item) => item.queueItemId === queueItemId)) return
    const wasPlaying = desiredPlaying || (audioElement && !audioElement.paused)
    return selectQueueItem(epoch, queueItemId, { play: Boolean(wasPlaying) })
  }

  return {
    activate,
    deactivate,
    attachElement,
    play,
    pause,
    stop,
    seek,
    setVolume,
    skipNext,
    skipPrev,
    setPlayMode,
    addQueueItems,
    removeQueueItem,
    moveQueueItem,
    clearQueue,
    skipToQueueItem,
    getQueue: () => [...queue],
    getCurrentQueueItemId: () => currentQueueItemId,
    getPlayMode: () => playMode,
    getAudioElement: () => audioElement,
  }
}
