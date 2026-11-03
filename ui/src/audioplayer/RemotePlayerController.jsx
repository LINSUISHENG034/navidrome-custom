import React, { useEffect, useRef, useState } from 'react'
import { useDispatch, useSelector, useStore } from 'react-redux'
import { makeStyles } from '@material-ui/core/styles'
import RemotePlayer from './RemotePlayer'
import DeviceSelector from './DeviceSelector'
import jukeboxClient from './jukeboxClient'
import {
  beginJukeboxEpisode,
  disableJukeboxEpisode,
  endJukeboxEpisode,
  updateJukeboxSessionStatus,
} from '../actions'
import {
  buildAtomicSnapshotMeta,
  captureEligibleJukeboxScope,
  createJukeboxSessionEpisode,
  createJukeboxEpisodeToken,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  isOlderOrdering,
  readOrderedFloor,
} from './jukeboxSession'

const commandHandlers = {
  play: (_, scope) => jukeboxClient.play(scope),
  pause: (_, scope) => jukeboxClient.pause(scope),
  skip: ({ index, offset = 0 }, scope) =>
    jukeboxClient.skip(index, offset, scope),
  seek: ({ position }, scope) => jukeboxClient.seek(position, scope),
  volume: ({ gain }, scope) => jukeboxClient.volume(gain, scope),
}

// Heartbeat/403 syntheses a local control-only status so the reducer-owned
// authority downgrades permissions; provenance stays local (no meta).
const synthesizeTakenOverStatus = ({ sessionId, clientId }) => ({
  sessionId,
  ownerClientId: clientId,
  ownershipState: 'taken_over',
  terminationReason: 'taken_over',
})

const useStyles = makeStyles(() => ({
  deviceSelectorContainer: {
    display: 'flex',
    justifyContent: 'flex-end',
    padding: '4px',
  },
}))

