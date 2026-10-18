package playback

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

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
	mu                    sync.Mutex      // protects all mutable state
	serviceCtx            context.Context //nolint:containedctx // playback service lifecycle ctx
	ParentPlaybackServer  PlaybackServer
	Default               bool
	User                  string
	Name                  string
	DeviceName            string
	PlaybackQueue         *Queue
	queueVersion          int
	Gain                  float32
	PlaybackEvents        chan model.PlaybackEvent
	ActiveTrack           Track
	onStateChange         func(DeviceStatus)
	startTrackSwitcher    sync.Once
	preparationVersion    uint64
	nextPlaybackToken     model.PlaybackToken
	activePlaybackToken   model.PlaybackToken
	pendingPlaybackTokens map[model.PlaybackToken]chan struct{}
	pendingPrepareCancels map[model.PlaybackToken]context.CancelFunc
	activePrepareCancel   context.CancelFunc
	preparing             sync.WaitGroup
	retiring              bool
	recoveryPosition      *int
	failureReason         string
}

type DeviceStatus struct {
	CurrentIndex int
	Playing      bool
	Gain         float32
	Position     int
	QueueVersion int
}

const DefaultGain float32 = 1.0

var newTrack = func(ctx context.Context, events chan<- model.PlaybackEvent, token model.PlaybackToken, deviceName string, mf model.MediaFile) (Track, error) {
	return mpv.NewTrack(ctx, events, token, deviceName, mf)
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

func (pd *playbackDevice) incrementQueueVersionLocked() {
	pd.queueVersion++
}

func (pd *playbackDevice) getStatus() DeviceStatus {
	pos := 0
	if pd.ActiveTrack != nil {
		pos = pd.ActiveTrack.Position()
	} else if pd.recoveryPosition != nil {
		pos = *pd.recoveryPosition
	}
	return DeviceStatus{
		CurrentIndex: pd.PlaybackQueue.Index,
		Playing:      pd.isPlaying(),
		Gain:         pd.Gain,
		Position:     pos,
		QueueVersion: pd.queueVersion,
	}
}

func (pd *playbackDevice) FailureReason() string {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	return pd.failureReason
}

// NewPlaybackDevice creates a new playback device which implements all the basic Jukebox mode commands defined here:
// http://www.subsonic.org/pages/api.jsp#jukeboxControl
// Starts the trackSwitcher goroutine for the device.
func NewPlaybackDevice(ctx context.Context, playbackServer PlaybackServer, name string, deviceName string) *playbackDevice {
	return &playbackDevice{
		serviceCtx:            ctx,
		ParentPlaybackServer:  playbackServer,
		User:                  "",
		Name:                  name,
		DeviceName:            deviceName,
		Gain:                  DefaultGain,
		PlaybackQueue:         NewQueue(),
		PlaybackEvents:        make(chan model.PlaybackEvent, 32),
		pendingPlaybackTokens: make(map[model.PlaybackToken]chan struct{}),
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
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandSet, IDs: ids})
}

func (pd *playbackDevice) setLocked(ctx context.Context, ids []string) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Set action", "ids", ids, "device", pd)
	queueChanged := !pd.PlaybackQueue.IsEmpty() || len(ids) > 0
	pd.clearLocked(ctx)
	status, err := pd.addLocked(ctx, ids)
	if err == nil && queueChanged {
		pd.incrementQueueVersionLocked()
		status = pd.getStatus()
	}
	return status, queueChanged, err
}

func (pd *playbackDevice) Start(ctx context.Context) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandStart})
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
			track, token, current, err := pd.loadTrackLocked(ctx, pd.PlaybackQueue.Index)
			if err != nil || !current {
				return pd.getStatus(), false, err
			}
			if pd.recoveryPosition != nil {
				if err := track.SetPosition(*pd.recoveryPosition); err != nil {
					pd.discardPendingTrack(token)
					track.Close()
					return pd.getStatus(), false, err
				}
			}
			pd.assignTrack(track, token)
			if pd.ActiveTrack != nil {
				pd.recoveryPosition = nil
				pd.ActiveTrack.Unpause()
				changed = true
			}
		}
	}

	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) Stop(ctx context.Context) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandPause})
}

