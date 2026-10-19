package playback

import (
	"github.com/navidrome/navidrome/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Queues", func() {
	var queue *Queue

	BeforeEach(func() {
		queue = NewQueue()
	})

	Describe("use empty queue", func() {
		It("is empty", func() {
			Expect(queue.Items).To(BeEmpty())
			Expect(queue.Index).To(Equal(-1))
		})
	})

	Describe("Operate on small queue", func() {
		BeforeEach(func() {
			mfs := model.MediaFiles{
				{
					ID: "1", Artist: "Queen", Compilation: false, Path: "/music1/hammer.mp3",
				},
				{
					ID: "2", Artist: "Vinyard Rose", Compilation: false, Path: "/music1/cassidy.mp3",
				},
			}
			queue.Add(mfs)
		})

		It("contains the preloaded data", func() {
			Expect(queue.Get).ToNot(BeNil())
			Expect(queue.Size()).To(Equal(2))
		})

		It("could read data by ID", func() {
			idx, err := queue.getMediaFileIndexByID("1")
			Expect(err).ToNot(HaveOccurred())
			Expect(idx).ToNot(BeNil())
			Expect(idx).To(Equal(0))

			queue.SetIndex(idx)

			mf := queue.Current()

			Expect(mf).ToNot(BeNil())
			Expect(mf.ID).To(Equal("1"))
			Expect(mf.Artist).To(Equal("Queen"))
			Expect(mf.Path).To(Equal("/music1/hammer.mp3"))
		})
	})

	Describe("Read/Write operations", func() {
		BeforeEach(func() {
			mfs := model.MediaFiles{
				{
					ID: "1", Artist: "Queen", Compilation: false, Path: "/music1/hammer.mp3",
				},
				{
					ID: "2", Artist: "Vinyard Rose", Compilation: false, Path: "/music1/cassidy.mp3",
				},
				{
					ID: "3", Artist: "Pink Floyd", Compilation: false, Path: "/music1/time.mp3",
				},
				{
					ID: "4", Artist: "Mike Oldfield", Compilation: false, Path: "/music1/moonlight-shadow.mp3",
				},
				{
					ID: "5", Artist: "Red Hot Chili Peppers", Compilation: false, Path: "/music1/californication.mp3",
				},
			}
			queue.Add(mfs)
		})

		It("contains the preloaded data", func() {
			Expect(queue.Get).ToNot(BeNil())
			Expect(queue.Size()).To(Equal(5))
		})

		It("could read data by ID", func() {
			idx, err := queue.getMediaFileIndexByID("5")
			Expect(err).ToNot(HaveOccurred())
			Expect(idx).ToNot(BeNil())
			Expect(idx).To(Equal(4))

			queue.SetIndex(idx)

			mf := queue.Current()

			Expect(mf).ToNot(BeNil())
			Expect(mf.ID).To(Equal("5"))
			Expect(mf.Artist).To(Equal("Red Hot Chili Peppers"))
			Expect(mf.Path).To(Equal("/music1/californication.mp3"))
		})

		It("could shuffle the data correctly", func() {
			queue.Shuffle()
			Expect(queue.Size()).To(Equal(5))
		})

		It("could remove entries correctly", func() {
			queue.Remove(0)
			Expect(queue.Size()).To(Equal(4))

			queue.Remove(3)
			Expect(queue.Size()).To(Equal(3))
		})

		It("clear the whole thing on request", func() {
			Expect(queue.Size()).To(Equal(5))
			queue.Clear()
			Expect(queue.Size()).To(Equal(0))
		})
	})

	Describe("Move operation", func() {
		BeforeEach(func() {
			mfs := model.MediaFiles{
				{ID: "1", Path: "/a.mp3"},
				{ID: "2", Path: "/b.mp3"},
				{ID: "3", Path: "/c.mp3"},
				{ID: "4", Path: "/d.mp3"},
			}
			queue.Add(mfs)
			queue.SetIndex(1) // currently playing ID "2"
		})

		It("moves a track forward without affecting current", func() {
			queue.Move(0, 2) // move ID "1" from 0 to 2
			Expect(queue.Items[0].ID).To(Equal("2"))
			Expect(queue.Items[1].ID).To(Equal("3"))
			Expect(queue.Items[2].ID).To(Equal("1"))
			Expect(queue.Items[3].ID).To(Equal("4"))
			Expect(queue.Index).To(Equal(0))
		})

		It("moves a track backward without affecting current", func() {
			queue.Move(3, 0) // move ID "4" from 3 to 0
			Expect(queue.Items[0].ID).To(Equal("4"))
			Expect(queue.Items[1].ID).To(Equal("1"))
			Expect(queue.Items[2].ID).To(Equal("2"))
			Expect(queue.Items[3].ID).To(Equal("3"))
			Expect(queue.Index).To(Equal(2))
		})

		It("moves the current track itself", func() {
			queue.Move(1, 3) // move current ID "2" from 1 to 3
			Expect(queue.Items[3].ID).To(Equal("2"))
			Expect(queue.Index).To(Equal(3))
		})
	})

	Describe("Remove with index adjustment", func() {
		BeforeEach(func() {
			mfs := model.MediaFiles{
				{ID: "1", Path: "/a.mp3"},
				{ID: "2", Path: "/b.mp3"},
				{ID: "3", Path: "/c.mp3"},
			}
			queue.Add(mfs)
			queue.SetIndex(2) // currently playing ID "3"
		})

		It("adjusts index when removing before current", func() {
			queue.Remove(0) // remove ID "1"
			Expect(queue.Size()).To(Equal(2))
			Expect(queue.Index).To(Equal(1))
			Expect(queue.Current().ID).To(Equal("3"))
		})

		It("sets index to -1 when removing the current track", func() {
			queue.Remove(2) // remove current ID "3"
			Expect(queue.Size()).To(Equal(2))
			Expect(queue.Index).To(Equal(-1))
		})
	})

})

