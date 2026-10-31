import {
  audioVolumeToUiVolume,
  clamp01,
  uiVolumeToAudioVolume,
} from './volumeMapping'

export const DEFAULT_VOLUME_PROFILE = 'default'
export const BLUETOOTH_JUKEBOX_VOLUME_PROFILE = 'bluetooth-jukebox'

export const KNOWN_VOLUME_PROFILES = Object.freeze([
  DEFAULT_VOLUME_PROFILE,
  BLUETOOTH_JUKEBOX_VOLUME_PROFILE,
])

export const HARDWARE_QUANTIZATION_TOLERANCE = 0.01

export const isKnownVolumeProfile = (profile) =>
  typeof profile === 'string' && KNOWN_VOLUME_PROFILES.includes(profile)

const BLUETOOTH_PROFILE_EXPONENT = 0.15
const BLUETOOTH_MARKERS = ['bluetooth', 'bluez', 'a2dp']

const normalizeDeviceHint = (value) =>
  typeof value === 'string' ? value.toLowerCase() : ''

const hasBluetoothMarker = (value) =>
  BLUETOOTH_MARKERS.some((marker) => value.includes(marker))

const resolveDeviceHint = (context = {}) =>
  normalizeDeviceHint(
    context.deviceName ||
      context.jukeboxRemote?.deviceName ||
      context.jukeboxDevice ||
      '',
  )

export const resolveVolumeProfile = (context = {}, explicitProfile = null) => {
  const profileToResolve =
    explicitProfile !== null && explicitProfile !== undefined
      ? explicitProfile
      : context.volumeProfile

  if (profileToResolve !== null && profileToResolve !== undefined) {
    if (typeof profileToResolve === 'string') {
      if (isKnownVolumeProfile(profileToResolve)) {
        return profileToResolve
      }
      const err = new Error(`Unknown volume profile: ${profileToResolve}`)
      err.code = 'VOLUME_PROFILE_UNKNOWN'
      throw err
    }
    const err = new Error(`Unknown volume profile: ${profileToResolve}`)
    err.code = 'VOLUME_PROFILE_UNKNOWN'
    throw err
  }

  if (
    context.isBluetooth === true ||
    hasBluetoothMarker(resolveDeviceHint(context))
  ) {
    return BLUETOOTH_JUKEBOX_VOLUME_PROFILE
  }

  return DEFAULT_VOLUME_PROFILE
}

export const audioVolumeToRemoteGain = (
  audioVolume,
  context = {},
  explicitProfile = null,
) => {
  const profile = resolveVolumeProfile(context, explicitProfile)
  if (profile === BLUETOOTH_JUKEBOX_VOLUME_PROFILE) {
    return Math.pow(clamp01(audioVolume), BLUETOOTH_PROFILE_EXPONENT)
  }
  return clamp01(audioVolume)
}

export const remoteGainToAudioVolume = (
  gain,
  context = {},
  explicitProfile = null,
) => {
  const profile = resolveVolumeProfile(context, explicitProfile)
  if (profile === BLUETOOTH_JUKEBOX_VOLUME_PROFILE) {
    return Math.pow(clamp01(gain), 1 / BLUETOOTH_PROFILE_EXPONENT)
  }
  return clamp01(gain)
}

export const uiVolumeToRemoteGain = (
  uiVolume,
  context = {},
  explicitProfile = null,
) =>
  audioVolumeToRemoteGain(
    uiVolumeToAudioVolume(uiVolume),
    context,
    explicitProfile,
  )

export const remoteGainToUiVolume = (
  gain,
  context = {},
  explicitProfile = null,
) =>
  audioVolumeToUiVolume(remoteGainToAudioVolume(gain, context, explicitProfile))

export const stepRemoteGainByUiDelta = (gain, delta, context = {}) =>
  uiVolumeToRemoteGain(remoteGainToUiVolume(gain, context) + delta, context)

export const resolveAuthoritativeGain = (
  reportedGain,
  lastConfirmedV,
  lastConfirmedG,
  context = {},
  explicitProfile = null,
) => {
  const g = clamp01(reportedGain)
  if (
    typeof lastConfirmedG === 'number' &&
    Math.abs(g - lastConfirmedG) <= HARDWARE_QUANTIZATION_TOLERANCE &&
    typeof lastConfirmedV === 'number'
  ) {
    return {
      volume: lastConfirmedV,
      gain: g,
      remapped: false,
    }
  }

  const newV = remoteGainToAudioVolume(g, context, explicitProfile)
  return {
    volume: newV,
    gain: g,
    remapped: true,
  }
}
