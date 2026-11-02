package playback

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// Queue is the single authoritative production playback queue. Items carry a
// stable, globally unique QueueItemID per enqueue instance, strictly
// separating queue item identity from MediaFile identity and array position.
type Queue struct {
	Index int
	Items []QueueCandidateItem
}

func NewQueue() *Queue {
	return &Queue{
		Index: -1,
		Items: []QueueCandidateItem{},
	}
}

func newQueueItems(items model.MediaFiles) []QueueCandidateItem {
	entries := make([]QueueCandidateItem, len(items))
	for i := range items {
		entries[i] = QueueCandidateItem{QueueItemID: uuid.NewString(), MediaFile: items[i]}
	}
	return entries
}

func (pd *Queue) String() string {
	var filenames strings.Builder
	for idx, item := range pd.Items {
		filenames.WriteString(fmt.Sprint(idx) + ":" + item.MediaFile.Path + " ")
	}
	return fmt.Sprintf("#Items: %d, idx: %d, files: %s", len(pd.Items), pd.Index, filenames.String())
}

// Current returns the current mediafile or nil. It is a legacy media view;
// CurrentItem exposes the stable queue instance.
func (pd *Queue) Current() *model.MediaFile {
	item := pd.CurrentItem()
	if item == nil {
		return nil
	}
	return &item.MediaFile
}

// CurrentItem returns the current queue instance or nil.
func (pd *Queue) CurrentItem() *QueueCandidateItem {
	if pd.Index == -1 {
		return nil
	}
	if pd.Index >= len(pd.Items) {
		log.Error("internal error: current song index out of bounds", "idx", pd.Index, "length", len(pd.Items))
		return nil
	}
	return &pd.Items[pd.Index]
}

// Get returns the whole queue as media files. It is a legacy media view;
// GetItems exposes the stable queue instances.
func (pd *Queue) Get() model.MediaFiles {
	files := make(model.MediaFiles, len(pd.Items))
	for i := range pd.Items {
		files[i] = pd.Items[i].MediaFile
	}
	return files
}

// GetItems returns a copy of the queue with stable instance IDs.
func (pd *Queue) GetItems() []QueueCandidateItem {
	return slices.Clone(pd.Items)
}

// ItemIDs returns the stable instance IDs in queue order.
func (pd *Queue) ItemIDs() []string {
	ids := make([]string, len(pd.Items))
	for i := range pd.Items {
		ids[i] = pd.Items[i].QueueItemID
	}
	return ids
}

// IndexOfQueueItemID returns the position of a queue instance or -1.
func (pd *Queue) IndexOfQueueItemID(queueItemID string) int {
	for i := range pd.Items {
		if pd.Items[i].QueueItemID == queueItemID {
			return i
		}
	}
	return -1
}

func (pd *Queue) Size() int {
	return len(pd.Items)
}

func (pd *Queue) IsEmpty() bool {
	return len(pd.Items) < 1
}

// Set is similar to a clear followed by a add, but will not change the currently playing track.
// Every item receives a fresh instance ID: legacy whole-queue replacement
// intentionally reassigns IDs (T13-AC10).
func (pd *Queue) Set(items model.MediaFiles) {
	pd.Clear()
	pd.Items = append(pd.Items, newQueueItems(items)...)
}

// adding mediafiles to the queue
func (pd *Queue) Add(items model.MediaFiles) {
	pd.Items = append(pd.Items, newQueueItems(items)...)
	if pd.Index == -1 && len(pd.Items) > 0 {
		pd.Index = 0
	}
}

// Insert adds mediafiles at a specific index while keeping the current track pointer stable.
func (pd *Queue) Insert(index int, items model.MediaFiles) {
	if len(items) == 0 {
		return
	}

	index = max(0, min(index, len(pd.Items)))
	entries := newQueueItems(items)
	newItems := make([]QueueCandidateItem, 0, len(pd.Items)+len(entries))
	newItems = append(newItems, pd.Items[:index]...)
	newItems = append(newItems, entries...)
	newItems = append(newItems, pd.Items[index:]...)
	pd.Items = newItems

	if pd.Index == -1 {
		pd.Index = 0
		return
	}
	if pd.Index >= index {
		pd.Index += len(entries)
	}
}

