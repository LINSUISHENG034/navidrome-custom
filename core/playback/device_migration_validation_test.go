package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	serverevents "github.com/navidrome/navidrome/server/events"
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
	sourceBefore, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := migrationTestTarget(t, server, "new")
	targetBefore, err := target.Status(ctx)
	if err != nil {
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
	if status.QueueVersion != targetBefore.QueueVersion+1 || status.StateSequence != targetBefore.StateSequence+1 {
		t.Fatalf("paused migration lost target-local ordering: before=%#v after=%#v", targetBefore, status)
	}
	sourceAfter, err := device.Status(ctx)
	if err != nil || sourceAfter.QueueVersion != sourceBefore.QueueVersion+1 || sourceAfter.StateSequence != sourceBefore.StateSequence+1 {
		t.Fatalf("paused migration lost source ordering: before=%#v after=%#v error=%v", sourceBefore, sourceAfter, err)
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
		{Action: CommandNext},
		{Action: CommandPrev},
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

func TestPlaybackModeCommandAuthorityAndReplay(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	ctx = request.WithUser(ctx, model.User{UserName: "admin"})
	attached, err := server.AttachSession(ctx, AttachRequest{SessionID: "mode", User: "admin", ClientID: "owner", DeviceName: "new"})
	if err != nil {
		t.Fatal(err)
	}
	scope := migrationCommandScope(CommandScope{User: "admin", SessionID: "mode", ClientID: "owner", BindingGeneration: &attached.BindingGeneration})
	device := server.playbackDevices[1]
	before, _ := device.Status(ctx)
	beforeScene := device.SceneFingerprint()
	wrongOwner, wrongUser, missingGeneration, staleGeneration, missingOperation := scope, scope, scope, scope, scope
	wrongOwner.ClientID = "observer"
	wrongUser.User = "other"
	missingGeneration.BindingGeneration = nil
	otherGeneration := attached.BindingGeneration + 1
	staleGeneration.BindingGeneration = &otherGeneration
	missingOperation.OperationID = ""
	command := PlaybackCommand{Action: CommandSetMode, Mode: PlaybackModeShuffle}
	for _, tc := range []struct {
		name  string
		scope CommandScope
		err   error
	}{
		{"bare", CommandScope{User: "admin"}, ErrSessionOwnership},
		{"observer", wrongOwner, ErrSessionOwnership},
		{"foreign user", wrongUser, ErrSessionOwnership},
		{"missing generation", missingGeneration, ErrGenerationRequired},
		{"different generation", staleGeneration, ErrCommandSuperseded},
		{"missing operation", missingOperation, ErrOperationRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := server.ExecuteCommand(ctx, tc.scope, command); !errors.Is(err, tc.err) {
				t.Fatalf("mode authority rejection: got=%v want=%v", err, tc.err)
			}
			status, _ := device.Status(ctx)
			if status != before || device.mode != PlaybackModeOrder || device.SceneFingerprint() != beforeScene {
				t.Fatal("rejected mode command changed device state")
			}
		})
	}
	applied, err := server.ExecuteCommand(ctx, scope, command)
	if err != nil {
		t.Fatal(err)
	}
	if applied.StateSequence != before.StateSequence+1 || applied.QueueVersion != before.QueueVersion || device.mode != PlaybackModeShuffle {
		t.Fatalf("owner mode write did not apply once: %#v", applied)
	}
	newScope := migrationCommandScope(scope)
	current, err := server.ExecuteCommand(ctx, newScope, PlaybackCommand{Action: CommandSetMode, Mode: PlaybackModeRepeatTrack})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := server.ExecuteCommand(ctx, scope, command)
	observed, _ := device.Status(ctx)
	if err != nil || replayed != applied || observed != current || current.StateSequence != applied.StateSequence+1 || device.mode != PlaybackModeRepeatTrack {
		t.Fatalf("mode retry re-executed or lost stored result: replayed=%#v observed=%#v err=%v", replayed, observed, err)
	}
	record, err := server.QueryOperation(ctx, scope.SessionID, scope.OperationID)
	if err != nil || record.Status != OperationStatusApplied || record.Action != CommandSetMode || record.DeviceStatus == nil || *record.DeviceStatus != applied {
		t.Fatalf("mode operation result mismatch: %#v err=%v", record, err)
	}
	defaultStatus, _ := server.playbackDevices[0].Status(ctx)
	if defaultStatus.StateSequence != 0 || server.playbackDevices[0].mode != PlaybackModeOrder {
		t.Fatal("bound mode write changed default device")
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
	targetBefore, err := server.playbackDevices[1].Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandGain, Gain: 0.375}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.SessionSnapshot(ctx, "owner", "admin")
	if err != nil || snapshot.SessionStatus.DeviceName != "new" || snapshot.SessionStatus.DesiredGain != 0.375 || snapshot.SessionStatus.Gain != targetBefore.Gain || len(snapshot.JukeboxPlaylist.Entry) != 1 {
		t.Fatalf("scoped migration lost bound authority: %#v err=%v", snapshot, err)
	}
	oldStatus, _ := server.playbackDevices[0].Status(ctx)
	if oldStatus.Gain != DefaultGain || oldStatus.DesiredGain != DefaultGain || !server.playbackDevices[0].PlaybackQueue.IsEmpty() {
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
	if err != nil || newStatusAfter.Gain == 0.99 || newStatusAfter.DesiredGain == 0.99 || newStatusAfter.DeviceName != "new" {
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

func assertProtectedAnchorMoveUnchanged(t *testing.T, f *identityEditFixture, ctx context.Context, before SessionSnapshot, fingerprint string) {
	t.Helper()
	after, err := f.server.SessionSnapshot(ctx, "edit", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) || f.device.SceneFingerprint() != fingerprint || f.activeTrack() != nil {
		t.Fatalf("rejected move changed queue/version/current/scene/output: before=%#v after=%#v", before, after)
	}
}

func TestPlaybackProtectedAnchorMoveVersionRejections(t *testing.T) {
	t.Run("explicit zero is stale", func(t *testing.T) {
		f := newIdentityEditFixture(t)
		ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
		defer cancel()
		before, err := f.server.SessionSnapshot(ctx, "edit", "admin")
		if err != nil {
			t.Fatal(err)
		}
		version := before.SessionStatus.QueueVersion
		if before.SessionStatus.QueueVersion <= 0 || f.activeTrack() != nil {
			t.Fatalf("fixture must be idle with positive version: %#v", before)
		}
		fingerprint := f.device.SceneFingerprint()
		zero := 0
		move := PlaybackCommand{
			Action: CommandMove, QueueItemID: f.ids[3], AnchorQueueItemID: f.ids[0],
			Placement: MovePlacementBefore, ExpectedQueueVersion: &zero,
		}
		_, err = f.server.ExecuteCommand(ctx, f.scopeOf("g1:zero-version"), move)
		var conflict *QueueVersionConflictError
		if !errors.As(err, &conflict) || conflict.Expected != 0 || conflict.Actual != version {
			t.Fatalf("explicit zero must reach stale-version validation: %v", err)
		}
		assertProtectedAnchorMoveUnchanged(t, f, ctx, before, fingerprint)
	})

	t.Run("ownership precedes omitted version", func(t *testing.T) {
		f := newIdentityEditFixture(t)
		ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
		defer cancel()
		before, err := f.server.SessionSnapshot(ctx, "edit", "admin")
		if err != nil {
			t.Fatal(err)
		}
		if before.SessionStatus.QueueVersion <= 0 || f.activeTrack() != nil {
			t.Fatalf("fixture must be idle with positive version: %#v", before)
		}
		fingerprint := f.device.SceneFingerprint()
		scope := f.scopeOf("g1:wrong-owner")
		scope.ClientID = "observer"
		move := PlaybackCommand{Action: CommandMove, QueueItemID: f.ids[3], AnchorQueueItemID: f.ids[0], Placement: MovePlacementBefore}
		if _, err := f.server.ExecuteCommand(ctx, scope, move); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("ownership must precede omitted version: %v", err)
		}
		assertProtectedAnchorMoveUnchanged(t, f, ctx, before, fingerprint)
	})
}

func TestPlaybackProtectedAnchorMoveVersionAdmissionAndReplay(t *testing.T) {
	for _, name := range []string{"omitted", "current", "same ID corrected retry"} {
		t.Run(name, func(t *testing.T) {
			f := newIdentityEditFixture(t)
			ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
			defer cancel()
			before, err := f.server.SessionSnapshot(ctx, "edit", "admin")
			if err != nil {
				t.Fatal(err)
			}
			version := before.SessionStatus.QueueVersion
			if version <= 0 || f.activeTrack() != nil {
				t.Fatalf("fixture must be idle with positive version: %#v", before)
			}
			fingerprint := f.device.SceneFingerprint()
			scope := f.scopeOf("g1:version-move")
			move := PlaybackCommand{Action: CommandMove, QueueItemID: f.ids[3], AnchorQueueItemID: f.ids[0], Placement: MovePlacementBefore}
			if name == "omitted" || name == "same ID corrected retry" {
				for attempt := 0; attempt < 2; attempt++ {
					_, err := f.server.ExecuteCommand(ctx, scope, move)
					// ExecuteCommand exposes the error, not its transient NOT_EXECUTED record.
					// Admission failures must be rejected anew, not persisted as replay results.
					if err == nil || err.Error() != "protected anchor moves require expectedQueueVersion" || !isOperationAdmissionError(err) {
						t.Fatalf("attempt %d: expected version-required admission rejection, got %v", attempt, err)
					}
					record, err := f.server.QueryOperation(ctx, "edit", scope.OperationID)
					if err != nil || record.Status != OperationStatusUnknown {
						t.Fatalf("admission rejection retained: %#v, %v", record, err)
					}
					assertProtectedAnchorMoveUnchanged(t, f, ctx, before, fingerprint)
				}
				if name == "omitted" {
					return
				}
			}
			move.ExpectedQueueVersion = &version
			status, err := f.server.ExecuteCommand(ctx, scope, move)
			if err != nil || status.QueueVersion != version+1 {
				t.Fatalf("current version must apply once: %#v, %v", status, err)
			}
			record, err := f.server.QueryOperation(ctx, "edit", scope.OperationID)
			if err != nil || record.Status != OperationStatusApplied {
				t.Fatalf("corrected/current result: %#v, %v", record, err)
			}
			replay, err := f.server.ExecuteCommand(ctx, scope, move)
			if err != nil || !reflect.DeepEqual(replay, status) {
				t.Fatalf("applied replay changed result: %#v, %v", replay, err)
			}
			after, err := f.server.SessionSnapshot(ctx, "edit", "admin")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, item := range after.JukeboxPlaylist.Entry {
				got = append(got, item.QueueItemID)
			}
			want := []string{f.ids[3], f.ids[0], f.ids[1], f.ids[2]}
			if !reflect.DeepEqual(got, want) || after.SessionStatus.QueueVersion != version+1 || after.SessionStatus.QueueItemID != before.SessionStatus.QueueItemID || after.SessionStatus.TrackID != before.SessionStatus.TrackID || after.SessionStatus.Position != before.SessionStatus.Position || after.SessionStatus.Playing || f.activeTrack() != nil {
				t.Fatalf("applied move/replay violated idle queue continuity: %#v", after)
			}
		})
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
		ExpectedQueueVersion: &snapshot.SessionStatus.QueueVersion,
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

type modeMigrationFixture struct {
	ctx     context.Context
	server  *playbackServer
	source  *playbackDevice
	target  *playbackDevice
	request CommitMigrationRequest
}

func newModeMigrationFixture(t *testing.T, shape string, sourceName string) *modeMigrationFixture {
	t.Helper()
	ctx, server := newMigrationValidationServer(t)
	source := migrationTestTarget(t, server, sourceName)
	target := migrationTestTarget(t, server, "new")
	source.mode, source.stateSequence = PlaybackModeShuffle, 5
	target.mode, target.stateSequence, target.queueVersion = PlaybackModeRepeatTrack, 17, 3
	attached := attachMigrationSource(t, server, "mode-migration", "owner", sourceName)
	reserveForCommit(t, server, attached.SessionID, "owner", attached.BindingGeneration, "mode-import", "new")
	req := CommitMigrationRequest{
		MigrationID: "mode-import", OperationID: "g1:mode-import", SessionID: attached.SessionID, ClientID: "owner",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
	}
	switch shape {
	case "legacy":
		req.Queue, req.CurrentIndex = []string{"track", "track"}, 1
	case "identity":
		current := "mode-item-2"
		req.QueueItems = &[]QueueItemRef{{QueueItemID: "mode-item-1", MediaFileID: "track"}, {QueueItemID: current, MediaFileID: "track"}}
		req.CurrentQueueItemID = &current
	case "empty":
		req.QueueItems = &[]QueueItemRef{}
	default:
		t.Fatalf("unknown mode migration shape %q", shape)
	}
	return &modeMigrationFixture{ctx: ctx, server: server, source: source, target: target, request: req}
}

func TestPlaybackLegacySwitchModeContext(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sourceQueue  bool
		targetQueue  bool
		targetOutput bool
		equalMode    bool
	}{
		{"queue import", true, false, false, false},
		{"queue replacement", true, true, false, false},
		{"both idle mode change", false, false, false, false},
		{"both idle same mode", false, false, false, true},
		{"retain target queue", false, true, false, false},
		{"retain target output", false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, server := newMigrationValidationServer(t)
			source, target := server.playbackDevices[0], server.playbackDevices[1]
			if tc.sourceQueue {
				if _, err := source.Set(ctx, []string{"track", "track"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.targetQueue {
				if _, err := target.Set(ctx, []string{"track"}); err != nil {
					t.Fatal(err)
				}
			}
			source.mode, target.mode = PlaybackModeShuffle, PlaybackModeRepeatTrack
			if tc.equalMode {
				target.mode = source.mode
			}
			if tc.targetOutput {
				target.ActiveTrack, target.activePlaybackToken = &mockTrack{playing: true, position: 9}, 37
			}
			attached := attachMigrationSource(t, server, "mode-switch", "owner", "old")
			before, _ := target.Status(ctx)
			revision := target.sceneRevision
			want, wantIDs := before, target.PlaybackQueue.ItemIDs()
			want.Mode = target.mode
			if tc.sourceQueue {
				want.CurrentIndex, want.Position, want.Playing = source.PlaybackQueue.Index, 0, false
				want.QueueVersion++
				want.StateSequence++
				want.Mode, wantIDs = source.mode, source.PlaybackQueue.ItemIDs()
			} else if !tc.targetQueue && !tc.targetOutput {
				want.Mode = source.mode
				if want.Mode != target.mode {
					want.StateSequence++
				}
			}
			output, token := target.ActiveTrack, target.activePlaybackToken
			scope := CommandScope{User: "admin", SessionID: attached.SessionID, ClientID: "owner", BindingGeneration: &attached.BindingGeneration, OperationID: "g1:mode-switch"}
			if err := server.SwitchDeviceForCommand(ctx, scope, "new"); err != nil {
				t.Fatal(err)
			}
			after, _ := target.Status(ctx)
			if after != want || target.mode != want.Mode || target.sceneRevision != revision+1 || !reflect.DeepEqual(target.PlaybackQueue.ItemIDs(), wantIDs) {
				t.Errorf("legacy switch lost context/mode ordering: before=%#v after=%#v want=%#v actualMode=%q", before, after, want, target.mode)
			}
			if !tc.sourceQueue && (target.ActiveTrack != output || target.activePlaybackToken != token) {
				t.Error("empty-source legacy switch replaced retained output")
			}
		})
	}
}

func TestPlaybackMigrationModeImportAndCarry(t *testing.T) {
	for _, shape := range []string{"legacy", "identity", "empty"} {
		for _, source := range []string{"old", "new"} {
			for _, mode := range []PlaybackMode{"", PlaybackModeOrder, PlaybackModeRepeatList, PlaybackModeRepeatTrack, PlaybackModeShuffle} {
				name := string(mode)
				if mode == "" {
					name = "omitted"
				}
				t.Run(shape+"/"+source+"/"+name, func(t *testing.T) {
					fixture := newModeMigrationFixture(t, shape, source)
					before, _ := fixture.target.Status(fixture.ctx)
					wantMode := fixture.source.mode
					if mode != "" {
						fixture.request.Mode, wantMode = &mode, mode
					}
					committed, err := fixture.server.CommitMigration(fixture.ctx, fixture.request)
					if err != nil {
						t.Fatal(err)
					}
					if fixture.target.mode != wantMode || committed.PlayMode != string(wantMode) || committed.StateSequence != before.StateSequence+1 || committed.QueueVersion != before.QueueVersion+1 {
						t.Errorf("mode import/carry or target-local ordering failed: before=%#v committed=%#v actualMode=%q wantMode=%q", before, committed, fixture.target.mode, wantMode)
					}
					record, err := fixture.server.QueryOperation(fixture.ctx, fixture.request.SessionID, fixture.request.OperationID)
					if err != nil || record.SessionStatus == nil || record.SessionStatus.PlayMode != string(wantMode) {
						t.Errorf("original migration query lost committed mode: record=%#v err=%v", record, err)
					}
				})
			}
		}
	}
}

func TestPlaybackMigrationRejectsInvalidModeBeforeMutation(t *testing.T) {
	for _, value := range []any{"", "repeat", "ORDER", " order", nil} {
		t.Run(fmt.Sprintf("mode=%v", value), func(t *testing.T) {
			fixture := newModeMigrationFixture(t, "identity", "old")
			encoded, err := json.Marshal(fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			payload["mode"] = value
			encoded, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &fixture.request); err != nil {
				t.Fatal(err)
			}
			before, _ := fixture.target.Status(fixture.ctx)
			fingerprint := fixture.target.SceneFingerprint()
			if _, err := fixture.server.CommitMigration(fixture.ctx, fixture.request); !errors.Is(err, ErrInvalidPlayMode) {
				t.Errorf("invalid migration mode accepted: %v", err)
			}
			after, _ := fixture.target.Status(fixture.ctx)
			reservation, err := fixture.server.QueryReservation(fixture.ctx, fixture.request.MigrationID)
			if err != nil || reservation.Status != string(ReservationActive) || after != before || fixture.target.SceneFingerprint() != fingerprint {
				t.Errorf("invalid mode consumed reservation or mutated target: before=%#v after=%#v reservation=%#v err=%v", before, after, reservation, err)
			}
			record, err := fixture.server.QueryOperation(fixture.ctx, fixture.request.SessionID, fixture.request.OperationID)
			if err != nil || record.Status != OperationStatusUnknown {
				t.Errorf("invalid mode entered operation history: %#v err=%v", record, err)
			}
		})
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
		target := migrationTestTarget(t, server, "new")
		target.mu.Lock()
		target.stateSequence = 17
		target.mu.Unlock()
		before, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-id", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-id", OperationID: "g1:commit-id", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: identityQueue(), CurrentQueueItemID: currentTwo(),
		})
		if err != nil {
			t.Fatalf("identity commit: %v", err)
		}
		if committed.QueueVersion != before.QueueVersion+1 || committed.StateSequence != before.StateSequence+1 {
			t.Fatalf("identity commit lost target-local ordering: before=%#v after=%#v", before, committed)
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
		before, err := migrationTestTarget(t, server, "new").Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-empty", OperationID: "g1:commit-empty", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{},
		})
		if err != nil {
			t.Fatalf("empty identity commit: %v", err)
		}
		if committed.QueueVersion != before.QueueVersion+1 || committed.StateSequence != before.StateSequence+1 {
			t.Fatalf("empty identity import lost ordering: before=%#v after=%#v", before, committed)
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

	assertMigrationIdentityRejections(t, ctx, identityQueue, currentTwo)
}

func assertMigrationIdentityRejections(t *testing.T, ctx context.Context, identityQueue func() *[]QueueItemRef, currentTwo func() *string) {
	t.Helper()
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

func TestPlaybackMigrationPlaybackInstanceCarry(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})

	// startSourceInstance drives the default ("old") source device to a known
	// state and returns its exact playback instance identity.
	startSourceInstance := func(t *testing.T, paused bool) (*playbackServer, *playbackDevice, SessionStatus, string) {
		t.Helper()
		_, server := newMigrationValidationServer(t)
		source, err := server.GetDeviceForUser("admin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Set(ctx, []string{"track"}); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if paused {
			if _, err := source.Stop(ctx); err != nil {
				t.Fatal(err)
			}
		}
		status, err := source.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.PlaybackInstanceID == "" {
			t.Fatalf("expected non-empty source playback instance: %#v", status)
		}
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		return server, migrationTestTarget(t, server, "new"), attached, status.PlaybackInstanceID
	}
	currentOne := func() *string { s := "qi-1"; return &s }

	t.Run("identity commit carries exact source instance into commit status snapshot and target start", func(t *testing.T) {
		server, target, attached, sourceInstance := startSourceInstance(t, false)
		before, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry", OperationID: "g1:commit-carry", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems:         &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-2", MediaFileID: "track"}},
			CurrentQueueItemID: currentOne(),
		})
		if err != nil {
			t.Fatalf("identity carry commit: %v", err)
		}
		if committed.QueueVersion != before.QueueVersion+1 || committed.StateSequence != before.StateSequence+1 {
			t.Fatalf("identity carry changed target-local ordering: before=%#v committed=%#v", before, committed)
		}
		if committed.PlaybackInstanceID != sourceInstance {
			t.Fatalf("identity commit did not carry exact source instance: want %q got %q", sourceInstance, committed.PlaybackInstanceID)
		}
		snapshot, err := server.SessionSnapshot(ctx, "src", "admin")
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.SessionStatus.PlaybackInstanceID != sourceInstance {
			t.Fatalf("snapshot lost carried instance: want %q got %q", sourceInstance, snapshot.SessionStatus.PlaybackInstanceID)
		}
		started, err := target.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if started.PlaybackInstanceID != sourceInstance {
			t.Fatalf("target start did not keep carried instance: want %q got %q", sourceInstance, started.PlaybackInstanceID)
		}
	})

	t.Run("legacy commit carries when imported current media matches", func(t *testing.T) {
		server, target, attached, sourceInstance := startSourceInstance(t, false)
		before, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-legacy", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry-legacy", OperationID: "g1:commit-carry-legacy", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			Queue: []string{"track", "track"}, CurrentIndex: 0,
		})
		if err != nil {
			t.Fatalf("legacy carry commit: %v", err)
		}
		if committed.QueueVersion != before.QueueVersion+1 || committed.StateSequence != before.StateSequence+1 {
			t.Fatalf("legacy carry changed target-local ordering: before=%#v committed=%#v", before, committed)
		}
		if committed.PlaybackInstanceID != sourceInstance {
			t.Fatalf("legacy commit did not carry exact source instance: want %q got %q", sourceInstance, committed.PlaybackInstanceID)
		}
	})

	t.Run("paused source stays paused with exact identity", func(t *testing.T) {
		server, target, attached, sourceInstance := startSourceInstance(t, true)
		before, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-paused", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry-paused", OperationID: "g1:commit-carry-paused", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}, CurrentQueueItemID: currentOne(),
		})
		if err != nil {
			t.Fatalf("paused carry commit: %v", err)
		}
		if committed.QueueVersion != before.QueueVersion+1 || committed.StateSequence != before.StateSequence+1 {
			t.Fatalf("paused carry changed target-local ordering: before=%#v committed=%#v", before, committed)
		}
		if committed.Playing {
			t.Fatalf("paused source resumed on target: %#v", committed)
		}
		if committed.PlaybackInstanceID != sourceInstance {
			t.Fatalf("paused commit did not carry exact source instance: want %q got %q", sourceInstance, committed.PlaybackInstanceID)
		}
		targetStatus, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if targetStatus.PlaybackInstanceID != sourceInstance || targetStatus.Playing {
			t.Fatalf("target did not retain paused carried identity: %#v", targetStatus)
		}
	})

	t.Run("mismatched current media does not carry and first start mints fresh", func(t *testing.T) {
		server, target, attached, sourceInstance := startSourceInstance(t, false)
		repo := server.datastore.(*tests.MockDataStore).MockedMediaFile.(*tests.MockMediaFileRepo)
		repo.SetData(model.MediaFiles{{ID: "track", Path: "/validation.wav"}, {ID: "other", Path: "/other.wav"}})
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-mismatch", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry-mismatch", OperationID: "g1:commit-carry-mismatch", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "other"}}, CurrentQueueItemID: currentOne(),
		})
		if err != nil {
			t.Fatalf("mismatched media commit: %v", err)
		}
		if committed.PlaybackInstanceID != "" {
			t.Fatalf("mismatched media must not carry source instance: got %q", committed.PlaybackInstanceID)
		}
		started, err := target.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if started.PlaybackInstanceID == "" || started.PlaybackInstanceID == sourceInstance {
			t.Fatalf("first start must mint a fresh instance distinct from source: source=%q got=%q", sourceInstance, started.PlaybackInstanceID)
		}
	})

	t.Run("source without instance does not carry and first start mints fresh", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		target := migrationTestTarget(t, server, "new")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-idle", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry-idle", OperationID: "g1:commit-carry-idle", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}, CurrentQueueItemID: currentOne(),
		})
		if err != nil {
			t.Fatalf("idle source commit: %v", err)
		}
		if committed.PlaybackInstanceID != "" {
			t.Fatalf("idle source must not issue or carry an instance: got %q", committed.PlaybackInstanceID)
		}
		started, err := target.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if started.PlaybackInstanceID == "" {
			t.Fatalf("first start must mint a fresh instance: %#v", started)
		}
	})

	t.Run("empty import does not mint identity", func(t *testing.T) {
		server, target, attached, _ := startSourceInstance(t, false)
		before, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-empty", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry-empty", OperationID: "g1:commit-carry-empty", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{},
		})
		if err != nil {
			t.Fatalf("empty import commit: %v", err)
		}
		if committed.PlaybackInstanceID != "" || committed.Playing {
			t.Fatalf("empty import must not mint identity or start playback: %#v", committed)
		}
		if after, _ := target.Status(ctx); after.PlaybackInstanceID != "" {
			t.Fatalf("empty import minted target identity: before=%#v after=%#v", before, after)
		}
	})

	t.Run("rejected commit does not mutate target identity", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		target := migrationTestTarget(t, server, "new")
		if _, err := target.Set(ctx, []string{"track"}); err != nil {
			t.Fatal(err)
		}
		if _, err := target.Start(ctx); err != nil {
			t.Fatal(err)
		}
		targetStatus, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		targetInstance := targetStatus.PlaybackInstanceID
		if targetInstance == "" {
			t.Fatalf("expected non-empty target playback instance: %#v", targetStatus)
		}
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-reject", "new")
		fingerprint := target.SceneFingerprint()
		_, err = server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry-reject", OperationID: "g1:commit-carry-reject", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			Queue: []string{"track"}, QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}},
		})
		if !errors.Is(err, ErrMigrationProtocolConflict) {
			t.Fatalf("expected ErrMigrationProtocolConflict, got %v", err)
		}
		after, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after.PlaybackInstanceID != targetInstance {
			t.Fatalf("rejected commit mutated target identity: before=%q after=%q", targetInstance, after.PlaybackInstanceID)
		}
		if target.SceneFingerprint() != fingerprint {
			t.Fatal("rejected commit mutated target scene")
		}
		info, qErr := server.QueryReservation(ctx, "mig-carry-reject")
		if qErr != nil || info.Status != string(ReservationActive) {
			t.Fatalf("rejected commit consumed reservation: %#v %v", info, qErr)
		}
	})

	t.Run("index-shifting queue edits before start keep carried instance on the exact occurrence", func(t *testing.T) {
		currentTwo := func() *string { s := "qi-2"; return &s }
		shifts := []struct {
			name  string
			apply func(t *testing.T, target *playbackDevice)
		}{
			{"insert before carried occurrence", func(t *testing.T, target *playbackDevice) {
				if _, err := target.Insert(ctx, 0, []string{"track"}); err != nil {
					t.Fatal(err)
				}
			}},
			{"remove before carried occurrence", func(t *testing.T, target *playbackDevice) {
				if _, err := target.Remove(ctx, 0); err != nil {
					t.Fatal(err)
				}
			}},
			{"move carried occurrence", func(t *testing.T, target *playbackDevice) {
				if _, err := target.Move(ctx, 1, 2); err != nil {
					t.Fatal(err)
				}
			}},
		}
		for _, shift := range shifts {
			t.Run(shift.name, func(t *testing.T) {
				server, target, attached, sourceInstance := startSourceInstance(t, false)
				reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-shift", "new")
				committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
					MigrationID: "mig-carry-shift", OperationID: "g1:commit-carry-shift", SessionID: "src", ClientID: "tab",
					User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
					QueueItems:         &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-2", MediaFileID: "track"}, {QueueItemID: "qi-3", MediaFileID: "track"}},
					CurrentQueueItemID: currentTwo(),
				})
				if err != nil {
					t.Fatalf("carry shift commit: %v", err)
				}
				if committed.PlaybackInstanceID != sourceInstance || committed.CurrentIndex != 1 {
					t.Fatalf("carry shift commit did not stage exact source instance: want %q at index 1 got %#v", sourceInstance, committed)
				}
				shift.apply(t, target)
				snapshot, err := server.SessionSnapshot(ctx, "src", "admin")
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.SessionStatus.QueueItemID != "qi-2" {
					t.Fatalf("%s no longer selects the carried occurrence: %#v", shift.name, snapshot.SessionStatus)
				}
				started, err := target.Start(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if started.PlaybackInstanceID != sourceInstance {
					t.Fatalf("%s before start dropped carried instance: want %q got %q", shift.name, sourceInstance, started.PlaybackInstanceID)
				}
			})
		}
	})

	t.Run("selecting another track before start supersedes carried identity", func(t *testing.T) {
		server, target, attached, sourceInstance := startSourceInstance(t, false)
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-carry-supersede", "new")
		committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "mig-carry-supersede", OperationID: "g1:commit-carry-supersede", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems:         &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-2", MediaFileID: "track"}},
			CurrentQueueItemID: currentOne(),
		})
		if err != nil {
			t.Fatalf("supersede carry commit: %v", err)
		}
		if committed.PlaybackInstanceID != sourceInstance {
			t.Fatalf("identity commit did not carry exact source instance: want %q got %q", sourceInstance, committed.PlaybackInstanceID)
		}
		if _, err := target.Skip(ctx, 1, 0); err != nil {
			t.Fatal(err)
		}
		started, err := target.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if started.PlaybackInstanceID == "" || started.PlaybackInstanceID == sourceInstance {
			t.Fatalf("start after superseding selection must mint a new instance: source=%q got=%q", sourceInstance, started.PlaybackInstanceID)
		}
	})
}

