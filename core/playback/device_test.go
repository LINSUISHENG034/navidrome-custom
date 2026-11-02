package playback

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type mockTrack struct {
	playing          bool
	pauseCalls       int
	closeCalls       int
	position         int
	positionSetCalls int
	volumeValues     []float32
	muted            bool
	muteCalls        int
	onSetMute        func(bool) error
	onSetVolume      func(float32) error
	onSetPosition    func(int) error
	onPause          func() error
	onUnpause        func() error
	onClose          func()
}

func (m *mockTrack) IsPlaying() bool { return m.playing }
func (m *mockTrack) SetVolume(value float32) error {
	m.volumeValues = append(m.volumeValues, value)
	if m.onSetVolume != nil {
		return m.onSetVolume(value)
	}
	return nil
}
func (m *mockTrack) SetMute(value bool) error {
	m.muteCalls++
	m.muted = value
	if m.onSetMute != nil {
		return m.onSetMute(value)
	}
	return nil
}
func (m *mockTrack) Pause() error {
	m.pauseCalls++
	if m.onPause != nil {
		if err := m.onPause(); err != nil {
			return err
		}
	}
	m.playing = false
	return nil
}
func (m *mockTrack) Unpause() error {
	if m.onUnpause != nil {
		if err := m.onUnpause(); err != nil {
			return err
		}
	}
	m.playing = true
	return nil
}
func (m *mockTrack) Position() int { return m.position }
func (m *mockTrack) SetPosition(offset int) error {
	m.positionSetCalls++
	if m.onSetPosition != nil {
		if err := m.onSetPosition(offset); err != nil {
			return err
		}
	}
	m.position = offset
	return nil
}
func (m *mockTrack) Close() {
	m.closeCalls++
	if m.onClose != nil {
		m.onClose()
	}
}
func (m *mockTrack) String() string { return "mockTrack" }

func TestPlaybackDeviceSetMode(t *testing.T) {
	cases := []struct {
		name    string
		initial PlaybackMode
		mode    PlaybackMode
		invalid bool
	}{
		{"order", PlaybackModeShuffle, PlaybackModeOrder, false},
		{"order loop", PlaybackModeOrder, PlaybackModeRepeatList, false},
		{"single loop", PlaybackModeOrder, PlaybackModeRepeatTrack, false},
		{"shuffle", PlaybackModeOrder, PlaybackModeShuffle, false},
		{"same mode", PlaybackModeShuffle, PlaybackModeShuffle, false},
		{"empty value", PlaybackModeShuffle, "", true},
		{"unknown value", PlaybackModeShuffle, "repeat", true},
		{"wrong case", PlaybackModeShuffle, "ORDER", true},
		{"whitespace", PlaybackModeShuffle, " order", true},
	}
	for _, tc := range cases {
		for _, state := range []string{"empty", "paused", "playing"} {
			t.Run(tc.name+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
				device.mode = tc.initial
				device.stateSequence, device.queueVersion = 11, 7
				if state != "empty" {
					device.PlaybackQueue.Add(model.MediaFiles{{ID: "1"}, {ID: "1"}, {ID: "2"}})
					device.PlaybackQueue.SetIndex(1)
					device.ActiveTrack = &mockTrack{playing: state == "playing", position: 17}
					device.activePlaybackToken = 37
				}
				before, _ := device.Status(ctx)
				beforeScene := device.SceneFingerprint()
				itemIDs := device.PlaybackQueue.ItemIDs()
				track, token := device.ActiveTrack, device.activePlaybackToken
				var published []DeviceStatus
				device.onStateChange = func(status DeviceStatus) {
					if !device.mu.TryLock() {
						t.Error("mode callback ran under the device lock")
						return
					}
					device.mu.Unlock()
					published = append(published, status)
				}
				status, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandSetMode, Mode: tc.mode})
				if tc.invalid {
					if !errors.Is(err, ErrInvalidPlayMode) {
						t.Fatalf("invalid mode error: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				changed := !tc.invalid && tc.mode != tc.initial
				wantMode, wantStatus, wantNotifications := tc.initial, before, 0
				if changed {
					wantMode = tc.mode
					wantStatus.StateSequence++
					wantNotifications = 1
				}
				wantStatus.Mode = wantMode
				observed, _ := device.Status(ctx)
				if device.mode != wantMode || status != wantStatus || observed != wantStatus {
					t.Errorf("mode/state mismatch: mode=%q returned=%#v observed=%#v wantMode=%q wantStatus=%#v", device.mode, status, observed, wantMode, wantStatus)
				}
				if len(published) != wantNotifications || (changed && published[0] != wantStatus) {
					t.Errorf("mode publication mismatch: %v", published)
				}
				if (device.SceneFingerprint() != beforeScene) != changed {
					t.Error("scene revision did not match mode mutation")
				}
				if !slices.Equal(itemIDs, device.PlaybackQueue.ItemIDs()) || device.ActiveTrack != track || device.activePlaybackToken != token {
					t.Error("mode write changed queue identity or active output")
				}
				if active, ok := track.(*mockTrack); ok && (active.pauseCalls != 0 || active.closeCalls != 0 || active.positionSetCalls != 0 || active.muteCalls != 0 || len(active.volumeValues) != 0) {
					t.Error("mode write issued an output command")
				}
			})
		}
	}
}

func TestPlaybackDevicePlaybackInstanceIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	device.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/one.mp3"}, {ID: "2", Path: "/two.mp3"}})
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{}, nil
	}
	defer func() { _, _ = device.Shutdown(ctx); device.preparing.Wait(); newTrack = previousNewTrack }()

	started, err := device.Start(ctx)
	if err != nil || started.PlaybackInstanceID == "" {
		t.Fatalf("initial start did not issue playback instance: status=%#v err=%v", started, err)
	}
	first := started.PlaybackInstanceID
	paused, err := device.Stop(ctx)
	if err != nil || paused.PlaybackInstanceID != first {
		t.Fatalf("pause changed playback instance: status=%#v err=%v", paused, err)
	}
	resumed, err := device.Start(ctx)
	if err != nil || resumed.PlaybackInstanceID != first {
		t.Fatalf("resume changed playback instance: status=%#v err=%v", resumed, err)
	}
	selected, err := device.Skip(ctx, 1, 0)
	if err != nil || selected.PlaybackInstanceID == "" || selected.PlaybackInstanceID == first {
		t.Fatalf("new track did not issue a new playback instance: status=%#v err=%v", selected, err)
	}
	device.mu.Lock()
	device.mode = PlaybackModeRepeatTrack
	token := device.activePlaybackToken
	device.mu.Unlock()
	beforeLoop, _ := device.Status(ctx)
	device.PlaybackEvents <- model.PlaybackEvent{Token: token, Kind: model.PlaybackEventNaturalEnd}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		looped, _ := device.Status(ctx)
		if looped.PlaybackInstanceID != "" && looped.PlaybackInstanceID != beforeLoop.PlaybackInstanceID {
			break
		}
		time.Sleep(time.Millisecond)
	}
	looped, _ := device.Status(ctx)
	if looped.PlaybackInstanceID == "" || looped.PlaybackInstanceID == beforeLoop.PlaybackInstanceID {
		t.Fatalf("single-loop natural end did not issue a new instance: before=%#v after=%#v", beforeLoop, looped)
	}
	if _, err := device.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	cleared, _ := device.Status(ctx)
	if cleared.PlaybackInstanceID != "" {
		t.Fatalf("clear retained playback instance: %#v", cleared)
	}
}

func TestPlaybackDeviceNaturalEndPlaybackModes(t *testing.T) {
	cases := []struct {
		name              string
		mode              PlaybackMode
		current           int
		queueSize         int
		wantIndex         int
		wantPlaying       bool
		wantTrack         string
		wantSequenceDelta uint64
	}{
		{"order advances", PlaybackModeOrder, 0, 3, 1, true, "2", 2},
		{"order retains final and stops", PlaybackModeOrder, 2, 3, 2, false, "3", 1},
		{"order loop wraps", PlaybackModeRepeatList, 2, 3, 0, true, "1", 2},
		{"single loop replays current", PlaybackModeRepeatTrack, 1, 3, 1, true, "2", 2},
		{"shuffle excludes current", PlaybackModeShuffle, 1, 3, 0, true, "1", 2},
		{"order single item stops", PlaybackModeOrder, 0, 1, 0, false, "1", 1},
		{"order loop single item replays", PlaybackModeRepeatList, 0, 1, 0, true, "1", 2},
		{"single loop single item replays", PlaybackModeRepeatTrack, 0, 1, 0, true, "1", 2},
		{"shuffle single item replays", PlaybackModeShuffle, 0, 1, 0, true, "1", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			repo := tests.CreateMockMediaFileRepo()
			files := model.MediaFiles{{ID: "1", Path: "/1.mp3"}, {ID: "2", Path: "/2.mp3"}, {ID: "3", Path: "/3.mp3"}}
			if tc.queueSize == 1 {
				files = files[:1]
			}
			repo.SetData(files)
			server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
			device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
			if _, err := device.Set(ctx, []string{"1", "2", "3"}[:tc.queueSize]); err != nil {
				t.Fatal(err)
			}
			device.mu.Lock()
			device.mode = tc.mode
			device.PlaybackQueue.SetIndex(tc.current)
			if tc.mode == PlaybackModeShuffle && tc.queueSize > 1 {
				device.randomFn = func(int) int { return 0 }
			}
			device.mu.Unlock()
			previousNewTrack := newTrack
			newTrack = func(_ context.Context, events chan<- model.PlaybackEvent, token model.PlaybackToken, deviceName string, media model.MediaFile) (Track, error) {
				return &mockTrack{playing: true}, nil
			}
			t.Cleanup(func() {
				_, _ = device.Shutdown(ctx)
				device.preparing.Wait()
				newTrack = previousNewTrack
			})
			if _, err := device.Start(ctx); err != nil {
				t.Fatal(err)
			}
			device.mu.Lock()
			token := device.activePlaybackToken
			device.mu.Unlock()
			initial, err := device.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			device.PlaybackEvents <- model.PlaybackEvent{Token: token, Kind: model.PlaybackEventNaturalEnd}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				status, err := device.Status(ctx)
				if err != nil {
					t.Fatal(err)
				}
				device.mu.Lock()
				current := device.PlaybackQueue.Current()
				device.mu.Unlock()
				if status.StateSequence >= initial.StateSequence+tc.wantSequenceDelta && status.CurrentIndex == tc.wantIndex && status.Playing == tc.wantPlaying && current != nil && current.ID == tc.wantTrack {
					return
				}
				time.Sleep(time.Millisecond)
			}
			status, _ := device.Status(ctx)
			device.mu.Lock()
			current := device.PlaybackQueue.Current()
			device.mu.Unlock()
			t.Fatalf("natural end mismatch: status=%#v current=%#v", status, current)
		})
	}
}

func TestPlaybackDeviceStartPauseStateSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	device.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/track.mp3"}})
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{}, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		cancel()
		device.preparing.Wait()
		newTrack = previousNewTrack
	})
	var notifications []DeviceStatus
	device.onStateChange = func(status DeviceStatus) {
		notifications = append(notifications, status)
	}
	initial, err := device.Status(ctx)
	if err != nil || initial.StateSequence != 0 {
		t.Fatalf("fresh device sequence: status=%#v error=%v", initial, err)
	}
	steps := []struct {
		name     string
		command  func(context.Context) (DeviceStatus, error)
		sequence uint64
		playing  bool
		notifies int
	}{
		{"initial start", device.Start, 1, true, 1},
		{"already playing", device.Start, 1, true, 1},
		{"pause", device.Stop, 2, false, 2},
		{"already paused", device.Stop, 2, false, 2},
		{"resume", device.Start, 3, true, 3},
	}
	for _, step := range steps {
		status, err := step.command(ctx)
		if err != nil || status.StateSequence != step.sequence || status.Playing != step.playing {
			t.Fatalf("%s: status=%#v error=%v; want sequence=%d playing=%v", step.name, status, err, step.sequence, step.playing)
		}
		if status.QueueVersion != initial.QueueVersion {
			t.Fatalf("%s changed structural version: %#v", step.name, status)
		}
		if len(notifications) != step.notifies || notifications[len(notifications)-1] != status {
			t.Fatalf("%s callback mismatch: notifications=%#v status=%#v", step.name, notifications, status)
		}
	}
	device.mu.Lock()
	if err := device.ActiveTrack.SetPosition(17); err != nil {
		device.mu.Unlock()
		t.Fatal(err)
	}
	device.mu.Unlock()
	_, status, err := device.Get(ctx)
	if err != nil || status.StateSequence != 3 || status.Position != 17 || len(notifications) != 3 {
		t.Fatalf("position/read advanced semantic state: status=%#v error=%v notifications=%#v", status, err, notifications)
	}
	other := NewPlaybackDevice(ctx, nil, "Other", "pulse/other")
	otherStatus, err := other.Status(ctx)
	if err != nil || otherStatus.StateSequence != 0 {
		t.Fatalf("device sequence was shared: status=%#v error=%v", otherStatus, err)
	}
}

