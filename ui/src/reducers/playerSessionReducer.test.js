import { describe, it, expect } from 'vitest'
import { playerReducer } from './playerReducer'
import {
  PLAYER_JUKEBOX_SESSION_STATUS,
  PLAYER_SET_JUKEBOX_MODE,
  beginJukeboxEpisode,
  disableJukeboxEpisode,
  endJukeboxEpisode,
} from '../actions'
import { captureEligibleJukeboxScope } from '../audioplayer/jukeboxSession'
import { audioVolumeToUiVolume } from '../audioplayer/volumeMapping'
import { remoteGainToUiVolume } from '../audioplayer/volumeProfiles'

const canonicalize = (value) => {
  if (Array.isArray(value)) return value.map(canonicalize)
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, canonicalize(value[key])]),
    )
  }
  return value
}

const canonicalJson = (value) => JSON.stringify(canonicalize(value))

// Whole-payload assertion: identical key sets AND identical canonical JSON,
// so a dropped, added or undefined-valued field fails even when every shared
// field matches.
const expectWholeRemote = (nextState, expectedRemote) => {
  expect(Object.keys(nextState.jukeboxRemote).sort()).toEqual(
    Object.keys(expectedRemote).sort(),
  )
  expect(canonicalJson(nextState.jukeboxRemote)).toBe(
    canonicalJson(expectedRemote),
  )
}

// Full SessionStatus-shaped observation carrying the server-issued ordering
// and identity contract (see core/playback SessionStatus / jukebox SSE).
// Shape alone never grants trust: the same body can arrive as a raw SSE
// observation or from the atomic session-snapshot boundary.
const strictRemoteStatus = (overrides = {}) => ({
  sessionId: 'jukebox-session:admin',
  ownerClientId: 'tab-a',
  ownershipState: 'attached',
  deviceName: 'pulse/test',
  currentIndex: 0,
  trackId: 'song-a',
  queueItemId: 'qi-a',
  playing: true,
  position: 12,
  gain: 0.25,
  desiredGain: 0.4,
  muted: false,
  attached: true,
  queueVersion: 20,
  stateSequence: 10,
  playMode: 'orderLoop',
  playbackInstanceId: 'inst-1',
  bindingGeneration: 2,
  lastHeartbeat: '2026-11-02T00:00:00Z',
  ...overrides,
})

// Trusted provenance is local action metadata, never server payload fields:
// a raw SSE observation may carry a complete SessionStatus-shaped body (and
// even a server-side baseline claim) and must still only raise floors and
// invalidate. Only an action dispatched from the atomic session-snapshot
// boundary inside the trusted episode may establish or replace the full
// scene or clear scene identity.
const atomicSnapshotMeta = {
  source: 'atomic-session-snapshot',
  sessionId: 'jukebox-session:admin',
  deviceName: 'pulse/test',
  clientId: 'tab-a',
}

const dispatchRemoteStatus = (state, overrides = {}, meta) =>
  playerReducer(state, {
    type: PLAYER_JUKEBOX_SESSION_STATUS,
    data: strictRemoteStatus(overrides),
    ...(meta ? { meta } : {}),
  })

// Episodes are reducer-owned lifecycle state: trusted atomic snapshots must
// name a live token with the revision and request binding captured at GET
// start. The fixture mirrors production: BEGIN once, then meta from the
// current registration and admitted scene.
const EPISODE_TOKEN = 'episode-token'

const ensureEpisode = (state) =>
  state.jukeboxEpisodes?.[EPISODE_TOKEN]
    ? state
    : playerReducer(
        state,
        beginJukeboxEpisode({
          token: EPISODE_TOKEN,
          sessionId: 'jukebox-session:admin',
          clientId: 'tab-a',
        }),
      )

const episodeMeta = (state, overrides = {}) => ({
  source: 'atomic-session-snapshot',
  sessionId: 'jukebox-session:admin',
  clientId: 'tab-a',
  token: EPISODE_TOKEN,
  revision: state.jukeboxEpisodes?.[EPISODE_TOKEN]?.revision ?? 0,
  requestBinding: {
    bindingGeneration: state.jukeboxRemote?.bindingGeneration ?? null,
    deviceName: state.jukeboxRemote?.deviceName ?? null,
  },
  ...overrides,
})

const dispatchAtomicSnapshot = (state, overrides = {}, metaOverrides = {}) => {
  const withEpisode = ensureEpisode(state)
  return dispatchRemoteStatus(
    withEpisode,
    overrides,
    episodeMeta(withEpisode, metaOverrides),
  )
}

const strictRemoteBaseline = {
  attached: true,
  bindingGeneration: 2,
  currentIndex: 0,
  desiredGain: 0.4,
  deviceName: 'pulse/test',
  gain: 0.25,
  muted: false,
  playMode: 'orderLoop',
  playing: true,
  playbackInstanceId: 'inst-1',
  position: 12,
  queueItemId: 'qi-a',
  queueVersion: 20,
  stateSequence: 10,
  trackId: 'song-a',
}

const strictBaselineState = () =>
  dispatchAtomicSnapshot(playerReducer(undefined, { type: '@@INIT' }))

