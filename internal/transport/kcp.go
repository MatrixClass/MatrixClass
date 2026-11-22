package transport

import (
	"encoding/binary"
	"errors"
	"sync"
)

const (
	IKCP_RTO_NDL     uint32 = 30
	IKCP_RTO_MIN     uint32 = 100
	IKCP_RTO_DEF     uint32 = 200
	IKCP_RTO_MAX     uint32 = 60000
	IKCP_CMD_PUSH    uint8  = 81
	IKCP_CMD_ACK     uint8  = 82
	IKCP_CMD_WASK    uint8  = 83
	IKCP_CMD_WINS    uint8  = 84
	IKCP_ASK_SEND    uint32 = 1
	IKCP_ASK_TELL    uint32 = 2
	IKCP_WND_SND     uint32 = 32
	IKCP_WND_RCV     uint32 = 128
	IKCP_MTU_DEF     uint32 = 1400
	IKCP_ACK_FAST    uint32 = 3
	IKCP_INTERVAL    uint32 = 100
	IKCP_OVERHEAD    uint32 = 24
	IKCP_DEADLINK    uint32 = 20
	IKCP_THRESH_INIT uint32 = 2
	IKCP_THRESH_MIN  uint32 = 2
	IKCP_PROBE_INIT  uint32 = 7000
	IKCP_PROBE_LIMIT uint32 = 120000
)

var (
	ErrKCPInvalidConv  = errors.New("kcp: invalid conversation id")
	ErrKCPBufferTooBig = errors.New("kcp: buffer exceeds maximum fragments")
	ErrKCPShortBuffer  = errors.New("kcp: receive buffer too small")
	ErrKCPInvalidMTU   = errors.New("kcp: mtu too small")
)

type kcpSegment struct {
	conv     uint32
	cmd      uint8
	frg      uint8
	wnd      uint16
	ts       uint32
	sn       uint32
	una      uint32
	resendts uint32
	rto      uint32
	fastack  uint32
	xmit     uint32
	data     []byte
}

type kcpAck struct {
	sn uint32
	ts uint32
}

type KCPStats struct {
	TxBytes     uint64
	RxBytes     uint64
	TxPackets   uint64
	RxPackets   uint64
	Retransmits uint64
}

// KCP implements the KCP ARQ protocol state machine in pure Go.
type KCP struct {
	mu sync.Mutex

	conv      uint32
	mtu       uint32
	mss       uint32
	state     uint32
	sndUna    uint32
	sndNxt    uint32
	rcvNxt    uint32
	ssthresh  uint32
	rxRttVal  int32
	rxSRTT    int32
	rxRTO     int32
	rxMinRTO  int32
	sndWnd    uint32
	rcvWnd    uint32
	rmtWnd    uint32
	cwnd      uint32
	probe     uint32
	current   uint32
	interval  uint32
	tsFlush   uint32
	xmit      uint32
	nodelay   uint32
	updated   bool
	tsProbe   uint32
	probeWait uint32
	deadLink  uint32
	incr      uint32

	sndQueue []*kcpSegment
	rcvQueue []*kcpSegment
	sndBuf   []*kcpSegment
	rcvBuf   []*kcpSegment

	acklist    []kcpAck
	buffer     []byte
	fastResend uint32
	nocwnd     uint32
	stream     bool

	output func(buf []byte, size int)
	stats  KCPStats
}

func NewKCP(conv uint32, output func(buf []byte, size int)) *KCP {
	k := &KCP{
		conv:     conv,
		sndWnd:   IKCP_WND_SND,
		rcvWnd:   IKCP_WND_RCV,
		rmtWnd:   IKCP_WND_RCV,
		mtu:      IKCP_MTU_DEF,
		mss:      IKCP_MTU_DEF - IKCP_OVERHEAD,
		buffer:   make([]byte, (IKCP_MTU_DEF+IKCP_OVERHEAD)*3),
		rxRTO:    int32(IKCP_RTO_DEF),
		rxMinRTO: int32(IKCP_RTO_MIN),
		interval: IKCP_INTERVAL,
		tsFlush:  IKCP_INTERVAL,
		ssthresh: IKCP_THRESH_INIT,
		deadLink: IKCP_DEADLINK,
		output:   output,
	}
	return k
}