func (pd *playbackDevice) Shutdown(ctx context.Context) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandStop})
}

// stopLocked contains the Stop logic and must be called with pd.mu held.
func (pd *playbackDevice) stopLocked(ctx context.Context) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Stop action", "device", pd)
	pd.cancelPreparationLocked()
	changed := pd.ActiveTrack != nil && pd.isPlaying()
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.Pause()
	}
	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) shutdownLocked(ctx context.Context) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Shutdown action", "device", pd)
	pd.cancelPreparationLocked()
	changed := pd.ActiveTrack != nil
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.Pause()
	}
	pd.retireActiveTrackLocked()
	pd.recoveryPosition = nil
	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) Skip(ctx context.Context, index int, offset int) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandSkip, Index: index, Offset: offset})
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

	if index != pd.PlaybackQueue.Index {
		pd.recoveryPosition = nil
		pd.retireActiveTrackLocked()
	}

	if pd.ActiveTrack == nil {
		track, token, current, err := pd.loadTrackLocked(ctx, index)
		if err != nil || !current {
			return pd.getStatus(), false, err
		}
		pd.assignTrack(track, token)
	}

	if pd.ActiveTrack != nil {
		err := pd.ActiveTrack.SetPosition(offset)
		if err != nil {
			log.Error(ctx, "error setting position", err)
			return pd.getStatus(), false, err
		}
		pd.recoveryPosition = nil
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
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandAdd, IDs: ids})
}

func (pd *playbackDevice) Insert(ctx context.Context, index int, ids []string) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandInsert, Index: index, IDs: ids})
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
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandClear})
}

func (pd *playbackDevice) clearCommandLocked(ctx context.Context) (DeviceStatus, bool, error) {
	changed := pd.ActiveTrack != nil || !pd.PlaybackQueue.IsEmpty()
	queueChanged := !pd.PlaybackQueue.IsEmpty()
	pd.clearLocked(ctx)
	if queueChanged {
		pd.incrementQueueVersionLocked()
	}
	return pd.getStatus(), changed, nil
}

// clearLocked contains the Clear logic and must be called with pd.mu held.
func (pd *playbackDevice) clearLocked(ctx context.Context) {
	log.Debug(ctx, "Processing Clear action", "device", pd)
	pd.cancelPreparationLocked()
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.Pause()
	}
	pd.retireActiveTrackLocked()
	pd.recoveryPosition = nil
	pd.PlaybackQueue.Clear()
}

func (pd *playbackDevice) Remove(ctx context.Context, index int) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandRemove, Index: index})
}

func (pd *playbackDevice) removeLocked(ctx context.Context, index int) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Remove action", "index", index, "device", pd)
	changed := index > -1 && index < pd.PlaybackQueue.Size()
	// Cancel preparation and close the output when removing the current item.
	if changed && pd.PlaybackQueue.Index == index {
		pd.cancelPreparationLocked()
		if pd.ActiveTrack != nil {
			pd.ActiveTrack.Pause()
		}
		pd.retireActiveTrackLocked()
		pd.recoveryPosition = nil
	}

	if index > -1 && index < pd.PlaybackQueue.Size() {
		pd.PlaybackQueue.Remove(index)
		pd.incrementQueueVersionLocked()
	} else {
		log.Error(ctx, "Index to remove out of range: "+fmt.Sprint(index))
	}
	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) Move(ctx context.Context, fromIndex, toIndex int) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandMove, From: fromIndex, To: toIndex})
}

