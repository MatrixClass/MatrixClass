package transport

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"sync"
	"testing"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
)

type delayedPacket struct {
	deliverAt uint32
	data      []byte
}

type lossyLink struct {
	mu       sync.Mutex
	rng      *mrand.Rand
	lossRate float64
	minDelay uint32
	maxDelay uint32
	aToB     []delayedPacket
	bToA     []delayedPacket
}

func newLossyLink(seed int64, lossRate float64, minDelay, maxDelay uint32) *lossyLink {
	return &lossyLink{
		rng:      mrand.New(mrand.NewSource(seed)),
		lossRate: lossRate,
		minDelay: minDelay,
		maxDelay: maxDelay,
	}
}

func (l *lossyLink) send(fromA bool, now uint32, buf []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rng.Float64() < l.lossRate {
		return
	}
	jitter := uint32(0)
	if l.maxDelay > l.minDelay {
		jitter = uint32(l.rng.Intn(int(l.maxDelay - l.minDelay + 1)))
	}
	cp := make([]byte, len(buf))
	copy(cp, buf)
	pkt := delayedPacket{
		deliverAt: now + l.minDelay + jitter,
		data:      cp,
	}
	if fromA {
		l.aToB = append(l.aToB, pkt)
	} else {
		l.bToA = append(l.bToA, pkt)
	}
}

func (l *lossyLink) popReady(forB bool, now uint32) [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	queue := &l.aToB
	if !forB {
		queue = &l.bToA
	}
	var ready [][]byte
	remaining := (*queue)[:0]
	for _, p := range *queue {
		if p.deliverAt <= now {
			ready = append(ready, p.data)
		} else {
			remaining = append(remaining, p)
		}
	}
	*queue = remaining
	return ready
}

func TestKCPARQLossAndReordering(t *testing.T) {
	var now uint32 = 0
	link := newLossyLink(20261118, 0.15, 5, 45)

	kcpA := NewKCP(0x11223344, func(buf []byte, size int) {
		link.send(true, now, buf[:size])
	})
	kcpB := NewKCP(0x11223344, func(buf []byte, size int) {
		link.send(false, now, buf[:size])
	})

	kcpA.NoDelay(1, 10, 2, 1)
	kcpB.NoDelay(1, 10, 2, 1)
	kcpA.WndSize(128, 128)
	kcpB.WndSize(128, 128)
	_ = kcpA.SetMTU(512)
	_ = kcpB.SetMTU(512)

	const totalMessages = 120
	expected := make([][]byte, totalMessages)
	for i := 0; i < totalMessages; i++ {
		// Mix multi-fragment messages (> MTU) and small messages
		size := 200 + (i%7)*350
		msg := make([]byte, size)
		for j := range msg {
			msg[j] = byte((i*31 + j) & 0xff)
		}
		expected[i] = msg
	}

	sentIdx := 0
	var received [][]byte
	recvBuf := make([]byte, 8192)

	maxSimMS := uint32(60000)
	for now = 0; now < maxSimMS && len(received) < totalMessages; now += 5 {
		for sentIdx < totalMessages && kcpA.WaitSnd() < 64 {
			if _, err := kcpA.Send(expected[sentIdx]); err != nil {
				t.Fatalf("kcpA.Send(%d) failed: %v", sentIdx, err)
			}
			sentIdx++
		}

		if kcpA.Check(now) <= now {
			kcpA.Update(now)
		}
		if kcpB.Check(now) <= now {
			kcpB.Update(now)
		}

		for _, pkt := range link.popReady(true, now) {
			if err := kcpB.Input(pkt); err != nil {
				t.Fatalf("kcpB.Input failed: %v", err)
			}
		}
		for _, pkt := range link.popReady(false, now) {
			if err := kcpA.Input(pkt); err != nil {
				t.Fatalf("kcpA.Input failed: %v", err)
			}
		}

		for {
			n, err := kcpB.Recv(recvBuf)
			if err != nil {
				t.Fatalf("kcpB.Recv failed: %v", err)
			}
			if n <= 0 {
				break
			}
			got := make([]byte, n)
			copy(got, recvBuf[:n])
			received = append(received, got)
		}
	}

	if len(received) != totalMessages {
		t.Fatalf("incomplete delivery under 15%% loss: got %d/%d messages", len(received), totalMessages)
	}
	for i := 0; i < totalMessages; i++ {
		if !bytes.Equal(received[i], expected[i]) {
			t.Fatalf("message %d corrupted: got len=%d want len=%d", i, len(received[i]), len(expected[i]))
		}
	}
	stats := kcpA.Stats()
	if stats.Retransmits == 0 {
		t.Fatalf("expected retransmissions under 15%% packet loss, got 0")
	}
	t.Logf("KCP 15%% loss & reorder passed in %dms simulated time (txPkts=%d, retransmits=%d)",
		now, stats.TxPackets, stats.Retransmits)
}

