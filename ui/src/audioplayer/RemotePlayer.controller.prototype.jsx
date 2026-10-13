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
  play: () => jukeboxClient.play(),
  pause: () => jukeboxClient.pause(),
  skip: ({ index, offset = 0 }) => jukeboxClient.skip(index, offset),
  seek: ({ position }) => jukeboxClient.seek(position),
  volume: ({ gain }) => jukeboxClient.volume(gain),
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
    const record = {
      attached: false,
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
          await handler(command)
          if (active) await record.refresh()
        } catch (err) {
          if (active) setError(err.message)
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
            setError(null)
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
    const attachment = retirement.current.then(() =>
      active
        ? attachJukeboxSession({
            client: jukeboxClient,
            sessionId,
            clientId,
            deviceName,
          })
        : null,
    )
    const retire = () => {
      if (!retired) {
        retired = attachment
          .then(
            (status) =>
              status &&
              detachJukeboxSession({
                client: jukeboxClient,
                sessionId,
                clientId,
              }),
          )
          .catch(() => {})
      }
      return retired
    }
    attachment
      .then((status) => {
        if (!active || !status) return
        record.attached = true
        cleanupHeartbeat = startJukeboxHeartbeatLoop({
          jukeboxMode: true,
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
              return
            }
            await record.refresh()
          },
        })
        return record.refresh()
      })
      .catch((err) => {
        if (active) setError(err.message)
      })
    return () => {
      active = false
      cleanupHeartbeat()
      if (current.current === record) current.current = null
      retirement.current = retire()
    }
  }, [dispatch, sessionId, clientId, deviceName])

  useEffect(() => {
    if (current.current?.attached) current.current.refresh()
  }, [remoteEvent, reconnectEvent])

  return (
    <section aria-label="Remote controller prototype">
      {error && <p role="alert">{error}</p>}
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