func timeDiff(later, earlier uint32) int32 {
	return int32(later - earlier)
}

func minU32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func maxU32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

func boundU32(lower, mid, upper uint32) uint32 {
	return minU32(maxU32(lower, mid), upper)
}

func (k *KCP) SetOutput(output func(buf []byte, size int)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.output = output
}

func (k *KCP) SetStreamMode(stream bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stream = stream
}

func (k *KCP) Conv() uint32 {
	return k.conv
}

func (k *KCP) State() uint32 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.state
}

func (k *KCP) SetMTU(mtu int) error {
	if mtu < 50 || mtu < int(IKCP_OVERHEAD) {
		return ErrKCPInvalidMTU
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.mtu = uint32(mtu)
	k.mss = k.mtu - IKCP_OVERHEAD
	k.buffer = make([]byte, (k.mtu+IKCP_OVERHEAD)*3)
	return nil
}

func (k *KCP) NoDelay(nodelay, interval, resend, nc int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if nodelay >= 0 {
		k.nodelay = uint32(nodelay)
		if nodelay != 0 {
			k.rxMinRTO = int32(IKCP_RTO_NDL)
		} else {
			k.rxMinRTO = int32(IKCP_RTO_MIN)
		}
	}
	if interval >= 0 {
		if interval > 5000 {
			interval = 5000
		} else if interval < 10 {
			interval = 10
		}
		k.interval = uint32(interval)
	}
	if resend >= 0 {
		k.fastResend = uint32(resend)
	}
	if nc >= 0 {
		k.nocwnd = uint32(nc)
	}
}

func (k *KCP) WndSize(sndWnd, rcvWnd int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if sndWnd > 0 {
		k.sndWnd = uint32(sndWnd)
	}
	if rcvWnd > 0 {
		k.rcvWnd = maxU32(uint32(rcvWnd), IKCP_WND_RCV)
	}
}

func (k *KCP) WaitSnd() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.sndBuf) + len(k.sndQueue)
}

func (k *KCP) Stats() KCPStats {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.stats
}

func (k *KCP) PeekSize() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.peekSizeLocked()
}

func (k *KCP) peekSizeLocked() int {
	if len(k.rcvQueue) == 0 {
		return -1
	}
	seg := k.rcvQueue[0]
	if seg.frg == 0 {
		return len(seg.data)
	}
	if len(k.rcvQueue) < int(seg.frg)+1 {
		return -1
	}
	length := 0
	for _, s := range k.rcvQueue {
		length += len(s.data)
		if s.frg == 0 {
			break
		}
	}
	return length
}

func (k *KCP) Recv(buffer []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if len(k.rcvQueue) == 0 {
		return 0, nil
	}

	if k.stream {
		if len(buffer) == 0 {
			return 0, nil
		}
		recoverNeeded := len(k.rcvQueue) >= int(k.rcvWnd)
		readBytes := 0
		consumed := 0
		for i, seg := range k.rcvQueue {
			if readBytes >= len(buffer) {
				break
			}
			n := copy(buffer[readBytes:], seg.data)
			readBytes += n
			if n < len(seg.data) {
				seg.data = seg.data[n:]
				break
			}
			consumed = i + 1
		}
		if consumed > 0 {
			k.rcvQueue = k.rcvQueue[consumed:]
		}
		k.moveRcvBufToQueue()
		if len(k.rcvQueue) < int(k.rcvWnd) && recoverNeeded {
			k.probe |= IKCP_ASK_TELL
		}
		return readBytes, nil
	}

	peekSize := k.peekSizeLocked()
	if peekSize < 0 {
		return 0, nil
	}
	if peekSize > len(buffer) {
		return -1, ErrKCPShortBuffer
	}

	recoverNeeded := len(k.rcvQueue) >= int(k.rcvWnd)
	n := 0
	consumed := 0
	for _, seg := range k.rcvQueue {
		copy(buffer[n:], seg.data)
		n += len(seg.data)
		consumed++
		if seg.frg == 0 {
			break
		}
	}
	if consumed > 0 {
		k.rcvQueue = k.rcvQueue[consumed:]
	}

	k.moveRcvBufToQueue()
	if len(k.rcvQueue) < int(k.rcvWnd) && recoverNeeded {
		k.probe |= IKCP_ASK_TELL
	}
	return n, nil
}