func TestPlaybackDeviceStartPauseFailureSequence(t *testing.T) {
	failure := errors.New("output rejected command")
	cases := []struct {
		name       string
		track      *mockTrack
		queue      bool
		factoryErr error
		pause      bool
		canceled   bool
		wantErr    error
	}{
		{name: "empty start"},
		{name: "load failure", queue: true, factoryErr: failure, wantErr: failure},
		{name: "initial unpause failure", queue: true, wantErr: failure},
		{name: "resume failure", track: &mockTrack{}, wantErr: failure},
		{name: "pause failure", track: &mockTrack{playing: true}, pause: true, wantErr: failure},
		{name: "canceled admission", track: &mockTrack{}, canceled: true, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
			if tc.track != nil {
				device.ActiveTrack = tc.track
			}
			if tc.queue {
				device.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/track.mp3"}})
			}
			if tc.track != nil {
				tc.track.onPause = func() error { return failure }
				tc.track.onUnpause = func() error { return failure }
			}
			previousNewTrack := newTrack
			newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
				if tc.factoryErr != nil {
					return nil, tc.factoryErr
				}
				return &mockTrack{onUnpause: func() error { return failure }}, nil
			}
			t.Cleanup(func() {
				cancel()
				device.preparing.Wait()
				newTrack = previousNewTrack
			})
			notifications := 0
			device.onStateChange = func(DeviceStatus) { notifications++ }
			if tc.canceled {
				cancel()
			}
			command := device.Start
			if tc.pause {
				command = device.Stop
			}
			status, err := command(ctx)
			if !errors.Is(err, tc.wantErr) || status.StateSequence != 0 || notifications != 0 {
				t.Fatalf("unsuccessful transition advanced sequence: status=%#v error=%v notifications=%d", status, err, notifications)
			}
			if status.Playing != tc.pause {
				t.Fatalf("failed transition changed playing state: %#v", status)
			}
		})
	}
}

func TestPlaybackDeviceFailureKeepsQueuePositionAndRejectsRetiredToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "1", Path: "/same.mp3"}, {ID: "2", Path: "/next.mp3"}})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1", "1", "2"}); err != nil {
		t.Fatalf("set queue: %v", err)
	}

	type preparedTrack struct {
		token model.PlaybackToken
		track *mockTrack
	}
	prepared := make(chan preparedTrack, 2)
	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		track := &mockTrack{}
		prepared <- preparedTrack{token: token, track: track}
		return track, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		cancel()
		newTrack = previousNewTrack
	})

	if status, err := device.Start(ctx); err != nil || !status.Playing {
		t.Fatalf("initial start: status=%#v, err=%v", status, err)
	}
	first := <-prepared
	position := 17
	device.PlaybackEvents <- model.PlaybackEvent{
		Token:    first.token,
		Kind:     model.PlaybackEventFailure,
		Position: &position,
		Err:      errors.New("IPC disconnected"),
	}
	waitDeviceStatus(t, device, ctx, DeviceStatus{
		CurrentIndex:       0,
		Playing:            false,
		Gain:               DefaultGain,
		DesiredGain:        DefaultGain,
		Position:           position,
		QueueVersion:       1,
		StateSequence:      3,
		PlaybackInstanceID: device.playbackInstanceID,
		Mode:               PlaybackModeOrder,
	})
	if first.track.closeCalls != 1 {
		t.Fatalf("failed output was not released: closes=%d", first.track.closeCalls)
	}

	if status, err := device.Start(ctx); err != nil || !status.Playing {
		t.Fatalf("resume after output failure: status=%#v, err=%v", status, err)
	}
	second := <-prepared
	if second.token == first.token {
		t.Fatalf("reused playback token for repeated media ID: %d", second.token)
	}
	if second.track.position != position {
		t.Fatalf("recovery position: got=%d, want=%d", second.track.position, position)
	}

	device.PlaybackEvents <- model.PlaybackEvent{Token: first.token, Kind: model.PlaybackEventNaturalEnd}
	device.PlaybackEvents <- model.PlaybackEvent{Token: first.token, Kind: model.PlaybackEventFailure, Err: errors.New("late fault")}
	assertDeviceStatusStable(t, device, ctx, DeviceStatus{
		CurrentIndex:       0,
		Playing:            true,
		Gain:               DefaultGain,
		DesiredGain:        DefaultGain,
		Position:           position,
		QueueVersion:       1,
		StateSequence:      4,
		PlaybackInstanceID: device.playbackInstanceID,
		Mode:               PlaybackModeOrder,
	}, 100*time.Millisecond)
	if second.track.closeCalls != 0 {
		t.Fatalf("stale event closed successor output: closes=%d", second.track.closeCalls)
	}

	retiredPosition := 20
	device.PlaybackEvents <- model.PlaybackEvent{
		Token:    second.token,
		Kind:     model.PlaybackEventRetired,
		Position: &retiredPosition,
	}
	waitDeviceStatus(t, device, ctx, DeviceStatus{
		CurrentIndex:       0,
		Playing:            false,
		Gain:               DefaultGain,
		DesiredGain:        DefaultGain,
		Position:           retiredPosition,
		QueueVersion:       1,
		StateSequence:      5,
		PlaybackInstanceID: device.playbackInstanceID,
		Mode:               PlaybackModeOrder,
	})
	if second.track.closeCalls != 1 {
		t.Fatalf("retired output was not released: closes=%d", second.track.closeCalls)
	}
}

func TestPlaybackDeviceUnknownFailurePositionDoesNotInventSeekOffset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1"}); err != nil {
		t.Fatalf("set queue: %v", err)
	}

	type preparedOutput struct {
		token model.PlaybackToken
		track *mockTrack
	}
	prepared := make(chan preparedOutput, 2)
	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		track := &mockTrack{}
		prepared <- preparedOutput{token: token, track: track}
		return track, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		cancel()
		newTrack = previousNewTrack
	})

	if _, err := device.Start(ctx); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	first := <-prepared
	device.PlaybackEvents <- model.PlaybackEvent{Token: first.token, Kind: model.PlaybackEventFailure, Err: errors.New("position unavailable")}
	waitDeviceStatus(t, device, ctx, DeviceStatus{
		CurrentIndex:       0,
		Playing:            false,
		Gain:               DefaultGain,
		DesiredGain:        DefaultGain,
		Position:           0,
		QueueVersion:       1,
		StateSequence:      3,
		PlaybackInstanceID: device.playbackInstanceID,
		Mode:               PlaybackModeOrder,
	})
	if first.track.closeCalls != 1 {
		t.Fatalf("failed output was not released: closes=%d", first.track.closeCalls)
	}

	if _, err := device.Start(ctx); err != nil {
		t.Fatalf("resume after output failure: %v", err)
	}
	second := <-prepared
	if second.track.positionSetCalls != 0 {
		t.Fatalf("unknown position was fabricated as a seek target: calls=%d", second.track.positionSetCalls)
	}
}

func waitDeviceStatus(t *testing.T, device *playbackDevice, ctx context.Context, expected DeviceStatus) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := device.Status(ctx)
		if err != nil {
			t.Fatalf("read playback status: %v", err)
		}
		if status == expected {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("playback status did not become %#v; got %#v", expected, status)
		}
	}
}

func assertDeviceStatusStable(t *testing.T, device *playbackDevice, ctx context.Context, expected DeviceStatus, duration time.Duration) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := device.Status(ctx)
		if err != nil {
			t.Fatalf("read playback status: %v", err)
		}
		if status != expected {
			t.Fatalf("playback status changed: expected %#v, got %#v", expected, status)
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return
		}
	}
}

func TestPlaybackDeviceInvokesStateChangeCallbackOnSkip(t *testing.T) {
	called := make(chan DeviceStatus, 1)
	pd := &playbackDevice{
		PlaybackQueue:  NewQueue(),
		PlaybackEvents: make(chan model.PlaybackEvent, 16),
		ActiveTrack:    &mockTrack{},
		onStateChange:  func(status DeviceStatus) { called <- status },
	}
	pd.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})

	status, err := pd.Skip(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("skip failed: %v", err)
	}

	select {
	case got := <-called:
		if got != status {
			t.Fatalf("expected callback status %#v, got %#v", status, got)
		}
	default:
		t.Fatal("expected state change callback to be invoked")
	}
}

type pendingPlaybackResult struct {
	status DeviceStatus
	err    error
}

type playbackEventEmitter struct {
	events chan<- model.PlaybackEvent
	token  model.PlaybackToken
}

func (e playbackEventEmitter) emit(kind model.PlaybackEventKind, position *int, err error) {
	e.events <- model.PlaybackEvent{Token: e.token, Kind: kind, Position: position, Err: err}
}

type pendingPlaybackHarness struct {
	ctx        context.Context
	device     *playbackDevice
	track      *mockTrack
	successor  *mockTrack
	entered    chan struct{}
	resolved   chan struct{}
	playedGain chan float32
	playedIDs  chan string
	ended      chan playbackEventEmitter
	release    func()
}

func newPendingPlaybackHarness(t *testing.T, blockedID string, ids []string) *pendingPlaybackHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "1", Path: "/a.mp3"}, {ID: "2", Path: "/b.mp3"}, {ID: "3", Path: "/c.mp3"}})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	h := &pendingPlaybackHarness{
		ctx:        ctx,
		device:     NewPlaybackDevice(ctx, server, "Speaker", "pulse/test"),
		track:      &mockTrack{},
		successor:  &mockTrack{},
		entered:    make(chan struct{}),
		resolved:   make(chan struct{}),
		playedGain: make(chan float32, 1),
		playedIDs:  make(chan string, 3),
		ended:      make(chan playbackEventEmitter, 1),
	}
	if _, err := h.device.Set(ctx, ids); err != nil {
		t.Fatalf("set queue: %v", err)
	}
	release := make(chan struct{})
	var releaseOnce, resolvedOnce, playedOnce sync.Once
	h.release = func() { releaseOnce.Do(func() { close(release) }) }
	h.track.onClose = func() { resolvedOnce.Do(func() { close(h.resolved) }) }
	h.track.onUnpause = func() error {
		playedOnce.Do(func() {
			gain := float32(0)
			if len(h.track.volumeValues) > 0 {
				gain = h.track.volumeValues[len(h.track.volumeValues)-1]
			}
			h.playedGain <- gain
		})
		resolvedOnce.Do(func() { close(h.resolved) })
		return nil
	}
	observe := func(output *mockTrack, id string) *mockTrack {
		onUnpause := output.onUnpause
		output.onUnpause = func() error {
			if onUnpause != nil {
				_ = onUnpause()
			}
			h.playedIDs <- id
			return nil
		}
		return output
	}
	previousNewTrack := newTrack
	blocked := false
	initial := &mockTrack{}
	newTrack = func(_ context.Context, events chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, mf model.MediaFile) (Track, error) {
		if mf.ID == blockedID {
			if !blocked {
				blocked = true
				output := observe(h.track, mf.ID)
				close(h.entered)
				<-release
				return output, nil
			}
			return observe(h.successor, mf.ID), nil
		}
		h.ended <- playbackEventEmitter{events: events, token: token}
		return observe(initial, mf.ID), nil
	}
	t.Cleanup(func() {
		_, _ = h.device.Shutdown(ctx)
		h.release()
		select {
		case <-h.entered:
			select {
			case <-h.resolved:
			case <-time.After(time.Second):
				t.Error("pending output did not resolve during cleanup")
			}
		default:
		}
		cancel()
		newTrack = previousNewTrack
	})
	return h
}

func (h *pendingPlaybackHarness) wait(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func (h *pendingPlaybackHarness) begin(t *testing.T, action func(context.Context) (DeviceStatus, error)) <-chan pendingPlaybackResult {
	t.Helper()
	done := make(chan pendingPlaybackResult, 1)
	go func() {
		status, err := action(h.ctx)
		done <- pendingPlaybackResult{status: status, err: err}
	}()
	h.wait(t, h.entered, "playback did not begin preparing the output")
	return done
}

func (h *pendingPlaybackHarness) finish(t *testing.T, done <-chan pendingPlaybackResult) DeviceStatus {
	t.Helper()
	h.release()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("preparation completion: %v", result.err)
		}
		return result.status
	case <-time.After(time.Second):
		t.Fatal("playback did not finish after releasing the output")
		return DeviceStatus{}
	}
}

func (h *pendingPlaybackHarness) assertRetired(t *testing.T, playing bool, ids []string) {
	t.Helper()
	queue, status, err := h.device.Get(h.ctx)
	if err != nil || status.Playing != playing {
		t.Fatalf("cancelled preparation changed playback: status=%#v, err=%v", status, err)
	}
	if h.track.playing || h.track.closeCalls != 1 {
		t.Fatalf("cancelled output was not retired: playing=%v, closes=%d", h.track.playing, h.track.closeCalls)
	}
	if playing && (!h.successor.playing || h.successor.closeCalls != 0) {
		t.Fatalf("cancelled output displaced successor: playing=%v, closes=%d", h.successor.playing, h.successor.closeCalls)
	}
	if len(queue) != len(ids) {
		t.Fatalf("unexpected authoritative queue length: got=%d, want=%d", len(queue), len(ids))
	}
	for i, id := range ids {
		if queue[i].ID != id {
			t.Fatalf("unexpected authoritative queue at %d: got=%s, want=%s", i, queue[i].ID, id)
		}
	}
}

func testPlaybackDeviceCancelsPendingStart(t *testing.T, stop func(*playbackDevice, context.Context) (DeviceStatus, error), resume bool) {
	t.Helper()
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	stopped, err := stop(h.device, h.ctx)
	if err != nil || stopped.Playing {
		t.Fatalf("stop while preparing: status=%#v, err=%v", stopped, err)
	}
	if resume {
		resumed, err := h.device.Start(h.ctx)
		if err != nil || !resumed.Playing {
			t.Fatalf("successor start: status=%#v, err=%v", resumed, err)
		}
	}
	if status := h.finish(t, done); status.Playing != resume {
		t.Fatalf("cancelled start returned the wrong playback state: %#v", status)
	}
	h.assertRetired(t, resume, []string{"1"})
}

func TestPlaybackDeviceClearCancelsPendingStart(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	if _, err := h.device.Clear(h.ctx); err != nil {
		t.Fatalf("clear while preparing: %v", err)
	}
	h.finish(t, done)
	h.assertRetired(t, false, nil)
}

