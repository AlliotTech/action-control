// Package mimopreview consumes copies of the verified AC204 Mimo video packets.
// It neither opens a network socket nor sends camera commands. See
// docs/MIMO_PREVIEW_PROTOCOL.md for the evidence behind this deliberately narrow
// parser. Unknown formats fail closed instead of being passed to a decoder.
package mimopreview

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"
)

// ReadPCAP accepts classic Ethernet pcap, including a pipe with partial reads.
// It never allocates from an unchecked record length or accepts truncated data.
func ReadPCAP(r io.Reader, consume func(time.Time, []byte) error) error {
	header := make([]byte, 24)
	if _, err := io.ReadFull(r, header); err != nil {
		return fmt.Errorf("pcap header: %w", err)
	}
	var order binary.ByteOrder
	resolution := uint32(1_000_000)
	switch string(header[:4]) {
	case "\xd4\xc3\xb2\xa1":
		order = binary.LittleEndian
	case "\xa1\xb2\xc3\xd4":
		order = binary.BigEndian
	case "\x4d\x3c\xb2\xa1":
		order, resolution = binary.LittleEndian, 1_000_000_000
	case "\xa1\xb2\x3c\x4d":
		order, resolution = binary.BigEndian, 1_000_000_000
	default:
		return errors.New("unsupported pcap magic")
	}
	snaplen := order.Uint32(header[16:20])
	if order.Uint16(header[4:6]) != 2 || order.Uint16(header[6:8]) != 4 || order.Uint32(header[20:24]) != 1 || snaplen < 14 || snaplen > 1<<20 {
		return errors.New("expected bounded Ethernet pcap 2.4")
	}
	record := make([]byte, 16)
	for {
		if _, err := io.ReadFull(r, record); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("pcap record: %w", err)
		}
		seconds, fraction := order.Uint32(record[:4]), order.Uint32(record[4:8])
		size, original := order.Uint32(record[8:12]), order.Uint32(record[12:16])
		if size < 14 || size > snaplen || original != size || fraction >= resolution {
			return errors.New("invalid or truncated pcap record")
		}
		packet := make([]byte, size)
		if _, err := io.ReadFull(r, packet); err != nil {
			return fmt.Errorf("pcap packet: %w", err)
		}
		at := time.Unix(int64(seconds), int64(fraction)*(1_000_000_000/int64(resolution)))
		if err := consume(at, packet); err != nil {
			return err
		}
	}
}

type flow struct {
	source, destination netip.Addr
	port, session       uint16
}

func videoPacket(packet []byte) (flow, []byte, error) {
	var key flow
	if len(packet) < 14 {
		return key, nil, errors.New("short Ethernet header")
	}
	offset, protocol := 14, binary.BigEndian.Uint16(packet[12:14])
	for i := 0; i < 2 && (protocol == 0x8100 || protocol == 0x88a8); i++ {
		if len(packet) < offset+4 {
			return key, nil, errors.New("short VLAN header")
		}
		protocol = binary.BigEndian.Uint16(packet[offset+2 : offset+4])
		offset += 4
	}
	if protocol != 0x0800 {
		return key, nil, nil
	}
	ip := packet[offset:]
	if len(ip) < 20 || ip[0]>>4 != 4 {
		return key, nil, errors.New("invalid IPv4 header")
	}
	ihl, total := int(ip[0]&15)*4, int(binary.BigEndian.Uint16(ip[2:4]))
	if ihl < 20 || total < ihl || total > len(ip) {
		return key, nil, errors.New("invalid IPv4 lengths")
	}
	if ip[9] != 17 {
		return key, nil, nil
	}
	if binary.BigEndian.Uint16(ip[6:8])&0x3fff != 0 {
		return key, nil, errors.New("fragmented IPv4 video is unsupported")
	}
	udp := ip[ihl:total]
	if len(udp) < 8 || int(binary.BigEndian.Uint16(udp[4:6])) != len(udp) {
		return key, nil, errors.New("invalid UDP length")
	}
	if binary.BigEndian.Uint16(udp[:2]) != 9004 {
		return key, nil, nil
	}
	data := udp[8:]
	if len(data) < 8 || binary.LittleEndian.Uint16(data[:2])>>14 != 2 || int(binary.LittleEndian.Uint16(data[:2])&0x3fff) != len(data) || xor(data[:7]) != data[7] {
		return key, nil, errors.New("unverified SW header")
	}
	if data[6] != 2 {
		return key, nil, nil // No control messages, pairing data, or ACK payloads.
	}
	key.source = netip.AddrFrom4([4]byte(ip[12:16]))
	key.destination = netip.AddrFrom4([4]byte(ip[16:20]))
	key.port = binary.BigEndian.Uint16(udp[2:4])
	key.session = binary.LittleEndian.Uint16(data[2:4])
	return key, data, nil
}