func TestKCPWindowProbeCommands(t *testing.T) {
	var wireAtoB, wireBtoA [][]byte
	kcpA := NewKCP(77, func(buf []byte, size int) {
		cp := make([]byte, size)
		copy(cp, buf[:size])
		wireAtoB = append(wireAtoB, cp)
	})
	kcpB := NewKCP(77, func(buf []byte, size int) {
		cp := make([]byte, size)
		copy(cp, buf[:size])
		wireBtoA = append(wireBtoA, cp)
	})

	// Force remote window to 0 on kcpA to exercise IKCP_CMD_WASK / IKCP_CMD_WINS
	kcpA.rmtWnd = 0
	kcpA.Update(0)
	kcpA.Update(8000)

	for _, p := range wireAtoB {
		if err := kcpB.Input(p); err != nil {
			t.Fatalf("kcpB.Input: %v", err)
		}
	}
	wireAtoB = nil
	kcpB.Update(8100)

	if len(wireBtoA) == 0 {
		t.Fatalf("expected IKCP_CMD_WINS response from kcpB")
	}
	for _, p := range wireBtoA {
		if err := kcpA.Input(p); err != nil {
			t.Fatalf("kcpA.Input: %v", err)
		}
	}
	if kcpA.rmtWnd == 0 {
		t.Fatalf("expected kcpA remote window to recover after WINS, still 0")
	}
}

func TestFECRecoveryAndAESGCM(t *testing.T) {
	enc, err := NewFECEncoder(6, 3)
	if err != nil {
		t.Fatalf("NewFECEncoder: %v", err)
	}
	dec := NewFECDecoder()

	original := make([]byte, 9500)
	if _, err := rand.Read(original); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	shards, err := enc.EncodeBlock(original)
	if err != nil {
		t.Fatalf("EncodeBlock: %v", err)
	}
	if len(shards) != 9 {
		t.Fatalf("expected 9 shards, got %d", len(shards))
	}

	// Drop 3 shards (indices 0, 3, 5 — two data shards + one data shard = 3 lost shards, matching parityShards=3)
	// Feed remaining shards in reverse order to also verify out-of-order reconstruction.
	dropped := map[int]bool{0: true, 3: true, 5: true}
	var recovered []byte
	for i := len(shards) - 1; i >= 0; i-- {
		if dropped[i] {
			continue
		}
		block, ready, err := dec.DecodeBlock(shards[i])
		if err != nil {
			t.Fatalf("DecodeBlock shard %d: %v", i, err)
		}
		if ready {
			recovered = block
		}
	}
	if !bytes.Equal(recovered, original) {
		t.Fatalf("FEC block recovery mismatch: got %d bytes, want %d bytes", len(recovered), len(original))
	}

	// Test streaming stripe mode as well
	streamEnc, err := NewFECEncoder(4, 2)
	if err != nil {
		t.Fatalf("NewFECEncoder stream: %v", err)
	}
	streamDec := NewFECDecoder()
	inputs := [][]byte{
		[]byte("alpha-packet"),
		[]byte("bravo-longer-packet-payload"),
		[]byte("charlie"),
		[]byte("delta-end-of-stripe"),
	}
	var stripe [][]byte
	for _, in := range inputs {
		out, err := streamEnc.AddPacket(in)
		if err != nil {
			t.Fatalf("AddPacket: %v", err)
		}
		if out != nil {
			stripe = out
		}
	}
	if len(stripe) != 6 {
		t.Fatalf("expected 6 stripe packets, got %d", len(stripe))
	}
	// Drop 2 data packets (indices 1 and 2)
	var recoveredStripe [][]byte
	for idx, raw := range stripe {
		if idx == 1 || idx == 2 {
			continue
		}
		pkts, err := streamDec.DecodeStreamPacket(raw)
		if err != nil {
			t.Fatalf("DecodeStreamPacket: %v", err)
		}
		if pkts != nil {
			recoveredStripe = pkts
		}
	}
	if len(recoveredStripe) != len(inputs) {
		t.Fatalf("expected %d recovered packets, got %d", len(inputs), len(recoveredStripe))
	}
	for i := range inputs {
		if !bytes.Equal(recoveredStripe[i], inputs[i]) {
			t.Fatalf("stripe packet %d mismatch: got %q want %q", i, recoveredStripe[i], inputs[i])
		}
	}

	// Test AES-GCM FrameCipher
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cipher, err := NewFrameCipher(key)
	if err != nil {
		t.Fatalf("NewFrameCipher: %v", err)
	}
	ct, err := cipher.Encrypt(original)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	pt, err := cipher.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(pt, original) {
		t.Fatalf("AES-GCM roundtrip mismatch")
	}
	ct[len(ct)-1] ^= 0xff
	if _, err := cipher.Decrypt(ct); err == nil {
		t.Fatalf("expected tampered AES-GCM ciphertext to fail authentication")
	}
}

