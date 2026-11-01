package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/tests"
)

var migrationOperationSequence atomic.Uint64

func migrationCommandScope(scope CommandScope) CommandScope {
	if scope.SessionID != "" && scope.BindingGeneration != nil {
		scope.OperationID = fmt.Sprintf("g%d:validation-%d", *scope.BindingGeneration, migrationOperationSequence.Add(1))
	}
	return scope
}

func newMigrationValidationServer(t *testing.T) (context.Context, *playbackServer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "track", Path: "/validation.wav"}})
	server := &playbackServer{
		ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo},
		sessionManager: NewSessionManager(time.Minute), eventBroker: &fakeEventBroker{},
	}
	oldDevice := server.newPlaybackDevice(ctx, "Old", "old")
	oldDevice.Default = true
	server.playbackDevices = []*playbackDevice{oldDevice, server.newPlaybackDevice(ctx, "New", "new")}
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })
	return ctx, server
}

func TestPlaybackServerPausedMigrationPreservesResumePosition(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	device, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Skip(ctx, 0, 17); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := server.SwitchDevice(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	migrated, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	status, err := migrated.Status(ctx)
	if err != nil || status.Playing || status.Position != 17 {
		t.Fatalf("paused migration lost position: %#v, %v", status, err)
	}
	status, err = migrated.Start(ctx)
	if err != nil || !status.Playing || status.Position != 17 {
		t.Fatalf("resume did not preserve migrated position: %#v, %v", status, err)
	}
}

func TestPlaybackServerPlayingMigrationRestoresBeforeUnpause(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	firstPosition := make(chan int, 1)
	firstGain := make(chan float32, 1)
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, device string, _ model.MediaFile) (Track, error) {
		track := &mockTrack{}
		if device == "new" {
			track.onUnpause = func() error {
				firstPosition <- track.position
				gain := float32(-1)
				if len(track.volumeValues) > 0 {
					gain = track.volumeValues[len(track.volumeValues)-1]
				}
				firstGain <- gain
				return nil
			}
		}
		return track, nil
	}
	device, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.SetGain(ctx, 0.375); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Skip(ctx, 0, 17); err != nil {
		t.Fatal(err)
	}
	if err := server.SwitchDevice(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	if position, gain := <-firstPosition, <-firstGain; position != 17 || gain != 0.375 {
		t.Fatalf("first new-output unpause used position=%d gain=%v, want position=17 gain=0.375", position, gain)
	}
}

func startMigrationValidationService(t *testing.T, server *playbackServer) (context.Context, context.CancelFunc, *playbackDevice, <-chan error) {
	t.Helper()
	previousConfig := conf.Server.Jukebox
	conf.Server.Jukebox.Devices = []conf.AudioDeviceDefinition{{"Old", "old"}, {"New", "new"}}
	conf.Server.Jukebox.Default = "Old"
	conf.Server.Jukebox.AutoDiscoverBluetooth = false
	t.Cleanup(func() { conf.Server.Jukebox = previousConfig })
	server.playbackDevices = nil
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		device, err := server.GetDeviceForUser("admin")
		if err == nil {
			return ctx, cancel, device, done
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("service did not initialize its devices")
	return ctx, cancel, nil, done
}

func TestPlaybackServerRunWaitsForOutputRetirement(t *testing.T) {
	_, server := newMigrationValidationServer(t)
	closeEntered := make(chan struct{}, 1)
	releaseClose := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseClose) }) }
	closed := make(chan struct{}, 1)
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{onClose: func() {
			closeEntered <- struct{}{}
			<-releaseClose
			closed <- struct{}{}
		}}, nil
	}
	ctx, cancel, device, done := startMigrationValidationService(t, server)
	t.Cleanup(func() { _, _ = device.Shutdown(context.Background()) })
	t.Cleanup(unblock)
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-closeEntered:
	case <-time.After(time.Second):
		t.Fatal("service did not begin retiring its output")
	}
	select {
	case <-done:
		t.Fatal("Run returned while output Close was still releasing resources")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not finish after output retirement")
	}
	select {
	case <-closed:
	default:
		t.Fatal("Run returned before retiring its active output")
	}
}

func TestPlaybackServerRunWaitsForPendingOutputRetirement(t *testing.T) {
	if os.Getenv("ND_TEST_PENDING_SERVICE_RETIREMENT") != "1" {
		t.Skip("set ND_TEST_PENDING_SERVICE_RETIREMENT=1 for the pending-output shutdown acceptance probe")
	}
	_, server := newMigrationValidationServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{}, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		track := &mockTrack{onClose: func() { closed <- struct{}{} }}
		close(entered)
		<-release
		return track, nil
	}
	ctx, cancel, device, done := startMigrationValidationService(t, server)
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() {
		_, err := device.Start(ctx)
		started <- err
	}()
	t.Cleanup(func() {
		unblock()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Error("owned pending Start did not finish after releasing its factory")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("factory did not enter pending output preparation")
	}
	cancel()
	returnedBeforeRelease := false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		returnedBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late canceled output was not eventually released")
	}
	if !returnedBeforeRelease {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("Run did not finish after pending output retirement")
		}
	}
	if returnedBeforeRelease {
		t.Fatal("Run returned while its factory still owned an unretired preparing output; late output closed only after factory release")
	}
}

func TestPlaybackServiceStopsOtherOutputsBeforeWaitingForPendingFactory(t *testing.T) {
	_, server := newMigrationValidationServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	otherClosed := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, deviceName string, _ model.MediaFile) (Track, error) {
		if deviceName == "old" {
			close(entered)
			<-release
			return &mockTrack{}, nil
		}
		return &mockTrack{onClose: func() { close(otherClosed) }}, nil
	}
	ctx, cancel, device, done := startMigrationValidationService(t, server)
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { _, err := device.Start(ctx); started <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("pending factory did not enter")
	}
	other := server.getDeviceByName("new")
	if _, err := other.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	retiredBeforeFactoryRelease := false
	select {
	case <-otherClosed:
		retiredBeforeFactoryRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not finish after pending factory release")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pending start did not return")
	}
	if !retiredBeforeFactoryRelease {
		t.Fatal("pending factory prevented another active output from being stopped during service exit")
	}
}

func playbackAuthorityValidationCommands() []PlaybackCommand {
	return []PlaybackCommand{
		{Action: CommandSet, IDs: []string{"track", "track"}},
		{Action: CommandGain, Gain: 0.25},
		{Action: CommandStart},
		{Action: CommandSeek, Position: 17},
		{Action: CommandPause},
		{Action: CommandStart},
		{Action: CommandSkip, Index: 1, Offset: 3},
		{Action: CommandStop},
		{Action: CommandAdd, IDs: []string{"track"}},
		{Action: CommandInsert, Index: 1, IDs: []string{"track"}},
		{Action: CommandMove, From: 0, To: 2},
		{Action: CommandRemove, Index: 1},
		{Action: CommandShuffle},
		{Action: CommandClear},
	}
}

