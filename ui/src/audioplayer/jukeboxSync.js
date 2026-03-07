const toTrackIds = (audioLists = []) =>
  audioLists.map((item) => item?.trackId).filter(Boolean)

const toOccurrenceKeys = (trackIds = []) => {
  const seen = new Map()
  return trackIds.map((trackId) => {
    const count = seen.get(trackId) || 0
    seen.set(trackId, count + 1)
    return `${trackId}#${count}`
  })
}

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

const groupContiguousAdds = (added = []) => {
  if (added.length === 0) return []

  const groups = [{ ids: [added[0].trackId], index: added[0].index }]
  for (let i = 1; i < added.length; i++) {
    const current = added[i]
    const prev = groups[groups.length - 1]
    if (current.index === prev.index + prev.ids.length) {
      prev.ids.push(current.trackId)
      continue
    }
    groups.push({ ids: [current.trackId], index: current.index })
  }
  return groups
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

  if (
    oldIds.length === newIds.length &&
    oldIds.every((id, i) => id === newIds[i])
  ) {
    return { added: [], removed: [], fullReplace: false }
  }

  const oldKeys = toOccurrenceKeys(oldIds)
  const newKeys = toOccurrenceKeys(newIds)
  const oldSet = new Set(oldKeys)
  const newSet = new Set(newKeys)

  const removed = []
  for (let i = 0; i < oldKeys.length; i++) {
    if (!newSet.has(oldKeys[i])) {
      removed.push(i)
    }
  }

  const added = []
  for (let i = 0; i < newKeys.length; i++) {
    if (!oldSet.has(newKeys[i])) {
      added.push({ trackId: newIds[i], index: i })
    }
  }

  const remainingOld = oldKeys.filter((id) => newSet.has(id))
  const remainingNew = newKeys.filter((id) => oldSet.has(id))
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
    if (client.set) {
      await client.set(diff.newTrackIds || [])
    }
    return
  }

  if (client.remove) {
    for (const idx of [...diff.removed].reverse()) {
      await client.remove(idx)
    }
  }

  if (client.add && diff.added.length > 0) {
    for (const group of groupContiguousAdds(diff.added)) {
      await client.add(group.ids, group.index)
    }
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

  await client.skip(index, 0)
}

export const syncJukeboxTrackChangeAfterQueueSync = async (
  pendingQueueSync,
  client,
  payload,
) => {
  await pendingQueueSync
  return syncJukeboxTrackChange(client, payload)
}

export const syncJukeboxSeek = async (client, audioInfo = {}) => {
  if (!client?.seek) return
  await client.seek(toSeconds(audioInfo?.currentTime))
}
