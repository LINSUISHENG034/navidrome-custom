import React, { useCallback } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import { useGetOne } from 'react-admin'
import { GlobalHotKeys } from 'react-hotkeys'
import IconButton from '@material-ui/core/IconButton'
import { useMediaQuery, Typography } from '@material-ui/core'
import { RiSaveLine } from 'react-icons/ri'
import BluetoothIcon from '@material-ui/icons/Bluetooth'
import { LoveButton, useToggleLove } from '../common'
import { openSaveQueueDialog } from '../actions'
import { keyMap } from '../hotkeys'
import { makeStyles } from '@material-ui/core/styles'
import DeviceSelector from './DeviceSelector'

const useStyles = makeStyles((theme) => ({
  toolbar: {
    display: 'flex',
    alignItems: 'center',
    flexGrow: 1,
    justifyContent: 'flex-end',
    gap: '0.5rem',
    listStyle: 'none',
    padding: 0,
    margin: 0,
  },
  mobileListItem: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    listStyle: 'none',
    padding: theme.spacing(0.5),
    margin: 0,
    height: 24,
  },
  button: {
    width: '2.5rem',
    height: '2.5rem',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 0,
  },
  mobileButton: {
    width: 24,
    height: 24,
    padding: 0,
    margin: 0,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    fontSize: '18px',
  },
  mobileIcon: {
    fontSize: '18px',
    display: 'flex',
    alignItems: 'center',
  },
  jukeboxIndicator: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.25rem',
    color: theme.palette.primary.main,
    whiteSpace: 'nowrap',
    overflow: 'hidden',
  },
  jukeboxIcon: {
    fontSize: '1rem',
  },
  jukeboxLabel: {
    fontSize: '0.75rem',
    maxWidth: 120,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
  },
}))

const PlayerToolbar = ({ id, isRadio }) => {
  const dispatch = useDispatch()
  const { data, loading } = useGetOne('song', id, { enabled: !!id && !isRadio })
  const [toggleLove, toggling] = useToggleLove('song', data)
  const isDesktop = useMediaQuery('(min-width:810px)')
  const classes = useStyles()
  const jukeboxMode = useSelector((state) => state.player.jukeboxMode)
  const jukeboxDevice = useSelector((state) => state.player.jukeboxDevice)

  const handlers = {
    TOGGLE_LOVE: useCallback(() => toggleLove(), [toggleLove]),
  }

  const handleSaveQueue = useCallback(
    (e) => {
      dispatch(openSaveQueueDialog())
      e.stopPropagation()
    },
    [dispatch],
  )

  const buttonClass = isDesktop ? classes.button : classes.mobileButton
  const listItemClass = isDesktop ? classes.toolbar : classes.mobileListItem

  const saveQueueButton = (
    <IconButton
      size={isDesktop ? 'small' : undefined}
      onClick={handleSaveQueue}
      disabled={isRadio}
      data-testid="save-queue-button"
      className={buttonClass}
    >
      <RiSaveLine className={!isDesktop ? classes.mobileIcon : undefined} />
    </IconButton>
  )

  const loveButton = (
    <LoveButton
      record={data}
      resource={'song'}
      size={isDesktop ? undefined : 'inherit'}
      disabled={loading || toggling || !id || isRadio}
      className={buttonClass}
    />
  )

  const jukeboxIndicator = jukeboxMode && jukeboxDevice && (
    <span className={classes.jukeboxIndicator} title={jukeboxDevice}>
      <BluetoothIcon className={classes.jukeboxIcon} />
      {isDesktop && (
        <Typography variant="caption" className={classes.jukeboxLabel}>
          {jukeboxDevice}
        </Typography>
      )}
    </span>
  )

  return (
    <>
      <GlobalHotKeys keyMap={keyMap} handlers={handlers} allowChanges />
      {isDesktop ? (
        <li className={`${listItemClass} item`}>
          {jukeboxIndicator}
          <DeviceSelector isDesktop={isDesktop} buttonClass={buttonClass} />
          {saveQueueButton}
          {loveButton}
        </li>
      ) : (
        <>
          <li className={`${listItemClass} item`}>
            {jukeboxIndicator}
            <DeviceSelector isDesktop={isDesktop} buttonClass={buttonClass} />
          </li>
          <li className={`${listItemClass} item`}>{saveQueueButton}</li>
          <li className={`${listItemClass} item`}>{loveButton}</li>
        </>
      )}
    </>
  )
}

export default PlayerToolbar