func TestPlaybackDeviceSetCancelsPendingStart(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	if _, err := h.device.Set(h.ctx, []string{"3"}); err != nil {
		t.Fatalf("replace queue while preparing: %v", err)
	}
	h.finish(t, done)
	h.assertRetired(t, false, []string{"3"})
}

func testPlaybackDeviceCancelsPendingSkip(t *testing.T, stop func(*playbackDevice, context.Context) (DeviceStatus, error), resume bool) {
	t.Helper()
	h := newPendingPlaybackHarness(t, "2", []string{"1", "2"})
	if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
		t.Fatalf("initial start: status=%#v, err=%v", status, err)
	}
	before, _ := h.device.Status(h.ctx)
	done := h.begin(t, func(ctx context.Context) (DeviceStatus, error) { return h.device.Skip(ctx, 1, 0) })
	during, _ := h.device.Status(h.ctx)
	if during.StateSequence != before.StateSequence+1 || during.QueueVersion != before.QueueVersion {
		t.Errorf("pending skip did not expose its committed ordering: before=%#v during=%#v", before, during)
	}
	if status, err := stop(h.device, h.ctx); err != nil || status.Playing {
		t.Fatalf("stop while skipping: status=%#v, err=%v", status, err)
	}
	if resume {
		if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
			t.Fatalf("successor start: status=%#v, err=%v", status, err)
		}
	}
	winner, _ := h.device.Status(h.ctx)
	if status := h.finish(t, done); status != winner || status.Playing != resume {
		t.Fatalf("cancelled skip changed the winner's state or ordering: returned=%#v winner=%#v", status, winner)
	}
	h.assertRetired(t, resume, []string{"1", "2"})
}

func TestPlaybackDeviceStopCancelsPendingSkip(t *testing.T) {
	testPlaybackDeviceCancelsPendingSkip(t, (*playbackDevice).Stop, false)
}

func TestPlaybackDeviceShutdownCancelsPendingSkip(t *testing.T) {
	testPlaybackDeviceCancelsPendingSkip(t, (*playbackDevice).Shutdown, false)
}

func testPlaybackDeviceCancelsPendingAdvance(t *testing.T, resume bool) {
	t.Helper()
	h := newPendingPlaybackHarness(t, "2", []string{"1", "2"})
	if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
		t.Fatalf("initial start: status=%#v, err=%v", status, err)
	}
	var ended playbackEventEmitter
	select {
	case ended = <-h.ended:
	case <-time.After(time.Second):
		t.Fatal("initial output did not expose its natural-end event")
	}
	ended.emit(model.PlaybackEventNaturalEnd, nil, nil)
	h.wait(t, h.entered, "natural advance did not begin preparing the next output")
	if status, err := h.device.Stop(h.ctx); err != nil || status.Playing {
		t.Fatalf("stop while advancing: status=%#v, err=%v", status, err)
	}
	if resume {
		if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
			t.Fatalf("successor start: status=%#v, err=%v", status, err)
		}
	}
	h.release()
	h.wait(t, h.resolved, "natural-advance output did not resolve")
	h.assertRetired(t, resume, []string{"1", "2"})
}

func TestPlaybackDeviceStopCancelsPendingNaturalAdvance(t *testing.T) {
	testPlaybackDeviceCancelsPendingAdvance(t, false)
}

func TestPlaybackDeviceCancelledSkipDoesNotDisplaceSuccessor(t *testing.T) {
	testPlaybackDeviceCancelsPendingSkip(t, (*playbackDevice).Stop, true)
}

func TestPlaybackDeviceCancelledNaturalAdvanceDoesNotDisplaceSuccessor(t *testing.T) {
	testPlaybackDeviceCancelsPendingAdvance(t, true)
}

func TestPlaybackDevicePendingStartUsesLatestGainBeforeUnpause(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	if status, err := h.device.SetGain(h.ctx, 0.37); err != nil || status.DesiredGain != float32(0.37) {
		t.Fatalf("gain while preparing: status=%#v, err=%v", status, err)
	}
	if status := h.finish(t, done); !status.Playing || status.Gain != float32(0.37) {
		t.Fatalf("start did not retain the latest gain: %#v", status)
	}
	select {
	case gain := <-h.playedGain:
		if gain != float32(0.37) {
			t.Fatalf("first playback used stale gain: %v", gain)
		}
	case <-time.After(time.Second):
		t.Fatal("output did not expose its gain at first playback")
	}
}

func TestPlaybackDeviceRemoveCancelsPreparationForCurrentItem(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1", "2"})
	done := h.begin(t, h.device.Start)
	if _, err := h.device.Remove(h.ctx, 0); err != nil {
		t.Fatalf("remove current item while preparing: %v", err)
	}
	h.finish(t, done)
	queue, status, err := h.device.Get(h.ctx)
	if err != nil || len(queue) != 1 || queue[0].ID != "2" {
		t.Fatalf("remove did not retain the next queue item: length=%d, status=%#v, err=%v", len(queue), status, err)
	}
	select {
	case playedID := <-h.playedIDs:
		t.Fatalf("deleted prepared content started: output=%s, current queue item=%s", playedID, queue[0].ID)
	default:
	}
	h.assertRetired(t, false, []string{"2"})
	if resumed, err := h.device.Start(h.ctx); err != nil || !resumed.Playing {
		t.Fatalf("start retained queue item: status=%#v, err=%v", resumed, err)
	}
	queue, status, err = h.device.Get(h.ctx)
	if err != nil || status.CurrentIndex < 0 || status.CurrentIndex >= len(queue) {
		t.Fatalf("retained item status: status=%#v, length=%d, err=%v", status, len(queue), err)
	}
	select {
	case playedID := <-h.playedIDs:
		if playedID != "2" || playedID != queue[status.CurrentIndex].ID {
			t.Fatalf("output differs from current queue: output=%s, queue=%s", playedID, queue[status.CurrentIndex].ID)
		}
	case <-time.After(time.Second):
		t.Fatal("retained item did not reach the output")
	}
}

func TestPlaybackDeviceStopCancelsPendingStart(t *testing.T) {
	testPlaybackDeviceCancelsPendingStart(t, (*playbackDevice).Stop, false)
}

func TestPlaybackDeviceShutdownCancelsPendingStart(t *testing.T) {
	testPlaybackDeviceCancelsPendingStart(t, (*playbackDevice).Shutdown, false)
}

func TestPlaybackDeviceCancelledStartDoesNotDisplaceSuccessor(t *testing.T) {
	testPlaybackDeviceCancelsPendingStart(t, (*playbackDevice).Stop, true)
}

var _ = Describe("playbackDevice concurrency", func() {
	It("handles concurrent Status and Clear calls without panic", func() {
		pd := &playbackDevice{
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
		}

		ctx := context.Background()
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				_, _ = pd.Status(ctx)
			}()
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				_, _ = pd.Clear(ctx)
			}()
		}
		wg.Wait()
		Expect(true).To(BeTrue()) // reaching here = no panic/race
	})

	It("Stop pauses but keeps the active track resumable", func() {
		track := &mockTrack{playing: true, position: 17}
		pd := &playbackDevice{
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Stop(context.Background())

		Expect(err).ToNot(HaveOccurred())
		Expect(track.pauseCalls).To(Equal(1))
		Expect(track.closeCalls).To(Equal(0))
		Expect(pd.ActiveTrack).To(BeIdenticalTo(track))
		Expect(status.Playing).To(BeFalse())
	})

	It("Shutdown pauses, closes, and releases the active track", func() {
		track := &mockTrack{playing: true, position: 23}
		pd := &playbackDevice{
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Shutdown(context.Background())

		Expect(err).ToNot(HaveOccurred())
		Expect(track.pauseCalls).To(Equal(1))
		Expect(track.closeCalls).To(Equal(1))
		Expect(pd.ActiveTrack).To(BeNil())
		Expect(status.Playing).To(BeFalse())
		Expect(status.Position).To(Equal(0))
	})

	It("SetGain propagates synchronous IPC error, retains desired gain, but keeps confirmed gain unchanged", func() {
		errIPC := errors.New("mpv ipc: set_property volume failed")
		track := &mockTrack{
			playing: true,
			onSetVolume: func(float32) error {
				return errIPC
			},
		}
		pd := &playbackDevice{
			serviceCtx:     context.Background(),
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
			Gain:           0.64,
			DesiredGain:    0.64,
		}

		status, err := pd.SetGain(context.Background(), 0.8)

		Expect(err).To(MatchError(errIPC))
		Expect(pd.DesiredGain).To(Equal(float32(0.8)))
		Expect(pd.Gain).To(Equal(float32(0.64)))
		Expect(status.Gain).To(Equal(float32(0.64)))
	})

	It("loadTrackLocked confirms initial gain before first Unpause and cleans up pending track on volume failure", func() {
		errVol := errors.New("mpv ipc: initial volume setting failed")
		closed := false
		mockNewTrack := &mockTrack{
			onSetVolume: func(float32) error {
				return errVol
			},
			onClose: func() {
				closed = true
			},
		}
		previousNewTrack := newTrack
		newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
			return mockNewTrack, nil
		}
		DeferCleanup(func() { newTrack = previousNewTrack })

		ctx := context.Background()
		repo := tests.CreateMockMediaFileRepo()
		repo.SetData(model.MediaFiles{{ID: "t1", Path: "/song.mp3"}})
		server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
		pd := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
		pd.DesiredGain = 0.5
		pd.PlaybackQueue.Add(model.MediaFiles{{ID: "t1", Path: "/song.mp3"}})

		status, err := pd.Start(ctx)

		Expect(err).To(MatchError(errVol))
		Expect(closed).To(BeTrue())
		Expect(pd.ActiveTrack).To(BeNil())
		Expect(status.Playing).To(BeFalse())
	})

	It("Stop synchronously propagates Pause error and retains recoverable paused-track semantics without destroying track", func() {
		errPause := errors.New("mpv ipc: pause command timed out")
		track := &mockTrack{
			playing: true,
			onPause: func() error {
				return errPause
			},
		}
		pd := &playbackDevice{
			serviceCtx:     context.Background(),
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Stop(context.Background())

		Expect(err).To(MatchError(errPause))
		Expect(pd.ActiveTrack).To(BeIdenticalTo(track))
		Expect(track.closeCalls).To(Equal(0))
		Expect(status.Playing).To(BeTrue())
	})

	It("Start synchronously propagates Unpause error without producing false Playing state", func() {
		errUnpause := errors.New("mpv ipc: unpause rejected")
		track := &mockTrack{
			playing: false,
			onUnpause: func() error {
				return errUnpause
			},
		}
		pd := &playbackDevice{
			serviceCtx:     context.Background(),
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Start(context.Background())

		Expect(err).To(MatchError(errUnpause))
		Expect(status.Playing).To(BeFalse())
		Expect(pd.isPlaying()).To(BeFalse())
	})

	It("Deduplicates synchronous failure and subsequent asynchronous PlaybackEventFailure without double advance", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		errUnpause := errors.New("mpv ipc: unpause socket error")
		token := model.PlaybackToken(42)
		track := &mockTrack{
			playing: false,
			onUnpause: func() error {
				return errUnpause
			},
		}
		pd := &playbackDevice{
			serviceCtx:          ctx,
			PlaybackQueue:       NewQueue(),
			PlaybackEvents:      make(chan model.PlaybackEvent, 16),
			ActiveTrack:         track,
			activePlaybackToken: token,
		}

		// Synchronous failure
		status, err := pd.Start(ctx)
		Expect(err).To(MatchError(errUnpause))
		Expect(status.Playing).To(BeFalse())

		// Asynchronous event for the same token arrives later
		fingerprintBeforeFailure := pd.SceneFingerprint()
		pos := 10
		pd.handlePlaybackEvent(model.PlaybackEvent{
			Token:    token,
			Kind:     model.PlaybackEventFailure,
			Position: &pos,
			Err:      errUnpause,
		})

		// Active track retired safely, recovery position preserved, no panic or second advance
		Expect(pd.ActiveTrack).To(BeNil())
		Expect(pd.recoveryPosition).ToNot(BeNil())
		Expect(*pd.recoveryPosition).To(Equal(10))
		Expect(pd.SceneFingerprint()).ToNot(Equal(fingerprintBeforeFailure))
	})
})

func TestPlaybackDeviceMuteRemainsIndependentAndPrecedesFirstUnpause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/mute-test")
	track := &mockTrack{}
	device.ActiveTrack = track
	device.Gain = .64
	device.DesiredGain = .64

	status, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandMute, Muted: true})
	if err != nil || !status.Muted || status.Gain != .64 || !track.muted {
		t.Fatalf("mute changed gain or was not confirmed: status=%#v track=%#v err=%v", status, track, err)
	}
	status, err = device.executeCommand(ctx, PlaybackCommand{Action: CommandGain, Gain: .2})
	if err != nil || !status.Muted || status.Gain != .2 {
		t.Fatalf("gain adjustment destroyed mute: %#v err=%v", status, err)
	}
	status, err = device.executeCommand(ctx, PlaybackCommand{Action: CommandMute, Muted: false})
	if err != nil || status.Muted || status.Gain != .2 || track.muted {
		t.Fatalf("unmute did not preserve current gain: %#v track=%#v err=%v", status, track, err)
	}

	sequence := []string{}
	initial := &mockTrack{
		onSetVolume: func(value float32) error { sequence = append(sequence, "volume"); return nil },
		onSetMute:   func(value bool) error { sequence = append(sequence, "mute"); return nil },
		onUnpause:   func() error { sequence = append(sequence, "unpause"); return nil },
	}
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return initial, nil
	}
	defer func() { newTrack = previousNewTrack }()
	device.ActiveTrack = nil
	device.Muted = true
	device.DesiredGain = .64
	device.PlaybackQueue.Set(model.MediaFiles{{ID: "mute-track", Path: "/mute.mp3"}})
	status, err = device.Start(ctx)
	if err != nil || !status.Playing || !status.Muted {
		t.Fatalf("initial muted track did not start with authoritative state: %#v err=%v", status, err)
	}
	if len(sequence) != 3 || sequence[0] != "volume" || sequence[1] != "mute" || sequence[2] != "unpause" {
		t.Fatalf("initial output commands were not confirmed before unpause: %v", sequence)
	}
}

