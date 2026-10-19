/**
 * Mock Remote Audio Adapter
 *
 * Core responsibility:
 * Concrete playback engine adapter providing deterministic remote playback simulation for verification.
 * Strictly enforces zero browser audio engine creation (no <audio>, no AudioContext, no media stream requests).
 * Confirms state via authoritative remote events and supports deterministic failure/permission/timeout injection.
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

export function createMockRemoteAudioAdapter(options = {}) {
  let activeEpoch = 0
  let isActive = false
  let stateCallback = null

  let remoteQueue = []
  let currentQueueItemId = null
  let queueVersion = 0
  let playMode = normalizePlaybackMode(
    options.initialPlayMode || PlaybackMode.ORDER,
  )
  const randomFn = options.randomFn || Math.random
  const wireCommands = []

  let remoteState = {
    status: PlaybackStatus.IDLE,
    position: options.initialPosition || 0,
    duration: options.initialDuration || 180,
    volume: options.initialVolume ?? 1,
  }

  // Configurable behavior for testing fault injection
  let failureConfig = options.failure || null
  let delayMs = options.delayMs || 0

  function activate(epoch, onStateChange) {
    activeEpoch = epoch
    isActive = true
    stateCallback = onStateChange
  }

  function deactivate() {
    isActive = false
    activeEpoch = -1
    stateCallback = null
    remoteQueue = []
    currentQueueItemId = null
    wireCommands.length = 0
  }

  function setFailure(config) {
    failureConfig = config
  }

  async function simulateAction(actionName, statePatch) {
    if (!isActive) return

    if (delayMs > 0) {
      await new Promise((resolve) => setTimeout(resolve, delayMs))
    }

    if (
      failureConfig &&
      (failureConfig.action === actionName || failureConfig.action === '*')
    ) {
      if (failureConfig.timeout) {
        // Hang to trigger command timeout
        await new Promise((resolve) =>
          setTimeout(resolve, failureConfig.timeoutMs || 10000),
        )
      }

      const err = {
        code: failureConfig.code || 'REMOTE_ACTION_FAILED',
        message:
          failureConfig.message || `Remote action ${actionName} was rejected`,
      }
      if (stateCallback) {
        stateCallback(activeEpoch, {
          status: PlaybackStatus.ERROR,
          error: err,
        })
      }
      throw err
    }

    const patch = typeof statePatch === 'function' ? statePatch() : statePatch
    remoteState = { ...remoteState, ...patch }
    if (stateCallback) {
      stateCallback(activeEpoch, patch)
    }
  }

  return {
    activate,
    deactivate,
    play: (epoch) =>
      simulateAction('play', () => ({
        status: PlaybackStatus.PLAYING,
        position: remoteState.position,
        duration: remoteState.duration,
        volume: remoteState.volume,
      })),
    pause: (epoch) =>
      simulateAction('pause', () => ({
        status: PlaybackStatus.PAUSED,
        position: remoteState.position,
      })),
    seek: (epoch, positionSeconds) =>
      simulateAction('seek', () => ({
        position: Math.max(0, Math.min(positionSeconds, remoteState.duration)),
      })),
    setVolume: (epoch, volumeFraction) =>
      simulateAction('volume', () => ({
        volume: Math.max(0, Math.min(1, volumeFraction)),
      })),
    setPlayMode: (epoch, mode) => {
      const normalized = normalizePlaybackMode(mode)
      wireCommands.push({
        action: 'setPlayMode',
        epoch,
        mode: normalized,
      })
      return simulateAction('setPlayMode', () => {
        playMode = normalized
        return { playMode }
      })
    },
    skipNext: (epoch) => {
      wireCommands.push({
        action: 'skipNext',
        epoch,
      })
      return simulateAction('skipNext', () => {
        const res = getNextTrack({
          queue: remoteQueue,
          currentQueueItemId,
          mode: playMode,
          isManual: true,
          randomFn,
        })
        if (!res.queueItemId) return {}
        currentQueueItemId = res.queueItemId
        return {
          currentQueueItemId,
          position: 0,
          status: PlaybackStatus.PLAYING,
        }
      })
    },
    skipPrev: (epoch) => {
      wireCommands.push({
        action: 'skipPrev',
        epoch,
      })
      return simulateAction('skipPrev', () => {
        // Authoritative server state holds remote playback position
        const curPos = remoteState.position
        const res = getPrevTrack({
          queue: remoteQueue,
          currentQueueItemId,
          mode: playMode,
          currentTime: curPos,
          randomFn,
        })
        if (res.rewindOnly) {
          return { position: 0 }
        }
        if (!res.queueItemId) return {}
        currentQueueItemId = res.queueItemId
        return {
          currentQueueItemId,
          position: 0,
          status: PlaybackStatus.PLAYING,
        }
      })
    },
    addQueueItems: (epoch, items = [], index = -1) => {
      wireCommands.push({
        action: 'add',
        epoch,
        items: items.map((i) => ({
          queueItemId: i.queueItemId || i.uuid,
          trackId: i.trackId,
        })),
        index,
      })

      return simulateAction('addQueueItems', () => {
        const res = applyQueueAdd(remoteQueue, currentQueueItemId, items, index)
        remoteQueue = res.queue
        currentQueueItemId = res.currentQueueItemId
        queueVersion += 1
        return {
          queue: [...remoteQueue],
          currentQueueItemId,
          queueVersion,
        }
      })
    },
    removeQueueItem: (epoch, queueItemId) => {
      wireCommands.push({
        action: 'remove',
        epoch,
        queueItemId,
      })

      return simulateAction('removeQueueItem', () => {
        const isPlaying = remoteState.status === PlaybackStatus.PLAYING
        const res = applyQueueRemove(
          remoteQueue,
          currentQueueItemId,
          queueItemId,
          isPlaying,
        )
        if (!res.removed) return {}

        remoteQueue = res.queue
        currentQueueItemId = res.currentQueueItemId
        queueVersion += 1

        return {
          queue: [...remoteQueue],
          currentQueueItemId: res.currentQueueItemId,
          queueVersion,
          ...(res.status ? { status: res.status, position: 0 } : {}),
        }
      })
    },
    moveQueueItem: (epoch, queueItemId, toIndex) => {
      wireCommands.push({
        action: 'move',
        epoch,
        queueItemId,
        toIndex,
      })

      return simulateAction('moveQueueItem', () => {
        const res = applyQueueMove(remoteQueue, queueItemId, toIndex)
        if (!res.moved) return {}

        remoteQueue = res.queue
        queueVersion += 1

        return {
          queue: [...remoteQueue],
          currentQueueItemId,
          queueVersion,
        }
      })
    },
    clearQueue: (epoch) => {
      wireCommands.push({
        action: 'clear',
        epoch,
      })

      return simulateAction('clearQueue', () => {
        const res = applyQueueClear()
        remoteQueue = res.queue
        currentQueueItemId = res.currentQueueItemId
        queueVersion += 1
        return {
          queue: [],
          currentQueueItemId: null,
          queueVersion,
          status: PlaybackStatus.IDLE,
          position: 0,
        }
      })
    },
    skipToQueueItem: (epoch, queueItemId) => {
      wireCommands.push({
        action: 'skip',
        epoch,
        queueItemId,
      })

      return simulateAction('skipToQueueItem', () => {
        currentQueueItemId = queueItemId
        return {
          currentQueueItemId,
          position: 0,
        }
      })
    },
    // Direct injection of authoritative server push (e.g. position progress or track change)
    pushServerSnapshot: (snapshot) => {
      if (Array.isArray(snapshot.queue)) {
        remoteQueue = [...snapshot.queue]
      }
      if (snapshot.currentQueueItemId !== undefined) {
        currentQueueItemId = snapshot.currentQueueItemId
      }
      if (typeof snapshot.queueVersion === 'number') {
        queueVersion = snapshot.queueVersion
      }
      if (snapshot.playMode !== undefined) {
        playMode = normalizePlaybackMode(snapshot.playMode)
      }
      remoteState = { ...remoteState, ...snapshot }
      if (isActive && stateCallback) {
        stateCallback(activeEpoch, snapshot)
      }
    },
    // Simulate autonomous server-side progression (continues even when UI is disconnected)
    triggerNaturalEnd: () => {
      const res = getNextTrack({
        queue: remoteQueue,
        currentQueueItemId,
        mode: playMode,
        isManual: false,
        randomFn,
      })
      if (res.shouldStop) {
        remoteState.status = PlaybackStatus.PAUSED
        remoteState.position = remoteState.duration
        if (isActive && stateCallback) {
          stateCallback(activeEpoch, {
            status: PlaybackStatus.PAUSED,
            position: remoteState.duration,
          })
        }
        return { stopped: true }
      }
      if (res.queueItemId) {
        currentQueueItemId = res.queueItemId
        remoteState.position = 0
        remoteState.status = PlaybackStatus.PLAYING
        if (isActive && stateCallback) {
          stateCallback(activeEpoch, {
            currentQueueItemId,
            position: 0,
            status: PlaybackStatus.PLAYING,
          })
        }
        return { stopped: false, currentQueueItemId }
      }
      return { stopped: false }
    },
    getWireCommands: () => [...wireCommands],
    clearWireCommands: () => {
      wireCommands.length = 0
    },
    getQueue: () => [...remoteQueue],
    getCurrentQueueItemId: () => currentQueueItemId,
    getPlayMode: () => playMode,
    setFailure,
    getRemoteState: () => ({ ...remoteState }),
  }
}
