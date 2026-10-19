/**
 * Browser Audio Adapter
 *
 * Core responsibility:
 * Concrete playback engine adapter managing a real browser HTMLAudioElement for local playback.
 * Legally holds the player's real media element, translates real DOM media events into authoritative local state updates,
 * and cleanly stops, cleans up, and invalidates late callbacks during engine deactivation.
 */

import {
  PlaybackStatus,
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

  let queue = []
  let currentQueueItemId = null
  let queueVersion = 0

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
    queue = []
    currentQueueItemId = null

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

  async function addQueueItems(epoch, items = [], index = -1) {
    if (!isActive || epoch !== activeEpoch) return
    const res = applyQueueAdd(queue, currentQueueItemId, items, index)
    queue = res.queue
    currentQueueItemId = res.currentQueueItemId
    queueVersion += 1

    if (stateCallback) {
      stateCallback(activeEpoch, {
        queue: [...queue],
        currentQueueItemId,
        queueVersion,
      })
    }
  }

  async function removeQueueItem(epoch, queueItemId) {
    if (!isActive || epoch !== activeEpoch) return
    const isCurrentlyPlaying = audioElement && !audioElement.paused
    const res = applyQueueRemove(
      queue,
      currentQueueItemId,
      queueItemId,
      isCurrentlyPlaying,
    )
    if (!res.removed) return

    queue = res.queue
    currentQueueItemId = res.currentQueueItemId
    queueVersion += 1

    if (res.status === PlaybackStatus.IDLE) {
      if (audioElement) {
        try {
          audioElement.pause()
        } catch {
          // Safe ignore
        }
        audioElement.removeAttribute('src')
      }
    } else if (res.successor) {
      if (audioElement) {
        if (res.successor.musicSrc) {
          audioElement.src = res.successor.musicSrc
        }
        audioElement.currentTime = 0
        if (
          res.status === PlaybackStatus.PLAYING &&
          typeof audioElement.play === 'function'
        ) {
          audioElement.play().catch(() => {})
        } else {
          audioElement.pause()
        }
      }
    }

    if (stateCallback) {
      stateCallback(activeEpoch, {
        queue: [...queue],
        currentQueueItemId: res.currentQueueItemId,
        queueVersion,
        ...(res.status ? { status: res.status, position: 0 } : {}),
      })
    }
  }

  async function moveQueueItem(epoch, queueItemId, toIndex) {
    if (!isActive || epoch !== activeEpoch) return
    const res = applyQueueMove(queue, queueItemId, toIndex)
    if (!res.moved) return

    queue = res.queue
    queueVersion += 1

    // Move invariant: moving current playing item keeps playback continuous,
    // does NOT reload audio element, does NOT reset currentTime!

    if (stateCallback) {
      stateCallback(activeEpoch, {
        queue: [...queue],
        currentQueueItemId,
        queueVersion,
      })
    }
  }

  async function clearQueue(epoch) {
    if (!isActive || epoch !== activeEpoch) return
    const res = applyQueueClear()
    queue = res.queue
    currentQueueItemId = res.currentQueueItemId
    queueVersion += 1

    if (audioElement) {
      try {
        audioElement.pause()
      } catch {
        // Safe ignore
      }
      audioElement.removeAttribute('src')
    }

    if (stateCallback) {
      stateCallback(activeEpoch, {
        queue: [],
        currentQueueItemId: null,
        queueVersion,
        status: PlaybackStatus.IDLE,
        position: 0,
      })
    }
  }

  async function skipToQueueItem(epoch, queueItemId) {
    if (!isActive || epoch !== activeEpoch) return
    const target = queue.find((item) => item.queueItemId === queueItemId)
    if (!target) return

    currentQueueItemId = queueItemId
    const isCurrentlyPlaying = audioElement && !audioElement.paused
    if (audioElement) {
      if (target.musicSrc) {
        audioElement.src = target.musicSrc
      }
      audioElement.currentTime = 0
      if (isCurrentlyPlaying) {
        audioElement.play().catch(() => {})
      }
    }

    if (stateCallback) {
      stateCallback(activeEpoch, {
        currentQueueItemId,
        position: 0,
        status: isCurrentlyPlaying
          ? PlaybackStatus.PLAYING
          : PlaybackStatus.PAUSED,
      })
    }
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
    addQueueItems,
    removeQueueItem,
    moveQueueItem,
    clearQueue,
    skipToQueueItem,
    getQueue: () => [...queue],
    getCurrentQueueItemId: () => currentQueueItemId,
    getAudioElement: () => audioElement,
  }
}