func TestPlaybackExplicitTakeoverPreservesPlaybackAndRevokesAllOldOwnerMutations(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	ctx = request.WithUser(ctx, model.User{UserName: "admin"})
	status, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "old-tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	oldScope := CommandScope{User: "admin", SessionID: "owner", ClientID: "old-tab", BindingGeneration: &status.BindingGeneration}
	for _, command := range []PlaybackCommand{
		{Action: CommandSet, IDs: []string{"track", "track"}},
		{Action: CommandStart},
		{Action: CommandSeek, Position: 17},
		{Action: CommandGain, Gain: 0.375},
	} {
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), command); err != nil {
			t.Fatal(err)
		}
	}
	before, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil {
		t.Fatal(err)
	}
	takenOver, err := server.AttachSession(ctx, AttachRequest{
		SessionID: "owner", User: "admin", ClientID: "new-tab", DeviceName: "old", Takeover: true,
	})
	if err != nil || takenOver.OwnerClientID != "new-tab" || !takenOver.Playing || takenOver.Position != 17 || takenOver.Gain != 0.375 || takenOver.QueueVersion != before.SessionStatus.QueueVersion {
		t.Fatalf("explicit takeover changed playback: %#v before=%#v err=%v", takenOver, before, err)
	}
	for _, command := range playbackAuthorityValidationCommands() {
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("old owner retained %s after explicit takeover: %v", command.Action, err)
		}
	}
	if err := server.SwitchDeviceForCommand(ctx, migrationCommandScope(oldScope), "new"); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("old owner retained device switch: %v", err)
	}
	if _, err := server.HeartbeatSession(ctx, "owner", "old-tab"); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("old owner retained heartbeat: %v", err)
	}
	if _, err := server.DetachSession(ctx, "owner", "old-tab"); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("old owner retained detach: %v", err)
	}
	for _, attempted := range []AttachRequest{
		{SessionID: "alternate", User: "admin", ClientID: "other", DeviceName: "old", Takeover: true},
		{SessionID: "owner", User: "another-user", ClientID: "other", DeviceName: "old", Takeover: true},
	} {
		if _, err := server.AttachSession(ctx, attempted); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("explicit takeover bypassed authenticated session identity: %#v err=%v", attempted, err)
		}
	}
	after, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil || after.SessionStatus.OwnerClientID != "new-tab" || after.SessionStatus.Position != before.SessionStatus.Position || after.SessionStatus.Gain != before.SessionStatus.Gain || after.SessionStatus.QueueVersion != before.SessionStatus.QueueVersion || len(after.JukeboxPlaylist.Entry) != len(before.JukeboxPlaylist.Entry) {
		t.Fatalf("denied old-owner mutations changed playback: before=%#v after=%#v err=%v", before, after, err)
	}
	newScope := oldScope
	newScope.ClientID = "new-tab"
	newScope.BindingGeneration = &takenOver.BindingGeneration
	if status, err := server.ExecuteCommand(ctx, migrationCommandScope(newScope), PlaybackCommand{Action: CommandPause}); err != nil || status.Playing || status.Position != 17 {
		t.Fatalf("new owner cannot control preserved playback: %#v err=%v", status, err)
	}
}

func TestPlaybackExplicitTakeoverSerializesWithAdmittedOldOwnerMutation(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	status, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "old-tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{onSetVolume: func(gain float32) error {
			if gain == 0.25 {
				close(entered)
				<-release
			}
			return nil
		}}, nil
	}
	oldScope := CommandScope{User: "admin", SessionID: "owner", ClientID: "old-tab", BindingGeneration: &status.BindingGeneration}
	for _, command := range []PlaybackCommand{{Action: CommandSet, IDs: []string{"track"}}, {Action: CommandStart}} {
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), command); err != nil {
			t.Fatal(err)
		}
	}
	mutated := make(chan error, 1)
	go func() {
		_, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), PlaybackCommand{Action: CommandGain, Gain: 0.25})
		mutated <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old-owner mutation did not reach admitted state lock")
	}
	takenOver := make(chan error, 1)
	go func() {
		_, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "new-tab", DeviceName: "old", Takeover: true})
		takenOver <- err
	}()
	select {
	case <-takenOver:
		t.Fatal("takeover completed before the admitted old-owner mutation linearized")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	for name, done := range map[string]<-chan error{"admitted mutation": mutated, "takeover": takenOver} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s failed: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s deadlocked", name)
		}
	}
	state, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil || state.SessionStatus.OwnerClientID != "new-tab" || state.SessionStatus.Gain != 0.25 || len(state.JukeboxPlaylist.Entry) != 1 {
		t.Fatalf("serialized takeover lost admitted playback state: %#v err=%v", state, err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), PlaybackCommand{Action: CommandGain, Gain: 0.5}); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("old owner mutated after takeover completion: %v", err)
	}
}

func TestPlaybackExplicitTakeoverCancelsInFlightOldOwnerPreparation(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	status, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "old-tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	oldScope := CommandScope{User: "admin", SessionID: "owner", ClientID: "old-tab", BindingGeneration: &status.BindingGeneration}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	unpaused := make(chan struct{}, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	previous := newTrack
	newTrack = func(prepareCtx context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		close(entered)
		<-release
		if prepareCtx.Err() == nil {
			return nil, errors.New("takeover did not cancel old-owner preparation")
		}
		return &mockTrack{onClose: func() { close(closed) }, onUnpause: func() error { unpaused <- struct{}{}; return nil }}, nil
	}
	started := make(chan error, 1)
	go func() {
		_, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), PlaybackCommand{Action: CommandStart})
		started <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old-owner preparation did not enter")
	}
	takeoverStatus, err := server.AttachSession(ctx, AttachRequest{
		SessionID: "owner", User: "admin", ClientID: "new-tab", DeviceName: "old", Takeover: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(oldScope), PlaybackCommand{Action: CommandStop}); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("old owner retained command authority during retirement: %v", err)
	}
	unblock()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("superseded old-owner preparation did not return")
	}
	select {
	case <-closed:
	default:
		t.Fatal("superseded output was not retired")
	}
	select {
	case <-unpaused:
		t.Fatal("late old-owner preparation restored playback after takeover")
	default:
	}
	newTrack = previous
	newScope := oldScope
	newScope.ClientID = "new-tab"
	newScope.BindingGeneration = &takeoverStatus.BindingGeneration
	if status, err := server.ExecuteCommand(ctx, migrationCommandScope(newScope), PlaybackCommand{Action: CommandStart}); err != nil || !status.Playing {
		t.Fatalf("new owner could not retry logical playback: %#v err=%v", status, err)
	}
}

