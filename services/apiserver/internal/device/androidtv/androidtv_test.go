package androidtv

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"math/big"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
)

var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		panic(err)
	}
	return k
})

func allowedSession(t *testing.T, handle func(string, []byte) string) (*session, *fakeTV) {
	t.Helper()
	tv := newFakeTV(t, handle)
	tv.allow(&testKey().PublicKey)
	return &session{addr: tv.addr(), key: testKey(), name: "TV"}, tv
}

func TestRunWithAllowedKey(t *testing.T) {
	s, _ := allowedSession(t, func(cmd string, _ []byte) string { return "ran " + cmd })
	out, err := s.run(t.Context(), "echo hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "ran echo hi" {
		t.Fatalf("output %q", out)
	}
}

func TestRunOffersNewKeyForApproval(t *testing.T) {
	tv := newFakeTV(t, func(string, []byte) string { return "ok" })
	tv.mu.Lock()
	tv.prompt = true
	tv.mu.Unlock()
	if _, err := run(t.Context(), tv.addr(), testKey(), "true", nil); err != nil {
		t.Fatal(err)
	}
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if want := publicKey(testKey()) + " castor\x00"; len(tv.offered) != 1 || tv.offered[0] != want {
		t.Fatalf("offered %q", tv.offered)
	}
}

func TestRunExplainsPendingApproval(t *testing.T) {
	tv := newFakeTV(t, func(string, []byte) string { return "" })
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, err := run(ctx, tv.addr(), testKey(), "true", nil)
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("err = %v, want the prompt explained", err)
	}
}

func TestRunUnreachable(t *testing.T) {
	tv := newFakeTV(t, nil)
	addr := tv.addr()
	_ = tv.ln.Close()
	_, err := run(t.Context(), addr, testKey(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), "ADB debugging") {
		t.Fatalf("err = %v", err)
	}
}

// installer is a package manager holding version, answering install with refusals in turn and then Success.
type installer struct {
	mu        sync.Mutex
	version   string
	refusals  []string
	installed [][]byte
	removed   int
}

func (p *installer) handle(cmd string, stdin []byte) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "dumpsys package "+receiverPackage):
		if p.version == "" {
			return "Dexopt state:\n"
		}
		return "Packages:\n  Package [" + receiverPackage + "]\n    versionCode=" + p.version + " minSdk=25 targetSdk=30\n"
	case strings.HasPrefix(cmd, "cmd package install -r -S "):
		if len(p.refusals) > 0 {
			r := p.refusals[0]
			p.refusals = p.refusals[1:]
			return r
		}
		p.installed = append(p.installed, stdin)
		p.version = strconv.Itoa(receiverVersion())
		return "Success\n"
	case cmd == "pm uninstall "+receiverPackage:
		p.removed++
		p.version = ""
		return "Success\n"
	}
	return ""
}

