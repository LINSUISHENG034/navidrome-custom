/**
 * Playback Control Module
 *
 * Core responsibility:
 * Unified intent-to-adapter orchestrator and authoritative playback state manager.
 * Decouples callers and UI from transport details, remote session leases, and engine implementations.
 * Enforces single intent dispatch, state backfill without command echo, command timeout protection,
 * and generation epoch invalidation across engine/mode switches.
 */

import { audioVolumeToRemoteGain } from './volumeProfiles'

export const PlaybackStatus = {
  IDLE: 'idle',
  PREPARING: 'preparing',
  PLAYING: 'playing',
  PAUSED: 'paused',
  ERROR: 'error',
}

export const CommandResultStatus = {
  APPLIED: 'APPLIED',
  NOT_EXECUTED: 'NOT_EXECUTED',
  FAILED: 'FAILED',
  UNKNOWN: 'UNKNOWN',
}

export const PlaybackMode = {
  ORDER: 'order',
  REPEAT_LIST: 'orderLoop',
  REPEAT_TRACK: 'singleLoop',
  SHUFFLE: 'shufflePlay',
}

export const PREV_REWIND_THRESHOLD_S = 1.0

export function normalizePlaybackMode(mode) {
  switch (mode) {
    case 'order':
      return PlaybackMode.ORDER
    case 'orderLoop':
      return PlaybackMode.REPEAT_LIST
    case 'singleLoop':
      return PlaybackMode.REPEAT_TRACK
    case 'shufflePlay':
      return PlaybackMode.SHUFFLE
    default:
      return PlaybackMode.ORDER
  }
}

/**
 * Shared, deterministic mode transitions for track selection.
 * Reused by both BrowserAudioAdapter and MockRemoteAudioAdapter.
 */
export function getNextTrack({
  queue = [],
  currentQueueItemId = null,
  mode = PlaybackMode.ORDER,
  isManual = false,
  randomFn = Math.random,
} = {}) {
  const normalizedMode = normalizePlaybackMode(mode)
  if (!queue || queue.length === 0) {
    return { queueItemId: null, shouldStop: true }
  }

  if (queue.length === 1) {
    const onlyId = queue[0].queueItemId
    if (isManual) {
      return { queueItemId: onlyId, shouldStop: false }
    }
    // Natural end on single item:
    // Order stops; repeat list, repeat track, and shuffle replay the single item
    if (normalizedMode === PlaybackMode.ORDER) {
      return { queueItemId: onlyId, shouldStop: true }
    }
    return { queueItemId: onlyId, shouldStop: false }
  }

  const curIdx = queue.findIndex(
    (item) => item.queueItemId === currentQueueItemId,
  )
  const safeIdx = curIdx >= 0 ? curIdx : 0

  if (normalizedMode === PlaybackMode.SHUFFLE) {
    // Multi-item queue excludes current item; does not reorder visible queue
    const candidates = queue.filter((_, idx) => idx !== safeIdx)
    const pickIdx = Math.floor(randomFn() * candidates.length)
    return { queueItemId: candidates[pickIdx].queueItemId, shouldStop: false }
  }

  if (normalizedMode === PlaybackMode.REPEAT_TRACK) {
    if (isManual) {
      // Manual skip advances even in singleLoop mode (single loop does not block active skip)
      const nextIdx = (safeIdx + 1) % queue.length
      return { queueItemId: queue[nextIdx].queueItemId, shouldStop: false }
    }
    // Natural end replays current item
    return { queueItemId: queue[safeIdx].queueItemId, shouldStop: false }
  }

  if (normalizedMode === PlaybackMode.REPEAT_LIST) {
    const nextIdx = (safeIdx + 1) % queue.length
    return { queueItemId: queue[nextIdx].queueItemId, shouldStop: false }
  }

  // ORDER mode
  if (isManual) {
    // Manual skip in order mode wraps around at the end of queue
    const nextIdx = (safeIdx + 1) % queue.length
    return { queueItemId: queue[nextIdx].queueItemId, shouldStop: false }
  }

  // Natural end in order mode stops at last item
  if (safeIdx >= queue.length - 1) {
    return { queueItemId: queue[safeIdx].queueItemId, shouldStop: true }
  }
  return { queueItemId: queue[safeIdx + 1].queueItemId, shouldStop: false }
}

export function getPrevTrack({
  queue = [],
  currentQueueItemId = null,
  mode = PlaybackMode.ORDER,
  currentTime = 0,
  threshold = PREV_REWIND_THRESHOLD_S,
  randomFn = Math.random,
} = {}) {
  const normalizedMode = normalizePlaybackMode(mode)
  if (!queue || queue.length === 0) {
    return { queueItemId: null, rewindOnly: false }
  }

  // Threshold rule: when position > 1s, rewinds to 0s without switching tracks
  if (currentTime > threshold) {
    return { queueItemId: currentQueueItemId, rewindOnly: true }
  }

  if (queue.length === 1) {
    return { queueItemId: queue[0].queueItemId, rewindOnly: false }
  }

  const curIdx = queue.findIndex(
    (item) => item.queueItemId === currentQueueItemId,
  )
  const safeIdx = curIdx >= 0 ? curIdx : 0

  if (normalizedMode === PlaybackMode.SHUFFLE) {
    // Multi-item queue random selection excluding current item
    const candidates = queue.filter((_, idx) => idx !== safeIdx)
    const pickIdx = Math.floor(randomFn() * candidates.length)
    return { queueItemId: candidates[pickIdx].queueItemId, rewindOnly: false }
  }

  // Non-random modes (ORDER, REPEAT_LIST, REPEAT_TRACK) switch to previous item with wrap-around
  const prevIdx = (safeIdx - 1 + queue.length) % queue.length
  return { queueItemId: queue[prevIdx].queueItemId, rewindOnly: false }
}