func TestPlaybackCommandAuthorityValidation(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	ctx = request.WithUser(ctx, model.User{UserName: "admin"})
	status, err := server.AttachSession(ctx, AttachRequest{SessionID: "bound", User: "admin", ClientID: "owner", DeviceName: "new"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "bound", ClientID: "owner", BindingGeneration: &status.BindingGeneration}
	commands := playbackAuthorityValidationCommands()
	for _, command := range commands {
		if _, err := server.ExecuteCommand(ctx, CommandScope{User: "admin"}, command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("bare command bypassed non-default binding for %s: %v", command.Action, err)
		}
		wrongOwner := scope
		wrongOwner.ClientID = "other"
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(wrongOwner), command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("non-owner %s accepted: %v", command.Action, err)
		}
		wrongUser := scope
		wrongUser.User = "other-user"
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(wrongUser), command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("forged user %s accepted: %v", command.Action, err)
		}
		status, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), command)
		if command.Action == CommandMove {
			// T13-AC10: protected incremental edits lacking stable instance
			// identity are explicitly rejected after authority checks pass.
			if !errors.Is(err, ErrMoveProtocolConflict) {
				t.Fatalf("owner legacy %s not rejected: %v", command.Action, err)
			}
			continue
		}
		if command.Action == CommandSkip || command.Action == CommandRemove {
			// T13: protected selection/removal lacking stable instance identity
			// is explicitly rejected after authority checks pass.
			if !errors.Is(err, ErrIdentityRequired) {
				t.Fatalf("owner legacy %s not rejected: %v", command.Action, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("owner %s failed: %v", command.Action, err)
		}
		if command.Action == CommandSeek && status.Position != 17 {
			t.Fatalf("seek used wrong position: %#v", status)
		}
		if command.Action == CommandPause && status.Playing {
			t.Fatalf("pause did not pause: %#v", status)
		}
		if command.Action == CommandStart && !status.Playing {
			t.Fatalf("start did not start: %#v", status)
		}
		defaultStatus, _ := server.playbackDevices[0].Status(ctx)
		if defaultStatus.Gain != DefaultGain || defaultStatus.Playing || !server.playbackDevices[0].PlaybackQueue.IsEmpty() {
			t.Fatalf("session-bound command %s changed default: %#v", command.Action, defaultStatus)
		}
	}
	if _, err := server.ExecuteCommand(ctx, CommandScope{User: "admin", SessionID: "absent", ClientID: "owner"}, PlaybackCommand{Action: CommandGain}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing explicit session fell back: %v", err)
	}
	if _, err := server.ExecuteCommand(ctx, CommandScope{User: "admin", SessionID: "bound"}, PlaybackCommand{Action: CommandGain}); !errors.Is(err, ErrCommandScope) {
		t.Fatalf("partial scope accepted: %v", err)
	}
}

func TestPlaybackScopedSwitchRejectsBypassAndRebindsAuthority(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	status, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "owner", ClientID: "tab", BindingGeneration: &status.BindingGeneration}
	for _, denied := range []CommandScope{{User: "admin"}, {User: "admin", SessionID: "owner", ClientID: "other"}} {
		if err := server.SwitchDeviceForCommand(ctx, migrationCommandScope(denied), "new"); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("switch bypass accepted: %#v err=%v", denied, err)
		}
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	oldFingerprint := server.playbackDevices[0].SceneFingerprint()
	newFingerprint := server.playbackDevices[1].SceneFingerprint()
	if err := server.SwitchDeviceForCommand(ctx, migrationCommandScope(scope), "new"); err != nil {
		t.Fatal(err)
	}
	if server.playbackDevices[0].SceneFingerprint() == oldFingerprint || server.playbackDevices[1].SceneFingerprint() == newFingerprint {
		t.Fatal("device rebind did not advance both scene fingerprints")
	}
	snapAfterSwitch, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil {
		t.Fatal(err)
	}
	scope.BindingGeneration = &snapAfterSwitch.SessionStatus.BindingGeneration
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandGain, Gain: 0.375}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil || snapshot.SessionStatus.DeviceName != "new" || snapshot.SessionStatus.Gain != 0.375 || len(snapshot.JukeboxPlaylist.Entry) != 1 {
		t.Fatalf("scoped migration lost bound authority: %#v err=%v", snapshot, err)
	}
	oldStatus, _ := server.playbackDevices[0].Status(ctx)
	if oldStatus.Gain != DefaultGain || !server.playbackDevices[0].PlaybackQueue.IsEmpty() {
		t.Fatalf("follow-up scoped command modified retired device: %#v", oldStatus)
	}
}

func TestPlaybackScopedStopCancelsPreparingOutputWithoutResurrection(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	status, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "owner", ClientID: "tab", BindingGeneration: &status.BindingGeneration}
	_, err = server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandSet, IDs: []string{"track"}})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	canceled := make(chan struct{})
	closed := make(chan struct{})
	unpaused := make(chan struct{}, 1)
	newTrack = func(prepareCtx context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		close(entered)
		<-prepareCtx.Done()
		close(canceled)
		return &mockTrack{onClose: func() { close(closed) }, onUnpause: func() error { unpaused <- struct{}{}; return nil }}, nil
	}
	started := make(chan error, 1)
	go func() {
		_, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandStart})
		started <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandStop}); err != nil {
		t.Fatal(err)
	}
	for name, done := range map[string]<-chan struct{}{"cancellation": canceled, "retirement": closed} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("stop did not complete %s", name)
		}
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Start did not return")
	}
	select {
	case <-unpaused:
		t.Fatal("canceled preparing output restored playback")
	default:
	}
	snapshot, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil || snapshot.SessionStatus.Playing || len(snapshot.JukeboxPlaylist.Entry) != 1 {
		t.Fatalf("stop lost logical authority: %#v err=%v", snapshot, err)
	}
}

func TestPlaybackScopedMigrationPreparationCanBeCanceledWithoutLateUnpause(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	status, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "owner", ClientID: "tab", BindingGeneration: &status.BindingGeneration}
	for _, command := range []PlaybackCommand{{Action: CommandSet, IDs: []string{"track"}}, {Action: CommandStart}} {
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), command); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	unpaused := make(chan struct{}, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	previous := newTrack
	newTrack = func(prepareCtx context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, deviceName string, _ model.MediaFile) (Track, error) {
		if deviceName != "new" {
			return nil, errors.New("migration prepared wrong output")
		}
		close(entered)
		<-release
		if prepareCtx.Err() == nil {
			return nil, errors.New("stop did not cancel migration preparation context")
		}
		return &mockTrack{onClose: func() { close(closed) }, onUnpause: func() error { unpaused <- struct{}{}; return nil }}, nil
	}
	switched := make(chan error, 1)
	go func() { switched <- server.SwitchDeviceForCommand(ctx, migrationCommandScope(scope), "new") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("migration did not enter target preparation")
	}
	stopScope := scope
	gen2 := uint64(2)
	stopScope.BindingGeneration = &gen2
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(stopScope), PlaybackCommand{Action: CommandStop}); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-switched:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled migration did not return")
	}
	select {
	case <-closed:
	default:
		t.Fatal("late migration output was not retired")
	}
	select {
	case <-unpaused:
		t.Fatal("late migration output resumed after stop")
	default:
	}
	state, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil || state.SessionStatus.DeviceName != "new" || state.SessionStatus.Playing || len(state.JukeboxPlaylist.Entry) != 1 {
		t.Fatalf("canceled migration lost logical session: %#v err=%v", state, err)
	}
	newTrack = previous
	scope.BindingGeneration = &state.SessionStatus.BindingGeneration
	if status, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandStart}); err != nil || !status.Playing {
		t.Fatalf("logical session cannot retry after canceled migration: %#v err=%v", status, err)
	}
}

