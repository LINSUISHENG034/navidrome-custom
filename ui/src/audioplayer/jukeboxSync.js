const toTrackIds = (audioLists = []) =>
  audioLists.map((item) => item?.trackId).filter(Boolean)

const toSeconds = (value) => Math.max(0, Math.floor(Number(value) || 0))

const resolvePlayIndex = ({ audioLists = [], playId, trackId }) => {
  if (audioLists.length === 0) return 0

  if (playId) {
    const byPlayId = audioLists.findIndex((item) => item?.uuid === playId)
    if (byPlayId >= 0) return byPlayId
  }

  if (trackId) {
    const byTrackId = audioLists.findIndex((item) => item?.trackId === trackId)
    if (byTrackId >= 0) return byTrackId
  }

  return 0
}

export const syncJukeboxQueue = async (client, audioLists = []) => {
  const trackIds = toTrackIds(audioLists)
  if (!client?.set || trackIds.length === 0) return
  await client.set(trackIds)
}

export const syncJukeboxTrackChange = async (
  client,
  { audioLists = [], playId, audioInfo = {} } = {},
) => {
  const trackIds = toTrackIds(audioLists)
  if (!client?.set || !client?.skip || trackIds.length === 0) return

  const index = resolvePlayIndex({
    audioLists,
    playId,
    trackId: audioInfo?.trackId,
  })
  const offset = toSeconds(audioInfo?.currentTime)

  await client.set(trackIds)
  await client.skip(index, offset)
}

export const syncJukeboxSeek = async (client, audioInfo = {}) => {
  if (!client?.seek) return
  await client.seek(toSeconds(audioInfo?.currentTime))
}

export const enforceBrowserAudioMode = (audioInstance, jukeboxMode) => {
  if (!audioInstance) return
  audioInstance.muted = Boolean(jukeboxMode)
}

