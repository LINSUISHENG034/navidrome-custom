import {
  audioVolumeToUiVolume,
  clamp01,
  uiVolumeToAudioVolume,
} from './volumeMapping'

export const DEFAULT_VOLUME_PROFILE = 'default'
export const BLUETOOTH_JUKEBOX_VOLUME_PROFILE = 'bluetooth-jukebox'

const BLUETOOTH_PROFILE_EXPONENT = 0.3
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

export const getVolumeProfile = (context = {}) =>
  hasBluetoothMarker(resolveDeviceHint(context))
    ? BLUETOOTH_JUKEBOX_VOLUME_PROFILE
    : DEFAULT_VOLUME_PROFILE

export const uiVolumeToRemoteGain = (uiVolume, context = {}) => {
  const profile = getVolumeProfile(context)
  if (profile === BLUETOOTH_JUKEBOX_VOLUME_PROFILE) {
    return Math.pow(clamp01(uiVolume), BLUETOOTH_PROFILE_EXPONENT)
  }
  return uiVolumeToAudioVolume(uiVolume)
}

export const remoteGainToUiVolume = (gain, context = {}) => {
  const profile = getVolumeProfile(context)
  if (profile === BLUETOOTH_JUKEBOX_VOLUME_PROFILE) {
    return Math.pow(clamp01(gain), 1 / BLUETOOTH_PROFILE_EXPONENT)
  }
  return audioVolumeToUiVolume(gain)
}

export const audioVolumeToRemoteGain = (audioVolume, context = {}) => {
  if (getVolumeProfile(context) === DEFAULT_VOLUME_PROFILE) {
    return clamp01(audioVolume)
  }
  return uiVolumeToRemoteGain(audioVolumeToUiVolume(audioVolume), context)
}

export const stepRemoteGainByUiDelta = (gain, delta, context = {}) => {
  if (getVolumeProfile(context) === DEFAULT_VOLUME_PROFILE) {
    return clamp01(gain + delta)
  }
  return uiVolumeToRemoteGain(remoteGainToUiVolume(gain, context) + delta, context)
}