// ReplaceItems swaps the whole queue while preserving the provided instance
// IDs and current index. It is used by migration paths that must not silently
// lose instance identity (T13-AC10).
func (pd *Queue) ReplaceItems(items []QueueCandidateItem, index int) {
	pd.Items = slices.Clone(items)
	pd.Index = index
}

// empties whole queue
func (pd *Queue) Clear() {
	pd.Index = -1
	pd.Items = nil
}

// Move moves a track from fromIndex to toIndex, adjusting the current index
// to continue pointing at the same queue instance even when IDs repeat.
// Retained items keep their instance IDs.
func (pd *Queue) Move(fromIndex, toIndex int) {
	if fromIndex == toIndex {
		return
	}
	if fromIndex < 0 || fromIndex >= len(pd.Items) || toIndex < 0 || toIndex >= len(pd.Items) {
		return
	}

	currentID := ""
	if pd.Index >= 0 && pd.Index < len(pd.Items) {
		currentID = pd.Items[pd.Index].QueueItemID
	}
	item := pd.Items[fromIndex]
	remaining := make([]QueueCandidateItem, 0, len(pd.Items)-1)
	remaining = append(remaining, pd.Items[:fromIndex]...)
	remaining = append(remaining, pd.Items[fromIndex+1:]...)

	newItems := make([]QueueCandidateItem, 0, len(pd.Items))
	newItems = append(newItems, remaining[:toIndex]...)
	newItems = append(newItems, item)
	newItems = append(newItems, remaining[toIndex:]...)
	pd.Items = newItems

	pd.Index = pd.IndexOfQueueItemID(currentID)
}

// MoveRelative moves the queue instance itemID adjacent to anchorID: "before"
// places it directly before the anchor, "after" directly after it. Retained
// items keep their instance IDs and the current index follows the current
// instance. ok is false when either ID is missing; applied is false when the
// requested placement already holds (self-reference or already adjacent),
// making those moves explicit no-ops (T13-AC03).
func (pd *Queue) MoveRelative(itemID, anchorID, placement string) (applied bool, ok bool) {
	fromIdx := pd.IndexOfQueueItemID(itemID)
	if fromIdx == -1 {
		return false, false
	}
	anchorIdx := pd.IndexOfQueueItemID(anchorID)
	if anchorIdx == -1 {
		return false, false
	}
	if itemID == anchorID {
		return false, true
	}
	if (placement == "before" && fromIdx == anchorIdx-1) || (placement == "after" && fromIdx == anchorIdx+1) {
		return false, true
	}

	currentID := ""
	if pd.Index >= 0 && pd.Index < len(pd.Items) {
		currentID = pd.Items[pd.Index].QueueItemID
	}

	item := pd.Items[fromIdx]
	remaining := make([]QueueCandidateItem, 0, len(pd.Items)-1)
	remaining = append(remaining, pd.Items[:fromIdx]...)
	remaining = append(remaining, pd.Items[fromIdx+1:]...)

	insertPos := 0
	for i := range remaining {
		if remaining[i].QueueItemID == anchorID {
			insertPos = i
			break
		}
	}
	if placement == "after" {
		insertPos++
	}

	newItems := make([]QueueCandidateItem, 0, len(pd.Items))
	newItems = append(newItems, remaining[:insertPos]...)
	newItems = append(newItems, item)
	newItems = append(newItems, remaining[insertPos:]...)
	pd.Items = newItems

	pd.Index = pd.IndexOfQueueItemID(currentID)
	return true, true
}

// idx Zero-based index of the song to skip to or remove.
func (pd *Queue) Remove(idx int) {
	if idx < 0 || idx >= len(pd.Items) {
		return
	}

	pd.Items = append(pd.Items[:idx], pd.Items[idx+1:]...)

	switch {
	case pd.Index == idx:
		pd.Index = -1
	case pd.Index > idx:
		pd.Index--
	}
}

