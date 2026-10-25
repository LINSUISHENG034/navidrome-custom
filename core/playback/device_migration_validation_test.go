package playback

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/tests"
)

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
	_, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "old-tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	oldScope := CommandScope{User: "admin", SessionID: "owner", ClientID: "old-tab"}
	for _, command := range []PlaybackCommand{
		{Action: CommandSet, IDs: []string{"track", "track"}},
		{Action: CommandStart},
		{Action: CommandSeek, Position: 17},
		{Action: CommandGain, Gain: 0.375},
	} {
		if _, err := server.ExecuteCommand(ctx, oldScope, command); err != nil {
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
		if _, err := server.ExecuteCommand(ctx, oldScope, command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("old owner retained %s after explicit takeover: %v", command.Action, err)
		}
	}
	if err := server.SwitchDeviceForCommand(ctx, oldScope, "new"); !errors.Is(err, ErrSessionOwnership) {
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
	if status, err := server.ExecuteCommand(ctx, newScope, PlaybackCommand{Action: CommandPause}); err != nil || status.Playing || status.Position != 17 {
		t.Fatalf("new owner cannot control preserved playback: %#v err=%v", status, err)
	}
}

func TestPlaybackExplicitTakeoverSerializesWithAdmittedOldOwnerMutation(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	_, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "old-tab", DeviceName: "old"})
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
	oldScope := CommandScope{User: "admin", SessionID: "owner", ClientID: "old-tab"}
	for _, command := range []PlaybackCommand{{Action: CommandSet, IDs: []string{"track"}}, {Action: CommandStart}} {
		if _, err := server.ExecuteCommand(ctx, oldScope, command); err != nil {
			t.Fatal(err)
		}
	}
	mutated := make(chan error, 1)
	go func() {
		_, err := server.ExecuteCommand(ctx, oldScope, PlaybackCommand{Action: CommandGain, Gain: 0.25})
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
	if _, err := server.ExecuteCommand(ctx, oldScope, PlaybackCommand{Action: CommandGain, Gain: 0.5}); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("old owner mutated after takeover completion: %v", err)
	}
}

func TestPlaybackExplicitTakeoverCancelsInFlightOldOwnerPreparation(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	_, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "old-tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	oldScope := CommandScope{User: "admin", SessionID: "owner", ClientID: "old-tab"}
	if _, err := server.ExecuteCommand(ctx, oldScope, PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
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
		_, err := server.ExecuteCommand(ctx, oldScope, PlaybackCommand{Action: CommandStart})
		started <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old-owner preparation did not enter")
	}
	if _, err := server.AttachSession(ctx, AttachRequest{
		SessionID: "owner", User: "admin", ClientID: "new-tab", DeviceName: "old", Takeover: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, oldScope, PlaybackCommand{Action: CommandStop}); !errors.Is(err, ErrSessionOwnership) {
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
	if status, err := server.ExecuteCommand(ctx, newScope, PlaybackCommand{Action: CommandStart}); err != nil || !status.Playing {
		t.Fatalf("new owner could not retry logical playback: %#v err=%v", status, err)
	}
}

func TestPlaybackCommandAuthorityValidation(t *testing.T) {
	ctx, server := newMigrationValidationServer(t)
	ctx = request.WithUser(ctx, model.User{UserName: "admin"})
	_, err := server.AttachSession(ctx, AttachRequest{SessionID: "bound", User: "admin", ClientID: "owner", DeviceName: "new"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "bound", ClientID: "owner"}
	commands := playbackAuthorityValidationCommands()
	for _, command := range commands {
		if _, err := server.ExecuteCommand(ctx, CommandScope{User: "admin"}, command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("bare command bypassed non-default binding for %s: %v", command.Action, err)
		}
		wrongOwner := scope
		wrongOwner.ClientID = "other"
		if _, err := server.ExecuteCommand(ctx, wrongOwner, command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("non-owner %s accepted: %v", command.Action, err)
		}
		wrongUser := scope
		wrongUser.User = "other-user"
		if _, err := server.ExecuteCommand(ctx, wrongUser, command); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("forged user %s accepted: %v", command.Action, err)
		}
		status, err := server.ExecuteCommand(ctx, scope, command)
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
	_, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "owner", ClientID: "tab"}
	for _, denied := range []CommandScope{{User: "admin"}, {User: "admin", SessionID: "owner", ClientID: "other"}} {
		if err := server.SwitchDeviceForCommand(ctx, denied, "new"); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("switch bypass accepted: %#v err=%v", denied, err)
		}
	}
	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	if err := server.SwitchDeviceForCommand(ctx, scope, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandGain, Gain: 0.375}); err != nil {
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
	_, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "owner", ClientID: "tab"}
	_, err = server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandSet, IDs: []string{"track"}})
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
		_, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandStart})
		started <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandStop}); err != nil {
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
	_, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner", User: "admin", ClientID: "tab", DeviceName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	scope := CommandScope{User: "admin", SessionID: "owner", ClientID: "tab"}
	for _, command := range []PlaybackCommand{{Action: CommandSet, IDs: []string{"track"}}, {Action: CommandStart}} {
		if _, err := server.ExecuteCommand(ctx, scope, command); err != nil {
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
	go func() { switched <- server.SwitchDeviceForCommand(ctx, scope, "new") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("migration did not enter target preparation")
	}
	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandStop}); err != nil {
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
	if status, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandStart}); err != nil || !status.Playing {
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
	scope1 := CommandScope{User: "admin", SessionID: "owner-session", ClientID: "tab-1"}
	if _, err := server.AttachSession(ctx, AttachRequest{SessionID: "owner-session", User: "admin", ClientID: "tab-1", DeviceName: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope1, PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope1, PlaybackCommand{Action: CommandStart}); err != nil {
		t.Fatal(err)
	}

	scope2 := CommandScope{User: "admin", SessionID: "target-session", ClientID: "other-tab"}
	if _, err := server.AttachSession(ctx, AttachRequest{SessionID: "target-session", User: "admin", ClientID: "other-tab", DeviceName: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope2, PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope2, PlaybackCommand{Action: CommandStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope2, PlaybackCommand{Action: CommandSeek, Position: 12}); err != nil {
		t.Fatal(err)
	}

	err := server.SwitchDeviceForCommand(ctx, scope1, "new")
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
	scope := CommandScope{User: "admin", SessionID: "s1", ClientID: "tab-1"}
	if _, err := server.AttachSession(ctx, AttachRequest{SessionID: "s1", User: "admin", ClientID: "tab-1", DeviceName: "old"}); err != nil {
		t.Fatal(err)
	}

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

	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandSet, IDs: []string{"track"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandSeek, Position: 10}); err != nil {
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
		switched <- server.SwitchDeviceForCommand(ctx, scope, "new")
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