func TestEnsureReceiverInstallsOnlyAMissingOrOtherVersion(t *testing.T) {
	pm := &installer{}
	s, _ := allowedSession(t, pm.handle)
	for range 2 {
		if err := s.ensureReceiver(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	pm.mu.Lock()
	if len(pm.installed) != 1 || !bytes.Equal(pm.installed[0], receiverAPK) {
		t.Fatalf("installed %d times", len(pm.installed))
	}
	pm.version = "999"
	pm.mu.Unlock()
	if err := s.ensureReceiver(t.Context()); err != nil {
		t.Fatal(err)
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if len(pm.installed) != 2 {
		t.Fatalf("another version was kept")
	}
}

func TestEnsureReceiverReplacesAnInstallItCannotUpdate(t *testing.T) {
	pm := &installer{version: "999", refusals: []string{"Failure [INSTALL_FAILED_VERSION_DOWNGRADE: Downgrade detected]"}}
	s, _ := allowedSession(t, pm.handle)
	if err := s.ensureReceiver(t.Context()); err != nil {
		t.Fatal(err)
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.removed != 1 || len(pm.installed) != 1 {
		t.Fatalf("removed %d, installed %d", pm.removed, len(pm.installed))
	}
}

func TestEnsureReceiverReportsARefusal(t *testing.T) {
	pm := &installer{refusals: []string{"Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]"}}
	s, _ := allowedSession(t, pm.handle)
	err := s.ensureReceiver(t.Context())
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "INSUFFICIENT_STORAGE") || pm.removed != 0 {
		t.Fatalf("err = %v, removed %d", err, pm.removed)
	}
}

func TestPlayHandsTheURLAsOneWord(t *testing.T) {
	s, tv := allowedSession(t, func(string, []byte) string { return "Status: ok\n" })
	u, _ := url.Parse("http://192.168.1.2:8410/media/x?a=1&b='2'")
	if err := s.Play(t.Context(), u, mediav1.Container_CONTAINER_HLS); err != nil {
		t.Fatal(err)
	}
	cmd := tv.ran()[0]
	want := `-d 'http://192.168.1.2:8410/media/x?a=1&b='\''2'\''' --es format hls`
	if !strings.HasPrefix(cmd, "am start -W -n "+receiverActivity) || !strings.Contains(cmd, want) {
		t.Fatalf("command %q", cmd)
	}
}

func TestPlayRefusesWhatThePlayerCannotOpen(t *testing.T) {
	s, _ := allowedSession(t, func(string, []byte) string { return "" })
	if err := s.Play(t.Context(), &url.URL{Scheme: "http", Host: "x"}, mediav1.Container_CONTAINER_WEBM); err == nil {
		t.Fatal("webm accepted")
	}
}

func TestPlayReportsAMissingPlayer(t *testing.T) {
	s, _ := allowedSession(t, func(string, []byte) string {
		return "Error: Activity class {" + receiverActivity + "} does not exist.\n"
	})
	if err := s.Play(t.Context(), &url.URL{Scheme: "http", Host: "x"}, mediav1.Container_CONTAINER_MP4); err == nil {
		t.Fatal("missing activity accepted")
	}
}

func TestControlReadsThePlayersAnswer(t *testing.T) {
	answers := map[string]string{
		"pause":  "Broadcasting: Intent { act=x }\nBroadcast completed: result=1, data=\"paused\"\n",
		"resume": "Broadcasting: Intent { act=x }\nBroadcast completed: result=0\n",
	}
	s, _ := allowedSession(t, func(cmd string, _ []byte) string {
		op := cmd[strings.LastIndex(cmd, " ")+1:]
		return answers[op]
	})
	if err := s.Pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(t.Context()); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("resume with no player: %v", err)
	}
}

func TestEnding(t *testing.T) {
	for _, tc := range []struct {
		state  string
		active bool
		over   bool
		fails  bool
	}{
		{state: "", active: false, over: false},
		{state: "", active: true, over: true},
		{state: "loading", active: true, over: false},
		{state: "paused", active: true, over: false},
		{state: "ended", active: true, over: true},
		{state: "failed:1/-1004", active: false, over: true, fails: true},
	} {
		over, err := ending(tc.state, tc.active)
		if over != tc.over || (err != nil) != tc.fails {
			t.Errorf("ending(%q, %v) = %v, %v", tc.state, tc.active, over, err)
		}
	}
}

func TestAwaitEndReportsAFailure(t *testing.T) {
	s, _ := allowedSession(t, func(string, []byte) string {
		return "Broadcast completed: result=1, data=\"failed:1/-1004\"\n"
	})
	err := s.AwaitEnd(t.Context())
	if err == nil || !strings.Contains(err.Error(), "1/-1004") {
		t.Fatalf("err = %v", err)
	}
}

func TestPublicKeyIsMincryptLayout(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(publicKey(testKey()))
	if err != nil {
		t.Fatal(err)
	}
	const words = keyBits / 32
	if len(raw) != 4*(3+2*words) {
		t.Fatalf("%d bytes", len(raw))
	}
	if binary.LittleEndian.Uint32(raw) != words {
		t.Fatalf("length word %d", binary.LittleEndian.Uint32(raw))
	}
	n := new(big.Int)
	for i := words - 1; i >= 0; i-- {
		n.Lsh(n, 32).Or(n, big.NewInt(int64(binary.LittleEndian.Uint32(raw[8+4*i:]))))
	}
	if n.Cmp(testKey().N) != 0 {
		t.Fatal("modulus differs")
	}
	n0inv := uint64(binary.LittleEndian.Uint32(raw[4:]))
	if uint32(n0inv*n.Uint64()) != 0xffffffff {
		t.Fatal("n0inv is not -1/n mod 2^32")
	}
	if e := binary.LittleEndian.Uint32(raw[len(raw)-4:]); int(e) != testKey().E {
		t.Fatalf("exponent %d", e)
	}
}

func TestKeyAtKeepsTheFirstKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "castor", "adbkey")
	first, err := keyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := keyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(again) {
		t.Fatal("a second key was made")
	}
}

func TestLocate(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.20":             "192.168.1.20:5555",
		"192.168.1.20:5556":        "192.168.1.20:5556",
		"http://192.168.1.20:8009": "192.168.1.20:5555",
	} {
		got, err := Family{}.Locate(t.Context(), in)
		if err != nil || got != want {
			t.Errorf("Locate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestDescriptionNamesTheTV(t *testing.T) {
	const miBox = `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<friendlyName>MIBOX4</friendlyName><manufacturer>Xiaomi</manufacturer><modelName>MIBOX4</modelName>
<UDN>uuid:1234</UDN></device></root>`
	var d description
	if err := xml.Unmarshal([]byte(miBox), &d); err != nil || d.Device.FriendlyName != "MIBOX4" || d.Device.UDN != "uuid:1234" {
		t.Fatalf("%+v, %v", d, err)
	}
}

func TestAnswersOnlyAnOpenPort(t *testing.T) {
	tv := newFakeTV(t, nil)
	if !answers(t.Context(), tv.addr()) {
		t.Fatal("listening port not answered")
	}
	addr := tv.addr()
	_ = tv.ln.Close()
	if answers(t.Context(), addr) {
		t.Fatal("closed port answered")
	}
}
