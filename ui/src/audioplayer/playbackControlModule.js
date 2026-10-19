/**
 * Playback Control Module
 *
 * Core responsibility:
 * Unified intent-to-adapter orchestrator and authoritative playback state manager.
 * Decouples callers and UI from transport details, remote session leases, and engine implementations.
 * Enforces single intent dispatch, state backfill without command echo, command timeout protection,
 * and generation epoch invalidation across engine/mode switches.
 */

export const PlaybackStatus = {
  IDLE: 'idle',
  PREPARING: 'preparing',
  PLAYING: 'playing',
  PAUSED: 'paused',
  ERROR: 'error',
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

export function applyQueueMove(queue = [], queueItemId, toIndex) {
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
  const defaultTimeoutMs = config.commandTimeoutMs || 5000

  let state = {
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
    playMode: normalizePlaybackMode(
      config.initialPlayMode || PlaybackMode.ORDER,
    ),
  }

  const listeners = new Set()
  const metrics = {
    intentsIssued: 0,
    stateBackfills: 0,
    commandsSent: {
      play: 0,
      pause: 0,
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

    // 3. Activate new adapter with current epoch and authoritative state callback
    if (activeAdapter && typeof activeAdapter.activate === 'function') {
      activeAdapter.activate(newEpoch, (epoch, stateUpdate) => {
        // Guard against late / superseded engine callbacks
        if (epoch !== currentEpoch) {
          return
        }
        handleAuthoritativeStateUpdate(stateUpdate)
      })
    }
  }

  function handleAuthoritativeStateUpdate(update) {
    // Guard against stale snapshots / out-of-order replay (Spec line 20)
    if (
      typeof update.queueVersion === 'number' &&
      update.queueVersion < state.queueVersion
    ) {
      return
    }

    metrics.stateBackfills += 1

    let hasChange = false
    const nextState = { ...state }

    if (update.status && update.status !== state.status) {
      nextState.status = update.status
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

  async function executeWithTimeout(promise, timeoutMs) {
    let timer
    const timeoutPromise = new Promise((_, reject) => {
      timer = setTimeout(() => {
        reject({
          code: 'COMMAND_TIMEOUT',
          message: `Playback command timed out after ${timeoutMs}ms`,
        })
      }, timeoutMs)
    })

    try {
      return await Promise.race([promise, timeoutPromise])
    } finally {
      clearTimeout(timer)
    }
  }

  async function dispatchCommand(commandName, executor, isPlayIntent = false) {
    if (!activeAdapter) {
      const err = {
        code: 'NO_ACTIVE_ENGINE',
        message: 'No playback engine active',
      }
      state = { ...state, status: PlaybackStatus.ERROR, error: err }
      notifySubscribers()
      throw err
    }

    metrics.intentsIssued += 1
    if (metrics.commandsSent[commandName] !== undefined) {
      metrics.commandsSent[commandName] += 1
    }

    if (isPlayIntent) {
      // Mark preparing; request sent does NOT equal confirmed playback
      state = { ...state, status: PlaybackStatus.PREPARING, error: null }
      notifySubscribers()
    }

    try {
      return await executeWithTimeout(executor(currentEpoch), defaultTimeoutMs)
    } catch (err) {
      const formattedError = {
        code: err?.code || `${commandName.toUpperCase()}_REQUEST_FAILED`,
        message: err?.message || `Failed to execute ${commandName}`,
      }
      state = { ...state, status: PlaybackStatus.ERROR, error: formattedError }
      notifySubscribers()
      throw formattedError
    }
  }

  return {
    setAdapter,
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
      dispatchCommand('play', (epoch) => activeAdapter.play(epoch), true),
    pause: () =>
      dispatchCommand('pause', (epoch) => activeAdapter.pause(epoch)),
    seek: (pos) =>
      dispatchCommand('seek', (epoch) => activeAdapter.seek(epoch, pos)),
    setVolume: (vol) =>
      dispatchCommand('volume', (epoch) => activeAdapter.setVolume(epoch, vol)),
    skipNext: () =>
      dispatchCommand('skipNext', (epoch) => activeAdapter.skipNext(epoch)),
    skipPrev: () =>
      dispatchCommand('skipPrev', (epoch) =>
        activeAdapter.skipPrev(epoch, state.position),
      ),
    setPlayMode: (mode) => {
      const normalized = normalizePlaybackMode(mode)
      return dispatchCommand('setPlayMode', (epoch) =>
        activeAdapter.setPlayMode(epoch, normalized),
      )
    },
    // Queue editing contract intents
    addQueueItems: (items, index = -1) =>
      dispatchCommand('addQueueItems', (epoch) =>
        activeAdapter.addQueueItems(epoch, items, index),
      ),
    removeQueueItem: (queueItemId) =>
      dispatchCommand('removeQueueItem', (epoch) =>
        activeAdapter.removeQueueItem(epoch, queueItemId),
      ),
    moveQueueItem: (queueItemId, toIndex) =>
      dispatchCommand('moveQueueItem', (epoch) =>
        activeAdapter.moveQueueItem(epoch, queueItemId, toIndex),
      ),
    clearQueue: () =>
      dispatchCommand('clearQueue', (epoch) => activeAdapter.clearQueue(epoch)),
    skipToQueueItem: (queueItemId) =>
      dispatchCommand('skipToQueueItem', (epoch) =>
        activeAdapter.skipToQueueItem(epoch, queueItemId),
      ),
    playNext: (items) => {
      const curIdx = state.queue.findIndex(
        (it) => it.queueItemId === state.currentQueueItemId,
      )
      const targetIndex = curIdx >= 0 ? curIdx + 1 : state.queue.length
      return dispatchCommand('addQueueItems', (epoch) =>
        activeAdapter.addQueueItems(epoch, items, targetIndex),
      )
    },
    playAll: async (items, startIndex = 0) => {
      metrics.intentsIssued += 1
      if (!activeAdapter) {
        throw { code: 'NO_ACTIVE_ENGINE', message: 'No playback engine active' }
      }
      metrics.commandsSent.clearQueue += 1
      await executeWithTimeout(
        activeAdapter.clearQueue(currentEpoch),
        defaultTimeoutMs,
      )
      metrics.commandsSent.addQueueItems += 1
      await executeWithTimeout(
        activeAdapter.addQueueItems(currentEpoch, items, 0),
        defaultTimeoutMs,
      )
      if (items.length > 0 && startIndex >= 0 && startIndex < items.length) {
        metrics.commandsSent.skipToQueueItem += 1
        await executeWithTimeout(
          activeAdapter.skipToQueueItem(
            currentEpoch,
            items[startIndex].queueItemId,
          ),
          defaultTimeoutMs,
        )
      }
    },
    // Direct backfill channel for authoritative server snapshots
    backfillState: (update) => handleAuthoritativeStateUpdate(update),
  }
}
