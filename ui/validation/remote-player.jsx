import React from 'react'
import ReactDOM from 'react-dom'
import { Provider } from 'react-redux'
import { combineReducers, createStore } from 'redux'
import { activityReducer } from '../src/reducers/activityReducer'
import { playerReducer } from '../src/reducers/playerReducer'
import RemotePlayerController from '../src/audioplayer/RemotePlayerController'
import { startEventStream } from '../src/eventStream'
import config from '../src/config'

config.devNewEventStream = true

const params = new URLSearchParams(window.location.search)
const sessionId = params.get('sessionId') || undefined
const clientId = params.get('clientId') || undefined
const deviceName = params.get('deviceName') || undefined
const store = createStore(
  combineReducers({ player: playerReducer, activity: activityReducer }),
)
const root = document.getElementById('root')

const renderMessage = (message) => {
  root.textContent = message
}

if (!localStorage.getItem('is-authenticated')) {
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
