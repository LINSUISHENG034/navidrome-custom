import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import { useMediaQuery } from '@material-ui/core'
import { ThemeProvider } from '@material-ui/core/styles'
import {
  createMuiTheme,
  useAuthState,
  useDataProvider,
  useTranslate,
} from 'react-admin'
import ReactGA from 'react-ga'
import { GlobalHotKeys } from 'react-hotkeys'
import ReactJkMusicPlayer from 'navidrome-music-player'
import 'navidrome-music-player/assets/index.css'
import useCurrentTheme from '../themes/useCurrentTheme'
import config from '../config'
import useStyle from './styles'
import AudioTitle from './AudioTitle'
import {
  clearQueue,
  currentPlaying,
  setPlayMode,
  setVolume,
  syncQueue,
  setAudioInstance as setAudioInstanceAction,
  updateJukeboxStatus,
} from '../actions'
import PlayerToolbar from './PlayerToolbar'
import { sendNotification } from '../utils'
import { baseUrl } from '../utils/urls'
import subsonic from '../subsonic'
import locale from './locale'
import { keyMap } from '../hotkeys'
import keyHandlers from './keyHandlers'
import { calculateGain } from '../utils/calculateReplayGain'
import jukeboxClient from './jukeboxClient'
import { enqueueJukeboxCommand } from './jukeboxCommandQueue'
import {
  computeQueueDiff,
  syncJukeboxQueueIncremental,
  syncJukeboxSeek,
  syncJukeboxTrackChange,
} from './jukeboxSync'

