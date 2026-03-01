// Package playback implements audio playback using PlaybackDevices. It is used to implement the Jukebox mode in turn.
// It makes use of the MPV library to do the playback. Major parts are:
// - decoder which includes decoding and transcoding of various audio file formats
// - device implementing the basic functions to work with audio devices like set, play, stop, skip, ...
// - queue a simple playlist
package playback

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/playback/bluetooth"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils/singleton"
)

type PlaybackServer interface {
	Run(ctx context.Context) error
	GetDeviceForUser(user string) (*playbackDevice, error)
	GetMediaFile(id string) (*model.MediaFile, error)
	ListDevices() []DeviceInfo
	SwitchDevice(ctx context.Context, deviceName string) error
	RefreshDevices(ctx context.Context) error
}

// DeviceInfo represents an audio output device exposed via the API.
type DeviceInfo struct {
	Name        string `json:"name"`
	DeviceName  string `json:"deviceName"`
	IsDefault   bool   `json:"isDefault"`
	IsBluetooth bool   `json:"isBluetooth"`
	Connected   bool   `json:"connected"`
}

type playbackServer struct {
	ctx             *context.Context
	datastore       model.DataStore
	playbackDevices []playbackDevice
}

// GetInstance returns the playback-server singleton
func GetInstance(ds model.DataStore) PlaybackServer {
	return singleton.GetInstance(func() *playbackServer {
		return &playbackServer{datastore: ds}
	})
}

// Run starts the playback server which serves request until canceled using the given context
func (ps *playbackServer) Run(ctx context.Context) error {
	ps.ctx = &ctx

	devices, err := ps.initDeviceStatus(ctx, conf.Server.Jukebox.Devices, conf.Server.Jukebox.Default)
	if err != nil {
		return err
	}
	ps.playbackDevices = devices

	if conf.Server.Jukebox.AutoDiscoverBluetooth {
		ps.mergeBluetoothDevices(ctx)
	}

	log.Info(ctx, fmt.Sprintf("%d audio devices found", len(ps.playbackDevices)))

	defaultDevice, _ := ps.getDefaultDevice()

	log.Info(ctx, "Using audio device: "+defaultDevice.DeviceName)

	if conf.Server.Jukebox.AutoDiscoverBluetooth {
		go ps.monitorBluetoothConnections(ctx)
	}

	<-ctx.Done()

	// Should confirm all subprocess are terminated before returning
	return nil
}

const btMonitorInterval = 10 * time.Second

// monitorBluetoothConnections periodically checks whether Bluetooth sinks are still
// available. If the active default device is a BT sink that has disappeared, playback
// is automatically paused.
func (ps *playbackServer) monitorBluetoothConnections(ctx context.Context) {
	ticker := time.NewTicker(btMonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ps.checkBluetoothConnections(ctx)
		}
	}
}

func (ps *playbackServer) checkBluetoothConnections(ctx context.Context) {
	activeSinks := make(map[string]bool)
	for _, sink := range bluetooth.DiscoverAllSinks(ctx) {
		activeSinks[sink.MPVDeviceName()] = true
	}

	for idx := range ps.playbackDevices {
		d := &ps.playbackDevices[idx]
		if !strings.HasPrefix(d.DeviceName, "pulse/bluez_") {
			continue
		}
		if _, connected := activeSinks[d.DeviceName]; !connected && d.isPlaying() {
			d.ActiveTrack.Pause()
			log.Warn(ctx, "Bluetooth device disconnected, pausing playback", "device", d.Name)
		}
	}
}

func (ps *playbackServer) initDeviceStatus(ctx context.Context, devices []conf.AudioDeviceDefinition, defaultDevice string) ([]playbackDevice, error) {
	pbDevices := make([]playbackDevice, max(1, len(devices)))
	defaultDeviceFound := false

	if defaultDevice == "" {
		// if there are no devices given and no default device, we create a synthetic device named "auto"
		if len(devices) == 0 {
			pbDevices[0] = *NewPlaybackDevice(ctx, ps, "auto", "auto")
		}

		// if there is but only one entry and no default given, just use that.
		if len(devices) == 1 {
			if len(devices[0]) != 2 {
				return []playbackDevice{}, fmt.Errorf("audio device definition ought to contain 2 fields, found: %d ", len(devices[0]))
			}
			pbDevices[0] = *NewPlaybackDevice(ctx, ps, devices[0][0], devices[0][1])
		}

		if len(devices) > 1 {
			return []playbackDevice{}, fmt.Errorf("number of audio device found is %d, but no default device defined. Set Jukebox.Default", len(devices))
		}

		pbDevices[0].Default = true
		return pbDevices, nil
	}

	for idx, audioDevice := range devices {
		if len(audioDevice) != 2 {
			return []playbackDevice{}, fmt.Errorf("audio device definition ought to contain 2 fields, found: %d ", len(audioDevice))
		}

		pbDevices[idx] = *NewPlaybackDevice(ctx, ps, audioDevice[0], audioDevice[1])

		if audioDevice[0] == defaultDevice {
			pbDevices[idx].Default = true
			defaultDeviceFound = true
		}
	}

	if !defaultDeviceFound {
		return []playbackDevice{}, fmt.Errorf("default device name not found: %s ", defaultDevice)
	}
	return pbDevices, nil
}

func (ps *playbackServer) getDefaultDevice() (*playbackDevice, error) {
	for idx := range ps.playbackDevices {
		if ps.playbackDevices[idx].Default {
			return &ps.playbackDevices[idx], nil
		}
	}
	return nil, fmt.Errorf("no default device found")
}

// GetMediaFile retrieves the MediaFile given by the id parameter
func (ps *playbackServer) GetMediaFile(id string) (*model.MediaFile, error) {
	return ps.datastore.MediaFile(*ps.ctx).Get(id)
}

