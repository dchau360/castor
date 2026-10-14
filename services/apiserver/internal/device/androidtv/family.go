// Package androidtv casts to Android TV devices, Fire TV among them, through a player app castor installs and drives over ADB.
package androidtv

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/httpu"
	"github.com/huin/goupnp/ssdp"

	"github.com/stupside/castor/services/apiserver/internal/device"
)

const (
	// Android TVs, Fire TV among them, announce DIAL for second-screen apps; castor only uses it to find them.
	searchTarget   = "urn:dial-multiscreen-org:service:dial:1"
	discoverySends = 3
	httpTimeout    = 10 * time.Second
	probeTimeout   = 2 * time.Second
	adbPort        = "5555"

	// patience is what connecting may take the first time: someone allowing castor on the TV, then the install.
	patience = 2 * time.Minute
)

// Family is the Android TV strategy.
type Family struct{}

const familyType device.Type = "androidtv"

func (Family) Type() device.Type { return familyType }

var _ device.Family = Family{}

// description is the part of a DIAL device description castor reads.
type description struct {
	Device struct {
		FriendlyName string `xml:"friendlyName"`
		ModelName    string `xml:"modelName"`
		UDN          string `xml:"UDN"`
	} `xml:"device"`
}

// Discover finds the DIAL devices that also answer on the ADB port: TVs castor can drive, not every Cast or DIAL screen.
func (Family) Discover(ctx context.Context) []device.Info {
	hc, err := httpu.NewHTTPUClient()
	if err != nil {
		slog.WarnContext(ctx, "android tv discovery", "error", err)
		return nil
	}
	defer hc.Close()

	responses, err := ssdp.RawSearch(ctx, hc, searchTarget, discoverySends)
	if err != nil {
		slog.WarnContext(ctx, "android tv discovery", "error", err)
		return nil
	}
	// A TV may announce DIAL more than once; it is one device per host.
	found := map[string]*url.URL{}
	for _, resp := range responses {
		if loc, err := resp.Location(); err == nil && loc.Hostname() != "" {
			if prev, ok := found[loc.Hostname()]; !ok || loc.String() < prev.String() {
				found[loc.Hostname()] = loc
			}
		}
	}
	hosts := slices.Sorted(maps.Keys(found))

	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), httpTimeout)
	defer cancel()
	client := &http.Client{Timeout: httpTimeout}
	reached := make([]*device.Info, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Go(func() {
			if !answers(dctx, net.JoinHostPort(host, adbPort)) {
				return
			}
			// The description only names the TV; one that will not describe itself is still castable.
			desc, err := describe(dctx, client, found[host])
			if err != nil {
				slog.DebugContext(ctx, "android tv description", "host", host, "error", err)
			}
			reached[i] = &device.Info{
				ID:      cmp.Or(desc.Device.UDN, host),
				Name:    cmp.Or(strings.TrimSpace(desc.Device.FriendlyName), strings.TrimSpace(desc.Device.ModelName), host),
				Type:    familyType,
				Address: host,
			}
		})
	}
	wg.Wait()
	var devices []device.Info
	for _, info := range reached {
		if info != nil {
			devices = append(devices, *info)
		}
	}
	return devices
}

// answers is whether addr accepts connections; on the ADB port, only a TV with ADB debugging on does.
func answers(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: probeTimeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = nc.Close()
	return true
}

func describe(ctx context.Context, hc *http.Client, loc *url.URL) (description, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc.String(), nil)
	if err != nil {
		return description{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return description{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return description{}, fmt.Errorf("%s: %s", loc, resp.Status)
	}
	var desc description
	err = xml.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&desc)
	return desc, err
}

// Locate reduces a pinned address to the host's ADB endpoint.
func (Family) Locate(_ context.Context, address string) (string, error) {
	host := address
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	if host == "" {
		return "", fmt.Errorf("android tv address %q has no host", address)
	}
	return device.WithPort(host, adbPort), nil
}

func (Family) Connect(ctx context.Context, info device.Info) (device.Device, error) {
	if _, _, err := net.SplitHostPort(info.Address); err != nil {
		return nil, fmt.Errorf("android tv address %q: %w", info.Address, err)
	}
	key, err := loadKey()
	if err != nil {
		return nil, fmt.Errorf("loading castor's adb key: %w", err)
	}
	s := &session{addr: info.Address, key: key, name: info.Name}

	// The registry's budget suits a device that answers, not a person crossing the room to allow castor;
	// only the caller giving up ends this early.
	patient, cancel := context.WithTimeout(context.WithoutCancel(ctx), patience)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			cancel()
		}
	})
	defer stop()
	if err := s.ensureReceiver(patient); err != nil {
		return nil, err
	}
	return s, nil
}
