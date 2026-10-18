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