// Fixed canonical UUID literals for explicit playbackInstanceId wire values.
// Explicit imports must bind exactly what a client supplies, so tests never
// mint or fake these values.
const (
	explicitInstanceID    = "7f3a2b1c-9d4e-4a5f-b6c7-d8e9f0a1b2c3"
	explicitInstanceIDAlt = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	// explicitInstanceIDV1 is a canonical version-1 UUID: the import boundary
	// policy accepts any canonical UUID text without a version restriction.
	explicitInstanceIDV1 = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

// decodeCommitMigrationWire re-encodes req as raw JSON, applies mutate to the
// wire payload, and decodes it back. Explicit playbackInstanceId tests use it
// to exercise the wire contract without referencing Go fields that do not
// exist yet; the baseline decoder ignores the unknown key, so only behavior
// assertions turn RED, never compilation.
func decodeCommitMigrationWire(t *testing.T, req CommitMigrationRequest, mutate func(map[string]any)) (CommitMigrationRequest, error) {
	t.Helper()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(payload)
	}
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CommitMigrationRequest
	err = json.Unmarshal(encoded, &decoded)
	return decoded, err
}

// explicitImportCurrentRef returns a fresh pointer to the given queue item ID
// so wire-decoded requests never alias fixture state.
func explicitImportCurrentRef(id string) *string { s := id; return &s }

