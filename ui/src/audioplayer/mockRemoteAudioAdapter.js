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
  let stateSequence = 0
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
    error: null,
  }

  // Configurable behavior for testing fault injection
  let failureConfig = options.failure || null
  let delayMs = options.delayMs || 0

  let hasActiveSession = Boolean(options.hasActiveSession)
  let activeSessionOwner =
    options.activeSessionOwner || (hasActiveSession ? 'other-client' : null)

  function publishState(patch = {}, sequence = stateSequence + 1) {
    remoteState = { ...remoteState, ...patch }
    stateSequence = sequence
    if (isActive && stateCallback) {
      stateCallback(activeEpoch, {
        ...remoteState,
        queue: [...remoteQueue],
        currentQueueItemId,
        queueVersion,
        playMode,
        stateSequence,
      })
    }
  }

  function activate(epoch, onStateChange) {
    activeEpoch = epoch
    isActive = true
    stateCallback = onStateChange
    stateSequence = 0
    publishState()
  }

  function deactivate() {
    isActive = false
    activeEpoch = -1
    stateCallback = null
    remoteQueue = []
    currentQueueItemId = null
    remoteState = {
      ...remoteState,
      status: PlaybackStatus.IDLE,
      position: 0,
      error: null,
    }
    wireCommands.length = 0
  }

  function setFailure(config) {
    failureConfig = config
  }

  function recordCommand(command) {
    if (isActive && command.epoch === activeEpoch) wireCommands.push(command)
  }

  async function simulateAction(
    epoch,
    actionName,
    statePatch,
    commandExtra = {},
    wireAction = actionName,
  ) {
    if (!isActive || epoch !== activeEpoch) return
    recordCommand({ action: wireAction, epoch, ...commandExtra })
    const callback = stateCallback
    const failure =
      failureConfig &&
      (failureConfig.action === actionName ||
        failureConfig.action === wireAction ||
        failureConfig.action === '*')
        ? { ...failureConfig }
        : null
    const assertCurrent = () => {
      if (!isActive || activeEpoch !== epoch || stateCallback !== callback) {
        throw {
          code: 'COMMAND_SUPERSEDED',
          message: 'The remote operation belongs to an inactive playback scene',
        }
      }
    }

    if (delayMs > 0) {
      await new Promise((resolve) => setTimeout(resolve, delayMs))
    }
    assertCurrent()

    if (failure) {
      if (failure.timeout) {
        await new Promise((resolve) =>
          setTimeout(resolve, failure.timeoutMs || 10000),
        )
      }
      assertCurrent()
      const err = {
        code: failure.code || 'REMOTE_ACTION_FAILED',
        message: failure.message || `Remote action ${actionName} was rejected`,
      }
      publishState({ status: PlaybackStatus.ERROR, error: err })
      throw err
    }

    const patch = typeof statePatch === 'function' ? statePatch() : statePatch
    publishState({ ...patch, error: null })
  }

  return {
    activate,
    deactivate,
    play: (epoch) =>
      simulateAction(epoch, 'play', () => ({
        status: PlaybackStatus.PLAYING,
        position: remoteState.position,
        duration: remoteState.duration,
        volume: remoteState.volume,
      })),
    pause: (epoch) =>
      simulateAction(epoch, 'pause', () => ({
        status: PlaybackStatus.PAUSED,
        position: remoteState.position,
      })),
    stop: (epoch) =>
      simulateAction(epoch, 'stop', () => ({
        status: PlaybackStatus.PAUSED,
        position: remoteState.position,
      })),
    seek: (epoch, positionSeconds) =>
      simulateAction(
        epoch,
        'seek',
        () => ({
          position: Math.max(
            0,
            Math.min(positionSeconds, remoteState.duration),
          ),
        }),
        { position: positionSeconds },
      ),
    setVolume: (epoch, volumeFraction) =>
      simulateAction(
        epoch,
        'volume',
        () => ({
          volume: Math.max(0, Math.min(1, volumeFraction)),
        }),
        { volume: volumeFraction },
      ),
    setPlayMode: (epoch, mode) => {
      const normalized = normalizePlaybackMode(mode)
      return simulateAction(
        epoch,
        'setPlayMode',
        () => {
          playMode = normalized
          return { playMode }
        },
        { mode: normalized },
      )
    },
    skipNext: (epoch) =>
      simulateAction(epoch, 'skipNext', () => {
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
      }),
    skipPrev: (epoch) =>
      simulateAction(epoch, 'skipPrev', () => {
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
      }),
    addQueueItems: (epoch, items = [], index = -1) =>
      simulateAction(
        epoch,
        'addQueueItems',
        () => {
          const res = applyQueueAdd(
            remoteQueue,
            currentQueueItemId,
            items,
            index,
          )
          remoteQueue = res.queue
          currentQueueItemId = res.currentQueueItemId
          queueVersion += 1
          return {
            queue: [...remoteQueue],
            currentQueueItemId,
            queueVersion,
          }
        },
        {
          items: items.map((i) => ({
            queueItemId: i.queueItemId || i.uuid,
            trackId: i.trackId,
          })),
          index,
        },
        'add',
      ),
    removeQueueItem: (epoch, queueItemId) =>
      simulateAction(
        epoch,
        'removeQueueItem',
        () => {
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
        },
        { queueItemId },
        'remove',
      ),
    moveQueueItem: (
      epoch,
      queueItemId,
      toIndex,
      expectedQueueVersion = queueVersion,
    ) =>
      simulateAction(
        epoch,
        'moveQueueItem',
        () => {
          const res = applyQueueMove(remoteQueue, queueItemId, toIndex, {
            expectedQueueVersion,
            queueVersion,
          })
          if (!res.moved) return {}

          remoteQueue = res.queue
          queueVersion += 1

          return {
            queue: [...remoteQueue],
            currentQueueItemId,
            queueVersion,
          }
        },
        { queueItemId, toIndex, expectedQueueVersion },
        'move',
      ),
    clearQueue: (epoch) =>
      simulateAction(
        epoch,
        'clearQueue',
        () => {
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
        },
        {},
        'clear',
      ),
    skipToQueueItem: (epoch, queueItemId) =>
      simulateAction(
        epoch,
        'skipToQueueItem',
        () => {
          if (!remoteQueue.some((item) => item.queueItemId === queueItemId))
            return {}
          currentQueueItemId = queueItemId
          return {
            currentQueueItemId,
            position: 0,
          }
        },
        { queueItemId },
        'skip',
      ),
    // Direct injection of authoritative server push (e.g. position progress or track change)
    pushServerSnapshot: (snapshot, epoch = activeEpoch) => {
      if (epoch !== activeEpoch) return
      if (
        (typeof snapshot.queueVersion === 'number' &&
          snapshot.queueVersion < queueVersion) ||
        (typeof snapshot.stateSequence === 'number' &&
          snapshot.stateSequence <= stateSequence)
      )
        return
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
      publishState(snapshot, snapshot.stateSequence ?? stateSequence + 1)
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
        publishState({
          status: PlaybackStatus.PAUSED,
          position: remoteState.duration,
        })
        return { stopped: true }
      }
      if (res.queueItemId) {
        currentQueueItemId = res.queueItemId
        publishState({
          currentQueueItemId,
          position: 0,
          status: PlaybackStatus.PLAYING,
        })
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
    hasActiveSession: () => hasActiveSession,
    getActiveSessionOwner: () => activeSessionOwner,
    admitClient: async ({
      forceTakeover = false,
      clientId = 'client',
    } = {}) => {
      if (
        hasActiveSession &&
        !forceTakeover &&
        clientId !== activeSessionOwner
      ) {
        return false
      }
      hasActiveSession = true
      activeSessionOwner = clientId
      return true
    },
    getPlayCount: () => wireCommands.filter((c) => c.action === 'play').length,
    getStopCount: () => wireCommands.filter((c) => c.action === 'stop').length,
    getAppliedVolume: () => remoteState.volume,
    getRemoteState: () => ({ ...remoteState }),
  }
}