func (k *KCP) moveRcvBufToQueue() {
	moved := 0
	for _, seg := range k.rcvBuf {
		if seg.sn == k.rcvNxt && len(k.rcvQueue) < int(k.rcvWnd) {
			k.rcvQueue = append(k.rcvQueue, seg)
			k.rcvNxt++
			moved++
		} else {
			break
		}
	}
	if moved > 0 {
		k.rcvBuf = k.rcvBuf[moved:]
	}
}

func (k *KCP) Send(buffer []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.mss == 0 {
		return 0, ErrKCPInvalidMTU
	}

	origLen := len(buffer)
	if k.stream {
		sent := 0
		if len(k.sndQueue) > 0 {
			old := k.sndQueue[len(k.sndQueue)-1]
			if len(old.data) < int(k.mss) {
				capacity := int(k.mss) - len(old.data)
				extend := len(buffer)
				if extend > capacity {
					extend = capacity
				}
				old.data = append(old.data, buffer[:extend]...)
				old.frg = 0
				buffer = buffer[extend:]
				sent += extend
			}
		}
		if len(buffer) == 0 {
			return origLen, nil
		}
	}

	var count int
	if len(buffer) <= int(k.mss) {
		count = 1
	} else {
		count = (len(buffer) + int(k.mss) - 1) / int(k.mss)
	}

	if !k.stream && count >= 256 {
		return 0, ErrKCPBufferTooBig
	}
	if count == 0 {
		count = 1
	}

	offset := 0
	for i := 0; i < count; i++ {
		size := len(buffer) - offset
		if size > int(k.mss) {
			size = int(k.mss)
		}
		segData := make([]byte, size)
		if size > 0 {
			copy(segData, buffer[offset:offset+size])
		}
		var frg uint8
		if !k.stream {
			frg = uint8(count - i - 1)
		}
		k.sndQueue = append(k.sndQueue, &kcpSegment{
			conv: k.conv,
			frg:  frg,
			data: segData,
		})
		offset += size
	}
	return origLen, nil
}

func (k *KCP) updateAck(rtt int32) {
	if k.rxSRTT == 0 {
		k.rxSRTT = rtt
		k.rxRttVal = rtt / 2
	} else {
		delta := rtt - k.rxSRTT
		if delta < 0 {
			delta = -delta
		}
		k.rxRttVal = (3*k.rxRttVal + delta) / 4
		k.rxSRTT = (7*k.rxSRTT + rtt) / 8
		if k.rxSRTT < 1 {
			k.rxSRTT = 1
		}
	}
	rto := k.rxSRTT + maxI32(int32(k.interval), 4*k.rxRttVal)
	k.rxRTO = int32(boundU32(uint32(k.rxMinRTO), uint32(rto), IKCP_RTO_MAX))
}

func maxI32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func (k *KCP) shrinkBuf() {
	if len(k.sndBuf) > 0 {
		k.sndUna = k.sndBuf[0].sn
	} else {
		k.sndUna = k.sndNxt
	}
}

func (k *KCP) parseAck(sn uint32) {
	if timeDiff(sn, k.sndUna) < 0 || timeDiff(sn, k.sndNxt) >= 0 {
		return
	}
	for i, seg := range k.sndBuf {
		if sn == seg.sn {
			k.sndBuf = append(k.sndBuf[:i], k.sndBuf[i+1:]...)
			break
		}
		if timeDiff(sn, seg.sn) < 0 {
			break
		}
	}
}

func (k *KCP) parseUna(una uint32) {
	count := 0
	for _, seg := range k.sndBuf {
		if timeDiff(una, seg.sn) > 0 {
			count++
		} else {
			break
		}
	}
	if count > 0 {
		k.sndBuf = k.sndBuf[count:]
	}
}

func (k *KCP) parseFastAck(sn, ts uint32) {
	if timeDiff(sn, k.sndUna) < 0 || timeDiff(sn, k.sndNxt) >= 0 {
		return
	}
	for _, seg := range k.sndBuf {
		if timeDiff(sn, seg.sn) < 0 {
			break
		} else if sn != seg.sn {
			seg.fastack++
		}
	}
}

