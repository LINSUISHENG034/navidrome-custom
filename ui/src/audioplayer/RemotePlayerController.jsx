import React, { useEffect, useRef, useState } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import RemotePlayer from './RemotePlayer'
import jukeboxClient from './jukeboxClient'
import {
  canControlJukebox,
  createJukeboxSessionEpisode,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
} from './jukeboxSession'

const snapshotAllowsControl = (snapshot, clientId) =>
  canControlJukebox({
    jukeboxMode: true,
    jukeboxControl: snapshot?.sessionStatus,
  }) && snapshot?.sessionStatus?.ownerClientId === clientId

const commandHandlers = {
  play: (_, scope) => jukeboxClient.play(scope),
  pause: (_, scope) => jukeboxClient.pause(scope),
  skip: ({ index, offset = 0 }, scope) =>
    jukeboxClient.skip(index, offset, scope),
  seek: ({ position }, scope) => jukeboxClient.seek(position, scope),
  volume: ({ gain }, scope) => jukeboxClient.volume(gain, scope),
}

// Snapshot-driven controller; default-route integration is handled separately.
const RemotePlayerController = ({
  sessionId = getJukeboxSessionId(),
  clientId = getOrCreateJukeboxClientId(),
  deviceName = null,
}) => {
  const dispatch = useDispatch()
  const [snapshot, setSnapshot] = useState(null)
  const [error, setError] = useState(null)
  const [controlAvailable, setControlAvailable] = useState(false)
  const [takingControl, setTakingControl] = useState(false)
  const retirement = useRef(Promise.resolve())
  const current = useRef(null)
  const remoteEvent = useSelector((state) => state.player?.jukeboxRemote)
  const reconnectEvent = useSelector(
    (state) => state.activity?.streamReconnected,
  )

  useEffect(() => {
    let active = true
    let requestVersion = 0
    const scope = { sessionId, clientId }
    const record = {
      attached: false,
      observing: false,
      attachmentError: null,
      heartbeatAllowsControl: true,
      command: async (command) => {
        if (
          !active ||
          !record.attached ||
          !record.controlAvailable ||
          !snapshotAllowsControl(record.snapshot, clientId)
        )
          return
        try {
          const handler = commandHandlers[command.type]
          if (!handler) throw new Error('Unsupported remote command')
          await handler(command, scope)
          if (active) await record.refresh()
        } catch (err) {
          if (!active) return
          setError(err.message)
          if (err.status === 403) {
            record.attachmentError = err.message
            record.heartbeatAllowsControl = false
            record.controlAvailable = false
            setControlAvailable(false)
            await record.refresh()
          }
        }
      },
      refresh: async () => {
        const version = ++requestVersion
        try {
          const next = await jukeboxClient.sessionSnapshot(sessionId)
          if (active && version === requestVersion) {
            record.snapshot = next
            record.controlAvailable = record.heartbeatAllowsControl
            setSnapshot(next)
            setControlAvailable(record.controlAvailable)
            setError(record.attachmentError)
          }
        } catch (err) {
          if (active && version === requestVersion) {
            record.controlAvailable = false
            setControlAvailable(false)
            setError(err.message)
          }
        }
      },
    }
    current.current = record
    setSnapshot(null)
    setError(null)
    setControlAvailable(false)
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
        record.heartbeatAllowsControl = snapshotAllowsControl(
          { sessionStatus: status },
          clientId,
        )
        if (!record.heartbeatAllowsControl) {
          requestVersion++
          record.controlAvailable = false
          setControlAvailable(false)
        }
        await record.refresh()
      },
    })
    const attachment = episode.attach({ deviceName, dispatch: null })
    const acceptAttachment = (status) => {
      if (!active || !status) return
      record.attached = snapshotAllowsControl(
        { sessionStatus: status },
        clientId,
      )
      record.observing = true
      record.attachmentError = null
      record.heartbeatAllowsControl = record.attached
      return record.refresh()
    }
    const rejectAttachment = (err) => {
      if (!active) return
      record.attachmentError = err.message
      setError(err.message)
      if (err.status === 403) {
        record.observing = true
        record.heartbeatAllowsControl = false
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
    const handlePageHide = (event) => {
      if (event.persisted) return
      active = false
      record.controlAvailable = false
      retirement.current = episode.retire({ keepalive: true })
    }
    window.addEventListener('pagehide', handlePageHide)
    return () => {
      active = false
      window.removeEventListener('pagehide', handlePageHide)
      if (current.current === record) current.current = null
      retirement.current = episode.retire()
    }
  }, [dispatch, sessionId, clientId, deviceName])

  useEffect(() => {
    if (current.current?.observing) current.current.refresh()
  }, [remoteEvent, reconnectEvent])

  return (
    <section aria-label="Remote controller">
      {error && <p role="alert">{error}</p>}
      {snapshot &&
        (!controlAvailable || !snapshotAllowsControl(snapshot, clientId)) && (
          <button
            type="button"
            disabled={takingControl}
            onClick={() => current.current?.takeControl()}
          >
            Take control
          </button>
        )}
      {!snapshot ? (
        <p>Loading remote state</p>
      ) : (
        <fieldset
          disabled={
            !controlAvailable || !snapshotAllowsControl(snapshot, clientId)
          }
        >
          <RemotePlayer
            snapshot={snapshot}
            onCommand={(command) => current.current?.command(command)}
          />
        </fieldset>
      )}
    </section>
  )
}

export default RemotePlayerController
