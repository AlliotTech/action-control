package mimopreview

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"testing/iotest"
	"time"
)

var testKey = [][]byte{{0x67, 0x64, 0, 0x20, 1}, {0x68, 0xee}, {0x65, 0xb8}}
var testP = [][]byte{{0x41, 0xc0}}

func testMedia(nals [][]byte, clock uint32) []byte {
	b := []byte{0, 0, 1, 255, 0, 0, 0, 0, 0x90, 0x11, 0, 0, 0, 0, 0, 0}
	for _, nal := range nals {
		b = append(b, 0, 0, 0, 1)
		b = append(b, nal...)
	}
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-16))
	binary.LittleEndian.PutUint32(b[12:16], clock)
	b[10] = xor(b[:10])
	return b
}

func testFragment(fid byte, count, index int, payload []byte) []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint16(b[:2], uint16(0x8000|20+len(payload)))
	binary.LittleEndian.PutUint16(b[2:4], 0x1234)
	b[6], b[7] = 2, 0
	b[7] = xor(b[:7])
	binary.LittleEndian.PutUint32(b[16:20], uint32(fid)|uint32(count)<<8|uint32(index)<<15)
	return append(b, payload...)
}

func testEthernet(data []byte) []byte {
	b := make([]byte, 42)
	b[12], b[13], b[14], b[22], b[23] = 8, 0, 0x45, 64, 17
	binary.BigEndian.PutUint16(b[16:18], uint16(28+len(data)))
	copy(b[26:30], []byte{192, 168, 2, 1})
	copy(b[30:34], []byte{192, 168, 2, 2})
	binary.BigEndian.PutUint16(b[34:36], 9004)
	binary.BigEndian.PutUint16(b[36:38], 45000)
	binary.BigEndian.PutUint16(b[38:40], uint16(8+len(data)))
	return append(b, data...)
}

func testPCAP(packets ...[]byte) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(b[4:6], 2)
	binary.LittleEndian.PutUint16(b[6:8], 4)
	binary.LittleEndian.PutUint32(b[16:20], 65535)
	binary.LittleEndian.PutUint32(b[20:24], 1)
	for i, packet := range packets {
		raw := testEthernet(packet)
		record := make([]byte, 16)
		binary.LittleEndian.PutUint32(record[:4], 100)
		binary.LittleEndian.PutUint32(record[4:8], uint32(i*40000))
		binary.LittleEndian.PutUint32(record[8:12], uint32(len(raw)))
		binary.LittleEndian.PutUint32(record[12:16], uint32(len(raw)))
		b = append(b, record...)
		b = append(b, raw...)
	}
	return b
}

