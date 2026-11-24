package transport

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Handshake (student = client, teacher = server), carried over the KCP stream
// before smux starts:
//
//	C -> S  "MCH1" | X25519 ephemeral (32) | nonce (32)
//	S -> C  "MCS1" | X25519 ephemeral (32) | Ed25519 identity (32) | sig (64)
//
// sig = Ed25519(identity, label | SHA-256(client hello | server eph | identity)).
// Both sides derive AES-256-GCM keys per direction with HKDF-SHA256 over the
// X25519 shared secret, so every later byte is confidential and authenticated
// and only a holder of the pinned teacher key can complete the exchange.
const (
	handshakeTimeout   = 6 * time.Second
	clientHelloSize    = 4 + 32 + 32
	serverHelloSize    = 4 + 32 + ed25519.PublicKeySize + ed25519.SignatureSize
	secureMaxRecord    = 16 * 1024
	secureRecordHeader = 4
	handshakeSigLabel  = "MatrixClass/handshake/v1\x00"
)

var (
	clientHelloMagic = []byte("MCH1")
	serverHelloMagic = []byte("MCS1")

	ErrHandshakeFailed = errors.New("transport: secure handshake failed")
	ErrRecordTooLarge  = errors.New("transport: secure record too large")
)

// ServerHandshake authenticates the teacher to a connecting student and
// returns an encrypted connection.
func ServerHandshake(conn net.Conn, id *TeacherIdentity) (net.Conn, error) {
	if id == nil {
		return nil, fmt.Errorf("%w: missing teacher identity", ErrHandshakeFailed)
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	hello := make([]byte, clientHelloSize)
	if _, err := io.ReadFull(conn, hello); err != nil {
		return nil, fmt.Errorf("%w: read client hello: %v", ErrHandshakeFailed, err)
	}
	if !bytes.Equal(hello[:4], clientHelloMagic) {
		return nil, fmt.Errorf("%w: unexpected client hello", ErrHandshakeFailed)
	}
	clientPub, err := ecdh.X25519().NewPublicKey(hello[4:36])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	identity := id.PublicKey()
	transcript := handshakeTranscript(hello, eph.PublicKey().Bytes(), identity)
	sig := id.sign(append([]byte(handshakeSigLabel), transcript...))

	reply := make([]byte, 0, serverHelloSize)
	reply = append(reply, serverHelloMagic...)
	reply = append(reply, eph.PublicKey().Bytes()...)
	reply = append(reply, identity...)
	reply = append(reply, sig...)
	if _, err := conn.Write(reply); err != nil {
		return nil, fmt.Errorf("%w: write server hello: %v", ErrHandshakeFailed, err)
	}
	shared, err := eph.ECDH(clientPub)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	c2s, s2c, err := deriveSessionKeys(shared, transcript)
	if err != nil {
		return nil, err
	}
	return newSecureConn(conn, c2s, s2c)
}

// ClientHandshake verifies the teacher identity against trust and returns an
// encrypted connection plus the teacher public key.
func ClientHandshake(conn net.Conn, trust TeacherTrust) (net.Conn, ed25519.PublicKey, error) {
	if trust == nil {
		trust = NewMemoryTrust("")
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	hello := make([]byte, 0, clientHelloSize)
	hello = append(hello, clientHelloMagic...)
	hello = append(hello, eph.PublicKey().Bytes()...)
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	hello = append(hello, nonce...)
	if _, err := conn.Write(hello); err != nil {
		return nil, nil, fmt.Errorf("%w: write client hello: %v", ErrHandshakeFailed, err)
	}

	reply := make([]byte, serverHelloSize)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, nil, fmt.Errorf("%w: read server hello: %v", ErrHandshakeFailed, err)
	}
	if !bytes.Equal(reply[:4], serverHelloMagic) {
		return nil, nil, fmt.Errorf("%w: unexpected server hello", ErrHandshakeFailed)
	}
	serverEph := reply[4:36]
	identity := ed25519.PublicKey(append([]byte(nil), reply[36:36+ed25519.PublicKeySize]...))
	sig := reply[36+ed25519.PublicKeySize:]
	transcript := handshakeTranscript(hello, serverEph, identity)
	if !ed25519.Verify(identity, append([]byte(handshakeSigLabel), transcript...), sig) {
		return nil, nil, fmt.Errorf("%w: bad teacher signature", ErrHandshakeFailed)
	}
	if err := trust.Accept(identity); err != nil {
		return nil, nil, err
	}
	serverPub, err := ecdh.X25519().NewPublicKey(serverEph)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	shared, err := eph.ECDH(serverPub)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	c2s, s2c, err := deriveSessionKeys(shared, transcript)
	if err != nil {
		return nil, nil, err
	}
	sc, err := newSecureConn(conn, s2c, c2s)
	if err != nil {
		return nil, nil, err
	}
	return sc, identity, nil
}

func handshakeTranscript(clientHello, serverEph, identity []byte) []byte {
	h := sha256.New()
	h.Write(clientHello)
	h.Write(serverEph)
	h.Write(identity)
	return h.Sum(nil)
}

func deriveSessionKeys(shared, transcript []byte) (c2s, s2c []byte, err error) {
	c2s, err = hkdf.Key(sha256.New, shared, transcript, "matrixclass c2s", 32)
	if err != nil {
		return nil, nil, err
	}
	s2c, err = hkdf.Key(sha256.New, shared, transcript, "matrixclass s2c", 32)
	return c2s, s2c, err
}

// secureConn frames the stream into AES-GCM records with counter nonces. The
// underlying KCP stream is reliable and ordered, so counters never repeat.
type secureConn struct {
	net.Conn
	readAEAD  cipher.AEAD
	writeAEAD cipher.AEAD

	rmu      sync.Mutex
	readSeq  uint64
	pending  []byte
	recvBuf  []byte
	wmu      sync.Mutex
	writeSeq uint64
	sendBuf  []byte
}

func newSecureConn(conn net.Conn, readKey, writeKey []byte) (*secureConn, error) {
	readAEAD, err := newGCM(readKey)
	if err != nil {
		return nil, err
	}
	writeAEAD, err := newGCM(writeKey)
	if err != nil {
		return nil, err
	}
	return &secureConn{Conn: conn, readAEAD: readAEAD, writeAEAD: writeAEAD}, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func recordNonce(seq uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}

func (c *secureConn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	written := 0
	for written < len(b) {
		end := written + secureMaxRecord
		if end > len(b) {
			end = len(b)
		}
		chunk := b[written:end]
		need := secureRecordHeader + len(chunk) + c.writeAEAD.Overhead()
		if cap(c.sendBuf) < need {
			c.sendBuf = make([]byte, need)
		}
		out := c.sendBuf[:secureRecordHeader]
		out = c.writeAEAD.Seal(out, recordNonce(c.writeSeq), chunk, nil)
		binary.BigEndian.PutUint32(out[:secureRecordHeader], uint32(len(out)-secureRecordHeader))
		if _, err := c.Conn.Write(out); err != nil {
			return written, err
		}
		c.writeSeq++
		written = end
	}
	return written, nil
}

func (c *secureConn) Read(b []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for len(c.pending) == 0 {
		var hdr [secureRecordHeader]byte
		if _, err := io.ReadFull(c.Conn, hdr[:]); err != nil {
			return 0, err
		}
		size := int(binary.BigEndian.Uint32(hdr[:]))
		if size < c.readAEAD.Overhead() || size > secureMaxRecord+c.readAEAD.Overhead() {
			return 0, ErrRecordTooLarge
		}
		if cap(c.recvBuf) < size {
			c.recvBuf = make([]byte, size)
		}
		record := c.recvBuf[:size]
		if _, err := io.ReadFull(c.Conn, record); err != nil {
			return 0, err
		}
		plain, err := c.readAEAD.Open(record[:0], recordNonce(c.readSeq), record, nil)
		if err != nil {
			return 0, fmt.Errorf("%w: record authentication failed", ErrHandshakeFailed)
		}
		c.readSeq++
		c.pending = plain
	}
	n := copy(b, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// Stats exposes the wrapped KCP statistics when available.
func (c *secureConn) Stats() KCPStats {
	if kc, ok := c.Conn.(*KCPConn); ok {
		return kc.Stats()
	}
	return KCPStats{}
}
