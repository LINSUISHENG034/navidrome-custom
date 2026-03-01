import { describe, expect, it, vi } from 'vitest'
import {
  enforceBrowserAudioMode,
  syncJukeboxQueue,
  syncJukeboxSeek,
  syncJukeboxTrackChange,
} from './jukeboxSync'

describe('jukeboxSync', () => {
  it('syncs queue and current track index for track changes', async () => {
    const client = {
      set: vi.fn(() => Promise.resolve({})),
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

    expect(client.set).toHaveBeenCalledWith(['t1', 't2', 't3'])
    expect(client.skip).toHaveBeenCalledWith(1, 17)
  })

  it('falls back to trackId lookup when playId is not present', async () => {
    const client = {
      set: vi.fn(() => Promise.resolve({})),
      skip: vi.fn(() => Promise.resolve({})),
    }
    const audioLists = [
      { uuid: 'u1', trackId: 't1' },
      { uuid: 'u2', trackId: 't2' },
    ]

    await syncJukeboxTrackChange(client, {
      audioLists,
      playId: '',
      audioInfo: { trackId: 't2', currentTime: 3.9 },
    })

    expect(client.skip).toHaveBeenCalledWith(1, 3)
  })

  it('syncs queue changes to jukebox', async () => {
    const client = {
      set: vi.fn(() => Promise.resolve({})),
    }
    const audioLists = [{ trackId: 'a' }, { trackId: 'b' }]

    await syncJukeboxQueue(client, audioLists)

    expect(client.set).toHaveBeenCalledWith(['a', 'b'])
  })

  it('syncs seek operation to jukebox', async () => {
    const client = {
      seek: vi.fn(() => Promise.resolve({})),
    }

    await syncJukeboxSeek(client, { currentTime: 98.6 })

    expect(client.seek).toHaveBeenCalledWith(98)
  })

  it('mutes browser audio in jukebox mode and restores on local mode', () => {
    const audio = { muted: false }

    enforceBrowserAudioMode(audio, true)
    expect(audio.muted).toBe(true)

    enforceBrowserAudioMode(audio, false)
    expect(audio.muted).toBe(false)
  })
})
