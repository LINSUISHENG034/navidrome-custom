package mpv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/model"
)

const pipewireFixtureTarget = "bluez_output.validation.a2dp-sink"

func TestMain(m *testing.M) {
	if os.Getenv("ND_TEST_PIPEWIRE_DIRECTORY") != "" {
		switch role := filepath.Base(os.Args[0]); role {
		case "mpv", "pw-dump", "pw-cli", "pw-link":
			if err := os.Setenv("ND_TEST_PIPEWIRE_ROLE", role); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			os.Args = append([]string{os.Args[0], "-test.run=^TestPipeWireProcessFixture$", "--"}, os.Args[1:]...)
		}
	}
	os.Exit(m.Run())
}

func TestPipeWireNewTrackRejectsMissingTargetDespiteReadyDriver(t *testing.T) {
	assertPipeWirePreparationRejected(t, "missing-target", "NewTrack admitted a ready-looking Pulse output although the independent graph has no requested Bluetooth sink")
}

func TestPipeWireNewTrackRejectsUnlinkedTargetDespiteReadyDriver(t *testing.T) {
	assertPipeWirePreparationRejected(t, "unlinked-target", "NewTrack admitted a ready-looking Pulse output although no actual links connect its stream to the requested Bluetooth sink")
}

func TestPipeWireNewTrackCreatesOwnedChannelLinksBeforeReadyAndReleasesOnClose(t *testing.T) {
	track, err, dir := newPipeWireFixtureTrack(t, "bindable-target")
	if err != nil {
		t.Fatalf("NewTrack could not prepare the available target through the external link executor: %v", err)
	}
	if track == nil {
		t.Fatal("successful output preparation returned no track")
	}
	assertPipeWireFixtureCompleteChannelLinks(t, dir)
	track.Close()
	assertPipeWireFixtureReleased(t, dir, true)
}

func TestPipeWireNewTrackNeverAdmitsPartialStereoAfterLateChannelAppears(t *testing.T) {
	assertPipeWireFixtureNeverPartiallyReady(t, "late-channel")
}

func TestPipeWireNewTrackRejectsStaticPartialStereoLayout(t *testing.T) {
	assertPipeWireFixtureNeverPartiallyReady(t, "static-partial-channel")
}

func assertPipeWireFixtureNeverPartiallyReady(t *testing.T, scenario string) {
	t.Helper()
	track, err, dir := newPipeWireFixtureTrack(t, scenario)
	if err != nil {
		if track != nil {
			t.Fatal("failed partial-stereo preparation returned an active track")
		}
		assertPipeWireFixtureReleased(t, dir, false)
		return
	}
	if track == nil {
		t.Fatal("successful partial-stereo preparation returned no track")
	}
	assertPipeWireFixtureCompleteChannelLinks(t, dir)
	track.Close()
	assertPipeWireFixtureReleased(t, dir, true)
}

func assertPipeWireFixtureCompleteChannelLinks(t *testing.T, dir string) {
	t.Helper()
	graph := readPipeWireFixtureGraph(t, dir)
	channels := map[int]bool{}
	for _, object := range graph {
		if object["type"] != "PipeWire:Interface:Link" {
			continue
		}
		info := object["info"].(map[string]any)
		if info["output-node-id"] != float64(10) || info["input-node-id"] != float64(30) {
			t.Fatalf("ready output has a link to an unexpected node: %v", info)
		}
		outputPort := int(info["output-port-id"].(float64))
		if (outputPort != 11 && outputPort != 12) || info["input-port-id"] != float64(outputPort+20) {
			t.Fatalf("ready output has an incorrect channel link: %v", info)
		}
		channels[outputPort] = true
	}
	if len(channels) != 2 {
		t.Fatalf("NewTrack returned ready without both independently observed channel links: %v", channels)
	}
}

