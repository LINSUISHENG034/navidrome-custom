/**
 * Browser Audio Adapter
 *
 * Owns local media execution and translates confirmed media results into the
 * same authoritative state contract as the remote adapter.
 */

import {
  PlaybackStatus,
  PlaybackMode,
  CommandResultStatus,
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
  let explicitlyDetached = Boolean(
    options.unattached || options.audioElement === null,
  )
  let audioElement = options.unattached
    ? null
    : options.audioElement !== undefined
      ? options.audioElement
      : null
  let ownsElement = false
  if (!audioElement && !explicitlyDetached) {
    audioElement = document.createElement('audio')
    audioElement.className = 'navidrome-browser-audio-engine'
    ownsElement = true
  }
  let boundElement = null
  let boundEpoch = null
  let resourceGeneration = 0
  const eventListeners = new Map()
  const operations = new Map()
  const reservations = new Map()
  let activeReservation = null

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
    resourceReleased = false
    invalidateAttempt()
    currentQueueItemId = targetQueueItemId
    desiredPlaying = play

    if (play && !audioElement) {
      const failure = {
        code: 'NO_AUDIO_ELEMENT',
        message: 'The local player is not attached to a media element',
      }
      observePlaybackFailure(failure)
      throw failure
    }

    const el = audioElement
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
      position:
        el && restart
          ? 0
          : el
            ? mediaState(el).position
            : localState.position || 0,
      error: null,
    })

    if (!el) {
      attempt.pending = false
      return
    }

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
      if (el) {
        try {
          el.pause()
        } catch {
          // ignore pause failure
        }
      }
      const failure = {
        code:
          typeof error?.code === 'string'
            ? error.code
            : 'BROWSER_PLAY_REQUEST_FAILED',
        message: error?.message || 'The browser could not start playback',
        name: error?.name,
      }
      observePlaybackFailure(failure)
      throw failure
    }
  }

  function handleMediaEvent(eventName, el, epoch, eventResGen) {
    if (!isActive || el !== audioElement || epoch !== activeEpoch) return
    if (eventResGen !== resourceGeneration) return
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
        if (!el.error || resourceReleased) break
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
    const currentResGen = resourceGeneration
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
      const listener = () => handleMediaEvent(name, el, epoch, currentResGen)
      eventListeners.set(name, listener)
      el.addEventListener(name, listener)
    })
  }

  function releaseElement() {
    resourceReleased = true
    resourceGeneration += 1
    unbindEvents()
    if (!audioElement) return
    audioElement.pause()
    audioElement.removeAttribute('src')
    if (typeof audioElement.load === 'function') audioElement.load()
    loadedQueueItemId = null
  }

  function attachElement(el) {
    if (audioElement === el && el !== null) {
      if (isActive && el) bindEvents(el)
      return
    }

    if (!el) {
      if (activeReservation) {
        activeReservation.status = 'INVALIDATED'
        activeReservation = null
      }
      desiredPlaying = false
      invalidateAttempt()
      releaseElement()
      audioElement = null
      explicitlyDetached = true
      return
    }

    explicitlyDetached = false
    const previousElement = audioElement
    if (previousElement) {
      unbindEvents()
    }
    audioElement = el

    if (activeReservation && activeReservation.status === 'WAITING') {
      activeReservation.resolveReady(el)
    }

    if (isActive) {
      bindEvents(el)
      if (currentQueueItemId) {
        const target = queue.find(
          (item) => item.queueItemId === currentQueueItemId,
        )
        if (
          target &&
          loadedQueueItemId !== currentQueueItemId &&
          desiredPlaying
        ) {
          let source =
            typeof target.musicSrc === 'function'
              ? target.musicSrc()
              : target.musicSrc
          if (typeof source === 'string' && source) {
            el.src = source
            loadedQueueItemId = currentQueueItemId
          }
        }
        if (localState.position > 0) {
          el.currentTime = localState.position
        }
        publishState({
          ...mediaState(el),
          status: desiredPlaying
            ? PlaybackStatus.PLAYING
            : PlaybackStatus.PAUSED,
          error: null,
        })
      } else {
        publishState({
          ...mediaState(el),
          status: PlaybackStatus.IDLE,
          error: null,
        })
      }
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
    if (audioElement) bindEvents(audioElement)
    publishState(audioElement ? mediaState(audioElement) : {})
  }

  function deactivate() {
    isActive = false
    activeEpoch = -1
    resourceReleased = true
    resourceGeneration += 1
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

  async function play(epoch, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    const opId = options.operationId
    try {
      const res = await selectQueueItem(epoch, currentQueueItemId, {
        play: true,
        restart: false,
      })
      if (opId) {
        operations.set(opId, {
          status: CommandResultStatus.APPLIED,
          operationId: opId,
        })
      }
      return res
    } catch (err) {
      if (opId) {
        operations.set(opId, {
          status: CommandResultStatus.FAILED,
          error: err,
          operationId: opId,
        })
      }
      throw err
    }
  }

  async function pauseOutput(epoch, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    const opId = options.operationId
    desiredPlaying = false
    invalidateAttempt()
    if (audioElement) {
      audioElement.pause()
    }
    publishState({
      status: PlaybackStatus.PAUSED,
      position: audioElement
        ? mediaState(audioElement).position
        : localState.position || 0,
      error: null,
    })
    if (opId) {
      operations.set(opId, {
        status: CommandResultStatus.APPLIED,
        operationId: opId,
      })
    }
  }

  async function pause(epoch, options = {}) {
    return pauseOutput(epoch, options)
  }

  async function stop(epoch, options = {}) {
    return pauseOutput(epoch, options)
  }

  async function seek(epoch, positionSeconds, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    const opId = options.operationId
    if (audioElement) {
      audioElement.currentTime = Math.max(0, positionSeconds)
      publishState({ position: mediaState(audioElement).position })
    } else {
      localState.position = Math.max(0, positionSeconds)
      publishState({ position: localState.position })
    }
    if (opId) {
      operations.set(opId, {
        status: CommandResultStatus.APPLIED,
        operationId: opId,
      })
    }
  }

  async function setVolume(epoch, volumeFraction, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    const opId = options.operationId
    const vol = Math.max(0, Math.min(1, volumeFraction))
    if (audioElement) {
      audioElement.volume = vol
      publishState({ volume: audioElement.volume, isMuted: audioElement.muted })
    } else {
      localState.volume = vol
      publishState({ volume: vol, isMuted: localState.isMuted })
    }
    if (opId) {
      operations.set(opId, {
        status: CommandResultStatus.APPLIED,
        operationId: opId,
      })
    }
  }

  async function setMute(epoch, isMuted, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    const opId = options.operationId
    const muted = Boolean(isMuted)
    if (audioElement) {
      audioElement.muted = muted
      publishState({ volume: audioElement.volume, isMuted: audioElement.muted })
    } else {
      localState.isMuted = muted
      publishState({ volume: localState.volume, isMuted: muted })
    }
    if (opId) {
      operations.set(opId, {
        status: CommandResultStatus.APPLIED,
        operationId: opId,
      })
    }
  }

  async function setPlayMode(epoch, mode, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    const opId = options.operationId
    playMode = normalizePlaybackMode(mode)
    publishState()
    if (opId) {
      operations.set(opId, {
        status: CommandResultStatus.APPLIED,
        operationId: opId,
      })
    }
  }

  async function reserveMigration(operationId, options = {}) {
    const { migrationId, signal, readinessTimeoutMs = 3000 } = options

    if (signal?.aborted) {
      const cancelErr = {
        code: 'MIGRATION_CANCELLED',
        message: 'Migration was cancelled before reservation',
        migrationId,
        operationId,
      }
      if (operationId) {
        operations.set(operationId, {
          status: CommandResultStatus.FAILED,
          error: cancelErr,
          operationId,
        })
      }
      throw cancelErr
    }

    if (audioElement && !explicitlyDetached) {
      const res = {
        status: CommandResultStatus.APPLIED,
        migrationId,
        operationId,
      }
      activeReservation = {
        migrationId,
        operationId,
        status: 'ACTIVE',
        element: audioElement,
      }
      if (migrationId) reservations.set(migrationId, activeReservation)
      if (operationId) operations.set(operationId, res)
      return res
    }

    return new Promise((resolve, reject) => {
      let timer = null
      let onAbort = null

      const cleanup = () => {
        if (timer) clearTimeout(timer)
        if (signal && onAbort) signal.removeEventListener('abort', onAbort)
      }

      timer = setTimeout(() => {
        cleanup()
        if (activeReservation?.migrationId === migrationId) {
          activeReservation = null
        }
        const err = {
          code: 'TARGET_NOT_READY',
          message: `Browser audio element was not attached within ${readinessTimeoutMs}ms`,
          status: CommandResultStatus.FAILED,
          migrationId,
          operationId,
        }
        if (operationId) {
          operations.set(operationId, {
            status: CommandResultStatus.FAILED,
            error: err,
            operationId,
          })
        }
        reject(err)
      }, readinessTimeoutMs)

      let pendingRes = null
      if (signal) {
        onAbort = () => {
          cleanup()
          if (pendingRes) {
            pendingRes.status = 'CANCELLED'
          }
          if (activeReservation?.migrationId === migrationId) {
            activeReservation = null
          }
          const err = {
            code: 'MIGRATION_CANCELLED',
            message:
              'Migration was cancelled while waiting for browser readiness',
            migrationId,
            operationId,
          }
          if (operationId) {
            operations.set(operationId, {
              status: CommandResultStatus.FAILED,
              error: err,
              operationId,
            })
          }
          reject(err)
        }
        signal.addEventListener('abort', onAbort, { once: true })
      }

      pendingRes = {
        migrationId,
        operationId,
        status: 'WAITING',
        resolveReady: (el) => {
          cleanup()
          pendingRes.status = 'ACTIVE'
          pendingRes.element = el
          activeReservation = pendingRes
          if (migrationId) reservations.set(migrationId, pendingRes)
          const res = {
            status: CommandResultStatus.APPLIED,
            migrationId,
            operationId,
          }
          if (operationId) operations.set(operationId, res)
          resolve(res)
        },
      }
      activeReservation = pendingRes
      if (migrationId) reservations.set(migrationId, pendingRes)
    })
  }

  async function releaseMigrationReservation(
    operationId,
    { migrationId } = {},
  ) {
    const res =
      (migrationId && reservations.get(migrationId)) || activeReservation
    if (res && (!migrationId || res.migrationId === migrationId)) {
      res.status = 'RELEASED'
      if (activeReservation?.migrationId === res.migrationId) {
        activeReservation = null
      }
    }
    const result = {
      status: CommandResultStatus.APPLIED,
      migrationId,
      operationId,
    }
    if (operationId) operations.set(operationId, result)
    return result
  }

  async function queryOperation(operationId) {
    if (!operationId) return { status: CommandResultStatus.UNKNOWN }
    const record = operations.get(operationId)
    if (record) return record
    return { status: CommandResultStatus.UNKNOWN, operationId }
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

  async function recordCommand(options, execute) {
    const operationId = options.operationId
    try {
      const result = await execute()
      if (operationId) {
        operations.set(operationId, {
          status: CommandResultStatus.APPLIED,
          operationId,
        })
      }
      return result
    } catch (error) {
      if (operationId) {
        operations.set(operationId, {
          status: CommandResultStatus.FAILED,
          operationId,
          error,
        })
      }
      throw error
    }
  }

  async function addQueueItems(epoch, items = [], index = -1, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    return recordCommand(options, () => {
      const result = applyQueueAdd(queue, currentQueueItemId, items, index)
      queue = result.queue
      currentQueueItemId = result.currentQueueItemId
      queueVersion += 1
      publishState()
    })
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

  async function clearQueue(epoch, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    return recordCommand(options, () => {
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
    })
  }

  async function skipToQueueItem(epoch, queueItemId, options = {}) {
    if (!isActive || epoch !== activeEpoch) return
    if (!queue.some((item) => item.queueItemId === queueItemId)) return
    const wasPlaying = desiredPlaying || (audioElement && !audioElement.paused)
    return recordCommand(options, () =>
      selectQueueItem(epoch, queueItemId, { play: Boolean(wasPlaying) }),
    )
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
    setMute,
    skipNext,
    skipPrev,
    setPlayMode,
    addQueueItems,
    removeQueueItem,
    moveQueueItem,
    clearQueue,
    skipToQueueItem,
    reserveMigration,
    releaseMigrationReservation,
    queryOperation,
    getReservations: () => new Map(reservations),
    getQueue: () => [...queue],
    getCurrentQueueItemId: () => currentQueueItemId,
    getPlayMode: () => playMode,
    getAudioElement: () => audioElement,
  }
}
