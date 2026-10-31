/* eslint-disable react-refresh/only-export-components */
import React, { useEffect, useRef, useState } from 'react'
import ReactDOM from 'react-dom'
import { Provider } from 'react-redux'
import { combineReducers, createStore } from 'redux'
import { activityReducer } from '../src/reducers/activityReducer'
import { playerReducer } from '../src/reducers/playerReducer'
import RemotePlayerController from '../src/audioplayer/RemotePlayerController'
import { UnifiedNativePlayer } from '../src/audioplayer/UnifiedNativePlayer'
import { createPlaybackControlModule } from '../src/audioplayer/playbackControlModule'
import { createBrowserAudioAdapter } from '../src/audioplayer/browserAudioAdapter'
import { createMockRemoteAudioAdapter } from '../src/audioplayer/mockRemoteAudioAdapter'
import { startEventStream } from '../src/eventStream'
import config from '../src/config'
import 'navidrome-music-player/assets/index.css'

config.devNewEventStream = true

const params = new URLSearchParams(window.location.search)
const sessionId = params.get('sessionId') || undefined
const clientId = params.get('clientId') || undefined
const deviceName = params.get('deviceName') || undefined
const store = createStore(
  combineReducers({ player: playerReducer, activity: activityReducer }),
)
const root = document.getElementById('root')

