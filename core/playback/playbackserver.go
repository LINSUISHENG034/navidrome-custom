// Package playback implements audio playback using PlaybackDevices. It is used to implement the Jukebox mode in turn.
// It makes use of the MPV library to do the playback. Major parts are:
// - decoder which includes decoding and transcoding of various audio file formats
// - device implementing the basic functions to work with audio devices like set, play, stop, skip, ...
// - queue a simple playlist
package playback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/playback/bluetooth"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	serverevents "github.com/navidrome/navidrome/server/events"
	"github.com/navidrome/navidrome/utils/singleton"
)

type PlaybackServer interface {
	Run(ctx context.Context) error
	GetDeviceForUser(user string) (*playbackDevice, error)
	GetMediaFile(id string) (*model.MediaFile, error)
	ListDevices() []DeviceInfo
	SwitchDevice(ctx context.Context, deviceName string) error
	RefreshDevices(ctx context.Context) error
	AttachSession(ctx context.Context, req AttachRequest) (SessionStatus, error)
	HeartbeatSession(ctx context.Context, sessionID, clientID string) (SessionStatus, error)
	DetachSession(ctx context.Context, sessionID, clientID string) (SessionStatus, error)
	HandoffSession(ctx context.Context, req HandoffRequest) (SessionStatus, error)
	SessionStatus(ctx context.Context, sessionID string) (SessionStatus, error)
	SessionSnapshot(ctx context.Context, sessionID, user string) (SessionSnapshot, error)
	ExecuteCommand(ctx context.Context, scope CommandScope, command PlaybackCommand) (DeviceStatus, error)
	SwitchDeviceForCommand(ctx context.Context, scope CommandScope, deviceName string) error
	ReserveMigration(ctx context.Context, req ReserveMigrationRequest) (CommandResult, error)
	ReleaseMigrationReservation(ctx context.Context, req ReleaseMigrationRequest) (CommandResult, error)
	QueryReservation(ctx context.Context, migrationID string) (ReservationInfo, error)
	CommitMigration(ctx context.Context, req CommitMigrationRequest) (SessionStatus, error)
	QueryOperation(ctx context.Context, sessionID, operationID string) (OperationRecord, error)
}

var (
	ErrCommandScope             = errors.New("sessionId and clientId must be supplied together")
	ErrDeviceNotFound           = errors.New("device not found")
	ErrGenerationRequired       = errors.New("bindingGeneration is required")
	ErrCommandSuperseded        = errors.New("command superseded by newer generation")
	ErrProtectedContextConflict = fmt.Errorf("%w: cannot issue legacy writes to protected playback context", ErrSessionOwnership)
	ErrMigrationConflict        = errors.New("target device is reserved by another migration")
	ErrReservationNotFound      = errors.New("migration reservation not found or not active")
	ErrOperationRequired        = errors.New("operationId is required")
	ErrOperationIdentity        = errors.New("operationId must be g<bindingGeneration>:<stable ID>")
	ErrOperationUnknown         = errors.New("operation outcome is unknown")
	ErrOperationCapacity        = errors.New("operation execution capacity exceeded")
)

type TargetOccupiedError struct {
	SceneFingerprint string `json:"sceneFingerprint"`
	QueueCount       int    `json:"queueCount"`
}

func (e *TargetOccupiedError) Error() string {
	return fmt.Sprintf("target device is occupied (fingerprint: %s, queueCount: %d)", e.SceneFingerprint, e.QueueCount)
}

type CommandScope struct {
	User              string  `json:"-"`
	SessionID         string  `json:"sessionId,omitempty"`
	ClientID          string  `json:"clientId,omitempty"`
	BindingGeneration *uint64 `json:"bindingGeneration,omitempty"`
	OperationID       string  `json:"operationId,omitempty"`
}

type PlaybackCommand struct {
	Action   string
	IDs      []string
	Index    int
	Offset   int
	Position int
	From     int
	To       int
	Gain     float32
	Muted    bool
}

const (
	CommandSet     = "set"
	CommandStart   = "start"
	CommandPause   = "pause"
	CommandStop    = "stop"
	CommandSkip    = "skip"
	CommandSeek    = "seek"
	CommandAdd     = "add"
	CommandInsert  = "insert"
	CommandClear   = "clear"
	CommandRemove  = "remove"
	CommandMove    = "move"
	CommandShuffle = "shuffle"
	CommandGain    = "gain"
	CommandMute    = "mute"
)

// DeviceInfo represents an audio output device exposed via the API.
type DeviceInfo struct {
	Name        string `json:"name"`
	DeviceName  string `json:"deviceName"`
	IsDefault   bool   `json:"isDefault"`
	IsBluetooth bool   `json:"isBluetooth"`
	Connected   bool   `json:"connected"`
}

type SessionSnapshot struct {
	SessionStatus   SessionStatus           `json:"sessionStatus"`
	JukeboxPlaylist JukeboxPlaylistSnapshot `json:"jukeboxPlaylist"`
}

type JukeboxPlaylistSnapshot struct {
	CurrentIndex int                    `json:"currentIndex"`
	Playing      bool                   `json:"playing"`
	Position     int                    `json:"position"`
	Gain         float32                `json:"gain"`
	Muted        bool                   `json:"muted"`
	Entry        []JukeboxEntrySnapshot `json:"entry"`
}

type JukeboxEntrySnapshot struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Duration float32 `json:"duration"`
}

const (
	OperationStatusApplied     = "APPLIED"
	OperationStatusNotExecuted = "NOT_EXECUTED"
	OperationStatusFailed      = "FAILED"
	OperationStatusUnknown     = "UNKNOWN"
	OperationIDFormat          = "g<bindingGeneration>:<stable ID>"
)

type OperationRecord struct {
	Status            string         `json:"status"`
	Action            string         `json:"action,omitempty"`
	SessionID         string         `json:"sessionId"`
	OperationID       string         `json:"operationId"`
	BindingGeneration uint64         `json:"bindingGeneration,omitempty"`
	DeviceStatus      *DeviceStatus  `json:"deviceStatus,omitempty"`
	SessionStatus     *SessionStatus `json:"sessionStatus,omitempty"`
	CommandResult     *CommandResult `json:"commandResult,omitempty"`
	ErrorCode         string         `json:"errorCode,omitempty"`
	ErrorMessage      string         `json:"errorMessage,omitempty"`
	CreatedAt         time.Time      `json:"createdAt"`
	user              string
	clientID          string
	requestHash       string
	err               error
}

type OperationHistoryError struct {
	BindingGeneration uint64
}

func (e *OperationHistoryError) Error() string {
	return "operation history is indeterminate; a new binding generation is required"
}
func (e *OperationHistoryError) Unwrap() error { return ErrOperationUnknown }

type OperationExecutionUnknownError struct {
	BindingGeneration uint64
	Cause             error
}

func (e *OperationExecutionUnknownError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return ErrOperationUnknown.Error()
}
func (e *OperationExecutionUnknownError) Unwrap() error { return ErrOperationUnknown }
func (e *OperationExecutionUnknownError) Is(target error) bool {
	return target == ErrOperationUnknown || errors.Is(e.Cause, target)
}

type operationPending struct {
	done   chan struct{}
	record OperationRecord
}

type operationAdmission struct {
	record  *OperationRecord
	pending *operationPending
	first   bool
	retired []OperationRecord
	err     error
}

type operationKey struct {
	sessionID   string
	operationID string
}

type OperationStore struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	order    []operationKey
	records  map[operationKey]OperationRecord
	inflight map[operationKey]*operationPending
}

func NewOperationStore(capacity int, ttl time.Duration, now ...func() time.Time) *OperationStore {
	nowFn := time.Now
	if len(now) > 0 && now[0] != nil {
		nowFn = now[0]
	}
	if capacity <= 0 {
		capacity = conf.DefaultJukeboxOperationCapacity
	}
	if ttl <= 0 {
		ttl = conf.DefaultJukeboxOperationTTL
	}
	return &OperationStore{
		capacity: capacity,
		ttl:      ttl,
		now:      nowFn,
		records:  make(map[operationKey]OperationRecord),
		inflight: make(map[operationKey]*operationPending),
	}
}

func (s *OperationStore) pruneLocked() []OperationRecord {
	var retired []OperationRecord
	kept := s.order[:0]
	for _, key := range s.order {
		rec := s.records[key]
		if s.now().Sub(rec.CreatedAt) >= s.ttl {
			retired = append(retired, rec)
			delete(s.records, key)
		} else {
			kept = append(kept, key)
		}
	}
	s.order = kept
	return retired
}

func (s *OperationStore) prune() []OperationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked()
}