func (k *KCP) ackPush(sn, ts uint32) {
	k.acklist = append(k.acklist, kcpAck{sn: sn, ts: ts})
}

func (k *KCP) parseData(newseg *kcpSegment) {
	sn := newseg.sn
	if timeDiff(sn, k.rcvNxt+k.rcvWnd) >= 0 || timeDiff(sn, k.rcvNxt) < 0 {
		return
	}
	repeat := false
	insertIdx := len(k.rcvBuf)
	for i := len(k.rcvBuf) - 1; i >= 0; i-- {
		seg := k.rcvBuf[i]
		if seg.sn == sn {
			repeat = true
			break
		}
		if timeDiff(sn, seg.sn) > 0 {
			insertIdx = i + 1
			break
		}
		if i == 0 {
			insertIdx = 0
		}
	}
	if !repeat {
		k.rcvBuf = append(k.rcvBuf, nil)
		copy(k.rcvBuf[insertIdx+1:], k.rcvBuf[insertIdx:])
		k.rcvBuf[insertIdx] = newseg
	}
	k.moveRcvBufToQueue()
}

func (k *KCP) Input(data []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if len(data) < int(IKCP_OVERHEAD) {
		return ErrKCPShortBuffer
	}

	prevUna := k.sndUna
	var maxAck uint32
	var latestTS uint32
	flag := false

	k.stats.RxBytes += uint64(len(data))
	k.stats.RxPackets++

	offset := 0
	for len(data)-offset >= int(IKCP_OVERHEAD) {
		conv := binary.LittleEndian.Uint32(data[offset : offset+4])
		if conv != k.conv {
			return ErrKCPInvalidConv
		}
		cmd := data[offset+4]
		frg := data[offset+5]
		wnd := binary.LittleEndian.Uint16(data[offset+6 : offset+8])
		ts := binary.LittleEndian.Uint32(data[offset+8 : offset+12])
		sn := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
		una := binary.LittleEndian.Uint32(data[offset+16 : offset+20])
		length := binary.LittleEndian.Uint32(data[offset+20 : offset+24])
		offset += int(IKCP_OVERHEAD)

		if len(data)-offset < int(length) {
			return ErrKCPShortBuffer
		}
		if cmd != IKCP_CMD_PUSH && cmd != IKCP_CMD_ACK && cmd != IKCP_CMD_WASK && cmd != IKCP_CMD_WINS {
			return ErrKCPShortBuffer
		}

		k.rmtWnd = uint32(wnd)
		k.parseUna(una)
		k.shrinkBuf()

		switch cmd {
		case IKCP_CMD_ACK:
			if timeDiff(k.current, ts) >= 0 {
				k.updateAck(timeDiff(k.current, ts))
			}
			k.parseAck(sn)
			k.shrinkBuf()
			if !flag {
				flag = true
				maxAck = sn
				latestTS = ts
			} else if timeDiff(sn, maxAck) > 0 {
				maxAck = sn
				latestTS = ts
			}
		case IKCP_CMD_PUSH:
			if timeDiff(sn, k.rcvNxt+k.rcvWnd) < 0 {
				k.ackPush(sn, ts)
				if timeDiff(sn, k.rcvNxt) >= 0 {
					segData := make([]byte, length)
					copy(segData, data[offset:offset+int(length)])
					seg := &kcpSegment{
						conv: conv,
						cmd:  cmd,
						frg:  frg,
						wnd:  wnd,
						ts:   ts,
						sn:   sn,
						una:  una,
						data: segData,
					}
					k.parseData(seg)
				}
			}
		case IKCP_CMD_WASK:
			k.probe |= IKCP_ASK_TELL
		case IKCP_CMD_WINS:
			// Remote window already updated above.
		}

		offset += int(length)
	}

	if flag {
		k.parseFastAck(maxAck, latestTS)
	}

	if timeDiff(k.sndUna, prevUna) > 0 {
		if k.cwnd < k.rmtWnd {
			mss := k.mss
			if k.cwnd < k.ssthresh {
				k.cwnd++
				k.incr += mss
			} else {
				if k.incr < mss {
					k.incr = mss
				}
				k.incr += (mss*mss)/k.incr + mss/16
				if (k.cwnd+1)*mss <= k.incr {
					if mss > 0 {
						k.cwnd = (k.incr + mss - 1) / mss
					} else {
						k.cwnd++
					}
				}
			}
			if k.cwnd > k.rmtWnd {
				k.cwnd = k.rmtWnd
				k.incr = k.rmtWnd * mss
			}
		}
	}

	return nil
}

