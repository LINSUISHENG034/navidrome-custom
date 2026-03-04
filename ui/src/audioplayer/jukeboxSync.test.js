import { describe, expect, it, vi } from 'vitest'
import {
  computeQueueDiff,
  enforceBrowserAudioPause,
  syncJukeboxQueueIncremental,
  syncJukeboxSeek,
  syncJukeboxTrackChange,
} from './jukeboxSync'

describe('computeQueueDiff', () => {
  it('detects added tracks', () => {
    const oldQueue = [{ trackId: 'a' }, { trackId: 'b' }]
    const newQueue = [{ trackId: 'a' }, { trackId: 'b' }, { trackId: 'c' }]
    const diff = computeQueueDiff(oldQueue, newQueue)
    expect(diff.added).toEqual([{ trackId: 'c', index: 2 }])
    expect(diff.removed).toEqual([])
  })

  it('detects removed tracks', () => {
    const oldQueue = [{ trackId: 'a' }, { trackId: 'b' }, { trackId: 'c' }]
    const newQueue = [{ trackId: 'a' }, { trackId: 'c' }]
    const diff = computeQueueDiff(oldQueue, newQueue)
    expect(diff.removed).toEqual([1]) // index 1 in old queue
    expect(diff.added).toEqual([])
  })

  it('detects full replacement when order completely changes', () => {
    const oldQueue = [{ trackId: 'a' }, { trackId: 'b' }]
    const newQueue = [{ trackId: 'c' }, { trackId: 'd' }]
    const diff = computeQueueDiff(oldQueue, newQueue)
    expect(diff.fullReplace).toBe(true)
  })

  it('returns empty diff when queues are identical', () => {
    const queue = [{ trackId: 'a' }, { trackId: 'b' }]
    const diff = computeQueueDiff(queue, queue)
    expect(diff.added).toEqual([])
    expect(diff.removed).toEqual([])
    expect(diff.fullReplace).toBe(false)
  })
})

describe('syncJukeboxQueueIncremental', () => {
  it('sends add operations for added tracks', async () => {
    const client = {
      add: vi.fn(() => Promise.resolve({})),
      remove: vi.fn(() => Promise.resolve({})),
      set: vi.fn(() => Promise.resolve({})),
    }
    const diff = { added: [{ trackId: 'c', index: 2 }], removed: [], fullReplace: false }
    await syncJukeboxQueueIncremental(client, diff)
    expect(client.add).toHaveBeenCalledWith(['c'])
    expect(client.remove).not.toHaveBeenCalled()
    expect(client.set).not.toHaveBeenCalled()
  })

  it('sends remove operations for removed tracks (reverse order)', async () => {
    const client = {
      add: vi.fn(() => Promise.resolve({})),
      remove: vi.fn(() => Promise.resolve({})),
      set: vi.fn(() => Promise.resolve({})),
    }
    const diff = { added: [], removed: [1, 3], fullReplace: false }
    await syncJukeboxQueueIncremental(client, diff)
    // Must remove in reverse order to keep indices valid
    expect(client.remove).toHaveBeenCalledTimes(2)
    expect(client.remove).toHaveBeenNthCalledWith(1, 3)
    expect(client.remove).toHaveBeenNthCalledWith(2, 1)
  })

  it('falls back to set for full replacement', async () => {
    const client = {
      add: vi.fn(() => Promise.resolve({})),
      remove: vi.fn(() => Promise.resolve({})),
      set: vi.fn(() => Promise.resolve({})),
    }
    const diff = {
      added: [],
      removed: [],
      fullReplace: true,
      newTrackIds: ['x', 'y'],
    }
    await syncJukeboxQueueIncremental(client, diff)
    expect(client.set).toHaveBeenCalledWith(['x', 'y'])
  })
})

describe('syncJukeboxTrackChange', () => {
  it('sends skip to correct index', async () => {
    const client = {
      skip: vi.fn(() => Promise.resolve({})),
    }
    const audioLists = [
      { uuid: 'u1', trackId: 't1' },
      { uuid: 'u2', trackId: 't2' },
      { uuid: 'u3', trackId: 't3' },
    ]

    await syncJukeboxTrackChange(client, {
      audioLists,
      playId: 'u2',
      audioInfo: { currentTime: 17 },
    })

    expect(client.skip).toHaveBeenCalledWith(1, 17)
  })
})

describe('syncJukeboxSeek', () => {
  it('syncs seek operation to jukebox', async () => {
    const client = {
      seek: vi.fn(() => Promise.resolve({})),
    }

    await syncJukeboxSeek(client, { currentTime: 98.6 })
    expect(client.seek).toHaveBeenCalledWith(98)
  })
})

describe('enforceBrowserAudioPause', () => {
  it('pauses browser audio in jukebox mode', () => {
    const audio = { paused: false, pause: vi.fn(), play: vi.fn() }
    enforceBrowserAudioPause(audio, true)
    expect(audio.pause).toHaveBeenCalled()
  })

  it('does not resume audio when leaving jukebox mode (caller handles this)', () => {
    const audio = { paused: true, pause: vi.fn(), play: vi.fn() }
    enforceBrowserAudioPause(audio, false)
    // enforceBrowserAudioPause only pauses; resuming is handled by DeviceSelector
    expect(audio.pause).not.toHaveBeenCalled()
    expect(audio.play).not.toHaveBeenCalled()
  })
})
