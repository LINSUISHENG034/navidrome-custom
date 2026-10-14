package playback

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/model"
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
			track.onUnpause = func() {
				firstPosition <- track.position
				gain := float32(-1)
				if len(track.volumeValues) > 0 {
					gain = track.volumeValues[len(track.volumeValues)-1]
				}
				firstGain <- gain
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
