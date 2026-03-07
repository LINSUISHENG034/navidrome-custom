package playback

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/navidrome/navidrome/core/playback/mpv"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

type Track interface {
	IsPlaying() bool
	SetVolume(value float32) // Used to control the playback volume. A float value between 0.0 and 1.0.
	Pause()
	Unpause()
	Position() int
	SetPosition(offset int) error
	Close()
	String() string
}

type playbackDevice struct {
	mu                   sync.Mutex // protects all mutable state
	serviceCtx           context.Context
	ParentPlaybackServer PlaybackServer
	Default              bool
	User                 string
	Name                 string
	DeviceName           string
	PlaybackQueue        *Queue
	Gain                 float32
	PlaybackDone         chan bool
	ActiveTrack          Track
	onStateChange        func(DeviceStatus)
	startTrackSwitcher   sync.Once
}

type DeviceStatus struct {
	CurrentIndex int
	Playing      bool
	Gain         float32
	Position     int
}

const DefaultGain float32 = 1.0

var newTrack = func(ctx context.Context, playbackDoneChannel chan bool, deviceName string, mf model.MediaFile) (Track, error) {
	return mpv.NewTrack(ctx, playbackDoneChannel, deviceName, mf)
}

func invokeCallback(cb func()) {
	if cb != nil {
		cb()
	}
}

func (pd *playbackDevice) stateChangeNotifierLocked() func() {
	if pd.onStateChange == nil {
		return nil
	}
	status := pd.getStatus()
	return func() {
		pd.onStateChange(status)
	}
}

func (pd *playbackDevice) getStatus() DeviceStatus {
	pos := 0
	if pd.ActiveTrack != nil {
		pos = pd.ActiveTrack.Position()
	}
	return DeviceStatus{
		CurrentIndex: pd.PlaybackQueue.Index,
		Playing:      pd.isPlaying(),
		Gain:         pd.Gain,
		Position:     pos,
	}
}

// NewPlaybackDevice creates a new playback device which implements all the basic Jukebox mode commands defined here:
// http://www.subsonic.org/pages/api.jsp#jukeboxControl
// Starts the trackSwitcher goroutine for the device.
func NewPlaybackDevice(ctx context.Context, playbackServer PlaybackServer, name string, deviceName string) *playbackDevice {
	return &playbackDevice{
		serviceCtx:           ctx,
		ParentPlaybackServer: playbackServer,
		User:                 "",
		Name:                 name,
		DeviceName:           deviceName,
		Gain:                 DefaultGain,
		PlaybackQueue:        NewQueue(),
		PlaybackDone:         make(chan bool),
	}
}

func (pd *playbackDevice) String() string {
	return fmt.Sprintf("Name: %s, Gain: %.4f, Loaded track: %s", pd.Name, pd.Gain, pd.ActiveTrack)
}

func (pd *playbackDevice) Get(ctx context.Context) (model.MediaFiles, DeviceStatus, error) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	log.Debug(ctx, "Processing Get action", "device", pd)
	return pd.PlaybackQueue.Get(), pd.getStatus(), nil
}

func (pd *playbackDevice) Status(ctx context.Context) (DeviceStatus, error) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	log.Debug(ctx, fmt.Sprintf("processing Status action on: %s, queue: %s", pd, pd.PlaybackQueue))
	return pd.getStatus(), nil
}

