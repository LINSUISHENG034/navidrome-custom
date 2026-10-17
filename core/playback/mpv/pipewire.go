package mpv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/navidrome/navidrome/conf"
)

const (
	pipeWireOutputToken        = "navidrome.output-token"
	maxPipeWireDiagnosticBytes = 4 * 1024
)

type pipeWireDiagnostic struct {
	data []byte
}

func (diagnostic *pipeWireDiagnostic) Write(data []byte) (int, error) {
	remaining := maxPipeWireDiagnosticBytes - len(diagnostic.data)
	if remaining > 0 {
		diagnostic.data = append(diagnostic.data, data[:min(len(data), remaining)]...)
	}
	return len(data), nil
}

var errPipeWirePending = errors.New("PipeWire output is still preparing")

type pipeWireObject struct {
	ID   uint32 `json:"id"`
	Type string `json:"type"`
	Info struct {
		Props      map[string]any `json:"props"`
		Direction  string         `json:"direction"`
		State      string         `json:"state"`
		Error      string         `json:"error"`
		OutputNode uint32         `json:"output-node-id"`
		OutputPort uint32         `json:"output-port-id"`
		InputNode  uint32         `json:"input-node-id"`
		InputPort  uint32         `json:"input-port-id"`
	} `json:"info"`
}

type pipeWirePortPair struct {
	output pipeWireObject
	input  pipeWireObject
}

type pipeWireFault struct {
	generation string
	err        error
}

type pipeWireOutput struct {
	stream    pipeWireObject
	sink      pipeWireObject
	ports     []pipeWirePortPair
	token     string
	owner     *Executor
	stdout    pipeWireDiagnostic
	stderr    pipeWireDiagnostic
	drained   chan struct{}
	input     *os.File
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error

	checkTimeout   time.Duration
	checkGate      chan struct{}
	observerCancel context.CancelFunc
	observerDone   chan struct{}
	expected       atomic.Bool
	faultMu        sync.Mutex
	fault          error
}

func pipeWireBluetoothTarget(deviceName string) string {
	target, ok := strings.CutPrefix(deviceName, "pulse/")
	if !ok || (!strings.HasPrefix(target, "bluez_output.") && !strings.HasPrefix(target, "bluez_sink.")) {
		return ""
	}
	return target
}

func pipeWireEnvironment(name string) []string {
	return []string{
		"PULSE_PROP_OVERRIDE_node.autoconnect=false",
		"PULSE_PROP_OVERRIDE_node.dont-reconnect=true",
		"PULSE_PROP_OVERRIDE_node.name=" + name,
	}
}

func pipeWireSnapshot(ctx context.Context) ([]pipeWireObject, error) {
	command := exec.CommandContext(ctx, "pw-dump", "--no-colors")
	command.WaitDelay = trackCloseTimeout
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("observe PipeWire output graph: %w", err)
	}
	var graph []pipeWireObject
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&graph); err != nil {
		return nil, fmt.Errorf("decode PipeWire output graph: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("PipeWire output graph contains trailing data")
	}
	return graph, nil
}

