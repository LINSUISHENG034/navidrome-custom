# Jukebox Session Architecture Upgrade Notes

## Why this is deferred

The current Bluetooth/Jukebox bugfix intentionally follows a low-risk path:

- keep the existing Jukebox architecture
- optimize behavior consistency
- avoid introducing new session-expiry regressions

This means the current fix does **not** attempt to make Bluetooth playback fully equivalent to browser-local playback.

That deeper goal requires an architecture upgrade.

---

## Proposed Scheme C Direction

### 1. Move from default-device playback to session-owned playback

Current behavior is effectively tied to the current default playback device, not to an individual browser tab or user session.

Future direction:

- introduce a first-class Jukebox session id
- bind playback ownership to session id instead of only to default device
- track session metadata per browser tab/window
- allow explicit ownership transfer when switching devices

### 2. Separate playback intent from transport lifecycle

Today browser lifecycle events indirectly drive remote playback.

Future direction:

- model remote playback state independently from browser `<audio>` events
- use the browser audio element only as a local UI adapter when in Jukebox mode
- define explicit server-side commands for `pause`, `resume`, `stop`, `detach`, `heartbeat`, and `abandon`

### 3. Introduce server-tracked heartbeats only after ownership is explicit

Heartbeat-based cleanup is valuable, but only after session ownership is reliable.

Future direction:

- send periodic heartbeats tagged by session id
- expire only the owning session’s playback lease
- avoid killing playback just because a hidden tab was timer-throttled
- support grace periods and ownership renewal rules

### 4. Make remote status a first-class UI model

Future direction:

- represent Jukebox remote state independently from browser-local player state
- avoid using the browser audio element as the authoritative state machine in remote mode
- decouple UI controls from browser `play/pause` event quirks

### 5. Optionally support resumable detached playback

Future direction:

- decide whether remote playback should stop, pause, or continue after the controlling UI disappears
- make that a product-level policy, not an accidental consequence of browser unload behavior

---

## Why Scheme C is not part of the current fix

### Reason 1: It is larger than the reported bug scope

The reported issues are lifecycle and volume consistency bugs, not a request to redesign Jukebox ownership semantics.

### Reason 2: A partial session design would likely create new regressions

Adding a short-TTL heartbeat without explicit session ownership could:

- stop playback while the user only switched windows
- kill remote playback because the browser throttled background timers
- create conflicts between multiple tabs sharing the same device

### Reason 3: The current architecture still relies on local-player UI assumptions

The current web player library derives much of its state from the browser audio element. Replacing that assumption safely requires a broader UI/state-model refactor.

### Reason 4: This upgrade needs product decisions, not only bugfix code

Before implementing Scheme C, the project should decide:

- who owns a Jukebox session
- whether remote playback may outlive the tab
- how takeover between tabs/devices should work
- whether multiple concurrent controllers are allowed

---

## Recommended future project breakdown

1. Define Jukebox session ownership rules
2. Add backend session model and lease storage
3. Add explicit detach/abandon/heartbeat APIs
4. Refactor frontend remote-state management away from browser media events
5. Add multi-tab and background-throttling regression tests