/**
 * Shared, deterministic queue state transitions.
 * Reused by both BrowserAudioAdapter and MockRemoteAudioAdapter to prevent
 * divergent behavior or copy-paste duplication.
 */
export function applyQueueAdd(
  queue = [],
  currentId = null,
  items = [],
  index = -1,
) {
  const validated = items.map((item) => {
    const queueItemId = item.queueItemId || item.uuid
    if (!queueItemId) {
      throw new Error(
        '[QueueContract] Each queue item must possess a stable queueItemId',
      )
    }
    return { ...item, queueItemId }
  })

  let nextQueue
  if (index === -1 || index >= queue.length) {
    nextQueue = [...queue, ...validated]
  } else {
    const insertAt = Math.max(0, index)
    nextQueue = [
      ...queue.slice(0, insertAt),
      ...validated,
      ...queue.slice(insertAt),
    ]
  }

  const nextCurrentId =
    currentId || (nextQueue.length > 0 ? nextQueue[0].queueItemId : null)
  return { queue: nextQueue, currentQueueItemId: nextCurrentId }
}

export function applyQueueRemove(
  queue = [],
  currentId = null,
  queueItemId,
  isPlaying = false,
) {
  const targetIdx = queue.findIndex((item) => item.queueItemId === queueItemId)
  if (targetIdx === -1) {
    return { queue, currentQueueItemId: currentId, removed: false }
  }

  const isCurrent = currentId === queueItemId
  const nextQueue = queue.filter((item) => item.queueItemId !== queueItemId)

  if (!isCurrent) {
    return { queue: nextQueue, currentQueueItemId: currentId, removed: true }
  }

  if (nextQueue.length === 0) {
    return {
      queue: [],
      currentQueueItemId: null,
      status: PlaybackStatus.IDLE,
      position: 0,
      removed: true,
    }
  }

  const successorIdx =
    targetIdx < nextQueue.length ? targetIdx : nextQueue.length - 1
  const successor = nextQueue[successorIdx]
  const nextStatus = isPlaying ? PlaybackStatus.PLAYING : PlaybackStatus.PAUSED

  return {
    queue: nextQueue,
    currentQueueItemId: successor.queueItemId,
    status: nextStatus,
    position: 0,
    successor,
    removed: true,
  }
}

export function applyQueueMove(
  queue = [],
  queueItemId,
  toIndex,
  { expectedQueueVersion, queueVersion } = {},
) {
  if (
    expectedQueueVersion !== undefined &&
    expectedQueueVersion !== queueVersion
  ) {
    throw {
      code: 'QUEUE_VERSION_CONFLICT',
      message: 'The queue changed before the move could be applied',
    }
  }
  const fromIndex = queue.findIndex((item) => item.queueItemId === queueItemId)
  if (fromIndex === -1) {
    return { queue, moved: false }
  }

  const boundedTo = Math.max(0, Math.min(toIndex, queue.length - 1))
  if (fromIndex === boundedTo) {
    return { queue, moved: true }
  }

  const item = queue[fromIndex]
  const remaining = queue.filter((_, idx) => idx !== fromIndex)
  const nextQueue = [
    ...remaining.slice(0, boundedTo),
    item,
    ...remaining.slice(boundedTo),
  ]

  return { queue: nextQueue, moved: true }
}

export function applyQueueClear() {
  return {
    queue: [],
    currentQueueItemId: null,
    status: PlaybackStatus.IDLE,
    position: 0,
  }
}

