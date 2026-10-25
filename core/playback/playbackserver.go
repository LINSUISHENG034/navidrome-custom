// Package playback implements audio playback using PlaybackDevices. It is used to implement the Jukebox mode in turn.
// It makes use of the MPV library to do the playback. Major parts are:
// - decoder which includes decoding and transcoding of various audio file formats
// - device implementing the basic functions to work with audio devices like set, play, stop, skip, ...
// - queue a simple playlist
package playback

import (
	"context"
	"errors"
	"fmt"
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
	ReleaseMigrationReservation(ctx context.Context, migrationID string, clientID string) (CommandResult, error)
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
)

type OperationRecord struct {
	Status            string        `json:"status"`
	Action            string        `json:"action,omitempty"`
	SessionID         string        `json:"sessionId"`
	OperationID       string        `json:"operationId"`
	BindingGeneration uint64        `json:"bindingGeneration,omitempty"`
	DeviceStatus      *DeviceStatus `json:"deviceStatus,omitempty"`
	ErrorCode         string        `json:"errorCode,omitempty"`
	ErrorMessage      string        `json:"errorMessage,omitempty"`
	CreatedAt         time.Time     `json:"createdAt"`
}

type OperationStore struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	order    []string
	records  map[string]OperationRecord
	inflight map[string][]chan OperationRecord
}

