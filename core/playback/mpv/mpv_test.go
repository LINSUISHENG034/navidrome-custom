package mpv

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("MPV", func() {
	var (
		testScript string
		tempDir    string
	)

	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())

		// Reset MPV cache
		mpvOnce = sync.Once{}
		mpvPath = ""
		mpvErr = nil

		// Create temporary directory for test files
		var err error
		tempDir, err = os.MkdirTemp("", "mpv_test_*")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { os.RemoveAll(tempDir) })

		// Create mock MPV script that outputs arguments to stdout
		testScript = createMockMPVScript(tempDir)

		// Configure test MPV path
		conf.Server.MPVPath = testScript
	})

	Describe("createMPVCommand", func() {
		Context("with default template", func() {
			BeforeEach(func() {
				conf.Server.MPVCmdTemplate = "mpv --audio-device=%d --no-audio-display --pause %f --input-ipc-server=%s"
			})

			It("creates correct command with simple paths", func() {
				args := createMPVCommand("auto", "/music/test.mp3", "/tmp/socket")
				Expect(args).To(Equal([]string{
					testScript,
					"--audio-device=auto",
					"--no-audio-display",
					"--pause",
					"/music/test.mp3",
					"--input-ipc-server=/tmp/socket",
				}))
			})

			It("handles paths with spaces", func() {
				args := createMPVCommand("auto", "/music/My Album/01 - Song.mp3", "/tmp/socket")
				Expect(args).To(Equal([]string{
					testScript,
					"--audio-device=auto",
					"--no-audio-display",
					"--pause",
					"/music/My Album/01 - Song.mp3",
					"--input-ipc-server=/tmp/socket",
				}))
			})

			It("handles complex device names", func() {
				deviceName := "coreaudio/AppleUSBAudioEngine:Cambridge Audio :Cambridge Audio USB Audio 1.0:0000:1"
				args := createMPVCommand(deviceName, "/music/test.mp3", "/tmp/socket")
				Expect(args).To(Equal([]string{
					testScript,
					"--audio-device=" + deviceName,
					"--no-audio-display",
					"--pause",
					"/music/test.mp3",
					"--input-ipc-server=/tmp/socket",
				}))
			})
		})

		Context("with snapcast template (issue #3619)", func() {
			BeforeEach(func() {
				// This is the template that fails with naive space splitting
				conf.Server.MPVCmdTemplate = "mpv --no-audio-display --pause %f --input-ipc-server=%s --audio-channels=stereo --audio-samplerate=48000 --audio-format=s16 --ao=pcm --ao-pcm-file=/audio/snapcast_fifo"
			})

			It("creates correct command for snapcast integration", func() {
				args := createMPVCommand("auto", "/music/test.mp3", "/tmp/socket")
				Expect(args).To(Equal([]string{
					testScript,
					"--no-audio-display",
					"--pause",
					"/music/test.mp3",
					"--input-ipc-server=/tmp/socket",
					"--audio-channels=stereo",
					"--audio-samplerate=48000",
					"--audio-format=s16",
					"--ao=pcm",
					"--ao-pcm-file=/audio/snapcast_fifo",
				}))
			})
		})

		Context("with wrapper script template", func() {
			BeforeEach(func() {
				// Test case that would break with naive splitting due to quoted arguments
				conf.Server.MPVCmdTemplate = `/tmp/mpv.sh --no-audio-display --pause %f --input-ipc-server=%s --audio-channels=stereo`
			})

			It("handles wrapper script paths", func() {
				args := createMPVCommand("auto", "/music/test.mp3", "/tmp/socket")
				Expect(args).To(Equal([]string{
					"/tmp/mpv.sh",
					"--no-audio-display",
					"--pause",
					"/music/test.mp3",
					"--input-ipc-server=/tmp/socket",
					"--audio-channels=stereo",
				}))
			})
		})

		Context("with extra spaces in template", func() {
			BeforeEach(func() {
				conf.Server.MPVCmdTemplate = "mpv    --audio-device=%d   --no-audio-display     --pause %f --input-ipc-server=%s"
			})

			It("handles extra spaces correctly", func() {
				args := createMPVCommand("auto", "/music/test.mp3", "/tmp/socket")
				Expect(args).To(Equal([]string{
					testScript,
					"--audio-device=auto",
					"--no-audio-display",
					"--pause",
					"/music/test.mp3",
					"--input-ipc-server=/tmp/socket",
				}))
			})
		})
		Context("with paths containing spaces in template arguments", func() {
			BeforeEach(func() {
				// Template with spaces in the path arguments themselves
				conf.Server.MPVCmdTemplate = `mpv --no-audio-display --pause %f --ao-pcm-file="/audio/my folder/snapcast_fifo" --input-ipc-server=%s`
			})

			It("handles spaces in quoted template argument paths", func() {
				args := createMPVCommand("auto", "/music/test.mp3", "/tmp/socket")
				// This test reveals the limitation of strings.Fields() - it will split on all spaces
				// Expected behavior would be to keep the path as one argument
				Expect(args).To(Equal([]string{
					testScript,
					"--no-audio-display",
					"--pause",
					"/music/test.mp3",
					"--ao-pcm-file=/audio/my folder/snapcast_fifo", // This should be one argument
					"--input-ipc-server=/tmp/socket",
				}))
			})
		})

		Context("with malformed template", func() {
			BeforeEach(func() {
				// Template with unmatched quotes that will cause shell parsing to fail
				conf.Server.MPVCmdTemplate = `mpv --no-audio-display --pause %f --input-ipc-server=%s --ao-pcm-file="/unclosed/quote`
			})

			It("returns nil when shell parsing fails", func() {
				args := createMPVCommand("auto", "/music/test.mp3", "/tmp/socket")
				Expect(args).To(BeNil())
			})
		})

		Context("with empty template", func() {
			BeforeEach(func() {
				conf.Server.MPVCmdTemplate = ""
			})

			It("returns empty slice for empty template", func() {
				args := createMPVCommand("auto", "/music/test.mp3", "/tmp/socket")
				Expect(args).To(BeEmpty())
			})
		})
	})

	Describe("start", func() {
		BeforeEach(func() {
			conf.Server.MPVCmdTemplate = "mpv --audio-device=%d --no-audio-display --pause %f --input-ipc-server=%s"
		})

		It("executes MPV command and captures arguments correctly", func() {
			tests.SkipOnWindows("mpv binary not available in CI (#TBD-mpv-windows)")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			deviceName := "auto"
			filename := "/music/test.mp3"
			socketName := "/tmp/test_socket"

			args := createMPVCommand(deviceName, filename, socketName)
			executor, err := start(ctx, args)
			Expect(err).ToNot(HaveOccurred())

			// Read all the output from stdout (this will block until the process finishes or is canceled)
			output, err := io.ReadAll(executor)
			Expect(err).ToNot(HaveOccurred())

			// Parse the captured arguments
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			Expect(lines).To(HaveLen(6))
			Expect(lines[0]).To(Equal(testScript))
			Expect(lines[1]).To(Equal("--audio-device=auto"))
			Expect(lines[2]).To(Equal("--no-audio-display"))
			Expect(lines[3]).To(Equal("--pause"))
			Expect(lines[4]).To(Equal("/music/test.mp3"))
			Expect(lines[5]).To(Equal("--input-ipc-server=/tmp/test_socket"))
		})

		It("handles file paths with spaces", func() {
			tests.SkipOnWindows("mpv binary not available in CI (#TBD-mpv-windows)")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			deviceName := "auto"
			filename := "/music/My Album/01 - My Song.mp3"
			socketName := "/tmp/test socket"

			args := createMPVCommand(deviceName, filename, socketName)
			executor, err := start(ctx, args)
			Expect(err).ToNot(HaveOccurred())

			// Read all the output from stdout (this will block until the process finishes or is canceled)
			output, err := io.ReadAll(executor)
			Expect(err).ToNot(HaveOccurred())

			// Parse the captured arguments
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			Expect(lines).To(ContainElement("/music/My Album/01 - My Song.mp3"))
			Expect(lines).To(ContainElement("--input-ipc-server=/tmp/test socket"))
		})

		Context("with complex snapcast configuration", func() {
			BeforeEach(func() {
				conf.Server.MPVCmdTemplate = "mpv --no-audio-display --pause %f --input-ipc-server=%s --audio-channels=stereo --audio-samplerate=48000 --audio-format=s16 --ao=pcm --ao-pcm-file=/audio/snapcast_fifo"
			})

			It("passes all snapcast arguments correctly", func() {
				tests.SkipOnWindows("mpv binary not available in CI (#TBD-mpv-windows)")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				deviceName := "auto"
				filename := "/music/album/track.flac"
				socketName := "/tmp/mpv-ctrl-test.socket"

				args := createMPVCommand(deviceName, filename, socketName)
				executor, err := start(ctx, args)
				Expect(err).ToNot(HaveOccurred())

				// Read all the output from stdout (this will block until the process finishes or is canceled)
				output, err := io.ReadAll(executor)
				Expect(err).ToNot(HaveOccurred())

				// Parse the captured arguments
				lines := strings.Split(strings.TrimSpace(string(output)), "\n")

				// Verify all expected arguments are present
				Expect(lines).To(ContainElement("--no-audio-display"))
				Expect(lines).To(ContainElement("--pause"))
				Expect(lines).To(ContainElement("/music/album/track.flac"))
				Expect(lines).To(ContainElement("--input-ipc-server=/tmp/mpv-ctrl-test.socket"))
				Expect(lines).To(ContainElement("--audio-channels=stereo"))
				Expect(lines).To(ContainElement("--audio-samplerate=48000"))
				Expect(lines).To(ContainElement("--audio-format=s16"))
				Expect(lines).To(ContainElement("--ao=pcm"))
				Expect(lines).To(ContainElement("--ao-pcm-file=/audio/snapcast_fifo"))
			})
		})

		Context("with nil args", func() {
			It("returns error when args is nil", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()

				_, err := start(ctx, nil)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no command arguments provided"))
			})

			It("returns error when args is empty", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()

				_, err := start(ctx, []string{})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no command arguments provided"))
			})
		})
	})

	Describe("mpvCommand", func() {
		BeforeEach(func() {
			// Reset the mpv command cache
			mpvOnce = sync.Once{}
			mpvPath = ""
			mpvErr = nil
		})

		It("finds the configured MPV path", func() {
			conf.Server.MPVPath = testScript
			path, err := mpvCommand()
			Expect(err).ToNot(HaveOccurred())
			Expect(path).To(Equal(testScript))
		})
	})

	Describe("NewTrack integration", func() {
		var testMediaFile model.MediaFile

		BeforeEach(func() {
			DeferCleanup(configtest.SetupConfig())
			conf.Server.MPVPath = testScript

			// Create a test media file
			testMediaFile = model.MediaFile{
				ID:   "test-id",
				Path: "/music/test.mp3",
			}
		})

		Context("with malformed template", func() {
			BeforeEach(func() {
				// Template with unmatched quotes that will cause shell parsing to fail
				conf.Server.MPVCmdTemplate = `mpv --no-audio-display --pause %f --input-ipc-server=%s --ao-pcm-file="/unclosed/quote`
			})

			It("returns error when createMPVCommand fails", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()

				events := make(chan model.PlaybackEvent, 1)
				_, err := NewTrack(ctx, events, 1, "auto", testMediaFile)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("no mpv command arguments provided"))
			})
		})
	})
})