// injectExplicitInstanceID adds the fixed explicit playbackInstanceId wire
// value to a raw commit payload.
func injectExplicitInstanceID(payload map[string]any) {
	payload["playbackInstanceId"] = explicitInstanceID
}

// startExplicitImportSource drives the default ("old") device to a playing
// scene and returns the server, target device, the attached session, and the
// exact server-minted source instance.
func startExplicitImportSource(t *testing.T, ctx context.Context) (*playbackServer, *playbackDevice, SessionStatus, string) {
	t.Helper()
	_, server := newMigrationValidationServer(t)
	source, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Set(ctx, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Start(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := source.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PlaybackInstanceID == "" {
		t.Fatalf("expected non-empty source instance: %#v", status)
	}
	attached := attachMigrationSource(t, server, "src", "tab", "old")
	return server, migrationTestTarget(t, server, "new"), attached, status.PlaybackInstanceID
}

// assertImportedInstanceEverywhere pins the imported instance to the commit
// response path: session snapshot, immediate target status, and the first
// target Start, in that order.
func assertImportedInstanceEverywhere(t *testing.T, ctx context.Context, server *playbackServer, target *playbackDevice, sessionID, want string) {
	t.Helper()
	snapshot, err := server.SessionSnapshot(ctx, sessionID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SessionStatus.PlaybackInstanceID != want {
		t.Fatalf("snapshot lost bound instance: want %q got %q", want, snapshot.SessionStatus.PlaybackInstanceID)
	}
	status, err := target.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PlaybackInstanceID != want {
		t.Fatalf("target status lost bound instance: want %q got %q", want, status.PlaybackInstanceID)
	}
	started, err := target.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if started.PlaybackInstanceID != want {
		t.Fatalf("first start did not keep bound instance: want %q got %q", want, started.PlaybackInstanceID)
	}
}

func TestPlaybackMigrationExplicitInstanceImport(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	t.Run("explicit id takes precedence over server-source carry on identity shape", func(t *testing.T) {
		explicitImportIdentityPrecedence(t, ctx)
	})
	t.Run("explicit id binds on legacy normalized index shape", func(t *testing.T) {
		explicitImportLegacyShape(t, ctx)
	})
	t.Run("explicit id works same-device without source carry", func(t *testing.T) {
		explicitImportSameDevice(t, ctx)
	})
	t.Run("omitted field keeps inferred server-source carry unchanged", func(t *testing.T) {
		explicitImportOmittedCarry(t, ctx)
	})
	t.Run("explicit id binds to the imported occurrence and survives index shifts", func(t *testing.T) {
		explicitImportSurvivesIndexShift(t, ctx)
	})
	t.Run("selecting another occurrence before start supersedes explicit id", func(t *testing.T) {
		explicitImportSupersededByReselect(t, ctx)
	})
	t.Run("failed import load with explicit id leaves target stopped", func(t *testing.T) {
		explicitImportFailedLoadStaysStopped(t, ctx)
	})
	t.Run("inferred carry with duplicate media keeps provenance compatibility", func(t *testing.T) {
		explicitImportDuplicateMediaCarryCompat(t, ctx)
	})
}

func explicitImportIdentityPrecedence(t *testing.T, ctx context.Context) {
	t.Helper()
	server, target, attached, sourceInstance := startExplicitImportSource(t, ctx)
	if sourceInstance == explicitInstanceID {
		t.Fatalf("fixture collision: source minted the explicit literal %q", sourceInstance)
	}
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-id", "new")
	req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
		MigrationID: "mig-exp-id", OperationID: "g1:commit-exp-id", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		QueueItems:         &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-2", MediaFileID: "track"}},
		CurrentQueueItemID: explicitImportCurrentRef("qi-1"),
	}, injectExplicitInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := server.CommitMigration(ctx, req)
	if err != nil {
		t.Fatalf("explicit identity commit: %v", err)
	}
	if committed.PlaybackInstanceID != explicitInstanceID {
		t.Fatalf("explicit instance not bound at commit: want %q got %q (carried source was %q)", explicitInstanceID, committed.PlaybackInstanceID, sourceInstance)
	}
	assertImportedInstanceEverywhere(t, ctx, server, target, "src", explicitInstanceID)
}

func explicitImportLegacyShape(t *testing.T, ctx context.Context) {
	t.Helper()
	server, target, attached, sourceInstance := startExplicitImportSource(t, ctx)
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-legacy", "new")
	req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
		MigrationID: "mig-exp-legacy", OperationID: "g1:commit-exp-legacy", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		Queue: []string{"track", "track"}, CurrentIndex: 0,
	}, injectExplicitInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := server.CommitMigration(ctx, req)
	if err != nil {
		t.Fatalf("explicit legacy commit: %v", err)
	}
	if committed.PlaybackInstanceID != explicitInstanceID {
		t.Fatalf("explicit instance not bound on legacy shape: want %q got %q (carried source was %q)", explicitInstanceID, committed.PlaybackInstanceID, sourceInstance)
	}
	assertImportedInstanceEverywhere(t, ctx, server, target, "src", explicitInstanceID)
}

func explicitImportSameDevice(t *testing.T, ctx context.Context) {
	t.Helper()
	_, server := newMigrationValidationServer(t)
	attached := attachMigrationSource(t, server, "src", "tab", "old")
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-same", "old")
	req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
		MigrationID: "mig-exp-same", OperationID: "g1:commit-exp-same", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "old", ExpectedGeneration: attached.BindingGeneration,
		QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}, CurrentQueueItemID: explicitImportCurrentRef("qi-1"),
	}, func(payload map[string]any) {
		// A canonical version-1 UUID proves the import has no version
		// restriction; the same-device path has no server carry to
		// compete with the explicit value.
		payload["playbackInstanceId"] = explicitInstanceIDV1
	})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := server.CommitMigration(ctx, req)
	if err != nil {
		t.Fatalf("same-device explicit commit: %v", err)
	}
	if committed.PlaybackInstanceID != explicitInstanceIDV1 {
		t.Fatalf("same-device explicit instance not bound: want %q got %q", explicitInstanceIDV1, committed.PlaybackInstanceID)
	}
	assertImportedInstanceEverywhere(t, ctx, server, migrationTestTarget(t, server, "old"), "src", explicitInstanceIDV1)
}

