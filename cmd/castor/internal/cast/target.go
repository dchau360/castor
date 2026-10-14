package cast

import (
	"context"
	"errors"
	"fmt"
	"strings"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
)

// Device is the device the commands cast to: found by name, or pinned at its host.
type Device struct {
	Name string `yaml:"name" validate:"required_without=Host"`
	Type string `yaml:"type" validate:"required"`
	Host string `yaml:"host"`
}

// Target is the device d names: pinned at its host, or found by name among those the API lists.
func Target(ctx context.Context, devices castorv1connect.DeviceServiceClient, d Device) (*castorv1.Target, error) {
	if d.Type == "" {
		return nil, errors.New("no device to cast to: set device.name and device.type (castor scan lists them)")
	}
	kind, err := configuredDeviceType(d.Type)
	if err != nil {
		return nil, err
	}
	if d.Host != "" {
		return &castorv1.Target{Target: &castorv1.Target_Pinned_{Pinned: &castorv1.Target_Pinned{Type: kind, Address: d.Host}}}, nil
	}
	listed, err := devices.ListDevices(ctx, &castorv1.ListDevicesRequest{})
	if err != nil {
		return nil, err
	}
	for _, found := range listed.GetDevices() {
		if found.GetType() == kind && strings.EqualFold(found.GetName(), d.Name) {
			return &castorv1.Target{Target: &castorv1.Target_DeviceId{DeviceId: found.GetId()}}, nil
		}
	}
	return nil, fmt.Errorf("device %q (type %s) not found", d.Name, d.Type)
}

var deviceTypes = map[string]castorv1.DeviceType{
	"dlna":       castorv1.DeviceType_DEVICE_TYPE_DLNA,
	"chromecast": castorv1.DeviceType_DEVICE_TYPE_CHROMECAST,
	"roku":       castorv1.DeviceType_DEVICE_TYPE_ROKU,
	"androidtv":  castorv1.DeviceType_DEVICE_TYPE_ANDROIDTV,
}

// deviceTypeAliases are other names configuration accepts; scan only ever prints the names above.
var deviceTypeAliases = map[string]string{"firetv": "androidtv"}

func configuredDeviceType(name string) (castorv1.DeviceType, error) {
	if canonical, ok := deviceTypeAliases[name]; ok {
		name = canonical
	}
	kind, ok := deviceTypes[name]
	if !ok {
		return castorv1.DeviceType_DEVICE_TYPE_UNSPECIFIED, fmt.Errorf("unknown device type: %q", name)
	}
	return kind, nil
}

// DeviceTypeName is the family name shown by scan and the device picker, also used in configuration.
func DeviceTypeName(kind castorv1.DeviceType) string {
	for name, k := range deviceTypes {
		if k == kind {
			return name
		}
	}
	return "unknown"
}
