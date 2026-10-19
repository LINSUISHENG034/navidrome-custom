/**
 * Mock Remote Audio Adapter
 *
 * Core responsibility:
 * Concrete playback engine adapter providing deterministic remote playback simulation for verification.
 * Strictly enforces zero browser audio engine creation (no <audio>, no AudioContext, no media stream requests).
 * Confirms state via authoritative remote events and supports deterministic failure/permission/timeout injection.
 */

import { PlaybackStatus } from './playbackControlModule'

export function createMockRemoteAudioAdapter(options = {}) {
  let activeEpoch = 0
  let isActive = false
  let stateCallback = null

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
    skipNext: (epoch) =>
      simulateAction('skipNext', () => ({
        position: 0,
      })),
    skipPrev: (epoch) =>
      simulateAction('skipPrev', () => ({
        position: 0,
      })),
    // Direct injection of authoritative server push (e.g. position progress or track change)
    pushServerSnapshot: (snapshot) => {
      if (!isActive || !stateCallback) return
      remoteState = { ...remoteState, ...snapshot }
      stateCallback(activeEpoch, snapshot)
    },
    setFailure,
    getRemoteState: () => ({ ...remoteState }),
  }
}