func assertPipeWireFixtureReleased(t *testing.T, dir string, requireOwner bool) {
	t.Helper()
	assertPipeWireFixtureProcessRetired(t, dir, "mpv.pid")
	_, err := os.Stat(filepath.Join(dir, "pw-cli.pid"))
	if requireOwner || err == nil {
		assertPipeWireFixtureProcessRetired(t, dir, "pw-cli.pid")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, object := range readPipeWireFixtureGraph(t, dir) {
		if object["type"] == "PipeWire:Interface:Link" {
			t.Fatal("Close left a client-owned link in the independently observed graph")
		}
	}
}

func assertPipeWirePreparationRejected(t *testing.T, scenario, admissionFailure string) {
	t.Helper()
	track, err, dir := newPipeWireFixtureTrack(t, scenario)
	if err == nil {
		if track != nil {
			track.Close()
		}
		t.Fatal(admissionFailure)
	}
	if track != nil {
		t.Error("failed output preparation returned an active track")
	}
	assertPipeWireFixtureProcessRetired(t, dir, "mpv.pid")
}

func newPipeWireFixtureTrack(t *testing.T, scenario string) (*MpvTrack, error, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("PipeWire output and process retirement observations require Linux")
	}
	t.Cleanup(configtest.SetupConfig())
	conf.Server.Jukebox.OutputPreparationTimeout = 3 * time.Second
	conf.Server.Jukebox.OutputPollInterval = 10 * time.Millisecond
	dir := configurePipeWireFixture(t, scenario)
	conf.Server.MPVCmdTemplate = "mpv --no-config --ao=pulse --audio-device=%d --pause --input-ipc-server=%s %f"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	events := make(chan model.PlaybackEvent, 4)
	track, err := NewTrack(ctx, events, 71, "pulse/"+pipewireFixtureTarget, model.MediaFile{
		ID: "missing-output", Path: filepath.Join(dir, "not-decoded.wav"),
	})
	if track != nil {
		t.Cleanup(track.Close)
	}
	t.Cleanup(func() { assertPipeWireFixtureSafeCommands(t, dir) })
	return track, err, dir
}

func configurePipeWireFixture(t *testing.T, scenario string) string {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ND_TEST_PIPEWIRE_DIRECTORY", dir)
	t.Setenv("ND_TEST_PIPEWIRE_SCENARIO", scenario)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, role := range []string{"mpv", "pw-dump", "pw-link", "pw-cli"} {
		if err := os.Symlink(executable, filepath.Join(dir, role)); err != nil {
			t.Fatal(err)
		}
	}
	conf.Server.MPVPath = filepath.Join(dir, "mpv")
	resetPipeWireTestMPVCache(t)
	return dir
}

func resetPipeWireTestMPVCache(t *testing.T) {
	t.Helper()
	mpvOnce = sync.Once{}
	mpvPath, mpvErr = "", nil
	t.Cleanup(func() {
		mpvOnce = sync.Once{}
		mpvPath, mpvErr = "", nil
	})
}