func TestKCPConnAndSmuxConcurrentStreams(t *testing.T) {
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	fc, err := NewFrameCipher(key)
	if err != nil {
		t.Fatalf("NewFrameCipher: %v", err)
	}
	cfg := DefaultKCPConfig()
	cfg.Cipher = fc

	listener, err := ListenKCP("127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("ListenKCP: %v", err)
	}
	defer listener.Close()

	serverErrCh := make(chan error, 1)
	envReceived := make(chan protocol.Envelope, 4)

	var serverSession *SmuxSession
	serverReady := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		sess, err := NewSmuxSession(conn, true, func(_ core.Session, env protocol.Envelope) {
			envReceived <- env
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		serverSession = sess
		close(serverReady)

		// Echo back every stream concurrently
		for {
			kind, st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func(k protocol.StreamKind, stream io.ReadWriteCloser) {
				defer stream.Close()
				fk, mt, data, err := protocol.ReadFrame(stream)
				if err != nil {
					return
				}
				if fk != k {
					return
				}
				_ = protocol.WriteFrame(stream, k, mt, data)
			}(kind, st)
		}
	}()

	clientConn, err := DialKCP(listener.Addr().String(), cfg)
	if err != nil {
		t.Fatalf("DialKCP: %v", err)
	}
	clientSession, err := NewSmuxSession(clientConn, false, nil)
	if err != nil {
		t.Fatalf("NewSmuxSession client: %v", err)
	}
	defer clientSession.Close()

	// Send control envelope first (triggers KCP packet so server Accept completes)
	helloEnv, err := protocol.NewEnvelope(protocol.MsgHello, "student-01", map[string]string{"name": "Alice"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := clientSession.SendEnvelope(helloEnv); err != nil {
		t.Fatalf("SendEnvelope: %v", err)
	}

	select {
	case <-serverReady:
	case err := <-serverErrCh:
		t.Fatalf("server error: %v", err)
	case <-time.NewTimer(5 * time.Second).C:
		t.Fatalf("timed out waiting for server session")
	}
	defer serverSession.Close()

	select {
	case gotEnv := <-envReceived:
		if gotEnv.Type != protocol.MsgHello || gotEnv.SenderID != "student-01" {
			t.Fatalf("unexpected envelope: %+v", gotEnv)
		}
	case <-time.NewTimer(5 * time.Second).C:
		t.Fatalf("timed out waiting for control envelope")
	}

	// Run 12 concurrent streams with random payloads up to 32KB each
	const concurrency = 12
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			kind := protocol.StreamKind((idx % 4) + 2) // StreamMedia..StreamTelemetry
			st, err := clientSession.OpenStream(kind)
			if err != nil {
				errCh <- fmt.Errorf("stream %d OpenStream: %w", idx, err)
				return
			}
			nBig, _ := rand.Int(rand.Reader, big.NewInt(16384))
			size := 8192 + int(nBig.Int64())
			payload := make([]byte, size)
			_, _ = rand.Read(payload)

			if err := protocol.WriteFrame(st, kind, protocol.MsgFileChunk, payload); err != nil {
				_ = st.Close()
				errCh <- fmt.Errorf("stream %d WriteFrame: %w", idx, err)
				return
			}
			gotKind, gotType, echoed, err := protocol.ReadFrame(st)
			_ = st.Close()
			if err != nil {
				errCh <- fmt.Errorf("stream %d ReadFrame: %w", idx, err)
				return
			}
			if gotKind != kind || gotType != protocol.MsgFileChunk || !bytes.Equal(echoed, payload) {
				errCh <- fmt.Errorf("stream %d echo mismatch: kind=%d/%d len=%d/%d", idx, gotKind, kind, len(echoed), len(payload))
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
}
