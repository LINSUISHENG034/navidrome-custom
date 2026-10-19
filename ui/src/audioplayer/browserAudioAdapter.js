/**
 * Browser Audio Adapter
 *
 * Core responsibility:
 * Concrete playback engine adapter managing a real browser HTMLAudioElement for local playback.
 * Legally holds the player's real media element, translates real DOM media events into authoritative local state updates,
 * and cleanly stops, cleans up, and invalidates late callbacks during engine deactivation.
 */

import { PlaybackStatus } from './playbackControlModule'

export function createBrowserAudioAdapter(options = {}) {
  let activeEpoch = 0
  let isActive = false
  let stateCallback = null
  let audioElement = options.audioElement || null
  let ownsElement = false

  // Event handlers bound to current active instance
  const eventListeners = {}

  function ensureAudioElement() {
    if (!audioElement) {
      audioElement = document.createElement('audio')
      audioElement.className = 'navidrome-browser-audio-engine'
      ownsElement = true
    }
    return audioElement
  }

  function handleMediaEvent(eventName) {
    if (!isActive || !stateCallback) return

    const el = audioElement
    if (!el) return

    switch (eventName) {
      case 'play':
      case 'playing':
        stateCallback(activeEpoch, {
          status: PlaybackStatus.PLAYING,
          position: el.currentTime || 0,
          duration: el.duration || 0,
          volume: el.volume,
          isMuted: el.muted,
        })
        break
      case 'pause':
        stateCallback(activeEpoch, {
          status: PlaybackStatus.PAUSED,
          position: el.currentTime || 0,
        })
        break
      case 'timeupdate':
        stateCallback(activeEpoch, {
          position: el.currentTime || 0,
        })
        break
      case 'volumechange':
        stateCallback(activeEpoch, {
          volume: el.volume,
          isMuted: el.muted,
        })
        break
      case 'ended':
        stateCallback(activeEpoch, {
          status: PlaybackStatus.PAUSED,
          position: el.duration || el.currentTime || 0,
        })
        break
      case 'error': {
        const mediaError = el.error
        stateCallback(activeEpoch, {
          status: PlaybackStatus.ERROR,
          error: {
            code: 'MEDIA_ELEMENT_ERROR',
            message:
              mediaError?.message || 'Audio element encountered playback error',
          },
        })
        break
      }
      default:
        break
    }
  }

  function bindEvents(el) {
    if (!el) return
    const events = [
      'play',
      'playing',
      'pause',
      'timeupdate',
      'volumechange',
      'ended',
      'error',
    ]
    events.forEach((evt) => {
      const listener = () => handleMediaEvent(evt)
      eventListeners[evt] = listener
      el.addEventListener(evt, listener)
    })
  }

  function unbindEvents(el) {
    if (!el) return
    Object.entries(eventListeners).forEach(([evt, listener]) => {
      el.removeEventListener(evt, listener)
      delete eventListeners[evt]
    })
  }

  function attachElement(el) {
    if (audioElement && audioElement !== el) {
      unbindEvents(audioElement)
    }
    audioElement = el
    ownsElement = false
    if (isActive && el) {
      bindEvents(el)
    }
  }

  function activate(epoch, onStateChange) {
    activeEpoch = epoch
    isActive = true
    stateCallback = onStateChange

    const el = ensureAudioElement()
    bindEvents(el)
  }

  function deactivate() {
    isActive = false
    activeEpoch = -1

    if (audioElement) {
      try {
        audioElement.pause()
      } catch {
        // Safe ignore
      }
      unbindEvents(audioElement)

      if (ownsElement && audioElement.parentNode) {
        audioElement.parentNode.removeChild(audioElement)
      }
      // Clear source to release network streams and memory
      audioElement.removeAttribute('src')
      if (typeof audioElement.load === 'function') {
        try {
          audioElement.load()
        } catch {
          // Safe ignore in environments without full media stack (e.g. jsdom)
        }
      }
    }

    stateCallback = null
  }

  async function play() {
    if (!isActive) return
    const el = ensureAudioElement()
    await el.play()
  }

  async function pause() {
    if (!isActive) return
    const el = ensureAudioElement()
    el.pause()
  }

  async function seek(epoch, positionSeconds) {
    if (!isActive || epoch !== activeEpoch) return
    const el = ensureAudioElement()
    el.currentTime = Math.max(0, positionSeconds)
  }

  async function setVolume(epoch, volumeFraction) {
    if (!isActive || epoch !== activeEpoch) return
    const el = ensureAudioElement()
    const clamped = Math.max(0, Math.min(1, volumeFraction))
    el.volume = clamped
  }

  async function skipNext() {
    if (!isActive) return
    // Forwarded to playlist/track manager when implemented
  }

  async function skipPrev() {
    if (!isActive) return
    // Forwarded to playlist/track manager when implemented
  }

  return {
    activate,
    deactivate,
    attachElement,
    play,
    pause,
    seek,
    setVolume,
    skipNext,
    skipPrev,
    getAudioElement: () => audioElement,
  }
}
