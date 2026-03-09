import React, { useState, useEffect, useCallback } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import IconButton from '@material-ui/core/IconButton'
import Menu from '@material-ui/core/Menu'
import MenuItem from '@material-ui/core/MenuItem'
import ListItemIcon from '@material-ui/core/ListItemIcon'
import ListItemText from '@material-ui/core/ListItemText'
import Divider from '@material-ui/core/Divider'
import { makeStyles } from '@material-ui/core/styles'
import BluetoothIcon from '@material-ui/icons/Bluetooth'
import BluetoothDisabledIcon from '@material-ui/icons/BluetoothDisabled'
import SpeakerIcon from '@material-ui/icons/Speaker'
import CheckIcon from '@material-ui/icons/Check'
import RefreshIcon from '@material-ui/icons/Refresh'
import config from '../config'
import httpClient from '../dataProvider/httpClient'
import jukeboxClient from './jukeboxClient'
import { enqueueJukeboxCommand } from './jukeboxCommandQueue'
import bluetoothClient from './bluetoothClient'
import { suppressJukeboxMediaEvents } from './jukeboxLifecycle'
import { clamp01 } from './volumeMapping'
import { setJukeboxMode } from '../actions'
import {
  attachJukeboxSession,
  detachJukeboxSession,
  getJukeboxSessionId,
  getOrCreateJukeboxClientId,
} from './jukeboxSession'
import { useNotify } from 'react-admin'

const POLL_INTERVAL_MS = 10000

const useStyles = makeStyles(() => ({
  activeIcon: {
    minWidth: 32,
  },
  menuItem: {
    minWidth: 200,
  },
  disconnected: {
    opacity: 0.5,
  },
}))