// Public-state observations with null AO do not prove physical readiness or native output identity.
var _ = Describe("MPV null output prototype", func() {
	It("observes paused output without advancing and distinguishes file lifecycle events", func() {
		if os.Getenv("ND_TEST_MPV_NULL_OUTPUT") != "1" {
			Skip("Set ND_TEST_MPV_NULL_OUTPUT=1 to run the real MPV null-output prototype")
		}
		tests.SkipOnWindows("prototype uses a Unix IPC socket")

		path, err := exec.LookPath("mpv")
		Expect(err).ToNot(HaveOccurred())
		dir := GinkgoT().TempDir()
		first := filepath.Join(dir, "first.wav")
		second := filepath.Join(dir, "second.wav")
		Expect(writeNullOutputWAV(first)).To(Succeed())
		Expect(writeNullOutputWAV(second)).To(Succeed())
		socket := filepath.Join(dir, "mpv.socket")

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var processLog bytes.Buffer
		// Explicit null AO without fallback and no config/scripts must never use a host output.
		cmd := exec.CommandContext(ctx, path, nullOutputMPVArgs(socket)...)
		cmd.Stdout, cmd.Stderr = &processLog, &processLog
		DeferCleanup(func() {
			cancel()
			_ = cmd.Wait()
			fmt.Fprintf(GinkgoWriter, "MPV process output:\n%s\n", processLog.String())
		})
		Expect(cmd.Start()).To(Succeed())

		var conn net.Conn
		Eventually(func() error {
			conn, err = net.DialTimeout("unix", socket, 100*time.Millisecond)
			return err
		}, 3*time.Second, 20*time.Millisecond).Should(Succeed())
		DeferCleanup(func() { _ = conn.Close() })
		probe := &nullOutputProbe{conn: conn, decoder: json.NewDecoder(conn)}
		properties := []string{"current-ao", "audio-out-params", "pause", "time-pos", "idle-active"}
		inspect := func(stage string) map[string]any {
			state := make(map[string]any)
			for _, name := range properties {
				value, err := probe.command("get_property", name)
				if err != nil {
					state[name] = err.Error()
				} else {
					state[name] = value
				}
			}
			fmt.Fprintf(GinkgoWriter, "%s: %v\n", stage, state)
			return state
		}
		inspect("idle before content")

		_, err = probe.command("loadfile", first, "replace")
		Expect(err).ToNot(HaveOccurred())
		_, err = probe.waitEvent("file-loaded")
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() error {
			_, err := probe.command("get_property", "audio-out-params")
			return err
		}, 2*time.Second, 20*time.Millisecond).Should(Succeed())
		state := inspect("first loaded while paused")
		Expect(state["current-ao"]).To(Equal("null"))
		Expect(state["audio-out-params"]).To(HaveKeyWithValue("samplerate", float64(48000)))
		Expect(state["pause"]).To(BeTrue())
		Expect(state["time-pos"]).To(BeNumerically("<=", 0.001))
		Consistently(func() (any, error) {
			return probe.command("get_property", "time-pos")
		}, 300*time.Millisecond, 25*time.Millisecond).Should(Equal(state["time-pos"]))

		_, err = probe.command("set_property", "pause", false)
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() (any, error) {
			return probe.command("get_property", "time-pos")
		}, time.Second, 20*time.Millisecond).Should(BeNumerically(">", 0.05))
		_, err = probe.command("set_property", "pause", true)
		Expect(err).ToNot(HaveOccurred())
		_, err = probe.command("seek", 0.5, "absolute+exact")
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() (any, error) {
			return probe.command("get_property", "time-pos")
		}, time.Second, 20*time.Millisecond).Should(BeNumerically("~", 0.5, 0.01))
		Consistently(func() (any, error) {
			return probe.command("get_property", "time-pos")
		}, 300*time.Millisecond, 25*time.Millisecond).Should(BeNumerically("~", 0.5, 0.01))
		inspect("first paused after seek")

		_, err = probe.command("loadfile", second, "replace")
		Expect(err).ToNot(HaveOccurred())
		ended, err := probe.waitEvent("end-file")
		Expect(err).ToNot(HaveOccurred())
		Expect(ended["reason"]).To(Equal("stop"))
		_, err = probe.waitEvent("file-loaded")
		Expect(err).ToNot(HaveOccurred())
		state = inspect("second loaded in persistent process")
		Expect(state["current-ao"]).To(Equal("null"))
		Expect(state["pause"]).To(BeTrue())
		_, err = probe.command("set_property", "pause", false)
		Expect(err).ToNot(HaveOccurred())
		ended, err = probe.waitEvent("end-file")
		Expect(err).ToNot(HaveOccurred())
		Expect(ended["reason"]).To(Equal("eof"))
		state = inspect("idle after natural EOF")
		Expect(state["idle-active"]).To(BeTrue())

		_, err = probe.command("set_property", "pause", true)
		Expect(err).ToNot(HaveOccurred())
		_, err = probe.command("loadfile", first, "replace")
		Expect(err).ToNot(HaveOccurred())
		_, err = probe.waitEvent("file-loaded")
		Expect(err).ToNot(HaveOccurred())
		_, err = probe.command("stop")
		Expect(err).ToNot(HaveOccurred())
		ended, err = probe.waitEvent("end-file")
		Expect(err).ToNot(HaveOccurred())
		Expect(ended["reason"]).To(Equal("stop"))
		state = inspect("idle after explicit stop")
		Expect(state["idle-active"]).To(BeTrue())
		_, err = probe.command("quit")
		Expect(err).ToNot(HaveOccurred())
	})

	It("reports a transport fault with its token and position, then exits the old MPV", func() {
		if os.Getenv("ND_TEST_MPV_NULL_OUTPUT") != "1" {
			Skip("Set ND_TEST_MPV_NULL_OUTPUT=1 to run the real MPV null-output prototype")
		}
		tests.SkipOnWindows("prototype uses a Unix IPC socket")
		configureNullOutputMPV()
		dir := GinkgoT().TempDir()
		Expect(writeNullOutputWAV(filepath.Join(dir, "transport-fault.wav"))).To(Succeed())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		DeferCleanup(cancel)
		const token model.PlaybackToken = 42
		mediaFile := model.MediaFile{
			ID: "transport-fault", LibraryPath: dir, Path: "transport-fault.wav", Suffix: "wav",
		}
		events := make(chan model.PlaybackEvent, 2)
		track, err := NewTrack(ctx, events, token, "auto", mediaFile)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			cancel()
			track.Close()
			_ = track.Conn.Close()
			_ = track.Exe.Close()
		})

		conn, err := net.DialTimeout("unix", track.IPCSocketName, 100*time.Millisecond)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = conn.Close() })
		probe := &nullOutputProbe{conn: conn, decoder: json.NewDecoder(conn)}
		Eventually(func() (any, error) {
			return probe.command("get_property", "current-ao")
		}, 2*time.Second, 20*time.Millisecond).Should(Equal("null"))
		paused, err := probe.command("get_property", "pause")
		Expect(err).ToNot(HaveOccurred())
		Expect(paused).To(BeTrue())
		position, err := probe.command("get_property", "time-pos")
		Expect(err).ToNot(HaveOccurred())
		Expect(position).To(BeNumerically("<=", 0.001))
		Consistently(func() (any, error) {
			return probe.command("get_property", "time-pos")
		}, 200*time.Millisecond, 20*time.Millisecond).Should(Equal(position))
		Expect(track.SetPosition(1)).To(Succeed())
		track.Unpause()
		Eventually(func() (any, error) {
			return probe.command("get_property", "time-pos")
		}, time.Second, 20*time.Millisecond).Should(BeNumerically(">", 1.2))
		track.Pause()
		Expect(track.Position()).To(Equal(1))

		Expect(track.Conn.Close()).To(Succeed())
		var event model.PlaybackEvent
		select {
		case event = <-events:
		case <-time.After(trackCloseTimeout):
			Fail("IPC transport loss did not produce a bounded failure event")
		}
		Expect(event.Token).To(Equal(token))
		Expect(event.Kind).To(Equal(model.PlaybackEventFailure))
		Expect(event.Err).To(HaveOccurred())
		if event.Position == nil {
			Fail("IPC failure did not retain an observed recovery position")
			return
		}
		Expect(*event.Position).To(Equal(1))
		waitCtx, waitCancel := context.WithTimeout(context.Background(), trackCloseTimeout)
		DeferCleanup(waitCancel)
		Expect(track.Exe.Wait(waitCtx)).To(Succeed())
		_, err = probe.command("get_property", "pause")
		Expect(err).To(HaveOccurred())

		recoveredEvents := make(chan model.PlaybackEvent, 1)
		recovered, err := NewTrack(ctx, recoveredEvents, token+1, "auto", mediaFile)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(recovered.Close)
		Expect(recovered.SetPosition(*event.Position)).To(Succeed())
		Expect(recovered.Position()).To(Equal(1))
	})

	It("reports natural EOF with the active token while null MPV remains idle", func() {
		if os.Getenv("ND_TEST_MPV_NULL_OUTPUT") != "1" {
			Skip("Set ND_TEST_MPV_NULL_OUTPUT=1 to run the real MPV null-output prototype")
		}
		tests.SkipOnWindows("prototype uses a Unix IPC socket")
		configureNullOutputMPV()
		dir := GinkgoT().TempDir()
		Expect(writeNullOutputWAV(filepath.Join(dir, "natural-eof.wav"))).To(Succeed())
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		DeferCleanup(cancel)
		const token model.PlaybackToken = 43
		events := make(chan model.PlaybackEvent, 2)
		track, err := NewTrack(ctx, events, token, "auto", model.MediaFile{
			ID: "natural-eof", LibraryPath: dir, Path: "natural-eof.wav", Suffix: "wav",
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(track.Close)

		track.Unpause()
		var event model.PlaybackEvent
		select {
		case event = <-events:
		case <-ctx.Done():
			Fail("real MPV did not report natural EOF before the deadline")
		}
		Expect(event.Token).To(Equal(token))
		Expect(event.Kind).To(Equal(model.PlaybackEventNaturalEnd))
		Expect(event.Err).To(BeNil())
		idle, err := track.Conn.Call(ctx, "get_property", "idle-active")
		Expect(err).ToNot(HaveOccurred())
		Expect(idle).To(BeTrue())
		Expect(track.Conn.Err()).To(BeNil())
	})

	It("reports explicit close as retirement, never as natural EOF", func() {
		if os.Getenv("ND_TEST_MPV_NULL_OUTPUT") != "1" {
			Skip("Set ND_TEST_MPV_NULL_OUTPUT=1 to run the real MPV null-output prototype")
		}
		tests.SkipOnWindows("prototype uses a Unix IPC socket")
		configureNullOutputMPV()
		dir := GinkgoT().TempDir()
		Expect(writeNullOutputWAV(filepath.Join(dir, "explicit-close.wav"))).To(Succeed())
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		DeferCleanup(cancel)
		const token model.PlaybackToken = 44
		events := make(chan model.PlaybackEvent, 2)
		track, err := NewTrack(ctx, events, token, "auto", model.MediaFile{
			ID: "explicit-close", LibraryPath: dir, Path: "explicit-close.wav", Suffix: "wav",
		})
		Expect(err).ToNot(HaveOccurred())
		track.Pause()
		Consistently(events, 100*time.Millisecond).ShouldNot(Receive())
		closedAt := time.Now()
		track.Close()
		Expect(time.Since(closedAt)).To(BeNumerically("<", trackCloseTimeout))
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		DeferCleanup(waitCancel)
		Expect(track.Exe.Wait(waitCtx)).To(Succeed())
		var event model.PlaybackEvent
		select {
		case event = <-events:
		case <-ctx.Done():
			Fail("explicit close did not report retirement before the deadline")
		}
		Expect(event.Token).To(Equal(token))
		Expect(event.Kind).To(Equal(model.PlaybackEventRetired))
	})

	It("releases the output process when its context is canceled", func() {
		if os.Getenv("ND_TEST_MPV_NULL_OUTPUT") != "1" {
			Skip("Set ND_TEST_MPV_NULL_OUTPUT=1 to run the real MPV null-output prototype")
		}
		tests.SkipOnWindows("prototype uses a Unix IPC socket")
		configureNullOutputMPV()
		dir := GinkgoT().TempDir()
		Expect(writeNullOutputWAV(filepath.Join(dir, "context-close.wav"))).To(Succeed())
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		DeferCleanup(cancel)
		const token model.PlaybackToken = 45
		events := make(chan model.PlaybackEvent, 2)
		track, err := NewTrack(ctx, events, token, "auto", model.MediaFile{
			ID: "context-close", LibraryPath: dir, Path: "context-close.wav", Suffix: "wav",
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(track.Close)
		cancel()
		var event model.PlaybackEvent
		select {
		case event = <-events:
		case <-time.After(trackCloseTimeout):
			Fail("context cancellation did not report bounded retirement")
		}
		Expect(event.Token).To(Equal(token))
		Expect(event.Kind).To(Equal(model.PlaybackEventRetired))
		waitCtx, waitCancel := context.WithTimeout(context.Background(), trackCloseTimeout)
		DeferCleanup(waitCancel)
		Expect(track.Exe.Wait(waitCtx)).To(Succeed())
	})
})

func configureNullOutputMPV() {
	DeferCleanup(configtest.SetupConfig())
	mpvOnce = sync.Once{}
	mpvPath, mpvErr = "", nil
	DeferCleanup(func() {
		mpvOnce = sync.Once{}
		mpvPath, mpvErr = "", nil
	})
	path, err := exec.LookPath("mpv")
	Expect(err).ToNot(HaveOccurred())
	conf.Server.MPVPath = path
	args := append([]string{"mpv"}, nullOutputMPVArgs("%s")...)
	conf.Server.MPVCmdTemplate = strings.Join(append(args, "%f"), " ")
}

func nullOutputMPVArgs(socket string) []string {
	return []string{
		"--no-config", "--load-scripts=no", "--no-video", "--vo=null", "--no-audio-display",
		"--ao=null", "--audio-fallback-to-null=no", "--idle=yes", "--pause=yes",
		"--gapless-audio=yes", "--audio-samplerate=48000", "--audio-format=s16", "--audio-channels=stereo",
		"--input-ipc-server=" + socket,
	}
}

// This IPC probe retains real events and bounds every exchange; no ready events are fabricated.
type nullOutputProbe struct {
	conn      net.Conn
	decoder   *json.Decoder
	requestID int
	events    []map[string]any
}

func (p *nullOutputProbe) command(args ...any) (any, error) {
	p.requestID++
	if err := p.conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, err
	}
	if err := json.NewEncoder(p.conn).Encode(map[string]any{"command": args, "request_id": p.requestID}); err != nil {
		return nil, err
	}
	for {
		var message map[string]any
		if err := p.decoder.Decode(&message); err != nil {
			return nil, err
		}
		if message["event"] != nil {
			p.events = append(p.events, message)
			fmt.Fprintf(GinkgoWriter, "MPV event: %v\n", message)
			continue
		}
		if message["request_id"] == float64(p.requestID) {
			if message["error"] != "success" {
				return nil, fmt.Errorf("MPV %v: %v", args, message["error"])
			}
			return message["data"], nil
		}
	}
}

func (p *nullOutputProbe) waitEvent(name string) (map[string]any, error) {
	if err := p.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, err
	}
	for {
		for i, event := range p.events {
			if event["event"] == name {
				p.events = append(p.events[:i], p.events[i+1:]...)
				return event, nil
			}
		}
		var event map[string]any
		if err := p.decoder.Decode(&event); err != nil {
			return nil, err
		}
		if event["event"] != nil {
			p.events = append(p.events, event)
			fmt.Fprintf(GinkgoWriter, "MPV event: %v\n", event)
		}
	}
}

