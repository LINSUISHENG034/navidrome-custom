import React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import DeviceSelector from './DeviceSelector'
import httpClient from '../dataProvider/httpClient'
import jukeboxClient from './jukeboxClient'

const notifyMock = vi.fn()

vi.mock('react-admin', () => ({
  useNotify: () => notifyMock,
}))

vi.mock('../config', () => ({
  default: {
    jukeboxEnabled: true,
    bluetoothManagementEnabled: true,
  },
}))

vi.mock('../dataProvider/httpClient', () => ({
  default: vi.fn(),
}))

vi.mock('./jukeboxClient', () => ({
  default: {
    set: vi.fn(() => Promise.resolve({})),
    skip: vi.fn(() => Promise.resolve({})),
    volume: vi.fn(() => Promise.resolve({})),
    play: vi.fn(() => Promise.resolve({})),
    start: vi.fn(() => Promise.resolve({})),
    stop: vi.fn(() => Promise.resolve({})),
    attachSession: vi.fn(() => Promise.resolve({ sessionId: 'jukebox-session:admin' })),
    detachSession: vi.fn(() => Promise.resolve({})),
  },
}))

const dispatchMock = vi.fn()
const audioInstanceMock = {
  paused: true,
  muted: false,
  volume: 1,
  play: vi.fn(() => Promise.resolve()),
  pause: vi.fn(),
  currentTime: 0,
}
const playerStateMock = {
  audioInstance: audioInstanceMock,
  queue: [],
  savedPlayIndex: 0,
  jukeboxMode: false,
  jukeboxDevice: null,
}

vi.mock('react-redux', () => ({
  useDispatch: () => dispatchMock,
  useSelector: (selector) =>
    selector({
      player: playerStateMock,
    }),
}))