var _ = Describe("Queue operations with duplicate IDs", func() {
	var queue *Queue

	BeforeEach(func() {
		queue = NewQueue()
	})

	It("inserts tracks at a requested index", func() {
		queue.Add(model.MediaFiles{{ID: "a", Path: "/a.mp3"}, {ID: "c", Path: "/c.mp3"}})

		queue.Insert(1, model.MediaFiles{{ID: "b", Path: "/b.mp3"}})

		Expect(queue.Items).To(HaveLen(3))
		Expect(queue.Items[0].ID).To(Equal("a"))
		Expect(queue.Items[1].ID).To(Equal("b"))
		Expect(queue.Items[2].ID).To(Equal("c"))
	})

	It("keeps the current duplicate instance when removing before it", func() {
		queue.Add(model.MediaFiles{{ID: "a", Path: "/a1.mp3"}, {ID: "b", Path: "/b.mp3"}, {ID: "a", Path: "/a2.mp3"}})
		queue.SetIndex(2)

		queue.Remove(1)

		Expect(queue.Index).To(Equal(1))
		Expect(queue.Current()).ToNot(BeNil())
		Expect(queue.Current().Path).To(Equal("/a2.mp3"))
	})

	It("keeps the current duplicate instance when moving another track", func() {
		queue.Add(model.MediaFiles{{ID: "a", Path: "/a1.mp3"}, {ID: "b", Path: "/b.mp3"}, {ID: "a", Path: "/a2.mp3"}, {ID: "c", Path: "/c.mp3"}})
		queue.SetIndex(2)

		queue.Move(3, 1)

		Expect(queue.Index).To(Equal(3))
		Expect(queue.Current()).ToNot(BeNil())
		Expect(queue.Current().Path).To(Equal("/a2.mp3"))
	})
})