func TestPlaybackServiceCancellationRetiresCooperativePreparingOutput(t *testing.T) {
	_, server := newMigrationValidationServer(t)
	entered := make(chan struct{})
	closed := make(chan struct{})
	newTrack = func(ctx context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		close(entered)
		<-ctx.Done()
		return &mockTrack{onClose: func() { close(closed) }}, nil
	}
	ctx, cancel, device, done := startMigrationValidationService(t, server)
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { _, err := device.Start(ctx); started <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation did not enter")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cooperative preparing output blocked service exit")
	}
	select {
	case <-closed:
	default:
		t.Fatal("Run returned before cooperative output was closed")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("preparing Start did not return")
	}
	if _, err := device.Start(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("retired service admitted new preparation: %v", err)
	}
}

func TestPlaybackDeviceNaturalEndOfQueueRetiresLastOutput(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	closed := make(chan struct{}, 2)
	created := make(chan model.PlaybackToken, 2)
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		created <- token
		return &mockTrack{onClose: func() { closed <- struct{}{} }}, nil
	}
	device, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	token := <-created
	device.PlaybackEvents <- model.PlaybackEvent{Token: token, Kind: model.PlaybackEventNaturalEnd}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("natural end of the queue did not release the final output")
	}
	queue, status, err := device.Get(ctx)
	if err != nil || status.Playing || status.CurrentIndex != 0 || len(queue) != 1 || queue[0].ID != "track" {
		t.Fatalf("unexpected completed queue: %#v, %#v, %v", queue, status, err)
	}
	select {
	case <-created:
		t.Fatal("end of queue created a successor output")
	default:
	}
}

func TestPlaybackServerMigrationToAttachedTargetDoesNotDeadlock(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	device, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AttachSession(ctx, AttachRequest{SessionID: "target", User: "admin", ClientID: "controller", DeviceName: "new"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.SwitchDevice(ctx, "new") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("migration to an attached target did not finish; output notification re-enters server state")
	}
	status, err := server.SessionStatus(ctx, "target")
	if err != nil || !status.Playing || status.TrackID != "track" {
		t.Fatalf("target did not observe migrated playback: %#v, %v", status, err)
	}
}

func TestPlaybackServerScopedSwitchToTargetWithExistingSessionRejected(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	st1, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner-session", User: "admin", ClientID: "tab-1", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope1 := CommandScope{User: "admin", SessionID: "owner-session", ClientID: "tab-1", BindingGeneration: &st1.BindingGeneration}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope1), PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope1), PlaybackCommand{Action: CommandStart}); err != nil {
		t.Fatal(err)
	}

	st2, err := server.AttachSession(ctx, AttachRequest{SessionID: "target-session", User: "admin", ClientID: "other-tab", DeviceName: "new"})
	if err != nil {
		t.Fatal(err)
	}
	scope2 := CommandScope{User: "admin", SessionID: "target-session", ClientID: "other-tab", BindingGeneration: &st2.BindingGeneration}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope2), PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope2), PlaybackCommand{Action: CommandStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope2), PlaybackCommand{Action: CommandSeek, Position: 12}); err != nil {
		t.Fatal(err)
	}

	err = server.SwitchDeviceForCommand(ctx, migrationCommandScope(scope1), "new")
	if !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("expected ErrSessionOwnership when target has active session, got: %v", err)
	}

	targetStatus, err := server.SessionStatus(ctx, "target-session")
	if err != nil || !targetStatus.Playing || targetStatus.Position != 12 || targetStatus.DeviceName != "new" {
		t.Fatalf("target session modified after rejected migration: %#v err=%v", targetStatus, err)
	}
	oldStatus, err := server.SessionStatus(ctx, "owner-session")
	if err != nil || !oldStatus.Playing || oldStatus.DeviceName != "old" {
		t.Fatalf("old session modified after rejected migration: %#v err=%v", oldStatus, err)
	}
}

func TestPlaybackSameOwnerLateCommandAcrossDeviceSwitchDoesNotMutateNewScene(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	st, err := server.AttachSession(ctx, AttachRequest{SessionID: "s1", User: "admin", ClientID: "tab-1", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "s1", ClientID: "tab-1", BindingGeneration: &st.BindingGeneration}

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var enterOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{onSetVolume: func(gain float32) error {
			if gain == 0.125 {
				enterOnce.Do(func() { close(entered) })
				<-release
			}
			return nil
		}}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandSeek, Position: 10}); err != nil {
		t.Fatal(err)
	}

	oldDev := server.playbackDevices[0]
	lateCmdDone := make(chan error, 1)
	go func() {
		_, err := oldDev.SetGain(ctx, 0.125)
		lateCmdDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("late command did not enter execution")
	}

	switched := make(chan error, 1)
	go func() {
		switched <- server.SwitchDeviceForCommand(ctx, migrationCommandScope(scope), "new")
	}()

	select {
	case <-switched:
		t.Fatal("switch finished before in-flight old device mutation linearized")
	case <-time.After(20 * time.Millisecond):
	}

	unblock()

	select {
	case err := <-lateCmdDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("late command deadlocked")
	}

	select {
	case err := <-switched:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("switch deadlocked")
	}

	// Verify session was rebound to new device
	newStatus, err := server.SessionStatus(ctx, "s1")
	if err != nil || newStatus.DeviceName != "new" || !newStatus.Playing || newStatus.Position != 10 {
		t.Fatalf("switch did not rebind cleanly: %#v, %v", newStatus, err)
	}

	// Subsequent late mutations targeting the retired old device cannot mutate the new scene
	_, _ = oldDev.SetGain(ctx, 0.99)
	newStatusAfter, err := server.SessionStatus(ctx, "s1")
	if err != nil || newStatusAfter.Gain == 0.99 || newStatusAfter.DeviceName != "new" {
		t.Fatalf("late old device command corrupted new scene: %#v", newStatusAfter)
	}
}