func (pd *playbackDevice) moveLocked(ctx context.Context, fromIndex, toIndex int) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Move action", "from", fromIndex, "to", toIndex, "device", pd)
	if fromIndex < 0 || fromIndex >= pd.PlaybackQueue.Size() || toIndex < 0 || toIndex >= pd.PlaybackQueue.Size() {
		return pd.getStatus(), false, fmt.Errorf("index out of range: from=%d, to=%d, size=%d", fromIndex, toIndex, pd.PlaybackQueue.Size())
	}
	pd.PlaybackQueue.Move(fromIndex, toIndex)
	pd.incrementQueueVersionLocked()
	return pd.getStatus(), true, nil
}

func (pd *playbackDevice) Shuffle(ctx context.Context) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandShuffle})
}

func (pd *playbackDevice) shuffleLocked(ctx context.Context) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing Shuffle action", "device", pd)
	changed := pd.PlaybackQueue.Size() > 1
	if changed {
		pd.PlaybackQueue.Shuffle()
		pd.incrementQueueVersionLocked()
	}
	return pd.getStatus(), changed, nil
}

// SetGain is used to control the playback volume. A float value between 0.0 and 1.0.
func (pd *playbackDevice) SetGain(ctx context.Context, gain float32) (DeviceStatus, error) {
	return pd.executeCommand(ctx, PlaybackCommand{Action: CommandGain, Gain: gain})
}

func (pd *playbackDevice) setGainLocked(ctx context.Context, gain float32) (DeviceStatus, bool, error) {
	log.Debug(ctx, "Processing SetGain action", "newGain", gain, "device", pd)
	changed := pd.Gain != gain
	if pd.ActiveTrack != nil {
		pd.ActiveTrack.SetVolume(gain)
	}
	pd.Gain = gain
	return pd.getStatus(), changed, nil
}

func (pd *playbackDevice) executeCommand(ctx context.Context, command PlaybackCommand) (DeviceStatus, error) {
	pd.mu.Lock()
	return pd.finishCommandLocked(ctx, command)
}

// finishCommandLocked releases the device mutex before publishing state.
func (pd *playbackDevice) finishCommandLocked(ctx context.Context, command PlaybackCommand) (DeviceStatus, error) {
	status, changed, err := pd.commandLocked(ctx, command)
	var notify func()
	if err == nil && changed {
		notify = pd.stateChangeNotifierLocked()
	}
	pd.mu.Unlock()
	invokeCallback(notify)
	return status, err
}

func (pd *playbackDevice) commandLocked(ctx context.Context, command PlaybackCommand) (DeviceStatus, bool, error) {
	if pd.retiring || ctx.Err() != nil {
		return pd.getStatus(), false, context.Canceled
	}
	switch command.Action {
	case CommandSet:
		return pd.setLocked(ctx, command.IDs)
	case CommandStart:
		return pd.startLocked(ctx)
	case CommandPause:
		return pd.stopLocked(ctx)
	case CommandStop:
		return pd.shutdownLocked(ctx)
	case CommandSkip:
		return pd.skipLocked(ctx, command.Index, command.Offset)
	case CommandSeek:
		return pd.skipLocked(ctx, pd.PlaybackQueue.Index, command.Position)
	case CommandAdd, CommandInsert:
		var status DeviceStatus
		var err error
		if command.Action == CommandInsert {
			status, err = pd.insertLocked(ctx, command.Index, command.IDs)
		} else {
			status, err = pd.addLocked(ctx, command.IDs)
		}
		changed := len(command.IDs) > 0
		if err == nil && changed {
			pd.incrementQueueVersionLocked()
			status = pd.getStatus()
		}
		return status, changed, err
	case CommandClear:
		return pd.clearCommandLocked(ctx)
	case CommandRemove:
		return pd.removeLocked(ctx, command.Index)
	case CommandMove:
		return pd.moveLocked(ctx, command.From, command.To)
	case CommandShuffle:
		return pd.shuffleLocked(ctx)
	case CommandGain:
		return pd.setGainLocked(ctx, command.Gain)
	default:
		return pd.getStatus(), false, fmt.Errorf("unknown playback command: %s", command.Action)
	}
}