func pipeWireProperty(object pipeWireObject, key string) string {
	switch value := object.Info.Props[key].(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func pipeWireMatches(object pipeWireObject, value string, keys ...string) bool {
	for _, key := range keys {
		if pipeWireProperty(object, key) == value {
			return true
		}
	}
	return false
}

func pipeWireCommandName(name string) bool {
	for i, c := range name {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || !(c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.')) {
			return false
		}
	}
	return name != ""
}

func pipeWireNamedNode(graph []pipeWireObject, name, class string) (pipeWireObject, error) {
	if !pipeWireCommandName(name) {
		return pipeWireObject{}, fmt.Errorf("unsupported PipeWire node name %q", name)
	}
	var matches []pipeWireObject
	for _, object := range graph {
		if object.Type == "PipeWire:Interface:Node" && pipeWireMatches(object, name, "node.name", "node.nick", "node.description", "object.path") {
			matches = append(matches, object)
		}
	}
	if len(matches) != 1 || pipeWireProperty(matches[0], "node.name") != name || pipeWireProperty(matches[0], "media.class") != class {
		return pipeWireObject{}, fmt.Errorf("requested PipeWire node %q is unavailable or ambiguous", name)
	}
	return matches[0], nil
}

func planPipeWireOutput(graph []pipeWireObject, pid int, name, target string) (*pipeWireOutput, error) {
	sink, err := pipeWireNamedNode(graph, target, "Audio/Sink")
	if err != nil {
		return nil, err
	}
	stream, err := pipeWireNamedNode(graph, name, "Stream/Output/Audio")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPipeWirePending, err)
	}
	if pipeWireProperty(stream, "application.process.id") != strconv.Itoa(pid) ||
		pipeWireProperty(stream, "node.autoconnect") != "false" || pipeWireProperty(stream, "node.dont-reconnect") != "true" {
		return nil, errors.New("MPV output lacks an independently verified non-autoconnecting stream")
	}
	for _, node := range []pipeWireObject{stream, sink} {
		if node.ID == 0 || pipeWireProperty(node, "object.serial") == "" {
			return nil, errors.New("PipeWire output has no generation identity")
		}
	}
	output := &pipeWireOutput{stream: stream, sink: sink, token: name + "." + pipeWireProperty(stream, "object.serial")}
	channels := map[string]bool{}
	for _, port := range graph {
		if !pipeWirePortOf(port, stream.ID, "output") {
			continue
		}
		channel := pipeWireProperty(port, "audio.channel")
		if channel == "" {
			return nil, fmt.Errorf("%w: output channel is not known", errPipeWirePending)
		}
		if channels[channel] {
			return nil, fmt.Errorf("ambiguous MPV output channel %q", channel)
		}
		channels[channel] = true
		if err := pipeWireUniquePort(graph, port); err != nil {
			return nil, err
		}
		var inputs []pipeWireObject
		for _, input := range graph {
			if pipeWirePortOf(input, sink.ID, "input") && pipeWireProperty(input, "audio.channel") == channel {
				inputs = append(inputs, input)
			}
		}
		if len(inputs) != 1 {
			return nil, fmt.Errorf("%w: target channel %q is missing or ambiguous", errPipeWirePending, channel)
		}
		if err := pipeWireUniquePort(graph, inputs[0]); err != nil {
			return nil, err
		}
		output.ports = append(output.ports, pipeWirePortPair{output: port, input: inputs[0]})
	}
	if len(output.ports) == 0 {
		return nil, fmt.Errorf("%w: MPV output ports are not available", errPipeWirePending)
	}
	return output, nil
}

func pipeWirePortOf(port pipeWireObject, node uint32, direction string) bool {
	return port.Type == "PipeWire:Interface:Port" && port.Info.Direction == direction &&
		pipeWireProperty(port, "node.id") == strconv.FormatUint(uint64(node), 10)
}

