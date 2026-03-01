package bluetooth

import (
	"testing"
)

func TestAudioSink_IsBluetooth(t *testing.T) {
	tests := []struct {
		name     string
		sink     AudioSink
		expected bool
	}{
		{"bluez_output prefix", AudioSink{Name: "bluez_output.24_C4_06_FA_00_37.a2dp-sink"}, true},
		{"bluez_sink prefix", AudioSink{Name: "bluez_sink.24_C4_06_FA_00_37.a2dp-sink"}, true},
		{"non-bluetooth sink", AudioSink{Name: "alsa_output.pci-0000_00_1f.3.analog-stereo"}, false},
		{"auto sink", AudioSink{Name: "auto"}, false},
		{"empty name", AudioSink{Name: ""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sink.IsBluetooth(); got != tt.expected {
				t.Errorf("IsBluetooth() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAudioSink_MPVDeviceName(t *testing.T) {
	tests := []struct {
		name     string
		sink     AudioSink
		expected string
	}{
		{"bluetooth sink", AudioSink{Name: "bluez_output.24_C4_06_FA_00_37.a2dp-sink"}, "pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink"},
		{"alsa sink", AudioSink{Name: "alsa_output.pci-0000_00_1f.3.analog-stereo"}, "pulse/alsa_output.pci-0000_00_1f.3.analog-stereo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sink.MPVDeviceName(); got != tt.expected {
				t.Errorf("MPVDeviceName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestAudioSink_FriendlyName(t *testing.T) {
	tests := []struct {
		name     string
		sink     AudioSink
		expected string
	}{
		{"bluez_output with a2dp", AudioSink{Name: "bluez_output.24_C4_06_FA_00_37.a2dp-sink"}, "Bluetooth 24:C4:06:FA:00:37"},
		{"bluez_sink with a2dp", AudioSink{Name: "bluez_sink.AA_BB_CC_DD_EE_FF.a2dp-sink"}, "Bluetooth AA:BB:CC:DD:EE:FF"},
		{"non-bluetooth returns raw name", AudioSink{Name: "alsa_output.pci-0000_00_1f.3.analog-stereo"}, "alsa_output.pci-0000_00_1f.3.analog-stereo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sink.FriendlyName(); got != tt.expected {
				t.Errorf("FriendlyName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestAudioSink_ToDeviceDefinition(t *testing.T) {
	sink := AudioSink{Name: "bluez_output.24_C4_06_FA_00_37.a2dp-sink"}
	def := sink.ToDeviceDefinition()

	if len(def) != 2 {
		t.Fatalf("ToDeviceDefinition() returned %d fields, want 2", len(def))
	}
	if def[0] != "Bluetooth 24:C4:06:FA:00:37" {
		t.Errorf("def[0] = %q, want %q", def[0], "Bluetooth 24:C4:06:FA:00:37")
	}
	if def[1] != "pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink" {
		t.Errorf("def[1] = %q, want %q", def[1], "pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink")
	}
}

func TestParsePactlOutput(t *testing.T) {
	input := `0	alsa_output.pci-0000_00_1f.3.analog-stereo	module-alsa-card.c	s16le	RUNNING
1	bluez_output.24_C4_06_FA_00_37.a2dp-sink	module-bluez5-device.c	s16le	IDLE
2	bluez_output.AA_BB_CC_DD_EE_FF.a2dp-sink	module-bluez5-device.c	s16le	SUSPENDED
`

	sinks := parsePactlOutput(input)
	if len(sinks) != 3 {
		t.Fatalf("parsePactlOutput() returned %d sinks, want 3", len(sinks))
	}

	// Verify first sink (non-bluetooth)
	if sinks[0].Name != "alsa_output.pci-0000_00_1f.3.analog-stereo" {
		t.Errorf("sinks[0].Name = %q", sinks[0].Name)
	}
	if sinks[0].IsBluetooth() {
		t.Error("sinks[0] should not be bluetooth")
	}

	// Verify second sink (bluetooth)
	if sinks[1].Name != "bluez_output.24_C4_06_FA_00_37.a2dp-sink" {
		t.Errorf("sinks[1].Name = %q", sinks[1].Name)
	}
	if !sinks[1].IsBluetooth() {
		t.Error("sinks[1] should be bluetooth")
	}
	if sinks[1].State != "IDLE" {
		t.Errorf("sinks[1].State = %q, want IDLE", sinks[1].State)
	}

	// Verify third sink (bluetooth)
	if !sinks[2].IsBluetooth() {
		t.Error("sinks[2] should be bluetooth")
	}
}

func TestParsePactlOutput_Empty(t *testing.T) {
	sinks := parsePactlOutput("")
	if len(sinks) != 0 {
		t.Errorf("parsePactlOutput(\"\") returned %d sinks, want 0", len(sinks))
	}
}

func TestDiscoverAllSinks_ParsesCorrectly(t *testing.T) {
	// This test validates that DiscoverAllSinks would return all sinks, not just BT ones.
	// We can only test the parsing layer since pactl may not be available.
	input := `0	alsa_output.pci-0000_00_1f.3.analog-stereo	module-alsa-card.c	s16le	RUNNING
1	bluez_output.24_C4_06_FA_00_37.a2dp-sink	module-bluez5-device.c	s16le	IDLE
`
	sinks := parsePactlOutput(input)
	if len(sinks) != 2 {
		t.Fatalf("parsePactlOutput() returned %d sinks, want 2", len(sinks))
	}
	if sinks[0].IsBluetooth() {
		t.Error("sinks[0] should not be bluetooth")
	}
	if !sinks[1].IsBluetooth() {
		t.Error("sinks[1] should be bluetooth")
	}
}

func TestParsePactlOutput_MalformedLines(t *testing.T) {
	input := `0
just_one_field
1	valid_sink	module	s16le	RUNNING
`
	sinks := parsePactlOutput(input)
	if len(sinks) != 1 {
		t.Fatalf("parsePactlOutput() returned %d sinks, want 1", len(sinks))
	}
	if sinks[0].Name != "valid_sink" {
		t.Errorf("sinks[0].Name = %q, want valid_sink", sinks[0].Name)
	}
}
