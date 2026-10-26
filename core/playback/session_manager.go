package playback

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
)

var (
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionOwnership = errors.New("session owned by another client")
	ErrHandoffInvalid   = errors.New("invalid or expired handoff token")
	defaultSessionTTL   = 45 * time.Second
	defaultHandoffTTL   = conf.DefaultJukeboxHandoffTTL
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
	Takeover   bool
}

type Session struct {
	SessionID                string     `json:"sessionId"`
	User                     string     `json:"user"`
	OwnerClientID            string     `json:"ownerClientId"`
	DeviceName               string     `json:"deviceName"`
	LastHeartbeat            time.Time  `json:"lastHeartbeat"`
	OwnershipState           string     `json:"ownershipState"`
	StaleSince               *time.Time `json:"staleSince,omitempty"`
	BindingGeneration        uint64     `json:"bindingGeneration"`
	HandoffTokenHash         string     `json:"-"`
	HandoffTokenExpiry       time.Time  `json:"-"`
	OperationFenceGeneration uint64     `json:"-"`
}

type SessionStatus struct {
	SessionID           string     `json:"sessionId"`
	DeviceName          string     `json:"deviceName"`
	OwnerClientID       string     `json:"ownerClientId"`
	CurrentIndex        int        `json:"currentIndex"`
	TrackID             string     `json:"trackId"`
	Playing             bool       `json:"playing"`
	Position            int        `json:"position"`
	Gain                float32    `json:"gain"`
	Muted               bool       `json:"muted"`
	DesiredGain         float32    `json:"desiredGain"`
	Attached            bool       `json:"attached"`
	OwnershipState      string     `json:"ownershipState"`
	TerminationReason   string     `json:"terminationReason,omitempty"`
	QueueVersion        int        `json:"queueVersion"`
	LastHeartbeat       time.Time  `json:"lastHeartbeat"`
	StaleSince          *time.Time `json:"staleSince,omitempty"`
	BindingGeneration   uint64     `json:"bindingGeneration"`
	ControlHandoffToken string     `json:"controlHandoffToken,omitempty"`
	OperationIDFormat   string     `json:"operationIdFormat"`
	RequiresNewGeneration bool    `json:"requiresNewGeneration"`
}

type HandoffRequest struct {
	SessionID    string
	User         string
	NewClientID  string
	HandoffToken string
}

func generateHandoffToken() (string, string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(bytes)
	h := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(h[:])
	return token, hash, nil
}

type SessionManager struct {
	mu         sync.RWMutex
	ttl        time.Duration
	maxStale   time.Duration
	handoffTTL time.Duration
	now        func() time.Time
	sessions   map[string]Session
}

func NewSessionManager(ttl time.Duration, maxStale ...time.Duration) *SessionManager {
	sessionMaxStale := defaultSessionMaxStale
	if len(maxStale) > 0 {
		sessionMaxStale = maxStale[0]
	}
	handoffTTL := conf.Server.Jukebox.HandoffTTL
	if handoffTTL <= 0 {
		handoffTTL = defaultHandoffTTL
	}
	return &SessionManager{
		ttl:        ttl,
		maxStale:   sessionMaxStale,
		handoffTTL: handoffTTL,
		now:        time.Now,
		sessions:   make(map[string]Session),
	}
}

func (sm *SessionManager) Attach(req AttachRequest) (Session, string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	generation := uint64(1)
	fenceGeneration := uint64(0)
	if previous, ok := sm.sessions[req.SessionID]; ok {
		fenceGeneration = previous.OperationFenceGeneration
		if req.Takeover {
			// Explicit takeover establishes a new write generation, even when the
			// existing owner explicitly renews authority after history loss.
			generation = max(1, previous.BindingGeneration) + 1
		} else if previous.OwnerClientID == req.ClientID {
			generation = max(1, previous.BindingGeneration)
		}
	}

	token, hash, _ := generateHandoffToken()
	now := sm.now().UTC()

	session := Session{
		SessionID:                req.SessionID,
		User:                     req.User,
		OwnerClientID:            req.ClientID,
		DeviceName:               req.DeviceName,
		LastHeartbeat:            now,
		OwnershipState:           SessionOwnershipAttached,
		BindingGeneration:        generation,
		OperationFenceGeneration: fenceGeneration,
		HandoffTokenHash:         hash,
		HandoffTokenExpiry:       now.Add(sm.handoffTTL),
	}
	sm.sessions[req.SessionID] = session
	return session, token
}