func explicitImportOmittedCarry(t *testing.T, ctx context.Context) {
	t.Helper()
	server, target, attached, sourceInstance := startExplicitImportSource(t, ctx)
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-omitted", "new")
	committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
		MigrationID: "mig-exp-omitted", OperationID: "g1:commit-exp-omitted", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}, CurrentQueueItemID: explicitImportCurrentRef("qi-1"),
	})
	if err != nil {
		t.Fatalf("omitted-field commit: %v", err)
	}
	if committed.PlaybackInstanceID != sourceInstance {
		t.Fatalf("omitted field changed inferred carry: want %q got %q", sourceInstance, committed.PlaybackInstanceID)
	}
	assertImportedInstanceEverywhere(t, ctx, server, target, "src", sourceInstance)
}

func explicitImportSurvivesIndexShift(t *testing.T, ctx context.Context) {
	t.Helper()
	server, target, attached, _ := startExplicitImportSource(t, ctx)
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-shift", "new")
	req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
		MigrationID: "mig-exp-shift", OperationID: "g1:commit-exp-shift", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		QueueItems:         &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-2", MediaFileID: "track"}, {QueueItemID: "qi-3", MediaFileID: "track"}},
		CurrentQueueItemID: explicitImportCurrentRef("qi-2"),
	}, injectExplicitInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := server.CommitMigration(ctx, req)
	if err != nil {
		t.Fatalf("shift fixture commit: %v", err)
	}
	if committed.PlaybackInstanceID != explicitInstanceID || committed.QueueItemID != "qi-2" {
		t.Fatalf("explicit instance not bound to imported occurrence: %#v", committed)
	}
	if _, err := target.Insert(ctx, 0, []string{"track"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.SessionSnapshot(ctx, "src", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SessionStatus.QueueItemID != "qi-2" {
		t.Fatalf("index shift moved the bound selection: %#v", snapshot.SessionStatus)
	}
	started, err := target.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if started.PlaybackInstanceID != explicitInstanceID {
		t.Fatalf("index shift before start dropped explicit instance: want %q got %q", explicitInstanceID, started.PlaybackInstanceID)
	}
}

func explicitImportSupersededByReselect(t *testing.T, ctx context.Context) {
	t.Helper()
	server, target, attached, _ := startExplicitImportSource(t, ctx)
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-supersede", "new")
	req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
		MigrationID: "mig-exp-supersede", OperationID: "g1:commit-exp-supersede", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		QueueItems:         &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-2", MediaFileID: "track"}},
		CurrentQueueItemID: explicitImportCurrentRef("qi-1"),
	}, injectExplicitInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := server.CommitMigration(ctx, req)
	if err != nil {
		t.Fatalf("supersede fixture commit: %v", err)
	}
	if committed.PlaybackInstanceID != explicitInstanceID {
		t.Fatalf("explicit instance not bound at commit: want %q got %q", explicitInstanceID, committed.PlaybackInstanceID)
	}
	if _, err := target.Skip(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	started, err := target.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if started.PlaybackInstanceID == "" || started.PlaybackInstanceID == explicitInstanceID {
		t.Fatalf("start after superseding selection must mint a fresh instance: explicit=%q got=%q", explicitInstanceID, started.PlaybackInstanceID)
	}
}

func explicitImportFailedLoadStaysStopped(t *testing.T, ctx context.Context) {
	t.Helper()
	server, target, attached, _ := startExplicitImportSource(t, ctx)
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-badload", "new")
	fingerprint := target.SceneFingerprint()
	before, err := target.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
		MigrationID: "mig-exp-badload", OperationID: "g1:commit-exp-badload", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "nope"}}, CurrentQueueItemID: explicitImportCurrentRef("qi-1"),
	}, injectExplicitInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.CommitMigration(ctx, req); err == nil {
		t.Fatal("unresolvable media import succeeded")
	}
	after, err := target.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Playing || after.PlaybackInstanceID != "" || after != before {
		t.Fatalf("failed import load mutated target or reported playback: before=%#v after=%#v", before, after)
	}
	if target.SceneFingerprint() != fingerprint {
		t.Fatal("failed import load mutated target scene")
	}
}

