import React from 'react'
import { render, screen } from '@testing-library/react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { Provider } from 'react-redux'
import AudioTitle from './AudioTitle'

vi.mock('@material-ui/core', async () => {
  const actual = await import('@material-ui/core')
  return {
    ...actual,
    useMediaQuery: vi.fn(),
  }
})

vi.mock('react-router-dom', () => ({
  Link: React.forwardRef(({ to, children, ...props }, ref) => (
    <a href={to} ref={ref} {...props}>
      {children}
    </a>
  )),
}))

vi.mock('react-dnd', () => ({
  useDrag: vi.fn(() => [null, () => {}]),
}))

const renderWithStore = (ui, state = {}) => {
  const store = {
    getState: () => state,
    subscribe: () => () => {},
    dispatch: () => {},
  }
  return render(<Provider store={store}>{ui}</Provider>)
}

describe('<AudioTitle />', () => {
  const baseSong = {
    id: 'song-1',
    albumId: 'album-1',
    playlistId: 'playlist-1',
    title: 'Test Song',
    artist: 'Artist',
    album: 'Album',
    year: '2020',
  }

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('links to playlist when playlistId is provided', () => {
    const audioInfo = { trackId: 'track-1', song: baseSong }
    renderWithStore(
      <AudioTitle audioInfo={audioInfo} gainInfo={{}} isMobile={false} />,
      { player: { jukeboxMode: false, current: {}, queue: [], jukeboxSession: null } },
    )
    const link = screen.getByRole('link')
    expect(link.getAttribute('href')).toBe('/playlist/playlist-1/show')
  })

  it('falls back to album link when no playlistId', () => {
    const audioInfo = {
      trackId: 'track-1',
      song: { ...baseSong, playlistId: undefined },
    }
    renderWithStore(
      <AudioTitle audioInfo={audioInfo} gainInfo={{}} isMobile={false} />,
      { player: { jukeboxMode: false, current: {}, queue: [], jukeboxSession: null } },
    )
    const link = screen.getByRole('link')
    expect(link.getAttribute('href')).toBe('/album/album-1/show')
  })

  it('renders remote track title from jukebox session state in jukebox mode', () => {
    const localAudioInfo = { trackId: 'track-local', song: { ...baseSong, title: 'Local Song' } }
    const remoteTrack = {
      trackId: 'track-remote',
      song: { ...baseSong, title: 'Remote Song', playlistId: undefined },
    }
    renderWithStore(
      <AudioTitle audioInfo={localAudioInfo} gainInfo={{}} isMobile={false} />,
      {
        player: {
          jukeboxMode: true,
          current: localAudioInfo,
          queue: [
            { trackId: 'track-local', song: { ...baseSong, title: 'Local Song' } },
            remoteTrack,
          ],
          jukeboxSession: { currentIndex: 1, trackId: 'track-remote' },
        },
      },
    )

    expect(screen.getByText('Remote Song')).toBeInTheDocument()
    expect(screen.queryByText('Local Song')).not.toBeInTheDocument()
  })
})
