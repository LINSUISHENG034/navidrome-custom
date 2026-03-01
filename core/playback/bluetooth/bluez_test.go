package bluetooth

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestFilterA2DPSinkDevices(t *testing.T) {
	devices := []BlueZDevice{
		{MAC: "24:C4:06:FA:00:37", Name: "Speaker A", UUIDs: []string{"0000110b-0000-1000-8000-00805f9b34fb"}},
		{MAC: "AA:BB:CC:DD:EE:FF", Name: "Keyboard", UUIDs: []string{"00001124-0000-1000-8000-00805f9b34fb"}},
	}

	filtered := filterA2DPSinks(devices)
	if len(filtered) != 1 || filtered[0].MAC != "24:C4:06:FA:00:37" {
		t.Fatalf("expected one A2DP sink, got %#v", filtered)
	}
}

func TestSupportsA2DP(t *testing.T) {
	tests := []struct {
		name  string
		uuids []string
		want  bool
	}{
		{name: "exact uuid", uuids: []string{"0000110b-0000-1000-8000-00805f9b34fb"}, want: true},
		{name: "uppercase uuid", uuids: []string{"0000110B-0000-1000-8000-00805F9B34FB"}, want: true},
		{name: "not a2dp", uuids: []string{"00001124-0000-1000-8000-00805f9b34fb"}, want: false},
		{name: "empty", uuids: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsA2DP(tt.uuids); got != tt.want {
				t.Fatalf("supportsA2DP(%v) = %v, want %v", tt.uuids, got, tt.want)
			}
		})
	}
}

func TestListDevicesParsesManagedObjects(t *testing.T) {
	managed := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		dbus.ObjectPath("/org/bluez/hci0/dev_24_C4_06_FA_00_37"): {
			"org.bluez.Device1": {
				"Address":   dbus.MakeVariant("24:C4:06:FA:00:37"),
				"Alias":     dbus.MakeVariant("Speaker A"),
				"Paired":    dbus.MakeVariant(true),
				"Trusted":   dbus.MakeVariant(true),
				"Connected": dbus.MakeVariant(false),
				"UUIDs": dbus.MakeVariant([]string{
					"0000110b-0000-1000-8000-00805f9b34fb",
				}),
			},
		},
		dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF"): {
			"org.bluez.Device1": {
				"Address":   dbus.MakeVariant("AA:BB:CC:DD:EE:FF"),
				"Alias":     dbus.MakeVariant("Keyboard"),
				"Paired":    dbus.MakeVariant(true),
				"Trusted":   dbus.MakeVariant(false),
				"Connected": dbus.MakeVariant(false),
				"UUIDs": dbus.MakeVariant([]string{
					"00001124-0000-1000-8000-00805f9b34fb",
				}),
			},
		},
	}

	devices := parseManagedObjects(managed)
	if len(devices) != 1 {
		t.Fatalf("expected 1 parsed A2DP device, got %d (%#v)", len(devices), devices)
	}
	if devices[0].MAC != "24:C4:06:FA:00:37" {
		t.Fatalf("unexpected MAC: %s", devices[0].MAC)
	}
	if devices[0].Name != "Speaker A" {
		t.Fatalf("unexpected name: %s", devices[0].Name)
	}
}
