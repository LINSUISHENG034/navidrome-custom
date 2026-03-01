import React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import DeviceSelector from './DeviceSelector'
import httpClient from '../dataProvider/httpClient'

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
    start: vi.fn(() => Promise.resolve({})),
    stop: vi.fn(() => Promise.resolve({})),
  },
}))

const dispatchMock = vi.fn()

vi.mock('react-redux', () => ({
  useDispatch: () => dispatchMock,
  useSelector: (selector) =>
    selector({
      player: {
        audioInstance: {
          paused: true,
          play: vi.fn(() => Promise.resolve()),
          pause: vi.fn(),
          currentTime: 0,
        },
        queue: [],
        savedPlayIndex: 0,
      },
    }),
}))

describe('<DeviceSelector />', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    httpClient.mockImplementation((url, options) => {
      if (url === '/api/jukebox/devices') {
        return Promise.resolve({
          json: [
            { deviceName: 'auto', name: 'Local', isDefault: true },
            {
              deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
              name: 'Bluetooth 24:C4:06:FA:00:37',
              isBluetooth: true,
              connected: false,
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
})
