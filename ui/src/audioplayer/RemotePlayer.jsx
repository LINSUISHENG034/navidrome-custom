import React, { useState } from 'react'
import { makeStyles } from '@material-ui/core/styles'
import { remoteGainToUiVolume, uiVolumeToRemoteGain } from './volumeProfiles'
import subsonic from '../subsonic'

const useStyles = makeStyles((theme) => ({
  playerDock: {
    position: 'fixed',
    bottom: 0,
    left: 0,
    right: 0,
    height: 76,
    backgroundColor: theme?.palette?.background?.paper || '#222',
    color: theme?.palette?.text?.primary || '#fff',
    boxShadow: '0 -2px 12px rgba(0,0,0,0.3)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '0 16px',
    zIndex: 1000,
    boxSizing: 'border-box',
    borderTop: `1px solid ${theme?.palette?.divider || 'rgba(255,255,255,0.12)'}`,
    userSelect: 'none',
  },
  visuallyHidden: {
    position: 'absolute',
    width: 1,
    height: 1,
    padding: 0,
    margin: -1,
    overflow: 'hidden',
    clip: 'rect(0, 0, 0, 0)',
    whiteSpace: 'nowrap',
    border: 0,
  },
  leftSection: {
    display: 'flex',
    alignItems: 'center',
    minWidth: 200,
    maxWidth: '30%',
    overflow: 'hidden',
    gap: 12,
  },
  coverContainer: {
    width: 48,
    height: 48,
    borderRadius: 4,
    overflow: 'hidden',
    flexShrink: 0,
    backgroundColor: theme?.palette?.action?.hover || '#333',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
  },
  coverImg: {
    width: '100%',
    height: '100%',
    objectFit: 'cover',
  },
  coverFallback: {
    fontSize: '1.2rem',
    opacity: 0.6,
  },
  trackDetails: {
    display: 'flex',
    flexDirection: 'column',
    overflow: 'hidden',
    minWidth: 0,
  },
  trackTitle: {
    margin: 0,
    fontSize: '0.9rem',
    fontWeight: 600,
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    color: theme?.palette?.text?.primary || '#fff',
  },
  trackArtist: {
    margin: 0,
    fontSize: '0.78rem',
    color: theme?.palette?.text?.secondary || 'rgba(255,255,255,0.7)',
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
  },
  remoteBadge: {
    fontSize: '0.68rem',
    fontWeight: 600,
    color: theme?.palette?.primary?.main || '#ff5722',
    letterSpacing: '0.5px',
    textTransform: 'uppercase',
  },
  centerSection: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    flexGrow: 1,
    maxWidth: 620,
    padding: '0 16px',
  },
  controlsRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 12,
    marginBottom: 4,
  },
  controlBtn: {
    background: 'transparent',
    border: 'none',
    color: theme?.palette?.text?.primary || '#fff',
    cursor: 'pointer',
    padding: '6px 12px',
    borderRadius: 4,
    fontSize: '0.85rem',
    fontWeight: 500,
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    transition: 'background-color 150ms ease',
    '&:disabled': {
      opacity: 0.3,
      cursor: 'not-allowed',
    },
    '&:hover:not(:disabled)': {
      backgroundColor: theme?.palette?.action?.hover || 'rgba(255,255,255,0.1)',
    },
  },
  playBtn: {
    background: theme?.palette?.primary?.main || '#ff5722',
    color: theme?.palette?.primary?.contrastText || '#fff',
    border: 'none',
    borderRadius: 20,
    padding: '6px 16px',
    cursor: 'pointer',
    fontSize: '0.85rem',
    fontWeight: 600,
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    transition: 'transform 100ms ease, background-color 150ms ease',
    '&:disabled': {
      opacity: 0.3,
      cursor: 'not-allowed',
    },
    '&:hover:not(:disabled)': {
      backgroundColor: theme?.palette?.primary?.dark || '#e64a19',
      transform: 'scale(1.03)',
    },
  },
  progressRow: {
    display: 'flex',
    alignItems: 'center',
    width: '100%',
    gap: 8,
    fontSize: '0.75rem',
    color: theme?.palette?.text?.secondary || 'rgba(255,255,255,0.7)',
  },
  seekSlider: {
    flexGrow: 1,
    cursor: 'pointer',
    accentColor: theme?.palette?.primary?.main || '#ff5722',
    height: 4,
  },
  rightSection: {
    display: 'flex',
    alignItems: 'center',
    gap: 12,
    minWidth: 200,
    justifyContent: 'flex-end',
  },
  volumeRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 6,
    fontSize: '0.8rem',
  },
  volumeSlider: {
    width: 80,
    cursor: 'pointer',
    accentColor: theme?.palette?.primary?.main || '#ff5722',
    height: 4,
  },
  queueToggleBtn: {
    background: 'transparent',
    border: `1px solid ${theme?.palette?.divider || 'rgba(255,255,255,0.2)'}`,
    color: theme?.palette?.text?.primary || '#fff',
    cursor: 'pointer',
    padding: '4px 10px',
    borderRadius: 4,
    fontSize: '0.75rem',
    transition: 'background-color 150ms ease',
    '&:hover': {
      backgroundColor: theme?.palette?.action?.hover || 'rgba(255,255,255,0.1)',
    },
  },
  queueDrawer: {
    position: 'fixed',
    bottom: 76,
    right: 16,
    width: 320,
    maxHeight: 380,
    overflowY: 'auto',
    backgroundColor: theme?.palette?.background?.paper || '#222',
    boxShadow: '0 -4px 20px rgba(0,0,0,0.5)',
    borderRadius: '8px 8px 0 0',
    padding: 8,
    margin: 0,
    listStyle: 'none',
    border: `1px solid ${theme?.palette?.divider || 'rgba(255,255,255,0.12)'}`,
    zIndex: 999,
  },
  queueItem: {
    padding: '8px 10px',
    cursor: 'pointer',
    borderRadius: 4,
    fontSize: '0.82rem',
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    color: theme?.palette?.text?.secondary || 'rgba(255,255,255,0.7)',
    transition: 'background-color 100ms ease',
    '&:hover': {
      backgroundColor: theme?.palette?.action?.hover || 'rgba(255,255,255,0.08)',
      color: theme?.palette?.text?.primary || '#fff',
    },
  },
  activeQueueItem: {
    padding: '8px 10px',
    cursor: 'pointer',
    borderRadius: 4,
    fontSize: '0.82rem',
    fontWeight: 600,
    color: theme?.palette?.primary?.main || '#ff5722',
    backgroundColor: theme?.palette?.action?.selected || 'rgba(255,255,255,0.12)',
  },
}))