// Set is similar to a clear followed by a add, but will not change the currently playing track.
func (pd *playbackDevice) Set(ctx context.Context, ids []string) (DeviceStatus, error) {
	pd.mu.Lock()
	log.Debug(ctx, "Processing Set action", "ids", ids, "device", pd)

	pd.clearLocked(ctx)
	status, err := pd.addLocked(ctx, ids)
	notify := pd.stateChangeNotifierLocked()
	if err != nil {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

func (pd *playbackDevice) Start(ctx context.Context) (DeviceStatus, error) {
	pd.mu.Lock()
	status, changed, err := pd.startLocked(ctx)
	notify := pd.stateChangeNotifierLocked()
	if err != nil || !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

// startLocked contains the Start logic and must be called with pd.mu held.
// It may temporarily release and re-acquire pd.mu around slow operations.
func (pd *playbackDevice) startLocked(ctx context.Context) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Start action", "device", pd)
	changed := false

	pd.startTrackSwitcher.Do(func() {
		log.Info(ctx, "Starting trackSwitcher goroutine")
		go func() {
			pd.trackSwitcherGoroutine()
		}()
	})

	if pd.ActiveTrack != nil {
		if pd.isPlaying() {
			log.Debug("trying to start an already playing track")
		} else {
			pd.ActiveTrack.Unpause()
			changed = true
		}
	} else {
		if !pd.PlaybackQueue.IsEmpty() {
			idx := pd.PlaybackQueue.Index
			pd.mu.Unlock()
			track, err := pd.switchActiveTrackByIndex(idx)
			pd.mu.Lock()
			if err != nil {
				return pd.getStatus(), false, err
			}
			pd.assignTrack(track)
			if pd.ActiveTrack != nil {
				pd.ActiveTrack.Unpause()
				changed = true
			}
		}
	}

	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) Stop(ctx context.Context) (DeviceStatus, error) {
	pd.mu.Lock()
	status, changed, err := pd.stopLocked(ctx)
	notify := pd.stateChangeNotifierLocked()
	if err != nil || !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

func (pd *playbackDevice) Shutdown(ctx context.Context) (DeviceStatus, error) {
	pd.mu.Lock()
	status, changed, err := pd.shutdownLocked(ctx)
	notify := pd.stateChangeNotifierLocked()
	if err != nil || !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

// stopLocked contains the Stop logic and must be called with pd.mu held.
func (pd *playbackDevice) stopLocked(ctx context.Context) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Stop action", "device", pd)
	changed := pd.ActiveTrack != nil && pd.isPlaying()
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.Pause()
	}
	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) shutdownLocked(ctx context.Context) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Shutdown action", "device", pd)
	changed := pd.ActiveTrack != nil
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.Pause()
		pd.ActiveTrack.Close()
		pd.ActiveTrack = nil
	}
	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) Skip(ctx context.Context, index int, offset int) (DeviceStatus, error) {
	pd.mu.Lock()
	status, changed, err := pd.skipLocked(ctx, index, offset)
	notify := pd.stateChangeNotifierLocked()
	if err != nil || !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

func (pd *playbackDevice) skipLocked(ctx context.Context, index int, offset int) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Skip action", "index", index, "offset", offset, "device", pd)

	// Skip is a no-op if already playing the requested track at offset 0.
	// This prevents the browser's auto-advance onAudioPlayTrackChange from
	// resetting the jukebox position after trackSwitcherGoroutine already advanced.
	if index == pd.PlaybackQueue.Index && offset == 0 && pd.ActiveTrack != nil && pd.isPlaying() {
		return pd.getStatus(), false, nil
	}

	wasPlaying := pd.isPlaying()

	if pd.ActiveTrack != nil && wasPlaying {
		pd.ActiveTrack.Pause()
	}

	if index != pd.PlaybackQueue.Index && pd.ActiveTrack != nil {
		pd.ActiveTrack.Close()
		pd.ActiveTrack = nil
	}

	if pd.ActiveTrack == nil {
		pd.mu.Unlock()
		track, err := pd.switchActiveTrackByIndex(index)
		pd.mu.Lock()
		if err != nil {
			return pd.getStatus(), false, err
		}
		pd.assignTrack(track)
	}

	if pd.ActiveTrack != nil {
		err := pd.ActiveTrack.SetPosition(offset)
		if err != nil {
			log.Error(ctx, "error setting position", err)
			return pd.getStatus(), false, err
		}
	}

	if wasPlaying {
		_, _, err := pd.startLocked(ctx)
		if err != nil {
			log.Error(ctx, "error starting new track after skipping")
			return pd.getStatus(), false, err
		}
	}

	return pd.getStatus(), true, nil
}

