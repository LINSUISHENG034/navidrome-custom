package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	blueZServiceName       = "org.bluez"
	blueZRootPath          = dbus.ObjectPath("/")
	blueZAdapterInterface  = "org.bluez.Adapter1"
	blueZDeviceInterface   = "org.bluez.Device1"
	dbusObjectMgrInterface = "org.freedesktop.DBus.ObjectManager"
	dbusPropsInterface     = "org.freedesktop.DBus.Properties"
	a2dpSinkUUID           = "0000110b-0000-1000-8000-00805f9b34fb"
)

// BlueZDevice represents a BlueZ device exposed by org.bluez.Device1.
type BlueZDevice struct {
	Path      dbus.ObjectPath `json:"path"`
	MAC       string          `json:"mac"`
	Name      string          `json:"name"`
	Paired    bool            `json:"paired"`
	Trusted   bool            `json:"trusted"`
	Connected bool            `json:"connected"`
	UUIDs     []string        `json:"uuids"`
}

// BlueZManager handles Bluetooth device actions over BlueZ D-Bus.
type BlueZManager interface {
	ListDevices(ctx context.Context) ([]BlueZDevice, error)
	Scan(ctx context.Context, timeout time.Duration) error
	Connect(ctx context.Context, mac string) error
	Disconnect(ctx context.Context, mac string) error
}

type bluezManager struct {
	conn        *dbus.Conn
	adapterPath dbus.ObjectPath
}

func NewBlueZManager() (BlueZManager, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}

	adapterPath, err := findAdapterPath(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &bluezManager{conn: conn, adapterPath: adapterPath}, nil
}

func findAdapterPath(conn *dbus.Conn) (dbus.ObjectPath, error) {
	managed, err := getManagedObjects(conn)
	if err != nil {
		return "", err
	}
	for path, ifaces := range managed {
		if _, ok := ifaces[blueZAdapterInterface]; ok {
			return path, nil
		}
	}
	return "", errors.New("no bluetooth adapter found")
}

func getManagedObjects(conn *dbus.Conn) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	obj := conn.Object(blueZServiceName, blueZRootPath)
	var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := obj.Call(dbusObjectMgrInterface+".GetManagedObjects", 0).Store(&managed); err != nil {
		return nil, err
	}
	return managed, nil
}

func (m *bluezManager) ListDevices(_ context.Context) ([]BlueZDevice, error) {
	managed, err := getManagedObjects(m.conn)
	if err != nil {
		return nil, err
	}
	return parseManagedObjects(managed), nil
}

func parseManagedObjects(managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant) []BlueZDevice {
	devices := make([]BlueZDevice, 0, len(managed))
	for path, ifaces := range managed {
		props, ok := ifaces[blueZDeviceInterface]
		if !ok {
			continue
		}

		mac := variantString(props, "Address")
		if mac == "" {
			mac = macFromPath(path)
		}
		name := variantString(props, "Alias")
		if name == "" {
			name = variantString(props, "Name")
		}
		if name == "" {
			name = mac
		}

		devices = append(devices, BlueZDevice{
			Path:      path,
			MAC:       normalizeMAC(mac),
			Name:      name,
			Paired:    variantBool(props, "Paired"),
			Trusted:   variantBool(props, "Trusted"),
			Connected: variantBool(props, "Connected"),
			UUIDs:     variantStringSlice(props, "UUIDs"),
		})
	}
	return filterA2DPSinks(devices)
}

func filterA2DPSinks(in []BlueZDevice) []BlueZDevice {
	out := make([]BlueZDevice, 0, len(in))
	for _, d := range in {
		if supportsA2DP(d.UUIDs) {
			out = append(out, d)
		}
	}
	return out
}

func supportsA2DP(uuids []string) bool {
	for _, u := range uuids {
		if strings.EqualFold(u, a2dpSinkUUID) {
			return true
		}
	}
	return false
}

func variantString(props map[string]dbus.Variant, key string) string {
	v, ok := props[key]
	if !ok {
		return ""
	}
	s, _ := v.Value().(string)
	return s
}

func variantBool(props map[string]dbus.Variant, key string) bool {
	v, ok := props[key]
	if !ok {
		return false
	}
	b, _ := v.Value().(bool)
	return b
}

func variantStringSlice(props map[string]dbus.Variant, key string) []string {
	v, ok := props[key]
	if !ok {
		return nil
	}
	switch vv := v.Value().(type) {
	case []string:
		return vv
	case []interface{}:
		out := make([]string, 0, len(vv))
		for _, i := range vv {
			s, ok := i.(string)
			if ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeMAC(mac string) string {
	mac = strings.TrimSpace(mac)
	if mac == "" {
		return ""
	}
	mac = strings.ReplaceAll(mac, "_", ":")
	return strings.ToUpper(mac)
}

func macFromPath(path dbus.ObjectPath) string {
	raw := string(path)
	idx := strings.LastIndex(raw, "/dev_")
	if idx < 0 {
		return ""
	}
	segment := raw[idx+len("/dev_"):]
	return normalizeMAC(segment)
}

func (m *bluezManager) Scan(ctx context.Context, timeout time.Duration) error {
	adapter := m.conn.Object(blueZServiceName, m.adapterPath)
	if err := adapter.Call(blueZAdapterInterface+".StartDiscovery", 0).Err; err != nil {
		return err
	}

	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	select {
	case <-time.After(timeout):
	case <-ctx.Done():
	}

	if err := adapter.Call(blueZAdapterInterface+".StopDiscovery", 0).Err; err != nil {
		return err
	}

	return ctx.Err()
}

func (m *bluezManager) Connect(ctx context.Context, mac string) error {
	devicePath, err := m.findDevicePathByMAC(mac)
	if err != nil {
		return err
	}
	device := m.conn.Object(blueZServiceName, devicePath)
	if err := device.Call(dbusPropsInterface+".Set", 0, blueZDeviceInterface, "Trusted", dbus.MakeVariant(true)).Err; err != nil {
		return err
	}
	return device.CallWithContext(ctx, blueZDeviceInterface+".Connect", 0).Err
}

func (m *bluezManager) Disconnect(ctx context.Context, mac string) error {
	devicePath, err := m.findDevicePathByMAC(mac)
	if err != nil {
		return err
	}
	device := m.conn.Object(blueZServiceName, devicePath)
	return device.CallWithContext(ctx, blueZDeviceInterface+".Disconnect", 0).Err
}

func (m *bluezManager) findDevicePathByMAC(mac string) (dbus.ObjectPath, error) {
	normalized := normalizeMAC(mac)
	if normalized == "" {
		return "", errors.New("mac is required")
	}

	managed, err := getManagedObjects(m.conn)
	if err != nil {
		return "", err
	}

	for path, ifaces := range managed {
		props, ok := ifaces[blueZDeviceInterface]
		if !ok {
			continue
		}
		if strings.EqualFold(normalizeMAC(variantString(props, "Address")), normalized) {
			return path, nil
		}
	}

	// Fall back to the canonical BlueZ device path shape.
	devID := strings.ReplaceAll(normalized, ":", "_")
	return dbus.ObjectPath(fmt.Sprintf("%s/dev_%s", m.adapterPath, devID)), nil
}
