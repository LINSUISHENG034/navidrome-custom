import React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Player } from './Player'
import jukeboxClient from './jukeboxClient'
import { uiVolumeToRemoteGain } from './volumeProfiles'

const dispatchMock = vi.fn()
const playerState = {
  queue: [{ trackId: 't1', uuid: 'u1', song: { id: 't1' } }],
  current: { trackId: 't1', uuid: 'u1', song: { id: 't1' } },
  clear: false,
  mode: 'order',
  volume: 0.5,
  jukeboxMode: true,
  jukeboxDevice: 'Bluetooth 24:C4:06:FA:00:37',
  jukeboxControl: { ownershipState: 'attached' },
  jukeboxRemote: {
    gain: 0.5,
    deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
  },
  audioInstance: null,
}

vi.mock('@material-ui/core', async () => {
  const actual = await import('@material-ui/core')
  return {
    ...actual,
    useMediaQuery: vi.fn(() => true),
  }
})

vi.mock('react-admin', () => ({
  createMuiTheme: (theme) => theme,
  useAuthState: () => ({ authenticated: true }),
  useDataProvider: () => ({
    getOne: vi.fn(() => Promise.resolve({ data: {} })),
  }),
  useTranslate: () => (key) => key,
}))

vi.mock('react-redux', () => ({
  useDispatch: () => dispatchMock,
  useSelector: (selector) =>
    selector({
      player: playerState,
      settings: { notifications: false },
      replayGain: { gainMode: 'none' },
      activity: { streamReconnected: false },
    }),
}))

vi.mock('react-ga', () => ({
  default: {
    event: vi.fn(),
  },
}))

vi.mock('react-hotkeys', () => ({
  GlobalHotKeys: () => null,
}))

vi.mock('navidrome-music-player', () => ({
  default: function MockMusicPlayer({
    onAudioVolumeChange,
    getAudioInstance,
  }) {
    React.useEffect(() => {
      getAudioInstance?.(null)
    }, [getAudioInstance])

    return (
      <button
        data-testid="trigger-volume-change"
        onClick={() => onAudioVolumeChange(0.36)}
      >
        change volume
      </button>
    )
  },
}))

vi.mock('../themes/useCurrentTheme', () => ({
  default: () => ({ player: { theme: 'dark' } }),
}))

vi.mock('./styles', () => ({
  default: () => ({ player: 'player' }),
}))

vi.mock('./AudioTitle', () => ({
  default: () => null,
}))

vi.mock('./PlayerToolbar', () => ({
  default: () => null,
}))

vi.mock('./locale', () => ({
  default: () => ({}),
}))

vi.mock('./jukeboxClient', () => ({
  default: {
    volume: vi.fn(() => Promise.resolve({})),
    attachSession: vi.fn(() =>
      Promise.resolve({ sessionId: 'jukebox-session:admin' }),
    ),
    detachSession: vi.fn(() => Promise.resolve({})),
    heartbeatSession: vi.fn(() => Promise.resolve({})),
  },
}))

vi.mock('./jukeboxCommandQueue', () => ({
  enqueueJukeboxCommand: vi.fn((fn) => Promise.resolve(fn())),
}))

describe('<Player /> Bluetooth volume profile', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.setItem('username', 'admin')
    sessionStorage.clear()
  })

  it('maps outgoing jukebox volume changes through the Bluetooth profile', async () => {
    render(<Player />)

    fireEvent.click(screen.getByTestId('trigger-volume-change'))

    await waitFor(() => {
      expect(jukeboxClient.volume).toHaveBeenCalledWith(
        uiVolumeToRemoteGain(0.6, {
          deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
        }),
      )
    })
  })
})