func TestPipeWirePrivateOutput(t *testing.T) {
	if os.Getenv("ND_TEST_PRIVATE_PIPEWIRE") != "1" {
		t.Skip("set ND_TEST_PRIVATE_PIPEWIRE=1 only after reviewing the private-daemon safety contract")
	}
	if runtime.GOOS != "linux" {
		t.Skip("private PipeWire protocol integration requires Linux")
	}
	t.Cleanup(configtest.SetupConfig())
	root := t.TempDir()
	for _, relative := range []string{"h", "c", "s", "k", "r", "r/p", "t", "l"} {
		if err := os.Mkdir(privatePipeWirePath(t, root, relative), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Lstat(root); err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatalf("private root must be owned-test storage with mode 0700: %v", err)
	}
	coreSocket := privatePipeWirePath(t, root, "r/nd-private")
	pulseSocket := privatePipeWirePath(t, root, "r/p/native")
	for _, socket := range []string{coreSocket, pulseSocket, privatePipeWirePath(t, root, "t/mpv-ctrl-"+strings.Repeat("x", 32)+".socket")} {
		if len(socket) > 100 {
			t.Fatalf("private socket path exceeds the conservative Unix limit: %q", socket)
		}
	}
	nonce := strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	coreName, pulseName, clientName := "nd-core-"+nonce+".conf", "nd-pulse-"+nonce+".conf", "nd-client-"+nonce+".conf"
	coreModules := []string{
		"libpipewire-module-protocol-native", "libpipewire-module-spa-node-factory",
		"libpipewire-module-client-node", "libpipewire-module-adapter",
		"libpipewire-module-link-factory", "libpipewire-module-access",
	}
	clientModules := []string{"libpipewire-module-protocol-native", "libpipewire-module-client-node", "libpipewire-module-adapter"}
	pulseModules := append(append([]string(nil), clientModules...), fmt.Sprintf("libpipewire-module-protocol-pulse args = { server.address = [ %q ] }", "unix:"+pulseSocket))
	const target = "bluez_output.private_validation.a2dp-sink"
	coreObjects := fmt.Sprintf(`context.objects = [
    { factory = spa-node-factory args = {
        factory.name = support.node.driver node.name = Dummy-Driver
        node.group = pipewire.dummy priority.driver = 20000
    } }
    { factory = adapter args = {
        factory.name = support.null-audio-sink node.name = %s media.class = Audio/Sink
        audio.channels = 2 audio.rate = 48000 audio.position = [ FL FR ]
        adapter.auto-port-config = { mode = dsp monitor = false position = preserve }
    } }
]`, target)
	configs := map[string]string{
		coreName: privatePipeWireConfig("core.daemon = true core.name = nd-private default.clock.rate = 48000", coreModules, coreObjects),
		pulseName: privatePipeWireConfig("core.daemon = false core.name = nd-private-pulse", pulseModules,
			"stream.properties = { adapter.auto-port-config = { mode = dsp monitor = false position = preserve } }"),
		clientName:                        privatePipeWireConfig("core.daemon = false core.name = nd-private-client", clientModules, ""),
		"pulse-client-" + nonce + ".conf": "autospawn = no\n",
	}
	for name, data := range configs {
		if err := os.WriteFile(privatePipeWirePath(t, root, filepath.Join("c", name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	privatePipeWireEnvironment(t, map[string]string{
		"HOME": privatePipeWirePath(t, root, "h"), "PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C",
		"XDG_CONFIG_HOME": privatePipeWirePath(t, root, "c"), "XDG_CONFIG_DIRS": privatePipeWirePath(t, root, "c"),
		"XDG_STATE_HOME": privatePipeWirePath(t, root, "s"), "XDG_CACHE_HOME": privatePipeWirePath(t, root, "k"),
		"XDG_RUNTIME_DIR": privatePipeWirePath(t, root, "r"), "PIPEWIRE_RUNTIME_DIR": privatePipeWirePath(t, root, "r"),
		"PIPEWIRE_CONFIG_DIR": privatePipeWirePath(t, root, "c"), "PIPEWIRE_CONFIG_PREFIX": "", "PIPEWIRE_CONFIG_NAME": clientName,
		"PIPEWIRE_REMOTE": "nd-private", "PULSE_RUNTIME_PATH": privatePipeWirePath(t, root, "r/p"), "PULSE_SERVER": "unix:" + pulseSocket,
		"PULSE_CLIENTCONFIG":       privatePipeWirePath(t, root, "c/pulse-client-"+nonce+".conf"),
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + privatePipeWirePath(t, root, "r/no-session-bus"),
		"DBUS_SYSTEM_BUS_ADDRESS":  "unix:path=" + privatePipeWirePath(t, root, "r/no-system-bus"),
		"TMPDIR":                   privatePipeWirePath(t, root, "t"),
	})
	lifetime, cancelLifetime := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelLifetime)
	core := startPrivatePipeWireDaemon(t, lifetime, root, "/usr/bin/pipewire", coreName)
	waitPrivatePipeWireSocket(t, core, coreSocket)
	assertPrivatePipeWireGraph(t, privatePipeWireGraph(t, root), coreModules)
	pulse := startPrivatePipeWireDaemon(t, lifetime, root, "/usr/bin/pipewire-pulse", pulseName)
	waitPrivatePipeWireSocket(t, pulse, pulseSocket)
	assertPrivatePipeWireGraph(t, privatePipeWireGraph(t, root), coreModules)

	wav := privatePipeWirePath(t, root, "t/source.wav")
	if err := writeNullOutputWAV(wav); err != nil {
		t.Fatal(err)
	}
	conf.Server.MPVPath = "/usr/bin/mpv"
	conf.Server.MPVCmdTemplate = "mpv --no-config --load-scripts=no --no-video --vo=null --ao=pulse --audio-device=%d --pause --audio-channels=stereo --audio-samplerate=48000 --input-ipc-server=%s %f"
	conf.Server.Jukebox.OutputPreparationTimeout = 3 * time.Second
	conf.Server.Jukebox.OutputPollInterval = 10 * time.Millisecond
	resetPipeWireTestMPVCache(t)
	ctx, cancel := context.WithTimeout(lifetime, 10*time.Second)
	t.Cleanup(cancel)
	events := make(chan model.PlaybackEvent, 4)
	track, err := NewTrack(ctx, events, 91, "pulse/"+target, model.MediaFile{ID: "private-output", Path: wav})
	if err != nil {
		t.Fatalf("private protocol preparation failed: %v", err)
	}
	t.Cleanup(track.Close)
	if track.IsPlaying() {
		t.Fatal("private protocol preparation unpaused the music")
	}
	graph := privatePipeWireGraph(t, root)
	assertPrivatePipeWireGraph(t, graph, coreModules)
	streamName, token := assertPrivatePipeWireChannelLinks(t, graph, target)
	entries, err := os.ReadDir(privatePipeWirePath(t, root, "t"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".socket") {
			socket := privatePipeWirePath(t, root, filepath.Join("t", entry.Name()))
			if len(socket) > 100 {
				t.Fatalf("MPV created an overlong private socket: %q", socket)
			}
		}
	}
	track.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		retired := true
		for _, object := range privatePipeWireGraph(t, root) {
			info, _ := object["info"].(map[string]any)
			props, _ := info["props"].(map[string]any)
			if (object["type"] == "PipeWire:Interface:Node" && props["node.name"] == streamName) ||
				(object["type"] == "PipeWire:Interface:Link" && props["navidrome.output-token"] == token) {
				retired = false
			}
		}
		if retired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not retire the private stream and its owned links")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Registered cleanup order is track, then Pulse, then Core, before private storage.
}

func privatePipeWireConfig(properties string, modules []string, extra string) string {
	var entries strings.Builder
	for _, module := range modules {
		fmt.Fprintf(&entries, "    { name = %s }\n", module)
	}
	return fmt.Sprintf(`context.properties = { %s support.dbus = false mem.allow-mlock = false }
context.spa-libs = {
    support.* = support/libspa-support
    audio.convert.* = audioconvert/libspa-audioconvert
}
context.modules = [
%s]
%s
context.exec = []
`, properties, entries.String(), extra)
}

func privatePipeWirePath(t *testing.T, root, relative string) string {
	t.Helper()
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("private artifact escapes its root: %q", path)
	}
	return path
}

func privatePipeWireEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

type privatePipeWireDaemon struct {
	command *exec.Cmd
	done    chan struct{}
	log     string
	err     error
	stop    sync.Once
}

func startPrivatePipeWireDaemon(t *testing.T, ctx context.Context, root, executable, config string) *privatePipeWireDaemon {
	t.Helper()
	logPath := privatePipeWirePath(t, root, filepath.Join("l", filepath.Base(executable)+".log"))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-c", config)
	command.WaitDelay = 2 * time.Second
	command.Dir = root
	command.Env = os.Environ()
	for index, entry := range command.Env {
		if strings.HasPrefix(entry, "PIPEWIRE_CONFIG_NAME=") {
			command.Env[index] = "PIPEWIRE_CONFIG_NAME=" + config
		}
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start private %s: %v", executable, err)
	}
	process := &privatePipeWireDaemon{command: command, done: make(chan struct{}), log: logPath}
	go func() {
		process.err = command.Wait()
		_ = logFile.Close()
		close(process.done)
	}()
	t.Cleanup(func() {
		process.stop.Do(func() {
			select {
			case <-process.done:
				return
			default:
			}
			_ = command.Process.Signal(os.Interrupt)
			select {
			case <-process.done:
				return
			case <-time.After(2 * time.Second):
			}
			_ = command.Process.Kill()
			select {
			case <-process.done:
			case <-time.After(2 * time.Second):
				t.Errorf("stored private process %s did not exit after kill", executable)
			}
		})
	})
	return process
}

func waitPrivatePipeWireSocket(t *testing.T, process *privatePipeWireDaemon, socket string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Lstat(socket)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("private socket must not be a symlink: %q", socket)
		}
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		select {
		case <-process.done:
			data, _ := os.ReadFile(process.log)
			t.Fatalf("private daemon exited before its socket: %v\n%s", process.err, data)
		case <-deadline.C:
			data, _ := os.ReadFile(process.log)
			t.Fatalf("private socket did not become ready: %q\n%s", socket, data)
		case <-ticker.C:
		}
	}
}

func privatePipeWireGraph(t *testing.T, root string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/pw-dump", "--no-colors")
	command.Dir = root
	command.Env = os.Environ()
	return readPipeWireTestGraph(t, command)
}

func assertPrivatePipeWireGraph(t *testing.T, graph []map[string]any, modules []string) {
	t.Helper()
	allowed := map[string]bool{}
	for _, name := range modules {
		allowed[name] = false
	}
	identified := false
	for _, object := range graph {
		info, _ := object["info"].(map[string]any)
		props, _ := info["props"].(map[string]any)
		switch object["type"] {
		case "PipeWire:Interface:Core":
			if (info["name"] != nil && info["name"] != "nd-private") || (props["core.name"] != nil && props["core.name"] != "nd-private") {
				t.Fatal("graph is not the private Core")
			}
			identified = info["name"] == "nd-private" || props["core.name"] == "nd-private"
		case "PipeWire:Interface:Device":
			t.Fatal("private graph unexpectedly contains a hardware Device")
		case "PipeWire:Interface:Module":
			name, _ := info["name"].(string)
			if _, ok := allowed[name]; !ok {
				t.Fatalf("private graph loaded an unexpected module: %q", name)
			}
			allowed[name] = true
		case "PipeWire:Interface:Node":
			if props["device.api"] != nil || props["api.alsa.path"] != nil || props["api.bluez5.address"] != nil {
				t.Fatal("private graph unexpectedly contains a physical audio backend")
			}
		}
	}
	if !identified {
		t.Fatal("independent graph lacks private Core identity")
	}
	for name, present := range allowed {
		if !present {
			t.Fatalf("private graph is missing an explicitly allowed module: %q", name)
		}
	}
}

func privatePipeWireID(value any) float64 {
	if text, ok := value.(string); ok {
		number, err := strconv.ParseUint(text, 10, 32)
		if err == nil {
			return float64(number)
		}
		return 0
	}
	number, _ := value.(float64)
	return number
}

func assertPrivatePipeWireChannelLinks(t *testing.T, graph []map[string]any, target string) (string, string) {
	t.Helper()
	var stream, sink float64
	var streamName string
	for _, object := range graph {
		if object["type"] != "PipeWire:Interface:Node" {
			continue
		}
		info, _ := object["info"].(map[string]any)
		props, _ := info["props"].(map[string]any)
		name, _ := props["node.name"].(string)
		if name == target {
			if sink != 0 {
				t.Fatal("private target identity is ambiguous")
			}
			sink, _ = object["id"].(float64)
		}
		if strings.HasPrefix(name, "navidrome-output-") && props["media.class"] == "Stream/Output/Audio" {
			if stream != 0 {
				t.Fatal("private MPV stream identity is ambiguous")
			}
			if fmt.Sprint(props["node.autoconnect"]) != "false" || fmt.Sprint(props["node.dont-reconnect"]) != "true" {
				t.Fatal("private MPV stream does not independently prohibit automatic connections")
			}
			pid, err := strconv.Atoi(fmt.Sprint(props["application.process.id"]))
			if err != nil || pid <= 0 || pid == os.Getpid() {
				t.Fatal("private MPV stream has no independently observed child identity")
			}
			stream, _ = object["id"].(float64)
			streamName = name
		}
	}
	if stream == 0 || sink == 0 {
		t.Fatal("private graph lacks the MPV stream or virtual target")
	}
	outputs, inputs := map[float64]string{}, map[float64]string{}
	for _, object := range graph {
		if object["type"] != "PipeWire:Interface:Port" {
			continue
		}
		info, _ := object["info"].(map[string]any)
		props, _ := info["props"].(map[string]any)
		id, _ := object["id"].(float64)
		channel, _ := props["audio.channel"].(string)
		if privatePipeWireID(props["node.id"]) == stream && info["direction"] == "output" {
			outputs[id] = channel
		}
		if privatePipeWireID(props["node.id"]) == sink && info["direction"] == "input" {
			inputs[id] = channel
		}
	}
	channels := map[string]bool{}
	var token string
	for _, object := range graph {
		if object["type"] != "PipeWire:Interface:Link" {
			continue
		}
		info, _ := object["info"].(map[string]any)
		if info["output-node-id"] != stream {
			continue
		}
		props, _ := info["props"].(map[string]any)
		value, _ := props["navidrome.output-token"].(string)
		outputPort, _ := info["output-port-id"].(float64)
		inputPort, _ := info["input-port-id"].(float64)
		channel := outputs[outputPort]
		if info["input-node-id"] != sink || (channel != "FL" && channel != "FR") || inputs[inputPort] != channel || channels[channel] ||
			value == "" || (token != "" && token != value) || (info["state"] != "paused" && info["state"] != "active") {
			t.Fatal("private output lacks exact owned channel links to the selected target")
		}
		token, channels[channel] = value, true
	}
	if len(outputs) != 2 || len(channels) != 2 {
		t.Fatal("private stereo output is not completely linked")
	}
	return streamName, token
}

func TestPipeWireProcessFixture(t *testing.T) {
	role := os.Getenv("ND_TEST_PIPEWIRE_ROLE")
	if role == "" {
		return
	}
	dir := os.Getenv("ND_TEST_PIPEWIRE_DIRECTORY")
	scenario := os.Getenv("ND_TEST_PIPEWIRE_SCENARIO")
	switch role {
	case "mpv":
		if err := os.WriteFile(filepath.Join(dir, "commands.jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mpv.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		properties := map[string]string{"node.name": "mpv", "node.autoconnect": "true", "node.dont-reconnect": "false"}
		for name := range properties {
			if value, ok := os.LookupEnv("PULSE_PROP_OVERRIDE_" + name); ok {
				properties[name] = value
			}
		}
		data, err := json.Marshal(properties)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mpv-properties.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		var socket string
		for _, arg := range os.Args {
			if strings.HasPrefix(arg, "--input-ipc-server=") {
				socket = strings.TrimPrefix(arg, "--input-ipc-server=")
			}
		}
		if socket == "" {
			t.Fatal("MPV fixture received no IPC socket")
		}
		listener := listenIPCAt(t, socket)
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		decoder, encoder := json.NewDecoder(conn), json.NewEncoder(conn)
		for {
			var request struct {
				Command   []any `json:"command"`
				RequestID int64 `json:"request_id"`
			}
			if err := decoder.Decode(&request); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatal(err)
			}
			recordPipeWireFixtureCommand(t, dir, request.Command)
			var data any
			responseError := "success"
			if len(request.Command) > 1 && request.Command[0] == "get_property" {
				switch request.Command[1] {
				case "current-ao":
					data = "pulse"
				case "audio-out-params":
					data = map[string]any{"samplerate": 48000, "channels": "stereo", "channel-count": 2, "format": "s16"}
				case "audio-device":
					data = "pulse/" + pipewireFixtureTarget
				case "time-pos":
					data = 0
				case "pause":
					data = true
				case "pid":
					data = os.Getpid()
				}
			}
			if (scenario == "bindable-target" || scenario == "late-channel" || scenario == "static-partial-channel") && len(request.Command) > 1 && request.Command[0] == "get_property" &&
				(request.Command[1] == "current-ao" || request.Command[1] == "audio-out-params") {
				requiredPeers := 2
				if scenario == "late-channel" || scenario == "static-partial-channel" {
					requiredPeers = 1
				}
				if len(readPipeWireFixtureLinks(t, dir)) < requiredPeers {
					data, responseError = nil, "property unavailable"
				}
			}
			if err := encoder.Encode(map[string]any{"request_id": request.RequestID, "error": responseError, "data": data}); err != nil {
				t.Fatal(err)
			}
			if len(request.Command) > 0 && request.Command[0] == "quit" {
				break
			}
		}
	case "pw-dump":
		pid := readPipeWireFixturePID(t, dir)
		childProperties := readPipeWireFixtureChildProperties(t, dir)
		streamProperties := map[string]any{
			"object.serial": 110, "media.class": "Stream/Output/Audio",
			"application.process.id": strconv.Itoa(pid), "target.object": pipewireFixtureTarget,
		}
		for name, value := range childProperties {
			streamProperties[name] = value
		}
		graph := []map[string]any{
			{"id": 10, "type": "PipeWire:Interface:Node", "info": map[string]any{"props": streamProperties}},
			{"id": 20, "type": "PipeWire:Interface:Node", "info": map[string]any{"props": map[string]any{
				"object.serial": 120, "node.name": "alsa_output.unrelated", "media.class": "Audio/Sink",
			}}},
		}
		outputChannels := []string{"FL", "FR"}
		if scenario == "static-partial-channel" || (scenario == "late-channel" && len(readPipeWireFixtureLinks(t, dir)) == 0) {
			outputChannels = outputChannels[:1]
		}
		for index, channel := range outputChannels {
			graph = append(graph, pipewireFixturePort(11+index, 10, "output", "output_"+channel, channel))
		}
		if scenario != "missing-target" {
			graph = append(graph, map[string]any{
				"id": 30, "type": "PipeWire:Interface:Node", "info": map[string]any{"props": map[string]any{
					"object.serial": 130, "node.name": pipewireFixtureTarget, "media.class": "Audio/Sink",
				}},
			})
			for index, channel := range []string{"FL", "FR"} {
				graph = append(graph, pipewireFixturePort(31+index, 30, "input", "playback_"+channel, channel))
			}
		}
		graph = append(graph, readPipeWireFixtureLinks(t, dir)...)
		if err := json.NewEncoder(os.Stdout).Encode(graph); err != nil {
			t.Fatal(err)
		}
	case "pw-cli":
		if scenario != "bindable-target" && scenario != "late-channel" && scenario != "static-partial-channel" {
			t.Fatal("fixture binding executor is unavailable")
		}
		runPipeWireFixtureLinkExecutor(t, dir)
	case "pw-link":
		t.Fatal("pw-link cannot supply the required client-owned link lifetime")
	default:
		t.Fatalf("unexpected fixture role %q", role)
	}
	os.Exit(0)
}

func pipewireFixturePort(id, node int, direction, name, channel string) map[string]any {
	return map[string]any{
		"id": id, "type": "PipeWire:Interface:Port", "info": map[string]any{
			"direction": direction, "props": map[string]any{
				"object.serial": id + 200, "node.id": node, "port.name": name, "audio.channel": channel,
			},
		},
	}
}

func runPipeWireFixtureLinkExecutor(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "pw-cli.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	streamName := readPipeWireFixtureChildProperties(t, dir)["node.name"]
	var links []map[string]any
	var token string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		recordPipeWireFixtureCommand(t, dir, []any{"pw-cli", line})
		if line == "quit" {
			return
		}
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] != "create-link" {
			t.Fatalf("unexpected link executor command %q; global destruction is forbidden", line)
		}
		if fields[1] != streamName || fields[3] != pipewireFixtureTarget {
			t.Fatalf("link creation did not use exact node names: %q", line)
		}
		channel := strings.TrimPrefix(fields[2], "output_")
		index := 0
		switch channel {
		case "FL":
		case "FR":
			index = 1
		default:
			t.Fatalf("unexpected output port name in %q", line)
		}
		if os.Getenv("ND_TEST_PIPEWIRE_SCENARIO") == "static-partial-channel" && channel == "FR" {
			t.Fatal("output_FR does not exist in the static partial layout")
		}
		if fields[4] != "playback_"+channel || len(links) >= 2 {
			t.Fatalf("unexpected input port or duplicate link command %q", line)
		}
		var properties map[string]any
		if err := json.Unmarshal([]byte(strings.Join(fields[5:], " ")), &properties); err != nil {
			t.Fatalf("invalid link properties: %v", err)
		}
		value, ok := properties["navidrome.output-token"].(string)
		if !ok || value == "" || (token != "" && token != value) || properties["object.linger"] != false || properties["link.passive"] != false {
			t.Fatalf("link properties do not establish one nonlingering owner: %v", properties)
		}
		token = value
		properties["object.serial"] = 140 + index
		links = append(links, map[string]any{
			"id": 40 + index, "type": "PipeWire:Interface:Link", "info": map[string]any{
				"output-node-id": 10, "output-port-id": 11 + index,
				"input-node-id": 30, "input-port-id": 31 + index,
				"state": "paused", "error": nil, "props": properties,
			},
		})
		data, err := json.Marshal(struct {
			PID   int              `json:"pid"`
			Links []map[string]any `json:"links"`
		}{os.Getpid(), links})
		if err != nil {
			t.Fatal(err)
		}
		pending := filepath.Join(dir, "links.next.json")
		if err := os.WriteFile(pending, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(pending, filepath.Join(dir, "links.json")); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "created link")
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func readPipeWireFixtureChildProperties(t *testing.T, dir string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "mpv-properties.json"))
	if err != nil {
		t.Fatal(err)
	}
	var properties map[string]string
	if err := json.Unmarshal(data, &properties); err != nil {
		t.Fatal(err)
	}
	return properties
}