func TestPlaybackServerMigrationTargetStartFailurePreservesFailedSceneWithoutOutput(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	device, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := device.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.SetGain(ctx, 0.5); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Skip(ctx, 0, 17); err != nil {
		t.Fatal(err)
	}

	targetPrepErr := errors.New("target volume/preparation failed")
	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, deviceName string, _ model.MediaFile) (Track, error) {
		if deviceName == "new" {
			return nil, targetPrepErr
		}
		return &mockTrack{}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	err = server.SwitchDevice(ctx, "new")
	if !errors.Is(err, targetPrepErr) {
		t.Fatalf("expected target preparation failure error, got: %v", err)
	}

	oldDev := server.playbackDevices[0]
	oldStatus, _ := oldDev.Status(ctx)
	if oldStatus.Playing || !oldDev.PlaybackQueue.IsEmpty() {
		t.Fatalf("old device still playing or not cleared: %#v", oldStatus)
	}

	newDev := server.playbackDevices[1]
	newStatus, _ := newDev.Status(ctx)
	if newStatus.Playing {
		t.Fatalf("target device is playing despite start failure: %#v", newStatus)
	}

	if newStatus.Position != 17 || newStatus.Gain != 0.5 || newDev.PlaybackQueue.IsEmpty() {
		t.Fatalf("failed scene was not preserved on target device: %#v", newStatus)
	}

	if !newDev.Default || oldDev.Default {
		t.Fatalf("default device not switched to target: old.Default=%v new.Default=%v", oldDev.Default, newDev.Default)
	}

	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		return &mockTrack{}, nil
	}
	resumedStatus, err := newDev.Start(ctx)
	if err != nil || !resumedStatus.Playing || resumedStatus.Position != 17 || resumedStatus.Gain != 0.5 {
		t.Fatalf("retry after volume/prep failure did not resume from preserved scene: %#v, %v", resumedStatus, err)
	}
}

