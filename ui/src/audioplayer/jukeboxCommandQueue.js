let _chain = Promise.resolve()

/**
 * Enqueue a Jukebox API call so that commands execute serially.
 * Each `fn` is a function returning a Promise; it will not start
 * until all previously enqueued commands have settled.
 */
export const enqueueJukeboxCommand = (fn) => {
  _chain = _chain.then(fn, fn)
  return _chain
}