func pipeWireUniquePort(graph []pipeWireObject, port pipeWireObject) error {
	name := pipeWireProperty(port, "port.name")
	if !pipeWireCommandName(name) || pipeWireProperty(port, "object.serial") == "" {
		return fmt.Errorf("unsupported or unidentified PipeWire port %q", name)
	}
	matches := 0
	for _, other := range graph {
		if other.Type == "PipeWire:Interface:Port" && other.Info.Direction == port.Info.Direction &&
			pipeWireProperty(other, "node.id") == pipeWireProperty(port, "node.id") &&
			pipeWireMatches(other, name, "port.name", "port.alias", "object.path") {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("PipeWire port name %q is ambiguous", name)
	}
	return nil
}

func (output *pipeWireOutput) ready(graph []pipeWireObject) (bool, error) {
	identities := []pipeWireObject{output.stream, output.sink}
	for _, pair := range output.ports {
		identities = append(identities, pair.output, pair.input)
	}
	for _, identity := range identities {
		found := false
		for _, current := range graph {
			if current.ID != identity.ID || current.Type != identity.Type ||
				pipeWireProperty(current, "object.serial") != pipeWireProperty(identity, "object.serial") {
				continue
			}
			keys := []string{"node.name", "media.class"}
			if identity.ID == output.stream.ID {
				keys = append(keys, "application.process.id", "node.autoconnect", "node.dont-reconnect")
			} else if identity.Type == "PipeWire:Interface:Port" {
				keys = []string{"node.id", "port.name", "audio.channel"}
				if current.Info.Direction != identity.Info.Direction {
					return false, errors.New("PipeWire output port direction changed")
				}
			}
			for _, key := range keys {
				if pipeWireProperty(current, key) != pipeWireProperty(identity, key) {
					return false, fmt.Errorf("PipeWire output identity property %q changed", key)
				}
			}
			found = true
			break
		}
		if !found {
			return false, errors.New("PipeWire output generation was replaced or removed")
		}
	}
	portCount := 0
	for _, port := range graph {
		if !pipeWirePortOf(port, output.stream.ID, "output") {
			continue
		}
		portCount++
		admitted := false
		for _, pair := range output.ports {
			if port.ID == pair.output.ID {
				admitted = true
				break
			}
		}
		if !admitted {
			return false, errors.New("MPV output channel layout changed during preparation")
		}
	}
	if portCount != len(output.ports) {
		return false, errors.New("MPV output channel layout is incomplete")
	}
	linked := map[uint32]bool{}
	for _, link := range graph {
		if link.Type != "PipeWire:Interface:Link" || link.Info.OutputNode != output.stream.ID {
			continue
		}
		if link.Info.InputNode != output.sink.ID || pipeWireProperty(link, pipeWireOutputToken) != output.token {
			return false, errors.New("MPV output has an unexpected destination or unowned link")
		}
		matched := false
		for _, pair := range output.ports {
			if link.Info.OutputPort == pair.output.ID && link.Info.InputPort == pair.input.ID {
				matched = true
				break
			}
		}
		if !matched || linked[link.Info.OutputPort] {
			return false, errors.New("MPV output has unexpected or duplicate channel links")
		}
		if link.Info.Error != "" || link.Info.State == "error" || link.Info.State == "unlinked" {
			return false, fmt.Errorf("PipeWire output link failed: %s", link.Info.Error)
		}
		if link.Info.State != "paused" && link.Info.State != "active" {
			continue
		}
		linked[link.Info.OutputPort] = true
	}
	return len(linked) == len(output.ports), nil
}

func preparePipeWireOutput(ctx, lifetime context.Context, pid int, name, target string) (result *pipeWireOutput, err error) {
	interval := conf.Server.Jukebox.OutputPollInterval
	if interval <= 0 {
		return nil, errors.New("Jukebox.OutputPollInterval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var output *pipeWireOutput
	for {
		graph, snapshotErr := pipeWireSnapshot(ctx)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		output, err = planPipeWireOutput(graph, pid, name, target)
		if err == nil {
			break
		}
		if !errors.Is(err, errPipeWirePending) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, output.close())
			if output.owner != nil {
				select {
				case <-output.owner.done:
					err = errors.Join(err, output.exitError("while retiring rejected output"))
				default:
				}
			}
		}
	}()
	ownerCtx, cancel := context.WithCancel(lifetime)
	output.cancel = cancel
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	output.input = writer
	owner, err := startWithInput(ownerCtx, []string{"pw-cli"}, reader, &output.stderr)
	_ = reader.Close()
	if err != nil {
		return nil, fmt.Errorf("start PipeWire link owner: %w", err)
	}
	output.owner = &owner
	output.drained = make(chan struct{})
	go func() {
		defer close(output.drained)
		_, _ = io.Copy(&output.stdout, &owner)
	}()
	properties, err := json.Marshal(map[string]any{
		"object.linger": false, "link.passive": false, pipeWireOutputToken: output.token,
	})
	if err != nil {
		return nil, err
	}
	for _, pair := range output.ports {
		// Numeric port IDs are global-first in link-factory, even with a node ID.
		command := fmt.Sprintf("create-link %s %s %s %s %s\n", name, pipeWireProperty(pair.output, "port.name"), target, pipeWireProperty(pair.input, "port.name"), properties)
		if err := output.write(ctx, command); err != nil {
			return nil, err
		}
	}
	for {
		graph, snapshotErr := pipeWireSnapshot(ctx)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		ready, readyErr := output.ready(graph)
		if readyErr != nil {
			return nil, readyErr
		}
		if ready {
			return output, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-owner.done:
			return nil, errors.New("PipeWire link owner exited before output admission")
		case <-ticker.C:
		}
	}
}

func (output *pipeWireOutput) exitError(when string) error {
	// Wait for the stdout consumer as well as Cmd.Wait before reading diagnostics.
	<-output.drained
	return fmt.Errorf("PipeWire link owner exited %s (%s); stdout=%q; stderr=%q", when,
		output.owner.cmd.ProcessState, output.stdout.data, output.stderr.data)
}

func (output *pipeWireOutput) verifyFormat(format map[string]any) error {
	channels, ok := format["channel-count"].(float64)
	if !ok || channels != float64(len(output.ports)) {
		return errors.New("PipeWire channel bindings do not match the negotiated MPV output format")
	}
	return nil
}

func (output *pipeWireOutput) verify(ctx context.Context) error {
	select {
	case <-output.owner.done:
		return output.exitError("after output admission")
	default:
	}
	graph, err := pipeWireSnapshot(ctx)
	if err != nil {
		return err
	}
	ready, err := output.ready(graph)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("PipeWire output is not bound to the admitted target")
	}
	return nil
}

