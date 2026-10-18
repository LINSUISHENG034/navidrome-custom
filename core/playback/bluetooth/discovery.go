package bluetooth

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"time"

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

// NormalizedMAC extracts the Bluetooth MAC address embedded in a BlueZ sink name.
func (s AudioSink) NormalizedMAC() string {
	if !s.IsBluetooth() {
		return ""
	}
	name := s.Name
	for _, prefix := range []string{"bluez_output.", "bluez_sink."} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
			break
		}
	}
	if idx := strings.Index(name, "."); idx > 0 {
		name = name[:idx]
	}
	return NormalizeMAC(name)
}

// MatchesMAC reports whether this sink belongs to the given Bluetooth MAC.
func (s AudioSink) MatchesMAC(mac string) bool {
	normalized := NormalizeMAC(mac)
	return normalized != "" && s.NormalizedMAC() == normalized
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

// NormalizeMAC returns a colon-separated uppercase MAC address.
func NormalizeMAC(mac string) string {
	return normalizeMAC(mac)
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

// DiscoverSinks shells out to `pactl list sinks short` and returns all parsed sinks.
// Returns an empty slice (not error) if pactl is unavailable.
func DiscoverSinks(ctx context.Context) []AudioSink {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "pactl", "list", "sinks", "short")
	output, err := cmd.Output()
	if err != nil {
		log.Debug(ctx, "pactl not available or failed", "err", err)
		return nil
	}
	return parsePactlOutput(string(output))
}

// DiscoverAllSinks is a compatibility wrapper around DiscoverSinks.
func DiscoverAllSinks(ctx context.Context) []AudioSink {
	return DiscoverSinks(ctx)
}

// DiscoverBluetoothSinks filters Bluetooth sinks from a provided snapshot, or
// discovers a fresh snapshot when none is provided.
func DiscoverBluetoothSinks(ctx context.Context, sinks ...[]AudioSink) []AudioSink {
	allSinks := []AudioSink(nil)
	if len(sinks) > 0 {
		allSinks = sinks[0]
	} else {
		allSinks = DiscoverSinks(ctx)
	}
	var btSinks []AudioSink
	for _, sink := range allSinks {
		if sink.IsBluetooth() {
			btSinks = append(btSinks, sink)
		}
	}

	log.Debug(ctx, "Bluetooth sink discovery complete", "total", len(allSinks), "bluetooth", len(btSinks))
	return btSinks
}

// FindBluetoothSinkByMAC finds a Bluetooth audio sink matching the provided MAC.
func FindBluetoothSinkByMAC(sinks []AudioSink, mac string) (AudioSink, bool) {
	for _, sink := range sinks {
		if sink.MatchesMAC(mac) {
			return sink, true
		}
	}
	return AudioSink{}, false
}

// WaitForSinkReady polls PulseAudio/PipeWire until a BlueZ sink for mac appears,
// or until timeout expires.
func WaitForSinkReady(ctx context.Context, mac string, timeout, interval time.Duration) (AudioSink, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		if sink, ok := FindBluetoothSinkByMAC(DiscoverSinks(ctx), mac); ok {
			return sink, true
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return AudioSink{}, false
		case <-timer.C:
		}
	}
}