func explicitImportDuplicateMediaCarryCompat(t *testing.T, ctx context.Context) {
	t.Helper()
	// Compatibility control: the pre-existing inferred carry is keyed by
	// the imported current media and stays occurrence-bound afterwards; a
	// duplicate-media import of another occurrence must keep carrying
	// exactly as before the explicit import existed. There is no blanket
	// source-ID equality gate for inferred carry.
	_, server := newMigrationValidationServer(t)
	source, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Set(ctx, []string{"track", "track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Start(ctx); err != nil {
		t.Fatal(err)
	}
	sourceStatus, err := source.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sourceInstance := sourceStatus.PlaybackInstanceID
	if sourceInstance == "" {
		t.Fatalf("expected non-empty source instance: %#v", sourceStatus)
	}
	attached := attachMigrationSource(t, server, "src", "tab", "old")
	target := migrationTestTarget(t, server, "new")
	reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-dupmedia", "new")
	committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
		MigrationID: "mig-exp-dupmedia", OperationID: "g1:commit-exp-dupmedia", SessionID: "src", ClientID: "tab",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		QueueItems:         &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}, {QueueItemID: "qi-2", MediaFileID: "track"}},
		CurrentQueueItemID: explicitImportCurrentRef("qi-2"),
	})
	if err != nil {
		t.Fatalf("duplicate-media inferred carry commit: %v", err)
	}
	if committed.PlaybackInstanceID != sourceInstance || committed.QueueItemID != "qi-2" {
		t.Fatalf("duplicate-media inferred carry changed: want %q on qi-2 got %#v", sourceInstance, committed)
	}
	started, err := target.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if started.PlaybackInstanceID != sourceInstance {
		t.Fatalf("duplicate-media start lost inferred carry: want %q got %q", sourceInstance, started.PlaybackInstanceID)
	}
}

func TestPlaybackMigrationExplicitInstanceImportDecodeReset(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	identityBody := func(migrationID, operationID string, generation uint64, extra string) string {
		return fmt.Sprintf(`{"migrationId":%q,"operationId":%q,"sessionId":"src","clientId":"tab","user":"admin","targetDeviceName":"new","expectedGeneration":%d,"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}],"currentQueueItemId":"qi-1"%s}`,
			migrationID, operationID, generation, extra)
	}

	t.Run("stale null bit from earlier decode does not poison later decode", func(t *testing.T) {
		server, _, attached, sourceInstance := func() (*playbackServer, *playbackDevice, SessionStatus, string) {
			t.Helper()
			_, server := newMigrationValidationServer(t)
			source, err := server.GetDeviceForUser("admin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Set(ctx, []string{"track"}); err != nil {
				t.Fatal(err)
			}
			if _, err := source.Start(ctx); err != nil {
				t.Fatal(err)
			}
			status, err := source.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			attached := attachMigrationSource(t, server, "src", "tab", "old")
			return server, migrationTestTarget(t, server, "new"), attached, status.PlaybackInstanceID
		}()
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-reset-null", "new")
		var req CommitMigrationRequest
		if err := json.Unmarshal([]byte(identityBody("mig-exp-reset-null", "g1:commit-exp-reset-null", attached.BindingGeneration, `,"playbackInstanceId":null`)), &req); err != nil {
			t.Fatal(err)
		}
		if _, err := server.CommitMigration(ctx, req); !errors.Is(err, ErrMigrationProtocolConflict) {
			t.Fatalf("explicit null from first decode accepted: %v", err)
		}
		// A later decode of the same value without the key must observe omitted
		// semantics; the stale null bit from the first decode must be reset.
		if err := json.Unmarshal([]byte(identityBody("mig-exp-reset-null", "g1:commit-exp-reset-omitted", attached.BindingGeneration, "")), &req); err != nil {
			t.Fatal(err)
		}
		committed, err := server.CommitMigration(ctx, req)
		if err != nil {
			t.Fatalf("decode reset did not restore omitted semantics: %v", err)
		}
		if committed.PlaybackInstanceID != sourceInstance {
			t.Fatalf("stale null bit poisoned later decode: want inferred carry %q got %q", sourceInstance, committed.PlaybackInstanceID)
		}
	})

	t.Run("stale presence bit from earlier decode does not poison later decode", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		source, err := server.GetDeviceForUser("admin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Set(ctx, []string{"track"}); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Start(ctx); err != nil {
			t.Fatal(err)
		}
		sourceStatus, err := source.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sourceInstance := sourceStatus.PlaybackInstanceID
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, "mig-exp-reset-present", "new")
		var req CommitMigrationRequest
		if err := json.Unmarshal([]byte(identityBody("mig-exp-reset-present", "g1:commit-exp-reset-present", attached.BindingGeneration, `,"playbackInstanceId":"`+explicitInstanceID+`"`)), &req); err != nil {
			t.Fatal(err)
		}
		// Decode the same variable again without the key before committing: a
		// stale presence bit must not make the server treat the field as
		// explicit (which would bind the literal below instead of carrying).
		if err := json.Unmarshal([]byte(identityBody("mig-exp-reset-present", "g1:commit-exp-reset-present2", attached.BindingGeneration, "")), &req); err != nil {
			t.Fatal(err)
		}
		committed, err := server.CommitMigration(ctx, req)
		if err != nil {
			t.Fatalf("decode reset did not restore omitted semantics: %v", err)
		}
		if committed.PlaybackInstanceID != sourceInstance {
			t.Fatalf("stale presence bit poisoned later decode: want inferred carry %q got %q", sourceInstance, committed.PlaybackInstanceID)
		}
	})
}