func readPipeWireFixtureLinks(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "links.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var owner struct {
		PID   int              `json:"pid"`
		Links []map[string]any `json:"links"`
	}
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(owner.PID))); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return owner.Links
}

func readPipeWireFixtureGraph(t *testing.T, dir string) []map[string]any {
	t.Helper()
	return readPipeWireTestGraph(t, exec.Command(filepath.Join(dir, "pw-dump")))
}

func readPipeWireTestGraph(t *testing.T, command *exec.Cmd) []map[string]any {
	t.Helper()
	data, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var graph []map[string]any
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatal(err)
	}
	return graph
}

func assertPipeWireFixtureProcessRetired(t *testing.T, dir, name string) {
	t.Helper()
	pid := readPipeWireFixturePIDFile(t, dir, name)
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("output left its %s child %d alive", name, pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func recordPipeWireFixtureCommand(t *testing.T, dir string, command []any) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, "commands.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(command); err != nil {
		t.Fatal(err)
	}
}

func readPipeWireFixturePID(t *testing.T, dir string) int {
	t.Helper()
	return readPipeWireFixturePIDFile(t, dir, "mpv.pid")
}

func readPipeWireFixturePIDFile(t *testing.T, dir, name string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func assertPipeWireFixtureSafeCommands(t *testing.T, dir string) {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, "commands.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var command []any
		if err := decoder.Decode(&command); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatal(err)
		}
		if len(command) == 3 && command[0] == "set_property" && command[1] == "pause" && command[2] == false {
			t.Error("output preparation sent an unpause command")
		}
		if len(command) == 2 && command[0] == "pw-cli" {
			fields := strings.Fields(command[1].(string))
			if len(fields) > 0 && (strings.HasPrefix(fields[0], "destroy") || fields[0] == "unlink") {
				t.Error("output cleanup used global destruction instead of retiring its link owner")
			}
		}
	}
}
