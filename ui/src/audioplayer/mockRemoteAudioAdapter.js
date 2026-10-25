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
import {
  audioVolumeToRemoteGain,
  resolveAuthoritativeGain,
} from './volumeProfiles'

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

  let targetDevice = options.device || {}
  const initialV = options.initialVolume
  let lastConfirmedV = initialV
  let initialGain = initialV !== undefined ? initialV : undefined
  if (initialV !== undefined) {
    try {
      initialGain = audioVolumeToRemoteGain(initialV, targetDevice)
    } catch {
      initialGain = initialV
    }
  }
  let lastConfirmedGain = initialGain
  let desiredGain = initialGain
  let appliedGain = initialGain

  let remoteState = {
    status: PlaybackStatus.IDLE,
    position: options.initialPosition || 0,
    duration: options.initialDuration || 180,
    volume: initialV,
    isMuted: Boolean(options.initialMuted),
    error: null,
  }

  // Configurable behavior for testing fault injection
  let failureConfig = options.failure || null
  let delayMs = options.delayMs || 0

  let hasActiveSession = Boolean(options.hasActiveSession)
  let activeSessionOwner =
    options.activeSessionOwner || (hasActiveSession ? 'other-client' : null)

  const operations = new Map()
  const reservations = new Map()

  function publishState(patch = {}, sequence = stateSequence + 1) {
    remoteState = { ...remoteState, ...patch }
    stateSequence = sequence
    if (isActive && stateCallback) {
      const update = {
        ...remoteState,
        queue: [...remoteQueue],
        currentQueueItemId,
        queueVersion,
        playMode,
        stateSequence,
      }
      if (update.volume === undefined) {
        delete update.volume
      }
      stateCallback(activeEpoch, update)
    }
  }

  function activate(epoch, onStateChange) {
    activeEpoch = epoch
    isActive = true
    stateCallback = onStateChange
    stateSequence = 0
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
      const opId = commandExtra.operationId
      if (failure.responseLost) {
        const patch =
          typeof statePatch === 'function' ? statePatch() : statePatch
        publishState({ ...patch, error: null })
        if (opId) {
          operations.set(opId, {
            status: 'APPLIED',
            action: actionName,
            patch,
          })
        }
        const err = {
          code: 'RESPONSE_LOST',
          message: `Response lost in transit for ${actionName}`,
          status: 'UNKNOWN',
          operationId: opId,
        }
        throw err
      }

      const err = {
        code: failure.code || 'REMOTE_ACTION_FAILED',
        message: failure.message || `Remote action ${actionName} was rejected`,
        status: failure.resultStatus || 'FAILED',
        operationId: opId,
      }
      if (opId) {
        operations.set(opId, {
          status: failure.resultStatus || 'FAILED',
          error: err,
          action: actionName,
        })
      }
      publishState({ status: PlaybackStatus.ERROR, error: err })
      throw err
    }

    const patch = typeof statePatch === 'function' ? statePatch() : statePatch
    if (commandExtra.operationId) {
      operations.set(commandExtra.operationId, {
        status: 'APPLIED',
        action: actionName,
        patch,
      })
    }
    publishState({ ...patch, error: null })
  }

  return {
    activate,
    deactivate,
    play: (epoch, options = {}) =>
      simulateAction(
        epoch,
        'play',
        () => ({
          status: PlaybackStatus.PLAYING,
          position: remoteState.position,
          duration: remoteState.duration,
        }),
        options,
      ),
    pause: (epoch, options = {}) =>
      simulateAction(
        epoch,
        'pause',
        () => ({
          status: PlaybackStatus.PAUSED,
          position: remoteState.position,
        }),
        options,
      ),
    stop: (epoch, options = {}) =>
      simulateAction(
        epoch,
        'stop',
        () => ({
          status: PlaybackStatus.PAUSED,
          position: remoteState.position,
        }),
        options,
      ),
    seek: (epoch, positionSeconds, options = {}) =>
      simulateAction(
        epoch,
        'seek',
        () => ({
          position: Math.max(
            0,
            Math.min(positionSeconds, remoteState.duration),
          ),
        }),
        { position: positionSeconds, ...options },
      ),
    setVolume: (epoch, canonicalVolume, options = {}) => {
      const explicitProfile =
        options.volumeProfile ?? targetDevice.volumeProfile
      const targetG = audioVolumeToRemoteGain(
        canonicalVolume,
        targetDevice,
        explicitProfile,
      )
      desiredGain = targetG
      return simulateAction(
        epoch,
        'volume',
        () => {
          appliedGain = targetG
          lastConfirmedGain = targetG
          lastConfirmedV = canonicalVolume
          return {
            volume: canonicalVolume,
            gain: targetG,
          }
        },
        { volume: targetG, canonicalVolume, ...options },
      )
    },
    setMute: (epoch, isMuted, options = {}) =>
      simulateAction(
        epoch,
        'setMute',
        () => ({
          isMuted: Boolean(isMuted),
        }),
        { isMuted: Boolean(isMuted), ...options },
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
      let volumeToPublish = undefined
      let gainToPublish = undefined
      if (snapshot.gain !== undefined || snapshot.volume !== undefined) {
        const reportedG =
          snapshot.gain !== undefined ? snapshot.gain : snapshot.volume
        const resolved = resolveAuthoritativeGain(
          reportedG,
          lastConfirmedV,
          lastConfirmedGain,
          targetDevice,
        )
        lastConfirmedV = resolved.volume
        lastConfirmedGain = resolved.gain
        appliedGain = resolved.gain
        desiredGain = resolved.gain
        volumeToPublish = resolved.volume
        gainToPublish = resolved.gain
      }
      publishState(
        {
          ...snapshot,
          ...(volumeToPublish !== undefined
            ? { volume: volumeToPublish, gain: gainToPublish }
            : {}),
        },
        snapshot.stateSequence ?? stateSequence + 1,
      )
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
      operationId,
    } = {}) => {
      recordCommand({
        action: 'admitClient',
        forceTakeover,
        clientId,
        operationId,
      })
      if (
        hasActiveSession &&
        !forceTakeover &&
        clientId !== activeSessionOwner
      ) {
        if (operationId) {
          operations.set(operationId, {
            status: 'FAILED',
            code: 'TARGET_OCCUPIED',
          })
        }
        return false
      }
      hasActiveSession = true
      activeSessionOwner = clientId
      if (operationId) {
        operations.set(operationId, { status: 'APPLIED', clientId })
      }
      return true
    },
    reserveMigration: async (operationId, options = {}) => {
      const {
        migrationId,
        clientId = 'client',
        forceTakeover = false,
      } = options
      if (
        options.deviceName ||
        options.isBluetooth !== undefined ||
        options.volumeProfile
      ) {
        targetDevice = { ...targetDevice, ...options }
      }
      recordCommand({
        action: 'reserveMigration',
        operationId,
        migrationId,
        clientId,
        forceTakeover,
      })
      if (failureConfig?.action === 'reserveMigration') {
        const err = {
          code: failureConfig.code || 'TARGET_ADMISSION_DENIED',
          message: failureConfig.message || 'Target reservation rejected',
        }
        if (operationId) {
          operations.set(operationId, { status: 'FAILED', error: err })
        }
        throw err
      }
      const existingRes = reservations.get(migrationId)
      if (
        existingRes &&
        existingRes.status === 'ACTIVE' &&
        existingRes.clientId === clientId
      ) {
        const res = { status: 'APPLIED', migrationId, clientId }
        if (operationId) operations.set(operationId, res)
        return res
      }
      if (
        hasActiveSession &&
        !forceTakeover &&
        clientId !== activeSessionOwner
      ) {
        const err = {
          code: 'TARGET_OCCUPIED',
          message: 'Target device already has an active controller',
        }
        if (operationId) {
          operations.set(operationId, { status: 'FAILED', error: err })
        }
        throw err
      }
      const previousOwner = activeSessionOwner
      reservations.set(migrationId, {
        migrationId,
        clientId,
        status: 'ACTIVE',
        previousOwner,
        forceTakeover,
      })
      hasActiveSession = true
      activeSessionOwner = clientId
      const res = { status: 'APPLIED', migrationId, clientId }
      if (operationId) operations.set(operationId, res)
      return res
    },
    releaseMigrationReservation: async (operationId, { migrationId } = {}) => {
      recordCommand({
        action: 'releaseMigrationReservation',
        operationId,
        migrationId,
      })
      if (failureConfig?.action === 'releaseMigrationReservation') {
        if (failureConfig.timeout) {
          await new Promise((resolve) =>
            setTimeout(resolve, failureConfig.timeoutMs || 10000),
          )
        }
        const err = {
          code: failureConfig.code || 'RELEASE_FAILED',
          message:
            failureConfig.message || 'Failed to release migration reservation',
          status: 'UNKNOWN',
        }
        if (operationId) {
          operations.set(operationId, { status: 'UNKNOWN', error: err })
        }
        throw err
      }
      const res = reservations.get(migrationId)
      if (res && res.status === 'ACTIVE') {
        res.status = 'RELEASED'
        if (res.previousOwner !== undefined) {
          activeSessionOwner = res.previousOwner
        }
        const result = { status: 'APPLIED', migrationId }
        if (operationId) operations.set(operationId, result)
        return result
      }
      const notExec = {
        status: 'NOT_EXECUTED',
        migrationId,
        reason: 'NOT_FOUND_OR_RELEASED',
      }
      if (operationId) operations.set(operationId, notExec)
      return notExec
    },
    queryReservation: (migrationId) => {
      const res = reservations.get(migrationId)
      if (!res) return { status: 'NOT_FOUND' }
      return { ...res }
    },
    queryOperation: async (operationId) => {
      if (operations.has(operationId)) {
        return operations.get(operationId)
      }
      return { status: 'UNKNOWN', operationId }
    },
    setOperationResult: (operationId, result) => {
      operations.set(operationId, result)
    },
    getReservations: () => new Map(reservations),
    getPlayCount: () => wireCommands.filter((c) => c.action === 'play').length,
    getStopCount: () => wireCommands.filter((c) => c.action === 'stop').length,
    getAppliedVolume: () => appliedGain ?? 1,
    getDesiredGain: () => desiredGain ?? 1,
    getConfirmedGain: () => lastConfirmedGain ?? 1,
    getDevice: () => ({ ...targetDevice }),
    setDevice: (dev) => {
      targetDevice = { ...targetDevice, ...dev }
    },
    getRemoteState: () => ({ ...remoteState }),
  }
}
