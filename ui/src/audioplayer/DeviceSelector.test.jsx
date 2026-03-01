import React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import DeviceSelector from './DeviceSelector'
import httpClient from '../dataProvider/httpClient'
import jukeboxClient from './jukeboxClient'

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
    play: vi.fn(() => Promise.resolve({})),
    start: vi.fn(() => Promise.resolve({})),
    stop: vi.fn(() => Promise.resolve({})),
  },
}))

const dispatchMock = vi.fn()
const audioInstanceMock = {
  paused: true,
  muted: false,
  play: vi.fn(() => Promise.resolve()),
  pause: vi.fn(),
  currentTime: 0,
}
const playerStateMock = {
  audioInstance: audioInstanceMock,
  queue: [],
  savedPlayIndex: 0,
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
    playerStateMock.queue = []
    playerStateMock.savedPlayIndex = 0

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
      if (
        url === '/api/jukebox/devices/switch' &&
        options?.method === 'POST'
      ) {
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
    })
  })
})