func TestPlaybackProtectedAnchorMoveReplayIsIdempotent(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	ctx = request.WithUser(ctx, model.User{UserName: "admin"})
	attached, err := server.AttachSession(ctx, AttachRequest{SessionID: "replay", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{
		User: "admin", SessionID: "replay", ClientID: "tab",
		BindingGeneration: &attached.BindingGeneration, OperationID: "g1:replay-set",
	}
	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandSet, IDs: []string{"track", "track", "track"}}); err != nil {
		t.Fatalf("protected set: %v", err)
	}
	snapshot, err := server.SessionSnapshot(ctx, "replay", "admin")
	if err != nil {
		t.Fatal(err)
	}
	entries := snapshot.JukeboxPlaylist.Entry
	if len(entries) != 3 || entries[0].QueueItemID == "" || entries[0].QueueItemID == entries[1].QueueItemID {
		t.Fatalf("snapshot lacks distinct queue item IDs: %#v", entries)
	}

	moveScope := CommandScope{
		User: "admin", SessionID: "replay", ClientID: "tab",
		BindingGeneration: &attached.BindingGeneration, OperationID: "g1:replay-move",
	}
	move := PlaybackCommand{
		Action: CommandMove, QueueItemID: entries[2].QueueItemID,
		AnchorQueueItemID: entries[0].QueueItemID, Placement: "before",
	}
	first, err := server.ExecuteCommand(ctx, moveScope, move)
	if err != nil {
		t.Fatalf("first anchor move: %v", err)
	}
	if first.QueueVersion != snapshot.SessionStatus.QueueVersion+1 {
		t.Fatalf("first move did not bump queue version once: %d -> %d", snapshot.SessionStatus.QueueVersion, first.QueueVersion)
	}

	replay, err := server.ExecuteCommand(ctx, moveScope, move)
	if err != nil {
		t.Fatalf("replay anchor move: %v", err)
	}
	if replay.QueueVersion != first.QueueVersion {
		t.Fatalf("replay re-executed the move: version %d -> %d", first.QueueVersion, replay.QueueVersion)
	}
	if replay.CurrentIndex != first.CurrentIndex || replay.Playing != first.Playing || replay.Position != first.Position ||
		replay.Gain != first.Gain || replay.DesiredGain != first.DesiredGain || replay.Muted != first.Muted {
		t.Fatalf("replay result differs from original: %#v vs %#v", replay, first)
	}

	after, err := server.SessionSnapshot(ctx, "replay", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionStatus.QueueVersion != first.QueueVersion {
		t.Fatalf("queue version changed after replay: %d", after.SessionStatus.QueueVersion)
	}
	order := after.JukeboxPlaylist.Entry
	if len(order) != 3 || order[0].QueueItemID != entries[2].QueueItemID ||
		order[1].QueueItemID != entries[0].QueueItemID || order[2].QueueItemID != entries[1].QueueItemID {
		t.Fatalf("replay mutated queue order: %#v", order)
	}
	if after.SessionStatus.QueueItemID != snapshot.SessionStatus.QueueItemID || after.SessionStatus.TrackID != snapshot.SessionStatus.TrackID {
		t.Fatalf("current identity changed: before=%#v after=%#v", snapshot.SessionStatus, after.SessionStatus)
	}
}

func migrationTestTarget(t *testing.T, server *playbackServer, deviceName string) *playbackDevice {
	t.Helper()
	for _, device := range server.playbackDevices {
		if device.DeviceName == deviceName {
			return device
		}
	}
	t.Fatalf("device %s not found", deviceName)
	return nil
}

func attachMigrationSource(t *testing.T, server *playbackServer, sessionID, clientID, deviceName string) SessionStatus {
	t.Helper()
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	status, err := server.AttachSession(ctx, AttachRequest{
		SessionID: sessionID, User: "admin", ClientID: clientID, DeviceName: deviceName,
	})
	if err != nil {
		t.Fatalf("attach %s: %v", sessionID, err)
	}
	return status
}

func reserveForCommit(t *testing.T, server *playbackServer, sessionID, clientID string, generation uint64, migrationID, targetDevice string) {
	t.Helper()
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	fp := migrationTestTarget(t, server, targetDevice).SceneFingerprint()
	resReq := ReserveMigrationRequest{
		MigrationID: migrationID, OperationID: "g1:reserve-" + migrationID,
		SessionID: sessionID, ClientID: clientID, User: "admin",
		DeviceName: targetDevice, ForceTakeover: true, ExpectedFingerprint: fp,
		BindingGeneration: &generation,
	}
	if _, err := server.ReserveMigration(ctx, resReq); err != nil {
		t.Fatalf("reserve %s: %v", migrationID, err)
	}
}

func TestPlaybackCommitMigrationIdentityTransport(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	identityQueue := func() *[]QueueItemRef {
		return &[]QueueItemRef{
			{QueueItemID: "qi-1", MediaFileID: "track"},
			{QueueItemID: "qi-2", MediaFileID: "track"},
			{QueueItemID: "qi-3", MediaFileID: "track"},
		}
	}
	currentTwo := func() *string { s := "qi-2"; return &s }

	t.Run("preserves duplicate instance IDs and maps current by ID", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-id", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-id", OperationID: "g1:commit-id", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: identityQueue(), CurrentQueueItemID: currentTwo(),
		})
		if err != nil {
			t.Fatalf("identity commit: %v", err)
		}
		if committed.CurrentIndex != 1 || committed.QueueItemID != "qi-2" || committed.TrackID != "track" {
			t.Fatalf("identity commit lost current mapping: %#v", committed)
		}
		snapshot, err := server.SessionSnapshot(ctx, "src", "admin")
		if err != nil {
			t.Fatal(err)
		}
		entries := snapshot.JukeboxPlaylist.Entry
		if len(entries) != 3 || entries[0].QueueItemID != "qi-1" || entries[1].QueueItemID != "qi-2" || entries[2].QueueItemID != "qi-3" {
			t.Fatalf("identity commit lost instance IDs: %#v", entries)
		}
	})

	t.Run("empty identity queue preserves idle context without startup", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-empty", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-empty", OperationID: "g1:commit-empty", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{},
		})
		if err != nil {
			t.Fatalf("empty identity commit: %v", err)
		}
		if committed.CurrentIndex != -1 || committed.Playing || committed.QueueItemID != "" {
			t.Fatalf("empty identity commit changed idle state: %#v", committed)
		}
	})

	t.Run("legacy queue rewrite still assigns fresh IDs", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-legacy", "new")
		if _, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-legacy", OperationID: "g1:commit-legacy", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			Queue: []string{"track", "track"}, CurrentIndex: 1,
		}); err != nil {
			t.Fatalf("legacy commit: %v", err)
		}
		snapshot, err := server.SessionSnapshot(ctx, "src", "admin")
		if err != nil {
			t.Fatal(err)
		}
		entries := snapshot.JukeboxPlaylist.Entry
		if len(entries) != 2 || entries[0].QueueItemID == "" || entries[0].QueueItemID == entries[1].QueueItemID {
			t.Fatalf("legacy commit did not assign fresh IDs: %#v", entries)
		}
		if snapshot.SessionStatus.CurrentIndex != 1 {
			t.Fatalf("legacy current index not mapped: %d", snapshot.SessionStatus.CurrentIndex)
		}
	})

	validationCases := []struct {
		name    string
		mutate  func(*CommitMigrationRequest)
		wantErr error
	}{
		{"mixed legacy queue and identity items", func(req *CommitMigrationRequest) {
			req.Queue = []string{"track"}
			req.QueueItems = identityQueue()
		}, ErrMigrationProtocolConflict},
		{"mixed current index and current ID", func(req *CommitMigrationRequest) {
			req.CurrentIndex = 2
			req.QueueItems = identityQueue()
			req.CurrentQueueItemID = currentTwo()
		}, ErrMigrationProtocolConflict},
		{"duplicate queue item IDs", func(req *CommitMigrationRequest) {
			req.QueueItems = &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-1", MediaFileID: "track"}}
		}, ErrMigrationProtocolConflict},
		{"empty queue item ID", func(req *CommitMigrationRequest) {
			req.QueueItems = &[]QueueItemRef{{QueueItemID: "", MediaFileID: "track"}}
		}, ErrMigrationProtocolConflict},
		{"current ID not a member", func(req *CommitMigrationRequest) {
			req.QueueItems = identityQueue()
			s := "qi-missing"
			req.CurrentQueueItemID = &s
		}, ErrMigrationProtocolConflict},
		{"unresolvable media file", func(req *CommitMigrationRequest) {
			req.QueueItems = &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "nope"}}
		}, nil},
	}
	for _, tc := range validationCases {
		t.Run("rejects "+tc.name+" before target mutation", func(t *testing.T) {
			_, server := newMigrationValidationServer(t)
			migrationTestTarget(t, server, "new").PlaybackQueue.Add(model.MediaFiles{{ID: "seed", Path: "/seed.mp3"}})
			attached := attachMigrationSource(t, server, "src", "tab", "old")
			reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-bad", "new")
			fpBefore := migrationTestTarget(t, server, "new").SceneFingerprint()
			req := CommitMigrationRequest{
				MigrationID: "mig-bad", OperationID: "g1:commit-bad", SessionID: "src", ClientID: "tab",
				User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			}
			tc.mutate(&req)
			_, err := server.CommitMigration(ctx, req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v, got %v", tc.wantErr, err)
				}
			} else if err == nil {
				t.Fatal("expected media resolution error")
			}
			after := migrationTestTarget(t, server, "new")
			after.mu.Lock()
			defer after.mu.Unlock()
			if after.sceneFingerprintLocked() != fpBefore {
				t.Fatal("rejected commit mutated target scene")
			}
			if after.PlaybackQueue.Size() != 1 || after.PlaybackQueue.Get()[0].ID != "seed" {
				t.Fatalf("rejected commit mutated target queue: %s", after.PlaybackQueue)
			}
			// Reservation remains active for a legitimate retry.
			info, qErr := server.QueryReservation(ctx, "mig-bad")
			if qErr != nil || info.Status != string(ReservationActive) {
				t.Fatalf("rejected commit consumed reservation: %#v %v", info, qErr)
			}
		})
	}
}