func TestPlaybackMigrationExplicitInstanceImportRejection(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	currentOne := func() *string { s := "qi-1"; return &s }
	identityReq := func(migrationID, operationID string, generation uint64) CommitMigrationRequest {
		return CommitMigrationRequest{
			MigrationID: migrationID, OperationID: operationID, SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: generation,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}, CurrentQueueItemID: currentOne(),
		}
	}
	newReservedFixture := func(t *testing.T, migrationID string) (*playbackServer, uint64) {
		t.Helper()
		_, server := newMigrationValidationServer(t)
		migrationTestTarget(t, server, "new").PlaybackQueue.Add(model.MediaFiles{{ID: "seed", Path: "/seed.mp3"}})
		attached := attachMigrationSource(t, server, "src", "tab", "old")
		reserveForCommit(t, server, "src", "tab", attached.BindingGeneration, migrationID, "new")
		return server, attached.BindingGeneration
	}
	// assertUnchanged pins the rejection boundary: no target mutation, no
	// reservation release, no user revocation visible in the scene, and no
	// operation-history entry, i.e. the request died before hashing/admission.
	assertUnchanged := func(t *testing.T, server *playbackServer, migrationID, operationID, fingerprint string) {
		t.Helper()
		after := migrationTestTarget(t, server, "new")
		after.mu.Lock()
		defer after.mu.Unlock()
		if after.sceneFingerprintLocked() != fingerprint {
			t.Fatal("rejected explicit-instance commit mutated target scene")
		}
		if after.PlaybackQueue.Size() != 1 || after.PlaybackQueue.Get()[0].ID != "seed" {
			t.Fatalf("rejected explicit-instance commit mutated target queue: %s", after.PlaybackQueue)
		}
		info, err := server.QueryReservation(ctx, migrationID)
		if err != nil || info.Status != string(ReservationActive) {
			t.Fatalf("rejected explicit-instance commit consumed reservation: %#v %v", info, err)
		}
		record, err := server.QueryOperation(ctx, "src", operationID)
		if err != nil || record.Status != OperationStatusUnknown {
			t.Fatalf("rejected explicit-instance commit entered operation history: %#v %v", record, err)
		}
	}

	t.Run("non-canonical values rejected before operation admission", func(t *testing.T) {
		values := []struct {
			name  string
			value any
		}{
			{"null", nil},
			{"empty", ""},
			{"whitespace", " \t\n"},
			{"leading-space", " " + explicitInstanceID},
			{"trailing-space", explicitInstanceID + " "},
			{"uppercase", "7F3A2B1C-9D4E-4A5F-B6C7-D8E9F0A1B2C3"},
			{"braced", "{" + explicitInstanceID + "}"},
			{"urn", "urn:uuid:" + explicitInstanceID},
			{"undashed-hex", "7f3a2b1c9d4e4a5fb6c7d8e9f0a1b2c3"},
			{"truncated", "7f3a2b1c-9d4e-4a5f"},
			{"non-hex", "zf3a2b1c-9d4e-4a5f-b6c7-d8e9f0a1b2c3"},
		}
		for _, tc := range values {
			t.Run(tc.name, func(t *testing.T) {
				migrationID := "mig-reject-" + tc.name
				server, generation := newReservedFixture(t, migrationID)
				fingerprint := migrationTestTarget(t, server, "new").SceneFingerprint()
				operationID := "g1:commit-reject-" + tc.name
				req, err := decodeCommitMigrationWire(t, identityReq(migrationID, operationID, generation),
					func(payload map[string]any) { payload["playbackInstanceId"] = tc.value })
				if err != nil {
					t.Fatalf("decode %s: %v", tc.name, err)
				}
				if _, err := server.CommitMigration(ctx, req); !errors.Is(err, ErrMigrationProtocolConflict) {
					t.Fatalf("explicit instance %q accepted: %v", tc.name, err)
				}
				assertUnchanged(t, server, migrationID, operationID, fingerprint)
			})
		}
	})

	t.Run("non-string values fail wire decode like other wrong-typed keys", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			value any
		}{
			{"number", float64(42)},
			{"bool", true},
			{"array", []any{}},
			{"object", map[string]any{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, server := newMigrationValidationServer(t)
				attached := attachMigrationSource(t, server, "src", "tab", "old")
				_, err := decodeCommitMigrationWire(t, identityReq("mig-reject-type", "g1:commit-reject-type", attached.BindingGeneration),
					func(payload map[string]any) { payload["playbackInstanceId"] = tc.value })
				if err == nil {
					t.Fatalf("non-string playbackInstanceId %s decoded without error", tc.name)
				}
			})
		}
	})

	t.Run("explicit id with empty queue rejected", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			shape func(req *CommitMigrationRequest)
		}{
			{"empty-identity-queue", func(req *CommitMigrationRequest) { req.QueueItems = &[]QueueItemRef{} }},
			{"empty-legacy-queue", func(req *CommitMigrationRequest) {
				req.QueueItems = nil
				req.Queue = []string{}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				migrationID := "mig-reject-" + tc.name
				server, generation := newReservedFixture(t, migrationID)
				fingerprint := migrationTestTarget(t, server, "new").SceneFingerprint()
				operationID := "g1:commit-reject-" + tc.name
				base := CommitMigrationRequest{
					MigrationID: migrationID, OperationID: operationID, SessionID: "src", ClientID: "tab",
					User: "admin", TargetDeviceName: "new", ExpectedGeneration: generation,
				}
				tc.shape(&base)
				req, err := decodeCommitMigrationWire(t, base, func(payload map[string]any) {
					payload["playbackInstanceId"] = explicitInstanceID
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := server.CommitMigration(ctx, req); !errors.Is(err, ErrMigrationProtocolConflict) {
					t.Fatalf("explicit instance with %s accepted: %v", tc.name, err)
				}
				assertUnchanged(t, server, migrationID, operationID, fingerprint)
			})
		}
	})

	t.Run("explicit id without a selected current item rejected", func(t *testing.T) {
		migrationID := "mig-reject-nocurrent"
		server, generation := newReservedFixture(t, migrationID)
		fingerprint := migrationTestTarget(t, server, "new").SceneFingerprint()
		operationID := "g1:commit-reject-nocurrent"
		req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
			MigrationID: migrationID, OperationID: operationID, SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: generation,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}},
		}, func(payload map[string]any) { payload["playbackInstanceId"] = explicitInstanceID })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.CommitMigration(ctx, req); !errors.Is(err, ErrMigrationProtocolConflict) {
			t.Fatalf("explicit instance without current item accepted: %v", err)
		}
		assertUnchanged(t, server, migrationID, operationID, fingerprint)
	})

	t.Run("omitted explicit id keeps idle imports allowed", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			shape func(req *CommitMigrationRequest)
		}{
			{"empty-identity-queue", func(req *CommitMigrationRequest) { req.QueueItems = &[]QueueItemRef{} }},
			{"identity-without-current", func(req *CommitMigrationRequest) {
				req.QueueItems = &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				migrationID := "mig-allow-" + tc.name
				server, generation := newReservedFixture(t, migrationID)
				base := CommitMigrationRequest{
					MigrationID: migrationID, OperationID: "g1:commit-allow-" + tc.name, SessionID: "src", ClientID: "tab",
					User: "admin", TargetDeviceName: "new", ExpectedGeneration: generation,
				}
				tc.shape(&base)
				committed, err := server.CommitMigration(ctx, base)
				if err != nil {
					t.Fatalf("omitted explicit id with %s rejected: %v", tc.name, err)
				}
				if committed.PlaybackInstanceID != "" || committed.Playing {
					t.Fatalf("idle import minted an instance: %#v", committed)
				}
			})
		}
	})

	t.Run("explicit id is not a credential", func(t *testing.T) {
		migrationID := "mig-reject-authority"
		server, generation := newReservedFixture(t, migrationID)
		fingerprint := migrationTestTarget(t, server, "new").SceneFingerprint()

		t.Run("wrong owner rejected without mutation", func(t *testing.T) {
			operationID := "g1:commit-reject-wrongowner"
			wrongOwner := identityReq(migrationID, operationID, generation)
			wrongOwner.ClientID = "other-tab"
			req, err := decodeCommitMigrationWire(t, wrongOwner, func(payload map[string]any) {
				payload["playbackInstanceId"] = explicitInstanceID
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := server.CommitMigration(ctx, req); !errors.Is(err, ErrSessionOwnership) {
				t.Fatalf("explicit instance bypassed ownership: %v", err)
			}
			assertUnchanged(t, server, migrationID, operationID, fingerprint)
		})

		t.Run("stale generation rejected without mutation", func(t *testing.T) {
			operationID := "g1:commit-reject-stalegen"
			stale := identityReq(migrationID, operationID, generation+1)
			req, err := decodeCommitMigrationWire(t, stale, func(payload map[string]any) {
				payload["playbackInstanceId"] = explicitInstanceID
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := server.CommitMigration(ctx, req); !errors.Is(err, ErrCommandSuperseded) {
				t.Fatalf("explicit instance bypassed generation: %v", err)
			}
			assertUnchanged(t, server, migrationID, operationID, fingerprint)
		})
	})
}

func TestPlaybackMigrationExplicitInstanceImportOperationIdentity(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	currentOne := func() *string { s := "qi-1"; return &s }
	_, server := newMigrationValidationServer(t)
	attached := attachMigrationSource(t, server, "src", "tab", "old")
	withInstance := func(instanceID string) CommitMigrationRequest {
		req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
			MigrationID: "mig-no-reservation", OperationID: "op-explicit-instance", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}, CurrentQueueItemID: currentOne(),
		}, func(payload map[string]any) { payload["playbackInstanceId"] = instanceID })
		if err != nil {
			t.Fatal(err)
		}
		return req
	}

	// The first attempt fails after admission (no reservation) and records a
	// FAILED operation at the current generation.
	if _, err := server.CommitMigration(ctx, withInstance(explicitInstanceID)); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("first attempt: %v", err)
	}
	// The same operationId with a different explicit instance is a different
	// normalized request identity and must not replay the stored result.
	if _, err := server.CommitMigration(ctx, withInstance(explicitInstanceIDAlt)); !errors.Is(err, ErrOperationIdentity) {
		t.Fatalf("changed explicit instance did not change request identity: %v", err)
	}
	// An identical replay reconciles with the stored record.
	if _, err := server.CommitMigration(ctx, withInstance(explicitInstanceID)); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("identical replay lost reconciliation: %v", err)
	}
	// QueryOperation keeps reconciling against the original record.
	record, err := server.QueryOperation(ctx, "src", "op-explicit-instance")
	if err != nil || record.Status != OperationStatusFailed || record.ErrorCode != "RESERVATION_NOT_FOUND" {
		t.Fatalf("query operation lost the original record: %#v err=%v", record, err)
	}

	// An invalid explicit import against a completed operation is rejected by
	// the pre-admission validation, not answered from the cached result.
	if _, err := server.CommitMigration(ctx, withInstance("")); !errors.Is(err, ErrMigrationProtocolConflict) {
		t.Fatalf("invalid explicit import replayed the cached result instead of rejecting: %v", err)
	}
	record, err = server.QueryOperation(ctx, "src", "op-explicit-instance")
	if err != nil || record.Status != OperationStatusFailed || record.ErrorCode != "RESERVATION_NOT_FOUND" {
		t.Fatalf("invalid explicit import mutated the completed record: %#v err=%v", record, err)
	}

	// Omitted field must not be filled before hashing: the identical
	// omitted-field request reconciles with its own record instead of
	// colliding as a different identity.
	omitted := func() CommitMigrationRequest {
		req, err := decodeCommitMigrationWire(t, CommitMigrationRequest{
			MigrationID: "mig-no-reservation", OperationID: "op-omitted-instance", SessionID: "src", ClientID: "tab",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{{QueueItemID: "qi-1", MediaFileID: "track"}}, CurrentQueueItemID: currentOne(),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	if _, err := server.CommitMigration(ctx, omitted()); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("first omitted attempt: %v", err)
	}
	if _, err := server.CommitMigration(ctx, omitted()); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("omitted field was filled before hashing and broke idempotent replay: %v", err)
	}
}

func TestPlaybackSwitchDevicePlaybackInstanceCarry(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})

	t.Run("playing source carries same instance through legacy switch", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		source, err := server.GetDeviceForUser("admin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Set(ctx, []string{"track"}); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Start(ctx); err != nil {
			t.Fatal(err)
		}
		sourceStatus, err := source.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sourceInstance := sourceStatus.PlaybackInstanceID
		if sourceInstance == "" {
			t.Fatalf("expected non-empty source playback instance: %#v", sourceStatus)
		}
		target := migrationTestTarget(t, server, "new")
		before, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.SwitchDevice(ctx, "new"); err != nil {
			t.Fatal(err)
		}
		after, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !after.Playing {
			t.Fatalf("expected playing target after switch: %#v", after)
		}
		if after.QueueVersion != before.QueueVersion+1 {
			t.Fatalf("legacy switch changed structural ordering: before=%#v after=%#v", before, after)
		}
		if after.PlaybackInstanceID != sourceInstance {
			t.Fatalf("playing switch did not carry exact source instance: want %q got %q", sourceInstance, after.PlaybackInstanceID)
		}
	})

	t.Run("paused source carries same instance and stays paused", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		source, err := server.GetDeviceForUser("admin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Set(ctx, []string{"track"}); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		sourceStatus, err := source.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sourceInstance := sourceStatus.PlaybackInstanceID
		if sourceInstance == "" {
			t.Fatalf("expected paused source to retain playback instance: %#v", sourceStatus)
		}
		target := migrationTestTarget(t, server, "new")
		if err := server.SwitchDevice(ctx, "new"); err != nil {
			t.Fatal(err)
		}
		after, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after.Playing {
			t.Fatalf("paused source must not start playing on target: %#v", after)
		}
		if after.PlaybackInstanceID != sourceInstance {
			t.Fatalf("paused switch did not carry exact source instance: want %q got %q", sourceInstance, after.PlaybackInstanceID)
		}
	})

	t.Run("idle source does not carry into occupied target", func(t *testing.T) {
		_, server := newMigrationValidationServer(t)
		target := migrationTestTarget(t, server, "new")
		if _, err := target.Set(ctx, []string{"track"}); err != nil {
			t.Fatal(err)
		}
		if _, err := target.Start(ctx); err != nil {
			t.Fatal(err)
		}
		targetStatus, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		targetInstance := targetStatus.PlaybackInstanceID
		if targetInstance == "" {
			t.Fatalf("expected non-empty target playback instance: %#v", targetStatus)
		}
		if err := server.SwitchDevice(ctx, "new"); err != nil {
			t.Fatal(err)
		}
		after, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !after.Playing || after.PlaybackInstanceID != targetInstance {
			t.Fatalf("idle source switch must not replace target identity: before=%q after=%#v", targetInstance, after)
		}
	})
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

