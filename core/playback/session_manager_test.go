package playback

import (
	"testing"
	"time"
)

func TestSessionManager_AttachAndHeartbeat(t *testing.T) {
	mgr := NewSessionManager(time.Minute)
	sess := mgr.Attach(AttachRequest{
		SessionID:  "s1",
		User:       "admin",
		ClientID:   "tab-1",
		DeviceName: "pulse/bluez_output.X.a2dp-sink",
	})

	if sess.SessionID != "s1" || sess.OwnerClientID != "tab-1" {
		t.Fatalf("unexpected session: %#v", sess)
	}

	if err := mgr.Heartbeat("s1", "tab-1"); err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}
}

func TestSessionManager_RejectsHeartbeatFromWrongClient(t *testing.T) {
	mgr := NewSessionManager(time.Minute)
	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	if err := mgr.Heartbeat("s1", "tab-2"); err == nil {
		t.Fatal("expected ownership error")
	}
}
