import React, { useEffect, useRef, useState } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import RemotePlayerPrototype from './RemotePlayer.prototype'
import jukeboxClient from './jukeboxClient'
import {
  attachJukeboxSession,
  canControlJukebox,
  detachJukeboxSession,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
  runJukeboxHeartbeat,
  startJukeboxHeartbeatLoop,
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

// Validation-only controller; not registered in the application routes.
const RemotePlayerControllerPrototype = ({
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
    let retired
    let requestVersion = 0
    let cleanupHeartbeat = () => {}
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
    let attachment = retirement.current.then(() =>
      active
        ? attachJukeboxSession({
            client: jukeboxClient,
            sessionId,
            clientId,
            deviceName,
          })
        : null,
    )
    const retire = (keepalive = false) => {
      if (!retired) {
        const detach = () =>
          detachJukeboxSession({
            client: jukeboxClient,
            sessionId,
            clientId,
            keepalive,
          })
        retired = attachment
          .then(
            (status) => status && detach(),
            () => record.attached && detach(),
          )
          .catch(() => {})
      }
      return retired
    }
    const acceptAttachment = (status) => {
      if (!active || !status) return
      record.attached = snapshotAllowsControl(
        { sessionStatus: status },
        clientId,
      )
      record.observing = true
      record.attachmentError = null
      record.heartbeatAllowsControl = record.attached
      cleanupHeartbeat()
      cleanupHeartbeat = startJukeboxHeartbeatLoop({
        jukeboxMode: record.attached,
        sessionId,
        clientId,
        onHeartbeat: async () => {
          const status = await runJukeboxHeartbeat({
            client: jukeboxClient,
            sessionId,
            clientId,
            deviceName,
            dispatch: (action) => {
              if (active) dispatch(action)
            },
          })
          if (!active) return
          record.heartbeatAllowsControl = snapshotAllowsControl(
            { sessionStatus: status },
            clientId,
          )
          if (!record.heartbeatAllowsControl) {
            requestVersion++
            record.controlAvailable = false
            setControlAvailable(false)
            return record.refresh()
          }
          await record.refresh()
        },
      })
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
      attachment = attachment
        .catch(() => {})
        .then(() =>
          active
            ? attachJukeboxSession({
                client: jukeboxClient,
                sessionId,
                clientId,
                deviceName,
                takeover: true,
              })
            : null,
        )
      attachment
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
      cleanupHeartbeat()
      record.controlAvailable = false
      retirement.current = retire(true)
    }
    window.addEventListener('pagehide', handlePageHide)
    return () => {
      active = false
      cleanupHeartbeat()
      window.removeEventListener('pagehide', handlePageHide)
      if (current.current === record) current.current = null
      retirement.current = retire()
    }
  }, [dispatch, sessionId, clientId, deviceName])

  useEffect(() => {
    if (current.current?.observing) current.current.refresh()
  }, [remoteEvent, reconnectEvent])

  return (
    <section aria-label="Remote controller prototype">
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
          <RemotePlayerPrototype
            snapshot={snapshot}
            onCommand={(command) => current.current?.command(command)}
          />
        </fieldset>
      )}
    </section>
  )
}

export default RemotePlayerControllerPrototype
