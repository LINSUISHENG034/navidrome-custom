package model

// PlaybackToken identifies one output instance, including repeated queue entries.
type PlaybackToken uint64

// PlaybackEventKind describes why one output instance stopped being authoritative.
type PlaybackEventKind uint8

const (
	// PlaybackEventNaturalEnd marks natural end-of-file and allows queue advancement.
	PlaybackEventNaturalEnd PlaybackEventKind = iota + 1
	// PlaybackEventRetired marks an explicit stop or output retirement.
	PlaybackEventRetired
	// PlaybackEventFailure marks an output or IPC failure requiring recovery.
	PlaybackEventFailure
)

// PlaybackEvent reports a terminal output observation for one playback instance.
type PlaybackEvent struct {
	Token    PlaybackToken
	Kind     PlaybackEventKind
	Position *int
	Err      error
}

// QueueProtocolCapabilities is the single additive queue-capability contract
// shared by attach responses, status responses, and SSE events. Version is an
// integer; clients must check boolean/version values, never field presence.
type QueueProtocolCapabilities struct {
	Version                  int  `json:"version"`
	RelativeMove             bool `json:"relativeMove"`
	IdentitySelect           bool `json:"identitySelect"`
	IdentityRemove           bool `json:"identityRemove"`
	IdentityMigration        bool `json:"identityMigration"`
	AnchorVersionEnforcement bool `json:"anchorVersionEnforcement"`
	OperationResults         bool `json:"operationResults"`
	// MigrationProof advertises the immutable server-generated migration commit
	// proof plus the separate atomic current snapshot on the original-operation
	// query. It is purely additive: absence or false means clients must not
	// assume proof support.
	MigrationProof bool `json:"migrationProof"`
}