func (pd *playbackDevice) Add(ctx context.Context, ids []string) (DeviceStatus, error) {
	pd.mu.Lock()
	status, err := pd.addLocked(ctx, ids)
	notify := pd.stateChangeNotifierLocked()
	if err != nil || len(ids) == 0 {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

func (pd *playbackDevice) Insert(ctx context.Context, index int, ids []string) (DeviceStatus, error) {
	pd.mu.Lock()
	status, err := pd.insertLocked(ctx, index, ids)
	notify := pd.stateChangeNotifierLocked()
	if err != nil || len(ids) == 0 {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

// addLocked contains the Add logic and must be called with pd.mu held.
func (pd *playbackDevice) addLocked(ctx context.Context, ids []string) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Add action", "ids", ids, "device", pd)
	if len(ids) < 1 {
		return pd.getStatus(), nil
	}

	items := model.MediaFiles{}

	for _, id := range ids {
		mf, err := pd.ParentPlaybackServer.GetMediaFile(id)
		if err != nil {
			return DeviceStatus{}, err
		}
		log.Debug(ctx, "Found mediafile: "+mf.Path)
		items = append(items, *mf)
	}
	pd.PlaybackQueue.Add(items)

	return pd.getStatus(), nil
}

func (pd *playbackDevice) insertLocked(ctx context.Context, index int, ids []string) (DeviceStatus, error) {
	log.Debug(ctx, "Processing Insert action", "ids", ids, "index", index, "device", pd)
	if len(ids) < 1 {
		return pd.getStatus(), nil
	}

	items := model.MediaFiles{}
	for _, id := range ids {
		mf, err := pd.ParentPlaybackServer.GetMediaFile(id)
		if err != nil {
			return DeviceStatus{}, err
		}
		log.Debug(ctx, "Found mediafile: "+mf.Path)
		items = append(items, *mf)
	}
	pd.PlaybackQueue.Insert(index, items)
	return pd.getStatus(), nil
}

func (pd *playbackDevice) Clear(ctx context.Context) (DeviceStatus, error) {
	pd.mu.Lock()
	changed := pd.ActiveTrack != nil || !pd.PlaybackQueue.IsEmpty()
	pd.clearLocked(ctx)
	status := pd.getStatus()
	notify := pd.stateChangeNotifierLocked()
	if !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, nil
}

// clearLocked contains the Clear logic and must be called with pd.mu held.
func (pd *playbackDevice) clearLocked(ctx context.Context) {
	log.Debug(ctx, "Processing Clear action", "device", pd)
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.Pause()
		pd.ActiveTrack.Close()
		pd.ActiveTrack = nil
	}
	pd.PlaybackQueue.Clear()
}

func (pd *playbackDevice) Remove(ctx context.Context, index int) (DeviceStatus, error) {
	pd.mu.Lock()
	log.Debug(ctx, "Processing Remove action", "index", index, "device", pd)
	changed := index > -1 && index < pd.PlaybackQueue.Size()
	// close and nil the active track if removing the currently playing track
	if pd.PlaybackQueue.Index == index && pd.ActiveTrack != nil {
		pd.ActiveTrack.Pause()
		pd.ActiveTrack.Close()
		pd.ActiveTrack = nil
	}

	if index > -1 && index < pd.PlaybackQueue.Size() {
		pd.PlaybackQueue.Remove(index)
	} else {
		log.Error(ctx, "Index to remove out of range: "+fmt.Sprint(index))
	}
	status := pd.getStatus()
	notify := pd.stateChangeNotifierLocked()
	if !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, nil
}

func (pd *playbackDevice) Move(ctx context.Context, fromIndex, toIndex int) (DeviceStatus, error) {
	pd.mu.Lock()
	log.Debug(ctx, "Processing Move action", "from", fromIndex, "to", toIndex, "device", pd)

	if fromIndex < 0 || fromIndex >= pd.PlaybackQueue.Size() || toIndex < 0 || toIndex >= pd.PlaybackQueue.Size() {
		status := pd.getStatus()
		pd.mu.Unlock()
		return status, fmt.Errorf("index out of range: from=%d, to=%d, size=%d", fromIndex, toIndex, pd.PlaybackQueue.Size())
	}

	pd.PlaybackQueue.Move(fromIndex, toIndex)
	status := pd.getStatus()
	notify := pd.stateChangeNotifierLocked()
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, nil
}

func (pd *playbackDevice) Shuffle(ctx context.Context) (DeviceStatus, error) {
	pd.mu.Lock()
	log.Debug(ctx, "Processing Shuffle action", "device", pd)
	changed := pd.PlaybackQueue.Size() > 1
	if pd.PlaybackQueue.Size() > 1 {
		pd.PlaybackQueue.Shuffle()
	}
	status := pd.getStatus()
	notify := pd.stateChangeNotifierLocked()
	if !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, nil
}

// SetGain is used to control the playback volume. A float value between 0.0 and 1.0.
func (pd *playbackDevice) SetGain(ctx context.Context, gain float32) (DeviceStatus, error) {
	pd.mu.Lock()
	log.Debug(ctx, "Processing SetGain action", "newGain", gain, "device", pd)
	changed := pd.Gain != gain

	if pd.ActiveTrack != nil {
		pd.ActiveTrack.SetVolume(gain)
	}
	pd.Gain = gain

	status := pd.getStatus()
	notify := pd.stateChangeNotifierLocked()
	if !changed {
		notify = nil
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, nil
}

func (pd *playbackDevice) isPlaying() bool {
	return pd.ActiveTrack != nil && pd.ActiveTrack.IsPlaying()
}

func (pd *playbackDevice) trackSwitcherGoroutine() {
	log.Debug("Started trackSwitcher goroutine", "device", pd)
	for {
		select {
		case <-pd.PlaybackDone:
			log.Debug("Track switching detected")
			pd.mu.Lock()
			stateChanged := false
			if pd.ActiveTrack != nil {
				pd.ActiveTrack.Close()
				pd.ActiveTrack = nil
				stateChanged = true
			}

			if !pd.PlaybackQueue.IsAtLastElement() {
				pd.PlaybackQueue.IncreaseIndex()
				idx := pd.PlaybackQueue.Index
				pd.mu.Unlock()

				log.Debug("Switching to next song", "index", idx)
				track, err := pd.switchActiveTrackByIndex(idx)
				if err != nil {
					log.Error("Error switching track", err)
					continue
				}

				pd.mu.Lock()
				pd.assignTrack(track)
				if pd.ActiveTrack != nil {
					pd.ActiveTrack.Unpause()
					stateChanged = true
				}
				notify := pd.stateChangeNotifierLocked()
				pd.mu.Unlock()
				invokeCallback(notify)
			} else {
				notify := pd.stateChangeNotifierLocked()
				pd.mu.Unlock()
				if stateChanged {
					invokeCallback(notify)
				}
				log.Debug("There is no song left in the playlist. Finish.")
			}
		case <-pd.serviceCtx.Done():
			log.Debug("Stopping trackSwitcher goroutine", "device", pd.Name)
			return
		}
	}
}

func (pd *playbackDevice) switchActiveTrackByIndex(index int) (Track, error) {
	pd.PlaybackQueue.SetIndex(index)
	currentTrack := pd.PlaybackQueue.Current()
	if currentTrack == nil {
		return nil, errors.New("could not get current track")
	}

	track, err := newTrack(pd.serviceCtx, pd.PlaybackDone, pd.DeviceName, *currentTrack)
	if err != nil {
		return nil, err
	}
	track.SetVolume(pd.Gain)
	return track, nil
}

// assignTrack safely replaces the active track while holding the mutex.
// If another goroutine set ActiveTrack during the unlock window, the
// orphaned process is closed before the new track is assigned.
func (pd *playbackDevice) assignTrack(newTrack Track) {
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.Close()
	}
	pd.ActiveTrack = newTrack
}
