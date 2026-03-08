import React from 'react'
import { useSelector } from 'react-redux'
import { useMediaQuery } from '@material-ui/core'
import { Link } from 'react-router-dom'
import clsx from 'clsx'
import { QualityInfo } from '../common'
import useStyle from './styles'
import { useDrag } from 'react-dnd'
import { DraggableTypes } from '../consts'
import { selectEffectiveCurrentTrack } from '../selectors/playerSelectors'

const AudioTitle = React.memo(({ audioInfo, gainInfo, isMobile }) => {
  const effectiveCurrentTrack = useSelector(selectEffectiveCurrentTrack)
  const jukeboxMode = useSelector((state) => state.player?.jukeboxMode)
  const classes = useStyle()
  const className = classes.audioTitle
  const isDesktop = useMediaQuery('(min-width:810px)')

  const resolvedAudioInfo = jukeboxMode && effectiveCurrentTrack ? effectiveCurrentTrack : audioInfo
  const song = resolvedAudioInfo.song
  const [, dragSongRef] = useDrag(
    () => ({
      type: DraggableTypes.SONG,
      item: { ids: [song?.id] },
      options: { dropEffect: 'copy' },
    }),
    [song],
  )

  if (!song) {
    return ''
  }

  const qi = {
    suffix: song.suffix,
    bitRate: song.bitRate,
    rgAlbumGain: song.rgAlbumGain,
    rgAlbumPeak: song.rgAlbumPeak,
    rgTrackGain: song.rgTrackGain,
    rgTrackPeak: song.rgTrackPeak,
  }

  const subtitle = song.tags?.['subtitle']
  const title = song.title + (subtitle ? ` (${subtitle})` : '')

  const linkTo = resolvedAudioInfo.isRadio
    ? `/radio/${resolvedAudioInfo.trackId}/show`
    : song.playlistId
      ? `/playlist/${song.playlistId}/show`
      : `/album/${song.albumId}/show`

  return (
    <Link to={linkTo} className={className} ref={dragSongRef}>
      <span>
        <span className={clsx(classes.songTitle, 'songTitle')}>{title}</span>
        {isDesktop && (
          <QualityInfo
            record={qi}
            className={classes.qualityInfo}
            {...gainInfo}
          />
        )}
      </span>
      {isMobile ? (
        <>
          <span className={classes.songInfo}>
            <span className={'songArtist'}>{song.artist}</span>
          </span>
          <span className={clsx(classes.songInfo, classes.songAlbum)}>
            <span className={'songAlbum'}>{song.album}</span>
            {song.year ? ` - ${song.year}` : ''}
          </span>
        </>
      ) : (
        <span className={classes.songInfo}>
          <span className={'songArtist'}>{song.artist}</span> -{' '}
          <span className={'songAlbum'}>{song.album}</span>
          {song.year ? ` - ${song.year}` : ''}
        </span>
      )}
    </Link>
  )
})

AudioTitle.displayName = 'AudioTitle'

export default AudioTitle