func (pd *Queue) Shuffle() {
	currentID := ""
	if pd.Index >= 0 && pd.Index < len(pd.Items) {
		currentID = pd.Items[pd.Index].QueueItemID
	}

	//nolint:gosec // shuffle order is not a security decision
	rand.Shuffle(len(pd.Items), func(i, j int) { pd.Items[i], pd.Items[j] = pd.Items[j], pd.Items[i] })

	if currentID == "" {
		return
	}
	pd.Index = pd.IndexOfQueueItemID(currentID)
	if pd.Index == -1 {
		log.Error("Could not find current queue item while shuffling", "queueItemId", currentID)
	}
}

func (pd *Queue) getMediaFileIndexByID(id string) (int, error) {
	for idx, item := range pd.Items {
		if item.MediaFile.ID == id {
			return idx, nil
		}
	}
	return -1, fmt.Errorf("ID not found in playlist: %s", id)
}

// Sets the index to a new, valid value inside the Items. Values lower than zero are going to be zero,
// values above will be limited by number of items.
func (pd *Queue) SetIndex(idx int) {
	pd.Index = max(0, min(idx, len(pd.Items)-1))
}

// Are we at the last track?
func (pd *Queue) IsAtLastElement() bool {
	return (pd.Index + 1) >= len(pd.Items)
}

// Goto next index
func (pd *Queue) IncreaseIndex() {
	if !pd.IsAtLastElement() {
		pd.SetIndex(pd.Index + 1)
	}
}

// PlaybackMode represents the 4 mutually exclusive playback modes.
// Values match the wire format used by the upstream native player ('order', 'orderLoop', 'singleLoop', 'shufflePlay').
type PlaybackMode string

const (
	PlaybackModeOrder       PlaybackMode = "order"
	PlaybackModeRepeatList  PlaybackMode = "orderLoop"
	PlaybackModeRepeatTrack PlaybackMode = "singleLoop"
	PlaybackModeShuffle     PlaybackMode = "shufflePlay"
)

func (mode PlaybackMode) valid() bool {
	switch mode {
	case PlaybackModeOrder, PlaybackModeRepeatList, PlaybackModeRepeatTrack, PlaybackModeShuffle:
		return true
	default:
		return false
	}
}

// PrevRewindThresholdSeconds is the threshold above which skipPrev rewinds to 0s
// instead of switching to the previous track.
const PrevRewindThresholdSeconds = 1.0

// QueueCandidateItem represents a candidate server-side queue entry with a stable
// instance identity, strictly separating track/media identity from queue instance identity.
type QueueCandidateItem struct {
	QueueItemID string          `json:"queueItemId"`
	MediaFile   model.MediaFile `json:"mediaFile"`
}

// QueueCandidateModel provides candidate server-side queue operations tracking items
// by stable QueueItemID across duplicate tracks, reordering, deletions, and playback modes.
type QueueCandidateModel struct {
	CurrentQueueItemID string
	Items              []QueueCandidateItem
	Mode               PlaybackMode
}

func NewQueueCandidateModel() *QueueCandidateModel {
	return &QueueCandidateModel{
		CurrentQueueItemID: "",
		Items:              nil,
		Mode:               PlaybackModeOrder,
	}
}

func (m *QueueCandidateModel) Size() int {
	return len(m.Items)
}

func (m *QueueCandidateModel) IsEmpty() bool {
	return len(m.Items) == 0
}

func (m *QueueCandidateModel) Current() *QueueCandidateItem {
	if m.CurrentQueueItemID == "" {
		return nil
	}
	for i := range m.Items {
		if m.Items[i].QueueItemID == m.CurrentQueueItemID {
			return &m.Items[i]
		}
	}
	return nil
}

func (m *QueueCandidateModel) Add(items []QueueCandidateItem) {
	m.Items = append(m.Items, items...)
	if m.CurrentQueueItemID == "" && len(m.Items) > 0 {
		m.CurrentQueueItemID = m.Items[0].QueueItemID
	}
}

