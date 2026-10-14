package androidtv

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// The ADB wire protocol, spoken straight to the device's adbd over TCP, so castor needs no adb binary or server.
const (
	cmdCNXN uint32 = 0x4e584e43
	cmdAUTH uint32 = 0x48545541
	cmdOPEN uint32 = 0x4e45504f
	cmdOKAY uint32 = 0x59414b4f
	cmdCLSE uint32 = 0x45534c43
	cmdWRTE uint32 = 0x45545257
	cmdSTLS uint32 = 0x534c5453

	authToken        uint32 = 1
	authSignature    uint32 = 2
	authRSAPublicKey uint32 = 3

	adbVersion  uint32 = 0x01000001
	hostMaxData uint32 = 256 * 1024
	headerSize         = 24
	localID     uint32 = 1
)

// errUnauthorized is a device still waiting for someone to allow castor's key on its screen.
var errUnauthorized = errors.New(`the TV has not allowed castor: on the TV, tick "Always allow from this computer" and choose OK, then cast again`)

type message struct {
	cmd, arg0, arg1 uint32
	data            []byte
}

// conn is one authenticated ADB connection, which castor uses for a single service and closes.
type conn struct {
	nc      net.Conn
	maxData uint32
	stop    func() bool
}

func writeMessage(w io.Writer, m message) error {
	var sum uint32
	for _, b := range m.data {
		sum += uint32(b)
	}
	hdr := make([]byte, headerSize, headerSize+len(m.data))
	binary.LittleEndian.PutUint32(hdr[0:], m.cmd)
	binary.LittleEndian.PutUint32(hdr[4:], m.arg0)
	binary.LittleEndian.PutUint32(hdr[8:], m.arg1)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(m.data)))
	// Devices speaking the first protocol version still verify the payload sum.
	binary.LittleEndian.PutUint32(hdr[16:], sum)
	binary.LittleEndian.PutUint32(hdr[20:], m.cmd^0xffffffff)
	_, err := w.Write(append(hdr, m.data...))
	return err
}

func readMessage(r io.Reader) (message, error) {
	hdr := make([]byte, headerSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return message{}, err
	}
	m := message{
		cmd:  binary.LittleEndian.Uint32(hdr[0:]),
		arg0: binary.LittleEndian.Uint32(hdr[4:]),
		arg1: binary.LittleEndian.Uint32(hdr[8:]),
	}
	n := binary.LittleEndian.Uint32(hdr[12:])
	if binary.LittleEndian.Uint32(hdr[20:]) != m.cmd^0xffffffff {
		return message{}, fmt.Errorf("adb: corrupt message header %x", hdr)
	}
	if n > 1<<20 {
		return message{}, fmt.Errorf("adb: message of %d bytes", n)
	}
	m.data = make([]byte, n)
	if _, err := io.ReadFull(r, m.data); err != nil {
		return message{}, err
	}
	return m, nil
}

// dial connects and authenticates to adbd at addr, bounded by ctx.
func dial(ctx context.Context, addr string, key *rsa.PrivateKey) (*conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("adb debugging unreachable at %s (is ADB debugging on?): %w", addr, err)
	}
	// The socket lives no longer than ctx, so a device that stops answering never holds a call past it.
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	c, err := handshake(nc, key)
	if err != nil {
		stop()
		_ = nc.Close()
		return nil, cmpErr(ctx, err)
	}
	c.stop = stop
	return c, nil
}

// run executes command on the device at addr over a connection of its own.
func run(ctx context.Context, addr string, key *rsa.PrivateKey, command string, stdin []byte) ([]byte, error) {
	c, err := dial(ctx, addr, key)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out, err := c.exec(command, stdin)
	return out, cmpErr(ctx, err)
}

// cmpErr prefers why ctx ended over the read it interrupted, keeping a pending authorization's own explanation.
func cmpErr(ctx context.Context, err error) error {
	if errors.Is(err, errUnauthorized) || ctx.Err() == nil {
		return err
	}
	return context.Cause(ctx)
}

func handshake(nc net.Conn, key *rsa.PrivateKey) (*conn, error) {
	if err := writeMessage(nc, message{cmd: cmdCNXN, arg0: adbVersion, arg1: hostMaxData, data: []byte("host::castor\x00")}); err != nil {
		return nil, err
	}
	signed, offered := false, false
	for {
		m, err := readMessage(nc)
		if err != nil {
			if offered {
				return nil, errUnauthorized
			}
			return nil, fmt.Errorf("adb handshake: %w", err)
		}
		switch m.cmd {
		case cmdCNXN:
			return &conn{nc: nc, maxData: min(m.arg1, hostMaxData)}, nil
		case cmdSTLS:
			return nil, errors.New("the device asks for TLS (Android 11 wireless debugging); turn on plain ADB debugging over the network instead")
		case cmdAUTH:
			if m.arg0 != authToken {
				return nil, fmt.Errorf("adb: unexpected auth type %d", m.arg0)
			}
			switch {
			case !signed:
				signed = true
				sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, m.data)
				if err != nil {
					return nil, fmt.Errorf("adb: signing token: %w", err)
				}
				err = writeMessage(nc, message{cmd: cmdAUTH, arg0: authSignature, data: sig})
				if err != nil {
					return nil, err
				}
			case !offered:
				// The key is new to the device: it shows a prompt and answers CNXN once someone accepts.
				offered = true
				err := writeMessage(nc, message{cmd: cmdAUTH, arg0: authRSAPublicKey, data: append([]byte(publicKey(key)+" castor"), 0)})
				if err != nil {
					return nil, err
				}
			default:
				return nil, errUnauthorized
			}
		default:
			return nil, fmt.Errorf("adb: unexpected %08x during handshake", m.cmd)
		}
	}
}

func (c *conn) Close() error {
	c.stop()
	return c.nc.Close()
}

// exec runs command through the device's shell, writing stdin to it, and returns everything it printed.
func (c *conn) exec(command string, stdin []byte) ([]byte, error) {
	if err := writeMessage(c.nc, message{cmd: cmdOPEN, arg0: localID, data: append([]byte("exec:"+command), 0)}); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	var remote uint32
	// The device acks the OPEN, then each of our writes, with OKAY; input goes one write per ack.
	for {
		m, err := readMessage(c.nc)
		if err != nil {
			return out.Bytes(), fmt.Errorf("adb %q: %w", firstWord(command), err)
		}
		switch m.cmd {
		case cmdOKAY:
			remote = m.arg0
			if len(stdin) > 0 {
				n := min(len(stdin), int(c.maxData))
				if err := writeMessage(c.nc, message{cmd: cmdWRTE, arg0: localID, arg1: remote, data: stdin[:n]}); err != nil {
					return nil, err
				}
				stdin = stdin[n:]
			}
		case cmdWRTE:
			out.Write(m.data)
			if err := writeMessage(c.nc, message{cmd: cmdOKAY, arg0: localID, arg1: m.arg0}); err != nil {
				return nil, err
			}
		case cmdCLSE:
			if remote == 0 {
				return nil, fmt.Errorf("adb: device refused to run %q", firstWord(command))
			}
			_ = writeMessage(c.nc, message{cmd: cmdCLSE, arg0: localID, arg1: remote})
			return out.Bytes(), nil
		default:
			return nil, fmt.Errorf("adb: unexpected %08x running %q", m.cmd, firstWord(command))
		}
	}
}

func firstWord(command string) string {
	word, _, _ := strings.Cut(command, " ")
	return word
}

// shellQuote makes s one word to the device's sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