export function createPlaybackControlModule(config = {}) {
  let activeAdapter = null
  let currentEpoch = 0
  let intentCounter = 0
  let migrationCounter = 0
  let activeMigration = null
  const defaultTimeoutMs = config.commandTimeoutMs || 5000

  const createInitialState = () => ({
    status: PlaybackStatus.IDLE,
    currentTrack: null,
    position: 0,
    duration: 0,
    volume: config.initialVolume ?? 1,
    isMuted: false,
    error: null,
    queue: [],
    currentQueueItemId: null,
    queueVersion: 0,
    stateSequence: null,
    playMode: normalizePlaybackMode(
      config.initialPlayMode || PlaybackMode.ORDER,
    ),
    acceptedIntent: null,
    intentSequence: 0,
    playbackInstanceId: null,
    pendingOutputTarget: null,
  })
  let state = createInitialState()

  const listeners = new Set()
  const metrics = {
    intentsIssued: 0,
    stateBackfills: 0,
    commandsSent: {
      play: 0,
      pause: 0,
      stop: 0,
      seek: 0,
      volume: 0,
      skipNext: 0,
      skipPrev: 0,
      setPlayMode: 0,
      addQueueItems: 0,
      removeQueueItem: 0,
      moveQueueItem: 0,
      clearQueue: 0,
      skipToQueueItem: 0,
    },
  }

  function notifySubscribers() {
    const frozenState = { ...state }
    listeners.forEach((listener) => {
      try {
        listener(frozenState)
      } catch (err) {
        // eslint-disable-next-line no-console
        console.error('[PlaybackControlModule] Listener error:', err)
      }
    })
  }

  function setAdapter(newAdapter) {
    // 1. Advance epoch to invalidate any late asynchronous callbacks from old adapter
    currentEpoch += 1
    const newEpoch = currentEpoch

    // 2. Stop and deactivate old adapter
    if (activeAdapter && typeof activeAdapter.deactivate === 'function') {
      try {
        activeAdapter.deactivate(currentEpoch - 1)
      } catch (err) {
        // eslint-disable-next-line no-console
        console.error(
          '[PlaybackControlModule] Adapter deactivation error:',
          err,
        )
      }
    }

    activeAdapter = newAdapter
    state = createInitialState()
    notifySubscribers()

    // A subscriber may synchronously switch again while accepting the new scene.
    if (currentEpoch !== newEpoch || activeAdapter !== newAdapter) return

    // Queue and playback versions belong to this activation, not the previous engine.
    if (newAdapter && typeof newAdapter.activate === 'function') {
      newAdapter.activate(newEpoch, (epoch, stateUpdate) => {
        if (
          epoch !== newEpoch ||
          currentEpoch !== newEpoch ||
          activeAdapter !== newAdapter
        ) {
          return
        }
        handleAuthoritativeStateUpdate(stateUpdate)
      })
    }
  }

  function handleAuthoritativeStateUpdate(update) {
    if (
      (typeof update.queueVersion === 'number' &&
        update.queueVersion < state.queueVersion) ||
      (typeof update.stateSequence === 'number' &&
        typeof state.stateSequence === 'number' &&
        update.stateSequence <= state.stateSequence)
    ) {
      return
    }

    metrics.stateBackfills += 1

    let hasChange = false
    const nextState = { ...state }
    if (typeof update.stateSequence === 'number') {
      nextState.stateSequence = update.stateSequence
      hasChange = true
    }

    if (update.status && update.status !== state.status) {
      nextState.status = update.status
      if (update.status === PlaybackStatus.PLAYING) {
        nextState.acceptedIntent = 'play'
      } else if (update.status === PlaybackStatus.IDLE) {
        nextState.acceptedIntent = null
      } else if (
        update.status === PlaybackStatus.PAUSED &&
        state.status !== PlaybackStatus.PREPARING
      ) {
        nextState.acceptedIntent = 'pause'
      }
      hasChange = true
    }
    if (
      update.playbackInstanceId !== undefined &&
      update.playbackInstanceId !== state.playbackInstanceId
    ) {
      nextState.playbackInstanceId = update.playbackInstanceId
      hasChange = true
    }
    if (
      typeof update.position === 'number' &&
      update.position !== state.position
    ) {
      nextState.position = update.position
      hasChange = true
    }
    if (
      typeof update.duration === 'number' &&
      update.duration !== state.duration
    ) {
      nextState.duration = update.duration
      hasChange = true
    }
    if (typeof update.volume === 'number' && update.volume !== state.volume) {
      nextState.volume = update.volume
      hasChange = true
    }
    if (
      typeof update.isMuted === 'boolean' &&
      update.isMuted !== state.isMuted
    ) {
      nextState.isMuted = update.isMuted
      hasChange = true
    }
    if (
      update.currentTrack !== undefined &&
      update.currentTrack !== state.currentTrack
    ) {
      nextState.currentTrack = update.currentTrack
      hasChange = true
    }
    if (Array.isArray(update.queue)) {
      nextState.queue = update.queue
      hasChange = true
    }
    if (
      update.currentQueueItemId !== undefined &&
      update.currentQueueItemId !== state.currentQueueItemId
    ) {
      nextState.currentQueueItemId = update.currentQueueItemId
      hasChange = true
    }
    if (
      typeof update.queueVersion === 'number' &&
      update.queueVersion !== state.queueVersion
    ) {
      nextState.queueVersion = update.queueVersion
      hasChange = true
    }
    if (update.playMode !== undefined) {
      const normalized = normalizePlaybackMode(update.playMode)
      if (normalized !== state.playMode) {
        nextState.playMode = normalized
        hasChange = true
      }
    }
    if (update.error !== undefined) {
      nextState.error = update.error
      if (update.error) {
        nextState.status = PlaybackStatus.ERROR
      }
      hasChange = true
    }

    if (hasChange) {
      state = nextState
      // State backfill strictly updates UI/subscribers, NEVER re-emits intents
      notifySubscribers()
    }
  }

  async function executeWithTimeout(promise, timeoutMs, opId, adapter) {
    let timer
    const timeoutPromise = new Promise((_, reject) => {
      timer = setTimeout(() => {
        reject({
          code: 'COMMAND_TIMEOUT',
          message: `Playback command timed out after ${timeoutMs}ms`,
          status: CommandResultStatus.UNKNOWN,
          operationId: opId,
        })
      }, timeoutMs)
    })

    try {
      return await Promise.race([promise, timeoutPromise])
    } catch (err) {
      const isUnknown =
        err?.code === 'COMMAND_TIMEOUT' ||
        err?.code === 'RESPONSE_LOST' ||
        err?.status === CommandResultStatus.UNKNOWN

      if (
        isUnknown &&
        adapter &&
        typeof adapter.queryOperation === 'function' &&
        opId
      ) {
        const queryResult = await adapter.queryOperation(opId)
        if (queryResult?.status === CommandResultStatus.APPLIED) {
          return queryResult
        }
        if (queryResult?.status === CommandResultStatus.NOT_EXECUTED) {
          throw {
            code: 'COMMAND_NOT_EXECUTED',
            message: `Operation ${opId} was not executed on the adapter`,
            status: CommandResultStatus.NOT_EXECUTED,
            operationId: opId,
          }
        }
        if (queryResult?.status === CommandResultStatus.FAILED) {
          throw {
            code: queryResult.error?.code || 'COMMAND_FAILED',
            message:
              queryResult.error?.message ||
              `Operation ${opId} failed on the adapter`,
            status: CommandResultStatus.FAILED,
            operationId: opId,
          }
        }
        throw {
          code: err?.code || 'COMMAND_UNKNOWN',
          message:
            err?.message ||
            `Operation ${opId} status remains UNKNOWN after query`,
          status: CommandResultStatus.UNKNOWN,
          operationId: opId,
        }
      }
      throw err
    } finally {
      clearTimeout(timer)
    }
  }

  function captureOperation() {
    if (!activeAdapter) {
      const error = {
        code: 'NO_ACTIVE_ENGINE',
        message: 'No playback engine active',
      }
      state = { ...state, status: PlaybackStatus.ERROR, error }
      notifySubscribers()
      throw error
    }
    return { adapter: activeAdapter, epoch: currentEpoch }
  }

  function isCurrentOperation(operation) {
    return (
      activeAdapter === operation.adapter && currentEpoch === operation.epoch
    )
  }

  function assertCurrentOperation(operation) {
    if (!isCurrentOperation(operation)) {
      throw {
        code: 'COMMAND_SUPERSEDED',
        message: 'Playback engine changed before the operation completed',
      }
    }
  }

  function markPreparing() {
    state = { ...state, status: PlaybackStatus.PREPARING, error: null }
    notifySubscribers()
  }

  async function runOperation(operation, commandName, executor) {
    try {
      assertCurrentOperation(operation)
      const result = await executor()
      assertCurrentOperation(operation)
      return result
    } catch (err) {
      const formattedError = {
        code: err?.code || `${commandName.toUpperCase()}_REQUEST_FAILED`,
        message: err?.message || `Failed to execute ${commandName}`,
        status: err?.status,
        operationId: err?.operationId,
      }
      if (
        isCurrentOperation(operation) &&
        formattedError.code !== 'COMMAND_SUPERSEDED'
      ) {
        state = {
          ...state,
          status: PlaybackStatus.ERROR,
          error: formattedError,
        }
        notifySubscribers()
      }
      throw formattedError
    }
  }

  async function sendCommand(operation, commandName, executor, opId) {
    assertCurrentOperation(operation)
    if (metrics.commandsSent[commandName] !== undefined) {
      metrics.commandsSent[commandName] += 1
    }
    const result = await executeWithTimeout(
      executor(operation.adapter),
      defaultTimeoutMs,
      opId,
      operation.adapter,
    )
    assertCurrentOperation(operation)
    return result
  }

  async function dispatchCommand(commandName, executor, isPlayIntent = false) {
    intentCounter += 1
    const seq = intentCounter
    state.intentSequence = seq

    if (activeMigration) {
      if (commandName === 'play') {
        state.acceptedIntent = 'play'
        activeMigration.acceptedIntent = 'play'
        activeMigration.intentSequence = seq
        notifySubscribers()
        return Promise.resolve()
      }
      if (commandName === 'pause' || commandName === 'stop') {
        state.acceptedIntent = 'pause'
        activeMigration.acceptedIntent = 'pause'
        activeMigration.intentSequence = seq
        notifySubscribers()
        return Promise.resolve()
      }

      return new Promise((resolve, reject) => {
        activeMigration.isolatedCommands.push({
          commandName,
          executor,
          intentSequence: seq,
          queueVersion: state.queueVersion,
          playbackInstanceId: state.playbackInstanceId,
          currentQueueItemId: state.currentQueueItemId,
          resolve,
          reject,
        })
      })
    }

    const operation = captureOperation()
    metrics.intentsIssued += 1
    if (isPlayIntent) {
      state.acceptedIntent = 'play'
      markPreparing()
    } else if (commandName === 'pause' || commandName === 'stop') {
      state.acceptedIntent = 'pause'
    }

    const opId = `op_${commandName}_${seq}`
    return runOperation(operation, commandName, () =>
      sendCommand(
        operation,
        commandName,
        (opAdapter) =>
          executor(operation.epoch, opAdapter, { operationId: opId }),
        opId,
      ),
    )
  }

  function failMigration(err, fallbackCode, fallbackMessage) {
    const formatted = {
      code: err?.code || fallbackCode,
      message: err?.message || fallbackMessage,
      status: err?.status,
      operationId: err?.operationId,
    }
    state = {
      ...state,
      pendingOutputTarget: null,
      status: PlaybackStatus.ERROR,
      error: formatted,
    }
    notifySubscribers()
    throw formatted
  }

  function assertMigrationEpoch(newEpoch, targetAdapter) {
    if (currentEpoch !== newEpoch || activeAdapter !== targetAdapter) {
      throw {
        code: 'COMMAND_SUPERSEDED',
        message: 'Migration interrupted by another playback scene change',
      }
    }
  }

  async function migrateOutput(options = {}) {
    const {
      targetAdapter,
      targetDevice = {},
      requestConfirmation,
      volumeProfile,
      forceTakeover = false,
      signal,
      readinessTimeoutMs = options.readinessTimeoutMs ??
        targetDevice.readinessTimeoutMs,
      overallTimeoutMs = 15000,
      phaseTimeoutMs = defaultTimeoutMs,
    } = options

    if (!targetAdapter) {
      throw {
        code: 'NO_TARGET_ADAPTER',
        message: 'A target adapter must be provided for migration',
      }
    }

    if (activeMigration) {
      throw {
        code: 'MIGRATION_IN_PROGRESS',
        message: 'Another output migration is currently in progress',
        activeMigrationId: activeMigration.migrationId,
      }
    }

    const migrationId =
      options.migrationId || `mig_${Date.now()}_${++migrationCounter}`
    const sourceAdapter = activeAdapter
    const abortController = new AbortController()

    if (signal) {
      if (signal.aborted) {
        throw {
          code: 'MIGRATION_CANCELLED',
          message: 'Migration was cancelled before starting',
          migrationId,
        }
      }
      signal.addEventListener('abort', () => abortController.abort(), {
        once: true,
      })
    }

    const checkAborted = () => {
      if (abortController.signal.aborted) {
        throw {
          code: 'MIGRATION_CANCELLED',
          message: 'Migration transaction was cancelled',
          migrationId,
        }
      }
    }

    const opId = (phase, step) => `${migrationId}:${phase}:${step}`

    const migrationTx = {
      migrationId,
      sourceAdapter,
      targetAdapter,
      abortController,
      acceptedIntent: state.acceptedIntent,
      intentSequence: state.intentSequence,
      isolatedCommands: [],
      reservationOwned: false,
    }
    activeMigration = migrationTx

    let overallTimer
    const overallTimeoutPromise = new Promise((_, reject) => {
      overallTimer = setTimeout(() => {
        abortController.abort()
        reject({
          code: 'MIGRATION_TIMEOUT',
          message: `Output migration timed out after ${overallTimeoutMs}ms`,
          migrationId,
        })
      }, overallTimeoutMs)
    })

    const runTransaction = async () => {
      checkAborted()

      // Phase 1: Target Admission & Explicit Confirmation
      state = {
        ...state,
        pendingOutputTarget: targetDevice,
      }
      notifySubscribers()

      const activeOwner =
        targetDevice.activeController ||
        (typeof targetAdapter.getActiveSessionOwner === 'function'
          ? targetAdapter.getActiveSessionOwner()
          : null)
      const isOriginalOwnerReattaching = Boolean(
        targetDevice.clientId &&
        activeOwner &&
        targetDevice.clientId === activeOwner,
      )
      const hasActive =
        (Boolean(targetDevice.hasActiveSession) ||
          Boolean(targetDevice.activeController) ||
          (typeof targetAdapter.hasActiveSession === 'function' &&
            targetAdapter.hasActiveSession())) &&
        !isOriginalOwnerReattaching
      let takeoverAllowed = forceTakeover
      if (hasActive && !takeoverAllowed) {
        if (typeof requestConfirmation === 'function') {
          const confirmed = await requestConfirmation(targetDevice)
          if (!confirmed) {
            throw {
              code: 'TARGET_CONFIRMATION_REJECTED',
              message: 'Target migration cancelled by user',
            }
          }
          takeoverAllowed = true
        } else {
          throw {
            code: 'TARGET_OCCUPIED',
            message: 'Target device already has an active controller',
          }
        }
      }

      checkAborted()

      const opIdAdmission = opId('phase1', 'admission')
      if (typeof targetAdapter.reserveMigration === 'function') {
        const reserveTimeout = readinessTimeoutMs
          ? Math.max(phaseTimeoutMs, readinessTimeoutMs + 1000)
          : phaseTimeoutMs
        const reserveRes = await executeWithTimeout(
          targetAdapter.reserveMigration(opIdAdmission, {
            migrationId,
            clientId: targetDevice.clientId,
            forceTakeover: takeoverAllowed,
            signal: abortController.signal,
            readinessTimeoutMs,
            ...targetDevice,
          }),
          reserveTimeout,
          opIdAdmission,
          targetAdapter,
        )
        if (
          reserveRes?.status === CommandResultStatus.FAILED ||
          reserveRes === false
        ) {
          throw {
            code: 'TARGET_ADMISSION_DENIED',
            message: 'Failed to reserve target device',
          }
        }
        migrationTx.reservationOwned = true
      } else if (typeof targetAdapter.admitClient === 'function') {
        const admitted = await executeWithTimeout(
          targetAdapter.admitClient({
            forceTakeover: takeoverAllowed,
            migrationId,
            operationId: opIdAdmission,
            ...targetDevice,
          }),
          phaseTimeoutMs,
          opIdAdmission,
          targetAdapter,
        )
        if (!admitted) {
          throw {
            code: 'TARGET_ADMISSION_DENIED',
            message: 'Failed to obtain control of target device',
          }
        }
        migrationTx.reservationOwned = true
      }

      checkAborted()

      // Phase 2: Old Output Stop Confirmation
      const snapshot = {
        queue: [...state.queue],
        currentQueueItemId: state.currentQueueItemId,
        position: state.position,
        volume: state.volume,
        isMuted: state.isMuted,
        playMode: state.playMode,
        status: state.status,
        acceptedIntent: state.acceptedIntent,
        intentSequence: state.intentSequence,
        playbackInstanceId: state.playbackInstanceId,
        queueVersion: state.queueVersion,
      }
      const wasPlayingOrPreparingPlay =
        snapshot.status === PlaybackStatus.PLAYING ||
        (snapshot.status === PlaybackStatus.PREPARING &&
          snapshot.acceptedIntent === 'play')

      if (sourceAdapter) {
        const opIdStop = opId('phase2', 'stop')
        try {
          if (typeof sourceAdapter.stop === 'function') {
            await executeWithTimeout(
              sourceAdapter.stop(currentEpoch, { operationId: opIdStop }),
              phaseTimeoutMs,
              opIdStop,
              sourceAdapter,
            )
          } else if (typeof sourceAdapter.pause === 'function') {
            await executeWithTimeout(
              sourceAdapter.pause(currentEpoch, { operationId: opIdStop }),
              phaseTimeoutMs,
              opIdStop,
              sourceAdapter,
            )
          }
        } catch (err) {
          if (migrationTx.reservationOwned) {
            const opIdRelease = opId('cleanup', 'release')
            try {
              if (
                typeof targetAdapter.releaseMigrationReservation === 'function'
              ) {
                await executeWithTimeout(
                  targetAdapter.releaseMigrationReservation(opIdRelease, {
                    migrationId,
                  }),
                  phaseTimeoutMs,
                  opIdRelease,
                  targetAdapter,
                )
              }
            } catch {
              // reservation cleanup failure recorded
            }
            migrationTx.reservationOwned = false
          }
          failMigration(
            err,
            'OLD_OUTPUT_STOP_FAILED',
            'Failed to confirm old output stopped',
          )
        }
      }

      checkAborted()

      // Phase 3: Scene Migration (Context Commit)
      currentEpoch += 1
      const newEpoch = currentEpoch

      if (sourceAdapter && typeof sourceAdapter.deactivate === 'function') {
        try {
          sourceAdapter.deactivate(currentEpoch - 1)
        } catch (err) {
          // eslint-disable-next-line no-console
          console.error(
            '[PlaybackControlModule] Adapter deactivation error:',
            err,
          )
        }
      }

      activeAdapter = targetAdapter
      state = {
        ...createInitialState(),
        queue: snapshot.queue,
        currentQueueItemId: snapshot.currentQueueItemId,
        position: snapshot.position,
        volume: snapshot.volume,
        isMuted: snapshot.isMuted,
        playMode: snapshot.playMode,
        status: PlaybackStatus.PREPARING,
        acceptedIntent: migrationTx.acceptedIntent ?? snapshot.acceptedIntent,
        intentSequence: migrationTx.intentSequence ?? snapshot.intentSequence,
        playbackInstanceId: snapshot.playbackInstanceId,
      }
      notifySubscribers()

      assertMigrationEpoch(newEpoch, targetAdapter)

      if (targetAdapter && typeof targetAdapter.activate === 'function') {
        targetAdapter.activate(newEpoch, (epoch, stateUpdate) => {
          if (
            epoch !== newEpoch ||
            currentEpoch !== newEpoch ||
            activeAdapter !== targetAdapter
          ) {
            return
          }
          handleAuthoritativeStateUpdate(stateUpdate)
        })
      }

      if (snapshot.queue.length > 0) {
        if (typeof targetAdapter.clearQueue === 'function') {
          await targetAdapter.clearQueue(newEpoch)
          assertMigrationEpoch(newEpoch, targetAdapter)
        }
        if (typeof targetAdapter.addQueueItems === 'function') {
          await targetAdapter.addQueueItems(newEpoch, snapshot.queue)
          assertMigrationEpoch(newEpoch, targetAdapter)
        }
        if (
          snapshot.currentQueueItemId &&
          typeof targetAdapter.skipToQueueItem === 'function'
        ) {
          await targetAdapter.skipToQueueItem(
            newEpoch,
            snapshot.currentQueueItemId,
          )
          assertMigrationEpoch(newEpoch, targetAdapter)
        }
        if (snapshot.position > 0 && typeof targetAdapter.seek === 'function') {
          await targetAdapter.seek(newEpoch, snapshot.position)
          assertMigrationEpoch(newEpoch, targetAdapter)
        }
        if (
          snapshot.playMode &&
          typeof targetAdapter.setPlayMode === 'function'
        ) {
          await targetAdapter.setPlayMode(newEpoch, snapshot.playMode)
          assertMigrationEpoch(newEpoch, targetAdapter)
        }
      }

      checkAborted()

      // Phase 4: Target Volume Setting & Confirmation
      let targetVolume = snapshot.volume
      if (typeof volumeProfile === 'function') {
        targetVolume = volumeProfile(snapshot.volume, targetDevice)
      } else if (typeof targetDevice?.volumeProfile === 'function') {
        targetVolume = targetDevice.volumeProfile(snapshot.volume, targetDevice)
      } else if (targetDevice?.isBluetooth) {
        targetVolume = audioVolumeToRemoteGain(snapshot.volume, targetDevice)
      }

      const opIdVolume = opId('phase4', 'volume')
      try {
        if (typeof targetAdapter.setVolume === 'function') {
          await executeWithTimeout(
            targetAdapter.setVolume(newEpoch, targetVolume, {
              operationId: opIdVolume,
            }),
            phaseTimeoutMs,
            opIdVolume,
            targetAdapter,
          )
          assertMigrationEpoch(newEpoch, targetAdapter)
        }
      } catch (volErr) {
        failMigration(
          volErr,
          'TARGET_VOLUME_FAILED',
          'Failed to apply volume to target device',
        )
      }

      checkAborted()

      // Phase 5: Conditional Startup based on final acceptedIntent
      const effectiveIntent =
        migrationTx.acceptedIntent ??
        (wasPlayingOrPreparingPlay ? 'play' : 'pause')
      const shouldPlayTarget = effectiveIntent === 'play'
      const opIdStart = opId('phase5', shouldPlayTarget ? 'play' : 'pause')

      if (
        snapshot.queue.length === 0 &&
        typeof targetAdapter.getRemoteState === 'function' &&
        targetAdapter.getRemoteState().status === PlaybackStatus.PLAYING
      ) {
        const targetState = targetAdapter.getRemoteState()
        assertMigrationEpoch(newEpoch, targetAdapter)
        state = {
          ...state,
          status: PlaybackStatus.PLAYING,
          position:
            typeof targetState.position === 'number'
              ? targetState.position
              : state.position,
          acceptedIntent: 'play',
        }
        notifySubscribers()
      } else if (shouldPlayTarget) {
        try {
          await executeWithTimeout(
            targetAdapter.play(newEpoch, { operationId: opIdStart }),
            phaseTimeoutMs,
            opIdStart,
            targetAdapter,
          )
          assertMigrationEpoch(newEpoch, targetAdapter)
          state = {
            ...state,
            status: PlaybackStatus.PLAYING,
            acceptedIntent: 'play',
          }
          notifySubscribers()
        } catch (playErr) {
          failMigration(
            playErr,
            'TARGET_START_FAILED',
            'Failed to start playback on target device',
          )
        }
      } else {
        if (typeof targetAdapter.pause === 'function') {
          try {
            await executeWithTimeout(
              targetAdapter.pause(newEpoch, { operationId: opIdStart }),
              phaseTimeoutMs,
              opIdStart,
              targetAdapter,
            )
          } catch (pauseErr) {
            failMigration(
              pauseErr,
              'TARGET_PAUSE_FAILED',
              'Failed to confirm target pause',
            )
          }
        }
        assertMigrationEpoch(newEpoch, targetAdapter)
        state = {
          ...state,
          status:
            snapshot.status === PlaybackStatus.IDLE
              ? PlaybackStatus.IDLE
              : PlaybackStatus.PAUSED,
          acceptedIntent: 'pause',
        }
        notifySubscribers()
      }

      // Phase 6: Settle isolated commands submitted during migration
      const isolated = migrationTx.isolatedCommands
      for (const cmd of isolated) {
        const isStale =
          cmd.queueVersion !== state.queueVersion ||
          (cmd.currentQueueItemId &&
            cmd.currentQueueItemId !== state.currentQueueItemId) ||
          (cmd.playbackInstanceId &&
            cmd.playbackInstanceId !== state.playbackInstanceId)

        if (isStale) {
          cmd.reject({
            code: 'COMMAND_SUPERSEDED',
            message:
              'Command submitted during migration is no longer applicable to the migrated playback context',
            stale: true,
          })
        } else {
          try {
            const res = await dispatchCommand(cmd.commandName, cmd.executor)
            cmd.resolve(res)
          } catch (err) {
            cmd.reject(err)
          }
        }
      }

      state = {
        ...state,
        pendingOutputTarget: null,
      }
      notifySubscribers()

      return { ...state }
    }

    try {
      return await Promise.race([runTransaction(), overallTimeoutPromise])
    } finally {
      clearTimeout(overallTimer)
      activeMigration = null
      if (state.pendingOutputTarget) {
        state = {
          ...state,
          pendingOutputTarget: null,
        }
        notifySubscribers()
      }
    }
  }

  return {
    setAdapter,
    migrateOutput,
    cancelMigration: (reason) => {
      if (activeMigration) {
        activeMigration.abortController.abort()
        return true
      }
      return false
    },
    isMigrating: () => Boolean(activeMigration),
    getActiveMigration: () => (activeMigration ? { ...activeMigration } : null),
    getState: () => ({ ...state }),
    getEpoch: () => currentEpoch,
    getMetrics: () => ({
      ...metrics,
      commandsSent: { ...metrics.commandsSent },
    }),
    subscribe: (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    play: () =>
      dispatchCommand(
        'play',
        (epoch, adapter, options) => adapter.play(epoch, options),
        true,
      ),
    pause: () =>
      dispatchCommand('pause', (epoch, adapter, options) =>
        adapter.pause(epoch, options),
      ),
    stop: () =>
      dispatchCommand('stop', (epoch, adapter, options) => {
        if (typeof adapter.stop === 'function') {
          return adapter.stop(epoch, options)
        }
        return adapter.pause(epoch, options)
      }),
    seek: (pos) =>
      dispatchCommand('seek', (epoch, adapter, options) =>
        adapter.seek(epoch, pos, options),
      ),
    setVolume: (vol) =>
      dispatchCommand('volume', (epoch, adapter) =>
        adapter.setVolume(epoch, vol),
      ),
    skipNext: () =>
      dispatchCommand('skipNext', (epoch, adapter) => adapter.skipNext(epoch)),
    skipPrev: () =>
      dispatchCommand('skipPrev', (epoch, adapter) =>
        adapter.skipPrev(epoch, state.position),
      ),
    setPlayMode: (mode) => {
      const normalized = normalizePlaybackMode(mode)
      return dispatchCommand('setPlayMode', (epoch, adapter) =>
        adapter.setPlayMode(epoch, normalized),
      )
    },
    // Queue editing contract intents
    addQueueItems: (items, index = -1) =>
      dispatchCommand('addQueueItems', (epoch, adapter) =>
        adapter.addQueueItems(epoch, items, index),
      ),
    removeQueueItem: (queueItemId) =>
      dispatchCommand('removeQueueItem', (epoch, adapter) =>
        adapter.removeQueueItem(epoch, queueItemId),
      ),
    moveQueueItem: (
      queueItemId,
      toIndex,
      expectedQueueVersion = state.queueVersion,
    ) =>
      dispatchCommand('moveQueueItem', (epoch, adapter) =>
        adapter.moveQueueItem(
          epoch,
          queueItemId,
          toIndex,
          expectedQueueVersion,
        ),
      ),
    clearQueue: () =>
      dispatchCommand('clearQueue', (epoch, adapter) =>
        adapter.clearQueue(epoch),
      ),
    skipToQueueItem: (queueItemId) =>
      dispatchCommand('skipToQueueItem', (epoch, adapter) =>
        adapter.skipToQueueItem(epoch, queueItemId),
      ),
    playNext: (items) => {
      const curIdx = state.queue.findIndex(
        (it) => it.queueItemId === state.currentQueueItemId,
      )
      const targetIndex = curIdx >= 0 ? curIdx + 1 : state.queue.length
      return dispatchCommand('addQueueItems', (epoch, adapter) =>
        adapter.addQueueItems(epoch, items, targetIndex),
      )
    },
    playAll: async (items, startIndex = 0) => {
      const operation = captureOperation()
      const { adapter, epoch } = operation
      metrics.intentsIssued += 1
      return runOperation(operation, 'playAll', async () => {
        await sendCommand(operation, 'clearQueue', () =>
          adapter.clearQueue(epoch),
        )
        await sendCommand(operation, 'addQueueItems', () =>
          adapter.addQueueItems(epoch, items, 0),
        )
        if (items.length > 0 && startIndex >= 0 && startIndex < items.length) {
          await sendCommand(operation, 'skipToQueueItem', () =>
            adapter.skipToQueueItem(
              epoch,
              items[startIndex].queueItemId || items[startIndex].uuid,
            ),
          )
          markPreparing()
          await sendCommand(operation, 'play', () => adapter.play(epoch))
        }
      })
    },
    // Transports capture the epoch when they subscribe; legacy one-argument
    // calls remain a current-scene backfill channel.
    backfillState: (update, epoch = currentEpoch) => {
      if (epoch === currentEpoch) handleAuthoritativeStateUpdate(update)
    },
  }
}