func (s *OperationStore) lookup(sessionID, operationID string) (OperationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[operationKey{sessionID: sessionID, operationID: operationID}]
	if ok && s.now().Sub(rec.CreatedAt) >= s.ttl {
		return OperationRecord{Status: OperationStatusUnknown, SessionID: sessionID, OperationID: operationID}, false
	}
	if !ok {
		rec = OperationRecord{Status: OperationStatusUnknown, SessionID: sessionID, OperationID: operationID}
	}
	return rec, ok
}

func (s *OperationStore) Get(sessionID, operationID string) (OperationRecord, bool) {
	return s.lookup(sessionID, operationID)
}

// acquire closes the lookup/admission race and shares one completion signal for
// all retries. Retired records are returned for processing after releasing mu.
func (s *OperationStore) acquire(initial OperationRecord) operationAdmission {
	s.mu.Lock()
	defer s.mu.Unlock()
	admission := operationAdmission{retired: s.pruneLocked()}
	key := operationKey{sessionID: initial.SessionID, operationID: initial.OperationID}
	if rec, ok := s.records[key]; ok {
		admission.record = &rec
		return admission
	}
	if pending, ok := s.inflight[key]; ok {
		admission.pending = pending
		return admission
	}
	if len(s.inflight) >= s.capacity {
		admission.err = ErrOperationCapacity
		return admission
	}
	admission.first = true
	admission.pending = &operationPending{done: make(chan struct{}), record: initial}
	s.inflight[key] = admission.pending
	return admission
}

func (s *OperationStore) abandon(sessionID, operationID string, rec OperationRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := operationKey{sessionID: sessionID, operationID: operationID}
	if pending, ok := s.inflight[key]; ok {
		pending.record = rec
		delete(s.inflight, key)
		close(pending.done)
	}
}

func (s *OperationStore) complete(sessionID, operationID string, rec OperationRecord) []OperationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	retired := s.pruneLocked()
	key := operationKey{sessionID: sessionID, operationID: operationID}
	rec.CreatedAt = s.now().UTC()
	rec.SessionID = sessionID
	rec.OperationID = operationID
	if _, exists := s.records[key]; !exists {
		if len(s.order) >= s.capacity {
			oldest := s.order[0]
			s.order = s.order[1:]
			retired = append(retired, s.records[oldest])
			delete(s.records, oldest)
		}
		s.order = append(s.order, key)
	}
	s.records[key] = rec
	if pending, ok := s.inflight[key]; ok {
		pending.record = rec
		delete(s.inflight, key)
		close(pending.done)
	}
	return retired
}

func (s *OperationStore) Complete(sessionID, operationID string, rec OperationRecord) []OperationRecord {
	return s.complete(sessionID, operationID, rec)
}

type ReservationStatus string

const (
	ReservationActive   ReservationStatus = "ACTIVE"
	ReservationReleased ReservationStatus = "RELEASED"
	ReservationExpired  ReservationStatus = "EXPIRED"
	ReservationNotFound ReservationStatus = "NOT_FOUND"
)

type MigrationReservation struct {
	MigrationID         string            `json:"migrationId"`
	DeviceName          string            `json:"deviceName"`
	SessionID           string            `json:"sessionId"`
	ClientID            string            `json:"clientId"`
	User                string            `json:"user"`
	Status              ReservationStatus `json:"status"`
	ExpectedFingerprint string            `json:"expectedFingerprint"`
	BindingGeneration   uint64            `json:"bindingGeneration"`
	CreatedAt           time.Time         `json:"createdAt"`
	ExpiresAt           time.Time         `json:"expiresAt"`
}

type ReserveMigrationRequest struct {
	MigrationID         string  `json:"migrationId"`
	OperationID         string  `json:"operationId"`
	BindingGeneration   *uint64 `json:"bindingGeneration,omitempty"`
	SessionID           string  `json:"sessionId"`
	ClientID            string  `json:"clientId"`
	User                string  `json:"user"`
	DeviceName          string  `json:"deviceName"`
	ForceTakeover       bool    `json:"forceTakeover"`
	ExpectedFingerprint string  `json:"expectedFingerprint,omitempty"`
}

type ReleaseMigrationRequest struct {
	MigrationID       string  `json:"migrationId"`
	OperationID       string  `json:"operationId"`
	SessionID         string  `json:"sessionId"`
	ClientID          string  `json:"clientId"`
	User              string  `json:"-"`
	BindingGeneration *uint64 `json:"bindingGeneration,omitempty"`
}

type CommitMigrationRequest struct {
	MigrationID         string   `json:"migrationId"`
	OperationID         string   `json:"operationId"`
	SessionID           string   `json:"sessionId"`
	ClientID            string   `json:"clientId"`
	User                string   `json:"user"`
	TargetDeviceName    string   `json:"targetDeviceName"`
	ExpectedGeneration  uint64   `json:"expectedGeneration"`
	BindingGeneration   *uint64  `json:"bindingGeneration,omitempty"`
	ExpectedFingerprint string   `json:"expectedFingerprint"`
	Queue               []string `json:"queue,omitempty"`
	CurrentIndex        int      `json:"currentIndex,omitempty"`
	Position            *int     `json:"position,omitempty"`
	Gain                *float32 `json:"gain,omitempty"`
	Muted               *bool    `json:"muted,omitempty"`
}