func TestPlaybackDevicePositionClockInterpolationDoesNotChangeSceneFingerprint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	t.Cleanup(func() { _, _ = device.Shutdown(ctx) })
	device.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/1.mp3"}, {ID: "2", Path: "/2.mp3"}})
	track := &mockTrack{playing: true, position: 10}
	device.mu.Lock()
	device.ActiveTrack = track
	device.mu.Unlock()

	fingerprint := device.SceneFingerprint()
	queueVersion := device.queueVersion

	// Position-clock interpolation: the track clock advances without any
	// semantic queue, item, play/pause, mode, owner, or binding change.
	track.position = 25
	status, err := device.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Position != 25 {
		t.Fatalf("position-clock interpolation not visible in status: %d", status.Position)
	}
	if current := device.SceneFingerprint(); current != fingerprint {
		t.Fatalf("position-clock interpolation changed scene fingerprint: %s -> %s", fingerprint, current)
	}
	if device.queueVersion != queueVersion {
		t.Fatalf("position-clock interpolation changed queue version: %d -> %d", queueVersion, device.queueVersion)
	}
}

type anchorMoveFixture struct {
	ctx     context.Context
	device  *playbackDevice
	itemIDs []string
	playing *mockTrack
}

func newAnchorMoveFixture(t *testing.T) *anchorMoveFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{
		{ID: "1", Path: "/one.mp3"},
		{ID: "2", Path: "/two.mp3"},
		{ID: "3", Path: "/three.mp3"},
	})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1", "1", "2", "3"}); err != nil {
		t.Fatalf("set queue: %v", err)
	}

	fixture := &anchorMoveFixture{ctx: ctx, device: device, itemIDs: device.PlaybackQueue.ItemIDs()}
	if len(fixture.itemIDs) != 4 {
		t.Fatalf("queue size: %d", len(fixture.itemIDs))
	}
	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		return fixture.newTrack(), nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		cancel()
		newTrack = previousNewTrack
	})
	return fixture
}

func (f *anchorMoveFixture) newTrack() *mockTrack {
	f.playing = &mockTrack{}
	return f.playing
}

func (f *anchorMoveFixture) start(t *testing.T) {
	t.Helper()
	if status, err := f.device.Start(f.ctx); err != nil || !status.Playing {
		t.Fatalf("start: status=%#v err=%v", status, err)
	}
}

func (f *anchorMoveFixture) anchorMove(command PlaybackCommand) (DeviceStatus, error) {
	command.Action = CommandMove
	return f.device.executeCommand(f.ctx, command)
}

func TestPlaybackDeviceStructuralStateSequence(t *testing.T) {
	fixture := newAnchorMoveFixture(t)
	device, ctx := fixture.device, fixture.ctx
	before, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var notifications []DeviceStatus
	device.onStateChange = func(status DeviceStatus) { notifications = append(notifications, status) }
	steps := []struct {
		name         string
		run          func() (DeviceStatus, error)
		versionStep  int
		sequenceStep uint64
		playing      bool
	}{
		{"add", func() (DeviceStatus, error) { return device.Add(ctx, []string{"1"}) }, 1, 1, false},
		{"empty add", func() (DeviceStatus, error) { return device.Add(ctx, nil) }, 0, 0, false},
		{"insert", func() (DeviceStatus, error) { return device.Insert(ctx, 1, []string{"2"}) }, 1, 1, false},
		{"anchor move", func() (DeviceStatus, error) {
			return fixture.anchorMove(PlaybackCommand{QueueItemID: fixture.itemIDs[3], AnchorQueueItemID: fixture.itemIDs[0], Placement: MovePlacementBefore})
		}, 1, 1, false},
		{"legacy move", func() (DeviceStatus, error) { return device.Move(ctx, 0, 1) }, 1, 1, false},
		{"legacy shuffle", func() (DeviceStatus, error) { return device.Shuffle(ctx) }, 1, 1, false},
		{"remove non-current", func() (DeviceStatus, error) {
			index := 0
			if device.PlaybackQueue.Index == index {
				index = 1
			}
			return device.Remove(ctx, index)
		}, 1, 1, false},
		{"clear", func() (DeviceStatus, error) { return device.Clear(ctx) }, 1, 1, false},
		{"empty clear", func() (DeviceStatus, error) { return device.Clear(ctx) }, 0, 0, false},
		{"set", func() (DeviceStatus, error) { return device.Set(ctx, []string{"1", "2"}) }, 1, 1, false},
		{"start", func() (DeviceStatus, error) { return device.Start(ctx) }, 0, 1, true},
		{"pause", func() (DeviceStatus, error) { return device.Stop(ctx) }, 0, 1, false},
		{"add after playback transitions", func() (DeviceStatus, error) { return device.Add(ctx, []string{"3"}) }, 1, 1, false},
	}
	for _, step := range steps {
		count := len(notifications)
		status, err := step.run()
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if status.QueueVersion != before.QueueVersion+step.versionStep || status.StateSequence != before.StateSequence+step.sequenceStep || status.Playing != step.playing {
			t.Fatalf("%s: before=%#v after=%#v; want version delta=%d sequence delta=%d playing=%v", step.name, before, status, step.versionStep, step.sequenceStep, step.playing)
		}
		if step.sequenceStep != 0 {
			count++
			if len(notifications) != count || notifications[count-1] != status {
				t.Fatalf("%s notification mismatch: status=%#v notifications=%#v", step.name, status, notifications)
			}
		} else if len(notifications) != count {
			t.Fatalf("%s no-op emitted a notification: %#v", step.name, notifications)
		}
		observed, err := device.Status(ctx)
		if err != nil || observed != status {
			t.Fatalf("%s returned a stale status: returned=%#v observed=%#v error=%v", step.name, status, observed, err)
		}
		before = status
	}
}

func TestPlaybackDeviceResolutionFailurePreservesScene(t *testing.T) {
	for _, state := range []struct {
		name    string
		playing bool
	}{
		{"paused", false},
		{"playing", true},
	} {
		t.Run(state.name, func(t *testing.T) {
			for _, command := range []struct {
				name string
				run  func(*playbackDevice, context.Context, []string) (DeviceStatus, error)
			}{
				{"set", (*playbackDevice).Set},
				{"add", (*playbackDevice).Add},
				{"insert", func(device *playbackDevice, ctx context.Context, ids []string) (DeviceStatus, error) {
					return device.Insert(ctx, 1, ids)
				}},
			} {
				t.Run(command.name, func(t *testing.T) {
					for _, input := range []struct {
						name string
						ids  []string
					}{
						{"first ID missing", []string{"missing"}},
						{"later ID missing", []string{"1", "missing"}},
					} {
						t.Run(input.name, func(t *testing.T) {
							assertResolutionFailurePreservesScene(t, state.playing, command.run, input.ids)
						})
					}
				})
			}
		})
	}
}

