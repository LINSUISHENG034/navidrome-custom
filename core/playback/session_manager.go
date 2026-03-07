package playback

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionOwnership = errors.New("session owned by another client")
	defaultSessionTTL   = 45 * time.Second
)

type AttachRequest struct {
	SessionID  string
	User       string
	ClientID   string
	DeviceName string
}

type Session struct {
	SessionID     string    `json:"sessionId"`
	User          string    `json:"user"`
	OwnerClientID string    `json:"ownerClientId"`
	DeviceName    string    `json:"deviceName"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}

type SessionStatus struct {
	SessionID     string    `json:"sessionId"`
	DeviceName    string    `json:"deviceName"`
	OwnerClientID string    `json:"ownerClientId"`
	CurrentIndex  int       `json:"currentIndex"`
	TrackID       string    `json:"trackId"`
	Playing       bool      `json:"playing"`
	Position      int       `json:"position"`
	Gain          float32   `json:"gain"`
	Attached      bool      `json:"attached"`
	QueueVersion  int       `json:"queueVersion"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}

type SessionManager struct {
	mu       sync.RWMutex
	ttl      time.Duration
	now      func() time.Time
	sessions map[string]Session
}

func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{
		ttl:      ttl,
		now:      time.Now,
		sessions: make(map[string]Session),
	}
}

func (sm *SessionManager) Attach(req AttachRequest) Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session := Session{
		SessionID:     req.SessionID,
		User:          req.User,
		OwnerClientID: req.ClientID,
		DeviceName:    req.DeviceName,
		LastHeartbeat: sm.now().UTC(),
	}
	sm.sessions[req.SessionID] = session
	return session
}

func (sm *SessionManager) Heartbeat(sessionID, clientID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if session.OwnerClientID != clientID {
		return ErrSessionOwnership
	}

	session.LastHeartbeat = sm.now().UTC()
	sm.sessions[sessionID] = session
	return nil
}

func (sm *SessionManager) Detach(sessionID, clientID string) error {
	_, err := sm.DetachSnapshot(sessionID, clientID)
	return err
}

func (sm *SessionManager) DetachSnapshot(sessionID, clientID string) (Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	if session.OwnerClientID != clientID {
		return Session{}, ErrSessionOwnership
	}

	delete(sm.sessions, sessionID)
	return session, nil
}

func (sm *SessionManager) Get(sessionID string) (Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[sessionID]
	return session, ok
}

func (sm *SessionManager) FindByDevice(deviceName string) []Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sessions := make([]Session, 0)
	for _, session := range sm.sessions {
		if session.DeviceName == deviceName {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

func (sm *SessionManager) ReapExpired() []Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.ttl <= 0 {
		return nil
	}

	now := sm.now().UTC()
	expired := make([]Session, 0)
	for id, session := range sm.sessions {
		if now.Sub(session.LastHeartbeat) <= sm.ttl {
			continue
		}
		expired = append(expired, session)
		delete(sm.sessions, id)
	}
	return expired
}