func (m *QueueCandidateModel) Insert(index int, items []QueueCandidateItem) {
	if len(items) == 0 {
		return
	}
	index = max(0, min(index, len(m.Items)))
	newItems := make([]QueueCandidateItem, 0, len(m.Items)+len(items))
	newItems = append(newItems, m.Items[:index]...)
	newItems = append(newItems, items...)
	newItems = append(newItems, m.Items[index:]...)
	m.Items = newItems

	if m.CurrentQueueItemID == "" && len(m.Items) > 0 {
		m.CurrentQueueItemID = m.Items[0].QueueItemID
	}
}

func (m *QueueCandidateModel) findIndexByID(queueItemID string) int {
	for i, item := range m.Items {
		if item.QueueItemID == queueItemID {
			return i
		}
	}
	return -1
}

// RemoveByQueueItemID removes a specific queue instance by QueueItemID.
// If the removed item is the current item:
// - It prioritizes its successor (index + 1)
// - If removing the last item, it selects the remaining last item (does not loop to 0)
// - If the queue becomes empty, it clears CurrentQueueItemID.
func (m *QueueCandidateModel) RemoveByQueueItemID(queueItemID string) (string, bool) {
	idx := m.findIndexByID(queueItemID)
	if idx == -1 {
		return m.CurrentQueueItemID, false
	}

	if queueItemID == m.CurrentQueueItemID {
		switch {
		case idx < len(m.Items)-1:
			m.CurrentQueueItemID = m.Items[idx+1].QueueItemID
		case len(m.Items) > 1:
			m.CurrentQueueItemID = m.Items[len(m.Items)-2].QueueItemID
		default:
			m.CurrentQueueItemID = ""
		}
	}

	m.Items = append(m.Items[:idx], m.Items[idx+1:]...)
	return m.CurrentQueueItemID, true
}

// MoveByQueueItemID moves a track by QueueItemID to toIndex.
// Moving the currently playing item preserves CurrentQueueItemID without restarting playback.
func (m *QueueCandidateModel) MoveByQueueItemID(queueItemID string, toIndex int) bool {
	fromIdx := m.findIndexByID(queueItemID)
	if fromIdx == -1 {
		return false
	}

	toIndex = max(0, min(toIndex, len(m.Items)-1))
	if fromIdx == toIndex {
		return true
	}

	item := m.Items[fromIdx]
	remaining := make([]QueueCandidateItem, 0, len(m.Items)-1)
	remaining = append(remaining, m.Items[:fromIdx]...)
	remaining = append(remaining, m.Items[fromIdx+1:]...)

	newItems := make([]QueueCandidateItem, 0, len(m.Items))
	newItems = append(newItems, remaining[:toIndex]...)
	newItems = append(newItems, item)
	newItems = append(newItems, remaining[toIndex:]...)
	m.Items = newItems

	return true
}

func (m *QueueCandidateModel) FindByQueueItemID(queueItemID string) (int, *QueueCandidateItem, bool) {
	idx := m.findIndexByID(queueItemID)
	if idx == -1 {
		return -1, nil, false
	}
	return idx, &m.Items[idx], true
}

func (m *QueueCandidateModel) SetCurrentByQueueItemID(queueItemID string) bool {
	idx := m.findIndexByID(queueItemID)
	if idx != -1 {
		m.CurrentQueueItemID = queueItemID
		return true
	}
	return false
}

func (m *QueueCandidateModel) Clear() {
	m.Items = nil
	m.CurrentQueueItemID = ""
}

func (m *QueueCandidateModel) SetMode(mode PlaybackMode) {
	if !mode.valid() {
		mode = PlaybackModeOrder
	}
	m.Mode = mode
}

func randInt(r *rand.Rand, n int) int {
	if n <= 1 {
		return 0
	}
	if r != nil {
		return r.Intn(n)
	}
	//nolint:gosec // mode selection in tests and candidate model
	return rand.Intn(n)
}

func (m *QueueCandidateModel) pickRandomOther(curIdx int, r *rand.Rand) int {
	candidates := make([]int, 0, len(m.Items)-1)
	for i := range m.Items {
		if i != curIdx {
			candidates = append(candidates, i)
		}
	}
	return candidates[randInt(r, len(candidates))]
}