func (k *KCP) wndUnused() uint16 {
	if len(k.rcvQueue) < int(k.rcvWnd) {
		return uint16(int(k.rcvWnd) - len(k.rcvQueue))
	}
	return 0
}

func encodeSegmentHeader(dst []byte, seg *kcpSegment) {
	binary.LittleEndian.PutUint32(dst[0:4], seg.conv)
	dst[4] = seg.cmd
	dst[5] = seg.frg
	binary.LittleEndian.PutUint16(dst[6:8], seg.wnd)
	binary.LittleEndian.PutUint32(dst[8:12], seg.ts)
	binary.LittleEndian.PutUint32(dst[12:16], seg.sn)
	binary.LittleEndian.PutUint32(dst[16:20], seg.una)
	binary.LittleEndian.PutUint32(dst[20:24], uint32(len(seg.data)))
}

func (k *KCP) emit(size int) {
	if size <= 0 || k.output == nil {
		return
	}
	k.stats.TxBytes += uint64(size)
	k.stats.TxPackets++
	k.output(k.buffer[:size], size)
}

func (k *KCP) Flush() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.flushLocked()
}

func (k *KCP) flushLocked() {
	if !k.updated {
		return
	}

	current := k.current
	lost := false
	change := false
	ptr := 0

	seg := kcpSegment{
		conv: k.conv,
		cmd:  IKCP_CMD_ACK,
		wnd:  k.wndUnused(),
		una:  k.rcvNxt,
	}

	for _, ack := range k.acklist {
		if ptr+int(IKCP_OVERHEAD) > int(k.mtu) {
			k.emit(ptr)
			ptr = 0
		}
		seg.sn = ack.sn
		seg.ts = ack.ts
		encodeSegmentHeader(k.buffer[ptr:], &seg)
		ptr += int(IKCP_OVERHEAD)
	}
	k.acklist = k.acklist[:0]

	if k.rmtWnd == 0 {
		if k.probeWait == 0 {
			k.probeWait = IKCP_PROBE_INIT
			k.tsProbe = k.current + k.probeWait
		} else {
			if timeDiff(k.current, k.tsProbe) >= 0 {
				if k.probeWait < IKCP_PROBE_INIT {
					k.probeWait = IKCP_PROBE_INIT
				}
				k.probeWait += k.probeWait / 2
				if k.probeWait > IKCP_PROBE_LIMIT {
					k.probeWait = IKCP_PROBE_LIMIT
				}
				k.tsProbe = k.current + k.probeWait
				k.probe |= IKCP_ASK_SEND
			}
		}
	} else {
		k.tsProbe = 0
		k.probeWait = 0
	}

	if (k.probe & IKCP_ASK_SEND) != 0 {
		seg.cmd = IKCP_CMD_WASK
		if ptr+int(IKCP_OVERHEAD) > int(k.mtu) {
			k.emit(ptr)
			ptr = 0
		}
		encodeSegmentHeader(k.buffer[ptr:], &seg)
		ptr += int(IKCP_OVERHEAD)
	}
	if (k.probe & IKCP_ASK_TELL) != 0 {
		seg.cmd = IKCP_CMD_WINS
		if ptr+int(IKCP_OVERHEAD) > int(k.mtu) {
			k.emit(ptr)
			ptr = 0
		}
		encodeSegmentHeader(k.buffer[ptr:], &seg)
		ptr += int(IKCP_OVERHEAD)
	}
	k.probe = 0

	cwnd := minU32(k.sndWnd, k.rmtWnd)
	if k.nocwnd == 0 {
		cwnd = minU32(k.cwnd, cwnd)
	}

	moved := 0
	for _, newseg := range k.sndQueue {
		if timeDiff(k.sndNxt, k.sndUna+cwnd) >= 0 {
			break
		}
		newseg.conv = k.conv
		newseg.cmd = IKCP_CMD_PUSH
		newseg.wnd = seg.wnd
		newseg.ts = current
		newseg.sn = k.sndNxt
		newseg.una = k.rcvNxt
		newseg.resendts = current
		newseg.rto = uint32(k.rxRTO)
		newseg.fastack = 0
		newseg.xmit = 0
		k.sndBuf = append(k.sndBuf, newseg)
		k.sndNxt++
		moved++
	}
	if moved > 0 {
		k.sndQueue = k.sndQueue[moved:]
	}

	resent := k.fastResend
	if resent <= 0 {
		resent = 0xffffffff
	}
	rtoMin := uint32(k.rxRTO) >> 3
	if k.nodelay != 0 {
		rtoMin = 0
	}

	for _, s := range k.sndBuf {
		needSend := false
		if s.xmit == 0 {
			needSend = true
			s.xmit++
			s.rto = uint32(k.rxRTO)
			s.resendts = current + s.rto + rtoMin
		} else if timeDiff(current, s.resendts) >= 0 {
			needSend = true
			s.xmit++
			k.xmit++
			k.stats.Retransmits++
			if k.nodelay == 0 {
				s.rto += maxU32(s.rto, uint32(k.rxRTO))
			} else {
				step := s.rto
				if k.nodelay < 2 {
					step = uint32(k.rxRTO)
				}
				s.rto += step / 2
			}
			s.resendts = current + s.rto
			lost = true
		} else if s.fastack >= resent {
			needSend = true
			s.xmit++
			s.fastack = 0
			s.resendts = current + s.rto
			change = true
			k.stats.Retransmits++
		}

		if needSend {
			s.ts = current
			s.wnd = seg.wnd
			s.una = k.rcvNxt
			need := int(IKCP_OVERHEAD) + len(s.data)
			if ptr+need > int(k.mtu) {
				k.emit(ptr)
				ptr = 0
			}
			encodeSegmentHeader(k.buffer[ptr:], s)
			ptr += int(IKCP_OVERHEAD)
			if len(s.data) > 0 {
				copy(k.buffer[ptr:], s.data)
				ptr += len(s.data)
			}
			if s.xmit >= k.deadLink {
				k.state = ^uint32(0)
			}
		}
	}

	if ptr > 0 {
		k.emit(ptr)
	}

	if change {
		inflight := k.sndNxt - k.sndUna
		k.ssthresh = inflight / 2
		if k.ssthresh < IKCP_THRESH_MIN {
			k.ssthresh = IKCP_THRESH_MIN
		}
		k.cwnd = k.ssthresh + resent
		k.incr = k.cwnd * k.mss
	}
	if lost {
		k.ssthresh = cwnd / 2
		if k.ssthresh < IKCP_THRESH_MIN {
			k.ssthresh = IKCP_THRESH_MIN
		}
		k.cwnd = 1
		k.incr = k.mss
	}
	if k.cwnd < 1 {
		k.cwnd = 1
		k.incr = k.mss
	}
}