// Snapshot-driven controller; default-route integration is handled separately.
// The reducer is the single admission authority: this component displays only
// the admitted status+playlist pair from Redux and never keeps a private
// snapshot copy or a second admission gate. Control admission is the shared
// caller-scoped capture predicate, identical for rendering and commands.
const RemotePlayerController = ({
  sessionId = getJukeboxSessionId(),
  clientId = getOrCreateJukeboxClientId(),
  deviceName = null,
}) => {
  const classes = useStyles()
  const dispatch = useDispatch()
  const store = useStore()
  const [error, setError] = useState(null)
  const [takingControl, setTakingControl] = useState(false)
  const retirement = useRef(Promise.resolve())
  const current = useRef(null)
  const playerState = useSelector((state) => state.player)
  const remoteEvent = playerState?.jukeboxRemote
  const controlEvent = playerState?.jukeboxControl
  const playlist = playerState?.jukeboxPlaylist
  const conflictEvent = controlEvent?.sceneConflicted
  const reconnectEvent = useSelector(
    (state) => state.activity?.streamReconnected,
  )
  const scope = { sessionId, clientId }
  const controlAvailable =
    captureEligibleJukeboxScope(playerState, {
      ...scope,
      token: current.current?.token,
    }) !== null

  useEffect(() => {
    let active = true
    let requestVersion = 0
    const effectScope = { sessionId, clientId }
    const token = createJukeboxEpisodeToken()
    // Episode lifecycle is reducer-owned: register synchronously at mount so
    // every fetch this episode starts can name a live token.
    dispatch(beginJukeboxEpisode({ token, sessionId, clientId, deviceName }))
    // The GET-time provenance: the episode's current revision plus the
    // binding the shared authority had admitted when the GET STARTED. The
    // response is validated against this, not against post-hoc knowledge.
    const captureRequestBinding = () => {
      const player = store.getState()?.player
      const registration = player?.jukeboxEpisodes?.[token]
      return {
        revision: registration?.revision ?? 0,
        requestBinding: {
          bindingGeneration: player?.jukeboxRemote?.bindingGeneration ?? null,
          deviceName: player?.jukeboxRemote?.deviceName ?? null,
        },
      }
    }
    const record = {
      token,
      observing: false,
      // Bounded-reconciliation guard: one follow-up GET per distinct
      // reconcile target armed by the authority, so repeated invalid,
      // missing-device or contradictory answers stay readonly instead of
      // storming the snapshot endpoint.
      reconciledTargets: new Set(),
      command: async (command) => {
        // Command admission re-runs the SAME caller-scoped predicate against
        // the CURRENT Redux authority at invocation time.
        if (
          !active ||
          captureEligibleJukeboxScope(store.getState()?.player, {
            ...effectScope,
            token,
          }) === null
        )
          return
        try {
          const handler = commandHandlers[command.type]
          if (!handler) throw new Error('Unsupported remote command')
          await handler(command, effectScope)
          if (active) await record.refresh()
        } catch (err) {
          if (!active) return
          setError(err.message)
          if (err.status === 403) {
            // The server fenced this episode: invalidate only its own
            // reconciliation (bumping its revision) so an older in-flight
            // response can never regrant, and let the authority downgrade.
            dispatch(disableJukeboxEpisode({ token }))
            dispatch(
              updateJukeboxSessionStatus(
                synthesizeTakenOverStatus(effectScope),
              ),
            )
          }
        }
      },
      refresh: async () => {
        const version = ++requestVersion
        const { revision, requestBinding } = captureRequestBinding()
        try {
          const next = await jukeboxClient.sessionSnapshot(sessionId)
          if (!active || version !== requestVersion) return
          if (next?.sessionStatus) {
            // The atomic HTTP boundary is trusted for ordered snapshots;
            // unordered (older-server) snapshots stay plain observations and
            // the shared authority decides admission or retention.
            dispatch(
              updateJukeboxSessionStatus(
                next,
                readOrderedFloor(next.sessionStatus)
                  ? buildAtomicSnapshotMeta({
                      sessionId,
                      clientId,
                      deviceName,
                      token,
                      revision,
                      requestBinding,
                    })
                  : undefined,
              ),
            )
          }
        } catch (err) {
          if (!active || version !== requestVersion) return
          setError(err.message)
          if (err.status === 403) {
            dispatch(disableJukeboxEpisode({ token }))
            dispatch(
              updateJukeboxSessionStatus(
                synthesizeTakenOverStatus(effectScope),
              ),
            )
          }
        }
      },
    }
    current.current = record
    setError(null)
    setTakingControl(false)
    const episode = createJukeboxSessionEpisode({
      client: jukeboxClient,
      sessionId,
      clientId,
      dispatch,
      getDeviceName: () => deviceName,
      previousRetirement: retirement.current,
      onHeartbeat: async (status) => {
        if (!active) return
        const owns =
          !!status &&
          status.ownershipState === 'attached' &&
          status.ownerClientId === clientId
        // A heartbeat that reports loss of control downgrades the authority;
        // reacquiring immediately would let a stale equal-ordering snapshot
        // re-promote permissions the server just fenced.
        if (owns) await record.refresh()
      },
    })
    const attachment = episode.attach({ deviceName, dispatch: null })
    const acceptAttachment = (status) => {
      if (!active || !status) return
      record.observing = true
      return record.refresh()
    }
    const rejectAttachment = (err) => {
      if (!active) return
      setError(err.message)
      if (err.status === 403) {
        record.observing = true
        return record.refresh()
      }
    }
    attachment.then(acceptAttachment).catch(rejectAttachment)
    record.takeControl = () => {
      if (!active || !record.observing || record.takeoverPending) return
      record.takeoverPending = true
      setTakingControl(true)
      episode
        .attach({ deviceName, takeover: true, dispatch: null })
        .then(acceptAttachment)
        .catch(rejectAttachment)
        .finally(() => {
          record.takeoverPending = false
          if (active) setTakingControl(false)
        })
    }
    const endEpisode = () => {
      // Retire only the matching token, synchronously before async
      // attachment retirement, so late responses are rejected whole.
      dispatch(endJukeboxEpisode({ token }))
    }
    const handlePageHide = (event) => {
      if (event.persisted) return
      active = false
      endEpisode()
      retirement.current = episode.retire({ keepalive: true })
    }
    window.addEventListener('pagehide', handlePageHide)
    return () => {
      active = false
      window.removeEventListener('pagehide', handlePageHide)
      if (current.current === record) current.current = null
      endEpisode()
      retirement.current = episode.retire()
    }
  }, [dispatch, store, sessionId, clientId, deviceName])

  useEffect(() => {
    const record = current.current
    if (!record?.observing) return
    // A newer partial observation raised the floor beyond the accepted scene:
    // reacquire the atomic snapshot so the authority can admit the pair.
    // Only the observed floor (jukeboxRemote) triggers this; control-only
    // changes such as a 403 downgrade must not start a refresh loop.
    const player = store.getState()?.player
    const floor = readOrderedFloor(player?.jukeboxRemote)
    const anchor = readOrderedFloor({
      bindingGeneration: player?.jukeboxControl?.sceneBindingGeneration,
      stateSequence: player?.jukeboxControl?.sceneStateSequence,
    })
    if (floor && anchor && isOlderOrdering(anchor, floor)) record.refresh()
  }, [remoteEvent, store])

  useEffect(() => {
    const record = current.current
    if (!record?.observing) return
    // A stream reconnect always reacquires the atomic snapshot.
    record.refresh()
  }, [reconnectEvent])

  useEffect(() => {
    const record = current.current
    if (!record?.observing || !conflictEvent) return
    // Equal-sequence semantic conflict: the authority retained the accepted
    // scene; reacquire once. Repeated conflicts coalesce because the shared
    // conflict marker only transitions again after reconciliation.
    record.refresh()
  }, [conflictEvent])

  const reconcileEvent = controlEvent?.bindingReconcile
  useEffect(() => {
    const record = current.current
    if (!record?.observing) return
    // Trusted A provenance observed a newer binding it cannot confer writes
    // on: the authority armed exactly one bounded reconciliation. Issue a
    // single follow-up GET per distinct target — record.refresh captures the
    // revision and binding at ITS own initiation, so disable/end/replacement
    // while awaiting invalidates its response whole. No polling loop, no
    // compensating POST, no auto takeover.
    const target = store.getState()?.player?.jukeboxControl?.bindingReconcile
    if (!target) return
    const key = `${target.bindingGeneration}:${target.deviceName ?? ''}`
    if (record.reconciledTargets.has(key)) return
    record.reconciledTargets.add(key)
    record.refresh()
  }, [reconcileEvent, store])

  const hasPair = playlist != null
  const displayedStatus = {
    sessionId: controlEvent?.sessionId,
    ownerClientId: controlEvent?.ownerClientId,
    ownershipState: controlEvent?.ownershipState,
    lastHeartbeat: controlEvent?.lastHeartbeat,
    ...(remoteEvent || {}),
  }

  return (
    <section aria-label="Remote controller">
      <div className={classes.deviceSelectorContainer}>
        <DeviceSelector isDesktop={true} />
      </div>
      {error && <p role="alert">{error}</p>}
      {hasPair && !controlAvailable && (
        <button
          type="button"
          disabled={takingControl}
          onClick={() => current.current?.takeControl()}
        >
          Take control
        </button>
      )}
      {!hasPair ? (
        <p>Loading remote state</p>
      ) : (
        <fieldset disabled={!controlAvailable}>
          <RemotePlayer
            snapshot={{
              sessionStatus: displayedStatus,
              jukeboxPlaylist: playlist,
            }}
            onCommand={(command) => current.current?.command(command)}
          />
        </fieldset>
      )}
    </section>
  )
}

export default RemotePlayerController