// Next selects the next queue item based on the active playback mode.
// In order, repeat_list, and repeat_track, manual next advances to the next track,
// wrapping around at the end of the queue. Single-repeat does not block manual skip.
// In natural end:
// - order stops at the end of the queue
// - repeat_list wraps around at the end
// - repeat_track replays the current track
// - shuffle selects randomly from remaining items (excluding current item) without reordering Items.
func (m *QueueCandidateModel) Next(manual bool, r *rand.Rand) (string, bool) {
	if len(m.Items) == 0 {
		return "", false
	}

	if len(m.Items) == 1 {
		if manual {
			m.CurrentQueueItemID = m.Items[0].QueueItemID
			return m.CurrentQueueItemID, true
		}
		if m.Mode == PlaybackModeOrder {
			return m.CurrentQueueItemID, false // stopped
		}
		m.CurrentQueueItemID = m.Items[0].QueueItemID
		return m.CurrentQueueItemID, true
	}

	curIdx := m.findIndexByID(m.CurrentQueueItemID)
	if curIdx == -1 {
		curIdx = 0
	}

	if m.Mode == PlaybackModeShuffle {
		pick := m.pickRandomOther(curIdx, r)
		m.CurrentQueueItemID = m.Items[pick].QueueItemID
		return m.CurrentQueueItemID, true
	}

	if m.Mode == PlaybackModeRepeatTrack {
		if manual {
			nextIdx := (curIdx + 1) % len(m.Items)
			m.CurrentQueueItemID = m.Items[nextIdx].QueueItemID
			return m.CurrentQueueItemID, true
		}
		// Natural end replays current item
		return m.CurrentQueueItemID, true
	}

	if m.Mode == PlaybackModeRepeatList {
		nextIdx := (curIdx + 1) % len(m.Items)
		m.CurrentQueueItemID = m.Items[nextIdx].QueueItemID
		return m.CurrentQueueItemID, true
	}

	// ORDER mode
	if manual {
		nextIdx := (curIdx + 1) % len(m.Items)
		m.CurrentQueueItemID = m.Items[nextIdx].QueueItemID
		return m.CurrentQueueItemID, true
	}

	if curIdx >= len(m.Items)-1 {
		return m.CurrentQueueItemID, false // stopped at end
	}
	m.CurrentQueueItemID = m.Items[curIdx+1].QueueItemID
	return m.CurrentQueueItemID, true
}

// Prev handles previous song intent.
// When currentTime > PrevRewindThresholdSeconds, it rewinds to 0 without switching tracks.
// When currentTime <= PrevRewindThresholdSeconds, non-random modes switch to previous track with wrap-around,
// and shuffle mode picks randomly from remaining items (excluding current item).
func (m *QueueCandidateModel) Prev(currentTime float64, r *rand.Rand) (string, bool) {
	if len(m.Items) == 0 {
		return "", false
	}

	if currentTime > PrevRewindThresholdSeconds {
		return m.CurrentQueueItemID, true // rewind only
	}

	if len(m.Items) == 1 {
		m.CurrentQueueItemID = m.Items[0].QueueItemID
		return m.CurrentQueueItemID, true
	}

	curIdx := m.findIndexByID(m.CurrentQueueItemID)
	if curIdx == -1 {
		curIdx = 0
	}

	if m.Mode == PlaybackModeShuffle {
		pick := m.pickRandomOther(curIdx, r)
		m.CurrentQueueItemID = m.Items[pick].QueueItemID
		return m.CurrentQueueItemID, true
	}

	prevIdx := (curIdx - 1 + len(m.Items)) % len(m.Items)
	m.CurrentQueueItemID = m.Items[prevIdx].QueueItemID
	return m.CurrentQueueItemID, true
}

// NaturalEnd advances the queue upon track natural completion.
func (m *QueueCandidateModel) NaturalEnd(r *rand.Rand) (string, bool) {
	return m.Next(false, r)
}