function UnifiedControlValidation() {
  const [controller] = useState(() =>
    createPlaybackControlModule({
      initialVolume: 0.64,
      commandTimeoutMs: 1500,
    }),
  )
  const [browserAdapter] = useState(() =>
    createBrowserAudioAdapter({ unattached: true }),
  )
  const source = useRef(null)
  const [snapshot, setSnapshot] = useState(controller.getState())
  const [engineMode, setEngineMode] = useState('remote')
  const [mountPlayer, setMountPlayer] = useState(
    !Number(params.get('mountDelayMs')),
  )
  const [ready, setReady] = useState(false)
  const [lastResult, setLastResult] = useState(null)
  const history = useRef([])

  const migrate = async (local) => {
    const target = local
      ? browserAdapter
      : createMockRemoteAudioAdapter({
          device: { volumeProfile: 'bluetooth-jukebox' },
        })
    if (!local && params.get('responseLoss') === 'volume') {
      const operationIds = []
      const operationResults = []
      target.setFailure({ action: 'volume', responseLost: true })
      const queryOperation = target.queryOperation
      target.queryOperation = async (operationId) => {
        operationIds.push(operationId)
        const result = await queryOperation(operationId)
        operationResults.push(result)
        return result
      }
      window.__unifiedControlValidation.responseLoss = {
        operationIds,
        operationResults,
        target,
      }
    }
    if (local && !mountPlayer) {
      window.setTimeout(
        () => setMountPlayer(true),
        Number(params.get('mountDelayMs')),
      )
    }
    try {
      // Select the destination render branch before settlement clears pendingOutputTarget;
      // otherwise a remote host would briefly unmount the just-migrated media element.
      if (local) setEngineMode('local')
      await controller.migrateOutput({
        targetAdapter: target,
        targetDevice: local
          ? { name: 'Browser Output', type: 'browser' }
          : {
              name: 'Contract Bluetooth Output',
              deviceId: 'contract-remote',
              volumeProfile: 'bluetooth-jukebox',
            },
        readinessTimeoutMs: 1500,
        overallTimeoutMs: 5000,
      })
      setEngineMode(local ? 'local' : 'remote')
      if (!local) source.current = target
      setLastResult({ outcome: 'APPLIED' })
    } catch (error) {
      // The module remains the authority for the visible failure and recovery.
      if (local && browserAdapter.getAudioElement()) setEngineMode('local')
      setLastResult({ outcome: 'FAILED', code: error.code, name: error.name })
    }
  }

  useEffect(() => {
    const unsubscribe = controller.subscribe((state) => {
      history.current.push({
        status: state.status,
        acceptedIntent: state.acceptedIntent,
        pendingOutputTarget: state.pendingOutputTarget,
        currentQueueItemId: state.currentQueueItemId,
        position: state.position,
        mountedAudio: document.querySelectorAll('audio').length,
      })
      setSnapshot(state)
    })
    const adapter = createMockRemoteAudioAdapter({ initialVolume: 0.64 })
    source.current = adapter
    controller.setAdapter(adapter)
    const prepare = async () => {
      await controller.addQueueItems([
        {
          queueItemId: 'contract-track',
          id: 'contract-media',
          name: 'Unified Control Acceptance',
          singer: 'Isolated browser fixture',
          musicSrc: `/validation/contract-${params.get('media') || 'valid'}.wav`,
        },
      ])
      await controller.skipToQueueItem('contract-track')
      await controller.seek(Number(params.get('position')) || 0)
      setReady(true)
      if (params.get('preparePlay') === '1') await controller.play()
      if (params.get('autostart') === '1') {
        await controller.play()
        await migrate(true)
      }
    }
    prepare().catch((error) =>
      setLastResult({ outcome: 'FAILED', code: error.code }),
    )
    window.__unifiedControlValidation = {
      controller,
      browserAdapter,
      history: history.current,
      snapshot: () => controller.getState(),
    }
    return () => {
      unsubscribe()
      controller.setAdapter(null)
      delete window.__unifiedControlValidation
    }
    // This validation-only scene is constructed once per document, like its host.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <main style={{ padding: 24, fontFamily: 'sans-serif' }}>
      <h1>Unified native control acceptance</h1>
      <p>
        Isolated validation entry. Remote output is a mock contract adapter;
        browser media and the native Player UI are real. No MPV is used.
      </p>
      <button
        id="migrate-browser"
        disabled={!ready || controller.isMigrating()}
        onClick={() => migrate(true)}
      >
        Migrate to Browser Output
      </button>{' '}
      <button
        id="migrate-remote"
        disabled={!ready || controller.isMigrating()}
        onClick={() => migrate(false)}
      >
        Migrate to contract Bluetooth output
      </button>
      <output id="contract-ready">{String(ready)}</output>
      <pre id="contract-state">{JSON.stringify(snapshot, null, 2)}</pre>
      <pre id="contract-result">{JSON.stringify(lastResult)}</pre>
      {mountPlayer && (
        <UnifiedNativePlayer
          controller={controller}
          browserAdapter={browserAdapter}
          engineMode={engineMode}
          mode="full"
          autoPlay={false}
          showDownload={false}
          showReload={false}
          showThemeSwitch={false}
          showMediaSession={false}
          responsive={params.get('mobile') === '1'}
          toggleMode={false}
        />
      )}
    </main>
  )
}

function SessionHandoffValidation() {
  const [documentId] = useState(() => clientId || crypto.randomUUID())
  const [state, setState] = useState({ status: 'connecting', readOnly: true })
  const current = useRef(state)
  const storageKey = `validation-handoff:${sessionId}`
  const accept = (value) => {
    current.current = value
    setState(value)
  }
  const request = async (route, body) => {
    const response = await fetch(`/api/jukebox/${route}`, {
      method: body ? 'POST' : 'GET',
      headers: {
        'Content-Type': 'application/json',
        'X-ND-Authorization': `Bearer ${localStorage.getItem('token')}`,
      },
      ...(body ? { body: JSON.stringify(body) } : {}),
    })
    const text = await response.text()
    let parsed = null
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = { error: text }
    }
    return { status: response.status, body: parsed }
  }
  useEffect(() => {
    const attach = async () => {
      if (params.get('deferHandoff') === '1') {
        await new Promise((resolve) => {
          window.__releaseValidationHandoff = resolve
        })
      }
      const token = sessionStorage.getItem(storageKey)
      const result = token
        ? await request('session/handoff', {
            sessionId,
            newClientId: documentId,
            handoffToken: token,
          })
        : await request('session/attach', { sessionId, clientId: documentId })
      if (result.status === 200) {
        if (result.body.controlHandoffToken) {
          sessionStorage.setItem(storageKey, result.body.controlHandoffToken)
        }
        accept({
          status: 'attached',
          readOnly: false,
          documentId,
          generation: result.body.bindingGeneration,
        })
      } else {
        accept({
          status: 'observer',
          readOnly: true,
          documentId,
          responseStatus: result.status,
        })
      }
    }
    attach().catch((error) =>
      accept({ status: 'error', readOnly: true, message: error.message }),
    )
    // Each document constructs a new identity; only its one-time token survives refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const writeVolume = async () => {
    const scope = current.current
    const result = await request('volume', {
      sessionId,
      clientId: documentId,
      bindingGeneration: scope.generation,
      operationId: `g${scope.generation}:${crypto.randomUUID()}`,
      gain: 0.64,
    })
    accept({
      ...scope,
      lastWriteStatus: result.status,
      readOnly: result.status !== 200,
    })
  }
  return (
    <main style={{ padding: 24, fontFamily: 'sans-serif' }}>
      <h1>Native API document handoff acceptance</h1>
      <p>Real API protocol only; this fixture never starts a server output.</p>
      <pre id="handoff-state">{JSON.stringify(state, null, 2)}</pre>
      <button id="session-volume" onClick={writeVolume}>
        Write desired gain without an active track
      </button>
    </main>
  )
}

const renderMessage = (message) => {
  root.textContent = message
}

if (params.get('contracts') === 'session-handoff') {
  ReactDOM.render(<SessionHandoffValidation />, root)
} else if (params.get('contracts') === 'unified-control') {
  ReactDOM.render(<UnifiedControlValidation />, root)
} else if (!localStorage.getItem('is-authenticated')) {
  renderMessage('Validation fixture requires an authenticated browser session.')
} else {
  startEventStream(store.dispatch)
    .then((stream) => {
      window.addEventListener('pagehide', () => stream?.close(), { once: true })
      ReactDOM.render(
        <Provider store={store}>
          <main>
            <h1>Remote player validation</h1>
            <p>
              Validation-only entry; the default application route is unchanged.
            </p>
            <RemotePlayerController
              sessionId={sessionId}
              clientId={clientId}
              deviceName={deviceName}
            />
          </main>
        </Provider>,
        root,
      )
    })
    .catch((error) => renderMessage(`Event stream failed: ${error.message}`))
}