describe('playerReducer strict remote admission and metadata preservation', () => {
  it('preserves the full strict metadata contract exactly', () => {
    expectWholeRemote(strictBaselineState(), strictRemoteBaseline)
  })

  it('preserves an explicit zero state sequence when establishing the initial floor', () => {
    const next = dispatchAtomicSnapshot(
      playerReducer(undefined, { type: '@@INIT' }),
      {
        bindingGeneration: 1,
        stateSequence: 0,
        queueVersion: 0,
        playMode: 'order',
        playbackInstanceId: 'inst-0',
        position: 0,
      },
    )

    expect(next.jukeboxRemote.stateSequence).toBe(0)
    expect(next.jukeboxRemote.bindingGeneration).toBe(1)
    expect(next.jukeboxRemote.queueVersion).toBe(0)
  })

  it('ignores a server baseline flag on a raw observation and never establishes the trusted scene from it', () => {
    const next = dispatchRemoteStatus(
      playerReducer(undefined, { type: '@@INIT' }),
      {
        // A payload-side claim cannot substitute for local provenance.
        fullSnapshot: true,
      },
    )

    expect(next.jukeboxRemote.bindingGeneration).toBe(2)
    expect(next.jukeboxRemote.stateSequence).toBe(10)
    expect(next.jukeboxRemote).not.toHaveProperty('playbackInstanceId')
  })

  it('drops a lower-generation update even when its sequence is higher', () => {
    const baseline = strictBaselineState()
    const next = dispatchRemoteStatus(baseline, {
      bindingGeneration: 1,
      stateSequence: 1000,
      queueVersion: 40,
      trackId: 'song-z',
      playing: false,
    })

    expect(next).toBe(baseline)
  })

  it('admits a higher generation that restarts sequencing from a new initial snapshot', () => {
    // The reacquired atomic snapshot of the rebased generation carries the
    // full new scene; the raw SSE event that announced it only raised the
    // floor.
    const next = dispatchAtomicSnapshot(strictBaselineState(), {
      bindingGeneration: 3,
      stateSequence: 1,
      queueVersion: 1,
      trackId: 'song-c',
      queueItemId: 'qi-c',
      playing: false,
      playMode: 'shufflePlay',
      playbackInstanceId: 'inst-2',
      position: 3,
    })

    expectWholeRemote(next, {
      ...strictRemoteBaseline,
      bindingGeneration: 3,
      stateSequence: 1,
      queueVersion: 1,
      trackId: 'song-c',
      queueItemId: 'qi-c',
      playing: false,
      playMode: 'shufflePlay',
      playbackInstanceId: 'inst-2',
      position: 3,
    })
  })

  it('drops a same-generation older sequence even at an equal queue version', () => {
    const baseline = strictBaselineState()
    const next = dispatchRemoteStatus(baseline, {
      stateSequence: 9,
      queueVersion: 20,
      trackId: 'song-y',
      playing: false,
    })

    expect(next).toBe(baseline)
  })

  it('treats equal-sequence position, heartbeat and handoff metadata as a non-conflicting observation', () => {
    const next = dispatchRemoteStatus(strictBaselineState(), {
      position: 13,
      lastHeartbeat: '2026-11-02T00:05:00Z',
      controlHandoffToken: 'handoff-1',
    })

    expect(next.jukeboxRemote.position).toBe(13)
    expect(next.jukeboxRemote.playing).toBe(true)
    expect(next.jukeboxRemote.trackId).toBe('song-a')
    expect(next.jukeboxRemote.queueVersion).toBe(20)
  })

  it('retains the scene and floor when an equal-sequence update conflicts on semantic playback fields', () => {
    const baseline = strictBaselineState()
    const next = dispatchRemoteStatus(baseline, {
      playing: false,
      trackId: 'song-b',
      queueItemId: 'qi-b',
      gain: 0.9,
      desiredGain: 0.9,
      muted: true,
      playMode: 'singleLoop',
      playbackInstanceId: 'inst-9',
      position: 2,
    })

    expectWholeRemote(next, strictRemoteBaseline)
  })

  it('clears the playback instance when a trusted full snapshot omits it on an idle scene', () => {
    const next = dispatchAtomicSnapshot(strictBaselineState(), {
      stateSequence: 11,
      playing: false,
      currentIndex: -1,
      trackId: '',
      queueItemId: '',
      playbackInstanceId: undefined,
      position: 0,
    })

    expect(next.jukeboxRemote.playing).toBe(false)
    expect(next.jukeboxRemote.currentIndex).toBe(-1)
    expect(next.jukeboxRemote.stateSequence).toBe(11)
    expect(next.jukeboxRemote.playbackInstanceId).toBeNull()
  })

  it('retains the scene and playback instance when a raw full-shaped observation omits the instance', () => {
    const next = dispatchRemoteStatus(strictBaselineState(), {
      stateSequence: 11,
      playing: false,
      currentIndex: -1,
      trackId: '',
      queueItemId: '',
      playbackInstanceId: undefined,
      position: 0,
    })

    expectWholeRemote(next, {
      ...strictRemoteBaseline,
      stateSequence: 11,
    })
  })

  it('does not clear the playback instance when a partial observation omits it', () => {
    const next = dispatchRemoteStatus(strictBaselineState(), {
      stateSequence: 12,
      position: 14,
      playbackInstanceId: undefined,
    })

    expect(next.jukeboxRemote.position).toBe(14)
    expect(next.jukeboxRemote.playing).toBe(true)
    expect(next.jukeboxRemote.trackId).toBe('song-a')
    expect(next.jukeboxRemote.playbackInstanceId).toBe('inst-1')
  })

  it.each([
    ['missing', undefined],
    ['zero', 0],
    ['not-a-number', 'generation-two'],
  ])(
    'cannot rebaseline a strict scene with an invalid server generation (%s)',
    (_, bindingGeneration) => {
      const baseline = strictBaselineState()
      const next = dispatchRemoteStatus(baseline, {
        bindingGeneration,
        stateSequence: 500,
        ownershipState: 'recovering',
        trackId: 'song-z',
        playing: false,
        lastHeartbeat: '2026-11-02T00:07:00Z',
      })

      expect(next.jukeboxRemote.trackId).toBe('song-a')
      expect(next.jukeboxRemote.playing).toBe(true)
      expect(next.jukeboxControl.ownershipState).toBe('recovering')
      expect(next.jukeboxControl.lastHeartbeat).toBe('2026-11-02T00:07:00Z')
    },
  )

  it('keeps best-effort observation outside a strict browser-local session', () => {
    const localState = {
      ...playerReducer(undefined, { type: '@@INIT' }),
      jukeboxMode: false,
      volume: 0.33,
    }
    const next = dispatchRemoteStatus(localState, {
      bindingGeneration: undefined,
      stateSequence: undefined,
      queueItemId: undefined,
      playMode: undefined,
      playbackInstanceId: undefined,
      trackId: 'song-x',
      position: 5,
    })

    expect(next.jukeboxRemote.trackId).toBe('song-x')
    expect(next.jukeboxRemote.position).toBe(5)
    expect(next.volume).toBe(0.33)
  })
})

