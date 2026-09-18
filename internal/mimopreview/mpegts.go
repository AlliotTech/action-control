package mimopreview

import "time"

// Muxer wraps already encoded, non-reordered H.264 in MPEG-TS for the existing
// browser player. No native encoder timestamps, decoding, or re-encoding.
type Muxer struct {
	continuity       [3]byte
	last             time.Time
	pts              uint64
	ClockCorrections uint64
}

func (m *Muxer) Write(unit *AccessUnit) []byte {
	if m.last.IsZero() {
		m.pts = 90000
	} else {
		delta := unit.Captured.Sub(m.last)
		if delta <= 0 || delta > 250*time.Millisecond {
			// The observed feed is ~25 fps. Stitch a new epoch onto the old
			// timeline after a recording transition or capture clock step.
			delta = 40 * time.Millisecond
			m.ClockCorrections++
		}
		m.pts += uint64(max(time.Millisecond, delta)) * 90000 / uint64(time.Second)
	}
	m.last = unit.Captured
	var out []byte
	if unit.Keyframe {
		out = m.table(out, 0, 0, []byte{0x00, 0xb0, 0x0d, 0, 1, 0xc1, 0, 0, 0, 1, 0xf0, 0})
		out = m.table(out, 0x1000, 1, []byte{0x02, 0xb0, 0x12, 0, 1, 0xc1, 0, 0, 0xe1, 0, 0xf0, 0, 0x1b, 0xe1, 0, 0xf0, 0})
	}
	pts := m.pts & ((1 << 33) - 1)
	pes := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5,
		0x21 | byte(pts>>29)&14, byte(pts >> 22), byte(pts>>14)&254 | 1, byte(pts >> 7), byte(pts<<1) | 1,
		0, 0, 0, 1, 9, 0xf0} // One leading AUD, never a trailing empty AU.
	pes = append(pes, unit.AnnexB...)
	first := true
	for len(pes) > 0 {
		packet := make([]byte, 188)
		for i := range packet {
			packet[i] = 0xff
		}
		packet[0], packet[1], packet[2], packet[3] = 0x47, 1, 0, 0x10|m.continuity[2]
		m.continuity[2] = (m.continuity[2] + 1) & 15
		capacity := 184
		if first {
			packet[1] |= 0x40
			capacity = 176 // Adaptation flags and PCR at every frame.
		}
		size := min(capacity, len(pes))
		offset := 4
		if size < 184 {
			packet[3] |= 0x20
			packet[4] = byte(183 - size)
			if packet[4] > 0 {
				packet[5] = 0
			}
			offset = 188 - size
		}
		if first {
			packet[5] = 0x10
			if unit.Keyframe {
				packet[5] |= 0x40
			}
			copy(packet[6:12], []byte{byte(pts >> 25), byte(pts >> 17), byte(pts >> 9), byte(pts >> 1), byte(pts<<7)&0x80 | 0x7e, 0})
		}
		copy(packet[offset:], pes[:size])
		out = append(out, packet...)
		pes, first = pes[size:], false
	}
	return out
}

func (m *Muxer) table(out []byte, pid uint16, counter int, section []byte) []byte {
	// MPEG-2 PSI uses a non-reflected CRC32, initial value all ones, no xorout.
	crc := uint32(0xffffffff)
	for _, b := range section {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	packet := make([]byte, 188)
	for i := range packet {
		packet[i] = 0xff
	}
	copy(packet, []byte{0x47, 0x40 | byte(pid>>8), byte(pid), 0x10 | m.continuity[counter], 0})
	m.continuity[counter] = (m.continuity[counter] + 1) & 15
	n := copy(packet[5:], section)
	copy(packet[5+n:], []byte{byte(crc >> 24), byte(crc >> 16), byte(crc >> 8), byte(crc)})
	return append(out, packet...)
}