func TestPlaybackManualNavigationOperationReplay(t *testing.T) {
	for _, action := range []string{CommandNext, CommandPrev} {
		t.Run(action, func(t *testing.T) {
			fixture := newIdentityEditFixture(t)
			command := PlaybackCommand{Action: action}
			first := fixture.run(t, "g1:first", command)
			second := fixture.run(t, "g1:second", command)
			if first.CurrentIndex == second.CurrentIndex || second.StateSequence != first.StateSequence+1 {
				t.Fatalf("fresh navigation did not advance: first=%#v second=%#v", first, second)
			}
			replayed := fixture.run(t, "g1:first", command)
			observed, err := fixture.device.Status(fixture.ctx)
			if err != nil || replayed != first || observed != second {
				t.Fatalf("navigation retry changed current state: replayed=%#v observed=%#v err=%v", replayed, observed, err)
			}
		})
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

func TestPlaybackProtectedRemovalFailurePublishesCommittedState(t *testing.T) {
	assertProtectedCommittedFailure(t, CommandRemove, 0, 1)
}

func TestPlaybackProtectedSkipFailurePublishesCommittedState(t *testing.T) {
	assertProtectedCommittedFailure(t, CommandSkip, 1, 0)
}

func assertProtectedCommittedFailure(t *testing.T, action string, targetIndex, queueDelta int) {
	t.Helper()
	ctx, server := newMigrationValidationServer(t)
	device := migrationTestTarget(t, server, "old")
	if _, err := device.Set(ctx, []string{"track", "track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Start(ctx); err != nil {
		t.Fatal(err)
	}
	attached := attachMigrationSource(t, server, "failed-"+action+"-session", "owner", "old")
	before, err := device.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	currentID := device.PlaybackQueue.Items[targetIndex].QueueItemID
	failure := errors.New("successor unpause rejected")
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return &mockTrack{onUnpause: func() error { return failure }}, nil
	}
	broker := server.eventBroker.(*fakeEventBroker)
	broker.mu.Lock()
	broker.sent = nil
	broker.mu.Unlock()
	scope := CommandScope{
		User: "admin", SessionID: attached.SessionID, ClientID: "owner",
		BindingGeneration: &attached.BindingGeneration, OperationID: "g1:" + action + "-failure",
	}
	command := PlaybackCommand{Action: action, QueueItemID: currentID}
	after, err := server.ExecuteCommand(ctx, scope, command)
	if !errors.Is(err, failure) || after.Playing || after.CurrentIndex != targetIndex || after.QueueVersion != before.QueueVersion+queueDelta || after.StateSequence != before.StateSequence+1 {
		t.Errorf("protected %s failure lost committed state: before=%#v after=%#v error=%v", action, before, after, err)
	}
	record, err := server.QueryOperation(ctx, scope.SessionID, scope.OperationID)
	if err != nil || record.Status != OperationStatusFailed || record.DeviceStatus == nil || *record.DeviceStatus != after {
		t.Errorf("committed failure did not retain a failed operation result: record=%#v error=%v", record, err)
	}
	replayed, err := server.ExecuteCommand(ctx, scope, command)
	if !errors.Is(err, failure) || replayed != after {
		t.Errorf("failed %s retry changed its result: original=%#v replayed=%#v error=%v", action, after, replayed, err)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.sent) != 1 {
		t.Fatalf("committed failure must publish once, including replay: %d events", len(broker.sent))
	}
	event, ok := broker.sent[0].event.(*serverevents.JukeboxStateUpdated)
	if !ok || event.Playing || event.QueueVersion != after.QueueVersion || event.StateSequence != after.StateSequence {
		t.Fatalf("committed failure event was not authoritative: %#v", broker.sent[0].event)
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

// TestPlaybackMigrationCommitProofInlineAndQueryContract is the T13-I RED for
// the immutable server-generated migration proof: the commit response must
// carry an inline migrationProof bound to the original migration/operation/
// session/owner/target plus source and committed binding generations and the
// commit-time queue/current/context; the same-original-operation query must
// return the identical historical proof plus a separate currentSnapshot, must
// never recommit, and must not mutate the scene. All assertions decode the
// existing exported status/record shapes to maps so the contract RED is a
// missing additive proof field, never a compile failure.
func TestPlaybackMigrationCommitProofInlineAndQueryContract(t *testing.T) {
	ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
	_, server := newMigrationValidationServer(t)
	attached := attachMigrationSource(t, server, "proof-src", "owner", "old")
	target := migrationTestTarget(t, server, "new")
	target.mu.Lock()
	target.stateSequence = 17
	target.mu.Unlock()
	before, err := target.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reserveForCommit(t, server, "proof-src", "owner", attached.BindingGeneration, "proof-mig", "new")
	position := 17
	mode := PlaybackModeShuffle
	committedAtStart := time.Now().Add(-time.Minute)
	committed, err := server.CommitMigration(ctx, CommitMigrationRequest{
		MigrationID: "proof-mig", OperationID: "g1:proof-mig", SessionID: "proof-src", ClientID: "owner",
		User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
		QueueItems: &[]QueueItemRef{
			{QueueItemID: "qi-1", MediaFileID: "track"},
			{QueueItemID: "qi-2", MediaFileID: "track"},
			{QueueItemID: "qi-3", MediaFileID: "track"},
		},
		CurrentQueueItemID: ptrString("qi-2"), Position: &position, Mode: &mode,
	})
	if err != nil {
		t.Fatalf("proof commit: %v", err)
	}
	if committed.BindingGeneration != attached.BindingGeneration+1 || committed.DeviceName != "new" ||
		committed.OwnerClientID != "owner" || committed.QueueItemID != "qi-2" || committed.CurrentIndex != 1 ||
		committed.Position != 17 || committed.PlayMode != string(PlaybackModeShuffle) {
		t.Fatalf("commit response lost committed context: %#v", committed)
	}

	proofOf := func(t *testing.T, label string, v any) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s marshal: %v", label, err)
		}
		var payload map[string]any
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatalf("%s decode: %v", label, err)
		}
		proof, ok := payload["migrationProof"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing migrationProof: %s", label, encoded)
		}
		return proof
	}
	assertProof := func(t *testing.T, label string, proof map[string]any) {
		t.Helper()
		if proof["migrationId"] != "proof-mig" || proof["operationId"] != "g1:proof-mig" ||
			proof["sessionId"] != "proof-src" || proof["clientId"] != "owner" || proof["user"] != "admin" ||
			proof["sourceDeviceName"] != "old" || proof["targetDeviceName"] != "new" ||
			proof["sourceBindingGeneration"] != float64(attached.BindingGeneration) ||
			proof["committedBindingGeneration"] != float64(attached.BindingGeneration+1) {
			t.Errorf("%s proof lost original identity/generation binding: %#v", label, proof)
		}
		queue, ok := proof["queue"].([]any)
		if !ok || len(queue) != 3 {
			t.Fatalf("%s proof queue must carry the three committed occurrences: %#v", label, proof)
		}
		for i, want := range []struct{ item, media string }{{"qi-1", "track"}, {"qi-2", "track"}, {"qi-3", "track"}} {
			entry, ok := queue[i].(map[string]any)
			if !ok || entry["queueItemId"] != want.item || entry["mediaFileId"] != want.media {
				t.Errorf("%s proof queue[%d] = %#v, want queueItemId=%s mediaFileId=%s", label, i, queue[i], want.item, want.media)
			}
		}
		if proof["currentQueueItemId"] != "qi-2" || proof["currentIndex"] != float64(1) ||
			proof["position"] != float64(17) || proof["mode"] != string(PlaybackModeShuffle) {
			t.Errorf("%s proof lost commit-time current/context: %#v", label, proof)
		}
		if proof["gain"] != float64(1) || proof["desiredGain"] != float64(1) || proof["muted"] != false {
			t.Errorf("%s proof lost commit-time gain/mute context: %#v", label, proof)
		}
		if proof["queueVersion"] != float64(committed.QueueVersion) ||
			proof["stateSequence"] != float64(committed.StateSequence) {
			t.Errorf("%s proof lost commit-time ordering: queueVersion=%v stateSequence=%v committed=%#v",
				label, proof["queueVersion"], proof["stateSequence"], committed)
		}
		committedAt, ok := proof["committedAt"].(string)
		if !ok || committedAt == "" {
			t.Fatalf("%s proof missing committedAt: %#v", label, proof)
		}
		stamp, err := time.Parse(time.RFC3339Nano, committedAt)
		if err != nil || stamp.Before(committedAtStart) || stamp.After(time.Now().Add(time.Minute)) {
			t.Errorf("%s proof committedAt invalid: %q err=%v", label, committedAt, err)
		}
	}

	t.Run("inline proof binds original ids and committed generations", func(t *testing.T) {
		assertProof(t, "inline", proofOf(t, "commit response", committed))
	})
	t.Run("same operation query returns identical historical proof and separate current snapshot", func(t *testing.T) {
		proof := proofOf(t, "operation record", mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig"))
		assertProof(t, "query", proof)
		record := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(payload["migrationProof"], proofOf(t, "commit response", committed)) {
			t.Errorf("queried historical proof differs from inline commit proof: query=%#v inline=%#v",
				payload["migrationProof"], proofOf(t, "commit response", committed))
		}
		snapshot, ok := payload["currentSnapshot"].(map[string]any)
		if !ok {
			t.Fatalf("operation record missing separate currentSnapshot: %s", encoded)
		}
		sessionStatus, ok := snapshot["sessionStatus"].(map[string]any)
		if !ok || sessionStatus["bindingGeneration"] != float64(attached.BindingGeneration+1) ||
			sessionStatus["deviceName"] != "new" || sessionStatus["queueItemId"] != "qi-2" {
			t.Errorf("currentSnapshot lost current session binding: %#v", snapshot)
		}
		playlist, ok := snapshot["jukeboxPlaylist"].(map[string]any)
		if !ok {
			t.Fatalf("currentSnapshot missing jukeboxPlaylist: %#v", snapshot)
		}
		entries, ok := playlist["entry"].([]any)
		if !ok || len(entries) != 3 {
			t.Fatalf("currentSnapshot must carry the full committed queue entries: %#v", playlist)
		}
		for i, want := range []string{"qi-1", "qi-2", "qi-3"} {
			entry, ok := entries[i].(map[string]any)
			if !ok || entry["queueItemId"] != want {
				t.Errorf("currentSnapshot entry[%d] = %#v, want queueItemId=%s", i, entries[i], want)
			}
		}
	})
	t.Run("query never recommits and does not mutate the committed scene", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			record := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
			if record.Status != OperationStatusApplied {
				t.Fatalf("query %d changed operation status: %#v", i, record)
			}
			if record.SessionStatus == nil || record.SessionStatus.BindingGeneration != attached.BindingGeneration+1 {
				t.Fatalf("query %d changed committed session status: %#v", i, record.SessionStatus)
			}
		}
		after, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after.QueueVersion != before.QueueVersion+1 || after.StateSequence != before.StateSequence+1 ||
			after.CurrentIndex != 1 || after.Position != 17 || string(after.Mode) != string(PlaybackModeShuffle) ||
			len(target.PlaybackQueue.ItemIDs()) != 3 {
			t.Errorf("query mutated the committed scene: before=%#v after=%#v", before, after)
		}
	})
	t.Run("duplicate original operation id never recommits the committed scene", func(t *testing.T) {
		// The commit advanced the session binding generation, so re-POSTing the
		// original operation identity either reconciles against the stored
		// record or is rejected as superseded; in both cases the committed
		// scene must not be applied a second time. The authoritative
		// reconciliation channel for the original id is the read-only query.
		_, _ = server.CommitMigration(ctx, CommitMigrationRequest{
			MigrationID: "proof-mig", OperationID: "g1:proof-mig", SessionID: "proof-src", ClientID: "owner",
			User: "admin", TargetDeviceName: "new", ExpectedGeneration: attached.BindingGeneration,
			QueueItems: &[]QueueItemRef{
				{QueueItemID: "qi-1", MediaFileID: "track"},
				{QueueItemID: "qi-2", MediaFileID: "track"},
				{QueueItemID: "qi-3", MediaFileID: "track"},
			},
			CurrentQueueItemID: ptrString("qi-2"), Position: &position, Mode: &mode,
		})
		after, err := target.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after.QueueVersion != before.QueueVersion+1 || after.StateSequence != before.StateSequence+1 ||
			len(target.PlaybackQueue.ItemIDs()) != 3 {
			t.Errorf("duplicate original operation id recommitted: before=%#v after=%#v", before, after)
		}
		record := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
		if record.Status != OperationStatusApplied {
			t.Fatalf("duplicate attempt corrupted the original operation record: %#v", record)
		}
		assertProof(t, "post-duplicate query", proofOf(t, "post-duplicate query record", record))
	})
	t.Run("unknown operation reads UNKNOWN without proof", func(t *testing.T) {
		record, err := server.QueryOperation(ctx, "proof-src", "g1:never-executed")
		if err != nil {
			t.Fatalf("unknown operation query: %v", err)
		}
		if record.Status != OperationStatusUnknown || record.MigrationProof != nil || record.CurrentSnapshot != nil {
			t.Errorf("unknown operation must read UNKNOWN without proof or snapshot: %#v", record)
		}
	})
	t.Run("cross-user query is denied", func(t *testing.T) {
		bobCtx := request.WithUser(context.Background(), model.User{UserName: "bob"})
		if _, err := server.QueryOperation(bobCtx, "proof-src", "g1:proof-mig"); !errors.Is(err, ErrSessionOwnership) {
			t.Errorf("cross-user query must fail closed with ErrSessionOwnership: %v", err)
		}
	})
	t.Run("later legitimate edits leave historical proof unchanged and current snapshot newer", func(t *testing.T) {
		gen := attached.BindingGeneration + 1
		scope := CommandScope{User: "admin", SessionID: "proof-src", ClientID: "owner", BindingGeneration: &gen}
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
			t.Fatalf("later queue edit: %v", err)
		}
		if _, err := server.ExecuteCommand(ctx, migrationCommandScope(scope), PlaybackCommand{Action: CommandGain, Gain: 0.25}); err != nil {
			t.Fatalf("later gain edit: %v", err)
		}
		record := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
		if record.Status != OperationStatusApplied {
			t.Fatalf("later edits changed operation status: %#v", record)
		}
		assertProof(t, "post-edit query", proofOf(t, "post-edit query record", record))
		proof := proofOf(t, "post-edit query", record)
		if proof["queueVersion"] != float64(committed.QueueVersion) {
			t.Errorf("historical proof recorded a newer queueVersion after later edits: %v", proof["queueVersion"])
		}
		if record.CurrentSnapshot == nil {
			t.Fatal("post-edit query lost the separate current snapshot")
		}
		if len(record.CurrentSnapshot.JukeboxPlaylist.Entry) != 1 {
			t.Errorf("current snapshot did not track the later queue edit: %#v", record.CurrentSnapshot.JukeboxPlaylist.Entry)
		}
		if record.CurrentSnapshot.SessionStatus.QueueVersion <= committed.QueueVersion {
			t.Errorf("current snapshot must be newer than the commit: snapshot=%v committed=%v",
				record.CurrentSnapshot.SessionStatus.QueueVersion, committed.QueueVersion)
		}
		if record.CurrentSnapshot.SessionStatus.DesiredGain != 0.25 {
			t.Errorf("current snapshot did not track the later gain edit: %#v", record.CurrentSnapshot.SessionStatus)
		}
	})
	t.Run("mutating returned Go objects cannot affect the cached record", func(t *testing.T) {
		record := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
		record.MigrationProof.Queue[0].QueueItemID = "hacked"
		record.MigrationProof.Position = 999
		record.SessionStatus.MigrationProof.Queue[0].MediaFileID = "hacked"
		record.CurrentSnapshot.SessionStatus.Position = 999
		committed.MigrationProof.Queue[0].QueueItemID = "hacked-inline"
		reRecord := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
		assertProof(t, "post-mutation query", proofOf(t, "post-mutation query record", reRecord))
		nested := reRecord.SessionStatus.MigrationProof
		if nested == nil || nested.Queue[0].QueueItemID != "qi-1" || nested.Queue[0].MediaFileID != "track" {
			t.Errorf("cached record aliased through a returned Go object: %#v", nested)
		}
	})
	t.Run("concurrent queries and snapshots stay coherent", func(t *testing.T) {
		baselineRecord := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
		baseline, err := json.Marshal(baselineRecord.MigrationProof)
		if err != nil {
			t.Fatal(err)
		}
		errs := make(chan error, 16)
		var wg sync.WaitGroup
		for worker := 0; worker < 4; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 25; i++ {
					record, err := server.QueryOperation(ctx, "proof-src", "g1:proof-mig")
					if err != nil {
						errs <- err
						return
					}
					encoded, err := json.Marshal(record.MigrationProof)
					if err != nil {
						errs <- err
						return
					}
					if string(encoded) != string(baseline) {
						errs <- fmt.Errorf("queried proof drifted under concurrency: %s", encoded)
						return
					}
					if _, err := server.SessionSnapshot(ctx, "proof-src", "admin"); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("concurrent query: %v", err)
		}
	})
	t.Run("takeover before query withholds proof on the response copy only", func(t *testing.T) {
		proofOfRecord := func(t *testing.T, label string) (map[string]any, map[string]any) {
			t.Helper()
			record := mustQueryOperationRecord(t, ctx, server, "proof-src", "g1:proof-mig")
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("%s marshal: %v", label, err)
			}
			var payload map[string]any
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatalf("%s decode: %v", label, err)
			}
			proof, _ := payload["migrationProof"].(map[string]any)
			return payload, proof
		}
		_, proof := proofOfRecord(t, "pre-takeover query")
		if proof == nil {
			t.Fatal("pre-takeover query must expose the committed proof")
		}
		takenOver, err := server.AttachSession(ctx, AttachRequest{
			SessionID: "proof-src", User: "admin", ClientID: "intruder", DeviceName: "new", Takeover: true,
		})
		if err != nil || takenOver.OwnerClientID != "intruder" ||
			takenOver.BindingGeneration != attached.BindingGeneration+2 {
			t.Fatalf("explicit takeover did not rebind the session: %#v err=%v", takenOver, err)
		}
		payload, proof := proofOfRecord(t, "post-takeover query")
		if proof != nil {
			t.Errorf("post-takeover query must withhold the stale proof from the response copy: %v", payload)
		}
		if payload["migrationProofSuperseded"] != true {
			t.Errorf("post-takeover query missing migrationProofSuperseded: %v", payload)
		}
		sessionStatus, ok := payload["sessionStatus"].(map[string]any)
		if !ok || sessionStatus["bindingGeneration"] != float64(attached.BindingGeneration+1) ||
			sessionStatus["ownerClientId"] != "owner" {
			t.Errorf("response copy rewrote the canonical historical record: %v", payload)
		}
	})
}