const DeviceSelector = ({ isDesktop, buttonClass }) => {
  const classes = useStyles()
  const dispatch = useDispatch()
  const notify = useNotify()
  const [anchorEl, setAnchorEl] = useState(null)
  const [devices, setDevices] = useState([])
  const [bluetoothDevices, setBluetoothDevices] = useState([])
  const playerState = useSelector((state) => state.player)
  const jukeboxMode = useSelector((state) => state.player.jukeboxMode)
  const jukeboxDevice = useSelector((state) => state.player.jukeboxDevice)
  const audioInstance = playerState.audioInstance

  const fetchDevices = useCallback(() => {
    httpClient('/api/jukebox/devices')
      .then(({ json }) => setDevices(json))
      .catch(() => setDevices([]))
  }, [])

  const fetchBluetoothDevices = useCallback(() => {
    if (!config.bluetoothManagementEnabled) {
      setBluetoothDevices([])
      return
    }
    bluetoothClient
      .list()
      .then((data) => setBluetoothDevices(data))
      .catch(() => setBluetoothDevices([]))
  }, [])

  useEffect(() => {
    if (!config.jukeboxEnabled) return
    fetchDevices()
    fetchBluetoothDevices()
    const interval = setInterval(fetchDevices, POLL_INTERVAL_MS)
    const btInterval = setInterval(fetchBluetoothDevices, POLL_INTERVAL_MS)
    return () => {
      clearInterval(interval)
      clearInterval(btInterval)
    }
  }, [fetchDevices, fetchBluetoothDevices])

  const handleOpen = useCallback((e) => {
    setAnchorEl(e.currentTarget)
    e.stopPropagation()
  }, [])

  const handleClose = useCallback(() => {
    setAnchorEl(null)
  }, [])

  const handleSwitch = useCallback(
    (device) => {
      const isLocalDevice = device.deviceName === 'auto'
      handleClose()

      // Switch the server-side output device
      httpClient('/api/jukebox/devices/switch', {
        method: 'POST',
        body: JSON.stringify({ deviceName: device.deviceName }),
      })
        .then(({ json }) => {
          setDevices(json)

          if (isLocalDevice) {
            const sessionId = getJukeboxSessionId()
            const clientId = getOrCreateJukeboxClientId()
            if (jukeboxMode) {
              detachJukeboxSession({
                client: jukeboxClient,
                sessionId,
                clientId,
              }).catch(() => {})
            }
            enqueueJukeboxCommand(() => jukeboxClient.stop()).catch(() => {})
            dispatch(setJukeboxMode(false))
            if (audioInstance) {
              audioInstance.muted = false
              if (audioInstance.paused) {
                audioInstance.play().catch(() => {})
              }
            }
          } else {
            // Switching to remote device — enter Jukebox mode
            const trackIds = playerState.queue.map((item) => item.trackId)
            const currentIndex = playerState.savedPlayIndex || 0
            const currentTime = audioInstance
              ? Math.floor(audioInstance.currentTime || 0)
              : 0
            const currentGain = audioInstance
              ? clamp01(audioInstance.volume)
              : 1

            // Mute browser audio — keeps element "playing" so UI stays correct
            if (audioInstance) {
              suppressJukeboxMediaEvents()
              audioInstance.muted = true
            }

            if (trackIds.length > 0) {
              enqueueJukeboxCommand(() =>
                jukeboxClient
                  .set(trackIds)
                  .then(() => jukeboxClient.skip(currentIndex, currentTime))
                  .then(() => jukeboxClient.volume(currentGain))
                  .then(() => jukeboxClient.play()),
              ).catch(() => {
                notify('Failed to start playback on remote device', 'warning')
              })
            }

            if (jukeboxMode) {
              attachJukeboxSession({
                client: jukeboxClient,
                sessionId: getJukeboxSessionId(),
                clientId: getOrCreateJukeboxClientId(),
                deviceName: device.deviceName,
                dispatch,
              }).catch(() => {})
            }

            dispatch(setJukeboxMode(true, device.name))
          }
        })
        .catch(() => {
          notify('Failed to switch audio device', 'warning')
        })
    },
    [handleClose, audioInstance, playerState, dispatch, notify],
  )

  const handleRefresh = useCallback(() => {
    httpClient('/api/jukebox/devices/refresh', {
      method: 'POST',
    })
      .then(({ json }) => setDevices(json))
      .catch(() => {
        notify('Failed to refresh device list', 'warning')
      })
  }, [notify])

  const handleScan = useCallback(() => {
    bluetoothClient
      .scan()
      .then(() => {
        fetchDevices()
        fetchBluetoothDevices()
      })
      .catch(() => {
        notify('Bluetooth scan failed', 'warning')
      })
  }, [fetchDevices, fetchBluetoothDevices, notify])

  const handleBluetoothConnect = useCallback(
    (mac) => {
      bluetoothClient
        .connect(mac)
        .then(() => {
          fetchDevices()
          fetchBluetoothDevices()
        })
        .catch(() => {
          notify('Failed to connect Bluetooth device', 'warning')
        })
    },
    [fetchDevices, fetchBluetoothDevices, notify],
  )

  const handleBluetoothDisconnect = useCallback(
    (mac) => {
      bluetoothClient
        .disconnect(mac)
        .then(() => {
          fetchDevices()
          fetchBluetoothDevices()
        })
        .catch(() => {
          notify('Failed to disconnect Bluetooth device', 'warning')
        })
    },
    [fetchDevices, fetchBluetoothDevices, notify],
  )

  if (
    !config.jukeboxEnabled ||
    (devices.length <= 1 && !config.bluetoothManagementEnabled)
  ) {
    return null
  }

  return (
    <>
      <IconButton
        size={isDesktop ? 'small' : undefined}
        onClick={handleOpen}
        className={buttonClass}
        data-testid="device-selector-button"
        aria-label="Select audio device"
        title={jukeboxMode && jukeboxDevice ? jukeboxDevice : undefined}
      >
        <BluetoothIcon
          fontSize={isDesktop ? 'default' : 'inherit'}
          color={jukeboxMode ? 'primary' : 'inherit'}
        />
      </IconButton>
      <Menu
        anchorEl={anchorEl}
        open={Boolean(anchorEl)}
        onClose={handleClose}
        getContentAnchorEl={null}
        anchorOrigin={{ vertical: 'top', horizontal: 'center' }}
        transformOrigin={{ vertical: 'bottom', horizontal: 'center' }}
      >
        {devices.map((device) => (
          <MenuItem
            key={device.deviceName}
            onClick={() => handleSwitch(device)}
            className={`${classes.menuItem} ${device.isBluetooth && !device.connected ? classes.disconnected : ''}`}
            disabled={device.isBluetooth && !device.connected}
          >
            <ListItemIcon className={classes.activeIcon}>
              {device.isDefault ? <CheckIcon fontSize="small" /> : <span />}
            </ListItemIcon>
            <ListItemIcon className={classes.activeIcon}>
              {device.isBluetooth ? (
                device.connected ? (
                  <BluetoothIcon fontSize="small" />
                ) : (
                  <BluetoothDisabledIcon fontSize="small" />
                )
              ) : (
                <SpeakerIcon fontSize="small" />
              )}
            </ListItemIcon>
            <ListItemText
              primary={device.name}
              secondary={
                device.isBluetooth && !device.connected ? 'Disconnected' : null
              }
            />
          </MenuItem>
        ))}
        <Divider />
        <MenuItem onClick={handleRefresh}>
          <ListItemIcon className={classes.activeIcon}>
            <RefreshIcon fontSize="small" />
          </ListItemIcon>
          <ListItemText primary="Refresh devices" />
        </MenuItem>
        {config.bluetoothManagementEnabled && (
          <MenuItem onClick={handleScan}>
            <ListItemIcon className={classes.activeIcon}>
              <RefreshIcon fontSize="small" />
            </ListItemIcon>
            <ListItemText primary="Scan for devices" />
          </MenuItem>
        )}
        {config.bluetoothManagementEnabled && bluetoothDevices.length > 0 && (
          <Divider />
        )}
        {config.bluetoothManagementEnabled &&
          bluetoothDevices.map((device) => {
            const action = device.connected ? 'Disconnect' : 'Connect'
            const onClick = () =>
              device.connected
                ? handleBluetoothDisconnect(device.mac)
                : handleBluetoothConnect(device.mac)
            return (
              <MenuItem key={`bluetooth-${device.mac}`} onClick={onClick}>
                <ListItemIcon className={classes.activeIcon}>
                  {device.connected ? (
                    <BluetoothDisabledIcon fontSize="small" />
                  ) : (
                    <BluetoothIcon fontSize="small" />
                  )}
                </ListItemIcon>
                <ListItemText
                  primary={`${action} ${device.name || device.mac}`}
                />
              </MenuItem>
            )
          })}
      </Menu>
    </>
  )
}

export default DeviceSelector