type CommandResult struct {
	Status      string `json:"status"`
	MigrationID string `json:"migrationId,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type ReservationInfo struct {
	Status              string    `json:"status"`
	MigrationID         string    `json:"migrationId,omitempty"`
	DeviceName          string    `json:"deviceName,omitempty"`
	SessionID           string    `json:"sessionId,omitempty"`
	ClientID            string    `json:"clientId,omitempty"`
	ExpectedFingerprint string    `json:"expectedFingerprint,omitempty"`
	CreatedAt           time.Time `json:"createdAt,omitempty"`
	ExpiresAt           time.Time `json:"expiresAt,omitempty"`
}

type playbackServer struct {
	mu                  sync.Mutex
	ctx                 *context.Context
	datastore           model.DataStore
	sessionManager      *SessionManager
	eventBroker         serverevents.Broker
	onDeviceStateChange func(*playbackDevice, DeviceStatus)
	playbackDevices     []*playbackDevice
	shuttingDown        bool
	operationStore      *OperationStore
	reservations        map[string]MigrationReservation
	now                 func() time.Time
}

var discoverAllSinks = bluetooth.DiscoverSinks

func (ps *playbackServer) newPlaybackDevice(ctx context.Context, name string, deviceName string) *playbackDevice {
	device := NewPlaybackDevice(ctx, ps, name, deviceName)
	device.onStateChange = func(status DeviceStatus) {
		if ps.onDeviceStateChange != nil {
			ps.onDeviceStateChange(device, status)
		}
		ps.publishJukeboxStateUpdates(device)
	}
	return device
}

func (ps *playbackServer) getEventBroker() serverevents.Broker {
	if ps.eventBroker != nil {
		return ps.eventBroker
	}
	return serverevents.GetBroker()
}

func (ps *playbackServer) publishJukeboxStateUpdates(device *playbackDevice) {
	if ps.sessionManager == nil || device == nil {
		return
	}
	for _, session := range ps.sessionManager.FindByDevice(device.DeviceName) {
		if session.User == "" {
			continue
		}
		status := ps.statusFromSession(session)
		ps.getEventBroker().SendMessage(jukeboxTargetContext(session.User), NewJukeboxStateUpdatedEvent(status))
	}
}

func (ps *playbackServer) reapExpiredSessions() {
	ps.mu.Lock()
	sessionManager := ps.sessionManager
	if sessionManager == nil {
		ps.mu.Unlock()
		return
	}
	transitioned, expired := sessionManager.ReapExpired()
	ps.mu.Unlock()

	for _, session := range transitioned {
		if session.User == "" {
			continue
		}
		status := ps.statusFromSession(session)
		ps.getEventBroker().SendMessage(jukeboxTargetContext(session.User), NewJukeboxStateUpdatedEvent(status))
	}
	for _, session := range expired {
		if session.User == "" {
			continue
		}
		status := ps.statusFromSession(session)
		status.Attached = false
		status.OwnershipState = SessionOwnershipDetached
		status.TerminationReason = SessionTerminationStaleExpired
		ps.getEventBroker().SendMessage(jukeboxTargetContext(session.User), NewJukeboxStateUpdatedEvent(status))
	}
}

func (ps *playbackServer) monitorSessionExpiry(ctx context.Context) {
	ticker := time.NewTicker(sessionReapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ps.reapExpiredSessions()
		}
	}
}

func (ps *playbackServer) getNow() time.Time {
	if ps.now != nil {
		return ps.now().UTC()
	}
	return time.Now().UTC()
}

func (ps *playbackServer) ensureOperationStoreLocked() *OperationStore {
	if ps.operationStore == nil {
		ps.operationStore = NewOperationStore(conf.Server.Jukebox.OperationCapacity, conf.Server.Jukebox.OperationTTL, ps.now)
	}
	return ps.operationStore
}

func (ps *playbackServer) ensureReservationsLocked() map[string]MigrationReservation {
	if ps.reservations == nil {
		ps.reservations = make(map[string]MigrationReservation)
	}
	return ps.reservations
}

func (ps *playbackServer) reservationLocked(migrationID string) (MigrationReservation, bool) {
	reservation, ok := ps.ensureReservationsLocked()[migrationID]
	if ok && reservation.Status == ReservationActive && !ps.getNow().Before(reservation.ExpiresAt) {
		reservation.Status = ReservationExpired
		ps.reservations[migrationID] = reservation
	}
	return reservation, ok
}

func (ps *playbackServer) ensureSessionManagerLocked() *SessionManager {
	if ps.sessionManager == nil {
		ps.sessionManager = NewSessionManager(defaultSessionTTL)
		if conf.Server.Jukebox.HandoffTTL > 0 {
			ps.sessionManager.handoffTTL = conf.Server.Jukebox.HandoffTTL
		}
		if ps.now != nil {
			ps.sessionManager.now = ps.now
		}
	}
	return ps.sessionManager
}

func (ps *playbackServer) ensureSessionManager() *SessionManager {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.ensureSessionManagerLocked()
}

func (ps *playbackServer) getDeviceByName(deviceName string) *playbackDevice {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.deviceByNameLocked(deviceName)
}

func (ps *playbackServer) deviceByNameLocked(deviceName string) *playbackDevice {
	for _, device := range ps.playbackDevices {
		if device.DeviceName == deviceName {
			return device
		}
	}
	return nil
}

func sessionUserAllowed(ctx context.Context, session Session) bool {
	user, authenticated := request.UserFrom(ctx)
	return !authenticated || user.UserName == session.User
}

func (ps *playbackServer) statusFromSession(session Session) SessionStatus {
	device := ps.getDeviceByName(session.DeviceName)
	if device == nil {
		return newSessionStatus(session)
	}

	device.mu.Lock()
	defer device.mu.Unlock()
	return sessionStatusFromDevice(session, device)
}

func newSessionStatus(session Session) SessionStatus {
	status := SessionStatus{
		SessionID:         session.SessionID,
		DeviceName:        session.DeviceName,
		OwnerClientID:     session.OwnerClientID,
		Attached:          true,
		OwnershipState:    session.OwnershipState,
		LastHeartbeat:     session.LastHeartbeat,
		StaleSince:        session.StaleSince,
		BindingGeneration: session.BindingGeneration,
		OperationIDFormat: OperationIDFormat,
		RequiresNewGeneration: session.OperationFenceGeneration >= session.BindingGeneration,
	}
	if status.OwnershipState == "" {
		status.OwnershipState = SessionOwnershipAttached
	}
	if session.StaleSince != nil {
		staleSince := *session.StaleSince
		status.StaleSince = &staleSince
	}
	return status
}

// sessionStatusFromDevice copies playback state while the device mutex is held.
func sessionStatusFromDevice(session Session, device *playbackDevice) SessionStatus {
	status := newSessionStatus(session)
	deviceStatus := device.getStatus()
	status.CurrentIndex = deviceStatus.CurrentIndex
	status.Playing = deviceStatus.Playing
	status.Position = deviceStatus.Position
	status.Gain = deviceStatus.Gain
	status.Muted = deviceStatus.Muted
	status.DesiredGain = device.desiredGainLocked()
	status.QueueVersion = device.queueVersion
	if current := device.PlaybackQueue.Current(); current != nil {
		status.TrackID = current.ID
	}
	return status
}

func (ps *playbackServer) AttachSession(ctx context.Context, req AttachRequest) (SessionStatus, error) {
	if req.DeviceName == "" && req.User != "" {
		device, err := ps.GetDeviceForUser(req.User)
		if err != nil {
			return SessionStatus{}, err
		}
		req.DeviceName = device.DeviceName
	}

	sessionManager := ps.ensureSessionManager()
	ps.mu.Lock()
	if ps.shuttingDown {
		ps.mu.Unlock()
		return SessionStatus{}, context.Canceled
	}
	if user, ok := request.UserFrom(ctx); ok && user.UserName != req.User {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}
	if previous, ok := sessionManager.Get(req.SessionID); ok {
		if previous.User != req.User || previous.DeviceName != req.DeviceName ||
			(previous.OwnerClientID != req.ClientID && !req.Takeover) {
			ps.mu.Unlock()
			return SessionStatus{}, ErrSessionOwnership
		}
	}
	device := ps.deviceByNameLocked(req.DeviceName)
	if device == nil {
		ps.mu.Unlock()
		return SessionStatus{}, fmt.Errorf("%w: %s", ErrDeviceNotFound, req.DeviceName)
	}
	for _, attached := range sessionManager.FindByDevice(req.DeviceName) {
		if attached.SessionID != req.SessionID && attached.OwnerClientID != "" {
			ps.mu.Unlock()
			return SessionStatus{}, ErrSessionOwnership
		}
	}
	// Session ownership and binding cannot change between command admission and
	// its first state mutation. Supersede pending work when the owner changes.
	device.mu.Lock()
	if previous, ok := sessionManager.Get(req.SessionID); !ok || previous.OwnerClientID != req.ClientID {
		device.cancelPreparationLocked()
	}
	previous, existed := sessionManager.Get(req.SessionID)
	session, token := sessionManager.Attach(req)
	if !existed || previous.OwnerClientID != session.OwnerClientID || previous.BindingGeneration != session.BindingGeneration {
		device.incrementSceneRevisionLocked()
	}
	device.mu.Unlock()
	ps.mu.Unlock()
	status := ps.statusFromSession(session)
	status.ControlHandoffToken = token
	return status, nil
}

func (ps *playbackServer) HeartbeatSession(ctx context.Context, sessionID, clientID string) (SessionStatus, error) {
	sessionManager := ps.ensureSessionManager()
	ps.mu.Lock()
	if session, ok := sessionManager.Get(sessionID); ok && !sessionUserAllowed(ctx, session) {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}
	token, err := sessionManager.Heartbeat(sessionID, clientID)
	ps.mu.Unlock()
	if err != nil {
		return SessionStatus{}, err
	}
	status, err := ps.SessionStatus(ctx, sessionID)
	if err != nil {
		return SessionStatus{}, err
	}
	status.ControlHandoffToken = token
	return status, nil
}

func (ps *playbackServer) HandoffSession(ctx context.Context, req HandoffRequest) (SessionStatus, error) {
	sessionManager := ps.ensureSessionManager()
	ps.mu.Lock()
	if ps.shuttingDown {
		ps.mu.Unlock()
		return SessionStatus{}, context.Canceled
	}
	if user, ok := request.UserFrom(ctx); ok && req.User != "" && user.UserName != req.User {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}
	previous, exists := sessionManager.Get(req.SessionID)
	var device *playbackDevice
	if exists {
		device = ps.deviceByNameLocked(previous.DeviceName)
		if device != nil {
			device.mu.Lock()
		}
	}
	session, token, err := sessionManager.Handoff(req)
	if device != nil {
		if err == nil {
			device.incrementSceneRevisionLocked()
		}
		device.mu.Unlock()
	}
	ps.mu.Unlock()
	if err != nil {
		return SessionStatus{}, err
	}
	status := ps.statusFromSession(session)
	status.ControlHandoffToken = token
	return status, nil
}

func (ps *playbackServer) DetachSession(ctx context.Context, sessionID, clientID string) (SessionStatus, error) {
	sessionManager := ps.ensureSessionManager()
	ps.mu.Lock()
	previous, exists := sessionManager.Get(sessionID)
	if exists && !sessionUserAllowed(ctx, previous) {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}
	var device *playbackDevice
	if exists && previous.OwnerClientID == clientID {
		device = ps.deviceByNameLocked(previous.DeviceName)
		if device != nil {
			device.mu.Lock()
			device.cancelPreparationLocked()
		}
	}
	session, err := sessionManager.DetachSnapshot(sessionID, clientID)
	if device != nil {
		if err == nil {
			device.incrementSceneRevisionLocked()
		}
		device.mu.Unlock()
	}
	ps.mu.Unlock()
	if err != nil {
		return SessionStatus{}, err
	}
	status := ps.statusFromSession(session)
	status.Attached = false
	status.OwnershipState = SessionOwnershipDetached
	status.TerminationReason = SessionTerminationUserDetached
	return status, nil
}

func (ps *playbackServer) SessionStatus(ctx context.Context, sessionID string) (SessionStatus, error) {
	session, ok := ps.ensureSessionManager().Get(sessionID)
	if !ok {
		return SessionStatus{}, ErrSessionNotFound
	}
	if !sessionUserAllowed(ctx, session) {
		return SessionStatus{}, ErrSessionOwnership
	}
	return ps.statusFromSession(session), nil
}

func (ps *playbackServer) SessionSnapshot(ctx context.Context, sessionID, user string) (SessionSnapshot, error) {
	sessionManager := ps.ensureSessionManager()
	ps.mu.Lock()
	session, ok := sessionManager.Get(sessionID)
	if !ok {
		ps.mu.Unlock()
		return SessionSnapshot{}, ErrSessionNotFound
	}
	if session.User != user || !sessionUserAllowed(ctx, session) {
		ps.mu.Unlock()
		return SessionSnapshot{}, ErrSessionOwnership
	}

	var device *playbackDevice
	for _, candidate := range ps.playbackDevices {
		if candidate.DeviceName == session.DeviceName {
			device = candidate
			break
		}
	}
	if device == nil {
		ps.mu.Unlock()
		status := newSessionStatus(session)
		return sessionSnapshotFromStatus(status, []JukeboxEntrySnapshot{}), nil
	}

	// Keep the device binding stable until its state lock is held.
	device.mu.Lock()
	ps.mu.Unlock()
	defer device.mu.Unlock()

	status := sessionStatusFromDevice(session, device)
	entries := make([]JukeboxEntrySnapshot, len(device.PlaybackQueue.Items))
	for i, item := range device.PlaybackQueue.Items {
		entries[i] = JukeboxEntrySnapshot{
			ID:       item.ID,
			Title:    item.Title,
			Artist:   item.Artist,
			Album:    item.Album,
			Duration: item.Duration,
		}
	}
	return sessionSnapshotFromStatus(status, entries), nil
}

func sessionSnapshotFromStatus(status SessionStatus, entries []JukeboxEntrySnapshot) SessionSnapshot {
	return SessionSnapshot{
		SessionStatus: status,
		JukeboxPlaylist: JukeboxPlaylistSnapshot{
			CurrentIndex: status.CurrentIndex,
			Playing:      status.Playing,
			Position:     status.Position,
			Gain:         status.Gain,
			Muted:        status.Muted,
			Entry:        entries,
		},
	}
}

// playbackDeviceContext returns the long-lived playback service context when
// available. This prevents devices discovered during short-lived HTTP requests
// from inheriting a canceled request context.
func (ps *playbackServer) playbackDeviceContext(fallback context.Context) context.Context {
	if ps.ctx != nil {
		return *ps.ctx
	}
	return fallback
}

// GetInstance returns the playback-server singleton
func GetInstance(ds model.DataStore) PlaybackServer {
	return singleton.GetInstance(func() *playbackServer {
		return &playbackServer{datastore: ds, sessionManager: NewSessionManager(defaultSessionTTL)}
	})
}

// Run starts the playback server which serves request until canceled using the given context
func (ps *playbackServer) Run(ctx context.Context) error {
	ps.ctx = &ctx

	devices, err := ps.initDeviceStatus(ctx, conf.Server.Jukebox.Devices, conf.Server.Jukebox.Default)
	if err != nil {
		return err
	}
	ps.mu.Lock()
	ps.playbackDevices = devices
	ps.shuttingDown = false
	ps.mu.Unlock()

	if conf.Server.Jukebox.AutoDiscoverBluetooth {
		ps.mergeBluetoothDevices(ctx)
	}

	ps.mu.Lock()
	log.Info(ctx, fmt.Sprintf("%d audio devices found", len(ps.playbackDevices)))
	ps.mu.Unlock()

	defaultDevice, _ := ps.getDefaultDevice()

	log.Info(ctx, "Using audio device: "+defaultDevice.DeviceName)

	if conf.Server.Jukebox.AutoDiscoverBluetooth {
		go ps.monitorBluetoothConnections(ctx)
	}
	go ps.monitorSessionExpiry(ctx)

	<-ctx.Done()

	ps.mu.Lock()
	ps.shuttingDown = true
	devices = append([]*playbackDevice(nil), ps.playbackDevices...)
	ps.mu.Unlock()
	for _, device := range devices {
		if err := device.retireServiceOutput(context.WithoutCancel(ctx)); err != nil {
			log.Warn(ctx, "Could not retire playback device during shutdown", "device", device.Name, err)
		}
	}
	// Cancel and close every device before joining any preparing factory. No
	// new preparation is admitted; completion includes actual late-output Close.
	for _, device := range devices {
		device.preparing.Wait()
	}
	return nil
}

const btMonitorInterval = 10 * time.Second
const sessionReapInterval = 15 * time.Second

// monitorBluetoothConnections periodically checks whether Bluetooth sinks are still
// available. If the active default device is a BT sink that has disappeared, playback
// is automatically paused.
func (ps *playbackServer) monitorBluetoothConnections(ctx context.Context) {
	ticker := time.NewTicker(btMonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ps.checkBluetoothConnections(ctx)
		}
	}
}

func (ps *playbackServer) checkBluetoothConnections(ctx context.Context) {
	activeSinks := make(map[string]bool)
	for _, sink := range discoverAllSinks(ctx) {
		activeSinks[sink.MPVDeviceName()] = true
	}

	var disconnected []*playbackDevice
	ps.mu.Lock()
	for idx := range ps.playbackDevices {
		d := ps.playbackDevices[idx]
		if !strings.HasPrefix(d.DeviceName, "pulse/bluez_") {
			continue
		}
		if _, connected := activeSinks[d.DeviceName]; !connected && d.isPlaying() {
			disconnected = append(disconnected, d)
		}
	}
	ps.mu.Unlock()

	for _, device := range disconnected {
		if _, err := device.Stop(ctx); err != nil {
			log.Warn(ctx, "Bluetooth device disconnect pause failed", "device", device.Name, "err", err)
			continue
		}
		log.Warn(ctx, "Bluetooth device disconnected, pausing playback", "device", device.Name)
	}
}

func (ps *playbackServer) initDeviceStatus(ctx context.Context, devices []conf.AudioDeviceDefinition, defaultDevice string) ([]*playbackDevice, error) {
	pbDevices := make([]*playbackDevice, max(1, len(devices)))
	defaultDeviceFound := false

	if defaultDevice == "" {
		// if there are no devices given and no default device, we create a synthetic device named "auto"
		if len(devices) == 0 {
			pbDevices[0] = ps.newPlaybackDevice(ctx, "auto", "auto")
		}

		// if there is but only one entry and no default given, just use that.
		if len(devices) == 1 {
			if len(devices[0]) != 2 {
				return []*playbackDevice{}, fmt.Errorf("audio device definition ought to contain 2 fields, found: %d ", len(devices[0]))
			}
			pbDevices[0] = ps.newPlaybackDevice(ctx, devices[0][0], devices[0][1])
		}

		if len(devices) > 1 {
			return []*playbackDevice{}, fmt.Errorf("number of audio device found is %d, but no default device defined. Set Jukebox.Default", len(devices))
		}

		pbDevices[0].Default = true
		return pbDevices, nil
	}

	for idx, audioDevice := range devices {
		if len(audioDevice) != 2 {
			return []*playbackDevice{}, fmt.Errorf("audio device definition ought to contain 2 fields, found: %d ", len(audioDevice))
		}

		pbDevices[idx] = ps.newPlaybackDevice(ctx, audioDevice[0], audioDevice[1])

		if audioDevice[0] == defaultDevice {
			pbDevices[idx].Default = true
			defaultDeviceFound = true
		}
	}

	if !defaultDeviceFound {
		return []*playbackDevice{}, fmt.Errorf("default device name not found: %s ", defaultDevice)
	}
	return pbDevices, nil
}

func (ps *playbackServer) getDefaultDeviceLocked() (*playbackDevice, error) {
	for idx := range ps.playbackDevices {
		if ps.playbackDevices[idx].Default {
			return ps.playbackDevices[idx], nil
		}
	}
	return nil, fmt.Errorf("no default device found")
}

func (ps *playbackServer) getDefaultDevice() (*playbackDevice, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.getDefaultDeviceLocked()
}

// GetMediaFile retrieves the MediaFile given by the id parameter
func (ps *playbackServer) GetMediaFile(id string) (*model.MediaFile, error) {
	if ps.datastore == nil || ps.ctx == nil {
		return nil, errors.New("no datastore")
	}
	return ps.datastore.MediaFile().Get(*ps.ctx, id)
}

// GetDeviceForUser returns the audio playback device for the given user. As of now this is but only the default device.
func (ps *playbackServer) GetDeviceForUser(user string) (*playbackDevice, error) {
	log.Debug("Processing GetDevice", "user", user)
	// README: here we might plug-in the user-device mapping one fine day
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.shuttingDown {
		return nil, context.Canceled
	}
	device, err := ps.getDefaultDeviceLocked()
	if err != nil {
		return nil, err
	}
	return device, nil
}

// authorizeCommandLocked resolves authority and target while ps.mu is held.
// Missing scope is legacy access, and cannot bypass a device's attached session.
func (ps *playbackServer) authorizeCommandLocked(ctx context.Context, scope CommandScope) (*playbackDevice, error) {
	if ps.shuttingDown || ctx.Err() != nil {
		return nil, context.Canceled
	}
	if user, ok := request.UserFrom(ctx); ok && user.UserName != scope.User {
		return nil, ErrSessionOwnership
	}
	if (scope.SessionID == "") != (scope.ClientID == "") {
		return nil, ErrCommandScope
	}
	if scope.SessionID == "" {
		device, err := ps.getDefaultDeviceLocked()
		if err != nil {
			return nil, err
		}
		if ps.sessionManager != nil && (ps.sessionManager.HasUserSession(scope.User) || len(ps.sessionManager.FindByDevice(device.DeviceName)) > 0) {
			return nil, ErrProtectedContextConflict
		}
		return device, nil
	}
	if ps.sessionManager == nil {
		return nil, ErrSessionNotFound
	}
	session, exists := ps.sessionManager.Get(scope.SessionID)
	if !exists {
		return nil, ErrSessionNotFound
	}
	if session.User != scope.User || session.OwnerClientID != scope.ClientID {
		return nil, ErrSessionOwnership
	}
	if scope.BindingGeneration == nil || *scope.BindingGeneration == 0 {
		return nil, ErrGenerationRequired
	}
	if *scope.BindingGeneration != session.BindingGeneration {
		return nil, ErrCommandSuperseded
	}
	device := ps.deviceByNameLocked(session.DeviceName)
	if device == nil {
		return nil, fmt.Errorf("%w: %s", ErrDeviceNotFound, session.DeviceName)
	}
	return device, nil
}

func operationRequestHash(action string, payload any) (string, error) {
	encoded, err := json.Marshal(struct {
		Action  string `json:"action"`
		Payload any    `json:"payload"`
	}{Action: action, Payload: payload})
	if err != nil {
		return "", fmt.Errorf("encode operation payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (ps *playbackServer) authorizeSessionLocked(ctx context.Context, scope CommandScope) (Session, error) {
	if scope.SessionID == "" || scope.ClientID == "" {
		return Session{}, ErrCommandScope
	}
	if ps.sessionManager == nil {
		return Session{}, ErrSessionNotFound
	}
	session, exists := ps.sessionManager.Get(scope.SessionID)
	if !exists {
		return Session{}, ErrSessionNotFound
	}
	if user, ok := request.UserFrom(ctx); ok && user.UserName != session.User {
		return Session{}, ErrSessionOwnership
	}
	if session.User != scope.User || session.OwnerClientID != scope.ClientID {
		return Session{}, ErrSessionOwnership
	}
	if scope.BindingGeneration == nil || *scope.BindingGeneration == 0 {
		return Session{}, ErrGenerationRequired
	}
	if *scope.BindingGeneration != session.BindingGeneration {
		return Session{}, ErrCommandSuperseded
	}
	return session, nil
}

func validateOperationIdentity(operationID string, generation uint64) error {
	if strings.TrimSpace(operationID) == "" {
		return ErrOperationRequired
	}
	prefix, suffix, ok := strings.Cut(operationID, ":")
	if ok && strings.HasPrefix(prefix, "g") && suffix != "" {
		encodedGeneration, err := strconv.ParseUint(strings.TrimPrefix(prefix, "g"), 10, 64)
		if err == nil && encodedGeneration != 0 && encodedGeneration != generation {
			return ErrCommandSuperseded
		}
	}
	return nil
}

func validateOperationRecordIdentity(record OperationRecord, expected OperationRecord) error {
	if record.user != expected.user || record.clientID != expected.clientID ||
		record.BindingGeneration != expected.BindingGeneration || record.Action != expected.Action ||
		record.requestHash != expected.requestHash {
		return ErrOperationIdentity
	}
	return nil
}

func operationErrorCode(err error) string {
	var occupied *TargetOccupiedError
	switch {
	case errors.As(err, &occupied):
		return "TARGET_OCCUPIED"
	case errors.Is(err, ErrMigrationConflict):
		return "MIGRATION_CONFLICT"
	case errors.Is(err, ErrReservationNotFound):
		return "RESERVATION_NOT_FOUND"
	case errors.Is(err, ErrOperationRequired):
		return "OPERATION_ID_REQUIRED"
	case errors.Is(err, ErrOperationIdentity):
		return "OPERATION_ID_INVALID"
	case errors.Is(err, ErrOperationCapacity):
		return "OPERATION_CAPACITY"
	case errors.Is(err, ErrOperationUnknown):
		return "OPERATION_UNKNOWN"
	case errors.Is(err, ErrCommandSuperseded):
		return "COMMAND_SUPERSEDED"
	case errors.Is(err, ErrSessionOwnership):
		return "SESSION_OWNERSHIP"
	default:
		return "COMMAND_FAILED"
	}
}

func operationRecordError(rec OperationRecord) error {
	if rec.err != nil {
		return rec.err
	}
	if rec.ErrorCode != "" {
		return errors.New(rec.ErrorMessage)
	}
	return nil
}

func isOperationAdmissionError(err error) bool {
	return errors.Is(err, ErrSessionOwnership) || errors.Is(err, ErrGenerationRequired) ||
		errors.Is(err, ErrCommandSuperseded) || errors.Is(err, ErrCommandScope) ||
		errors.Is(err, ErrOperationIdentity) || errors.Is(err, ErrOperationRequired)
}

// retireOperationsLocked applies history fences only after the store mutex has
// been released. ps.mu remains the outer lock, preserving ps -> session order.
func (ps *playbackServer) retireOperationsLocked(retired []OperationRecord) {
	for _, rec := range retired {
		if rec.SessionID != "" && rec.BindingGeneration > 0 {
			ps.ensureSessionManagerLocked().FenceOperations(rec.SessionID, rec.BindingGeneration)
		}
	}
}

func (ps *playbackServer) executeProtectedOperation(
	ctx context.Context,
	scope CommandScope,
	action string,
	payload any,
	execute func() (OperationRecord, error),
) (OperationRecord, error) {
	requestHash, err := operationRequestHash(action, payload)
	if err != nil {
		return OperationRecord{}, err
	}
	ps.mu.Lock()
	session, err := ps.authorizeSessionLocked(ctx, scope)
	if err != nil {
		ps.mu.Unlock()
		return OperationRecord{}, err
	}
	if err := validateOperationIdentity(scope.OperationID, session.BindingGeneration); err != nil {
		ps.mu.Unlock()
		return OperationRecord{}, err
	}
	if session.OperationFenceGeneration >= session.BindingGeneration {
		ps.mu.Unlock()
		return OperationRecord{}, &OperationHistoryError{BindingGeneration: session.BindingGeneration}
	}
	metadata := OperationRecord{
		Status: OperationStatusUnknown, Action: action, SessionID: scope.SessionID,
		OperationID: scope.OperationID, BindingGeneration: session.BindingGeneration,
		user: scope.User, clientID: scope.ClientID, requestHash: requestHash,
	}
	store := ps.ensureOperationStoreLocked()
	admission := store.acquire(metadata)
	ps.retireOperationsLocked(admission.retired)
	if admission.err != nil {
		ps.mu.Unlock()
		return OperationRecord{}, admission.err
	}
	if admission.record == nil {
		current, _ := ps.sessionManager.Get(session.SessionID)
		if current.OperationFenceGeneration >= session.BindingGeneration {
			historyErr := &OperationHistoryError{BindingGeneration: session.BindingGeneration}
			if admission.first {
				fenced := metadata
				fenced.err = historyErr
				fenced.ErrorCode = "OPERATION_UNKNOWN"
				fenced.ErrorMessage = historyErr.Error()
				store.abandon(scope.SessionID, scope.OperationID, fenced)
			}
			ps.mu.Unlock()
			return metadata, historyErr
		}
	}
	if admission.record != nil {
		rec := *admission.record
		ps.mu.Unlock()
		if err := validateOperationRecordIdentity(rec, metadata); err != nil {
			return OperationRecord{}, err
		}
		return rec, operationRecordError(rec)
	}
	pending := admission.pending
	if !admission.first {
		if err := validateOperationRecordIdentity(pending.record, metadata); err != nil {
			ps.mu.Unlock()
			return OperationRecord{}, err
		}
	}
	ps.mu.Unlock()

	if !admission.first {
		select {
		case <-pending.done:
			rec := pending.record
			return rec, operationRecordError(rec)
		case <-ctx.Done():
			return metadata, &OperationExecutionUnknownError{BindingGeneration: metadata.BindingGeneration, Cause: ctx.Err()}
		}
	}

	rec, executionErr := execute()
	rec.SessionID = scope.SessionID
	rec.OperationID = scope.OperationID
	rec.BindingGeneration = metadata.BindingGeneration
	rec.Action = action
	rec.user = scope.User
	rec.clientID = scope.ClientID
	rec.requestHash = requestHash
	if executionErr != nil {
		if isOperationAdmissionError(executionErr) {
			rec.Status = OperationStatusNotExecuted
			rec.err = executionErr
			rec.ErrorCode = operationErrorCode(executionErr)
			rec.ErrorMessage = executionErr.Error()
			ps.mu.Lock()
			store.abandon(scope.SessionID, scope.OperationID, rec)
			ps.mu.Unlock()
			return rec, executionErr
		}
		if errors.Is(executionErr, context.Canceled) || errors.Is(executionErr, context.DeadlineExceeded) {
			executionErr = &OperationExecutionUnknownError{BindingGeneration: metadata.BindingGeneration, Cause: executionErr}
		}
		rec.Status = OperationStatusFailed
		if errors.Is(executionErr, ErrOperationUnknown) {
			rec.Status = OperationStatusUnknown
		}
		rec.err = executionErr
		rec.ErrorCode = operationErrorCode(executionErr)
		rec.ErrorMessage = executionErr.Error()
	} else if rec.Status == "" || rec.Status == OperationStatusUnknown {
		rec.Status = OperationStatusApplied
	}

	ps.mu.Lock()
	retired := store.complete(scope.SessionID, scope.OperationID, rec)
	ps.retireOperationsLocked(retired)
	ps.mu.Unlock()
	return rec, executionErr
}

func (ps *playbackServer) ExecuteCommand(ctx context.Context, scope CommandScope, command PlaybackCommand) (DeviceStatus, error) {
	if scope.SessionID != "" {
		rec, err := ps.executeProtectedOperation(ctx, scope, command.Action, command, func() (OperationRecord, error) {
			ps.mu.Lock()
			device, err := ps.authorizeCommandLocked(ctx, scope)
			if err != nil {
				ps.mu.Unlock()
				return OperationRecord{}, err
			}
			// Acquire the state lock before releasing the authority/binding lock.
			device.mu.Lock()
			device.User = scope.User
			ps.mu.Unlock()
			status, err := device.finishCommandLocked(ctx, command)
			return OperationRecord{DeviceStatus: &status}, err
		})
		if rec.DeviceStatus != nil {
			return *rec.DeviceStatus, err
		}
		return DeviceStatus{}, err
	}

	ps.mu.Lock()
	device, err := ps.authorizeCommandLocked(ctx, scope)
	if err != nil {
		ps.mu.Unlock()
		return DeviceStatus{}, err
	}
	device.mu.Lock()
	device.User = scope.User
	ps.mu.Unlock()
	return device.finishCommandLocked(ctx, command)
}

func (ps *playbackServer) ReserveMigration(ctx context.Context, req ReserveMigrationRequest) (CommandResult, error) {
	execReserve := func() (OperationRecord, error) {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		if ps.shuttingDown || ctx.Err() != nil {
			return OperationRecord{}, context.Canceled
		}
		scope := CommandScope{User: req.User, SessionID: req.SessionID, ClientID: req.ClientID, BindingGeneration: req.BindingGeneration, OperationID: req.OperationID}
		if _, err := ps.authorizeSessionLocked(ctx, scope); err != nil {
			return OperationRecord{}, err
		}
		device := ps.deviceByNameLocked(req.DeviceName)
		if device == nil {
			return OperationRecord{}, fmt.Errorf("%w: %s", ErrDeviceNotFound, req.DeviceName)
		}
		reservations := ps.ensureReservationsLocked()
		now := ps.getNow()
		if existing, ok := ps.reservationLocked(req.MigrationID); ok {
			if existing.SessionID != req.SessionID || existing.ClientID != req.ClientID || existing.User != req.User || existing.DeviceName != req.DeviceName {
				return OperationRecord{}, ErrMigrationConflict
			}
			if req.BindingGeneration != nil && existing.BindingGeneration != *req.BindingGeneration {
				return OperationRecord{}, ErrCommandSuperseded
			}
			if existing.Status == ReservationActive && now.Before(existing.ExpiresAt) {
				device.mu.Lock()
				hasSession := false
				for _, session := range ps.sessionManager.FindByDevice(req.DeviceName) {
					if session.SessionID != req.SessionID && session.OwnerClientID != "" {
						hasSession = true
						break
					}
				}
				occupied := device.isOccupiedLocked(hasSession)
				currentFp := device.sceneFingerprintLocked()
				queueCount := device.PlaybackQueue.Size()
				device.mu.Unlock()
				if currentFp != existing.ExpectedFingerprint && req.ExpectedFingerprint == "" {
					return OperationRecord{}, &TargetOccupiedError{SceneFingerprint: currentFp, QueueCount: queueCount}
				}
				if occupied && !req.ForceTakeover && req.ExpectedFingerprint != "" {
					return OperationRecord{}, &TargetOccupiedError{SceneFingerprint: currentFp, QueueCount: queueCount}
				}
				if req.ExpectedFingerprint != "" && req.ExpectedFingerprint != currentFp {
					return OperationRecord{}, &TargetOccupiedError{SceneFingerprint: currentFp, QueueCount: queueCount}
				}
				if req.ExpectedFingerprint != "" {
					existing.ExpectedFingerprint = currentFp
					reservations[req.MigrationID] = existing
				}
				result := CommandResult{Status: OperationStatusApplied, MigrationID: req.MigrationID, OperationID: req.OperationID}
				return OperationRecord{CommandResult: &result}, nil
			}
			return OperationRecord{}, ErrMigrationConflict
		}
		for _, other := range reservations {
			if other.DeviceName == req.DeviceName && other.MigrationID != req.MigrationID && other.Status == ReservationActive && now.Before(other.ExpiresAt) {
				return OperationRecord{}, ErrMigrationConflict
			}
		}
		device.mu.Lock()
		hasSession := false
		for _, session := range ps.sessionManager.FindByDevice(req.DeviceName) {
			if session.SessionID != req.SessionID && session.OwnerClientID != "" {
				hasSession = true
				break
			}
		}
		isOccupied := device.isOccupiedLocked(hasSession)
		currentFp := device.sceneFingerprintLocked()
		queueCount := device.PlaybackQueue.Size()
		device.mu.Unlock()
		if (isOccupied && (!req.ForceTakeover || req.ExpectedFingerprint != currentFp)) ||
			(!isOccupied && req.ExpectedFingerprint != "" && req.ExpectedFingerprint != currentFp) {
			return OperationRecord{}, &TargetOccupiedError{SceneFingerprint: currentFp, QueueCount: queueCount}
		}
		reservationTTL := conf.Server.Jukebox.ReservationTTL
		if reservationTTL <= 0 {
			reservationTTL = conf.DefaultJukeboxReservationTTL
		}
		var gen uint64
		if req.BindingGeneration != nil {
			gen = *req.BindingGeneration
		}
		reservation := MigrationReservation{
			MigrationID: req.MigrationID, DeviceName: req.DeviceName, SessionID: req.SessionID,
			ClientID: req.ClientID, User: req.User, Status: ReservationActive,
			ExpectedFingerprint: currentFp, BindingGeneration: gen,
			CreatedAt: now, ExpiresAt: now.Add(reservationTTL),
		}
		reservations[req.MigrationID] = reservation
		result := CommandResult{Status: OperationStatusApplied, MigrationID: req.MigrationID, OperationID: req.OperationID}
		return OperationRecord{CommandResult: &result}, nil
	}

	if req.OperationID != "" {
		scope := CommandScope{User: req.User, SessionID: req.SessionID, ClientID: req.ClientID, BindingGeneration: req.BindingGeneration, OperationID: req.OperationID}
		rec, err := ps.executeProtectedOperation(ctx, scope, "migration.reserve", req, execReserve)
		if rec.CommandResult != nil {
			return *rec.CommandResult, err
		}
		return CommandResult{Status: rec.Status, MigrationID: req.MigrationID, OperationID: req.OperationID}, err
	}

	rec, err := execReserve()
	if rec.CommandResult != nil {
		return *rec.CommandResult, err
	}
	return CommandResult{Status: rec.Status, MigrationID: req.MigrationID, OperationID: req.OperationID}, err
}

func (ps *playbackServer) ReleaseMigrationReservation(ctx context.Context, req ReleaseMigrationRequest) (CommandResult, error) {
	execRelease := func() (OperationRecord, error) {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		scope := CommandScope{User: req.User, SessionID: req.SessionID, ClientID: req.ClientID, BindingGeneration: req.BindingGeneration, OperationID: req.OperationID}
		if _, err := ps.authorizeSessionLocked(ctx, scope); err != nil {
			return OperationRecord{}, err
		}
		reservations := ps.ensureReservationsLocked()
		reservation, ok := ps.reservationLocked(req.MigrationID)
		if !ok {
			result := CommandResult{Status: OperationStatusUnknown, MigrationID: req.MigrationID, OperationID: req.OperationID, Reason: "NOT_FOUND"}
			var gen uint64
			if req.BindingGeneration != nil {
				gen = *req.BindingGeneration
			}
			return OperationRecord{Status: OperationStatusUnknown, CommandResult: &result}, &OperationExecutionUnknownError{BindingGeneration: gen, Cause: ErrReservationNotFound}
		}
		if reservation.SessionID != req.SessionID || reservation.ClientID != req.ClientID || reservation.User != req.User || (req.BindingGeneration != nil && reservation.BindingGeneration != *req.BindingGeneration) {
			return OperationRecord{}, ErrSessionOwnership
		}
		result := CommandResult{MigrationID: req.MigrationID, OperationID: req.OperationID}
		switch {
		case reservation.Status == ReservationActive && ps.getNow().Before(reservation.ExpiresAt):
			reservation.Status = ReservationReleased
			reservations[req.MigrationID] = reservation
			result.Status = OperationStatusApplied
		case reservation.Status == ReservationReleased:
			result.Status = OperationStatusApplied
		default:
			result.Status = OperationStatusNotExecuted
			result.Reason = string(reservation.Status)
		}
		return OperationRecord{Status: result.Status, CommandResult: &result}, nil
	}

	if req.OperationID != "" {
		scope := CommandScope{User: req.User, SessionID: req.SessionID, ClientID: req.ClientID, BindingGeneration: req.BindingGeneration, OperationID: req.OperationID}
		rec, err := ps.executeProtectedOperation(ctx, scope, "migration.release", req, execRelease)
		if rec.CommandResult != nil {
			return *rec.CommandResult, err
		}
		return CommandResult{Status: rec.Status, MigrationID: req.MigrationID, OperationID: req.OperationID}, err
	}

	rec, err := execRelease()
	if rec.CommandResult != nil {
		return *rec.CommandResult, err
	}
	return CommandResult{Status: rec.Status, MigrationID: req.MigrationID, OperationID: req.OperationID}, err
}

func (ps *playbackServer) QueryReservation(ctx context.Context, migrationID string) (ReservationInfo, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	res, ok := ps.reservationLocked(migrationID)
	if !ok {
		return ReservationInfo{Status: string(ReservationNotFound)}, nil
	}
	if user, authenticated := request.UserFrom(ctx); authenticated && user.UserName != res.User {
		return ReservationInfo{}, ErrSessionOwnership
	}
	return ReservationInfo{
		Status:              string(res.Status),
		MigrationID:         res.MigrationID,
		DeviceName:          res.DeviceName,
		SessionID:           res.SessionID,
		ClientID:            res.ClientID,
		ExpectedFingerprint: res.ExpectedFingerprint,
		CreatedAt:           res.CreatedAt,
		ExpiresAt:           res.ExpiresAt,
	}, nil
}

func (ps *playbackServer) CommitMigration(ctx context.Context, req CommitMigrationRequest) (SessionStatus, error) {
	generation := req.ExpectedGeneration
	if req.BindingGeneration != nil {
		if generation != 0 && generation != *req.BindingGeneration {
			return SessionStatus{}, ErrCommandScope
		}
		generation = *req.BindingGeneration
	}
	var generationPtr *uint64
	if generation != 0 || req.BindingGeneration != nil {
		generationCopy := generation
		generationPtr = &generationCopy
	}
	scope := CommandScope{User: req.User, SessionID: req.SessionID, ClientID: req.ClientID, BindingGeneration: generationPtr, OperationID: req.OperationID}
	var revoked []Session
	normalized := req
	normalized.ExpectedGeneration = generation
	normalized.BindingGeneration = nil
	execCommit := func() (OperationRecord, error) {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		if ps.shuttingDown || ctx.Err() != nil {
			return OperationRecord{}, context.Canceled
		}
		session, err := ps.authorizeSessionLocked(ctx, scope)
		if err != nil {
			return OperationRecord{}, err
		}
		reservations := ps.ensureReservationsLocked()
		reservation, ok := ps.reservationLocked(req.MigrationID)
		now := ps.getNow()
		if !ok || reservation.Status != ReservationActive || !now.Before(reservation.ExpiresAt) {
			return OperationRecord{}, ErrReservationNotFound
		}
		if reservation.SessionID != req.SessionID || reservation.ClientID != req.ClientID || reservation.User != req.User || reservation.BindingGeneration != session.BindingGeneration {
			return OperationRecord{}, ErrSessionOwnership
		}
		if reservation.DeviceName != req.TargetDeviceName {
			return OperationRecord{}, fmt.Errorf("reservation device mismatch: %s vs %s", reservation.DeviceName, req.TargetDeviceName)
		}
		targetDevice := ps.deviceByNameLocked(req.TargetDeviceName)
		if targetDevice == nil {
			return OperationRecord{}, fmt.Errorf("%w: %s", ErrDeviceNotFound, req.TargetDeviceName)
		}
		targetDevice.mu.Lock()
		defer targetDevice.mu.Unlock()
		currentFp := targetDevice.sceneFingerprintLocked()
		if currentFp != reservation.ExpectedFingerprint || (req.ExpectedFingerprint != "" && req.ExpectedFingerprint != reservation.ExpectedFingerprint) {
			return OperationRecord{}, &TargetOccupiedError{SceneFingerprint: currentFp, QueueCount: targetDevice.PlaybackQueue.Size()}
		}

		sessionManager := ps.ensureSessionManagerLocked()
		revoked = sessionManager.RevokeDeviceOwners(req.TargetDeviceName, req.SessionID)
		reservation.Status = ReservationReleased
		reservations[req.MigrationID] = reservation
		session.DeviceName = req.TargetDeviceName
		session.BindingGeneration++
		sessionManager.Update(session)

		targetDevice.clearLocked(ctx)
		if len(req.Queue) > 0 {
			items := model.MediaFiles{}
			for _, id := range req.Queue {
				if mf, err := ps.GetMediaFile(id); err == nil && mf != nil {
					items = append(items, *mf)
				} else {
					items = append(items, model.MediaFile{ID: id, Path: "/" + id + ".mp3"})
				}
			}
			targetDevice.PlaybackQueue.Set(items)
			targetDevice.PlaybackQueue.SetIndex(req.CurrentIndex)
		}
		if req.Position != nil {
			pos := *req.Position
			targetDevice.recoveryPosition = &pos
		}
		if req.Gain != nil {
			targetDevice.Gain = *req.Gain
			targetDevice.DesiredGain = *req.Gain
		}
		if req.Muted != nil {
			targetDevice.Muted = *req.Muted
		}
		targetDevice.incrementQueueVersionLocked()
		targetDevice.incrementSceneRevisionLocked()
		status := sessionStatusFromDevice(session, targetDevice)
		return OperationRecord{SessionStatus: &status}, nil
	}

	var rec OperationRecord
	var err error
	if req.OperationID != "" {
		rec, err = ps.executeProtectedOperation(ctx, scope, "migration.commit", normalized, execCommit)
	} else {
		rec, err = execCommit()
	}
	for _, oldOwner := range revoked {
		status := ps.statusFromSession(oldOwner)
		status.Attached = false
		status.OwnershipState = SessionOwnershipDetached
		status.TerminationReason = "migration_revoked"
		ps.getEventBroker().SendMessage(jukeboxTargetContext(oldOwner.User), NewJukeboxStateUpdatedEvent(status))
	}
	if rec.SessionStatus != nil {
		return *rec.SessionStatus, err
	}
	return SessionStatus{}, err
}

func (ps *playbackServer) QueryOperation(ctx context.Context, sessionID, operationID string) (OperationRecord, error) {
	ps.mu.Lock()
	sessionManager := ps.ensureSessionManagerLocked()
	session, ok := sessionManager.Get(sessionID)
	if !ok {
		ps.mu.Unlock()
		return OperationRecord{}, ErrSessionNotFound
	}
	if !sessionUserAllowed(ctx, session) {
		ps.mu.Unlock()
		return OperationRecord{}, ErrSessionOwnership
	}
	store := ps.ensureOperationStoreLocked()
	retired := store.prune()
	ps.retireOperationsLocked(retired)
	rec, found := store.Get(sessionID, operationID)
	ps.mu.Unlock()
	if found {
		return rec, nil
	}
	return OperationRecord{Status: OperationStatusUnknown, SessionID: sessionID, OperationID: operationID}, nil
}

// mergeBluetoothDevices discovers Bluetooth sinks and appends any new ones to playbackDevices.
func (ps *playbackServer) mergeBluetoothDevices(ctx context.Context) {
	ps.mergeBluetoothDevicesFromSinks(ctx, discoverAllSinks(ctx))
}

func (ps *playbackServer) mergeBluetoothDevicesFromSinks(ctx context.Context, sinks []bluetooth.AudioSink) {
	btSinks := bluetooth.DiscoverBluetoothSinks(ctx, sinks)
	deviceCtx := ps.playbackDeviceContext(ctx)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.shuttingDown || ctx.Err() != nil {
		return
	}
	for _, sink := range btSinks {
		devName := sink.MPVDeviceName()
		if ps.hasDeviceLocked(devName) {
			continue
		}
		dev := ps.newPlaybackDevice(deviceCtx, sink.FriendlyName(), devName)
		ps.playbackDevices = append(ps.playbackDevices, dev)
		log.Info(ctx, "Discovered Bluetooth device", "name", sink.FriendlyName(), "device", devName)
	}
}

func (ps *playbackServer) hasDeviceLocked(deviceName string) bool {
	for _, d := range ps.playbackDevices {
		if d.DeviceName == deviceName {
			return true
		}
	}
	return false
}

func (ps *playbackServer) hasDevice(deviceName string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.hasDeviceLocked(deviceName)
}

// ListDevices returns info about all configured playback devices, including live connection status.
func (ps *playbackServer) ListDevices() []DeviceInfo {
	var sinkSnapshot []bluetooth.AudioSink
	if conf.Server.Jukebox.AutoDiscoverBluetooth && ps.ctx != nil {
		sinkSnapshot = discoverAllSinks(*ps.ctx)
		ps.mergeBluetoothDevicesFromSinks(*ps.ctx, sinkSnapshot)
	}

	activeSinks := make(map[string]bool)
	for _, sink := range sinkSnapshot {
		activeSinks[sink.MPVDeviceName()] = true
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()
	devices := make([]DeviceInfo, len(ps.playbackDevices))
	for i, d := range ps.playbackDevices {
		isBT := strings.HasPrefix(d.DeviceName, "pulse/bluez_")
		connected := true
		if isBT {
			_, connected = activeSinks[d.DeviceName]
		}
		devices[i] = DeviceInfo{
			Name:        d.Name,
			DeviceName:  d.DeviceName,
			IsDefault:   d.Default,
			IsBluetooth: isBT,
			Connected:   connected,
		}
	}
	return devices
}

// SwitchDevice changes the default playback device. If the old device was actively
// playing, the queue and playback state are migrated to the new device and playback
// resumes at the same position.
func (ps *playbackServer) SwitchDevice(ctx context.Context, deviceName string) error {
	return ps.switchDevice(ctx, deviceName, nil)
}

func (ps *playbackServer) SwitchDeviceForCommand(ctx context.Context, scope CommandScope, deviceName string) error {
	if scope.SessionID == "" {
		return ps.switchDevice(ctx, deviceName, &scope)
	}
	_, err := ps.executeProtectedOperation(ctx, scope, "device.switch", deviceName, func() (OperationRecord, error) {
		if err := ps.switchDevice(ctx, deviceName, &scope); err != nil {
			return OperationRecord{}, err
		}
		status, err := ps.SessionStatus(ctx, scope.SessionID)
		return OperationRecord{SessionStatus: &status}, err
	})
	return err
}

func (ps *playbackServer) switchDevice(ctx context.Context, deviceName string, scope *CommandScope) error {
	ps.mu.Lock()
	if ps.shuttingDown || ctx.Err() != nil {
		ps.mu.Unlock()
		return context.Canceled
	}
	if scope != nil {
		owned, err := ps.authorizeCommandLocked(ctx, *scope)
		if err != nil {
			ps.mu.Unlock()
			return err
		}
		defaultDevice, err := ps.getDefaultDeviceLocked()
		if err != nil || owned != defaultDevice {
			ps.mu.Unlock()
			return ErrSessionOwnership
		}
		if ps.sessionManager != nil {
			for _, attached := range ps.sessionManager.FindByDevice(deviceName) {
				if attached.SessionID != scope.SessionID {
					ps.mu.Unlock()
					return ErrSessionOwnership
				}
			}
		}
	}
	var newDev *playbackDevice
	for idx := range ps.playbackDevices {
		if ps.playbackDevices[idx].DeviceName == deviceName {
			newDev = ps.playbackDevices[idx]
			break
		}
	}
	if newDev == nil {
		ps.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDeviceNotFound, deviceName)
	}

	var oldDev *playbackDevice
	for idx := range ps.playbackDevices {
		if ps.playbackDevices[idx].Default {
			oldDev = ps.playbackDevices[idx]
			break
		}
	}

	var wasPlaying bool
	var savedPosition *int
	var savedGain float32
	var savedQueue model.MediaFiles
	var savedIndex int
	var preparationVersion uint64
	oldDeviceName := ""

	if oldDev != nil && oldDev != newDev {
		oldDeviceName = oldDev.DeviceName
		oldDev.mu.Lock()
		wasPlaying = oldDev.isPlaying()
		savedGain = oldDev.Gain
		savedQueue = oldDev.PlaybackQueue.Get()
		savedIndex = oldDev.PlaybackQueue.Index
		if oldDev.ActiveTrack != nil {
			position := oldDev.ActiveTrack.Position()
			savedPosition = &position
		} else if oldDev.recoveryPosition != nil {
			position := *oldDev.recoveryPosition
			savedPosition = &position
		}
		oldDev.clearLocked(ctx)
		oldDev.incrementQueueVersionLocked()
		oldDev.mu.Unlock()

		if len(savedQueue) > 0 {
			newDev.mu.Lock()
			newDev.clearLocked(ctx)
			newDev.PlaybackQueue.Set(savedQueue)
			newDev.PlaybackQueue.SetIndex(savedIndex)
			newDev.Gain = savedGain
			newDev.DesiredGain = savedGain
			newDev.recoveryPosition = savedPosition
			newDev.incrementQueueVersionLocked()
			preparationVersion = newDev.preparationVersion
			newDev.mu.Unlock()
		}
	}

	for idx := range ps.playbackDevices {
		ps.playbackDevices[idx].Default = (ps.playbackDevices[idx].DeviceName == deviceName)
	}

	var reboundSessions []Session
	if ps.sessionManager != nil && oldDeviceName != "" {
		reboundSessions = ps.sessionManager.RebindDevice(oldDeviceName, deviceName)
	}

	log.Info(ctx, "Switched default audio device", "device", deviceName)
	ps.mu.Unlock()

	// Preparation and notification must not hold the server mutex. Start restores
	// the recovery position and gain before the first unpause on the new output.
	if oldDeviceName != "" && len(savedQueue) > 0 && wasPlaying {
		newDev.mu.Lock()
		if newDev.preparationVersion == preparationVersion {
			_, changed, err := newDev.startLocked(ctx)
			notify := newDev.stateChangeNotifierLocked()
			if err != nil || !changed {
				notify = nil
			}
			newDev.mu.Unlock()
			invokeCallback(notify)
			if err != nil {
				return err
			}
		} else {
			newDev.mu.Unlock()
		}
	}

	for _, session := range reboundSessions {
		if session.User == "" {
			continue
		}
		status := ps.statusFromSession(session)
		ps.getEventBroker().SendMessage(jukeboxTargetContext(session.User), NewJukeboxStateUpdatedEvent(status))
	}

	return nil
}

// RefreshDevices re-runs Bluetooth discovery and appends any new devices.
func (ps *playbackServer) RefreshDevices(ctx context.Context) error {
	if !conf.Server.Jukebox.AutoDiscoverBluetooth {
		return nil
	}
	ps.mergeBluetoothDevices(ctx)
	return nil
}