// Characterizes the operation-store proof boundaries that the playback server
// relies on: a pending operation reads PENDING without any proof until the same
// original id completes APPLIED with the proof, Get egress never aliases the
// cached proof, and TTL eviction reads UNKNOWN without proof and never retries
// or recommits on its own.
func TestPlaybackMigrationProofStoreBoundaries(t *testing.T) {
	t.Run("pending gap reads PENDING without proof then completes APPLIED with proof", func(t *testing.T) {
		clock := time.Now()
		store := NewOperationStore(8, time.Minute, func() time.Time { return clock })
		admission := store.acquire(OperationRecord{Status: OperationStatusUnknown, SessionID: "s", OperationID: "op"})
		if !admission.first {
			t.Fatal("expected first admission for a new operation")
		}
		rec, ok := store.Get("s", "op")
		if !ok || rec.Status != OperationStatusPending || rec.MigrationProof != nil {
			t.Fatalf("pending gap must read PENDING without proof: %#v ok=%v", rec, ok)
		}
		proof := &MigrationProof{
			MigrationID: "m", OperationID: "op", SessionID: "s", ClientID: "owner", User: "admin",
			SourceDeviceName: "old", TargetDeviceName: "new", CommittedAt: clock,
			Queue: []MigrationProofItem{{QueueItemID: "qi-1", MediaFileID: "track"}},
		}
		store.Complete("s", "op", OperationRecord{Status: OperationStatusApplied, SessionID: "s", OperationID: "op", MigrationProof: proof})
		rec, ok = store.Get("s", "op")
		if !ok || rec.Status != OperationStatusApplied || rec.MigrationProof == nil || len(rec.MigrationProof.Queue) != 1 {
			t.Fatalf("completion must read APPLIED with proof for the same original id: %#v ok=%v", rec, ok)
		}
		rec.MigrationProof.Queue[0].QueueItemID = "hacked"
		again, _ := store.Get("s", "op")
		if again.MigrationProof == nil || again.MigrationProof.Queue[0].QueueItemID != "qi-1" {
			t.Errorf("Get egress aliases the cached proof: %#v", again.MigrationProof)
		}
	})
	t.Run("TTL eviction reads UNKNOWN without proof and does not retry", func(t *testing.T) {
		clock := time.Now()
		store := NewOperationStore(8, time.Minute, func() time.Time { return clock })
		proof := &MigrationProof{
			MigrationID: "m", OperationID: "op", SessionID: "s", CommittedAt: clock,
			Queue: []MigrationProofItem{{QueueItemID: "qi-1", MediaFileID: "track"}},
		}
		store.Complete("s", "op", OperationRecord{Status: OperationStatusApplied, SessionID: "s", OperationID: "op", MigrationProof: proof, CreatedAt: clock})
		if _, ok := store.Get("s", "op"); !ok {
			t.Fatal("record must be readable before its TTL expires")
		}
		clock = clock.Add(2 * time.Minute)
		rec, ok := store.Get("s", "op")
		if ok {
			t.Fatalf("evicted record must not be readable: %#v", rec)
		}
		if rec.Status != OperationStatusUnknown || rec.MigrationProof != nil {
			t.Errorf("eviction must read UNKNOWN without proof: %#v", rec)
		}
	})
}

func ptrString(s string) *string { return &s }

func mustQueryOperationRecord(t *testing.T, ctx context.Context, server *playbackServer, sessionID, operationID string) OperationRecord {
	t.Helper()
	record, err := server.QueryOperation(ctx, sessionID, operationID)
	if err != nil {
		t.Fatalf("QueryOperation(%s, %s): %v", sessionID, operationID, err)
	}
	return record
}