func TestPlaybackCommitMigrationWireShapeValidation(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	identityBody := `"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}],"currentQueueItemId":"qi-1"`

	t.Run("decode matrix", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			body    string
			wantErr bool
			invalid bool
		}{
			{"all shape keys omitted", `{}`, false, false},
			{"legacy empty queue only", `{"queue":[]}`, false, false},
			{"legacy zero current index only", `{"currentIndex":0}`, false, false},
			{"identity transport", `{` + identityBody + `}`, false, false},
			{"empty identity queue idle", `{"queueItems":[]}`, false, false},
			{"mixed explicit empty legacy with identity", `{"queue":[],"currentIndex":0,` + identityBody + `}`, true, false},
			{"mixed zero index with identity", `{"currentIndex":0,"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}]}`, true, false},
			{"mixed empty legacy queue with identity", `{"queue":[],"queueItems":[]}`, true, false},
			{"null queue items", `{"queueItems":null}`, true, false},
			{"null queue", `{"queue":null}`, true, false},
			{"null current index", `{"currentIndex":null}`, true, false},
			{"null current queue item id", `{"currentQueueItemId":null}`, true, false},
			{"mixed case legacy and identity keys", `{"Queue":[],"QueueItems":[]}`, true, false},
			{"duplicate keys keep last and stay mixed", `{"queue":[],"queueItems":[],"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}]}`, true, false},
			{"wrong type queue", `{"queue":5}`, false, true},
			{"wrong type queue items", `{"queueItems":"nope"}`, false, true},
			{"wrong type current index", `{"currentIndex":"x"}`, false, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var req CommitMigrationRequest
				err := json.Unmarshal([]byte(tc.body), &req)
				if tc.invalid {
					if err == nil {
						t.Fatalf("decode %s: expected type error", tc.body)
					}
					return
				}
				if err != nil {
					t.Fatalf("decode %s: %v", tc.body, err)
				}
				_, shapeErr := validateMigrationQueueShape(req)
				if tc.wantErr && !errors.Is(shapeErr, ErrMigrationProtocolConflict) {
					t.Fatalf("want protocol conflict for %s, got %v", tc.body, shapeErr)
				}
				if !tc.wantErr && shapeErr != nil {
					t.Fatalf("unexpected shape error for %s: %v", tc.body, shapeErr)
				}
			})
		}
	})

	commitWithBody := func(t *testing.T, server *playbackServer, generation uint64, migrationID, operationID, body string) error {
		t.Helper()
		var req CommitMigrationRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("decode commit body: %v", err)
		}
		req.MigrationID = migrationID
		req.OperationID = operationID
		req.SessionID = "src"
		req.ClientID = "tab"
		req.User = "admin"
		req.TargetDeviceName = "new"
		req.ExpectedGeneration = generation
		_, err := server.CommitMigration(ctx, req)
		return err
	}

	t.Run("mixed explicit wire keys rejected before side effects", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		migrationTestTarget(t, server, "new").PlaybackQueue.Add(model.MediaFiles{{ID: "seed", Path: "/seed.mp3"}})
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-wire", "new")
		fpBefore := migrationTestTarget(t, server, "new").SceneFingerprint()

		err := commitWithBody(t, server, attached.BindingGeneration, "mig-wire", "g1:commit-wire",
			`{"queue":[],"currentIndex":0,`+identityBody+`}`)
		if !errors.Is(err, ErrMigrationProtocolConflict) {
			t.Fatalf("want protocol conflict, got %v", err)
		}

		after := migrationTestTarget(t, server, "new")
		after.mu.Lock()
		defer after.mu.Unlock()
		if after.sceneFingerprintLocked() != fpBefore {
			t.Fatal("rejected wire commit mutated target scene")
		}
		if after.PlaybackQueue.Size() != 1 || after.PlaybackQueue.Get()[0].ID != "seed" {
			t.Fatalf("rejected wire commit mutated target queue: %s", after.PlaybackQueue)
		}
		info, qErr := server.QueryReservation(ctx, "mig-wire")
		if qErr != nil || info.Status != string(ReservationActive) {
			t.Fatalf("rejected wire commit consumed reservation: %#v %v", info, qErr)
		}
	})

	t.Run("explicit null shape key rejected before side effects", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		migrationTestTarget(t, server, "new").PlaybackQueue.Add(model.MediaFiles{{ID: "seed", Path: "/seed.mp3"}})
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-null", "new")
		err := commitWithBody(t, server, attached.BindingGeneration, "mig-null", "g1:commit-null", `{"queueItems":null}`)
		if !errors.Is(err, ErrMigrationProtocolConflict) {
			t.Fatalf("want protocol conflict, got %v", err)
		}
		info, qErr := server.QueryReservation(ctx, "mig-null")
		if qErr != nil || info.Status != string(ReservationActive) {
			t.Fatalf("rejected null commit consumed reservation: %#v %v", info, qErr)
		}
	})

	t.Run("legacy only explicit zero wire keys stay valid", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-legacy-zero", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-legacy-zero", OperationID: "g1:commit-legacy-zero", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			Queue: []string{}, CurrentIndex: 0,
		})
		if err != nil {
			t.Fatalf("explicit zero legacy commit: %v", err)
		}
		if committed.CurrentIndex != -1 || committed.Playing {
			t.Fatalf("explicit zero legacy commit changed idle state: %#v", committed)
		}
	})

	t.Run("malformed replay cannot bypass validation after successful identity commit", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-replay", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-replay", OperationID: "g1:commit-replay", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}},
		})
		if err != nil {
			t.Fatalf("identity commit: %v", err)
		}
		err = commitWithBody(t, server, committed.BindingGeneration, "mig-replay", "g1:commit-replay",
			`{"queue":[],"currentIndex":0,"queueItems":[{"queueItemId":"qi-2","mediaFileId":"track"}]}`)
		if !errors.Is(err, ErrMigrationProtocolConflict) {
			t.Fatalf("malformed replay bypassed validation: %v", err)
		}
	})
}

type identityEditFixture struct {
	ctx     context.Context
	server  *playbackServer
	scopeOf func(op string) CommandScope
	device  *playbackDevice
	ids     []string
}