func assertResolutionFailurePreservesScene(t *testing.T, playing bool, run func(*playbackDevice, context.Context, []string) (DeviceStatus, error), ids []string) {
	t.Helper()
	fixture := newAnchorMoveFixture(t)
	fixture.start(t)
	device, ctx, track := fixture.device, fixture.ctx, fixture.playing
	if !playing {
		if _, err := device.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	track.position = 17
	before, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	itemIDs := device.PlaybackQueue.ItemIDs()
	token, preparation, scene := device.activePlaybackToken, device.preparationVersion, device.sceneRevision
	pauseCalls, closeCalls := track.pauseCalls, track.closeCalls
	notifications := 0
	device.onStateChange = func(DeviceStatus) { notifications++ }
	t.Cleanup(func() { device.onStateChange = nil })

	returned, err := run(device, ctx, ids)
	if err == nil {
		t.Fatal("expected media resolution failure")
	}
	after, err := device.Status(ctx)
	if err != nil || after != before || returned != before {
		t.Errorf("resolution failure did not preserve authoritative status: before=%#v returned=%#v after=%#v error=%v", before, returned, after, err)
	}
	if !slices.Equal(itemIDs, device.PlaybackQueue.ItemIDs()) || device.ActiveTrack != track || device.activePlaybackToken != token {
		t.Errorf("resolution failure changed queue identity or active output: before IDs=%v after IDs=%v", itemIDs, device.PlaybackQueue.ItemIDs())
	}
	if device.preparationVersion != preparation || device.sceneRevision != scene {
		t.Errorf("resolution failure changed preparation or scene authority: preparation %d -> %d, scene %d -> %d", preparation, device.preparationVersion, scene, device.sceneRevision)
	}
	if track.pauseCalls != pauseCalls || track.closeCalls != closeCalls || notifications != 0 {
		t.Errorf("resolution failure caused output or publication effects: pauses %d -> %d closes %d -> %d notifications=%d", pauseCalls, track.pauseCalls, closeCalls, track.closeCalls, notifications)
	}
}

type removalFailureScene struct {
	status      DeviceStatus
	itemIDs     []string
	track       Track
	token       model.PlaybackToken
	preparation uint64
	revision    uint64
}

func captureRemovalFailureScene(fixture *anchorMoveFixture) removalFailureScene {
	device := fixture.device
	device.mu.Lock()
	defer device.mu.Unlock()
	return removalFailureScene{
		status: device.getStatus(), itemIDs: device.PlaybackQueue.ItemIDs(),
		track: device.ActiveTrack, token: device.activePlaybackToken,
		preparation: device.preparationVersion, revision: device.sceneRevision,
	}
}

type manualNavigationCase struct {
	name     string
	action   string
	size     int
	current  int
	position int
	ordered  int
	shuffled int
	fail     bool
}

func TestPlaybackDeviceManualModeNavigation(t *testing.T) {
	cases := []manualNavigationCase{
		{"next middle", CommandNext, 4, 1, 0, 2, 0, false},
		{"next wraps", CommandNext, 4, 3, 17, 0, 0, false},
		{"previous middle", CommandPrev, 4, 1, 0, 0, 0, false},
		{"previous wraps at threshold", CommandPrev, 4, 0, 1, 3, 1, false},
		{"previous rewinds above threshold", CommandPrev, 4, 1, 2, 1, 1, false},
		{"next single", CommandNext, 1, 0, 4, 0, 0, false},
		{"previous single", CommandPrev, 1, 0, 1, 0, 0, false},
		{"previous single rewind", CommandPrev, 1, 0, 4, 0, 0, false},
		{"next empty", CommandNext, 0, -1, 0, -1, -1, false},
		{"previous empty", CommandPrev, 0, -1, 0, -1, -1, false},
		{"rewind failure", CommandPrev, 4, 1, 2, 1, 1, true},
	}
	for _, mode := range []PlaybackMode{PlaybackModeOrder, PlaybackModeRepeatList, PlaybackModeRepeatTrack, PlaybackModeShuffle} {
		t.Run(string(mode), func(t *testing.T) {
			for _, playing := range []bool{false, true} {
				state := "paused"
				if playing {
					state = "playing"
				}
				t.Run(state, func(t *testing.T) {
					for _, tc := range cases {
						t.Run(tc.name, func(t *testing.T) { assertManualNavigation(t, mode, playing, tc) })
					}
				})
			}
		})
	}
}

func assertManualNavigation(t *testing.T, mode PlaybackMode, playing bool, tc manualNavigationCase) {
	t.Helper()
	fixture := newAnchorMoveFixture(t)
	device := fixture.device
	if tc.size == 1 {
		if _, err := device.Set(fixture.ctx, []string{"1"}); err != nil {
			t.Fatal(err)
		}
	} else if tc.size == 0 {
		if _, err := device.Clear(fixture.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := device.executeCommand(fixture.ctx, PlaybackCommand{Action: CommandSetMode, Mode: mode}); err != nil {
		t.Fatal(err)
	}
	if tc.size > 0 {
		device.PlaybackQueue.SetIndex(tc.current)
		fixture.start(t)
		if !playing {
			if _, err := device.Stop(fixture.ctx); err != nil {
				t.Fatal(err)
			}
		}
		fixture.playing.position = tc.position
	}
	device.randomFn = func(int) int { return 0 }
	before := captureRemovalFailureScene(fixture)
	failure := errors.New("rewind position rejected")
	if tc.fail {
		configureSkipFailure(fixture.playing, "position", failure)
	}
	factoryCalls := 0
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		factoryCalls++
		return fixture.newTrack(), nil
	}
	var notifications []DeviceStatus
	device.onStateChange = func(status DeviceStatus) { notifications = append(notifications, status) }
	t.Cleanup(func() { device.onStateChange = nil })
	returned, err := device.executeCommand(fixture.ctx, PlaybackCommand{Action: tc.action})
	after := captureRemovalFailureScene(fixture)
	if tc.fail {
		if !errors.Is(err, failure) || !reflect.DeepEqual(before, after) || len(notifications) != 0 || factoryCalls != 0 {
			t.Errorf("failed rewind changed state: before=%#v after=%#v notifications=%v factory=%d err=%v", before, after, notifications, factoryCalls, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	target := tc.ordered
	if mode == PlaybackModeShuffle {
		target = tc.shuffled
	}
	delta, wantFactory, wantNotifications := uint64(1), 0, 1
	if tc.size == 0 {
		delta, wantNotifications = 0, 0
	} else if target != tc.current {
		wantFactory = 1
		if playing {
			delta++
		}
	}
	wantStatus := before.status
	wantStatus.CurrentIndex, wantStatus.Position = target, 0
	wantStatus.StateSequence += delta
	if target != tc.current || tc.size == 1 {
		wantStatus.PlaybackInstanceID = returned.PlaybackInstanceID
	}
	if returned != wantStatus || after.status != wantStatus || after.revision != before.revision+delta || device.mode != mode {
		t.Errorf("manual navigation state: returned=%#v after=%#v wantStatus=%#v delta=%d mode=%q", returned, after, wantStatus, delta, device.mode)
	}
	if !slices.Equal(before.itemIDs, after.itemIDs) || factoryCalls != wantFactory || len(notifications) != wantNotifications {
		t.Errorf("navigation reordered queue, reloaded rewind, or mispublished: before=%v after=%v factory=%d notifications=%v", before.itemIDs, after.itemIDs, factoryCalls, notifications)
	}
	if tc.size > 0 && target == tc.current && (after.track != before.track || after.token != before.token) {
		t.Error("same-item reset replaced active output")
	}
}

type skipSequenceCase struct {
	name    string
	index   int
	offset  int
	loaded  bool
	playing bool
	stage   string
	delta   uint64
}

func TestPlaybackDeviceSkipStateSequence(t *testing.T) {
	for _, tc := range []skipSequenceCase{
		{"seek playing", 0, 17, true, true, "", 2},
		{"seek paused", 0, 17, true, false, "", 1},
		{"switch playing", 2, 17, true, true, "", 2},
		{"switch paused", 2, 17, true, false, "", 1},
		{"unloaded seek", 0, 17, false, false, "", 1},
		{"unloaded switch", 2, 17, false, false, "", 1},
		{"seek pause failure", 0, 17, true, true, "pause", 0},
		{"switch pause failure", 2, 17, true, true, "pause", 0},
		{"seek playing position failure", 0, 17, true, true, "position", 1},
		{"seek paused position failure", 0, 17, true, false, "position", 0},
		{"switch playing load failure", 2, 17, true, true, "load", 1},
		{"switch paused load failure", 2, 17, true, false, "load", 1},
		{"unloaded seek load failure", 0, 17, false, false, "load", 0},
		{"unloaded switch load failure", 2, 17, false, false, "load", 1},
		{"switch playing position failure", 2, 17, true, true, "position", 1},
		{"switch paused position failure", 2, 17, true, false, "position", 1},
		{"unloaded seek position failure", 0, 17, false, false, "position", 1},
		{"seek unpause failure", 0, 17, true, true, "unpause", 1},
		{"switch unpause failure", 2, 17, true, true, "unpause", 1},
		{"playing current zero offset no-op", 0, 0, true, true, "", 0},
		{"empty queue rejection", 0, 17, false, false, "empty", 0},
	} {
		t.Run(tc.name, func(t *testing.T) { assertSkipStateSequence(t, tc) })
	}
}

func configureSkipFailure(track *mockTrack, stage string, failure error) {
	switch stage {
	case "pause":
		track.onPause = func() error { return failure }
	case "position":
		track.onSetPosition = func(int) error { return failure }
	case "unpause":
		track.onUnpause = func() error { return failure }
	}
}

func assertSkipStateSequence(t *testing.T, tc skipSequenceCase) {
	t.Helper()
	fixture := newAnchorMoveFixture(t)
	device := fixture.device
	if tc.loaded {
		fixture.start(t)
		if !tc.playing {
			if _, err := device.Stop(fixture.ctx); err != nil {
				t.Fatal(err)
			}
		}
		fixture.playing.position = 5
	}
	if tc.stage == "empty" {
		if _, err := device.Clear(fixture.ctx); err != nil {
			t.Fatal(err)
		}
	}
	before := captureRemovalFailureScene(fixture)
	failure := errors.New("skip output rejected " + tc.stage)
	if tc.loaded {
		configureSkipFailure(fixture.playing, tc.stage, failure)
	}
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		during := captureRemovalFailureScene(fixture)
		wantSequence := before.status.StateSequence
		if tc.index != before.status.CurrentIndex {
			wantSequence++
		}
		if during.status.StateSequence != wantSequence || during.status.QueueVersion != before.status.QueueVersion {
			t.Errorf("unlocked skip preparation ordering: before=%#v during=%#v wantSequence=%d", before.status, during.status, wantSequence)
		}
		if tc.stage == "load" {
			return nil, failure
		}
		track := fixture.newTrack()
		configureSkipFailure(track, tc.stage, failure)
		return track, nil
	}
	var notifications []DeviceStatus
	device.onStateChange = func(status DeviceStatus) {
		if !device.mu.TryLock() {
			t.Error("skip callback ran under device lock")
			return
		}
		observed := device.getStatus()
		device.mu.Unlock()
		if observed != status {
			t.Errorf("skip callback is stale: callback=%#v observed=%#v", status, observed)
		}
		notifications = append(notifications, status)
	}
	t.Cleanup(func() { device.onStateChange = nil })
	command := PlaybackCommand{Action: CommandSeek, Position: tc.offset}
	if tc.index != before.status.CurrentIndex || tc.stage == "empty" {
		command = PlaybackCommand{Action: CommandSkip, Index: tc.index, Offset: tc.offset}
	}
	returned, err := device.executeCommand(fixture.ctx, command)
	if tc.stage == "empty" {
		if err == nil || err.Error() != "could not get current track" {
			t.Errorf("empty skip error changed: %v", err)
		}
	} else if (tc.stage == "" && err != nil) || (tc.stage != "" && !errors.Is(err, failure)) {
		t.Errorf("skip output error mismatch: %v", err)
	}
	after := captureRemovalFailureScene(fixture)
	assertSkipSequenceResult(t, tc, before, after, returned, notifications)
}

func assertSkipSequenceResult(t *testing.T, tc skipSequenceCase, before, after removalFailureScene, returned DeviceStatus, notifications []DeviceStatus) {
	t.Helper()
	if returned != after.status || after.status.StateSequence != before.status.StateSequence+tc.delta || after.revision != before.revision+tc.delta {
		t.Errorf("skip semantic accounting: before=%#v after=%#v returned=%#v wantDelta=%d", before, after, returned, tc.delta)
	}
	if after.status.QueueVersion != before.status.QueueVersion || !slices.Equal(after.itemIDs, before.itemIDs) {
		t.Error("skip changed structural queue state")
	}
	if tc.delta == 0 {
		expected := before
		if !tc.loaded && tc.stage == "load" && tc.index == before.status.CurrentIndex {
			expected.preparation++
		}
		if !reflect.DeepEqual(expected, after) || len(notifications) != 0 {
			t.Errorf("uncommitted skip mutated or published state: expected=%#v after=%#v notifications=%#v", expected, after, notifications)
		}
		return
	}
	if len(notifications) != 1 || notifications[0] != after.status {
		t.Errorf("committed skip not published once: state=%#v notifications=%#v", after.status, notifications)
	}
	if tc.stage == "" && (after.status.CurrentIndex != tc.index || after.status.Position != tc.offset || after.status.Playing != tc.playing) {
		t.Errorf("successful skip lost playback intent: %#v", after.status)
	}
	if tc.stage != "" && (after.status.CurrentIndex != tc.index || after.status.Playing) {
		t.Errorf("committed skip failure lost selected paused context: %#v", after.status)
	}
}

func TestPlaybackDeviceRemovalOrdersCommittedAndStartedStates(t *testing.T) {
	fixture := newAnchorMoveFixture(t)
	fixture.start(t)
	before := captureRemovalFailureScene(fixture)
	var during removalFailureScene
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		during = captureRemovalFailureScene(fixture)
		return fixture.newTrack(), nil
	}
	status, err := fixture.device.Remove(fixture.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if during.status.QueueVersion != before.status.QueueVersion+1 || during.status.StateSequence != before.status.StateSequence+1 || during.status.Playing {
		t.Errorf("unlocked successor preparation lacked committed ordering: before=%#v during=%#v", before.status, during.status)
	}
	if status.QueueVersion != before.status.QueueVersion+1 || status.StateSequence != before.status.StateSequence+2 || !status.Playing {
		t.Errorf("started successor reused its pre-start sequence: before=%#v during=%#v after=%#v", before.status, during.status, status)
	}
}

func TestPlaybackDeviceRemovalFailureStateSequence(t *testing.T) {
	for _, stage := range []string{"pause", "load", "unpause"} {
		t.Run(stage, func(t *testing.T) {
			for _, command := range []struct {
				name string
				run  func(*anchorMoveFixture) (DeviceStatus, error)
			}{
				{"index", func(f *anchorMoveFixture) (DeviceStatus, error) { return f.device.Remove(f.ctx, 0) }},
				{"identity", func(f *anchorMoveFixture) (DeviceStatus, error) {
					return f.device.executeCommand(f.ctx, PlaybackCommand{Action: CommandRemove, QueueItemID: f.itemIDs[0]})
				}},
			} {
				t.Run(command.name, func(t *testing.T) {
					assertRemovalFailureStateSequence(t, stage, command.run)
				})
			}
		})
	}
}

func assertRemovalFailureStateSequence(t *testing.T, stage string, run func(*anchorMoveFixture) (DeviceStatus, error)) {
	t.Helper()
	fixture := newAnchorMoveFixture(t)
	fixture.start(t)
	device, track := fixture.device, fixture.playing
	before := captureRemovalFailureScene(fixture)
	failure := errors.New("output rejected " + stage)
	if stage == "pause" {
		track.onPause = func() error { return failure }
	}
	factoryCalls := 0
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		factoryCalls++
		during := captureRemovalFailureScene(fixture)
		if during.status.QueueVersion != before.status.QueueVersion+1 || during.status.StateSequence != before.status.StateSequence+1 {
			t.Errorf("successor preparation exposed stale structural ordering: before=%#v during=%#v", before.status, during.status)
		}
		if stage == "load" {
			return nil, failure
		}
		return &mockTrack{onUnpause: func() error { return failure }}, nil
	}
	var notifications []DeviceStatus
	device.onStateChange = func(status DeviceStatus) {
		observed, err := device.Status(fixture.ctx)
		if err != nil || observed != status {
			t.Errorf("failure notification is not a fresh unlocked observation: status=%#v observed=%#v error=%v", status, observed, err)
		}
		notifications = append(notifications, status)
	}
	t.Cleanup(func() { device.onStateChange = nil })
	returned, err := run(fixture)
	if !errors.Is(err, failure) {
		t.Errorf("original output failure was not preserved: %v", err)
	}
	after := captureRemovalFailureScene(fixture)
	if returned != after.status {
		t.Errorf("failure returned stale state: returned=%#v observed=%#v", returned, after.status)
	}
	if stage == "pause" {
		if !reflect.DeepEqual(before, after) || track.closeCalls != 0 || factoryCalls != 0 || len(notifications) != 0 {
			t.Errorf("failed source pause mutated removal scene: before=%#v after=%#v closes=%d factory=%d notifications=%#v", before, after, track.closeCalls, factoryCalls, notifications)
		}
		return
	}
	if track.closeCalls != 1 || factoryCalls != 1 {
		t.Errorf("committed removal retired or prepared output incorrectly: closes=%d factory=%d", track.closeCalls, factoryCalls)
	}
	assertCommittedRemovalFailure(t, before, after, notifications)
}

func assertCommittedRemovalFailure(t *testing.T, before, after removalFailureScene, notifications []DeviceStatus) {
	t.Helper()
	if after.status.QueueVersion != before.status.QueueVersion+1 || after.status.StateSequence != before.status.StateSequence+1 || after.revision != before.revision+1 {
		t.Errorf("committed removal did not advance ordering once: before=%#v after=%#v", before, after)
	}
	if after.status.Playing || after.status.CurrentIndex != 0 || !slices.Equal(after.itemIDs, before.itemIDs[1:]) {
		t.Errorf("failed successor did not retain committed queue context: before=%#v after=%#v", before, after)
	}
	if len(notifications) != 1 || notifications[0] != after.status {
		t.Errorf("committed failure was not published exactly once: state=%#v notifications=%#v", after.status, notifications)
	}
}

func TestPlaybackDeviceStructuralFailureStateSequence(t *testing.T) {
	t.Run("media lookup fails before add mutation", func(t *testing.T) {
		fixture := newAnchorMoveFixture(t)
		before, err := fixture.device.Status(fixture.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.device.Add(fixture.ctx, []string{"missing"}); err == nil {
			t.Fatal("expected media lookup failure")
		}
		after, err := fixture.device.Status(fixture.ctx)
		if err != nil || after != before {
			t.Fatalf("failed lookup changed status: before=%#v after=%#v error=%v", before, after, err)
		}
	})
	t.Run("successor load fails after committed remove", func(t *testing.T) {
		fixture := newAnchorMoveFixture(t)
		fixture.start(t)
		before, err := fixture.device.Status(fixture.ctx)
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("successor load rejected")
		newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
			return nil, failure
		}
		after, err := fixture.device.Remove(fixture.ctx, before.CurrentIndex)
		if !errors.Is(err, failure) || after.Playing || after.QueueVersion != before.QueueVersion+1 || after.StateSequence != before.StateSequence+1 {
			t.Fatalf("committed removal lost ordering after output failure: before=%#v after=%#v error=%v", before, after, err)
		}
	})
}

func TestPlaybackDeviceAnchorMoveAssignsDistinctInstanceIDs(t *testing.T) {
	fixture := newAnchorMoveFixture(t)
	itemIDs := fixture.itemIDs

	// T13-AC01/AC02: duplicate MediaFile occurrences get distinct, non-empty
	// instance IDs that survive reordering and never collapse into one another.
	if itemIDs[0] == itemIDs[1] || itemIDs[0] == "" || itemIDs[1] == "" {
		t.Fatalf("duplicate occurrences share an instance ID: %v", itemIDs)
	}
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: itemIDs[3], AnchorQueueItemID: itemIDs[0], Placement: "before",
	}); err != nil {
		t.Fatalf("anchor move: %v", err)
	}
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: itemIDs[1], AnchorQueueItemID: itemIDs[3], Placement: "after",
	}); err != nil {
		t.Fatalf("second anchor move: %v", err)
	}
	got := fixture.device.PlaybackQueue.ItemIDs()
	if len(got) != 4 || got[0] == got[1] {
		t.Fatalf("duplicate occurrences collapsed after moves: %v", got)
	}
	for _, id := range itemIDs {
		if fixture.device.PlaybackQueue.IndexOfQueueItemID(id) == -1 {
			t.Fatalf("instance ID lost during moves: %v missing from %v", id, got)
		}
	}
}

