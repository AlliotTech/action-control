package mimopreview

import (
	"bytes"
	"testing"
	"time"
)

func readPTS(b []byte) uint64 {
	return uint64(b[0]>>1&7)<<30 | uint64(b[1])<<22 | uint64(b[2]>>1)<<15 | uint64(b[3])<<7 | uint64(b[4]>>1)
}

func TestMPEGTSFrameBoundariesContinuityAndClock(t *testing.T) {
	mux := &Muxer{}
	continuity := map[uint16]byte{}
	var previous uint64
	at := time.Unix(100, 0)
	for i, size := range []int{1, 155, 156, 157, 168, 169, 170, 352, 2048, 70000} {
		at = at.Add(40 * time.Millisecond)
		if i == 3 {
			at = at.Add(-time.Second)
		}
		if i == 6 {
			at = at.Add(time.Second)
		}
		unit := &AccessUnit{AnnexB: bytes.Repeat([]byte{0x23}, size), Captured: at, Keyframe: i%2 == 0}
		raw := mux.Write(unit)
		if len(raw)%188 != 0 {
			t.Fatal("not whole TS packets")
		}
		var pes []byte
		tables := 0
		for offset := 0; offset < len(raw); offset += 188 {
			packet := raw[offset : offset+188]
			pid := uint16(packet[1]&31)<<8 | uint16(packet[2])
			if packet[0] != 0x47 || packet[3]&15 != continuity[pid] {
				t.Fatal("sync/continuity mismatch")
			}
			continuity[pid] = (continuity[pid] + 1) & 15
			start := 4
			if packet[3]&0x20 != 0 {
				start += 1 + int(packet[4])
			}
			if start > 188 {
				t.Fatal("invalid adaptation length")
			}
			if pid != 0x100 {
				tables++
				if (pid != 0 && pid != 0x1000) || packet[4] != 0 {
					t.Fatal("invalid PSI packet")
				}
				continue
			}
			if (packet[1]&0x40 != 0) != (len(pes) == 0) {
				t.Fatal("invalid PES boundary")
			}
			if len(pes) == 0 {
				if packet[5]&0x10 == 0 || (packet[5]&0x40 != 0) != unit.Keyframe {
					t.Fatal("missing PCR/random access flag")
				}
			}
			pes = append(pes, packet[start:]...)
		}
		if (tables == 2) != unit.Keyframe || !bytes.Equal(pes[:9], []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5}) {
			t.Fatal("missing PSI/PES headers")
		}
		pts := readPTS(pes[9:14])
		if pts <= previous || (i > 0 && pts-previous != 3600) {
			t.Fatalf("unstable playback clock: %d -> %d", previous, pts)
		}
		previous = pts
		if !bytes.Equal(pes[14:20], []byte{0, 0, 0, 1, 9, 0xf0}) || !bytes.Equal(pes[20:], unit.AnnexB) {
			t.Fatal("video altered or TS stuffing entered video")
		}
	}
	if mux.ClockCorrections != 2 {
		t.Fatalf("clock corrections: %d", mux.ClockCorrections)
	}
}