func (pd *playbackDevice) isPlaying() bool {
	return pd.ActiveTrack != nil && pd.ActiveTrack.IsPlaying()
}

func (pd *playbackDevice) trackSwitcherGoroutine() {
	log.Debug("Started trackSwitcher goroutine", "device", pd)
	for {
		select {
		case event := <-pd.PlaybackEvents:
			pd.handlePlaybackEvent(event)
		case <-pd.serviceCtx.Done():
			log.Debug("Stopping trackSwitcher goroutine", "device", pd.Name)
			return
		}
	}
}

func (pd *playbackDevice) handlePlaybackEvent(event model.PlaybackEvent) {
	pd.mu.Lock()
	if ready := pd.pendingPlaybackTokens[event.Token]; ready != nil {
		pd.mu.Unlock()
		select {
		case <-ready:
			pd.handlePlaybackEvent(event)
		case <-pd.serviceCtx.Done():
		}
		return
	}
	if event.Token == 0 || event.Token != pd.activePlaybackToken || pd.ActiveTrack == nil {
		pd.mu.Unlock()
		return
	}

	var notify func()
	switch event.Kind {
	case model.PlaybackEventNaturalEnd:
		pd.recoveryPosition = nil
		pd.failureReason = ""
		pd.retireActiveTrackLocked()
		if !pd.PlaybackQueue.IsAtLastElement() {
			pd.PlaybackQueue.IncreaseIndex()
			idx := pd.PlaybackQueue.Index
			log.Debug("Switching to next song", "index", idx)

			var (
				track   Track
				token   model.PlaybackToken
				current bool
				err     error
			)
			const maxNaturalEndRetries = 2
			for attempt := 0; attempt <= maxNaturalEndRetries; attempt++ {
				if attempt > 0 {
					pd.mu.Unlock()
					select {
					case <-time.After(time.Duration(attempt*250) * time.Millisecond):
					case <-pd.serviceCtx.Done():
						pd.mu.Lock()
						break
					}
					pd.mu.Lock()
					if pd.retiring || pd.serviceCtx.Err() != nil || pd.PlaybackQueue.Index != idx {
						break
					}
				}
				track, token, current, err = pd.loadTrackLocked(pd.serviceCtx, idx)
				if err == nil && current {
					break
				}
				if errors.Is(err, context.Canceled) || !current {
					break
				}
				log.Warn("Transient error switching track on natural end, will retry", "attempt", attempt+1, "error", err)
			}

			if err != nil || !current {
				if err != nil && !errors.Is(err, context.Canceled) {
					log.Error("Error switching track after retries", err)
					pd.failureReason = "track_switch_failed"
				}
			} else {
				pd.failureReason = ""
				pd.assignTrack(track, token)
				if pd.ActiveTrack != nil {
					pd.ActiveTrack.Unpause()
				}
			}
		} else {
			log.Debug("There is no song left in the playlist. Finish.")
		}
		notify = pd.stateChangeNotifierLocked()
	case model.PlaybackEventRetired, model.PlaybackEventFailure:
		if event.Position != nil {
			position := *event.Position
			pd.recoveryPosition = &position
		}
		if event.Kind == model.PlaybackEventFailure {
			log.Error("Playback output failed", "token", event.Token, "positionKnown", event.Position != nil, event.Err)
			pd.failureReason = "output_failed"
		}
		pd.retireActiveTrackLocked()
		notify = pd.stateChangeNotifierLocked()
	default:
		log.Warn("Ignoring unknown playback event", "kind", event.Kind, "token", event.Token)
	}
	pd.mu.Unlock()
	invokeCallback(notify)
}