func xor(data []byte) byte {
	var sum byte
	for _, value := range data {
		sum ^= value
	}
	return sum
}

type pendingFrame struct {
	parts    [][]byte
	at       time.Time
	received int
	invalid  bool
}

type Stats struct {
	Packets           uint64 `json:"video_packets"`
	CompleteFrames    uint64 `json:"complete_frames"`
	OutputFrames      uint64 `json:"output_frames"`
	SkippedFrames     uint64 `json:"skipped_frames"`
	IncompleteFrames  uint64 `json:"incomplete_frames"`
	DuplicatePackets  uint64 `json:"duplicate_packets"`
	Discontinuities   uint64 `json:"discontinuities"`
	NativeClockResets uint64 `json:"native_clock_resets"`
}

type AccessUnit struct {
	AnnexB   []byte
	Captured time.Time
	Keyframe bool // Contains SPS, PPS, and an IDR in this unit.
}

// Decoder tracks exactly one flow and session. Its 64-frame window is smaller
// than half the 8-bit counter cycle. A long silence/session change requires a
// fresh consumer; guessing the number of counter wraps could join old video.
type Decoder struct {
	Stats                                 Stats
	key                                   flow
	haveKey, haveHighest, haveLast, ready bool
	highest, last                         int64
	lastPacket                            time.Time
	lastNative                            uint32
	pending                               map[int64]*pendingFrame
	completed                             map[int64][][32]byte
}

func (d *Decoder) Feed(at time.Time, ethernet []byte) (result *AccessUnit, err error) {
	defer func() {
		if err != nil {
			d.ready = false
		}
	}()
	key, data, err := videoPacket(ethernet)
	if err != nil || data == nil {
		return nil, err
	}
	if len(data) < 21 || len(data) > 1472 {
		return nil, errors.New("invalid video fragment length")
	}
	word := binary.LittleEndian.Uint32(data[16:20])
	fid, count, index := int64(word&255), int(word>>8&63), int(word>>15&63)
	if word&(1<<14) != 0 || word>>21 != 0 || count == 0 || index >= count {
		return nil, errors.New("FEC or unverified video fragment layout")
	}
	if d.haveKey && key != d.key {
		return nil, errors.New("Mimo preview session changed; reconnect")
	}
	if d.haveKey && (at.Sub(d.lastPacket) > 2*time.Second || at.Sub(d.lastPacket) < -time.Second) {
		return nil, errors.New("Mimo packet clock gap; reconnect")
	}
	if !d.haveKey {
		d.key, d.haveKey = key, true
		d.pending, d.completed = make(map[int64]*pendingFrame), make(map[int64][][32]byte)
	}
	d.lastPacket = at
	d.Stats.Packets++
	absolute := fid
	if d.haveHighest {
		absolute = d.highest + int64(int8(byte(fid)-byte(d.highest)))
	}
	if !d.haveHighest || absolute > d.highest {
		d.highest, d.haveHighest = absolute, true
	}
	if absolute < d.highest-63 {
		return nil, nil
	}
	for id := range d.pending {
		if id < d.highest-63 {
			delete(d.pending, id)
			d.Stats.IncompleteFrames++
		}
	}
	for id := range d.completed {
		if id < d.highest-63 {
			delete(d.completed, id)
		}
	}
	payload := data[20:]
	if old, ok := d.completed[absolute]; ok {
		if len(old) != count || old[index] != sha256.Sum256(payload) {
			return nil, errors.New("conflicting fragment after completed frame")
		}
		d.Stats.DuplicatePackets++
		return nil, nil
	}
	frame := d.pending[absolute]
	if frame == nil {
		frame = &pendingFrame{parts: make([][]byte, count), at: at}
		d.pending[absolute] = frame
	}
	if len(frame.parts) != count || (frame.parts[index] != nil && !bytes.Equal(frame.parts[index], payload)) {
		frame.invalid = true
		return nil, errors.New("conflicting video fragments")
	}
	if frame.invalid {
		return nil, nil
	}
	if frame.parts[index] != nil {
		d.Stats.DuplicatePackets++
		return nil, nil
	}
	frame.parts[index] = bytes.Clone(payload)
	frame.received++
	if frame.received != count {
		return nil, nil
	}
	hashes := make([][32]byte, count)
	for i, part := range frame.parts {
		hashes[i] = sha256.Sum256(part)
	}
	d.completed[absolute] = hashes
	delete(d.pending, absolute)
	d.Stats.CompleteFrames++
	if d.haveLast && absolute <= d.last {
		d.Stats.SkippedFrames++
		return nil, nil
	}
	unit, native, err := multimedia(bytes.Join(frame.parts, nil))
	if err != nil {
		return nil, err
	}
	if d.haveLast {
		if absolute != d.last+1 {
			d.ready = false
			d.Stats.Discontinuities++
		}
		if native < d.lastNative {
			d.ready = false
			d.Stats.NativeClockResets++
		}
	}
	d.last, d.haveLast, d.lastNative = absolute, true, native
	if unit.Keyframe {
		d.ready = true
	}
	if !d.ready {
		d.Stats.SkippedFrames++
		return nil, nil
	}
	unit.Captured = frame.at
	d.Stats.OutputFrames++
	return unit, nil
}