var _ = Describe("QueueCandidateModel with stable QueueItemID (Issue 03)", func() {
	var modelQueue *QueueCandidateModel

	BeforeEach(func() {
		modelQueue = NewQueueCandidateModel()
	})

	It("separates track identity from queue instance identity with duplicate tracks", func() {
		modelQueue.Add([]QueueCandidateItem{
			{QueueItemID: "qitem-1", MediaFile: model.MediaFile{ID: "track-A", Title: "Song A"}},
			{QueueItemID: "qitem-2", MediaFile: model.MediaFile{ID: "track-B", Title: "Song B"}},
			{QueueItemID: "qitem-3", MediaFile: model.MediaFile{ID: "track-A", Title: "Song A"}},
		})

		Expect(modelQueue.Size()).To(Equal(3))
		Expect(modelQueue.CurrentQueueItemID).To(Equal("qitem-1"))
		Expect(modelQueue.Current().MediaFile.ID).To(Equal("track-A"))

		idx1, item1, found1 := modelQueue.FindByQueueItemID("qitem-1")
		Expect(found1).To(BeTrue())
		Expect(idx1).To(Equal(0))
		Expect(item1.QueueItemID).To(Equal("qitem-1"))

		idx3, item3, found3 := modelQueue.FindByQueueItemID("qitem-3")
		Expect(found3).To(BeTrue())
		Expect(idx3).To(Equal(2))
		Expect(item3.QueueItemID).To(Equal("qitem-3"))
	})

	It("removes specific duplicate instance without affecting other instances of the same track", func() {
		modelQueue.Add([]QueueCandidateItem{
			{QueueItemID: "qitem-1", MediaFile: model.MediaFile{ID: "track-A", Title: "Song A"}},
			{QueueItemID: "qitem-2", MediaFile: model.MediaFile{ID: "track-B", Title: "Song B"}},
			{QueueItemID: "qitem-3", MediaFile: model.MediaFile{ID: "track-A", Title: "Song A"}},
		})

		nextID, removed := modelQueue.RemoveByQueueItemID("qitem-3")
		Expect(removed).To(BeTrue())
		Expect(nextID).To(Equal("qitem-1"))
		Expect(modelQueue.Size()).To(Equal(2))

		// Instance 1 remains untouched at index 0
		idx1, _, found1 := modelQueue.FindByQueueItemID("qitem-1")
		Expect(found1).To(BeTrue())
		Expect(idx1).To(Equal(0))

		// Instance 3 is gone
		_, _, found3 := modelQueue.FindByQueueItemID("qitem-3")
		Expect(found3).To(BeFalse())
	})

	It("selects successor when removing currently playing item", func() {
		modelQueue.Add([]QueueCandidateItem{
			{QueueItemID: "qitem-1", MediaFile: model.MediaFile{ID: "track-A"}},
			{QueueItemID: "qitem-2", MediaFile: model.MediaFile{ID: "track-B"}},
			{QueueItemID: "qitem-3", MediaFile: model.MediaFile{ID: "track-C"}},
		})
		modelQueue.SetCurrentByQueueItemID("qitem-2") // currently playing middle item

		nextID, removed := modelQueue.RemoveByQueueItemID("qitem-2")
		Expect(removed).To(BeTrue())
		Expect(nextID).To(Equal("qitem-3"))
		Expect(modelQueue.CurrentQueueItemID).To(Equal("qitem-3"))
		Expect(modelQueue.Size()).To(Equal(2))
	})

	It("selects remaining last item when removing last item (does not loop to 0)", func() {
		modelQueue.Add([]QueueCandidateItem{
			{QueueItemID: "qitem-1", MediaFile: model.MediaFile{ID: "track-A"}},
			{QueueItemID: "qitem-2", MediaFile: model.MediaFile{ID: "track-B"}},
			{QueueItemID: "qitem-3", MediaFile: model.MediaFile{ID: "track-C"}},
		})
		modelQueue.SetCurrentByQueueItemID("qitem-3") // currently playing end item

		nextID, removed := modelQueue.RemoveByQueueItemID("qitem-3")
		Expect(removed).To(BeTrue())
		Expect(nextID).To(Equal("qitem-2")) // remaining last item, NOT qitem-1
		Expect(modelQueue.CurrentQueueItemID).To(Equal("qitem-2"))
		Expect(modelQueue.Size()).To(Equal(2))
	})

	It("moving currently playing item preserves current queue item identity", func() {
		modelQueue.Add([]QueueCandidateItem{
			{QueueItemID: "qitem-1", MediaFile: model.MediaFile{ID: "track-A"}},
			{QueueItemID: "qitem-2", MediaFile: model.MediaFile{ID: "track-B"}},
			{QueueItemID: "qitem-3", MediaFile: model.MediaFile{ID: "track-C"}},
		})
		modelQueue.SetCurrentByQueueItemID("qitem-1")

		moved := modelQueue.MoveByQueueItemID("qitem-1", 2)
		Expect(moved).To(BeTrue())
		Expect(modelQueue.CurrentQueueItemID).To(Equal("qitem-1"))
		Expect(modelQueue.Items[2].QueueItemID).To(Equal("qitem-1"))
	})

	It("clearing queue empties items and resets current queue item identity", func() {
		modelQueue.Add([]QueueCandidateItem{
			{QueueItemID: "qitem-1", MediaFile: model.MediaFile{ID: "track-A"}},
		})
		Expect(modelQueue.IsEmpty()).To(BeFalse())

		modelQueue.Clear()
		Expect(modelQueue.IsEmpty()).To(BeTrue())
		Expect(modelQueue.CurrentQueueItemID).To(Equal(""))
		Expect(modelQueue.Current()).To(BeNil())
	})
})

