import { describe, expect, it } from 'vitest'
import {
  audioVolumeToRemoteGain,
  DEFAULT_VOLUME_PROFILE,
  BLUETOOTH_JUKEBOX_VOLUME_PROFILE,
  getVolumeProfile,
  HARDWARE_QUANTIZATION_TOLERANCE,
  remoteGainToAudioVolume,
  remoteGainToUiVolume,
  resolveAuthoritativeGain,
  resolveVolumeProfile,
  uiVolumeToRemoteGain,
} from './volumeProfiles'
import {
  audioVolumeToUiVolume,
  stepUiVolume,
  uiVolumeToAudioVolume,
} from './volumeMapping'

describe('volumeProfiles', () => {
  it('keeps the upstream default mapping for non-Bluetooth devices', () => {
    expect(
      getVolumeProfile({ deviceName: 'pulse/alsa_output.pci-0000_00_1f.3' }),
    ).toBe('default')
    expect(
      uiVolumeToRemoteGain(0.6, {
        deviceName: 'pulse/alsa_output.pci-0000_00_1f.3',
      }),
    ).toBeCloseTo(0.36)
    expect(
      remoteGainToUiVolume(0.36, {
        deviceName: 'pulse/alsa_output.pci-0000_00_1f.3',
      }),
    ).toBeCloseTo(0.6)
  })

  it('uses the Bluetooth profile for bluez and a2dp device markers', () => {
    expect(
      getVolumeProfile({ deviceName: 'pulse/bluez_output.24_C4.a2dp-sink' }),
    ).toBe('bluetooth-jukebox')
    expect(
      uiVolumeToRemoteGain(0.6, {
        deviceName: 'pulse/bluez_output.24_C4.a2dp-sink',
      }),
    ).toBeCloseTo(0.857917, 5)
    expect(
      remoteGainToUiVolume(0.857917, {
        deviceName: 'pulse/bluez_output.24_C4.a2dp-sink',
      }),
    ).toBeCloseTo(0.6, 5)
  })

  it('falls back to the jukebox device label when remote deviceName is unavailable', () => {
    expect(
      getVolumeProfile({ jukeboxDevice: 'Bluetooth Living Room Speaker' }),
    ).toBe('bluetooth-jukebox')
    expect(
      uiVolumeToRemoteGain(0.6, {
        jukeboxDevice: 'Bluetooth Living Room Speaker',
      }),
    ).toBeCloseTo(0.857917, 5)
  })

  it('maps canonical V to Bluetooth gain G = V^0.15 and inverts with zero roundtrip drift', () => {
    const context = { deviceName: 'pulse/bluez_output.24_C4.a2dp-sink' }
    const canonicalV = 0.64
    const uiU = audioVolumeToUiVolume(canonicalV)
    expect(uiU).toBeCloseTo(0.8)

    const expectedGain = Math.pow(0.64, 0.15) // ~0.935235
    expect(audioVolumeToRemoteGain(canonicalV, context)).toBeCloseTo(
      expectedGain,
      6,
    )

    const invertedV = remoteGainToAudioVolume(expectedGain, context)
    expect(invertedV).toBeCloseTo(canonicalV, 10)
    expect(audioVolumeToUiVolume(invertedV)).toBeCloseTo(uiU, 10)
  })

  it('applies keyboard steps in the U domain', () => {
    const canonicalV = 0.64 // U = 0.8
    const stepUp = stepUiVolume(canonicalV, 0.1) // U = 0.9 -> V = 0.81
    expect(stepUp).toBeCloseTo(0.81, 10)
    expect(audioVolumeToUiVolume(stepUp)).toBeCloseTo(0.9, 10)

    const stepDown = stepUiVolume(canonicalV, -0.1) // U = 0.7 -> V = 0.49
    expect(stepDown).toBeCloseTo(0.49, 10)
    expect(audioVolumeToUiVolume(stepDown)).toBeCloseTo(0.7, 10)

    // Clamping in U domain
    expect(stepUiVolume(0.81, 0.3)).toBeCloseTo(1.0, 10)
    expect(stepUiVolume(0.04, -0.5)).toBeCloseTo(0.0, 10)
  })

  it('handles boundary inputs V = 0, V approaching zero, V = 1 without discontinuity', () => {
    const context = { deviceName: 'pulse/bluez_output.24_C4.a2dp-sink' }
    // V = 0
    expect(audioVolumeToRemoteGain(0, context)).toBe(0)
    expect(remoteGainToAudioVolume(0, context)).toBe(0)

    // Approaching zero
    const tiny = 1e-6
    const tinyG = audioVolumeToRemoteGain(tiny, context)
    expect(tinyG).toBeGreaterThan(0)
    expect(remoteGainToAudioVolume(tinyG, context)).toBeCloseTo(tiny, 10)

    // V = 1
    expect(audioVolumeToRemoteGain(1, context)).toBe(1)
    expect(remoteGainToAudioVolume(1, context)).toBe(1)
  })

  it('explicit volumeProfile takes precedence over device hints', () => {
    // Explicit 'default' overrides bluez device marker and isBluetooth
    const explicitDefault = {
      volumeProfile: DEFAULT_VOLUME_PROFILE,
      isBluetooth: true,
      deviceName: 'pulse/bluez_output.24_C4.a2dp-sink',
    }
    expect(getVolumeProfile(explicitDefault)).toBe(DEFAULT_VOLUME_PROFILE)
    expect(audioVolumeToRemoteGain(0.64, explicitDefault)).toBeCloseTo(0.64)

    // Explicit 'bluetooth-jukebox' overrides ordinary device marker and isBluetooth: false
    const explicitBluetooth = {
      volumeProfile: BLUETOOTH_JUKEBOX_VOLUME_PROFILE,
      isBluetooth: false,
      deviceName: 'pulse/alsa_output.pci',
    }
    expect(getVolumeProfile(explicitBluetooth)).toBe(
      BLUETOOTH_JUKEBOX_VOLUME_PROFILE,
    )
    expect(audioVolumeToRemoteGain(0.64, explicitBluetooth)).toBeCloseTo(
      Math.pow(0.64, 0.15),
      6,
    )
  })

  it('throws VOLUME_PROFILE_UNKNOWN on unknown profile selection', () => {
    expect(() =>
      resolveVolumeProfile({ volumeProfile: 'custom-unsupported-curve' }),
    ).toThrow(expect.objectContaining({ code: 'VOLUME_PROFILE_UNKNOWN' }))
  })

  it('reuses confirmed V within quantization tolerance and inverts external changes once', () => {
    const context = { deviceName: 'pulse/bluez_output.24_C4.a2dp-sink' }
    const confirmedV = 0.64
    const confirmedG = Math.pow(0.64, 0.15) // ~0.935235

    expect(HARDWARE_QUANTIZATION_TOLERANCE).toBe(0.01)

    // Small rounding error / quantization within 0.01 (e.g. 0.94)
    const feedback1 = resolveAuthoritativeGain(
      0.94,
      confirmedV,
      confirmedG,
      context,
    )
    expect(feedback1.remapped).toBe(false)
    expect(feedback1.volume).toBe(confirmedV)
    expect(feedback1.gain).toBe(0.94)

    // External change outside 0.01 (e.g. 0.5)
    const feedback2 = resolveAuthoritativeGain(
      0.5,
      confirmedV,
      confirmedG,
      context,
    )
    expect(feedback2.remapped).toBe(true)
    const expectedNewV = Math.pow(0.5, 1 / 0.15)
    expect(feedback2.volume).toBeCloseTo(expectedNewV, 8)
    expect(feedback2.gain).toBe(0.5)

    // Subsequent identical feedback at 0.5 reuses the newly confirmed V
    const feedback3 = resolveAuthoritativeGain(
      0.5,
      feedback2.volume,
      0.5,
      context,
    )
    expect(feedback3.remapped).toBe(false)
    expect(feedback3.volume).toBe(feedback2.volume)
  })
})