func NewOperationStore(capacity int, ttl time.Duration, now ...func() time.Time) *OperationStore {
	nowFn := time.Now
	if len(now) > 0 && now[0] != nil {
		nowFn = now[0]
	}
	if capacity <= 0 {
		capacity = 1000
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &OperationStore{
		capacity: capacity,
		ttl:      ttl,
		now:      nowFn,
		records:  make(map[string]OperationRecord),
		inflight: make(map[string][]chan OperationRecord),
	}
}

func (s *OperationStore) Get(sessionID, operationID string) (OperationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + ":" + operationID
	rec, ok := s.records[key]
	if !ok {
		return OperationRecord{Status: OperationStatusUnknown, SessionID: sessionID, OperationID: operationID}, false
	}
	if s.ttl > 0 && s.now().Sub(rec.CreatedAt) > s.ttl {
		delete(s.records, key)
		for i, k := range s.order {
			if k == key {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
		return OperationRecord{Status: OperationStatusUnknown, SessionID: sessionID, OperationID: operationID}, false
	}
	return rec, true
}

func (s *OperationStore) Begin(sessionID, operationID string) (<-chan OperationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + ":" + operationID
	if waiters, exists := s.inflight[key]; exists {
		ch := make(chan OperationRecord, 1)
		s.inflight[key] = append(waiters, ch)
		return ch, false
	}
	s.inflight[key] = nil
	return nil, true
}

func (s *OperationStore) CleanupInflight(sessionID, operationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + ":" + operationID
	delete(s.inflight, key)
}

func (s *OperationStore) Complete(sessionID, operationID string, rec OperationRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + ":" + operationID
	rec.CreatedAt = s.now().UTC()
	rec.SessionID = sessionID
	rec.OperationID = operationID

	if _, exists := s.records[key]; !exists {
		for len(s.order) >= s.capacity && len(s.order) > 0 {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.records, oldest)
		}
		s.order = append(s.order, key)
	}
	s.records[key] = rec

	if waiters, exists := s.inflight[key]; exists {
		for _, ch := range waiters {
			ch <- rec
			close(ch)
		}
		delete(s.inflight, key)
	}
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
	CreatedAt           time.Time         `json:"createdAt"`
	ExpiresAt           time.Time         `json:"expiresAt"`
}

type ReserveMigrationRequest struct {
	MigrationID         string `json:"migrationId"`
	OperationID         string `json:"operationId"`
	SessionID           string `json:"sessionId"`
	ClientID            string `json:"clientId"`
	User                string `json:"user"`
	DeviceName          string `json:"deviceName"`
	ForceTakeover       bool   `json:"forceTakeover"`
	ExpectedFingerprint string `json:"expectedFingerprint,omitempty"`
}

type ReleaseMigrationRequest struct {
	MigrationID string `json:"migrationId"`
	OperationID string `json:"operationId"`
	SessionID   string `json:"sessionId"`
	ClientID    string `json:"clientId"`
}

type CommitMigrationRequest struct {
	MigrationID         string   `json:"migrationId"`
	OperationID         string   `json:"operationId"`
	SessionID           string   `json:"sessionId"`
	ClientID            string   `json:"clientId"`
	User                string   `json:"user"`
	TargetDeviceName    string   `json:"targetDeviceName"`
	ExpectedGeneration  uint64   `json:"expectedGeneration"`
	ExpectedFingerprint string   `json:"expectedFingerprint"`
	Queue               []string `json:"queue,omitempty"`
	CurrentIndex        int      `json:"currentIndex,omitempty"`
	Position            int      `json:"position,omitempty"`
	Gain                float32  `json:"gain,omitempty"`
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
		ps.operationStore = NewOperationStore(1000, 10*time.Minute, ps.now)
	}
	return ps.operationStore
}

func (ps *playbackServer) ensureReservationsLocked() map[string]MigrationReservation {
	if ps.reservations == nil {
		ps.reservations = make(map[string]MigrationReservation)
	}
	return ps.reservations
}

func (ps *playbackServer) ensureSessionManagerLocked() *SessionManager {
	if ps.sessionManager == nil {
		ps.sessionManager = NewSessionManager(defaultSessionTTL)
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
		if attached.SessionID != req.SessionID {
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
	session, token := sessionManager.Attach(req)
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
	session, token, err := sessionManager.Handoff(req)
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

func (ps *playbackServer) ExecuteCommand(ctx context.Context, scope CommandScope, command PlaybackCommand) (DeviceStatus, error) {
	if scope.SessionID != "" && scope.OperationID != "" {
		ps.mu.Lock()
		store := ps.ensureOperationStoreLocked()
		ps.mu.Unlock()

		if rec, found := store.Get(scope.SessionID, scope.OperationID); found {
			if rec.DeviceStatus != nil {
				return *rec.DeviceStatus, nil
			}
			if rec.ErrorCode != "" {
				return DeviceStatus{}, errors.New(rec.ErrorMessage)
			}
			return DeviceStatus{}, nil
		}

		waiter, isFirst := store.Begin(scope.SessionID, scope.OperationID)
		if !isFirst {
			select {
			case rec, ok := <-waiter:
				if ok && rec.DeviceStatus != nil {
					return *rec.DeviceStatus, nil
				}
				if ok && rec.ErrorCode != "" {
					return DeviceStatus{}, errors.New(rec.ErrorMessage)
				}
				return DeviceStatus{}, nil
			case <-ctx.Done():
				return DeviceStatus{}, ctx.Err()
			}
		}
		defer store.CleanupInflight(scope.SessionID, scope.OperationID)
	}

	ps.mu.Lock()
	device, err := ps.authorizeCommandLocked(ctx, scope)
	if err != nil {
		ps.mu.Unlock()
		if scope.SessionID != "" && scope.OperationID != "" {
			ps.mu.Lock()
			store := ps.ensureOperationStoreLocked()
			ps.mu.Unlock()
			store.Complete(scope.SessionID, scope.OperationID, OperationRecord{
				Status:       OperationStatusFailed,
				Action:       command.Action,
				SessionID:    scope.SessionID,
				OperationID:  scope.OperationID,
				ErrorCode:    "COMMAND_FAILED",
				ErrorMessage: err.Error(),
			})
		}
		return DeviceStatus{}, err
	}
	// Acquire the state lock before releasing the authority/binding lock. Slow
	// preparation releases only device.mu, leaving Stop free to supersede it.
	device.mu.Lock()
	device.User = scope.User
	ps.mu.Unlock()
	status, err := device.finishCommandLocked(ctx, command)

	if scope.SessionID != "" && scope.OperationID != "" {
		ps.mu.Lock()
		store := ps.ensureOperationStoreLocked()
		ps.mu.Unlock()
		opStatus := OperationStatusApplied
		errCode := ""
		errMsg := ""
		if err != nil {
			opStatus = OperationStatusFailed
			errCode = "COMMAND_FAILED"
			errMsg = err.Error()
		}
		stCopy := status
		gen := uint64(0)
		if scope.BindingGeneration != nil {
			gen = *scope.BindingGeneration
		}
		store.Complete(scope.SessionID, scope.OperationID, OperationRecord{
			Status:            opStatus,
			Action:            command.Action,
			SessionID:         scope.SessionID,
			OperationID:       scope.OperationID,
			BindingGeneration: gen,
			DeviceStatus:      &stCopy,
			ErrorCode:         errCode,
			ErrorMessage:      errMsg,
		})
	}

	return status, err
}

func (ps *playbackServer) ReserveMigration(ctx context.Context, req ReserveMigrationRequest) (CommandResult, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.shuttingDown || ctx.Err() != nil {
		return CommandResult{Status: OperationStatusFailed}, context.Canceled
	}
	if user, ok := request.UserFrom(ctx); ok && req.User != "" && user.UserName != req.User {
		return CommandResult{Status: OperationStatusFailed}, ErrSessionOwnership
	}
	device := ps.deviceByNameLocked(req.DeviceName)
	if device == nil {
		return CommandResult{Status: OperationStatusFailed}, fmt.Errorf("%w: %s", ErrDeviceNotFound, req.DeviceName)
	}

	reservations := ps.ensureReservationsLocked()
	now := ps.getNow()

	// Check if already reserved by this migration
	if existing, ok := reservations[req.MigrationID]; ok {
		if existing.Status == ReservationActive && now.Before(existing.ExpiresAt) {
			if req.ExpectedFingerprint != "" {
				existing.ExpectedFingerprint = req.ExpectedFingerprint
				reservations[req.MigrationID] = existing
			}
			return CommandResult{Status: OperationStatusApplied, MigrationID: req.MigrationID}, nil
		}
	}

	// Check conflicting active reservations for this target device
	for _, other := range reservations {
		if other.DeviceName == req.DeviceName && other.MigrationID != req.MigrationID &&
			other.Status == ReservationActive && now.Before(other.ExpiresAt) {
			return CommandResult{Status: OperationStatusFailed, MigrationID: req.MigrationID}, ErrMigrationConflict
		}
	}

	// Check occupancy
	device.mu.Lock()
	hasSession := false
	if ps.sessionManager != nil {
		for _, s := range ps.sessionManager.FindByDevice(req.DeviceName) {
			if s.SessionID != req.SessionID {
				hasSession = true
				break
			}
		}
	}
	isOccupied := device.isOccupiedLocked(hasSession)
	currentFp := device.sceneFingerprintLocked()
	queueCount := device.PlaybackQueue.Size()
	device.mu.Unlock()

	if isOccupied && !req.ForceTakeover {
		if req.ExpectedFingerprint == "" || req.ExpectedFingerprint != currentFp {
			return CommandResult{Status: OperationStatusFailed, MigrationID: req.MigrationID}, &TargetOccupiedError{
				SceneFingerprint: currentFp,
				QueueCount:       queueCount,
			}
		}
	}

	res := MigrationReservation{
		MigrationID:         req.MigrationID,
		DeviceName:          req.DeviceName,
		SessionID:           req.SessionID,
		ClientID:            req.ClientID,
		User:                req.User,
		Status:              ReservationActive,
		ExpectedFingerprint: currentFp,
		CreatedAt:           now,
		ExpiresAt:           now.Add(30 * time.Second),
	}
	reservations[req.MigrationID] = res

	return CommandResult{Status: OperationStatusApplied, MigrationID: req.MigrationID}, nil
}

func (ps *playbackServer) ReleaseMigrationReservation(ctx context.Context, migrationID string, clientID string) (CommandResult, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	reservations := ps.ensureReservationsLocked()
	res, ok := reservations[migrationID]
	if !ok {
		return CommandResult{Status: OperationStatusNotExecuted, MigrationID: migrationID, Reason: "NOT_FOUND"}, nil
	}
	if clientID != "" && res.ClientID != "" && res.ClientID != clientID {
		return CommandResult{Status: OperationStatusFailed, MigrationID: migrationID, Reason: "NOT_OWNER"}, ErrSessionOwnership
	}
	if res.Status == ReservationActive {
		res.Status = ReservationReleased
		reservations[migrationID] = res
		return CommandResult{Status: OperationStatusApplied, MigrationID: migrationID}, nil
	}
	if res.Status == ReservationReleased {
		return CommandResult{Status: OperationStatusApplied, MigrationID: migrationID}, nil
	}
	return CommandResult{Status: OperationStatusNotExecuted, MigrationID: migrationID, Reason: string(res.Status)}, nil
}

func (ps *playbackServer) QueryReservation(ctx context.Context, migrationID string) (ReservationInfo, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	reservations := ps.ensureReservationsLocked()
	res, ok := reservations[migrationID]
	if !ok {
		return ReservationInfo{Status: string(ReservationNotFound)}, nil
	}
	now := ps.getNow()
	status := res.Status
	if status == ReservationActive && now.After(res.ExpiresAt) {
		status = ReservationExpired
		res.Status = status
		reservations[migrationID] = res
	}
	return ReservationInfo{
		Status:              string(status),
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
	ps.mu.Lock()

	if ps.shuttingDown || ctx.Err() != nil {
		ps.mu.Unlock()
		return SessionStatus{}, context.Canceled
	}
	if user, ok := request.UserFrom(ctx); ok && req.User != "" && user.UserName != req.User {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}

	reservations := ps.ensureReservationsLocked()
	res, ok := reservations[req.MigrationID]
	now := ps.getNow()
	if !ok || res.Status != ReservationActive || now.After(res.ExpiresAt) {
		ps.mu.Unlock()
		return SessionStatus{}, ErrReservationNotFound
	}
	if res.SessionID != req.SessionID || (res.ClientID != "" && req.ClientID != "" && res.ClientID != req.ClientID) {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}
	if res.DeviceName != req.TargetDeviceName {
		ps.mu.Unlock()
		return SessionStatus{}, fmt.Errorf("reservation device mismatch: %s vs %s", res.DeviceName, req.TargetDeviceName)
	}

	sessionManager := ps.ensureSessionManagerLocked()
	session, ok := sessionManager.Get(req.SessionID)
	if !ok {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionNotFound
	}
	if req.User != "" && session.User != req.User {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}
	if req.ClientID != "" && session.OwnerClientID != req.ClientID {
		ps.mu.Unlock()
		return SessionStatus{}, ErrSessionOwnership
	}
	if req.ExpectedGeneration > 0 && session.BindingGeneration != req.ExpectedGeneration {
		ps.mu.Unlock()
		return SessionStatus{}, ErrCommandSuperseded
	}

	targetDevice := ps.deviceByNameLocked(req.TargetDeviceName)
	if targetDevice == nil {
		ps.mu.Unlock()
		return SessionStatus{}, fmt.Errorf("%w: %s", ErrDeviceNotFound, req.TargetDeviceName)
	}

	targetDevice.mu.Lock()
	currentFp := targetDevice.sceneFingerprintLocked()
	if req.ExpectedFingerprint != "" && currentFp != req.ExpectedFingerprint {
		targetDevice.mu.Unlock()
		ps.mu.Unlock()
		return SessionStatus{}, &TargetOccupiedError{
			SceneFingerprint: currentFp,
			QueueCount:       targetDevice.PlaybackQueue.Size(),
		}
	}

	res.Status = ReservationReleased
	reservations[req.MigrationID] = res

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
	if req.Position > 0 {
		pos := req.Position
		targetDevice.recoveryPosition = &pos
	}
	if req.Gain > 0 {
		targetDevice.Gain = req.Gain
		targetDevice.DesiredGain = req.Gain
	}
	targetDevice.incrementQueueVersionLocked()
	targetDevice.incrementSceneRevisionLocked()
	targetDevice.mu.Unlock()
	ps.mu.Unlock()

	return ps.statusFromSession(session), nil
}

func (ps *playbackServer) QueryOperation(ctx context.Context, sessionID, operationID string) (OperationRecord, error) {
	ps.mu.Lock()
	store := ps.ensureOperationStoreLocked()
	ps.mu.Unlock()

	if rec, ok := store.Get(sessionID, operationID); ok {
		return rec, nil
	}
	return OperationRecord{
		Status:      OperationStatusUnknown,
		SessionID:   sessionID,
		OperationID: operationID,
	}, nil
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
	return ps.switchDevice(ctx, deviceName, &scope)
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
