package playback

import (
	"errors"
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

func TestSessionManager_HeartbeatNonexistentSession(t *testing.T) {
	mgr := NewSessionManager(time.Minute)

	err := mgr.Heartbeat("nonexistent", "tab-1")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got: %v", err)
	}
}

func TestSessionManager_DetachSuccess(t *testing.T) {
	mgr := NewSessionManager(time.Minute)
	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	if err := mgr.Detach("s1", "tab-1"); err != nil {
		t.Fatalf("detach failed: %v", err)
	}

	if _, ok := mgr.Get("s1"); ok {
		t.Fatal("session should not exist after detach")
	}
}

func TestSessionManager_DetachRejectsWrongClient(t *testing.T) {
	mgr := NewSessionManager(time.Minute)
	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	err := mgr.Detach("s1", "tab-2")
	if !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("expected ErrSessionOwnership, got: %v", err)
	}

	// session should still exist
	if _, ok := mgr.Get("s1"); !ok {
		t.Fatal("session should survive rejected detach")
	}
}

func TestSessionManager_DetachNonexistentSession(t *testing.T) {
	mgr := NewSessionManager(time.Minute)

	err := mgr.Detach("nonexistent", "tab-1")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got: %v", err)
	}
}

func TestSessionManager_GetExistingSession(t *testing.T) {
	mgr := NewSessionManager(time.Minute)
	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "pulse/test"})

	sess, ok := mgr.Get("s1")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if sess.User != "admin" || sess.DeviceName != "pulse/test" {
		t.Fatalf("unexpected session data: %#v", sess)
	}
}

func TestSessionManager_GetNonexistentSession(t *testing.T) {
	mgr := NewSessionManager(time.Minute)

	_, ok := mgr.Get("nonexistent")
	if ok {
		t.Fatal("expected session not to exist")
	}
}

func TestSessionManager_AttachOverwritesTakeover(t *testing.T) {
	mgr := NewSessionManager(time.Minute)
	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	// second attach with different client silently takes over
	sess := mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-2", User: "admin", DeviceName: "auto"})
	if sess.OwnerClientID != "tab-2" {
		t.Fatalf("expected tab-2 to own session, got: %s", sess.OwnerClientID)
	}

	// old client can no longer heartbeat
	err := mgr.Heartbeat("s1", "tab-1")
	if !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("expected ErrSessionOwnership for old client, got: %v", err)
	}

	// new client can heartbeat
	if err := mgr.Heartbeat("s1", "tab-2"); err != nil {
		t.Fatalf("new client heartbeat failed: %v", err)
	}
}

func TestSessionManager_ReapExpiredRemovesStaleSession(t *testing.T) {
	ttl := 50 * time.Millisecond
	mgr := NewSessionManager(ttl)

	// controllable clock
	now := time.Now()
	mgr.now = func() time.Time { return now }

	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	// not yet expired
	now = now.Add(30 * time.Millisecond)
	expired := mgr.ReapExpired()
	if len(expired) != 0 {
		t.Fatalf("expected no expired sessions, got %d", len(expired))
	}

	// advance past TTL
	now = now.Add(30 * time.Millisecond)
	expired = mgr.ReapExpired()
	if len(expired) != 1 || expired[0].SessionID != "s1" {
		t.Fatalf("expected 1 expired session 's1', got: %v", expired)
	}

	// session should be gone
	if _, ok := mgr.Get("s1"); ok {
		t.Fatal("expired session should be removed")
	}
}

func TestSessionManager_ReapExpiredKeepsFreshSessions(t *testing.T) {
	ttl := 100 * time.Millisecond
	mgr := NewSessionManager(ttl)

	now := time.Now()
	mgr.now = func() time.Time { return now }

	mgr.Attach(AttachRequest{SessionID: "stale", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	// advance time, then attach a fresh session
	now = now.Add(80 * time.Millisecond)
	mgr.Attach(AttachRequest{SessionID: "fresh", ClientID: "tab-2", User: "admin", DeviceName: "auto"})

	// advance past stale TTL but not fresh TTL
	now = now.Add(30 * time.Millisecond)
	expired := mgr.ReapExpired()
	if len(expired) != 1 || expired[0].SessionID != "stale" {
		t.Fatalf("expected only 'stale' expired, got: %v", expired)
	}

	// fresh session should survive
	if _, ok := mgr.Get("fresh"); !ok {
		t.Fatal("fresh session should still exist")
	}
}

func TestSessionManager_ReapExpiredDisabledWithZeroTTL(t *testing.T) {
	mgr := NewSessionManager(0)
	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	// with TTL=0, reaping should be disabled
	expired := mgr.ReapExpired()
	if len(expired) != 0 {
		t.Fatalf("expected no reaping with zero TTL, got %d", len(expired))
	}

	if _, ok := mgr.Get("s1"); !ok {
		t.Fatal("session should survive with zero TTL")
	}
}

func TestSessionManager_HeartbeatRefreshesTTL(t *testing.T) {
	ttl := 50 * time.Millisecond
	mgr := NewSessionManager(ttl)

	now := time.Now()
	mgr.now = func() time.Time { return now }

	mgr.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "auto"})

	// advance close to TTL and heartbeat
	now = now.Add(40 * time.Millisecond)
	if err := mgr.Heartbeat("s1", "tab-1"); err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}

	// advance past original TTL but within refreshed TTL
	now = now.Add(40 * time.Millisecond)
	expired := mgr.ReapExpired()
	if len(expired) != 0 {
		t.Fatal("heartbeat should have refreshed TTL, but session expired")
	}
}
