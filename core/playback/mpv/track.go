package mpv

// Audio-playback using mpv media-server. See mpv.io
// https://github.com/dexterlb/mpvipc
// https://mpv.io/manual/master/#json-ipc
// https://mpv.io/manual/master/#properties

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/navidrome/navidrome/conf"
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
	output          atomic.Pointer[pipeWireOutput]
	outputFaults    chan pipeWireFault
	closeOnce       sync.Once
	eventOnce       sync.Once
	retireRequested atomic.Bool
	eofPending      atomic.Bool
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
	target := pipeWireBluetoothTarget(deviceName)
	outputName := "navidrome-output-" + filepath.Base(tmpSocketName)
	var environment []string
	prepareCtx := ctx
	if target != "" {
		if conf.Server.Jukebox.OutputPreparationTimeout <= 0 || conf.Server.Jukebox.OutputPollInterval <= 0 {
			return nil, errors.New("Jukebox output preparation timeout and poll interval must be positive")
		}
		if conf.Server.Jukebox.OutputCheckInterval <= 0 || conf.Server.Jukebox.OutputCheckTimeout <= 0 {
			return nil, errors.New("Jukebox output check interval and timeout must be positive")
		}
		var cancel context.CancelFunc
		prepareCtx, cancel = context.WithTimeout(ctx, conf.Server.Jukebox.OutputPreparationTimeout)
		defer cancel()
		environment = pipeWireEnvironment(outputName)
		args = append(args, "--no-config", "--load-scripts=no", "--pause=yes", "--ao=pulse", "--audio-device="+deviceName, "--audio-fallback-to-null=no")
	}
	exe, err := start(ctx, args, environment...)
	if err != nil {
		log.Error("Error starting mpv process", err)
		return nil, err
	}
	// Drain stdout even when no caller reads it; exec.Cmd.Wait also waits for its output copier.
	go func() { _, _ = io.Copy(io.Discard, &exe) }()
	cleanup := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), trackCloseTimeout)
		defer cancel()
		if err := exe.Stop(stopCtx); err != nil {
			log.Warn("Error stopping mpv after track initialization failed", err)
		}
		_ = exe.Close()
		removeSocket(tmpSocketName)
	}

	if err := waitForSocket(prepareCtx, tmpSocketName, trackReadyTimeout, 25*time.Millisecond); err != nil {
		cleanup()
		return nil, err
	}
	conn, err := dialIPC(prepareCtx, tmpSocketName)
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
		outputFaults:   make(chan pipeWireFault, 1),
	}
	if target != "" {
		// Pulse stream creation waits for a peer, so establish links before AO readiness.
		output, err := preparePipeWireOutput(prepareCtx, ctx, exe.cmd.Process.Pid, outputName, target)
		if err != nil {
			track.terminate(false)
			return nil, err
		}
		track.output.Store(output)
	}
	if err := track.waitForReadyContext(prepareCtx); err != nil {
		track.terminate(false)
		return nil, err
	}
	if output := track.output.Load(); output != nil {
		paused, err := conn.Call(prepareCtx, "get_property", "pause")
		if err == nil && paused != true {
			err = errors.New("Bluetooth output was not prepared paused")
		}
		if err == nil {
			err = output.verify(prepareCtx)
		}
		if err == nil {
			err = output.arm(prepareCtx, ctx, track.outputFaults)
		}
		if err != nil {
			track.terminate(false)
			return nil, err
		}
	}
	if err := prepareCtx.Err(); err != nil {
		track.retireRequested.Store(true)
		track.terminate(false)
		return nil, err
	}
	go track.monitorEvents()
	return track, nil
}

func (t *MpvTrack) waitForReady() error {
	return t.waitForReadyContext(t.ctx)
}