func startCode(data []byte, from int) (int, int) {
	for i := from; i+2 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 {
			continue
		}
		if data[i+2] == 1 {
			return i, 3
		}
		if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
			return i, 4
		}
	}
	return len(data), 0
}

func multimedia(body []byte) (*AccessUnit, uint32, error) {
	if len(body) < 16 || !bytes.Equal(body[:4], []byte{0, 0, 1, 255}) || xor(body[:11]) != 0 || body[11] != 0 || body[8] != 0x90 || body[9] != 0x11 || uint64(binary.LittleEndian.Uint32(body[4:8])) != uint64(len(body)-16) {
		return nil, 0, errors.New("unverified native H.264 multimedia header")
	}
	payload := body[16:]
	position, prefix := startCode(payload, 0)
	if position != 0 || prefix == 0 {
		return nil, 0, errors.New("missing Annex-B access unit")
	}
	unit := &AccessUnit{}
	var sps, pps, idr, slice bool
	for prefix != 0 {
		next, nextPrefix := startCode(payload, position+prefix)
		nal := payload[position+prefix : next]
		if len(nal) == 0 || nal[0]&0x80 != 0 {
			return nil, 0, errors.New("invalid H.264 NAL unit")
		}
		switch nal[0] & 31 {
		case 1, 5:
			if !supportedSlice(nal) {
				return nil, 0, errors.New("B frames or unverified H.264 slice type")
			}
			slice = true
			idr = idr || nal[0]&31 == 5
		case 7:
			if len(nal) < 5 {
				return nil, 0, errors.New("short H.264 SPS")
			}
			sps = true
		case 8:
			if len(nal) < 2 {
				return nil, 0, errors.New("short H.264 PPS")
			}
			pps = true
		case 6, 9:
			position, prefix = next, nextPrefix
			continue // Native SEI/AUD are unsuitable for direct browser playback.
		default:
			return nil, 0, errors.New("unsupported H.264 NAL type")
		}
		unit.AnnexB = append(unit.AnnexB, 0, 0, 0, 1)
		unit.AnnexB = append(unit.AnnexB, nal...)
		position, prefix = next, nextPrefix
	}
	if !slice {
		return nil, 0, errors.New("access unit has no video slice")
	}
	unit.Keyframe = sps && pps && idr
	return unit, binary.LittleEndian.Uint32(body[12:16]), nil
}

// This adapter supplies equal PTS/DTS, so accept only I/P slices. Do not silently
// play a future firmware's reordered B frames with fabricated decode timestamps.
func supportedSlice(nal []byte) bool {
	rbsp := bytes.ReplaceAll(nal[1:], []byte{0, 0, 3}, []byte{0, 0})
	bit := 0
	read := func() (uint32, bool) {
		if bit >= len(rbsp)*8 {
			return 0, false
		}
		value := uint32(rbsp[bit/8] >> (7 - bit%8) & 1)
		bit++
		return value, true
	}
	ue := func() (uint32, bool) {
		zeros := 0
		for {
			b, ok := read()
			if !ok || zeros > 30 {
				return 0, false
			}
			if b == 1 {
				break
			}
			zeros++
		}
		value := uint32(1)
		for range zeros {
			b, ok := read()
			if !ok {
				return 0, false
			}
			value = value<<1 | b
		}
		return value - 1, true
	}
	if _, ok := ue(); !ok { // first_mb_in_slice
		return false
	}
	typ, ok := ue()
	if !ok || typ > 9 {
		return false
	}
	return typ%5 == 2 || (typ%5 == 0 && nal[0]&31 == 1)
}
