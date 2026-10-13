package mpv

// Audio-playback using mpv media-server. See mpv.io
// https://github.com/dexterlb/mpvipc
// https://mpv.io/manual/master/#json-ipc
// https://mpv.io/manual/master/#properties

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

const (
	trackReadyTimeout = 3 * time.Second
	trackCloseTimeout = 2 * time.Second
	positionObserver  = 1
)

type MpvTrack struct {
	MediaFile      model.MediaFile
	PlaybackEvents chan<- model.PlaybackEvent
	Token          model.PlaybackToken
	Conn           *ipcClient
	IPCSocketName  string
	Exe            *Executor

	ctx             context.Context
	closeOnce       sync.Once
	eventOnce       sync.Once
	retireRequested atomic.Bool
	positionMu      sync.RWMutex
	lastPosition    int
	hasLastPosition bool
}

// NewTrack starts one MPV output and returns after the audio output is ready.
// It sends one terminal event to events; callers must drain events for the track's lifetime.
func NewTrack(ctx context.Context, events chan<- model.PlaybackEvent, token model.PlaybackToken, deviceName string, mf model.MediaFile) (*MpvTrack, error) {
	if events == nil {
		return nil, errors.New("playback event channel is required")
	}
	if token == 0 {
		return nil, errors.New("playback token is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log.Debug("Loading track", "trackPath", mf.Path, "mediaType", mf.ContentType())

	if _, err := mpvCommand(); err != nil {
		return nil, err
	}

	tmpSocketName := socketName("mpv-ctrl-", ".socket")
	args := createMPVCommand(deviceName, mf.AbsolutePath(), tmpSocketName)
	if len(args) == 0 {
		return nil, fmt.Errorf("no mpv command arguments provided")
	}
	exe, err := start(ctx, args)
	if err != nil {
		log.Error("Error starting mpv process", err)
		return nil, err
	}
	cleanup := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), trackCloseTimeout)
		defer cancel()
		if err := exe.Stop(stopCtx); err != nil {
			log.Warn("Error stopping mpv after track initialization failed", err)
		}
		_ = exe.Close()
		removeSocket(tmpSocketName)
	}

	if err := waitForSocket(ctx, tmpSocketName, trackReadyTimeout, 25*time.Millisecond); err != nil {
		cleanup()
		return nil, err
	}
	conn, err := dialIPC(ctx, tmpSocketName)
	if err != nil {
		cleanup()
		return nil, err
	}
	track := &MpvTrack{
		MediaFile:      mf,
		PlaybackEvents: events,
		Token:          token,
		Conn:           conn,
		IPCSocketName:  tmpSocketName,
		Exe:            &exe,
		ctx:            ctx,
	}
	if err := track.waitForReady(); err != nil {
		_ = conn.Close()
		cleanup()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		track.retireRequested.Store(true)
		_ = conn.Close()
		cleanup()
		return nil, err
	}
	go track.monitorEvents()
	return track, nil
}

func (t *MpvTrack) waitForReady() error {
	ctx, cancel := context.WithTimeout(t.ctx, trackReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		output, outputErr := t.Conn.Call(ctx, "get_property", "current-ao")
		params, paramsErr := t.Conn.Call(ctx, "get_property", "audio-out-params")
		outputName, outputReady := output.(string)
		_, paramsReady := params.(map[string]any)
		if outputErr == nil && paramsErr == nil && outputReady && outputName != "" && paramsReady {
			position, err := t.Conn.Call(ctx, "get_property", "time-pos")
			if err == nil {
				t.updatePosition(position)
			}
			if _, err := t.Conn.Call(ctx, "observe_property", positionObserver, "time-pos"); err != nil {
				return fmt.Errorf("observe mpv position: %w", err)
			}
			return nil
		}
		if err := t.Conn.Err(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for mpv output readiness: %w", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("waiting for mpv output readiness: %w", ctx.Err())
		case <-t.Conn.Done():
			return t.Conn.Err()
		}
	}
}

func (t *MpvTrack) monitorEvents() {
	for event := range t.Conn.Events() {
		switch event.Name {
		case "property-change":
			if event.ID == positionObserver {
				t.updatePosition(event.Data)
			}
		case "end-file":
			t.handleEndFile(event)
			return
		}
	}

	if t.retireRequested.Load() || t.ctx.Err() != nil {
		t.terminate(false)
		t.emitTerminal(model.PlaybackEventRetired, nil)
		return
	}
	err := t.Conn.Err()
	if err == nil {
		err = errors.New("mpv IPC closed without an end-file event")
	}
	t.terminate(false)
	t.emitTerminal(model.PlaybackEventFailure, err)
}

func (t *MpvTrack) handleEndFile(event ipcEvent) {
	if t.retireRequested.Load() || event.Reason == "quit" || event.Reason == "stop" {
		t.retireRequested.Store(true)
		t.terminate(false)
		t.emitTerminal(model.PlaybackEventRetired, nil)
		return
	}
	if event.Reason == "eof" {
		t.emitTerminal(model.PlaybackEventNaturalEnd, nil)
		return
	}
	t.terminate(false)
	t.emitTerminal(model.PlaybackEventFailure, fmt.Errorf("mpv ended track: %s", event.Reason))
}

func (t *MpvTrack) emitTerminal(kind model.PlaybackEventKind, err error) {
	t.eventOnce.Do(func() {
		event := model.PlaybackEvent{Token: t.Token, Kind: kind, Position: t.positionSnapshot(), Err: err}
		go func() {
			select {
			case t.PlaybackEvents <- event:
			case <-t.ctx.Done():
				select {
				case t.PlaybackEvents <- event:
				default:
				}
			}
		}()
	})
}

