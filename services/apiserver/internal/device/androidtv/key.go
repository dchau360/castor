package androidtv

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sync"
)

const keyBits = 2048

// loadKey is castor's own ADB identity, made once: the TV asks to allow it on first contact and remembers it by this key.
var loadKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return keyAt(filepath.Join(dir, "castor", "adbkey"))
})

func keyAt(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("%s holds no PEM key", path)
		}
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".adbkey-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := pem.Encode(tmp, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// A key another castor wrote first wins, so every process presents the one the TV allowed.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return keyAt(path)
		}
		return nil, err
	}
	return key, nil
}

// publicKey is key as adbd stores it: Android's mincrypt RSAPublicKey struct, base64.
func publicKey(key *rsa.PrivateKey) string {
	const words = keyBits / 32
	n := key.N
	r32 := new(big.Int).Lsh(big.NewInt(1), 32)
	n0inv := new(big.Int).ModInverse(new(big.Int).Mod(n, r32), r32)
	n0inv.Sub(r32, n0inv)
	rr := new(big.Int).Exp(big.NewInt(2), big.NewInt(2*keyBits), n)

	buf := make([]byte, 0, 4*(3+2*words))
	buf = binary.LittleEndian.AppendUint32(buf, words)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(n0inv.Uint64()))
	buf = appendWords(buf, n, words)
	buf = appendWords(buf, rr, words)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(key.E))
	return base64.StdEncoding.EncodeToString(buf)
}

// appendWords appends x as count little-endian 32-bit words, least significant first.
func appendWords(buf []byte, x *big.Int, count int) []byte {
	be := x.FillBytes(make([]byte, 4*count))
	for i := count - 1; i >= 0; i-- {
		buf = binary.LittleEndian.AppendUint32(buf, binary.BigEndian.Uint32(be[4*i:]))
	}
	return buf
}