describe('<DeviceSelector />', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    audioInstanceMock.paused = true
    audioInstanceMock.muted = false
    audioInstanceMock.volume = 1
    playerStateMock.queue = []
    playerStateMock.savedPlayIndex = 0
    playerStateMock.jukeboxMode = false
    playerStateMock.jukeboxDevice = null
    localStorage.setItem('username', 'admin')
    sessionStorage.clear()

    httpClient.mockImplementation((url, options) => {
      if (url === '/api/jukebox/devices') {
        return Promise.resolve({
          json: [
            { deviceName: 'auto', name: 'Local', isDefault: true },
            {
              deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
              name: 'Bluetooth 24:C4:06:FA:00:37',
              isBluetooth: true,
              connected: true,
            },
          ],
        })
      }
      if (url === '/api/jukebox/devices/switch' && options?.method === 'POST') {
        return Promise.resolve({
          json: [
            { deviceName: 'auto', name: 'Local', isDefault: false },
            {
              deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
              name: 'Bluetooth 24:C4:06:FA:00:37',
              isBluetooth: true,
              connected: true,
              isDefault: true,
            },
          ],
        })
      }
      if (url === '/api/bluetooth/devices') {
        return Promise.resolve({ json: [] })
      }
      if (url === '/api/bluetooth/scan' && options?.method === 'POST') {
        return Promise.resolve({ json: [] })
      }
      return Promise.resolve({ json: [] })
    })
  })

  it('shows Scan for devices and triggers /api/bluetooth/scan', async () => {
    render(<DeviceSelector isDesktop buttonClass="" />)

    const openButton = await screen.findByTestId('device-selector-button')
    fireEvent.click(openButton)

    const scanAction = await screen.findByText('Scan for devices')
    fireEvent.click(scanAction)

    await waitFor(() => {
      expect(httpClient).toHaveBeenCalledWith('/api/bluetooth/scan', {
        method: 'POST',
      })
    })
  })

  it('mutes browser audio when switching to a bluetooth jukebox device', async () => {
    playerStateMock.queue = [{ trackId: 't1' }]
    audioInstanceMock.paused = false
    audioInstanceMock.volume = 0.36
    audioInstanceMock.currentTime = 42

    render(<DeviceSelector isDesktop buttonClass="" />)

    const openButton = await screen.findByTestId('device-selector-button')
    fireEvent.click(openButton)

    const bluetoothDevice = await screen.findByText(
      'Bluetooth 24:C4:06:FA:00:37',
    )
    fireEvent.click(bluetoothDevice)

    await waitFor(() => {
      expect(httpClient).toHaveBeenCalledWith('/api/jukebox/devices/switch', {
        method: 'POST',
        body: JSON.stringify({
          deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
        }),
      })
    })

    await waitFor(() => {
      expect(audioInstanceMock.muted).toBe(true)
      expect(audioInstanceMock.pause).not.toHaveBeenCalled()
      expect(jukeboxClient.set).toHaveBeenCalledWith(['t1'])
      expect(jukeboxClient.skip).toHaveBeenCalledWith(0, 42)
      expect(jukeboxClient.volume).toHaveBeenCalledWith(0.36)
      expect(jukeboxClient.play).toHaveBeenCalled()
      expect(jukeboxClient.volume.mock.invocationCallOrder[0]).toBeLessThan(
        jukeboxClient.play.mock.invocationCallOrder[0],
      )
    })
  })

  it('renders Dividers between menu sections', async () => {
    render(<DeviceSelector isDesktop buttonClass="" />)

    const openButton = await screen.findByTestId('device-selector-button')
    fireEvent.click(openButton)

    await screen.findByText('Local')

    const menu = screen.getByRole('menu')
    const dividers = menu.querySelectorAll('hr')
    expect(dividers.length).toBeGreaterThanOrEqual(1)
  })

  it('shows notification when device switch fails', async () => {
    httpClient.mockImplementation((url, options) => {
      if (url === '/api/jukebox/devices') {
        return Promise.resolve({
          json: [
            { deviceName: 'auto', name: 'Local', isDefault: true },
            {
              deviceName: 'pulse/bluez_sink',
              name: 'BT Speaker',
              isBluetooth: true,
              connected: true,
            },
          ],
        })
      }
      if (url === '/api/jukebox/devices/switch') {
        return Promise.reject(new Error('Network error'))
      }
      if (url === '/api/bluetooth/devices') {
        return Promise.resolve({ json: [] })
      }
      return Promise.resolve({ json: [] })
    })

    render(<DeviceSelector isDesktop buttonClass="" />)

    const openButton = await screen.findByTestId('device-selector-button')
    fireEvent.click(openButton)

    const btDevice = await screen.findByText('BT Speaker')
    fireEvent.click(btDevice)

    await waitFor(() => {
      expect(notifyMock).toHaveBeenCalledWith(
        'Failed to switch audio device',
        'warning',
      )
    })
  })

  it('shows notification when Bluetooth scan fails', async () => {
    httpClient.mockImplementation((url, options) => {
      if (url === '/api/jukebox/devices') {
        return Promise.resolve({
          json: [
            { deviceName: 'auto', name: 'Local', isDefault: true },
            {
              deviceName: 'pulse/bluez_sink',
              name: 'BT Speaker',
              isBluetooth: true,
              connected: true,
            },
          ],
        })
      }
      if (url === '/api/bluetooth/devices') {
        return Promise.resolve({ json: [] })
      }
      if (url === '/api/bluetooth/scan') {
        return Promise.reject(new Error('Scan failed'))
      }
      return Promise.resolve({ json: [] })
    })

    render(<DeviceSelector isDesktop buttonClass="" />)

    const openButton = await screen.findByTestId('device-selector-button')
    fireEvent.click(openButton)

    const scanItem = await screen.findByText('Scan for devices')
    fireEvent.click(scanItem)

    await waitFor(() => {
      expect(notifyMock).toHaveBeenCalledWith(
        'Bluetooth scan failed',
        'warning',
      )
    })
  })

  it('rebinds the current jukebox session when switching remote devices in jukebox mode', async () => {
    playerStateMock.queue = [{ trackId: 't1' }]
    playerStateMock.jukeboxMode = true
    audioInstanceMock.paused = false

    render(<DeviceSelector isDesktop buttonClass="" />)

    const openButton = await screen.findByTestId('device-selector-button')
    fireEvent.click(openButton)

    const bluetoothDevice = await screen.findByText('Bluetooth 24:C4:06:FA:00:37')
    fireEvent.click(bluetoothDevice)

    await waitFor(() => {
      expect(jukeboxClient.attachSession).toHaveBeenCalledWith(
        'jukebox-session:admin',
        expect.any(String),
        'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
      )
    })
  })

  it('detaches the current jukebox session when switching back to local playback', async () => {
    playerStateMock.jukeboxMode = true
    audioInstanceMock.paused = true

    httpClient.mockImplementation((url, options) => {
      if (url === '/api/jukebox/devices') {
        return Promise.resolve({
          json: [
            { deviceName: 'auto', name: 'Local', isDefault: false },
            {
              deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
              name: 'Bluetooth 24:C4:06:FA:00:37',
              isBluetooth: true,
              connected: true,
              isDefault: true,
            },
          ],
        })
      }
      if (url === '/api/jukebox/devices/switch' && options?.method === 'POST') {
        return Promise.resolve({
          json: [
            { deviceName: 'auto', name: 'Local', isDefault: true },
            {
              deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
              name: 'Bluetooth 24:C4:06:FA:00:37',
              isBluetooth: true,
              connected: true,
              isDefault: false,
            },
          ],
        })
      }
      if (url === '/api/bluetooth/devices') {
        return Promise.resolve({ json: [] })
      }
      return Promise.resolve({ json: [] })
    })

    render(<DeviceSelector isDesktop buttonClass="" />)

    const openButton = await screen.findByTestId('device-selector-button')
    fireEvent.click(openButton)

    const localDevice = await screen.findByText('Local')
    fireEvent.click(localDevice)

    await waitFor(() => {
      expect(jukeboxClient.detachSession).toHaveBeenCalledWith(
        'jukebox-session:admin',
        expect.any(String),
      )
    })
  })

})
