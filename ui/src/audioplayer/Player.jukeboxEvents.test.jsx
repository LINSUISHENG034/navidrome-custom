import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'

vi.mock('./jukeboxClient', () => ({
  default: {
    play: vi.fn(() => Promise.resolve({})),
    pause: vi.fn(() => Promise.resolve({})),
    status: vi.fn(() => Promise.resolve({ playing: true })),
    volume: vi.fn(() => Promise.resolve({})),
  },
}))

vi.mock('./jukeboxCommandQueue', () => ({
  enqueueJukeboxCommand: vi.fn((fn) => fn()),
}))

import jukeboxClient from './jukeboxClient'
import { enqueueJukeboxCommand } from './jukeboxCommandQueue'

describe('Jukebox visibility guard logic', () => {
  let originalHidden

  beforeEach(() => {
    vi.clearAllMocks()
    originalHidden = Object.getOwnPropertyDescriptor(document, 'hidden')
  })

  afterEach(() => {
    if (originalHidden) {
      Object.defineProperty(document, 'hidden', originalHidden)
    } else {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
    }
  })

  describe('onAudioPause guard', () => {
    const simulateOnAudioPause = (jukeboxMode) => {
      if (jukeboxMode && !document.hidden) {
        enqueueJukeboxCommand(() => jukeboxClient.pause())
      }
    }

    it('forwards pause to jukebox when tab is visible (user-initiated)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
      simulateOnAudioPause(true)
      expect(jukeboxClient.pause).toHaveBeenCalledTimes(1)
    })

    it('suppresses pause when tab is hidden (browser-initiated)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => true,
      })
      simulateOnAudioPause(true)
      expect(jukeboxClient.pause).not.toHaveBeenCalled()
    })

    it('does not forward pause when not in jukebox mode', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
      simulateOnAudioPause(false)
      expect(jukeboxClient.pause).not.toHaveBeenCalled()
    })
  })

  describe('onAudioPlay guard', () => {
    const simulateOnAudioPlay = (jukeboxMode) => {
      if (jukeboxMode && !document.hidden) {
        enqueueJukeboxCommand(() => jukeboxClient.play())
      }
    }

    it('forwards play to jukebox when tab is visible (user-initiated)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
      simulateOnAudioPlay(true)
      expect(jukeboxClient.play).toHaveBeenCalledTimes(1)
    })

    it('suppresses play when tab is hidden (browser auto-resume)', () => {
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => true,
      })
      simulateOnAudioPlay(true)
      expect(jukeboxClient.play).not.toHaveBeenCalled()
    })
  })
})