func (t *MpvTrack) terminate(graceful bool) {
	t.closeOnce.Do(func() {
		if graceful && t.Conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			_, err := t.Conn.Call(ctx, "quit")
			cancel()
			if err != nil {
				log.Warn("Error sending quit command to mpv", err)
				graceful = false
			}
		}
		if t.Conn != nil {
			_ = t.Conn.Close()
		}
		if t.Exe != nil {
			if graceful {
				waitCtx, cancel := context.WithTimeout(context.Background(), trackCloseTimeout)
				err := t.Exe.Wait(waitCtx)
				cancel()
				if err != nil {
					log.Warn("mpv did not exit after quit; terminating process", err)
					stopCtx, stopCancel := context.WithTimeout(context.Background(), trackCloseTimeout)
					if stopErr := t.Exe.Stop(stopCtx); stopErr != nil {
						log.Error("Error terminating mpv process", stopErr)
					}
					stopCancel()
				}
			} else {
				stopCtx, cancel := context.WithTimeout(context.Background(), trackCloseTimeout)
				if err := t.Exe.Stop(stopCtx); err != nil {
					log.Error("Error terminating mpv process", err)
				}
				cancel()
			}
			_ = t.Exe.Close()
		}
		removeSocket(t.IPCSocketName)
	})
}

func (t *MpvTrack) String() string {
	return fmt.Sprintf("Name: %s, Socket: %s", t.MediaFile.Path, t.IPCSocketName)
}

// Used to control the playback volume. A float value between 0.0 and 1.0.
func (t *MpvTrack) SetVolume(value float32) {
	vol := int(value * 100)
	if _, err := t.call("set_property", "volume", vol); err != nil {
		log.Error("Error setting volume", "volume", value, "track", t, err)
		t.reportCommandFailure(err)
	}
}

func (t *MpvTrack) Unpause() {
	if _, err := t.call("set_property", "pause", false); err != nil {
		log.Error("Error unpausing track", "track", t, err)
		t.reportCommandFailure(err)
	}
}

func (t *MpvTrack) Pause() {
	if _, err := t.call("set_property", "pause", true); err != nil {
		log.Error("Error pausing track", "track", t, err)
		t.reportCommandFailure(err)
	}
}

func (t *MpvTrack) Close() {
	t.retireRequested.Store(true)
	t.terminate(true)
}

func (t *MpvTrack) call(command ...any) (any, error) {
	ctx, cancel := context.WithTimeout(t.ctx, ipcCallTimeout)
	defer cancel()
	return t.Conn.Call(ctx, command...)
}

func (t *MpvTrack) reportCommandFailure(err error) {
	if err == nil || strings.Contains(err.Error(), "mpv error: property unavailable") {
		return
	}
	if t.ctx.Err() != nil {
		t.retireRequested.Store(true)
		t.terminate(false)
		t.emitTerminal(model.PlaybackEventRetired, nil)
		return
	}
	t.terminate(false)
	t.emitTerminal(model.PlaybackEventFailure, err)
}

// Position returns the playback position in seconds.
func (t *MpvTrack) Position() int {
	position, err := t.call("get_property", "time-pos")
	if err == nil {
		if t.updatePosition(position) {
			return int(position.(float64))
		}
	} else if !strings.Contains(err.Error(), "mpv error: property unavailable") {
		log.Error("Error getting position in track", "track", t, err)
		t.reportCommandFailure(err)
	}
	return t.cachedPosition()
}

func (t *MpvTrack) SetPosition(offset int) error {
	log.Debug("Setting position", "offset", offset, "track", t)
	if current, known := t.positionSnapshotValue(); known && current == offset {
		return nil
	}
	if _, err := t.call("set_property", "time-pos", float64(offset)); err != nil {
		log.Error("Could not set the position in track", "track", t, "offset", offset, err)
		t.reportCommandFailure(err)
		return err
	}
	return nil
}

func (t *MpvTrack) IsPlaying() bool {
	pausing, err := t.call("get_property", "pause")
	if err != nil {
		log.Error("Problem getting paused status", "track", t, err)
		t.reportCommandFailure(err)
		return false
	}
	pause, ok := pausing.(bool)
	if !ok {
		log.Error("Could not cast pausing to boolean", "track", t, "value", pausing)
		return false
	}
	return !pause
}

func (t *MpvTrack) updatePosition(value any) bool {
	position, ok := value.(float64)
	if !ok {
		return false
	}
	t.positionMu.Lock()
	t.lastPosition = int(position)
	t.hasLastPosition = true
	t.positionMu.Unlock()
	return true
}

func (t *MpvTrack) positionSnapshotValue() (int, bool) {
	t.positionMu.RLock()
	defer t.positionMu.RUnlock()
	return t.lastPosition, t.hasLastPosition
}

func (t *MpvTrack) positionSnapshot() *int {
	position, known := t.positionSnapshotValue()
	if !known {
		return nil
	}
	return &position
}

func (t *MpvTrack) cachedPosition() int {
	position, _ := t.positionSnapshotValue()
	return position
}

func waitForSocket(ctx context.Context, path string, timeout time.Duration, pause time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(pause)
	defer ticker.Stop()

	for {
		fileInfo, err := os.Stat(path)
		if err == nil && fileInfo != nil && !fileInfo.IsDir() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout reached waiting for mpv socket: %s", timeout)
		case <-ticker.C:
		}
	}
}
