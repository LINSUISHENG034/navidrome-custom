import React from 'react'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import RemotePlayerPrototype from './RemotePlayer.prototype'

const snapshot = {
  sessionStatus: {
    currentIndex: 1,
    trackId: 'song-2',
    playing: true,
    position: 42,
    gain: 0.73,
    ownershipState: 'attached',
  },
  jukeboxPlaylist: {
    currentIndex: 1,
    playing: true,
    position: 42,
    gain: 0.73,
    entry: [
      {
        id: 'song-1',
        title: 'After Hours',
        artist: 'The Weeknd',
        album: 'After Hours',
        duration: 201,
      },
      {
        id: 'song-2',
        title: 'Blue in Green',
        artist: 'Miles Davis',
        album: 'Kind of Blue',
        duration: 250,
      },
      {
        id: 'song-3',
        title: 'So What',
        artist: 'Miles Davis',
        album: 'Kind of Blue',
        duration: 545,
      },
    ],
  },
}

describe('<RemotePlayerPrototype />', () => {
  it('renders current track, queue, position, and gain from server snapshots', () => {
    const { container } = render(
      <RemotePlayerPrototype snapshot={snapshot} onCommand={vi.fn()} />,
    )

    expect(container.querySelector('audio')).toBeNull()
    expect(
      screen.getByRole('heading', { name: 'Remote player prototype' }),
    ).toBeInTheDocument()
    expect(screen.getByText('Playing')).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Blue in Green' }),
    ).toBeInTheDocument()

    const queue = screen.getByRole('list', { name: 'Queue' })
    expect(
      within(queue).getByText('After Hours — The Weeknd'),
    ).toBeInTheDocument()
    expect(
      within(queue).getByText('Blue in Green — Miles Davis'),
    ).toBeInTheDocument()
    expect(
      within(queue).getByText('Blue in Green — Miles Davis').closest('li'),
    ).toHaveAttribute('aria-current', 'true')
    expect(screen.getByText('Position: 42 seconds')).toBeInTheDocument()
    expect(screen.getByText('Gain: 0.73')).toBeInTheDocument()
    expect(screen.getByRole('slider', { name: 'Seek' })).toHaveValue('42')
    expect(screen.getByRole('slider', { name: 'Volume' })).toHaveValue('0.73')
  })

  it('uses the server index when a media ID appears more than once in the queue', () => {
    const repeatedTrack = {
      id: 'same-song',
      title: 'Repeated Track',
      artist: 'Same Artist',
      album: 'Same Album',
      duration: 180,
    }
    const repeatedSnapshot = {
      sessionStatus: {
        ...snapshot.sessionStatus,
        currentIndex: 2,
        trackId: 'same-song',
      },
      jukeboxPlaylist: {
        ...snapshot.jukeboxPlaylist,
        currentIndex: 2,
        entry: [
          repeatedTrack,
          { ...repeatedTrack, id: 'middle-song', title: 'Middle Track' },
          repeatedTrack,
        ],
      },
    }
    const onCommand = vi.fn()

    render(
      <RemotePlayerPrototype
        snapshot={repeatedSnapshot}
        onCommand={onCommand}
      />,
    )

    const queueItems = within(
      screen.getByRole('list', { name: 'Queue' }),
    ).getAllByRole('listitem')
    expect(queueItems[0]).toHaveAttribute('aria-current', 'false')
    expect(queueItems[2]).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
    expect(onCommand).toHaveBeenCalledWith({
      type: 'skip',
      index: 1,
      offset: 0,
    })
  })

  it.each([
    ['missing currentIndex', undefined],
    ['no-selection index', -1],
    ['out-of-range currentIndex', 3],
  ])('does not select a queue item for %s', (_case, currentIndex) => {
    const sessionStatus = {
      ...snapshot.sessionStatus,
      trackId: 'song-2',
      playing: false,
    }
    if (currentIndex === undefined) {
      delete sessionStatus.currentIndex
    } else {
      sessionStatus.currentIndex = currentIndex
    }

    const onCommand = vi.fn()
    const invalidSnapshot = { ...snapshot, sessionStatus }
    render(
      <RemotePlayerPrototype
        snapshot={invalidSnapshot}
        onCommand={onCommand}
      />,
    )

    expect(
      screen.getByRole('heading', { name: 'Nothing playing' }),
    ).toBeInTheDocument()
    const queueItems = within(
      screen.getByRole('list', { name: 'Queue' }),
    ).getAllByRole('listitem')
    expect(
      queueItems.every((item) => item.getAttribute('aria-current') !== 'true'),
    ).toBe(true)
    expect(screen.getByRole('slider', { name: 'Seek' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Play' })).toBeDisabled()
    expect(onCommand).not.toHaveBeenCalled()
  })

  it('sends explicit user playback and level commands without changing authority', () => {
    const onCommand = vi.fn()
    const { rerender } = render(
      <RemotePlayerPrototype snapshot={snapshot} onCommand={onCommand} />,
    )

    expect(onCommand).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    fireEvent.change(screen.getByRole('slider', { name: 'Seek' }), {
      target: { value: '90' },
    })
    fireEvent.change(screen.getByRole('slider', { name: 'Volume' }), {
      target: { value: '0.61' },
    })

    expect(onCommand.mock.calls.map(([command]) => command)).toEqual([
      { type: 'skip', index: 0, offset: 0 },
      { type: 'pause' },
      { type: 'skip', index: 2, offset: 0 },
      { type: 'seek', position: 90 },
      { type: 'volume', gain: 0.61 },
    ])

    const pausedSnapshot = {
      ...snapshot,
      sessionStatus: { ...snapshot.sessionStatus, playing: false },
    }
    rerender(
      <RemotePlayerPrototype snapshot={pausedSnapshot} onCommand={onCommand} />,
    )
    expect(onCommand).toHaveBeenCalledTimes(5)
    fireEvent.click(screen.getByRole('button', { name: 'Play' }))
    expect(onCommand).toHaveBeenNthCalledWith(6, { type: 'play' })
  })

  it('changes displayed playback state only when a server snapshot changes', () => {
    const onCommand = vi.fn()
    const { rerender } = render(
      <RemotePlayerPrototype snapshot={snapshot} onCommand={onCommand} />,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    fireEvent.change(screen.getByRole('slider', { name: 'Seek' }), {
      target: { value: '90' },
    })
    fireEvent.change(screen.getByRole('slider', { name: 'Volume' }), {
      target: { value: '0.5' },
    })

    expect(screen.getByText('Playing')).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Blue in Green' }),
    ).toBeInTheDocument()
    expect(screen.getByText('Position: 42 seconds')).toBeInTheDocument()
    expect(screen.getByText('Gain: 0.73')).toBeInTheDocument()
    expect(screen.getByRole('slider', { name: 'Seek' })).toHaveValue('42')
    expect(screen.getByRole('slider', { name: 'Volume' })).toHaveValue('0.73')

    const nextSnapshot = {
      ...snapshot,
      sessionStatus: {
        ...snapshot.sessionStatus,
        currentIndex: 2,
        trackId: 'song-3',
        playing: false,
        position: 18,
        gain: 0.4,
      },
      jukeboxPlaylist: {
        ...snapshot.jukeboxPlaylist,
        currentIndex: 2,
        playing: false,
        position: 18,
        gain: 0.4,
      },
    }
    rerender(
      <RemotePlayerPrototype snapshot={nextSnapshot} onCommand={onCommand} />,
    )

    expect(screen.getByText('Paused')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'So What' })).toBeInTheDocument()
    expect(screen.getByText('Position: 18 seconds')).toBeInTheDocument()
    expect(screen.getByText('Gain: 0.4')).toBeInTheDocument()
    expect(screen.getByRole('slider', { name: 'Seek' })).toHaveValue('18')
    expect(screen.getByRole('slider', { name: 'Volume' })).toHaveValue('0.4')
    expect(onCommand).toHaveBeenCalledTimes(4)
  })

  it('keeps shared views snapshot-controlled and sends no lifecycle commands', () => {
    const onCommand = vi.fn()
    const renderPair = (currentSnapshot) => (
      <>
        <RemotePlayerPrototype
          snapshot={currentSnapshot}
          onCommand={onCommand}
        />
        <RemotePlayerPrototype
          snapshot={currentSnapshot}
          onCommand={onCommand}
        />
      </>
    )
    const { rerender, unmount } = render(renderPair(snapshot))
    const initialViews = screen.getAllByRole('region', {
      name: 'Remote player prototype',
    })

    expect(initialViews).toHaveLength(2)
    expect(onCommand).not.toHaveBeenCalled()
    fireEvent.click(
      within(initialViews[0]).getByRole('button', { name: 'Next' }),
    )
    expect(onCommand).toHaveBeenCalledWith({
      type: 'skip',
      index: 2,
      offset: 0,
    })
    initialViews.forEach((view) => {
      expect(
        within(view).getByRole('heading', { name: 'Blue in Green' }),
      ).toBeInTheDocument()
    })

    const nextSnapshot = {
      ...snapshot,
      sessionStatus: {
        ...snapshot.sessionStatus,
        currentIndex: 2,
        trackId: 'song-3',
      },
      jukeboxPlaylist: { ...snapshot.jukeboxPlaylist, currentIndex: 2 },
    }
    rerender(renderPair(nextSnapshot))
    screen
      .getAllByRole('region', { name: 'Remote player prototype' })
      .forEach((view) => {
        expect(
          within(view).getByRole('heading', { name: 'So What' }),
        ).toBeInTheDocument()
      })
    expect(onCommand).toHaveBeenCalledTimes(1)

    window.dispatchEvent(new Event('pagehide'))
    unmount()
    window.dispatchEvent(new Event('pagehide'))
    expect(onCommand.mock.calls.map(([command]) => command)).toEqual([
      { type: 'skip', index: 2, offset: 0 },
    ])

    const freshSnapshot = {
      ...snapshot,
      sessionStatus: {
        ...snapshot.sessionStatus,
        currentIndex: 0,
        trackId: 'fresh-song',
        playing: false,
        position: 0,
        gain: 0.5,
      },
      jukeboxPlaylist: {
        ...snapshot.jukeboxPlaylist,
        currentIndex: 0,
        entry: [
          {
            id: 'fresh-song',
            title: 'Fresh Queue',
            artist: 'New Artist',
            album: 'New Album',
            duration: 120,
          },
        ],
      },
    }
    const remounted = render(
      <RemotePlayerPrototype snapshot={freshSnapshot} onCommand={onCommand} />,
    )

    expect(
      screen.getByRole('heading', { name: 'Fresh Queue' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByText('After Hours — The Weeknd'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('Blue in Green — Miles Davis'),
    ).not.toBeInTheDocument()
    expect(onCommand).toHaveBeenCalledTimes(1)
    remounted.unmount()
  })
})
