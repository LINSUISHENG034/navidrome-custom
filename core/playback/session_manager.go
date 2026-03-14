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

const (
	defaultSessionMaxStale = 10 * time.Minute

	SessionOwnershipAttached   = "attached"
	SessionOwnershipRecovering = "recovering"
	SessionOwnershipDetached   = "detached"

	SessionTerminationStaleExpired = "stale_expired"
	SessionTerminationUserDetached = "user_detached"
)

type AttachRequest struct {
	SessionID  string
	User       string
	ClientID   string
	DeviceName string
}

type Session struct {
	SessionID      string     `json:"sessionId"`
	User           string     `json:"user"`
	OwnerClientID  string     `json:"ownerClientId"`
	DeviceName     string     `json:"deviceName"`
	LastHeartbeat  time.Time  `json:"lastHeartbeat"`
	OwnershipState string     `json:"ownershipState"`
	StaleSince     *time.Time `json:"staleSince,omitempty"`
}

type SessionStatus struct {
	SessionID         string    `json:"sessionId"`
	DeviceName        string    `json:"deviceName"`
	OwnerClientID     string    `json:"ownerClientId"`
	CurrentIndex      int       `json:"currentIndex"`
	TrackID           string    `json:"trackId"`
	Playing           bool      `json:"playing"`
	Position          int       `json:"position"`
	Gain              float32   `json:"gain"`
	Attached          bool      `json:"attached"`
	OwnershipState    string    `json:"ownershipState"`
	TerminationReason string    `json:"terminationReason,omitempty"`
	QueueVersion      int       `json:"queueVersion"`
	LastHeartbeat     time.Time `json:"lastHeartbeat"`
}

type SessionManager struct {
	mu       sync.RWMutex
	ttl      time.Duration
	maxStale time.Duration
	now      func() time.Time
	sessions map[string]Session
}

func NewSessionManager(ttl time.Duration, maxStale ...time.Duration) *SessionManager {
	sessionMaxStale := defaultSessionMaxStale
	if len(maxStale) > 0 {
		sessionMaxStale = maxStale[0]
	}
	return &SessionManager{
		ttl:      ttl,
		maxStale: sessionMaxStale,
		now:      time.Now,
		sessions: make(map[string]Session),
	}
}

func (sm *SessionManager) Attach(req AttachRequest) Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session := Session{
		SessionID:      req.SessionID,
		User:           req.User,
		OwnerClientID:  req.ClientID,
		DeviceName:     req.DeviceName,
		LastHeartbeat:  sm.now().UTC(),
		OwnershipState: SessionOwnershipAttached,
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
	session.OwnershipState = SessionOwnershipAttached
	session.StaleSince = nil
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

func (sm *SessionManager) RebindDevice(oldDeviceName, newDeviceName string) []Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if oldDeviceName == "" || newDeviceName == "" || oldDeviceName == newDeviceName {
		return nil
	}

	rebound := make([]Session, 0)
	for id, session := range sm.sessions {
		if session.DeviceName != oldDeviceName {
			continue
		}
		session.DeviceName = newDeviceName
		sm.sessions[id] = session
		rebound = append(rebound, session)
	}
	return rebound
}

func (sm *SessionManager) ReapExpired() (transitioned []Session, expired []Session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.ttl <= 0 {
		return nil, nil
	}

	now := sm.now().UTC()
	for id, session := range sm.sessions {
		if session.OwnershipState == "" {
			session.OwnershipState = SessionOwnershipAttached
		}

		elapsed := now.Sub(session.LastHeartbeat)
		if elapsed > sm.ttl && session.OwnershipState != SessionOwnershipRecovering {
			session.OwnershipState = SessionOwnershipRecovering
			staleAt := now
			session.StaleSince = &staleAt
			sm.sessions[id] = session
			transitioned = append(transitioned, session)
			continue
		}

		if session.OwnershipState == SessionOwnershipRecovering && session.StaleSince != nil && sm.maxStale > 0 && now.Sub(*session.StaleSince) > sm.maxStale {
			delete(sm.sessions, id)
			expired = append(expired, session)
		}
	}
	return transitioned, expired
}