func (k *KCP) Update(current uint32) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.current = current
	if !k.updated {
		k.updated = true
		k.tsFlush = k.current
	}

	slap := timeDiff(k.current, k.tsFlush)
	if slap >= 10000 || slap < -10000 {
		k.tsFlush = k.current
		slap = 0
	}
	if slap >= 0 {
		k.tsFlush += k.interval
		if timeDiff(k.current, k.tsFlush) >= 0 {
			k.tsFlush = k.current + k.interval
		}
		k.flushLocked()
	}
}

func (k *KCP) Check(current uint32) uint32 {
	k.mu.Lock()
	defer k.mu.Unlock()

	tsFlush := k.tsFlush
	tmPacket := uint32(0x7fffffff)

	if !k.updated {
		return current
	}
	if timeDiff(current, tsFlush) >= 10000 || timeDiff(current, tsFlush) < -10000 {
		tsFlush = current
	}
	if timeDiff(current, tsFlush) >= 0 {
		return current
	}

	tmFlush := uint32(timeDiff(tsFlush, current))
	for _, seg := range k.sndBuf {
		diff := timeDiff(seg.resendts, current)
		if diff <= 0 {
			return current
		}
		if uint32(diff) < tmPacket {
			tmPacket = uint32(diff)
		}
	}

	minimal := tmPacket
	if tmFlush < minimal {
		minimal = tmFlush
	}
	if minimal >= k.interval {
		minimal = k.interval
	}
	return current + minimal
}