const formatTime = (seconds) => {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00'
  const mins = Math.floor(seconds / 60)
  const secs = Math.floor(seconds % 60)
  return `${mins}:${secs < 10 ? '0' : ''}${secs}`
}

const RemotePlayer = ({ snapshot, onCommand }) => {
  const classes = useStyles()
  const [queueOpen, setQueueOpen] = useState(false)
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

  const coverUrl = currentTrack
    ? subsonic.getCoverArtUrl(currentTrack, 100, true)
    : null

  return (
    <section aria-label="Remote player" className={classes.playerDock}>
      {/* Accessible semantics for screen readers and automated test assertions */}
      <h2 className={classes.visuallyHidden}>Remote player</h2>
      <p className={classes.visuallyHidden}>State comes from server snapshots.</p>
      <p className={classes.visuallyHidden}>{playing ? 'Playing' : 'Paused'}</p>
      <p className={classes.visuallyHidden}>
        Device: {sessionStatus.deviceName || 'Unknown'}
      </p>
      <p className={classes.visuallyHidden}>
        Ownership: {sessionStatus.ownershipState || 'Unknown'}
      </p>
      <div className={classes.visuallyHidden}>
        Position: {position} seconds
      </div>
      <div className={classes.visuallyHidden}>Gain: {gain}</div>

      {/* Left: Album cover and track details */}
      <div className={classes.leftSection}>
        <div className={classes.coverContainer}>
          {coverUrl ? (
            <img
              src={coverUrl}
              alt={currentTrack?.title || 'Cover'}
              className={classes.coverImg}
              onError={(e) => {
                e.target.style.display = 'none'
              }}
            />
          ) : (
            <span className={classes.coverFallback}>🎵</span>
          )}
        </div>
        <div className={classes.trackDetails}>
          <h3 className={classes.trackTitle}>
            {currentTrack?.title || 'Nothing playing'}
          </h3>
          {currentTrack ? (
            <p className={classes.trackArtist}>
              {currentTrack.artist} — {currentTrack.album}
            </p>
          ) : (
            <span className={classes.trackArtist}>No track selected</span>
          )}
          <span className={classes.remoteBadge}>
            {sessionStatus.terminationReason === 'track_switch_failed'
              ? 'Error • Track switch failed'
              : sessionStatus.terminationReason === 'output_failed'
                ? 'Error • Playback output failed'
                : `Remote • ${sessionStatus.deviceName || 'Bluetooth'}`}
          </span>
        </div>
      </div>

      {/* Center: Controls and Seek Slider */}
      <div className={classes.centerSection}>
        <div className={classes.controlsRow}>
          <button
            type="button"
            className={classes.controlBtn}
            disabled={!hasCurrentTrack || currentIndex <= 0}
            onClick={() =>
              onCommand({ type: 'skip', index: currentIndex - 1, offset: 0 })
            }
          >
            Previous
          </button>
          <button
            type="button"
            className={classes.playBtn}
            disabled={!hasCurrentTrack && !playing}
            onClick={() => onCommand({ type: playing ? 'pause' : 'play' })}
          >
            {playing ? 'Pause' : 'Play'}
          </button>
          <button
            type="button"
            className={classes.controlBtn}
            disabled={!hasCurrentTrack || currentIndex >= queue.length - 1}
            onClick={() =>
              onCommand({ type: 'skip', index: currentIndex + 1, offset: 0 })
            }
          >
            Next
          </button>
        </div>
        <div className={classes.progressRow}>
          <span>{formatTime(position)}</span>
          <label className={classes.visuallyHidden} htmlFor="remote-seek-slider">
            Seek
          </label>
          <input
            id="remote-seek-slider"
            type="range"
            aria-label="Seek"
            className={classes.seekSlider}
            min="0"
            max={currentTrack?.duration || 0}
            value={position}
            disabled={!hasCurrentTrack}
            onChange={(event) =>
              onCommand({ type: 'seek', position: Number(event.target.value) })
            }
          />
          <span>{formatTime(currentTrack?.duration || 0)}</span>
        </div>
      </div>

      {/* Right: Volume & Queue Toggle */}
      <div className={classes.rightSection}>
        <div className={classes.volumeRow}>
          <span>🔊</span>
          <label
            className={classes.visuallyHidden}
            htmlFor="remote-volume-slider"
          >
            Volume
          </label>
          <input
            id="remote-volume-slider"
            type="range"
            aria-label="Volume"
            className={classes.volumeSlider}
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
        </div>
        <button
          type="button"
          className={classes.queueToggleBtn}
          onClick={() => setQueueOpen((open) => !open)}
        >
          Queue ({queue.length})
        </button>
      </div>

      {/* Floating or Accessible Queue list */}
      <ol
        aria-label="Queue"
        className={queueOpen ? classes.queueDrawer : classes.visuallyHidden}
      >
        {queue.map((track, index) => (
          <li
            key={`${track.id}-${index}`}
            className={
              hasCurrentTrack && index === currentIndex
                ? classes.activeQueueItem
                : classes.queueItem
            }
            aria-current={hasCurrentTrack && index === currentIndex}
            onClick={() => onCommand({ type: 'skip', index, offset: 0 })}
          >
            {track.title} — {track.artist}
          </li>
        ))}
      </ol>
    </section>
  )
}

export default RemotePlayer