func TestPlaybackDeviceAnchorMoveKeepsCurrentPlaybackContinuous(t *testing.T) {
	fixture := newAnchorMoveFixture(t)
	fixture.start(t)
	fixture.playing.position = 42
	currentID := fixture.itemIDs[0]

	// T13-AC06: moving the current queue item changes only its location. The
	// current instance, active playback instance, media resource, and playback
	// position stay continuous: no reload, no audible restart.
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: currentID, AnchorQueueItemID: fixture.itemIDs[3], Placement: "after",
	}); err != nil {
		t.Fatalf("anchor move of current item: %v", err)
	}
	fixture.device.mu.Lock()
	defer fixture.device.mu.Unlock()
	if fixture.device.ActiveTrack != fixture.playing {
		t.Fatal("anchor move of current item replaced the active track")
	}
	if got := fixture.device.PlaybackQueue.CurrentItem().QueueItemID; got != currentID {
		t.Fatalf("current instance changed: got=%s want=%s", got, currentID)
	}
	if got := fixture.device.PlaybackQueue.ItemIDs(); got[3] != currentID {
		t.Fatalf("moved current item not at anchor: %v", got)
	}
	if fixture.playing.position != 42 {
		t.Fatalf("playback position disturbed by move: %d", fixture.playing.position)
	}
}

func TestPlaybackDeviceAnchorMoveNoOpsAndFailures(t *testing.T) {
	fixture := newAnchorMoveFixture(t)
	currentID := fixture.itemIDs[0]

	// T13-AC03: self-reference and already-adjacent moves are APPLIED no-ops
	// with no queue-version or scene bump.
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: currentID, AnchorQueueItemID: fixture.itemIDs[3], Placement: "after",
	}); err != nil {
		t.Fatalf("initial placement move: %v", err)
	}
	versionBefore := fixture.device.queueVersion
	sequenceBefore := fixture.device.stateSequence
	sceneBefore := fixture.device.SceneFingerprint()
	noOps := []PlaybackCommand{
		{QueueItemID: currentID, AnchorQueueItemID: currentID, Placement: "before"},
		{QueueItemID: currentID, AnchorQueueItemID: fixture.itemIDs[3], Placement: "after"},
	}
	for _, command := range noOps {
		status, err := fixture.anchorMove(command)
		if err != nil {
			t.Fatalf("no-op move rejected: %v", err)
		}
		if status.QueueVersion != versionBefore || status.StateSequence != sequenceBefore {
			t.Fatalf("no-op move advanced ordering: status=%#v want version=%d sequence=%d", status, versionBefore, sequenceBefore)
		}
	}
	if fixture.SceneFingerprintChanged(sceneBefore) {
		t.Fatalf("no-op move bumped scene fingerprint: %s -> %s", sceneBefore, fixture.device.SceneFingerprint())
	}
	assertAnchorMoveFailureSnapshots(t, fixture, versionBefore, sceneBefore)
	if fixture.device.stateSequence != sequenceBefore {
		t.Fatal("rejected move advanced semantic sequence")
	}
	assertAnchorMoveVersionPresence(t, fixture, versionBefore)
}

func (f *anchorMoveFixture) SceneFingerprintChanged(before string) bool {
	return f.device.SceneFingerprint() != before
}

// T13-AC04: missing source and missing target are distinct errors carrying a
// fresh snapshot, without mutating the queue version or scene.
func assertAnchorMoveFailureSnapshots(t *testing.T, fixture *anchorMoveFixture, versionBefore int, sceneBefore string) {
	t.Helper()
	currentID := fixture.itemIDs[0]
	var sourceErr *QueueItemLookupError
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: "missing-source", AnchorQueueItemID: currentID, Placement: "before",
	}); err == nil {
		t.Fatal("missing source accepted")
	} else if !errors.As(err, &sourceErr) {
		t.Fatalf("missing source error type: %T %v", err, err)
	} else if sourceErr.Source != true || sourceErr.QueueItemID != "missing-source" {
		t.Fatalf("missing source error identity: %#v", sourceErr)
	} else if sourceErr.Status.QueueVersion != versionBefore {
		t.Fatalf("missing source snapshot is stale: %#v", sourceErr.Status)
	}
	var anchorErr *QueueItemLookupError
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: currentID, AnchorQueueItemID: "missing-target", Placement: "before",
	}); err == nil {
		t.Fatal("missing target accepted")
	} else if !errors.As(err, &anchorErr) {
		t.Fatalf("missing target error type: %T %v", err, err)
	} else if anchorErr.Source != false || anchorErr.QueueItemID != "missing-target" {
		t.Fatalf("missing target error identity: %#v", anchorErr)
	}
	if fixture.SceneFingerprintChanged(sceneBefore) || fixture.device.queueVersion != versionBefore {
		t.Fatal("failed move mutated queue version or scene")
	}
}

// T13-AC04: a stale expected version conflicts while an omitted expectation
// passes; expected version 0 is a real expectation, not an omitted one.
func assertAnchorMoveVersionPresence(t *testing.T, fixture *anchorMoveFixture, versionBefore int) {
	t.Helper()
	currentID := fixture.itemIDs[0]
	stale := versionBefore + 99
	var conflictErr *QueueVersionConflictError
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: currentID, AnchorQueueItemID: fixture.itemIDs[1], Placement: "before",
		ExpectedQueueVersion: &stale,
	}); err == nil {
		t.Fatal("stale expected version accepted")
	} else if !errors.As(err, &conflictErr) {
		t.Fatalf("version conflict error type: %T %v", err, err)
	} else if conflictErr.Expected != stale || conflictErr.Actual != versionBefore {
		t.Fatalf("version conflict details: %#v", conflictErr)
	} else if conflictErr.Status.QueueVersion != versionBefore {
		t.Fatalf("version conflict snapshot is stale: %#v", conflictErr.Status)
	}
	if fixture.device.queueVersion != versionBefore {
		t.Fatal("version conflict still mutated queue version")
	}
	zero := 0
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: currentID, AnchorQueueItemID: fixture.itemIDs[1], Placement: "before",
		ExpectedQueueVersion: &zero,
	}); err == nil {
		t.Fatal("expected version 0 treated as omitted")
	} else if !errors.As(err, &conflictErr) {
		t.Fatalf("expected version 0 error type: %T %v", err, err)
	}
	if _, err := fixture.anchorMove(PlaybackCommand{
		QueueItemID: currentID, AnchorQueueItemID: fixture.itemIDs[1], Placement: "before",
	}); err != nil {
		t.Fatalf("omitted expected version rejected: %v", err)
	}
}

func TestPlaybackDeviceClearResetsQueueCurrentAndOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	t.Cleanup(func() { _, _ = device.Shutdown(ctx) })
	device.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/1.mp3"}, {ID: "2", Path: "/2.mp3"}})
	device.PlaybackQueue.SetIndex(1)

	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	// Paused clear never starts playback and resets queue/current/position.
	if _, err := device.Clear(ctx); err != nil {
		t.Fatalf("paused clear: %v", err)
	}
	status, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Playing || status.CurrentIndex != -1 || status.Position != 0 || status.QueueVersion != 1 {
		t.Fatalf("paused clear did not reset output semantics: %#v", status)
	}
	device.mu.Lock()
	if device.ActiveTrack != nil || device.PlaybackQueue.Size() != 0 {
		device.mu.Unlock()
		t.Fatalf("paused clear left output or queue state: %s", device.PlaybackQueue)
	}
	device.mu.Unlock()

	// Playing clear stops output and clears the current item.
	device.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/1.mp3"}})
	device.PlaybackQueue.SetIndex(0)
	if _, err := device.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	versionBefore := device.queueVersion
	if _, err := device.Clear(ctx); err != nil {
		t.Fatalf("playing clear: %v", err)
	}
	status, err = device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Playing || status.CurrentIndex != -1 || status.Position != 0 || status.QueueVersion != versionBefore+1 {
		t.Fatalf("playing clear did not stop and reset: %#v", status)
	}
	device.mu.Lock()
	if device.ActiveTrack != nil || device.recoveryPosition != nil {
		device.mu.Unlock()
		t.Fatal("playing clear left an output or recovery position")
	}
	device.mu.Unlock()
}

// semanticTransitionFixture builds a device with a captured publication log
// for the T14-AC10 semantic transition matrix. The state change callback must
// run outside the device lock and must observe exactly the status it published.
type semanticTransitionFixture struct {
	ctx       context.Context
	device    *playbackDevice
	track     *mockTrack
	published []DeviceStatus
}

func newSemanticTransitionFixture(t *testing.T, withTrack, playing bool) *semanticTransitionFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	f := &semanticTransitionFixture{ctx: ctx, device: device}
	device.onStateChange = func(status DeviceStatus) {
		if !device.mu.TryLock() {
			t.Error("semantic transition callback ran under the device lock")
			return
		}
		observed := device.getStatus()
		device.mu.Unlock()
		if observed != status {
			t.Errorf("semantic transition published a stale status: published=%#v observed=%#v", status, observed)
		}
		f.published = append(f.published, status)
	}
	t.Cleanup(func() { device.onStateChange = nil })
	device.mu.Lock()
	device.Gain = 0.64
	device.DesiredGain = 0.64
	device.playbackInstanceID = "instance-semantic"
	if withTrack {
		f.track = &mockTrack{playing: playing}
		device.ActiveTrack = f.track
		device.activePlaybackToken = 41
	}
	device.mu.Unlock()
	return f
}

func (f *semanticTransitionFixture) assertOrderingUnchanged(t *testing.T, before DeviceStatus) {
	t.Helper()
	if f.device.queueVersion != before.QueueVersion {
		t.Errorf("semantic transition changed structural queue version: %d -> %d", before.QueueVersion, f.device.queueVersion)
	}
	if f.device.playbackInstanceID != "instance-semantic" {
		t.Errorf("semantic transition reissued the playback instance: %q", f.device.playbackInstanceID)
	}
}