func newIdentityEditFixture(t *testing.T) *identityEditFixture {
	t.Helper()
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	_, server := newMigrationValidationServer(t)
	attached, err := server.AttachSession(ctx, AttachRequest{SessionID: "edit", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &identityEditFixture{
		ctx:    ctx,
		server: server,
		scopeOf: func(op string) CommandScope {
			return CommandScope{User: "admin", SessionID: "edit", ClientID: "tab", BindingGeneration: &attached.BindingGeneration, OperationID: op}
		},
		device: migrationTestTarget(t, server, "old"),
	}
	if _, err := server.ExecuteCommand(ctx, fixture.scopeOf("g1:set"), PlaybackCommand{Action: CommandSet, IDs: []string{"track", "track", "track", "track"}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	snapshot, err := server.SessionSnapshot(ctx, "edit", "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.JukeboxPlaylist.Entry {
		fixture.ids = append(fixture.ids, entry.QueueItemID)
	}
	return fixture
}

func (f *identityEditFixture) run(t *testing.T, op string, command PlaybackCommand) DeviceStatus {
	t.Helper()
	status, err := f.server.ExecuteCommand(f.ctx, f.scopeOf(op), command)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return status
}

func (f *identityEditFixture) currentID(t *testing.T) string {
	t.Helper()
	f.device.mu.Lock()
	defer f.device.mu.Unlock()
	item := f.device.PlaybackQueue.CurrentItem()
	if item == nil {
		return ""
	}
	return item.QueueItemID
}

func (f *identityEditFixture) activeTrack() Track {
	f.device.mu.Lock()
	defer f.device.mu.Unlock()
	return f.device.ActiveTrack
}

func TestPlaybackIdentityRemoveRejectionEnvelopes(t *testing.T) {
	fixture := newIdentityEditFixture(t)
	fixture.device.mu.Lock()
	versionBefore := fixture.device.queueVersion
	fixture.device.mu.Unlock()

	// Protected index-only remove is rejected after authorization.
	if _, err := fixture.server.ExecuteCommand(fixture.ctx, fixture.scopeOf("g1:remove-index"), PlaybackCommand{Action: CommandRemove, Index: 0}); !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("protected index remove accepted: %v", err)
	}
	// ID+index conflict is rejected after authorization, before side effects.
	if _, err := fixture.server.ExecuteCommand(fixture.ctx, fixture.scopeOf("g1:remove-conflict"), PlaybackCommand{Action: CommandRemove, QueueItemID: fixture.ids[0], IndexProvided: true}); !errors.Is(err, ErrQueueIdentityConflict) {
		t.Fatalf("ID+index remove accepted: %v", err)
	}
	fixture.device.mu.Lock()
	got := fixture.device.queueVersion
	fixture.device.mu.Unlock()
	if got != versionBefore {
		t.Fatalf("rejected remove mutated queue version: %d", got)
	}
	// Missing ID returns its distinct error with a fresh snapshot.
	var lookupErr *QueueItemLookupError
	if _, err := fixture.server.ExecuteCommand(fixture.ctx, fixture.scopeOf("g1:remove-missing"), PlaybackCommand{Action: CommandRemove, QueueItemID: "qi-missing"}); !errors.As(err, &lookupErr) {
		t.Fatalf("missing identity remove: %T %v", err, err)
	} else if !lookupErr.Source || lookupErr.Status.QueueVersion != versionBefore {
		t.Fatalf("missing identity remove envelope: %#v", lookupErr)
	}
}

func TestPlaybackIdentityRemovePausedSemantics(t *testing.T) {
	fixture := newIdentityEditFixture(t)

	// Paused remove of the current item selects its successor, never starts playback.
	status := fixture.run(t, "g1:remove-current", PlaybackCommand{Action: CommandRemove, QueueItemID: fixture.ids[0]})
	if status.Playing || status.CurrentIndex != 0 {
		t.Fatalf("paused successor remove: %#v", status)
	}
	if current := fixture.currentID(t); current != fixture.ids[1] {
		t.Fatalf("paused remove did not prefer successor: %s", current)
	}

	// Paused remove of a non-current item leaves current untouched.
	status = fixture.run(t, "g1:remove-noncurrent", PlaybackCommand{Action: CommandRemove, QueueItemID: fixture.ids[3]})
	if status.Playing || status.CurrentIndex != 0 {
		t.Fatalf("paused non-current remove disturbed state: %#v", status)
	}

	// Paused select by identity moves current without startup.
	status = fixture.run(t, "g1:select-last", PlaybackCommand{Action: CommandSkip, QueueItemID: fixture.ids[2]})
	if status.Playing || status.CurrentIndex != 1 {
		t.Fatalf("paused identity select: %#v", status)
	}
	if current := fixture.currentID(t); current != fixture.ids[2] {
		t.Fatalf("identity select resolved wrong instance: %s", current)
	}
}

func TestPlaybackIdentityRemovePlayingSemantics(t *testing.T) {
	fixture := newIdentityEditFixture(t)

	// Playing remove of a non-current item keeps the active output untouched.
	fixture.run(t, "g1:select", PlaybackCommand{Action: CommandSkip, QueueItemID: fixture.ids[2]})
	fixture.run(t, "g1:start", PlaybackCommand{Action: CommandStart})
	playing := fixture.activeTrack()
	if playing == nil {
		t.Fatal("start did not load output")
	}
	// Removing the item before the current one shifts the current index down
	// while the active output instance stays untouched.
	status := fixture.run(t, "g1:remove-noncurrent", PlaybackCommand{Action: CommandRemove, QueueItemID: fixture.ids[1]})
	if !status.Playing || status.CurrentIndex != 1 || fixture.activeTrack() != playing {
		t.Fatalf("playing non-current remove disturbed playback: %#v", status)
	}

	// Appending while protected still mints fresh server-side IDs.
	fixture.run(t, "g1:add", PlaybackCommand{Action: CommandAdd, IDs: []string{"track"}})
	snapshot, err := fixture.server.SessionSnapshot(fixture.ctx, "edit", "admin")
	if err != nil {
		t.Fatal(err)
	}
	appendedID := snapshot.JukeboxPlaylist.Entry[len(snapshot.JukeboxPlaylist.Entry)-1].QueueItemID
	for _, id := range fixture.ids {
		if appendedID == id {
			t.Fatalf("protected add reused an existing instance ID: %s", appendedID)
		}
	}

	// Playing remove of the current item loads its immediate successor (the
	// item after it, not the queue tail) and keeps playing.
	status = fixture.run(t, "g1:remove-current", PlaybackCommand{Action: CommandRemove, QueueItemID: fixture.ids[2]})
	if !status.Playing || status.CurrentIndex != 1 {
		t.Fatalf("playing current remove lost intent: %#v", status)
	}
	if current := fixture.currentID(t); current != fixture.ids[3] {
		t.Fatalf("playing current remove did not select successor: %s", current)
	}
	if track := fixture.activeTrack(); track == playing || track == nil {
		t.Fatal("playing current remove did not switch to the successor output")
	}

	// Playing select of the appended last item, then removing that current
	// last item selects the remaining last item and keeps playing.
	fixture.run(t, "g1:add-again", PlaybackCommand{Action: CommandAdd, IDs: []string{"track"}})
	snapshot, err = fixture.server.SessionSnapshot(fixture.ctx, "edit", "admin")
	if err != nil {
		t.Fatal(err)
	}
	lastID := snapshot.JukeboxPlaylist.Entry[len(snapshot.JukeboxPlaylist.Entry)-1].QueueItemID
	fixture.run(t, "g1:select-appended", PlaybackCommand{Action: CommandSkip, QueueItemID: lastID})
	status = fixture.run(t, "g1:remove-playing-last", PlaybackCommand{Action: CommandRemove, QueueItemID: lastID})
	if !status.Playing || status.CurrentIndex != 2 {
		t.Fatalf("playing last remove lost intent: %#v", status)
	}
	if current := fixture.currentID(t); current != appendedID {
		t.Fatalf("last remove did not select remaining last: %s", current)
	}
}

func TestPlaybackQueueProtocolCapabilityContract(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	_, server := newMigrationValidationServer(t)
	attached, err := server.AttachSession(ctx, AttachRequest{SessionID: "cap", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	protocol := attached.QueueProtocol
	if protocol == nil || protocol.Version != 1 ||
		!protocol.RelativeMove || !protocol.IdentitySelect || !protocol.IdentityRemove ||
		!protocol.IdentityMigration || !protocol.AnchorVersionEnforcement || !protocol.OperationResults {
		t.Fatalf("attach did not advertise enforced queue protocol: %#v", protocol)
	}
	status, err := server.SessionStatus(ctx, "cap")
	if err != nil {
		t.Fatal(err)
	}
	if status.QueueProtocol == nil || status.QueueProtocol.Version != 1 {
		t.Fatalf("session status dropped queue protocol: %#v", status.QueueProtocol)
	}
	event := NewJukeboxStateUpdatedEvent(status)
	if event.QueueProtocol == nil || event.QueueProtocol.Version != 1 || !event.QueueProtocol.IdentityRemove {
		t.Fatalf("SSE event dropped queue protocol: %#v", event.QueueProtocol)
	}
}
