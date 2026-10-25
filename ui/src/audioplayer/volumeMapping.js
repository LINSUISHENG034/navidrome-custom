export const clamp01 = (value) => {
  const num = Number(value)
  if (Number.isNaN(num)) return 0
  return Math.max(0, Math.min(1, num))
}

export const audioVolumeToUiVolume = (audioVolume) =>
  Math.sqrt(clamp01(audioVolume))

export const uiVolumeToAudioVolume = (uiVolume) =>
  Math.pow(clamp01(uiVolume), 2)

export const stepUiVolume = (audioVolume, delta) => {
  const currentUi = audioVolumeToUiVolume(audioVolume)
  const nextUi = clamp01(currentUi + delta)
  return uiVolumeToAudioVolume(nextUi)
}