func (sm *SessionManager) Heartbeat(sessionID, clientID string) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return "", ErrSessionNotFound
	}
	if session.OwnerClientID != clientID {
		return "", ErrSessionOwnership
	}

	token, hash, _ := generateHandoffToken()
	now := sm.now().UTC()

	session.LastHeartbeat = now
	session.OwnershipState = SessionOwnershipAttached
	session.StaleSince = nil
	session.HandoffTokenHash = hash
	session.HandoffTokenExpiry = now.Add(sm.handoffTTL)
	sm.sessions[sessionID] = session
	return token, nil
}

func (sm *SessionManager) Handoff(req HandoffRequest) (Session, string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[req.SessionID]
	if !ok {
		return Session{}, "", ErrSessionNotFound
	}
	if req.User != "" && session.User != req.User {
		return Session{}, "", ErrSessionOwnership
	}
	if req.HandoffToken == "" || session.HandoffTokenHash == "" {
		return Session{}, "", ErrHandoffInvalid
	}
	now := sm.now().UTC()
	if now.After(session.HandoffTokenExpiry) {
		session.HandoffTokenHash = ""
		sm.sessions[req.SessionID] = session
		return Session{}, "", ErrHandoffInvalid
	}

	h := sha256.Sum256([]byte(req.HandoffToken))
	computedHash := hex.EncodeToString(h[:])
	if subtle.ConstantTimeCompare([]byte(session.HandoffTokenHash), []byte(computedHash)) != 1 {
		return Session{}, "", ErrHandoffInvalid
	}

	// Single-use: consumed atomically
	session.HandoffTokenHash = ""

	// Issue a new token for the new document owner
	newToken, newHash, _ := generateHandoffToken()

	session.OwnerClientID = req.NewClientID
	session.BindingGeneration++
	session.LastHeartbeat = now
	session.OwnershipState = SessionOwnershipAttached
	session.StaleSince = nil
	session.HandoffTokenHash = newHash
	session.HandoffTokenExpiry = now.Add(sm.handoffTTL)
	sm.sessions[req.SessionID] = session

	return session, newToken, nil
}

func (sm *SessionManager) Update(session Session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if previous, ok := sm.sessions[session.SessionID]; ok {
		session.OperationFenceGeneration = max(session.OperationFenceGeneration, previous.OperationFenceGeneration)
	}
	sm.sessions[session.SessionID] = session
}

func (sm *SessionManager) FenceOperations(sessionID string, generation uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if session, ok := sm.sessions[sessionID]; ok {
		session.OperationFenceGeneration = max(session.OperationFenceGeneration, generation)
		sm.sessions[sessionID] = session
	}
}

func (sm *SessionManager) RevokeDeviceOwners(deviceName, exceptSessionID string) []Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	var revoked []Session
	for id, session := range sm.sessions {
		if id == exceptSessionID || session.DeviceName != deviceName || session.OwnerClientID == "" {
			continue
		}
		session.OwnerClientID = ""
		session.BindingGeneration++
		session.OwnershipState = SessionOwnershipDetached
		session.HandoffTokenHash = ""
		session.HandoffTokenExpiry = time.Time{}
		sm.sessions[id] = session
		revoked = append(revoked, session)
	}
	return revoked
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

func (sm *SessionManager) HasUserSession(user string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, session := range sm.sessions {
		if session.User == user {
			return true
		}
	}
	return false
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
		session.BindingGeneration++
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
