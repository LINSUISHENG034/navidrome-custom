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

/**
 * Compute the diff between old and new queue.
 * Returns { added, removed, fullReplace, newTrackIds }.
 * - added: [{ trackId, index }] — tracks present in new but not old
 * - removed: [index] — indices in old queue that are missing from new (sorted ascending)
 * - fullReplace: true if the diff is too complex for incremental ops
 */
export const computeQueueDiff = (oldQueue = [], newQueue = []) => {
  const oldIds = toTrackIds(oldQueue)
  const newIds = toTrackIds(newQueue)

  // Fast path: identical
  if (
    oldIds.length === newIds.length &&
    oldIds.every((id, i) => id === newIds[i])
  ) {
    return { added: [], removed: [], fullReplace: false }
  }

  const oldSet = new Set(oldIds)
  const newSet = new Set(newIds)

  const removed = []
  for (let i = 0; i < oldIds.length; i++) {
    if (!newSet.has(oldIds[i])) {
      removed.push(i)
    }
  }

  const added = []
  for (let i = 0; i < newIds.length; i++) {
    if (!oldSet.has(newIds[i])) {
      added.push({ trackId: newIds[i], index: i })
    }
  }

  // If there are only adds or only removes, use incremental.
  // If both or if the remaining items changed order, fall back to full replace.
  const remainingOld = oldIds.filter((id) => newSet.has(id))
  const remainingNew = newIds.filter((id) => oldSet.has(id))
  const orderChanged =
    remainingOld.length !== remainingNew.length ||
    remainingOld.some((id, i) => id !== remainingNew[i])

  if (orderChanged || (added.length > 0 && removed.length > 0)) {
    return { added: [], removed: [], fullReplace: true, newTrackIds: newIds }
  }

  return { added, removed, fullReplace: false }
}

/**
 * Send incremental queue operations to the server.
 * Only falls back to set() when the diff is a full replacement.
 */
export const syncJukeboxQueueIncremental = async (client, diff) => {
  if (!client) return

  if (diff.fullReplace) {
    if (client.set && diff.newTrackIds?.length > 0) {
      await client.set(diff.newTrackIds)
    }
    return
  }

  // Remove in reverse order to keep indices valid
  if (client.remove) {
    for (const idx of [...diff.removed].reverse()) {
      await client.remove(idx)
    }
  }

  // Add new tracks
  if (client.add && diff.added.length > 0) {
    const ids = diff.added.map((item) => item.trackId)
    await client.add(ids)
  }
}

/**
 * Sync track change: just skip to the right index (queue is already synced incrementally).
 */
export const syncJukeboxTrackChange = async (
  client,
  { audioLists = [], playId, audioInfo = {} } = {},
) => {
  if (!client?.skip || audioLists.length === 0) return

  const index = resolvePlayIndex({
    audioLists,
    playId,
    trackId: audioInfo?.trackId,
  })
  const offset = toSeconds(audioInfo?.currentTime)

  await client.skip(index, offset)
}

export const syncJukeboxSeek = async (client, audioInfo = {}) => {
  if (!client?.seek) return
  await client.seek(toSeconds(audioInfo?.currentTime))
}

