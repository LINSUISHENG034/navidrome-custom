package bluetooth

import (
	"bufio"
	"context"
	"os/exec"
	"strings"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
)

// AudioSink represents a PulseAudio/PipeWire audio sink parsed from `pactl list sinks short`.
type AudioSink struct {
	Index  string
	Name   string
	Module string
	Sample string
	State  string
}

// IsBluetooth returns true if this sink is a Bluetooth audio device.
func (s AudioSink) IsBluetooth() bool {
	return strings.HasPrefix(s.Name, "bluez_output.") || strings.HasPrefix(s.Name, "bluez_sink.")
}

// MPVDeviceName returns the device name in the format mpv expects for PulseAudio output.
func (s AudioSink) MPVDeviceName() string {
	return "pulse/" + s.Name
}

// FriendlyName extracts a human-readable name from the Bluetooth sink name.
// For "bluez_output.24_C4_06_FA_00_37.a2dp-sink" it returns "Bluetooth 24:C4:06:FA:00:37".
func (s AudioSink) FriendlyName() string {
	if !s.IsBluetooth() {
		return s.Name
	}
	// Strip prefix: "bluez_output." or "bluez_sink."
	name := s.Name
	for _, prefix := range []string{"bluez_output.", "bluez_sink."} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
			break
		}
	}
	// Extract MAC portion (before the first dot after prefix removal)
	if idx := strings.Index(name, "."); idx > 0 {
		name = name[:idx]
	}
	// Convert underscores to colons for MAC format
	mac := strings.ReplaceAll(name, "_", ":")
	return "Bluetooth " + mac
}

// ToDeviceDefinition converts this sink to a conf.AudioDeviceDefinition
// compatible with Navidrome's jukebox device configuration.
func (s AudioSink) ToDeviceDefinition() conf.AudioDeviceDefinition {
	return conf.AudioDeviceDefinition{s.FriendlyName(), s.MPVDeviceName()}
}

// parsePactlOutput parses the output of `pactl list sinks short` into AudioSink structs.
// Each line has tab-separated fields: index, name, module, sample_spec, state.
func parsePactlOutput(output string) []AudioSink {
	var sinks []AudioSink
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sink := AudioSink{
			Index: fields[0],
			Name:  fields[1],
		}
		if len(fields) > 2 {
			sink.Module = fields[2]
		}
		if len(fields) > 3 {
			sink.Sample = fields[3]
		}
		if len(fields) > 4 {
			sink.State = fields[4]
		}
		sinks = append(sinks, sink)
	}
	return sinks
}

// DiscoverAllSinks shells out to `pactl list sinks short` and returns all parsed sinks.
// Returns an empty slice (not error) if pactl is unavailable.
func DiscoverAllSinks(ctx context.Context) []AudioSink {
	cmd := exec.CommandContext(ctx, "pactl", "list", "sinks", "short")
	output, err := cmd.Output()
	if err != nil {
		log.Debug(ctx, "pactl not available or failed", "err", err)
		return nil
	}
	return parsePactlOutput(string(output))
}

// DiscoverBluetoothSinks shells out to `pactl list sinks short`, parses the output,
// and returns only Bluetooth sinks. Returns an empty slice (not error) if pactl is
// unavailable — graceful degradation.
func DiscoverBluetoothSinks(ctx context.Context) []AudioSink {
	cmd := exec.CommandContext(ctx, "pactl", "list", "sinks", "short")
	output, err := cmd.Output()
	if err != nil {
		log.Debug(ctx, "pactl not available or failed, Bluetooth discovery disabled", "err", err)
		return nil
	}

	allSinks := parsePactlOutput(string(output))
	var btSinks []AudioSink
	for _, sink := range allSinks {
		if sink.IsBluetooth() {
			btSinks = append(btSinks, sink)
		}
	}

	log.Debug(ctx, "Bluetooth sink discovery complete", "total", len(allSinks), "bluetooth", len(btSinks))
	return btSinks
}