func (t *MpvTrack) waitForReadyContext(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, trackReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		output, outputErr := t.Conn.Call(ctx, "get_property", "current-ao")
		params, paramsErr := t.Conn.Call(ctx, "get_property", "audio-out-params")
		outputName, outputReady := output.(string)
		format, paramsReady := params.(map[string]any)
		if outputErr == nil && paramsErr == nil && outputReady && outputName != "" && paramsReady {
			if output := t.output.Load(); output != nil {
				if err := output.verifyFormat(format); err != nil {
					return err
				}
			}
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
	for {
		output := t.output.Load()
		var ownerDone <-chan struct{}
		if output != nil {
			ownerDone = output.owner.done
		}
		select {
		case event, open := <-t.Conn.Events():
			if !open {
				err := t.Conn.Err()
				if err == nil {
					err = errors.New("mpv IPC closed without an end-file event")
				}
				t.finishFailure(err)
				return
			}
			switch event.Name {
			case "property-change":
				if event.ID == positionObserver {
					t.updatePosition(event.Data)
				}
			case "end-file":
				t.handleEndFile(event)
				return
			}
		case fault := <-t.outputFaults:
			current := t.output.Load()
			if current == nil || fault.generation != current.token {
				continue
			}
			t.finishFailure(fault.err)
			return
		case <-ownerDone:
			if output != t.output.Load() {
				continue
			}
			if t.retireRequested.Load() || t.ctx.Err() != nil {
				t.finishFailure(nil)
				return
			}
			err := output.ownerError()
			output.latchFault(err)
			t.finishFailure(err)
			return
		case <-t.ctx.Done():
			t.finishFailure(t.ctx.Err())
			return
		}
	}
}

func (t *MpvTrack) finishFailure(err error) {
	retired := t.retireRequested.Load() || t.ctx.Err() != nil
	if retired {
		t.retireRequested.Store(true)
	}
	t.terminate(false)
	if retired {
		t.emitTerminal(model.PlaybackEventRetired, nil)
	} else {
		t.emitTerminal(model.PlaybackEventFailure, err)
	}
}

func (t *MpvTrack) handleEndFile(event ipcEvent) {
	if t.retireRequested.Load() || event.Reason == "quit" || event.Reason == "stop" {
		t.retireRequested.Store(true)
		t.terminate(false)
		t.emitTerminal(model.PlaybackEventRetired, nil)
		return
	}
	if event.Reason == "eof" {
		if output := t.output.Load(); output != nil {
			t.eofPending.Store(true)
			if err := output.acceptNaturalEnd(t.ctx); err != nil {
				t.finishFailure(err)
				return
			}
			t.retireRequested.Store(true)
			t.emitTerminal(model.PlaybackEventNaturalEnd, nil)
			if err := output.close(); err != nil {
				log.Error("Error retiring PipeWire output after natural end", err)
			}
			return
		}
		t.emitTerminal(model.PlaybackEventNaturalEnd, nil)
		return
	}
	t.finishFailure(fmt.Errorf("mpv ended track: %s", event.Reason))
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
		if output := t.output.Load(); output != nil {
			if err := output.close(); err != nil {
				log.Error("Error retiring PipeWire output links", err)
			}
		}
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
	if t == nil {
		return "<nil>"
	}
	return fmt.Sprintf("Name: %s, Socket: %s", t.MediaFile.Path, t.IPCSocketName)
}

// Used to control the playback volume. A float value between 0.0 and 1.0.
func (t *MpvTrack) SetVolume(value float32) error {
	if t == nil {
		return errors.New("track is nil")
	}
	vol := int(value * 100)
	if _, err := t.call("set_property", "volume", vol); err != nil {
		log.Error("Error setting volume", "volume", value, "track", t, err)
		t.reportCommandFailure(err)
		return err
	}
	return nil
}

func (t *MpvTrack) Unpause() error {
	if t == nil {
		return errors.New("track is nil")
	}
	if _, err := t.call("set_property", "pause", false); err != nil {
		log.Error("Error unpausing track", "track", t, err)
		t.reportCommandFailure(err)
		return err
	}
	return nil
}

func (t *MpvTrack) Pause() error {
	if t == nil {
		return errors.New("track is nil")
	}
	if _, err := t.call("set_property", "pause", true); err != nil {
		log.Error("Error pausing track", "track", t, err)
		t.reportCommandFailure(err)
		return err
	}
	return nil
}

func (t *MpvTrack) Close() {
	if t == nil {
		return
	}
	t.retireRequested.Store(true)
	t.terminate(true)
}

func (t *MpvTrack) call(command ...any) (any, error) {
	ctx, cancel := context.WithTimeout(t.ctx, ipcCallTimeout)
	defer cancel()
	return t.Conn.Call(ctx, command...)
}

func (t *MpvTrack) reportProbeFailure(err error) {
	if err == nil || strings.Contains(err.Error(), "mpv error: property unavailable") || t.retireRequested.Load() || t.eofPending.Load() {
		return
	}
	if output := t.output.Load(); output != nil && output.expected.Load() {
		return
	}
	t.finishFailure(err)
}

func (t *MpvTrack) reportCommandFailure(err error) {
	if err == nil || t.retireRequested.Load() {
		return
	}
	if output := t.output.Load(); output != nil {
		if output.expected.Load() {
			return
		}
		output.latchFault(err)
	}
	t.finishFailure(err)
}

// Position returns the measured playback position rounded to the nearest second.
func (t *MpvTrack) Position() int {
	if t == nil {
		return 0
	}
	position, err := t.call("get_property", "time-pos")
	if err == nil {
		if t.updatePosition(position) {
			return int(math.Round(position.(float64)))
		}
	} else if !strings.Contains(err.Error(), "mpv error: property unavailable") {
		log.Error("Error getting position in track", "track", t, err)
		t.reportProbeFailure(err)
	}
	return t.cachedPosition()
}

func (t *MpvTrack) SetPosition(offset int) (err error) {
	if t == nil {
		return errors.New("track is nil")
	}
	log.Debug("Setting position", "offset", offset, "track", t)
	defer func() {
		if err != nil {
			log.Error("Could not set the position in track", "track", t, "offset", offset, err)
			t.reportCommandFailure(err)
		}
	}()
	current, err := t.call("get_property", "time-pos")
	if err != nil {
		return err
	}
	if current == float64(offset) {
		return nil
	}
	if err = t.preparePausedSeek(); err != nil {
		return err
	}
	_, err = t.call("seek", float64(offset), "absolute+exact")
	return err
}

func (t *MpvTrack) preparePausedSeek() error {
	paused, err := t.call("get_property", "pause")
	if err != nil {
		return err
	}
	if paused != true {
		return nil
	}
	if output := t.output.Load(); output != nil {
		// PipeWire Bluetooth streams maintain explicit pinned link bindings.
		// Reopening AO while paused destroys the Pulse stream, which MPV does not
		// recreate while idle/paused. Exact seek preserves stream/link authority
		// without pre-seek buffered delay, but must verify output bindings remain intact.
		ctx, cancel := context.WithTimeout(t.ctx, ipcCallTimeout)
		defer cancel()
		return output.verify(ctx)
	}
	// A paused output can retain pre-seek buffered delay in its position report.
	// Reopen it while still paused; this costs output preparation, not audible playback.
	if _, err := t.call("ao-reload"); err != nil {
		return err
	}
	return t.waitForReady()
}

func (t *MpvTrack) IsPlaying() bool {
	if t == nil {
		return false
	}
	pausing, err := t.call("get_property", "pause")
	if err != nil {
		log.Error("Problem getting paused status", "track", t, err)
		t.reportProbeFailure(err)
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
	t.lastPosition = int(math.Round(position))
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