func writeNullOutputWAV(path string) error {
	const sampleRate, channels, bits, seconds = 8000, 1, 16, 2
	dataSize := sampleRate * channels * (bits / 8) * seconds
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataSize))
	copy(header[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], channels)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], sampleRate*channels*(bits/8))
	binary.LittleEndian.PutUint16(header[32:34], channels*(bits/8))
	binary.LittleEndian.PutUint16(header[34:36], bits)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))
	return os.WriteFile(path, append(header, make([]byte, dataSize)...), 0600)
}

// createMockMPVScript creates a mock script that outputs arguments to stdout
func createMockMPVScript(tempDir string) string {
	var scriptContent string
	var scriptExt string

	if runtime.GOOS == "windows" {
		scriptExt = ".bat"
		scriptContent = `@echo off
echo %0
:loop
if "%~1"=="" goto end
echo %~1
shift
goto loop
:end
`
	} else {
		scriptExt = ".sh"
		scriptContent = `#!/bin/sh
echo "$0"
for arg in "$@"; do
    echo "$arg"
done
`
	}

	scriptPath := filepath.Join(tempDir, "mock_mpv"+scriptExt)
	err := os.WriteFile(scriptPath, []byte(scriptContent), 0755) // nolint:gosec
	if err != nil {
		panic(fmt.Sprintf("Failed to create mock script: %v", err))
	}

	return scriptPath
}