// Persistent regressions for the verified remote-authority bypasses (audit
// probes B/B2/C/D, tmp/native-playback-validation/audit-remote-admission-20261102).
// A raw attach/status/SSE observation may raise the ordering floor and may
// downgrade control, but without trusted atomic-snapshot admission it must
// never enable control, and once control is lost a later raw observation
// must not re-enable it. Eligibility here is the explicit owner scope —
// this file's document identity is exercised in the keyboard suite.
describe('playerReducer persistent remote-authority admission regressions', () => {
  const enableJukeboxMode = (state) =>
    playerReducer(state, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: true, device: 'pulse/test' },
    })
  const ownerEligibility = (state) =>
    captureEligibleJukeboxScope(state, {
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
    })

  it('a raw attached observation without trusted atomic admission does not enable control', () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = dispatchRemoteStatus(state)

    expect(state.jukeboxControl.ownershipState).toBe('attached')
    expect(ownerEligibility(state)).toBeNull()
    // Floor-only: no scene identity (no current item, no playback instance).
    expect(state.jukeboxRemote.trackId ?? null).toBeNull()
    expect(state.jukeboxRemote).not.toHaveProperty('playbackInstanceId')
    expect(state.jukeboxRemote.bindingGeneration).toBe(2)
    expect(state.jukeboxRemote.stateSequence).toBe(10)
  })

  it('after a takeover downgrade, a later raw attached observation cannot re-enable control', () => {
    let state = enableJukeboxMode(strictBaselineState())
    expect(ownerEligibility(state)).not.toBeNull()

    state = dispatchRemoteStatus(state, {
      ownerClientId: 'tab-b',
      ownershipState: 'taken_over',
      terminationReason: 'taken_over',
    })
    expect(ownerEligibility(state)).toBeNull()

    // A later raw observation claims attach again: it must stay read-only.
    state = dispatchRemoteStatus(state, {
      ownerClientId: 'tab-b',
      ownershipState: 'attached',
    })
    expect(ownerEligibility(state)).toBeNull()
    // The accepted scene is untouched by raw observations.
    expect(state.jukeboxRemote.trackId).toBe('song-a')
    expect(state.jukeboxRemote.playing).toBe(true)
  })

  it('after a readonly recovering downgrade, a later raw attached observation cannot re-enable control', () => {
    let state = enableJukeboxMode(strictBaselineState())
    expect(ownerEligibility(state)).not.toBeNull()

    state = dispatchRemoteStatus(state, {
      ownershipState: 'recovering',
      terminationReason: null,
    })
    expect(ownerEligibility(state)).toBeNull()

    state = dispatchRemoteStatus(state, {
      ownershipState: 'attached',
    })
    expect(ownerEligibility(state)).toBeNull()
  })

  it('trusted meta with missing or invalid ordering cannot establish or reset the strict scene', () => {
    // Fresh authority: trusted provenance but a payload without usable
    // ordering must not establish scene identity (no crash, no scene).
    const fresh = dispatchAtomicSnapshot(
      playerReducer(undefined, { type: '@@INIT' }),
      {
        bindingGeneration: undefined,
        stateSequence: undefined,
        trackId: 't',
      },
    )
    expect(fresh.jukeboxRemote?.trackId ?? null).toBeNull()

    // Existing strict scene: trusted provenance with an invalid generation
    // must not reset the accepted scene (anti-ABA at the metadata boundary).
    const retained = dispatchAtomicSnapshot(strictBaselineState(), {
      bindingGeneration: 'generation-two',
      stateSequence: 500,
      trackId: 'song-x',
      playing: false,
    })
    expect(retained.jukeboxRemote.trackId).toBe('song-a')
    expect(retained.jukeboxRemote.playing).toBe(true)
    expect(retained.jukeboxRemote.bindingGeneration).toBe(2)
    expect(retained.jukeboxRemote.stateSequence).toBe(10)
  })

  it('trusted meta from a non-current session is not admitted by the shared authority', () => {
    let state = enableJukeboxMode(strictBaselineState())
    const next = playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      // Probe-D shape: the foreign session's snapshot advances the ordering
      // and would wholesale-replace the accepted scene if session provenance
      // were not checked against the current authority.
      data: strictRemoteStatus({ trackId: 'song-foreign', stateSequence: 11 }),
      meta: {
        ...atomicSnapshotMeta,
        sessionId: 'jukebox-session:SOMEONE-ELSE',
      },
    })

    expect(next.jukeboxRemote.trackId).toBe('song-a')
    expect(next.jukeboxControl.sessionId).toBe('jukebox-session:admin')
    expect(ownerEligibility(next)).not.toBeNull()
  })
})

