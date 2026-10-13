import { beforeEach, describe, expect, it, vi } from 'vitest'
import httpClient from '../dataProvider/httpClient'
import jukeboxClient from './jukeboxClient'

vi.mock('../dataProvider/httpClient', () => ({
  default: vi.fn(),
}))

describe('jukeboxClient.sessionSnapshot', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('loads the session-bound snapshot through the authenticated HTTP client', async () => {
    const payload = {
      sessionStatus: { sessionId: 'session-a' },
      jukeboxPlaylist: { currentIndex: 1, entry: [{ id: 'song-a' }] },
    }
    httpClient.mockResolvedValue({ json: payload })

    await expect(jukeboxClient.sessionSnapshot('session / a')).resolves.toEqual(
      payload,
    )
    expect(httpClient).toHaveBeenCalledWith(
      '/api/jukebox/session/snapshot?sessionId=session%20%2F%20a',
    )
  })
})
