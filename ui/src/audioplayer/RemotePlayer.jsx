import React from 'react'
import { remoteGainToUiVolume, uiVolumeToRemoteGain } from './volumeProfiles'

const RemotePlayer = ({ snapshot, onCommand }) => {
  const sessionStatus = snapshot?.sessionStatus || {}
  const playlist = snapshot?.jukeboxPlaylist || {}
  const queue = playlist.entry || []
  const currentIndex = sessionStatus.currentIndex
  const hasCurrentTrack =
    Number.isInteger(currentIndex) &&
    currentIndex >= 0 &&
    currentIndex < queue.length
  const currentTrack = hasCurrentTrack ? queue[currentIndex] : null
  const position = sessionStatus.position ?? 0
  const gain = sessionStatus.gain ?? 0
  const playing = sessionStatus.playing ?? false

  return (
    <section aria-label="Remote player">
      <h2>Remote player</h2>
      <p>State comes from server snapshots.</p>
      <p>{playing ? 'Playing' : 'Paused'}</p>
      <p>Device: {sessionStatus.deviceName || 'Unknown'}</p>
      <p>Ownership: {sessionStatus.ownershipState || 'Unknown'}</p>

      <h3>{currentTrack?.title || 'Nothing playing'}</h3>
      {currentTrack && (
        <p>
          {currentTrack.artist} — {currentTrack.album}
        </p>
      )}

      <div>Position: {position} seconds</div>
      <div>Gain: {gain}</div>
      <label>
        Seek
        <input
          type="range"
          aria-label="Seek"
          min="0"
          max={currentTrack?.duration || 0}
          value={position}
          disabled={!hasCurrentTrack}
          onChange={(event) =>
            onCommand({ type: 'seek', position: Number(event.target.value) })
          }
        />
      </label>
      <label>
        Volume
        <input
          type="range"
          aria-label="Volume"
          min="0"
          max="1"
          step="0.01"
          value={remoteGainToUiVolume(gain, sessionStatus)}
          onChange={(event) =>
            onCommand({
              type: 'volume',
              gain: uiVolumeToRemoteGain(
                Number(event.target.value),
                sessionStatus,
              ),
            })
          }
        />
      </label>

      <div>
        <button
          type="button"
          disabled={!hasCurrentTrack || currentIndex <= 0}
          onClick={() =>
            onCommand({ type: 'skip', index: currentIndex - 1, offset: 0 })
          }
        >
          Previous
        </button>
        <button
          type="button"
          disabled={!hasCurrentTrack && !playing}
          onClick={() => onCommand({ type: playing ? 'pause' : 'play' })}
        >
          {playing ? 'Pause' : 'Play'}
        </button>
        <button
          type="button"
          disabled={!hasCurrentTrack || currentIndex >= queue.length - 1}
          onClick={() =>
            onCommand({ type: 'skip', index: currentIndex + 1, offset: 0 })
          }
        >
          Next
        </button>
      </div>

      <ol aria-label="Queue">
        {queue.map((track, index) => (
          <li
            key={`${track.id}-${index}`}
            aria-current={hasCurrentTrack && index === currentIndex}
          >
            {track.title} — {track.artist}
          </li>
        ))}
      </ol>
    </section>
  )
}

export default RemotePlayer
