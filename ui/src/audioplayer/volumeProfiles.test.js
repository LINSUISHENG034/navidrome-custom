import { describe, expect, it } from 'vitest'
import {
  getVolumeProfile,
  remoteGainToUiVolume,
  uiVolumeToRemoteGain,
} from './volumeProfiles'

describe('volumeProfiles', () => {
  it('keeps the upstream default mapping for non-Bluetooth devices', () => {
    expect(getVolumeProfile({ deviceName: 'pulse/alsa_output.pci-0000_00_1f.3' })).toBe(
      'default',
    )
    expect(uiVolumeToRemoteGain(0.6, { deviceName: 'pulse/alsa_output.pci-0000_00_1f.3' })).toBeCloseTo(
      0.36,
    )
    expect(
      remoteGainToUiVolume(0.36, {
        deviceName: 'pulse/alsa_output.pci-0000_00_1f.3',
      }),
    ).toBeCloseTo(0.6)
  })

  it('uses the Bluetooth profile for bluez and a2dp device markers', () => {
    expect(getVolumeProfile({ deviceName: 'pulse/bluez_output.24_C4.a2dp-sink' })).toBe(
      'bluetooth-jukebox',
    )
    expect(uiVolumeToRemoteGain(0.6, { deviceName: 'pulse/bluez_output.24_C4.a2dp-sink' })).toBeCloseTo(
      0.857917,
      5,
    )
    expect(
      remoteGainToUiVolume(0.857917, {
        deviceName: 'pulse/bluez_output.24_C4.a2dp-sink',
      }),
    ).toBeCloseTo(0.6, 5)
  })

  it('falls back to the jukebox device label when remote deviceName is unavailable', () => {
    expect(getVolumeProfile({ jukeboxDevice: 'Bluetooth Living Room Speaker' })).toBe(
      'bluetooth-jukebox',
    )
    expect(uiVolumeToRemoteGain(0.6, { jukeboxDevice: 'Bluetooth Living Room Speaker' })).toBeCloseTo(
      0.857917,
      5,
    )
  })
})
