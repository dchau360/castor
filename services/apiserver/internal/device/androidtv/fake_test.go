package androidtv

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeTV is adbd as an Android TV runs it, with a shell that answers through handle.
type fakeTV struct {
	t      *testing.T
	ln     net.Listener
	handle func(command string, stdin []byte) string

	mu      sync.Mutex
	allowed []*rsa.PublicKey
	// prompt is whether a new key is allowed at once, as someone accepting on the TV; false leaves the prompt up.
	prompt   bool
	offered  []string
	commands []string
}

func newFakeTV(t *testing.T, handle func(command string, stdin []byte) string) *fakeTV {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tv := &fakeTV{t: t, ln: ln, handle: handle}
	t.Cleanup(func() { _ = ln.Close() })
	go tv.serve()
	return tv
}

func (tv *fakeTV) addr() string { return tv.ln.Addr().String() }

func (tv *fakeTV) allow(key *rsa.PublicKey) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.allowed = append(tv.allowed, key)
}

func (tv *fakeTV) ran() []string {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return append([]string(nil), tv.commands...)
}

func (tv *fakeTV) serve() {
	for {
		nc, err := tv.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer nc.Close()
			tv.session(nc)
		}()
	}
}

var stdinSize = regexp.MustCompile(`-S (\d+)`)

func (tv *fakeTV) session(nc net.Conn) {
	token := make([]byte, 20)
	_, _ = rand.Read(token)
	for {
		m, err := readMessage(nc)
		if err != nil {
			return
		}
		switch m.cmd {
		case cmdCNXN:
			_ = writeMessage(nc, message{cmd: cmdAUTH, arg0: authToken, data: token})
		case cmdAUTH:
			switch m.arg0 {
			case authSignature:
				if tv.verifies(token, m.data) {
					_ = writeMessage(nc, message{cmd: cmdCNXN, arg0: adbVersion, arg1: 4096, data: []byte("device::")})
				} else {
					_ = writeMessage(nc, message{cmd: cmdAUTH, arg0: authToken, data: token})
				}
			case authRSAPublicKey:
				tv.mu.Lock()
				tv.offered = append(tv.offered, string(m.data))
				accept := tv.prompt
				tv.mu.Unlock()
				if !accept {
					continue
				}
				_ = writeMessage(nc, message{cmd: cmdCNXN, arg0: adbVersion, arg1: 4096, data: []byte("device::")})
			}
		case cmdOPEN:
			command := strings.TrimSuffix(strings.TrimPrefix(string(m.data), "exec:"), "\x00")
			tv.mu.Lock()
			tv.commands = append(tv.commands, command)
			tv.mu.Unlock()
			const remote = 7
			_ = writeMessage(nc, message{cmd: cmdOKAY, arg0: remote, arg1: m.arg0})
			var stdin []byte
			if s := stdinSize.FindStringSubmatch(command); s != nil {
				want, _ := strconv.Atoi(s[1])
				for len(stdin) < want {
					w, err := readMessage(nc)
					if err != nil || w.cmd != cmdWRTE {
						tv.t.Errorf("reading stdin: %v %08x", err, w.cmd)
						return
					}
					stdin = append(stdin, w.data...)
					_ = writeMessage(nc, message{cmd: cmdOKAY, arg0: remote, arg1: m.arg0})
				}
			}
			if out := tv.handle(command, stdin); out != "" {
				_ = writeMessage(nc, message{cmd: cmdWRTE, arg0: remote, arg1: m.arg0, data: []byte(out)})
				if ack, err := readMessage(nc); err != nil || ack.cmd != cmdOKAY {
					tv.t.Errorf("output not acked: %v", err)
					return
				}
			}
			_ = writeMessage(nc, message{cmd: cmdCLSE, arg0: remote, arg1: m.arg0})
		case cmdCLSE:
			return
		}
	}
}

func (tv *fakeTV) verifies(token, sig []byte) bool {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	for _, k := range tv.allowed {
		if rsa.VerifyPKCS1v15(k, crypto.SHA1, token, sig) == nil {
			return true
		}
	}
	return false
}
