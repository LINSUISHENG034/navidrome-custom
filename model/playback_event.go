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
