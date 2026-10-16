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
	"time"

	"github.com/navidrome/navidrome/conf"
)

const pipeWireOutputToken = "navidrome.output-token"

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

type pipeWireOutput struct {
	stream    pipeWireObject
	sink      pipeWireObject
	ports     []pipeWirePortPair
	token     string
	owner     *Executor
	input     *os.File
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
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
		}
	}()
	ownerCtx, cancel := context.WithCancel(lifetime)
	output.cancel = cancel
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	output.input = writer
	owner, err := startWithInput(ownerCtx, []string{"pw-cli"}, reader)
	_ = reader.Close()
	if err != nil {
		return nil, fmt.Errorf("start PipeWire link owner: %w", err)
	}
	output.owner = &owner
	go func() { _, _ = io.Copy(io.Discard, &owner) }()
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
		return errors.New("PipeWire link owner has exited")
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

func (output *pipeWireOutput) close() error {
	output.closeOnce.Do(func() {
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