const Player = () => {
  const theme = useCurrentTheme()
  const translate = useTranslate()
  const playerTheme = theme.player?.theme || 'dark'
  const dataProvider = useDataProvider()
  const playerState = useSelector((state) => state.player)
  const dispatch = useDispatch()
  const [startTime, setStartTime] = useState(null)
  const [scrobbled, setScrobbled] = useState(false)
  const [preloaded, setPreload] = useState(false)
  const [audioInstance, setAudioInstanceLocal] = useState(null)
  const prevQueueRef = useRef([])

  const handleAudioInstance = useCallback(
    (instance) => {
      setAudioInstanceLocal(instance)
      dispatch(setAudioInstanceAction(instance))
    },
    [dispatch],
  )
  const isDesktop = useMediaQuery('(min-width:810px)')
  const isMobilePlayer =
    /Android|webOS|iPhone|iPad|iPod|BlackBerry|IEMobile|Opera Mini/i.test(
      navigator.userAgent,
    )

  const { authenticated } = useAuthState()
  const visible = authenticated && playerState.queue.length > 0
  const isRadio = playerState.current?.isRadio || false
  const classes = useStyle({
    isRadio,
    visible,
    enableCoverAnimation: config.enableCoverAnimation,
  })
  const showNotifications = useSelector(
    (state) => state.settings.notifications || false,
  )
  const gainInfo = useSelector((state) => state.replayGain)
  const [context, setContext] = useState(null)
  const [gainNode, setGainNode] = useState(null)

  useEffect(() => {
    if (
      context === null &&
      audioInstance &&
      config.enableReplayGain &&
      'AudioContext' in window &&
      (gainInfo.gainMode === 'album' || gainInfo.gainMode === 'track')
    ) {
      const ctx = new AudioContext()
      // we need this to support radios in firefox
      audioInstance.crossOrigin = 'anonymous'
      const source = ctx.createMediaElementSource(audioInstance)
      const gain = ctx.createGain()

      source.connect(gain)
      gain.connect(ctx.destination)

      setContext(ctx)
      setGainNode(gain)
    }
  }, [audioInstance, context, gainInfo.gainMode])

  useEffect(() => {
    if (gainNode) {
      if (playerState.jukeboxMode) {
        // Silence AudioContext output in jukebox mode to prevent dual audio
        gainNode.gain.setValueAtTime(0, context.currentTime)
      } else {
        const current = playerState.current || {}
        const song = current.song || {}
        const numericGain = calculateGain(gainInfo, song)
        gainNode.gain.setValueAtTime(numericGain, context.currentTime)
      }
    }
  }, [audioInstance, context, gainNode, playerState, gainInfo])

  useEffect(() => {
    const handleBeforeUnload = (e) => {
      let isPlaying
      if (playerState.jukeboxMode) {
        // In Jukebox mode, audio element is muted (not paused),
        // so check for an active track instead
        isPlaying = !!playerState.current?.uuid
      } else {
        isPlaying = !!(
          playerState.current?.uuid &&
          audioInstance &&
          !audioInstance.paused
        )
      }
      if (isPlaying) {
        e.preventDefault()
        e.returnValue = ''
      }
    }

    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [playerState, audioInstance])

  const defaultOptions = useMemo(
    () => ({
      theme: playerTheme,
      bounds: 'body',
      playMode: playerState.mode,
      mode: 'full',
      loadAudioErrorPlayNext: false,
      autoPlayInitLoadPlayList: true,
      clearPriorAudioLists: false,
      showDestroy: true,
      showDownload: false,
      showLyric: true,
      showReload: false,
      toggleMode: !isDesktop,
      glassBg: false,
      showThemeSwitch: false,
      showMediaSession: true,
      restartCurrentOnPrev: true,
      quietUpdate: true,
      defaultPosition: {
        top: 300,
        left: 120,
      },
      volumeFade: { fadeIn: 200, fadeOut: 200 },
      renderAudioTitle: (audioInfo, isMobile) => (
        <AudioTitle
          audioInfo={audioInfo}
          gainInfo={gainInfo}
          isMobile={isMobile}
        />
      ),
      locale: locale(translate),
      sortableOptions: { delay: 200, delayOnTouchOnly: true },
    }),
    [gainInfo, isDesktop, playerTheme, translate, playerState.mode],
  )

  const options = useMemo(() => {
    const current = playerState.current || {}
    return {
      ...defaultOptions,
      audioLists: playerState.queue.map((item) => item),
      playIndex: playerState.playIndex,
      autoPlay: playerState.clear || playerState.playIndex === 0,
      clearPriorAudioLists: playerState.clear,
      extendsContent: (
        <PlayerToolbar id={current.trackId} isRadio={current.isRadio} />
      ),
      defaultVolume: isMobilePlayer ? 1 : playerState.volume,
      showMediaSession: !current.isRadio,
    }
  }, [playerState, defaultOptions, isMobilePlayer])

  const onAudioListsChange = useCallback(
    (_, audioLists, audioInfo) => {
      dispatch(syncQueue(audioInfo, audioLists))
      if (playerState.jukeboxMode) {
        const diff = computeQueueDiff(prevQueueRef.current, audioLists)
        enqueueJukeboxCommand(() =>
          syncJukeboxQueueIncremental(jukeboxClient, diff),
        ).catch(() => {})
      }
      prevQueueRef.current = audioLists
    },
    [dispatch, playerState.jukeboxMode],
  )

  const nextSong = useCallback(() => {
    const idx = playerState.queue.findIndex(
      (item) => item.uuid === playerState.current.uuid,
    )
    return idx !== null ? playerState.queue[idx + 1] : null
  }, [playerState])

  const onAudioProgress = useCallback(
    (info) => {
      if (info.ended) {
        document.title = 'Navidrome'
      }

      const progress = (info.currentTime / info.duration) * 100
      if (isNaN(info.duration) || (progress < 50 && info.currentTime < 240)) {
        return
      }

      if (info.isRadio) {
        return
      }

      if (!preloaded) {
        const next = nextSong()
        if (next != null) {
          const audio = new Audio()
          audio.src = next.musicSrc
        }
        setPreload(true)
        return
      }

      if (!scrobbled) {
        info.trackId && subsonic.scrobble(info.trackId, startTime)
        setScrobbled(true)
      }
    },
    [startTime, scrobbled, nextSong, preloaded],
  )

  const onAudioVolumeChange = useCallback(
    // sqrt to compensate for the logarithmic volume
    (volume) => {
      dispatch(setVolume(Math.sqrt(volume)))
      if (playerState.jukeboxMode) {
        enqueueJukeboxCommand(() => jukeboxClient.volume(volume)).catch(
          () => {},
        )
      }
    },
    [dispatch, playerState.jukeboxMode],
  )

  const onAudioPlay = useCallback(
    (info) => {
      if (playerState.jukeboxMode) {
        if (audioInstance) audioInstance.muted = true
        // Only forward play to Jukebox when the tab is visible.
        // When the tab becomes visible, the browser may auto-resume audio —
        // this is NOT a user-initiated play and must not restart the Jukebox.
        if (!document.hidden) {
          enqueueJukeboxCommand(() => jukeboxClient.play()).catch(() => {})
        }
      }

      // Do this to start the context; on chrome-based browsers, the context
      // will start paused since it is created prior to user interaction
      if (context && context.state !== 'running') {
        context.resume()
      }

      dispatch(currentPlaying(info))
      if (startTime === null) {
        setStartTime(Date.now())
      }
      if (info.duration) {
        const song = info.song
        document.title = `${song.title} - ${song.artist} - Navidrome`
        if (!info.isRadio) {
          const pos = startTime === null ? null : Math.floor(info.currentTime)
          subsonic.nowPlaying(info.trackId, pos)
        }
        setPreload(false)
        if (config.gaTrackingId) {
          ReactGA.event({
            category: 'Player',
            action: 'Play song',
            label: `${song.title} - ${song.artist}`,
          })
        }
        if (showNotifications) {
          sendNotification(
            song.title,
            `${song.artist} - ${song.album}`,
            info.cover,
          )
        }
      }
    },
    [
      audioInstance,
      context,
      dispatch,
      playerState.jukeboxMode,
      showNotifications,
      startTime,
    ],
  )

  const onAudioPlayTrackChange = useCallback(
    (playId, audioLists, info) => {
      if (scrobbled) {
        setScrobbled(false)
      }
      if (startTime !== null) {
        setStartTime(null)
      }
      if (playerState.jukeboxMode) {
        enqueueJukeboxCommand(() =>
          syncJukeboxTrackChange(jukeboxClient, {
            audioLists,
            playId,
            audioInfo: info,
          }),
        ).catch(() => {})
      }
    },
    [playerState.jukeboxMode, scrobbled, startTime],
  )

  const onAudioPause = useCallback(
    (info) => {
      dispatch(currentPlaying(info))
      // Only forward pause to Jukebox when the tab is visible.
      // When hidden, the browser may auto-pause muted audio for power saving —
      // this is NOT a user-initiated pause and must not stop the Jukebox.
      if (playerState.jukeboxMode && !document.hidden) {
        enqueueJukeboxCommand(() => jukeboxClient.pause()).catch(() => {})
      }
    },
    [dispatch, playerState.jukeboxMode],
  )

  const onAudioSeeked = useCallback(
    (info) => {
      if (playerState.jukeboxMode) {
        enqueueJukeboxCommand(() => syncJukeboxSeek(jukeboxClient, info)).catch(
          () => {},
        )
      }
    },
    [playerState.jukeboxMode],
  )

  const onAudioEnded = useCallback(
    (currentPlayId, audioLists, info) => {
      setScrobbled(false)
      setStartTime(null)
      dispatch(currentPlaying(info))
      dataProvider
        .getOne('keepalive', { id: info.trackId })
        // eslint-disable-next-line no-console
        .catch((e) => console.log('Keepalive error:', e))
    },
    [dispatch, dataProvider],
  )

  const onCoverClick = useCallback((mode, audioLists, audioInfo) => {
    if (mode === 'full' && audioInfo?.song?.albumId) {
      window.location.href = `#/album/${audioInfo.song.albumId}/show`
    }
  }, [])

  const onBeforeDestroy = useCallback(() => {
    return new Promise((resolve, reject) => {
      dispatch(clearQueue())
      reject()
    })
  }, [dispatch])

  if (!visible) {
    document.title = 'Navidrome'
  }

  const handlers = useMemo(
    () => keyHandlers(audioInstance, playerState),
    [audioInstance, playerState],
  )

  useEffect(() => {
    if (isMobilePlayer && audioInstance) {
      audioInstance.volume = 1
    }
  }, [isMobilePlayer, audioInstance])

  // Jukebox status polling — poll every 2s when in Jukebox mode
  useEffect(() => {
    if (!playerState.jukeboxMode) return
    const poll = () => {
      jukeboxClient
        .status()
        .then((status) => dispatch(updateJukeboxStatus(status)))
        .catch(() => {})
    }
    poll()
    const interval = setInterval(poll, 2000)
    return () => clearInterval(interval)
  }, [playerState.jukeboxMode, dispatch])

  // In Jukebox mode, mute browser audio while server plays.
  useEffect(() => {
    if (!audioInstance) return
    audioInstance.muted = !!playerState.jukeboxMode
  }, [playerState.jukeboxMode, audioInstance])

  // Stop Jukebox playback when the browser window/tab is closed.
  // Uses fetch with keepalive to ensure the request survives page unload.
  useEffect(() => {
    const handlePageHide = () => {
      if (!playerState.jukeboxMode) return
      const token = localStorage.getItem('token')
      if (!token) return
      fetch(baseUrl('/api/jukebox/pause'), {
        method: 'POST',
        headers: {
          'X-ND-Authorization': `Bearer ${token}`,
          'Content-Type': 'application/json',
        },
        keepalive: true,
      }).catch(() => {})
    }
    window.addEventListener('pagehide', handlePageHide)
    return () => window.removeEventListener('pagehide', handlePageHide)
  }, [playerState.jukeboxMode])

  return (
    <ThemeProvider theme={createMuiTheme(theme)}>
      <ReactJkMusicPlayer
        {...options}
        className={classes.player}
        onAudioListsChange={onAudioListsChange}
        onAudioVolumeChange={onAudioVolumeChange}
        onAudioProgress={onAudioProgress}
        onAudioSeeked={onAudioSeeked}
        onAudioPlay={onAudioPlay}
        onAudioPlayTrackChange={onAudioPlayTrackChange}
        onAudioPause={onAudioPause}
        onPlayModeChange={(mode) => dispatch(setPlayMode(mode))}
        onAudioEnded={onAudioEnded}
        onCoverClick={onCoverClick}
        onBeforeDestroy={onBeforeDestroy}
        getAudioInstance={handleAudioInstance}
      />
      <GlobalHotKeys handlers={handlers} keyMap={keyMap} allowChanges />
    </ThemeProvider>
  )
}

export { Player }