describe('playerReducer caller-scope admission contract', () => {
  const enableJukeboxMode = (state) =>
    playerReducer(state, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: true, device: 'pulse/test' },
    })

  const ownerIdentity = {
    token: 'owner-token',
    sessionId: 'jukebox-session:admin',
    clientId: 'tab-a',
  }
  // Trusted meta for a live token: revision and request binding captured at
  // GET start against the CURRENT registration and admitted scene (the same
  // shape buildAtomicSnapshotMeta issues in production).
  const scopedMeta = (state, identity, overrides = {}) => ({
    source: 'atomic-session-snapshot',
    sessionId: identity.sessionId,
    clientId: identity.clientId,
    deviceName: 'pulse/test',
    token: identity.token,
    revision: state.jukeboxEpisodes?.[identity.token]?.revision ?? 0,
    requestBinding: {
      bindingGeneration: state.jukeboxRemote?.bindingGeneration ?? null,
      deviceName: state.jukeboxRemote?.deviceName ?? null,
    },
    ...overrides,
  })
  const dispatchScopedSnapshot = (
    state,
    identity,
    overrides = {},
    metaOverrides = {},
  ) =>
    playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus(overrides),
      meta: scopedMeta(state, identity, metaOverrides),
    })
  const beginIdentity = (state, identity) =>
    playerReducer(state, beginJukeboxEpisode(identity))
  // Caller-scoped eligibility for the OWNER on this state: the document's
  // default identity is exercised separately in the keyboard suite.
  const ownerScope = (state) =>
    captureEligibleJukeboxScope(state, ownerIdentity)

  // Live owner episode: the baseline scene is admitted through the reducer
  // lifecycle with a real live token (no tokenless shape exemption).
  const admittedOwnerScene = () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginIdentity(state, ownerIdentity)
    return dispatchScopedSnapshot(state, ownerIdentity)
  }

  it('a trusted-shaped action without a token is rejected whole, never downgraded to raw', () => {
    const baseline = admittedOwnerScene()
    // Shape-valid atomic provenance but NO token: under the approved
    // lifecycle there is no tokenless bypass — the whole action is rejected
    // (identical state), not treated as a raw observation.
    const next = playerReducer(baseline, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({ stateSequence: 11, trackId: 'song-x' }),
      meta: atomicSnapshotMeta,
    })

    expect(next).toBe(baseline)
  })

  it('a mismatched-session trusted action is rejected whole', () => {
    const baseline = admittedOwnerScene()
    const next = playerReducer(baseline, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({ trackId: 'song-foreign', stateSequence: 11 }),
      meta: {
        ...atomicSnapshotMeta,
        sessionId: 'jukebox-session:SOMEONE-ELSE',
      },
    })

    expect(next).toBe(baseline)
  })

  it('a registered observer snapshot is not rejected merely for owner mismatch', () => {
    const baseline = admittedOwnerScene()
    const observerIdentity = {
      token: 'observer-token',
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-b',
    }
    const state = beginIdentity(baseline, observerIdentity)
    // The snapshot came from the observer's own live atomic boundary while
    // the owner remains tab-a: the pair must still be admitted — eligibility
    // is a caller-scope concern, not admission.
    const next = dispatchScopedSnapshot(state, observerIdentity, {
      stateSequence: 11,
      ownerClientId: 'tab-a',
    })

    expect(next.jukeboxRemote.stateSequence).toBe(11)
    expect(next.jukeboxRemote.trackId).toBe('song-a')
    expect(next.jukeboxControl.sessionId).toBe('jukebox-session:admin')
    expect(next.jukeboxControl.ownerClientId).toBe('tab-a')
    expect(next.jukeboxControl.controlGranted).toBe(true)
  })

  it('an unregistered (expired) episode token is rejected whole', () => {
    const state = beginIdentity(admittedOwnerScene(), {
      token: 'token-live',
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
    })
    const baseline = dispatchScopedSnapshot(state, {
      token: 'token-live',
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-a',
    })
    const next = playerReducer(baseline, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({ trackId: 'song-x', stateSequence: 11 }),
      meta: scopedMeta(baseline, {
        token: 'token-ghost',
        sessionId: 'jukebox-session:admin',
        clientId: 'tab-a',
      }),
    })

    expect(next).toBe(baseline)
  })

  it('a fresh live token admits an identical scene after a same-identity remount', () => {
    const identity = { sessionId: 'jukebox-session:admin', clientId: 'tab-a' }
    const oldEpisode = { ...identity, token: 'token-old' }
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginIdentity(state, oldEpisode)
    state = dispatchScopedSnapshot(state, oldEpisode)
    state = playerReducer(state, endJukeboxEpisode(oldEpisode))
    state = beginIdentity(state, { ...identity, token: 'token-new' })

    // Identical scene, fresh live token: acceptance is visible as a new
    // admitted state (rejection would return the identical previous state).
    const accepted = dispatchScopedSnapshot(state, {
      ...identity,
      token: 'token-new',
    })
    expect(accepted).not.toBe(state)
    expect(accepted.jukeboxRemote.trackId).toBe('song-a')

    // The retired token is rejected whole.
    const retired = playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({ stateSequence: 11 }),
      meta: scopedMeta(state, oldEpisode),
    })
    expect(retired).toBe(state)
  })

  it('a GET begun before DISABLE cannot regrant after the disable revision', () => {
    const baseline = admittedOwnerScene()
    // The GET captured revision 0 at start; then the episode is disabled.
    const inFlightMeta = scopedMeta(baseline, ownerIdentity)
    let state = playerReducer(baseline, disableJukeboxEpisode(ownerIdentity))
    expect(state.jukeboxEpisodes[ownerIdentity.token].revision).toBe(1)
    const next = playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({ stateSequence: 11 }),
      meta: inFlightMeta,
    })

    expect(next).toBe(state)
    expect(ownerScope(next)).toBeNull()
  })

  it('an observer GET/end leaves the owner eligible on the same state', () => {
    const baseline = admittedOwnerScene()
    const observerIdentity = {
      token: 'observer-token',
      sessionId: 'jukebox-session:admin',
      clientId: 'tab-b',
    }
    let state = beginIdentity(baseline, observerIdentity)
    state = dispatchScopedSnapshot(state, observerIdentity, {
      stateSequence: 11,
      ownerClientId: 'tab-a',
    })
    expect(ownerScope(state)).not.toBeNull()
    // The observer retires: its reconciliation is removed; the owner's
    // eligibility on the SAME shared state is untouched.
    state = playerReducer(state, endJukeboxEpisode(observerIdentity))
    expect(ownerScope(state)).not.toBeNull()
    expect(captureEligibleJukeboxScope(state, observerIdentity)).toBeNull()
  })

  it('a trusted same-generation device contradiction retains the scene and suspends with a conflict', () => {
    // Reconciled binding: device pulse/test at generation 2.
    const baseline = admittedOwnerScene()
    // The same generation now reports a different canonical output device.
    // The admitted binding identity is contradicted, so the authority RETAINS
    // the accepted scene and pair, raises the conflict marker once, and
    // suspends writes — the episode is never reconciled to the contradicted
    // device and no follow-up GET bound to it can restore the grant.
    const next = dispatchScopedSnapshot(
      baseline,
      ownerIdentity,
      {
        deviceName: 'pulse/other-sink',
        stateSequence: 11,
      },
      { deviceName: 'pulse/other-sink' },
    )

    expect(next.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(next.jukeboxRemote.stateSequence).toBe(10)
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(next.jukeboxControl.sceneConflicted).toBe(true)
    expect(next.jukeboxEpisodes[ownerIdentity.token].reconciledBinding).toEqual(
      {
        bindingGeneration: 2,
        canonicalDevice: 'pulse/test',
      },
    )
    expect(ownerScope(next)).toBeNull()
  })

  it('an equal-order trusted snapshot naming a different device conflicts instead of replacing the scene', () => {
    const baseline = admittedOwnerScene()
    const next = dispatchScopedSnapshot(baseline, ownerIdentity, {
      deviceName: 'pulse/other-sink',
    })

    expect(next.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(next.jukeboxRemote.stateSequence).toBe(10)
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(next.jukeboxControl.sceneConflicted).toBe(true)
    expect(ownerScope(next)).toBeNull()
  })

  it('a heartbeat-style follow-up bound to the contradicted device never grants it', () => {
    let state = admittedOwnerScene()
    state = dispatchScopedSnapshot(state, ownerIdentity, {
      deviceName: 'pulse/other-sink',
      stateSequence: 11,
    })
    expect(state.jukeboxControl.sceneConflicted).toBe(true)

    // Ordinary production refresh: captureRequestBinding reads the RETAINED
    // scene (generation 2, pulse/test) at GET start. The repeat contradiction
    // is coalesced to the identical state — the conflict marker is raised
    // once and the grant can never be restored on the contradicted device.
    const productionRefresh = dispatchScopedSnapshot(state, ownerIdentity, {
      deviceName: 'pulse/other-sink',
      stateSequence: 12,
    })
    expect(productionRefresh).toBe(state)
    expect(productionRefresh.jukeboxControl.controlGranted).toBe(false)
    expect(productionRefresh.jukeboxControl.sceneConflicted).toBe(true)
    expect(productionRefresh.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(productionRefresh.jukeboxRemote.stateSequence).toBe(10)
    expect(ownerScope(productionRefresh)).toBeNull()

    // Same outcome when the request meta is itself bound to the contradicted
    // device: a mere binding match may never resurrect B over the retained
    // scene A.
    const boundToB = playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({
        deviceName: 'pulse/other-sink',
        stateSequence: 12,
      }),
      meta: {
        ...scopedMeta(state, ownerIdentity),
        requestBinding: {
          bindingGeneration: 2,
          deviceName: 'pulse/other-sink',
        },
      },
    })
    expect(boundToB.jukeboxControl.controlGranted).toBe(false)
    expect(boundToB.jukeboxControl.sceneConflicted).toBe(true)
    expect(boundToB.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(ownerScope(boundToB)).toBeNull()
  })

  it('a consistent device-A advance after a device conflict reconciles and restores the grant', () => {
    let state = admittedOwnerScene()
    state = dispatchScopedSnapshot(state, ownerIdentity, {
      deviceName: 'pulse/other-sink',
      stateSequence: 11,
    })
    expect(state.jukeboxControl.sceneConflicted).toBe(true)

    // The server binding is genuinely consistent with the retained scene A:
    // the conflict clears and writes are conferred again.
    const recovered = dispatchScopedSnapshot(state, ownerIdentity, {
      deviceName: 'pulse/test',
      stateSequence: 12,
      position: 30,
    })
    expect(recovered.jukeboxControl.sceneConflicted).toBe(false)
    expect(recovered.jukeboxControl.controlGranted).toBe(true)
    expect(recovered.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(recovered.jukeboxRemote.stateSequence).toBe(12)
    expect(recovered.jukeboxRemote.position).toBe(30)
    expect(ownerScope(recovered)).not.toBeNull()
  })

  it('a known-A snapshot answered by a higher generation B arms one bounded B reconciliation', () => {
    let state = admittedOwnerScene()
    state = dispatchScopedSnapshot(state, ownerIdentity, {
      bindingGeneration: 3,
      stateSequence: 1,
      deviceName: 'pulse/gen3',
    })

    // Legitimate generation advance: the shared B facts are admitted, but A
    // provenance confers no writes; the authority arms exactly one bounded
    // reconciliation against current B.
    expect(state.jukeboxRemote.deviceName).toBe('pulse/gen3')
    expect(state.jukeboxControl.controlGranted).toBe(false)
    expect(state.jukeboxControl.bindingReconcile).toEqual({
      bindingGeneration: 3,
      deviceName: 'pulse/gen3',
    })
    expect(ownerScope(state)).toBeNull()

    // The ONE follow-up GET starts against B: revision and binding captured
    // at its own initiation (the fixture meta mirrors that capture).
    const reconciled = dispatchScopedSnapshot(state, ownerIdentity, {
      bindingGeneration: 3,
      stateSequence: 2,
      deviceName: 'pulse/gen3',
    })
    expect(reconciled.jukeboxControl.controlGranted).toBe(true)
    expect(reconciled.jukeboxControl.bindingReconcile).toBeNull()
    expect(reconciled.jukeboxRemote.deviceName).toBe('pulse/gen3')
    expect(ownerScope(reconciled)).not.toBeNull()
  })

  it('a missing-device follow-up consumes the reconciliation and stays readonly', () => {
    let state = admittedOwnerScene()
    state = dispatchScopedSnapshot(state, ownerIdentity, {
      bindingGeneration: 3,
      stateSequence: 1,
      deviceName: 'pulse/gen3',
    })
    expect(state.jukeboxControl.bindingReconcile).not.toBeNull()

    // The bounded follow-up answers without a canonical device again: the
    // reconciliation is consumed (never re-armed on the same target), so the
    // authority stays readonly instead of storming the snapshot endpoint.
    const stuck = dispatchScopedSnapshot(state, ownerIdentity, {
      bindingGeneration: 3,
      stateSequence: 2,
      deviceName: null,
    })
    expect(stuck.jukeboxControl.controlGranted).toBe(false)
    expect(stuck.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(stuck)).toBeNull()
  })

  it('a first-baseline snapshot without a canonical device cannot confer writes', () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginIdentity(state, ownerIdentity)
    // Ordered, attached, live provenance — but no canonical device: the
    // scene is observable while the binding stays unknown.
    const next = dispatchScopedSnapshot(state, ownerIdentity, {
      deviceName: null,
    })

    expect(next.jukeboxRemote.trackId).toBe('song-a')
    expect(next.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(ownerScope(next)).toBeNull()
  })

  it('a higher generation without a device never reuses the previous binding device', () => {
    const baseline = admittedOwnerScene()
    // The server rebound to a new generation but the response carries no
    // device: the old binding device must not be carried over.
    const next = dispatchScopedSnapshot(baseline, ownerIdentity, {
      bindingGeneration: 3,
      stateSequence: 0,
      deviceName: null,
    })

    expect(next.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(ownerScope(next)).toBeNull()
  })

  it('a same-device trusted advance keeps the binding and the grant', () => {
    const baseline = admittedOwnerScene()
    // Ordinary state advance on the SAME device binding: the scene updates
    // and writes remain conferred (device equality preserves the binding).
    const next = dispatchScopedSnapshot(baseline, ownerIdentity, {
      stateSequence: 11,
      position: 20,
    })

    expect(next.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(next.jukeboxRemote.stateSequence).toBe(11)
    expect(next.jukeboxRemote.position).toBe(20)
    expect(next.jukeboxControl.controlGranted).toBe(true)
    expect(ownerScope(next)).not.toBeNull()
  })

  it('a raw equal-order device contradiction never rebinds the canonical device or drops eligibility', () => {
    const baseline = admittedOwnerScene()
    const next = playerReducer(baseline, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({
        deviceName: 'pulse/other-sink',
        position: 13,
        lastHeartbeat: '2026-11-02T00:05:00Z',
      }),
    })

    // Untrusted observations may still carry harmless position/heartbeat
    // metadata, but the canonical device is binding identity: raw provenance
    // never establishes or rebinds it, and the owner's eligibility on the
    // unchanged binding survives.
    expect(next.jukeboxRemote.position).toBe(13)
    expect(next.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(next.jukeboxControl.controlGranted).toBe(true)
    expect(ownerScope(next)).not.toBeNull()
  })

  it('a raw higher-sequence device contradiction raises the floor without rebinding the device', () => {
    const baseline = admittedOwnerScene()
    const next = playerReducer(baseline, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus({
        deviceName: 'pulse/other-sink',
        stateSequence: 11,
      }),
    })

    // The floor advances and writes are suspended, but the retained scene
    // keeps the admitted device — no raw rebind, no silent identity move.
    expect(next.jukeboxRemote.stateSequence).toBe(11)
    expect(next.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(next.jukeboxRemote.trackId).toBe('song-a')
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(ownerScope(next)).toBeNull()
  })
})

// R12 persistent expected-contract regressions (parent-flagged whole-candidate
// blocker, audit-caller-scope-20261103T035213Z/r2-probe.log): a missing
// device — OMITTED from the payload, not merely explicit null — is read-only.
// It must never inherit a prior-scene or raw-observation-seeded device to
// reconcile against it or to confer caller writes, at the same or a newer
// binding generation. Raw observations may raise the floor or invalidate but
// never grant; only an explicit current valid device from a fresh live
// trusted episode recovers the grant.
describe('playerReducer missing-device read-only contract (R12)', () => {
  const enableJukeboxMode = (state) =>
    playerReducer(state, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: true, device: 'pulse/test' },
    })

  const ownerIdentity = {
    token: 'owner-token',
    sessionId: 'jukebox-session:admin',
    clientId: 'tab-a',
  }
  // The fixture meta mirrors production captureRequestBinding: revision and
  // the binding the shared authority had admitted when the GET started.
  const scopedMeta = (state, overrides = {}) => ({
    source: 'atomic-session-snapshot',
    sessionId: ownerIdentity.sessionId,
    clientId: ownerIdentity.clientId,
    token: ownerIdentity.token,
    revision: state.jukeboxEpisodes?.[ownerIdentity.token]?.revision ?? 0,
    requestBinding: {
      bindingGeneration: state.jukeboxRemote?.bindingGeneration ?? null,
      deviceName: state.jukeboxRemote?.deviceName ?? null,
    },
    ...overrides,
  })
  const dispatchScopedSnapshot = (state, overrides = {}, metaOverrides = {}) =>
    playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus(overrides),
      meta: scopedMeta(state, metaOverrides),
    })
  const beginOwner = (state) =>
    playerReducer(state, beginJukeboxEpisode(ownerIdentity))
  const ownerScope = (state) =>
    captureEligibleJukeboxScope(state, ownerIdentity)

  // Live owner episode: baseline scene admitted through the reducer lifecycle
  // with device pulse/test granted at generation 2.
  const admittedOwnerScene = () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginOwner(state)
    return dispatchScopedSnapshot(state)
  }

  // Raw attach/status-shaped observation: raises the floor only, never grants.
  const rawObservation = (state, overrides = {}) =>
    playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: strictRemoteStatus(overrides),
    })

  it('a raw floor-establishing observation cannot seed the canonical device identity', () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginOwner(state)
    // A raw observation may raise the floor, but deviceName is binding
    // identity: raw provenance must never establish it, so there is nothing
    // for a later trusted omission to inherit.
    state = rawObservation(state, { deviceName: 'pulse/raw-seeded' })
    expect(state.jukeboxRemote.stateSequence).toBe(10)
    expect(state.jukeboxRemote.bindingGeneration).toBe(2)
    expect(state.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(state.jukeboxControl.controlGranted).toBe(false)

    // The first trusted baseline naming the real device still confers: the
    // missing-device readonly rule must not require prior device knowledge.
    const baseline = dispatchScopedSnapshot(state)
    expect(baseline.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(baseline.jukeboxControl.controlGranted).toBe(true)
    expect(ownerScope(baseline)).not.toBeNull()
  })

  it('an explicit-null trusted baseline after a raw floor seed does not inherit the raw device', () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginOwner(state)
    state = rawObservation(state, { deviceName: 'pulse/raw-seeded' })

    const baseline = dispatchScopedSnapshot(state, { deviceName: null })

    expect(baseline.jukeboxRemote.trackId).toBe('song-a')
    expect(baseline.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(baseline.jukeboxControl.controlGranted).toBe(false)
    expect(baseline.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(baseline)).toBeNull()
  })

  it('an explicit-null higher-generation snapshot after a raw floor seed does not carry the raw device', () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginOwner(state)
    state = rawObservation(state, { deviceName: 'pulse/raw-seeded' })

    const next = dispatchScopedSnapshot(state, {
      bindingGeneration: 3,
      stateSequence: 0,
      deviceName: null,
    })

    expect(next.jukeboxRemote.bindingGeneration).toBe(3)
    expect(next.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(next.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(next)).toBeNull()
  })

  it('a trusted baseline omitting the device after a raw floor seed is readonly and never inherits the raw device', () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginOwner(state)
    // No trusted scene yet: a raw observation with ordered fields establishes
    // the floor. It may raise the floor but must never confer control.
    state = rawObservation(state, { deviceName: 'pulse/raw-seeded' })
    expect(state.jukeboxRemote.stateSequence).toBe(10)
    expect(state.jukeboxRemote.bindingGeneration).toBe(2)
    expect(state.jukeboxControl.controlGranted).toBe(false)

    // First trusted atomic baseline OMITS the device field: missing device is
    // read-only — it must not inherit the raw-seeded device to reconcile or
    // grant caller writes.
    const baseline = dispatchScopedSnapshot(state, { deviceName: undefined })

    expect(baseline.jukeboxRemote.trackId).toBe('song-a')
    expect(baseline.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(baseline.jukeboxControl.controlGranted).toBe(false)
    expect(baseline.jukeboxControl.sceneConflicted).toBe(false)
    expect(baseline.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(baseline)).toBeNull()
  })

  it('a same-generation trusted advance omitting the device clears the canonical device and suspends without conflict or reconcile', () => {
    const baseline = admittedOwnerScene()
    expect(baseline.jukeboxControl.controlGranted).toBe(true)

    const next = dispatchScopedSnapshot(baseline, {
      stateSequence: 11,
      position: 20,
      deviceName: undefined,
    })

    expect(next.jukeboxRemote.stateSequence).toBe(11)
    expect(next.jukeboxRemote.position).toBe(20)
    expect(next.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(next.jukeboxControl.sceneConflicted).toBe(false)
    expect(next.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(next)).toBeNull()
  })

  it('a same-generation explicit-null advance matches the omitted-device read-only contract', () => {
    const baseline = admittedOwnerScene()

    const next = dispatchScopedSnapshot(baseline, {
      stateSequence: 11,
      deviceName: null,
    })

    expect(next.jukeboxRemote.stateSequence).toBe(11)
    expect(next.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(next.jukeboxControl.sceneConflicted).toBe(false)
    expect(next.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(next)).toBeNull()
  })

  it('a higher-generation trusted snapshot omitting the device never carries the raw-seeded device nor arms a reconciliation', () => {
    let state = enableJukeboxMode(playerReducer(undefined, { type: '@@INIT' }))
    state = beginOwner(state)
    state = rawObservation(state, { deviceName: 'pulse/raw-seeded' })
    expect(state.jukeboxControl.controlGranted).toBe(false)

    const next = dispatchScopedSnapshot(state, {
      bindingGeneration: 3,
      stateSequence: 0,
      deviceName: undefined,
    })

    expect(next.jukeboxRemote.bindingGeneration).toBe(3)
    expect(next.jukeboxRemote.stateSequence).toBe(0)
    expect(next.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(next.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(next)).toBeNull()

    // A repeated omitted-device answer at the new generation cannot re-arm.
    const repeat = dispatchScopedSnapshot(next, {
      bindingGeneration: 3,
      stateSequence: 1,
      deviceName: undefined,
    })
    expect(repeat.jukeboxControl.bindingReconcile).toBeNull()
    expect(repeat.jukeboxControl.controlGranted).toBe(false)
  })

  it('a higher-generation trusted snapshot omitting the device after a trusted scene never reuses the prior device', () => {
    const baseline = admittedOwnerScene()
    expect(baseline.jukeboxRemote.deviceName).toBe('pulse/test')

    const next = dispatchScopedSnapshot(baseline, {
      bindingGeneration: 3,
      stateSequence: 0,
      deviceName: undefined,
    })

    expect(next.jukeboxRemote.bindingGeneration).toBe(3)
    expect(next.jukeboxRemote.deviceName ?? null).toBeNull()
    expect(next.jukeboxControl.controlGranted).toBe(false)
    expect(next.jukeboxControl.bindingReconcile).toBeNull()
    expect(ownerScope(next)).toBeNull()
  })

  it('an explicit current valid device from the live episode recovers the grant after a missing-device admission', () => {
    let state = admittedOwnerScene()
    state = dispatchScopedSnapshot(state, {
      stateSequence: 11,
      deviceName: undefined,
    })
    expect(state.jukeboxControl.controlGranted).toBe(false)
    expect(state.jukeboxRemote.deviceName ?? null).toBeNull()

    const recovered = dispatchScopedSnapshot(state, {
      stateSequence: 12,
      deviceName: 'pulse/test',
    })

    expect(recovered.jukeboxRemote.deviceName).toBe('pulse/test')
    expect(recovered.jukeboxRemote.stateSequence).toBe(12)
    expect(recovered.jukeboxControl.controlGranted).toBe(true)
    expect(ownerScope(recovered)).not.toBeNull()
  })

  it('an explicit valid device at a new generation recovers the grant after a missing-device rebinding', () => {
    let state = admittedOwnerScene()
    state = dispatchScopedSnapshot(state, {
      bindingGeneration: 3,
      stateSequence: 0,
      deviceName: undefined,
    })
    expect(state.jukeboxControl.controlGranted).toBe(false)
    expect(state.jukeboxRemote.deviceName ?? null).toBeNull()

    const recovered = dispatchScopedSnapshot(state, {
      bindingGeneration: 3,
      stateSequence: 1,
      deviceName: 'pulse/gen3',
    })

    expect(recovered.jukeboxRemote.deviceName).toBe('pulse/gen3')
    expect(recovered.jukeboxControl.controlGranted).toBe(true)
    expect(ownerScope(recovered)).not.toBeNull()
  })
})

describe('playerReducer jukebox session state', () => {
  it('stores jukebox control and remote state separately', () => {
    const next = playerReducer(undefined, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'attached',
        currentIndex: 2,
        trackId: 't3',
        position: 41,
        playing: true,
      },
    })

    expect(next.jukeboxControl.sessionId).toBe('s1')
    expect(next.jukeboxControl.ownerClientId).toBe('tab-1')
    expect(next.jukeboxControl.ownershipState).toBe('attached')
    expect(next.jukeboxControl).not.toHaveProperty('attached')
    expect(next.jukeboxControl).not.toHaveProperty('deviceName')
    expect(next.jukeboxRemote.currentIndex).toBe(2)
    expect(next.jukeboxRemote.trackId).toBe('t3')
    expect(next).not.toHaveProperty('jukeboxSession')
    expect(next).not.toHaveProperty('jukeboxStatus')
  })

  it('syncs volume from session gain in jukebox mode', () => {
    const state = playerReducer(undefined, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: true, device: 'pulse/test' },
    })

    const next = playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        gain: 0.81,
      },
    })

    expect(next.volume).toBe(audioVolumeToUiVolume(0.81))
  })

  it('does not overwrite volume when not in jukebox mode', () => {
    const initial = {
      ...playerReducer(undefined, { type: '@@INIT' }),
      volume: 0.33,
      jukeboxMode: false,
    }

    const next = playerReducer(initial, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        gain: 0.81,
      },
    })

    expect(next.volume).toBe(0.33)
  })

  it('maps Bluetooth remote gain back to UI volume with the Bluetooth profile', () => {
    const state = playerReducer(undefined, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: true, device: 'Bluetooth 24:C4:06:FA:00:37' },
    })

    const next = playerReducer(state, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
        gain: 0.857917,
      },
    })

    expect(next.volume).toBeCloseTo(
      remoteGainToUiVolume(0.857917, {
        deviceName: 'pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink',
      }),
      5,
    )
  })

  it('clears jukebox control and remote state when jukebox mode is disabled', () => {
    const initial = {
      ...playerReducer(undefined, { type: '@@INIT' }),
      jukeboxMode: true,
      jukeboxControl: { sessionId: 's1', ownershipState: 'attached' },
      jukeboxRemote: { currentIndex: 2, trackId: 't3' },
    }

    const next = playerReducer(initial, {
      type: PLAYER_SET_JUKEBOX_MODE,
      data: { enabled: false, device: null },
    })

    expect(next.jukeboxMode).toBe(false)
    expect(next.jukeboxControl).toBeNull()
    expect(next.jukeboxRemote).toBeNull()
  })

  it('preserves remote playback state while control is recovering', () => {
    const next = playerReducer(undefined, {
      type: PLAYER_JUKEBOX_SESSION_STATUS,
      data: {
        sessionId: 's1',
        ownerClientId: 'tab-1',
        ownershipState: 'recovering',
        currentIndex: 1,
        trackId: 't2',
        playing: true,
        position: 33,
      },
    })

    expect(next.jukeboxControl.ownershipState).toBe('recovering')
    expect(next.jukeboxRemote.currentIndex).toBe(1)
    expect(next.jukeboxRemote.trackId).toBe('t2')
  })
})