// T14-AC10: asynchronous output Failure/Retired events are semantic state
// changes. Each effective event retires the current output exactly once,
// advances the independent state sequence exactly once, and publishes one
// fresh unlocked status; stale, zero, or already-retired tokens stay no-ops.
func TestPlaybackDeviceAsyncOutputEventStateSequence(t *testing.T) {
	position := 17
	cases := []struct {
		name     string
		kind     model.PlaybackEventKind
		position *int
	}{
		{"failure with recovery position", model.PlaybackEventFailure, &position},
		{"retired with recovery position", model.PlaybackEventRetired, &position},
		{"retired without recovery position", model.PlaybackEventRetired, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSemanticTransitionFixture(t, true, true)
			device := f.device
			before, err := device.Status(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			sceneBefore := device.SceneFingerprint()

			device.handlePlaybackEvent(model.PlaybackEvent{Token: 41, Kind: tc.kind, Position: tc.position, Err: errors.New("output fault")})

			want := before
			want.Playing = false
			want.StateSequence++
			if tc.position != nil {
				want.Position = *tc.position
			} else {
				want.Position = 0
			}
			observed, err := device.Status(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if observed != want {
				t.Errorf("async %v did not advance semantic state exactly once: before=%#v observed=%#v want=%#v", tc.kind, before, observed, want)
			}
			if len(f.published) != 1 || f.published[0] != want {
				t.Errorf("async %v did not publish the changed state once: published=%#v want=%#v", tc.kind, f.published, want)
			}
			if f.track.closeCalls != 1 {
				t.Errorf("async %v retired the output %d times, want 1", tc.kind, f.track.closeCalls)
			}
			if device.SceneFingerprint() == sceneBefore {
				t.Errorf("async %v did not advance the scene revision", tc.kind)
			}
			f.assertOrderingUnchanged(t, before)

			// Stale, zero and already-retired tokens must not advance or republish.
			stale := 99
			device.handlePlaybackEvent(model.PlaybackEvent{Token: 42, Kind: model.PlaybackEventFailure, Position: &stale})
			device.handlePlaybackEvent(model.PlaybackEvent{Token: 0, Kind: model.PlaybackEventRetired})
			device.handlePlaybackEvent(model.PlaybackEvent{Token: 41, Kind: tc.kind, Position: &stale})
			final, err := device.Status(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if final != want || len(f.published) != 1 || f.track.closeCalls != 1 {
				t.Errorf("stale async events were not no-ops: final=%#v published=%#v closes=%d", final, f.published, f.track.closeCalls)
			}
		})
	}
}

// T14-AC10: Shutdown is an effective retire of the current output. It advances
// scene and state sequence exactly once and publishes once, including when the
// final Pause returns an error after the internal retire mutation; an idle
// device stays a strict no-op.
func TestPlaybackDeviceShutdownStateSequence(t *testing.T) {
	failure := errors.New("pause ipc rejected")
	cases := []struct {
		name    string
		track   bool
		playing bool
		fail    bool
	}{
		{"playing retire", true, true, false},
		{"paused retire", true, false, false},
		{"pause error after retire mutation", true, true, true},
		{"idle no-op", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSemanticTransitionFixture(t, tc.track, tc.playing)
			device := f.device
			if tc.fail {
				f.track.onPause = func() error { return failure }
			}
			before, err := device.Status(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			sceneBefore := device.SceneFingerprint()

			status, err := device.Shutdown(f.ctx)

			if tc.fail {
				if !errors.Is(err, failure) {
					t.Errorf("failed shutdown lost the pause error: %v", err)
				}
			} else if err != nil {
				t.Errorf("shutdown returned an error: %v", err)
			}
			want := before
			want.Playing = false
			want.Position = 0
			if tc.track {
				// The output retire mutation committed even when Pause failed.
				want.StateSequence++
			}
			if status != want {
				t.Errorf("shutdown state mismatch: status=%#v want=%#v", status, want)
			}
			observed, err := device.Status(f.ctx)
			if err != nil || observed != want {
				t.Errorf("shutdown returned a stale status: observed=%#v want=%#v error=%v", observed, want, err)
			}
			if tc.track {
				if len(f.published) != 1 || f.published[0] != want {
					t.Errorf("shutdown did not publish the changed state once: published=%#v want=%#v", f.published, want)
				}
				if f.track.closeCalls != 1 {
					t.Errorf("shutdown retired the output %d times, want 1", f.track.closeCalls)
				}
				if device.SceneFingerprint() == sceneBefore {
					t.Error("shutdown did not advance the scene revision")
				}
			} else {
				if len(f.published) != 0 {
					t.Errorf("idle shutdown published state: %#v", f.published)
				}
				if device.SceneFingerprint() != sceneBefore {
					t.Error("idle shutdown advanced the scene revision")
				}
			}
			f.assertOrderingUnchanged(t, before)
		})
	}
}

// T14-AC10: gain writes are semantic transitions. An effective change advances
// the state sequence once and publishes once; without an active output only
// DesiredGain changes and the confirmed Gain stays unchanged; a failed
// SetVolume keeps the new desired value visible, reports the error, and still
// orders the mutation; a true no-op changes nothing.
func TestPlaybackDeviceGainStateSequence(t *testing.T) {
	failure := errors.New("volume ipc rejected")
	cases := []struct {
		name        string
		track       bool
		fail        bool
		gain        float32
		wantGain    float32
		wantDesired float32
		wantDelta   uint64
		wantPublish int
	}{
		{"idle desired gain only", false, false, 0.8, 0.64, 0.8, 1, 1},
		{"effective gain with output", true, false, 0.8, 0.8, 0.8, 1, 1},
		{"failed set volume keeps desired visible", true, true, 0.8, 0.64, 0.8, 1, 1},
		{"same gain no-op", true, false, 0.64, 0.64, 0.64, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSemanticTransitionFixture(t, tc.track, true)
			device := f.device
			if tc.fail {
				f.track.onSetVolume = func(float32) error { return failure }
			}
			before, err := device.Status(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			sceneBefore := device.SceneFingerprint()

			status, err := device.SetGain(f.ctx, tc.gain)

			if tc.fail {
				if !errors.Is(err, failure) {
					t.Errorf("failed gain lost the volume error: %v", err)
				}
			} else if err != nil {
				t.Errorf("gain returned an error: %v", err)
			}
			want := before
			want.Gain = tc.wantGain
			want.DesiredGain = tc.wantDesired
			want.StateSequence += tc.wantDelta
			if status != want {
				t.Errorf("gain state mismatch: status=%#v want=%#v", status, want)
			}
			observed, err := device.Status(f.ctx)
			if err != nil || observed != want {
				t.Errorf("gain returned a stale status: observed=%#v want=%#v error=%v", observed, want, err)
			}
			if len(f.published) != tc.wantPublish || (tc.wantPublish == 1 && f.published[0] != want) {
				t.Errorf("gain published %d statuses, want %d: %#v", len(f.published), tc.wantPublish, f.published)
			}
			if tc.wantDelta > 0 {
				if device.SceneFingerprint() == sceneBefore {
					t.Error("effective gain did not advance the scene revision")
				}
			} else if device.SceneFingerprint() != sceneBefore {
				t.Error("gain no-op advanced the scene revision")
			}
			f.assertOrderingUnchanged(t, before)
		})
	}
}

// T14-AC10: mute writes are semantic transitions. Effective transitions advance
// the state sequence once and publish once; an unmute failure after the applied
// gain was already restored must still publish the changed state once with the
// original error; a true no-op changes nothing.
func TestPlaybackDeviceMuteStateSequence(t *testing.T) {
	failure := errors.New("mute ipc rejected")
	cases := []struct {
		name        string
		track       bool
		initialMute bool
		muted       bool
		desired     float32
		failUnmute  bool
		wantMuted   bool
		wantGain    float32
		wantErr     bool
		wantDelta   uint64
		wantPublish int
	}{
		{"effective mute", true, false, true, 0.64, false, true, 0.64, false, 1, 1},
		{"effective unmute", true, true, false, 0.64, false, false, 0.64, false, 1, 1},
		{"unmute failure after applied gain", true, true, false, 0.8, true, true, 0.8, true, 1, 1},
		{"same mute no-op", false, false, false, 0.64, false, false, 0.64, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSemanticTransitionFixture(t, tc.track, true)
			device := f.device
			device.mu.Lock()
			device.Muted = tc.initialMute
			device.DesiredGain = tc.desired
			device.mu.Unlock()
			if tc.failUnmute {
				f.track.onSetMute = func(value bool) error {
					if !value {
						return failure
					}
					return nil
				}
			}
			before, err := device.Status(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			sceneBefore := device.SceneFingerprint()

			status, err := device.executeCommand(f.ctx, PlaybackCommand{Action: CommandMute, Muted: tc.muted})

			if tc.wantErr {
				if !errors.Is(err, failure) {
					t.Errorf("failed mute lost the output error: %v", err)
				}
			} else if err != nil {
				t.Errorf("mute returned an error: %v", err)
			}
			want := before
			want.Muted = tc.wantMuted
			want.Gain = tc.wantGain
			want.DesiredGain = tc.desired
			want.StateSequence += tc.wantDelta
			if status != want {
				t.Errorf("mute state mismatch: status=%#v want=%#v", status, want)
			}
			observed, err := device.Status(f.ctx)
			if err != nil || observed != want {
				t.Errorf("mute returned a stale status: observed=%#v want=%#v error=%v", observed, want, err)
			}
			if len(f.published) != tc.wantPublish || (tc.wantPublish == 1 && f.published[0] != want) {
				t.Errorf("mute published %d statuses, want %d: %#v", len(f.published), tc.wantPublish, f.published)
			}
			if tc.wantDelta > 0 {
				if device.SceneFingerprint() == sceneBefore {
					t.Error("effective mute did not advance the scene revision")
				}
			} else if device.SceneFingerprint() != sceneBefore {
				t.Error("mute no-op advanced the scene revision")
			}
			f.assertOrderingUnchanged(t, before)
		})
	}
}

// T14-AC10: a repeated gain request whose desired value was already committed
// by an earlier failed attempt must not advance ordering or republish when the
// retry fails again — attempted intent alone is not a committed state change.
// Once the output confirms the volume, the applied confirmation is ordered and
// published exactly once with the original error preserved on every failure.
func TestPlaybackDeviceFailedGainRetryAccounting(t *testing.T) {
	f := newSemanticTransitionFixture(t, true, true)
	device := f.device
	failure := errors.New("volume ipc rejected")
	failVolume := true
	f.track.onSetVolume = func(float32) error {
		if failVolume {
			return failure
		}
		return nil
	}
	before, err := device.Status(f.ctx)
	if err != nil {
		t.Fatal(err)
	}

	// First attempt commits the desired change; the output failure leaves the
	// applied gain unconfirmed, but the committed mutation is ordered and
	// published once with the original error.
	status, err := device.SetGain(f.ctx, 0.8)
	if !errors.Is(err, failure) {
		t.Fatalf("first gain lost the volume error: %v", err)
	}
	want := before
	want.DesiredGain = 0.8
	want.StateSequence++
	if status != want {
		t.Fatalf("first failed gain state mismatch: status=%#v want=%#v", status, want)
	}
	sceneAfterFirst := device.SceneFingerprint()
	if len(f.published) != 1 || f.published[0] != want {
		t.Fatalf("first failed gain did not publish the changed state once: %#v", f.published)
	}

	// Retry with the same value: the desired change was already committed and
	// the applied gain is still unconfirmed, so a repeated failure commits
	// nothing and must not advance scene/sequence or republish.
	status, err = device.SetGain(f.ctx, 0.8)
	if !errors.Is(err, failure) {
		t.Fatalf("retry lost the original volume error: %v", err)
	}
	if status != want {
		t.Fatalf("unconfirmed retry advanced ordering: status=%#v want=%#v", status, want)
	}
	if device.SceneFingerprint() != sceneAfterFirst || len(f.published) != 1 {
		t.Fatalf("unconfirmed retry advanced scene or republished: published=%#v", f.published)
	}
	if f.device.Gain != 0.64 || f.device.DesiredGain != 0.8 {
		t.Fatalf("unconfirmed retry changed gain state: gain=%v desired=%v", f.device.Gain, f.device.DesiredGain)
	}

	// Once the output confirms the volume, the applied confirmation is ordered
	// and published exactly once.
	failVolume = false
	confirmed, err := device.SetGain(f.ctx, 0.8)
	if err != nil {
		t.Fatalf("confirmed gain returned an error: %v", err)
	}
	confirmedWant := want
	confirmedWant.Gain = 0.8
	confirmedWant.StateSequence++
	if confirmed != confirmedWant {
		t.Fatalf("confirmed gain state mismatch: status=%#v want=%#v", confirmed, confirmedWant)
	}
	if len(f.published) != 2 || f.published[1] != confirmedWant {
		t.Fatalf("confirmed gain did not publish once: %#v", f.published)
	}
	if len(f.track.volumeValues) != 3 {
		t.Fatalf("gain attempts did not reach the output each time: %v", f.track.volumeValues)
	}
}

// T14-AC06 compatibility characterization for the retained legacy physical
// shuffle command (CommandShuffle). The ticket default retains the historical
// physical reorder of the visible queue; these tests pin that behavior so it
// cannot masquerade as the canonical shufflePlay mode selection: legacy
// shuffle never changes the Playback Mode, never reissues the playback
// instance, and is a bounded structural reorder. Canonical shufflePlay
// selection itself is covered by TestPlaybackDeviceNaturalEndPlaybackModes.

// T14-AC06: on empty and single-item queues the legacy shuffle is an exact
// no-op — no structural/semantic/scene counter advances and no publication.
func TestPlaybackDeviceLegacyShuffleNoOpSizes(t *testing.T) {
	t.Run("empty queue", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
		var published []DeviceStatus
		device.onStateChange = func(status DeviceStatus) {
			if !device.mu.TryLock() {
				t.Error("empty shuffle callback ran under the device lock")
				return
			}
			device.mu.Unlock()
			published = append(published, status)
		}
		before, err := device.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		status, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandShuffle})
		if err != nil {
			t.Fatal(err)
		}
		observed, err := device.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status != before || observed != before {
			t.Fatalf("empty shuffle was not a no-op: before=%#v returned=%#v observed=%#v", before, status, observed)
		}
		if len(published) != 0 {
			t.Fatalf("empty shuffle published state: %#v", published)
		}
		if device.queueVersion != 0 || device.stateSequence != 0 || device.sceneRevision != 1 {
			t.Fatalf("empty shuffle advanced counters: queueVersion=%d stateSequence=%d sceneRevision=%d",
				device.queueVersion, device.stateSequence, device.sceneRevision)
		}
	})

	t.Run("single item with active output", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
		device.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/1.mp3"}})
		device.PlaybackQueue.SetIndex(0)
		track := &mockTrack{playing: true, position: 12}
		device.mu.Lock()
		device.ActiveTrack = track
		device.activePlaybackToken = 41
		device.playbackInstanceID = "instance-single"
		device.mu.Unlock()
		itemIDs := device.PlaybackQueue.ItemIDs()
		var published []DeviceStatus
		device.onStateChange = func(status DeviceStatus) {
			if !device.mu.TryLock() {
				t.Error("single-item shuffle callback ran under the device lock")
				return
			}
			device.mu.Unlock()
			published = append(published, status)
		}
		before, err := device.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sceneBefore := device.SceneFingerprint()

		status, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandShuffle})
		if err != nil {
			t.Fatal(err)
		}
		observed, err := device.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status != before || observed != before {
			t.Fatalf("single-item shuffle was not a no-op: before=%#v returned=%#v observed=%#v", before, status, observed)
		}
		if len(published) != 0 {
			t.Fatalf("single-item shuffle published state: %#v", published)
		}
		if device.queueVersion != before.QueueVersion || device.stateSequence != before.StateSequence {
			t.Fatalf("single-item shuffle advanced ordering: before=%#v after=%#v", before, observed)
		}
		if device.SceneFingerprint() != sceneBefore {
			t.Fatal("single-item shuffle advanced the scene revision")
		}
		if !slices.Equal(itemIDs, device.PlaybackQueue.ItemIDs()) {
			t.Fatal("single-item shuffle changed the queue")
		}
		if device.ActiveTrack != track || !track.playing || track.position != 12 {
			t.Fatalf("single-item shuffle touched the active output: track=%#v playing=%v position=%d",
				device.ActiveTrack, track.playing, track.position)
		}
		if device.playbackInstanceID != "instance-single" {
			t.Fatalf("single-item shuffle reissued the playback instance: %q", device.playbackInstanceID)
		}
		if device.mode != PlaybackModeOrder {
			t.Fatalf("single-item shuffle changed the Playback Mode: %q", device.mode)
		}
	})
}