func (output *pipeWireOutput) write(ctx context.Context, command string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = output.input.Close() })
	defer stop()
	_, err := io.WriteString(output.input, command)
	return err
}

func (output *pipeWireOutput) arm(preparation, lifetime context.Context, faults chan<- pipeWireFault) error {
	interval := conf.Server.Jukebox.OutputCheckInterval
	output.checkTimeout = conf.Server.Jukebox.OutputCheckTimeout
	if interval <= 0 || output.checkTimeout <= 0 {
		return errors.New("Jukebox output check interval and timeout must be positive")
	}
	output.checkGate = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(lifetime)
	output.observerCancel = cancel
	output.observerDone = make(chan struct{})
	if err := output.checkSink(preparation); err != nil {
		cancel()
		close(output.observerDone)
		return err
	}
	go func() {
		defer close(output.observerDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := output.checkSink(ctx); err != nil {
					if ctx.Err() != nil || output.expected.Load() {
						return
					}
					select {
					case faults <- pipeWireFault{generation: output.token, err: err}:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	return nil
}

func (output *pipeWireOutput) latchFault(err error) {
	output.faultMu.Lock()
	defer output.faultMu.Unlock()
	if output.fault == nil && !output.expected.Load() {
		output.fault = err
	}
}

func (output *pipeWireOutput) latchedFault() error {
	output.faultMu.Lock()
	defer output.faultMu.Unlock()
	return output.fault
}

func (output *pipeWireOutput) ownerError() error {
	select {
	case <-output.owner.done:
		return output.exitError("while owning the selected output")
	default:
		return nil
	}
}

func (output *pipeWireOutput) checkSink(parent context.Context) (err error) {
	ctx, cancel := context.WithTimeout(parent, output.checkTimeout)
	defer cancel()
	select {
	case output.checkGate <- struct{}{}:
	case <-ctx.Done():
		if parent.Err() == nil {
			output.latchFault(ctx.Err())
		}
		return ctx.Err()
	}
	defer func() { <-output.checkGate }()
	// Latch before releasing the check gate so EOF cannot overtake a failed observation.
	defer func() {
		if err != nil && parent.Err() == nil {
			output.latchFault(err)
		}
	}()
	if output.expected.Load() {
		return context.Canceled
	}
	if err := output.latchedFault(); err != nil {
		return err
	}
	if err := output.ownerError(); err != nil {
		return err
	}
	graph, err := pipeWireSnapshot(ctx)
	if err != nil {
		return err
	}
	sink, err := pipeWireNamedNode(graph, pipeWireProperty(output.sink, "node.name"), "Audio/Sink")
	if err != nil {
		return err
	}
	if sink.ID != output.sink.ID || pipeWireProperty(sink, "object.serial") != pipeWireProperty(output.sink, "object.serial") {
		return errors.New("selected PipeWire output generation was replaced")
	}
	if sink.Info.State == "error" || sink.Info.Error != "" {
		return fmt.Errorf("selected PipeWire output failed: %s", sink.Info.Error)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := output.ownerError(); err != nil {
		return err
	}
	return output.latchedFault()
}

func (output *pipeWireOutput) acceptNaturalEnd(ctx context.Context) error {
	if err := output.checkSink(ctx); err != nil {
		return err
	}
	output.faultMu.Lock()
	defer output.faultMu.Unlock()
	if output.expected.Load() {
		return context.Canceled
	}
	if output.fault != nil {
		return output.fault
	}
	if err := output.ownerError(); err != nil {
		output.fault = err
		return err
	}
	output.expected.Store(true)
	return nil
}

func (output *pipeWireOutput) close() error {
	output.closeOnce.Do(func() {
		output.expected.Store(true)
		if output.observerCancel != nil {
			output.observerCancel()
		}
		if output.observerDone != nil {
			<-output.observerDone
		}
		defer func() {
			if output.cancel != nil {
				output.cancel()
			}
		}()
		if output.input != nil {
			ctx, cancel := context.WithTimeout(context.Background(), trackCloseTimeout)
			_ = output.write(ctx, "quit\n")
			cancel()
			_ = output.input.Close()
		}
		if output.owner != nil {
			ctx, cancel := context.WithTimeout(context.Background(), trackCloseTimeout)
			err := output.owner.Wait(ctx)
			cancel()
			if err != nil {
				ctx, cancel = context.WithTimeout(context.Background(), trackCloseTimeout)
				err = output.owner.Stop(ctx)
				cancel()
			}
			output.closeErr = err
			_ = output.owner.Close()
		}
	})
	return output.closeErr
}