func TestPCAPPipeReadsAndTruncation(t *testing.T) {
	packet := testFragment(1, 1, 0, testMedia(testKey, 1000))
	raw := testPCAP(packet)
	count := 0
	if err := ReadPCAP(iotest.OneByteReader(bytes.NewReader(raw)), func(at time.Time, data []byte) error {
		count++
		if at.UnixNano() != 100*int64(time.Second) || !bytes.Equal(data, testEthernet(packet)) {
			t.Fatal("pcap changed timestamp or packet")
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatalf("pipe: %v, %d packets", err, count)
	}
	for _, broken := range [][]byte{raw[:20], raw[:30], raw[:len(raw)-1], append(bytes.Clone(raw), '\n')} {
		if err := ReadPCAP(bytes.NewReader(broken), func(time.Time, []byte) error { return nil }); err == nil {
			t.Fatalf("accepted truncated input of %d bytes", len(broken))
		}
	}
	for _, position := range []int{20, 32, 36} {
		broken := bytes.Clone(raw)
		binary.LittleEndian.PutUint32(broken[position:position+4], 0xffffffff)
		if err := ReadPCAP(bytes.NewReader(broken), func(time.Time, []byte) error { t.Fatal("consumed invalid record"); return nil }); err == nil {
			t.Fatalf("accepted invalid field at %d", position)
		}
	}
}

func TestPCAPByteOrderAndNanoseconds(t *testing.T) {
	raw := testPCAP(testFragment(1, 1, 0, testMedia(testKey, 0)))
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, nano := range []bool{false, true} {
			b := bytes.Clone(raw)
			magic, fraction := uint32(0xa1b2c3d4), uint32(123456)
			if nano {
				magic, fraction = 0xa1b23c4d, 123456789
			}
			order.PutUint32(b[:4], magic)
			order.PutUint16(b[4:6], 2)
			order.PutUint16(b[6:8], 4)
			order.PutUint32(b[16:20], 65535)
			order.PutUint32(b[20:24], 1)
			order.PutUint32(b[24:28], 100)
			order.PutUint32(b[28:32], fraction)
			order.PutUint32(b[32:36], uint32(len(b)-40))
			order.PutUint32(b[36:40], uint32(len(b)-40))
			err := ReadPCAP(bytes.NewReader(b), func(at time.Time, _ []byte) error {
				want := int(fraction)
				if !nano {
					want *= 1000
				}
				if at.Nanosecond() != want {
					t.Fatalf("fraction %d != %d", at.Nanosecond(), want)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestFragmentsReorderingDuplicatesAndConflicts(t *testing.T) {
	body := testMedia(testKey, 1000)
	parts := [][]byte{body[:12], body[12:24], body[24:]}
	d := &Decoder{}
	for i, index := range []int{2, 0, 0, 1, 1} {
		unit, err := d.Feed(time.Unix(100, 0), testEthernet(testFragment(254, 3, index, parts[index])))
		if err != nil {
			t.Fatal(err)
		}
		if (unit != nil) != (i == 3) {
			t.Fatalf("unexpected output at fragment %d", i)
		}
	}
	if d.Stats.DuplicatePackets != 2 || d.Stats.OutputFrames != 1 {
		t.Fatal(d.Stats)
	}
	if _, err := d.Feed(time.Unix(100, 0), testEthernet(testFragment(254, 3, 0, []byte("conflict")))); err == nil {
		t.Fatal("accepted conflicting retransmission")
	}
	d = &Decoder{}
	_, _ = d.Feed(time.Unix(100, 0), testEthernet(testFragment(1, 2, 0, body[:12])))
	if _, err := d.Feed(time.Unix(100, 0), testEthernet(testFragment(1, 2, 0, []byte("conflict")))); err == nil {
		t.Fatal("conflict accepted")
	}
	if unit, _ := d.Feed(time.Unix(100, 0), testEthernet(testFragment(1, 2, 1, body[12:]))); unit != nil {
		t.Fatal("poisoned frame completed")
	}
}

func TestCounterWrapLossAndKeyframeRecovery(t *testing.T) {
	d := &Decoder{}
	at := time.Unix(100, 0)
	feed := func(fid byte, nals [][]byte, clock uint32, want bool) {
		t.Helper()
		at = at.Add(40 * time.Millisecond)
		unit, err := d.Feed(at, testEthernet(testFragment(fid, 1, 0, testMedia(nals, clock))))
		if err != nil || (unit != nil) != want {
			t.Fatalf("frame %d: got=%v err=%v", fid, unit != nil, err)
		}
	}
	feed(253, testP, 900, false)
	feed(254, testKey, 1000, true)
	feed(255, testP, 1040, true)
	feed(0, testP, 1080, true)
	if d.last != 256 {
		t.Fatalf("counter failed to wrap: %d", d.last)
	}
	feed(2, testP, 1160, false) // Missing reference frame 1.
	feed(1, testP, 1120, false) // Late frame must never play backwards.
	feed(3, testKey, 1200, true)
	feed(4, testP, 0, false) // Clock reset without a random access point.
	feed(5, testKey, 40, true)
	if d.Stats.Discontinuities != 1 || d.Stats.NativeClockResets != 1 {
		t.Fatal(d.Stats)
	}
	for fid := 6; fid < 220; fid++ {
		_, err := d.Feed(at, testEthernet(testFragment(byte(fid), 2, 0, []byte{1})))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(d.pending) > 64 || len(d.completed) > 64 || d.Stats.IncompleteFrames == 0 {
		t.Fatal("unbounded frame window")
	}
}

func TestUnknownFormatsAndSessionChangesAreRejected(t *testing.T) {
	good := testEthernet(testFragment(1, 1, 0, testMedia(testKey, 0)))
	cases := map[string]func([]byte){
		"IP length":     func(b []byte) { b[16], b[17] = 255, 255 },
		"IP fragments":  func(b []byte) { b[20] = 0x20 },
		"UDP length":    func(b []byte) { b[38], b[39] = 255, 255 },
		"SW checksum":   func(b []byte) { b[49] ^= 1 },
		"FEC":           func(b []byte) { b[59] |= 0x40 },
		"unknown flags": func(b []byte) { b[60] |= 0x20 },
		"MM checksum":   func(b []byte) { b[72] ^= 1 },
		"MM length":     func(b []byte) { b[66] = 255 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := bytes.Clone(good)
			mutate(b)
			if _, err := (&Decoder{}).Feed(time.Unix(100, 0), b); err == nil {
				t.Fatal("accepted unverified data")
			}
		})
	}
	for _, nals := range [][][]byte{{{0x41, 0xa0}}, {{0x24, 1}}, {{0x41}}, {{0x09, 0xf0}}} {
		if _, _, err := multimedia(testMedia(nals, 0)); err == nil {
			t.Fatal("accepted unsupported NAL/slice")
		}
	}
	d := &Decoder{}
	_, _ = d.Feed(time.Unix(100, 0), good)
	changed := bytes.Clone(good)
	changed[44] ^= 1
	changed[49] = xor(changed[42:49])
	if _, err := d.Feed(time.Unix(100, 1), changed); err == nil {
		t.Fatal("merged separate sessions")
	}
	if _, err := d.Feed(time.Unix(103, 0), good); err == nil {
		t.Fatal("guessed counter after long silence")
	}
}

func TestMultimediaFiltersNativeMetadata(t *testing.T) {
	nals := append([][]byte{{9, 0xf0}, {6, 1, 2}}, testKey...)
	nals = append(nals, []byte{9, 0xf0})
	unit, clock, err := multimedia(testMedia(nals, 1234))
	if err != nil || clock != 1234 || !unit.Keyframe {
		t.Fatal(err)
	}
	var want []byte
	for _, nal := range testKey {
		want = append(want, 0, 0, 0, 1)
		want = append(want, nal...)
	}
	if !bytes.Equal(unit.AnnexB, want) {
		t.Fatal("export changed video or retained native metadata")
	}
}

func FuzzDecoderRejectsMalformedPackets(f *testing.F) {
	f.Add(testEthernet(testFragment(1, 1, 0, testMedia(testKey, 0))))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, packet []byte) {
		d := &Decoder{}
		_, _ = d.Feed(time.Unix(100, 0), packet)
	})
}

func TestPCAPConsumerCanCancel(t *testing.T) {
	raw := testPCAP(testFragment(1, 1, 0, testMedia(testKey, 0)))
	if err := ReadPCAP(bytes.NewReader(raw), func(time.Time, []byte) error { return io.ErrClosedPipe }); err != io.ErrClosedPipe {
		t.Fatalf("lost consumer error: %v", err)
	}
}