// T14-AC06: on a multi-item queue the retained legacy shuffle performs one
// bounded physical reorder of the visible queue: structural version, semantic
// sequence, and scene revision each advance exactly once, one fresh status is
// published, the exact queue item set is preserved, and the current item,
// active output, playing state, position, playback instance, and Playback
// Mode are untouched. The reorder is random, so this test asserts invariants
// across the actual queue item IDs — never a specific resulting order.
func TestPlaybackDeviceLegacyShufflePreservesPlaybackContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	device.PlaybackQueue.Add(model.MediaFiles{
		{ID: "1", Path: "/1.mp3"},
		{ID: "2", Path: "/2.mp3"},
		{ID: "3", Path: "/3.mp3"},
		{ID: "4", Path: "/4.mp3"},
		{ID: "5", Path: "/5.mp3"},
	})
	device.PlaybackQueue.SetIndex(2)
	track := &mockTrack{playing: true, position: 9}
	device.mu.Lock()
	device.ActiveTrack = track
	device.activePlaybackToken = 41
	device.playbackInstanceID = "instance-legacy"
	device.mu.Unlock()

	var published []DeviceStatus
	device.onStateChange = func(status DeviceStatus) {
		if !device.mu.TryLock() {
			t.Error("legacy shuffle callback ran under the device lock")
			return
		}
		device.mu.Unlock()
		published = append(published, status)
	}
	before, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	device.mu.Lock()
	itemIDsBefore := slices.Clone(device.PlaybackQueue.ItemIDs())
	currentIDBefore := device.PlaybackQueue.CurrentItem().QueueItemID
	sceneBefore := device.sceneFingerprintLocked()
	device.mu.Unlock()

	status, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandShuffle})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status != observed {
		t.Fatalf("legacy shuffle returned a stale status: returned=%#v observed=%#v", status, observed)
	}
	want := before
	want.QueueVersion++
	want.StateSequence++
	want.CurrentIndex = observed.CurrentIndex
	if observed != want {
		t.Fatalf("legacy shuffle advanced state incorrectly: before=%#v observed=%#v want=%#v", before, observed, want)
	}

	device.mu.Lock()
	itemIDsAfter := device.PlaybackQueue.ItemIDs()
	currentAfter := device.PlaybackQueue.CurrentItem()
	device.mu.Unlock()
	sortedBefore, sortedAfter := slices.Clone(itemIDsBefore), slices.Clone(itemIDsAfter)
	slices.Sort(sortedBefore)
	slices.Sort(sortedAfter)
	if !slices.Equal(sortedBefore, sortedAfter) {
		t.Fatalf("legacy shuffle lost queue items: before=%v after=%v", itemIDsBefore, itemIDsAfter)
	}
	if len(itemIDsAfter) != len(itemIDsBefore) {
		t.Fatalf("legacy shuffle changed the queue size: %d -> %d", len(itemIDsBefore), len(itemIDsAfter))
	}
	if currentAfter == nil || currentAfter.QueueItemID != currentIDBefore {
		t.Fatalf("legacy shuffle did not preserve the current queue item: before=%q after=%#v", currentIDBefore, currentAfter)
	}
	if device.ActiveTrack != track || !track.playing || track.position != 9 {
		t.Fatalf("legacy shuffle touched the active output: playing=%v position=%d", track.playing, track.position)
	}
	if track.pauseCalls != 0 || track.closeCalls != 0 || track.positionSetCalls != 0 || track.muteCalls != 0 || len(track.volumeValues) != 0 {
		t.Fatalf("legacy shuffle issued an output command: %#v", track)
	}
	if device.playbackInstanceID != "instance-legacy" {
		t.Fatalf("legacy shuffle reissued the playback instance: %q", device.playbackInstanceID)
	}
	if device.mode != PlaybackModeOrder {
		t.Fatalf("legacy shuffle masqueraded as shufflePlay mode selection: mode=%q", device.mode)
	}
	if len(published) != 1 || published[0] != want {
		t.Fatalf("legacy shuffle did not publish the changed state once: published=%#v want=%#v", published, want)
	}
	if device.SceneFingerprint() == sceneBefore {
		t.Fatal("legacy shuffle did not advance the scene revision")
	}
}

// T14-AC06: the retained legacy physical shuffle is not the canonical
// shufflePlay mode selection. It leaves the Playback Mode untouched and
// consumes no random selection: after a legacy reorder, manual next still
// follows positional order semantics (the successor in the reordered visible
// queue), while an explicit canonical mode write to shufflePlay is a separate
// mode-authoritative command that never reorders the visible queue.
func TestPlaybackDeviceLegacyShuffleIsNotModeSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	device.PlaybackQueue.Add(model.MediaFiles{
		{ID: "1", Path: "/1.mp3"},
		{ID: "2", Path: "/2.mp3"},
		{ID: "3", Path: "/3.mp3"},
		{ID: "4", Path: "/4.mp3"},
	})
	device.PlaybackQueue.SetIndex(1)
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		device.preparing.Wait()
		newTrack = previousNewTrack
	})
	device.mu.Lock()
	device.randomFn = func(int) int { panic("legacy shuffle/next must not consume random selection") }
	device.mu.Unlock()

	legacy, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandShuffle})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Mode != PlaybackModeOrder {
		t.Fatalf("legacy shuffle changed the Playback Mode: %q", legacy.Mode)
	}
	device.mu.Lock()
	orderAfterShuffle := slices.Clone(device.PlaybackQueue.ItemIDs())
	indexAfterShuffle := device.PlaybackQueue.Index
	device.mu.Unlock()

	if _, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandNext}); err != nil {
		t.Fatal(err)
	}
	device.mu.Lock()
	successorID := orderAfterShuffle[(indexAfterShuffle+1)%len(orderAfterShuffle)]
	current := device.PlaybackQueue.CurrentItem()
	device.mu.Unlock()
	if current == nil || current.QueueItemID != successorID {
		t.Fatalf("manual next after legacy shuffle did not follow positional order: want=%q got=%#v", successorID, current)
	}
	if device.mode != PlaybackModeOrder {
		t.Fatalf("manual next after legacy shuffle changed the mode: %q", device.mode)
	}

	// The canonical shufflePlay mode write is explicit and mode-authoritative:
	// it changes only the mode (+1 semantic sequence) and never reorders the
	// visible queue.
	device.mu.Lock()
	itemIDsBeforeMode := slices.Clone(device.PlaybackQueue.ItemIDs())
	sequenceBeforeMode := device.stateSequence
	device.mu.Unlock()
	modeStatus, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandSetMode, Mode: PlaybackModeShuffle})
	if err != nil {
		t.Fatal(err)
	}
	if modeStatus.Mode != PlaybackModeShuffle {
		t.Fatalf("mode write did not select shufflePlay: %#v", modeStatus)
	}
	if modeStatus.StateSequence != sequenceBeforeMode+1 {
		t.Fatalf("mode write did not advance the semantic sequence once: %#v", modeStatus)
	}
	if !slices.Equal(itemIDsBeforeMode, device.PlaybackQueue.ItemIDs()) {
		t.Fatal("shufflePlay mode write reordered the visible queue")
	}
}

// T14-AC06: after the retained legacy physical reorder, positional order-mode
// semantics still govern Natural End — the reorder does not install shuffle
// continuation. The terminal item stops playback and is retained, and the
// natural stop does not reshuffle the visible queue.
func TestPlaybackDeviceLegacyShuffleOrderModeTerminalNaturalEndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	files := model.MediaFiles{{ID: "1", Path: "/1.mp3"}, {ID: "2", Path: "/2.mp3"}, {ID: "3", Path: "/3.mp3"}}
	repo.SetData(files)
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1", "2", "3"}); err != nil {
		t.Fatal(err)
	}
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		device.preparing.Wait()
		newTrack = previousNewTrack
	})
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Retained legacy physical reorder during playback.
	if _, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandShuffle}); err != nil {
		t.Fatal(err)
	}
	device.mu.Lock()
	orderAfterShuffle := slices.Clone(device.PlaybackQueue.ItemIDs())
	finalIndex := device.PlaybackQueue.Size() - 1
	device.mu.Unlock()

	if _, err := device.Skip(ctx, finalIndex, 0); err != nil {
		t.Fatal(err)
	}
	device.mu.Lock()
	token := device.activePlaybackToken
	queueVersionAfterSkip := device.queueVersion
	device.mu.Unlock()
	initial, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	device.PlaybackEvents <- model.PlaybackEvent{Token: token, Kind: model.PlaybackEventNaturalEnd}

	deadline := time.Now().Add(time.Second)
	var final DeviceStatus
	for time.Now().Before(deadline) {
		status, err := device.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Playing && status.CurrentIndex == finalIndex && status.StateSequence >= initial.StateSequence+1 {
			final = status
			break
		}
		time.Sleep(time.Millisecond)
	}
	if final.Playing || final.CurrentIndex != finalIndex {
		t.Fatalf("terminal natural end after legacy shuffle did not stop and retain the final item: final=%#v", final)
	}
	device.mu.Lock()
	current := device.PlaybackQueue.CurrentItem()
	device.mu.Unlock()
	if current == nil || current.QueueItemID != orderAfterShuffle[finalIndex] {
		t.Fatalf("terminal natural end did not retain the final queue item: want=%q got=%#v", orderAfterShuffle[finalIndex], current)
	}
	if !slices.Equal(orderAfterShuffle, device.PlaybackQueue.ItemIDs()) {
		t.Fatal("terminal natural end reshuffled the visible queue")
	}
	if device.queueVersion != queueVersionAfterSkip {
		t.Fatalf("terminal natural end advanced the structural queue version: %d -> %d", queueVersionAfterSkip, device.queueVersion)
	}
	if device.mode != PlaybackModeOrder {
		t.Fatalf("legacy shuffle installed a mode: %q", device.mode)
	}
}

// T14-AC05/AC06: canonical shufflePlay selection is mode-authoritative. With
// the existing injected deterministic random source it strictly excludes the
// current queue item, and it never physically reorders the visible queue —
// including after a prior retained legacy physical shuffle.
func TestPlaybackDeviceShufflePlaySelectionDoesNotReorderVisibleQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	files := model.MediaFiles{
		{ID: "1", Path: "/1.mp3"},
		{ID: "2", Path: "/2.mp3"},
		{ID: "3", Path: "/3.mp3"},
		{ID: "4", Path: "/4.mp3"},
	}
	repo.SetData(files)
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1", "2", "3", "4"}); err != nil {
		t.Fatal(err)
	}
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		device.preparing.Wait()
		newTrack = previousNewTrack
	})
	device.mu.Lock()
	device.mode = PlaybackModeShuffle
	device.PlaybackQueue.SetIndex(1)
	device.randomFn = func(int) int { return 0 }
	device.mu.Unlock()
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	device.mu.Lock()
	orderBefore := slices.Clone(device.PlaybackQueue.ItemIDs())
	queueVersionBefore := device.queueVersion
	token := device.activePlaybackToken
	device.mu.Unlock()
	initial, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Deterministic canonical selection: current index 1, injected pick 0
	// excludes the current item and lands on index 0.
	device.PlaybackEvents <- model.PlaybackEvent{Token: token, Kind: model.PlaybackEventNaturalEnd}
	deadline := time.Now().Add(time.Second)
	var advanced DeviceStatus
	for time.Now().Before(deadline) {
		status, err := device.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Playing && status.CurrentIndex == 0 && status.StateSequence >= initial.StateSequence+2 {
			advanced = status
			break
		}
		time.Sleep(time.Millisecond)
	}
	if advanced.CurrentIndex != 0 || !advanced.Playing {
		t.Fatalf("shufflePlay natural selection mismatch: advanced=%#v", advanced)
	}
	device.mu.Lock()
	orderAfterAdvance := slices.Clone(device.PlaybackQueue.ItemIDs())
	queueVersionAfterAdvance := device.queueVersion
	token = device.activePlaybackToken
	device.mu.Unlock()
	if !slices.Equal(orderAfterAdvance, orderBefore) {
		t.Fatal("shufflePlay natural selection reordered the visible queue")
	}
	if queueVersionAfterAdvance != queueVersionBefore {
		t.Fatalf("shufflePlay natural selection advanced the structural queue version: %d -> %d", queueVersionBefore, queueVersionAfterAdvance)
	}

	// A retained legacy physical reorder may change the visible order; a
	// subsequent canonical selection must leave that exact order untouched.
	if _, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandShuffle}); err != nil {
		t.Fatal(err)
	}
	device.mu.Lock()
	legacyOrder := slices.Clone(device.PlaybackQueue.ItemIDs())
	legacyIndex := device.PlaybackQueue.Index
	device.mu.Unlock()
	legacyStatus, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if legacyStatus.Mode != PlaybackModeShuffle {
		t.Fatalf("legacy shuffle changed the canonical mode: %q", legacyStatus.Mode)
	}

	device.PlaybackEvents <- model.PlaybackEvent{Token: token, Kind: model.PlaybackEventNaturalEnd}
	deadline = time.Now().Add(time.Second)
	var selected DeviceStatus
	for time.Now().Before(deadline) {
		status, err := device.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Playing && status.StateSequence >= legacyStatus.StateSequence+2 {
			selected = status
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !selected.Playing {
		t.Fatalf("shufflePlay selection after legacy shuffle did not continue playback: selected=%#v", selected)
	}
	device.mu.Lock()
	finalOrder := slices.Clone(device.PlaybackQueue.ItemIDs())
	current := device.PlaybackQueue.CurrentItem()
	device.mu.Unlock()
	if !slices.Equal(finalOrder, legacyOrder) {
		t.Fatal("shufflePlay selection after a legacy shuffle reordered the visible queue")
	}
	// Deterministic exclusion: injected pick 0 skips the current index.
	wantIndex := 0
	if wantIndex >= legacyIndex {
		wantIndex++
	}
	if selected.CurrentIndex != wantIndex || current == nil || current.QueueItemID != legacyOrder[wantIndex] {
		t.Fatalf("shufflePlay selection did not exclude the current item: selected=%#v current=%#v wantIndex=%d", selected, current, wantIndex)
	}
}