// loadTrackLocked snapshots queue state before releasing the mutex for output creation.
// A superseded result is retired without replacing current playback authority.
func (pd *playbackDevice) loadTrackLocked(ctx context.Context, index int) (Track, model.PlaybackToken, bool, error) {
	if pd.retiring || pd.serviceCtx.Err() != nil {
		return nil, 0, false, context.Canceled
	}
	pd.PlaybackQueue.SetIndex(index)
	currentTrack := pd.PlaybackQueue.Current()
	if currentTrack == nil {
		return nil, 0, false, errors.New("could not get current track")
	}

	pd.cancelPreparationLocked()
	version := pd.preparationVersion
	pd.nextPlaybackToken++
	if pd.nextPlaybackToken == 0 {
		pd.nextPlaybackToken++
	}
	token := pd.nextPlaybackToken
	if pd.pendingPlaybackTokens == nil {
		pd.pendingPlaybackTokens = make(map[model.PlaybackToken]chan struct{})
	}
	ready := make(chan struct{})
	pd.pendingPlaybackTokens[token] = ready
	if pd.pendingPrepareCancels == nil {
		pd.pendingPrepareCancels = make(map[model.PlaybackToken]context.CancelFunc)
	}
	prepareCtx, cancel := context.WithCancel(pd.serviceCtx)
	pd.pendingPrepareCancels[token] = cancel
	pd.preparing.Add(1)
	defer pd.preparing.Done()
	mediaFile := *currentTrack
	events, deviceName := pd.PlaybackEvents, pd.DeviceName
	pd.mu.Unlock()
	track, err := newTrack(prepareCtx, events, token, deviceName, mediaFile)
	pd.mu.Lock()
	if version != pd.preparationVersion || ctx.Err() != nil || pd.serviceCtx.Err() != nil {
		pd.discardPendingTrack(token)
		closeTrack(track)
		if err == nil && ctx.Err() != nil {
			return nil, 0, false, ctx.Err()
		}
		return nil, 0, false, nil
	}
	if err != nil {
		pd.discardPendingTrack(token)
		closeTrack(track)
		return nil, 0, true, err
	}
	track.SetVolume(pd.Gain)
	return track, token, true, nil
}

// assignTrack safely replaces the active track while holding the mutex.
// If another goroutine set ActiveTrack during the unlock window, the
// orphaned process is closed before the new track is assigned.
func (pd *playbackDevice) assignTrack(newTrack Track, token model.PlaybackToken) {
	pd.retireActiveTrackLocked()
	pd.ActiveTrack = newTrack
	pd.activePlaybackToken = token
	pd.activePrepareCancel = pd.pendingPrepareCancels[token]
	delete(pd.pendingPrepareCancels, token)
	pd.discardPendingTrack(token)
}

func (pd *playbackDevice) retireActiveTrackLocked() {
	track := pd.ActiveTrack
	pd.ActiveTrack = nil
	pd.activePlaybackToken = 0
	if pd.activePrepareCancel != nil {
		pd.activePrepareCancel()
		pd.activePrepareCancel = nil
	}
	if track != nil {
		track.Close()
	}
}

func (pd *playbackDevice) cancelPreparationLocked() {
	pd.preparationVersion++
	for _, cancel := range pd.pendingPrepareCancels {
		cancel()
	}
}

func (pd *playbackDevice) retireServiceOutput(ctx context.Context) error {
	pd.mu.Lock()
	pd.retiring = true
	_, _, err := pd.shutdownLocked(ctx)
	pd.mu.Unlock()
	return err
}

func (pd *playbackDevice) discardPendingTrack(token model.PlaybackToken) {
	if cancel := pd.pendingPrepareCancels[token]; cancel != nil {
		cancel()
		delete(pd.pendingPrepareCancels, token)
	}
	if ready := pd.pendingPlaybackTokens[token]; ready != nil {
		delete(pd.pendingPlaybackTokens, token)
		close(ready)
	}
}

func closeTrack(t Track) {
	if t != nil {
		t.Close()
	}
}
