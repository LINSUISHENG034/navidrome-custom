export const clamp01 = (value) => Math.max(0, Math.min(1, Number(value) || 0))

export const audioVolumeToUiVolume = (audioVolume) =>
  Math.sqrt(clamp01(audioVolume))

export const uiVolumeToAudioVolume = (uiVolume) =>
  Math.pow(clamp01(uiVolume), 2)