// GetDeviceForUser returns the audio playback device for the given user. As of now this is but only the default device.
func (ps *playbackServer) GetDeviceForUser(user string) (*playbackDevice, error) {
	log.Debug("Processing GetDevice", "user", user)
	// README: here we might plug-in the user-device mapping one fine day
	device, err := ps.getDefaultDevice()
	if err != nil {
		return nil, err
	}
	device.User = user
	return device, nil
}

// mergeBluetoothDevices discovers Bluetooth sinks and appends any new ones to playbackDevices.
func (ps *playbackServer) mergeBluetoothDevices(ctx context.Context) {
	btSinks := bluetooth.DiscoverBluetoothSinks(ctx)
	for _, sink := range btSinks {
		devName := sink.MPVDeviceName()
		if ps.hasDevice(devName) {
			continue
		}
		dev := NewPlaybackDevice(ctx, ps, sink.FriendlyName(), devName)
		ps.playbackDevices = append(ps.playbackDevices, *dev)
		log.Info(ctx, "Discovered Bluetooth device", "name", sink.FriendlyName(), "device", devName)
	}
}

func (ps *playbackServer) hasDevice(deviceName string) bool {
	for _, d := range ps.playbackDevices {
		if d.DeviceName == deviceName {
			return true
		}
	}
	return false
}

// ListDevices returns info about all configured playback devices, including live connection status.
func (ps *playbackServer) ListDevices() []DeviceInfo {
	// Auto-discover and merge any new Bluetooth devices on each call
	if conf.Server.Jukebox.AutoDiscoverBluetooth && ps.ctx != nil {
		ps.mergeBluetoothDevices(*ps.ctx)
	}

	// Get current sinks to check BT connection status
	activeSinks := make(map[string]bool)
	if conf.Server.Jukebox.AutoDiscoverBluetooth && ps.ctx != nil {
		for _, sink := range bluetooth.DiscoverAllSinks(*ps.ctx) {
			activeSinks[sink.MPVDeviceName()] = true
		}
	}

	devices := make([]DeviceInfo, len(ps.playbackDevices))
	for i, d := range ps.playbackDevices {
		isBT := strings.HasPrefix(d.DeviceName, "pulse/bluez_")
		connected := true
		if isBT {
			_, connected = activeSinks[d.DeviceName]
		}
		devices[i] = DeviceInfo{
			Name:        d.Name,
			DeviceName:  d.DeviceName,
			IsDefault:   d.Default,
			IsBluetooth: isBT,
			Connected:   connected,
		}
	}
	return devices
}

// SwitchDevice changes the default playback device. If the old device was actively
// playing, the queue and playback state are migrated to the new device and playback
// resumes at the same position.
func (ps *playbackServer) SwitchDevice(ctx context.Context, deviceName string) error {
	var newDev *playbackDevice
	for idx := range ps.playbackDevices {
		if ps.playbackDevices[idx].DeviceName == deviceName {
			newDev = &ps.playbackDevices[idx]
			break
		}
	}
	if newDev == nil {
		return fmt.Errorf("device not found: %s", deviceName)
	}

	// Find the current default device and capture its playback state
	var oldDev *playbackDevice
	for idx := range ps.playbackDevices {
		if ps.playbackDevices[idx].Default {
			oldDev = &ps.playbackDevices[idx]
			break
		}
	}

	// Capture state before stopping
	wasPlaying := oldDev != nil && oldDev.isPlaying()
	var savedPosition int
	var savedGain float32
	var savedQueue model.MediaFiles
	var savedIndex int

	if oldDev != nil && oldDev.DeviceName != deviceName {
		savedGain = oldDev.Gain
		savedQueue = oldDev.PlaybackQueue.Get()
		savedIndex = oldDev.PlaybackQueue.Index

		if oldDev.ActiveTrack != nil {
			savedPosition = oldDev.ActiveTrack.Position()
			oldDev.ActiveTrack.Pause()
			oldDev.ActiveTrack.Close()
			oldDev.ActiveTrack = nil
			log.Info(ctx, "Stopped playback on previous device", "device", oldDev.Name, "position", savedPosition)
		}
	}

	// Toggle default flags
	for idx := range ps.playbackDevices {
		ps.playbackDevices[idx].Default = (ps.playbackDevices[idx].DeviceName == deviceName)
	}

	// Migrate queue and state to the new device
	if oldDev != nil && oldDev.DeviceName != deviceName && len(savedQueue) > 0 {
		newDev.PlaybackQueue.Set(savedQueue)
		newDev.PlaybackQueue.SetIndex(savedIndex)
		newDev.Gain = savedGain

		if wasPlaying {
			_, err := newDev.Start(ctx)
			if err != nil {
				log.Error(ctx, "Error starting playback on new device", err)
				return err
			}
			if savedPosition > 0 {
				err = newDev.ActiveTrack.SetPosition(savedPosition)
				if err != nil {
					log.Warn(ctx, "Could not restore position on new device", "position", savedPosition, err)
				}
			}
			if newDev.ActiveTrack != nil {
				newDev.ActiveTrack.SetVolume(savedGain)
			}
			log.Info(ctx, "Resumed playback on new device", "device", deviceName, "position", savedPosition)
		}
	}

	log.Info(ctx, "Switched default audio device", "device", deviceName)
	return nil
}

// RefreshDevices re-runs Bluetooth discovery and appends any new devices.
func (ps *playbackServer) RefreshDevices(ctx context.Context) error {
	if !conf.Server.Jukebox.AutoDiscoverBluetooth {
		return nil
	}
	ps.mergeBluetoothDevices(ctx)
	return nil
}
