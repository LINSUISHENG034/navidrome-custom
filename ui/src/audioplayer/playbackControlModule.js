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
      dispatchCommand('skipPrev', (epoch) => activeAdapter.skipPrev(epoch)),
    // Direct backfill channel for authoritative server snapshots
    backfillState: (update) => handleAuthoritativeStateUpdate(update),
  }
}
