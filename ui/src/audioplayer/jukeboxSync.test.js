import { describe, expect, it, vi } from 'vitest'
import {
  computeQueueDiff,
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
    expect(diff.removed).toEqual([1])
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

  it('treats duplicate additions as incremental changes', () => {
    const diff = computeQueueDiff(
      [{ trackId: 'a' }, { trackId: 'b' }],
      [{ trackId: 'a' }, { trackId: 'b' }, { trackId: 'b' }],
    )
    expect(diff.added).toEqual([{ trackId: 'b', index: 2 }])
    expect(diff.removed).toEqual([])
    expect(diff.fullReplace).toBe(false)
  })

  it('treats removing one duplicate as an incremental removal', () => {
    const diff = computeQueueDiff(
      [{ trackId: 'a' }, { trackId: 'b' }, { trackId: 'b' }],
      [{ trackId: 'a' }, { trackId: 'b' }],
    )
    expect(diff.removed).toEqual([2])
    expect(diff.added).toEqual([])
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
    const diff = {
      added: [{ trackId: 'c', index: 2 }],
      removed: [],
      fullReplace: false,
    }
    await syncJukeboxQueueIncremental(client, diff)
    expect(client.add).toHaveBeenCalledWith(['c'], 2)
    expect(client.remove).not.toHaveBeenCalled()
    expect(client.set).not.toHaveBeenCalled()
  })

  it('preserves a middle insertion instead of append-only add', async () => {
    const client = {
      add: vi.fn(() => Promise.resolve({})),
      remove: vi.fn(() => Promise.resolve({})),
      set: vi.fn(() => Promise.resolve({})),
    }
    const diff = computeQueueDiff(
      [{ trackId: 'a' }, { trackId: 'c' }],
      [{ trackId: 'a' }, { trackId: 'b' }, { trackId: 'c' }],
    )

    await syncJukeboxQueueIncremental(client, diff)

    expect(client.add).toHaveBeenCalledWith(['b'], 1)
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

  it('sends set([]) when the queue becomes empty', async () => {
    const client = {
      add: vi.fn(() => Promise.resolve({})),
      remove: vi.fn(() => Promise.resolve({})),
      set: vi.fn(() => Promise.resolve({})),
    }

    await syncJukeboxQueueIncremental(client, {
      added: [],
      removed: [],
      fullReplace: true,
      newTrackIds: [],
    })

    expect(client.set).toHaveBeenCalledWith([])
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

    expect(client.skip).toHaveBeenCalledWith(1, 0)
  })

  it('resets offset to 0 when switching to a different queue track', async () => {
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
      playId: 'u3',
      audioInfo: { trackId: 't1', currentTime: 87 },
    })

    expect(client.skip).toHaveBeenCalledWith(2, 0)
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
