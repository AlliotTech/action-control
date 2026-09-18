#!/usr/bin/env python3
"""Synthetic protocol failure/reassembly tests; no device or firmware required."""

import io
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

from mimo_pcap import (
    FrameReassembler, InvalidPacket, SWPacket, analyze, control_payload,
    duml_messages, ipv4_udp, multimedia_frame, pcap_packets, xor,
)


def sw(kind, body):
    header = bytearray(struct.pack("<HHHBB", 0x8000 | (len(body) + 8), 0x1234, 8, kind, 0))
    header[7] = xor(header[:7])
    return bytes(header) + body


def fragment(fid, count, index, body, extra=0):
    return SWPacket.parse(sw(2, bytes(8) + struct.pack("<I", fid | count << 8 | index << 15 | extra) + body))


def mm(units, timestamp=1000):
    payload = b"".join(b"\x00\x00\x00\x01" + unit for unit in units)
    header = bytearray(b"\x00\x00\x01\xff" + struct.pack("<I", len(payload)) + b"\x90\x11\0\0" + struct.pack("<I", timestamp))
    header[10] = xor(header[:10])
    return bytes(header) + payload


IDR = [b"\x67\x64\x00\x20\x01", b"\x68\xee", b"\x65\xab"]
PFRAME = [b"\x41\xab"]


def ethernet(data):
    udp = struct.pack("!HHHH", 9004, 45000, len(data) + 8, 0) + data
    ip = struct.pack("!BBHHHBBH4s4s", 0x45, 0, 20 + len(udp), 1, 0, 64, 17, 0,
                     bytes([192, 168, 2, 1]), bytes([192, 168, 2, 2]))
    return bytes(12) + b"\x08\x00" + ip + udp


def pcap(packets):
    data = struct.pack("<IHHIIII", 0xA1B2C3D4, 2, 4, 0, 0, 65535, 1)
    for index, packet in enumerate(packets):
        raw = ethernet(packet.data)
        data += struct.pack("<IIII", 100, index * 1000, len(raw), len(raw)) + raw
    return data


class PcapTests(unittest.TestCase):
    def test_pcap_truncation_and_unexpected_trailer_are_rejected(self):
        raw = pcap([fragment(1, 1, 0, mm(IDR))])
        self.assertEqual(len(list(pcap_packets(io.BytesIO(raw)))), 1)
        for bad in (raw[:-1], raw + b"\n", raw[:30], b"not pcap"):
            with self.subTest(size=len(bad)), self.assertRaises(InvalidPacket):
                list(pcap_packets(io.BytesIO(bad)))

    def test_truncated_and_ip_fragmented_udp_is_rejected(self):
        raw = ethernet(fragment(1, 1, 0, mm(IDR)).data)
        self.assertEqual(ipv4_udp(raw)[0][1], 9004)
        for bad in (raw[:-1], raw[:20], raw[:20] + b"\x20\x00" + raw[22:]):
            with self.assertRaises(InvalidPacket):
                ipv4_udp(bad)

    def test_transport_length_version_and_checksum_are_required(self):
        valid = fragment(1, 1, 0, mm(IDR)).data
        for position in (0, 1, 4, 7):
            bad = bytearray(valid)
            bad[position] ^= 1
            with self.assertRaises(InvalidPacket):
                SWPacket.parse(bytes(bad))

    def test_out_of_order_fragments_and_retransmission(self):
        body = mm(IDR)
        fragments = [fragment(9, 3, i, body[i * 16:(i + 1) * 16]) for i in range(3)]
        # Synthetic frame length is below 48 bytes; all fragments remain nonempty.
        reassembler = FrameReassembler()
        self.assertIsNone(reassembler.feed(fragments[2], 0))
        self.assertIsNone(reassembler.feed(fragments[0], 0))
        self.assertIsNone(reassembler.feed(fragments[0], 0))
        result = reassembler.feed(fragments[1], 0)
        self.assertEqual(result[2], body)
        self.assertIsNone(reassembler.feed(fragments[1], 0))
        self.assertEqual(reassembler.stats["duplicate_fragments"], 2)

    def test_conflicting_fragments_do_not_complete_a_frame(self):
        reassembler = FrameReassembler()
        self.assertIsNone(reassembler.feed(fragment(1, 2, 0, b"first"), 0))
        with self.assertRaises(InvalidPacket):
            reassembler.feed(fragment(1, 2, 0, b"different"), 0)
        self.assertIsNone(reassembler.feed(fragment(1, 2, 1, b"tail"), 0))

    def test_frame_counter_wrap_and_missing_fragment_expiration(self):
        reassembler = FrameReassembler()
        absolute = [reassembler.feed(fragment(fid, 1, 0, b"ok"), 0)[0] for fid in (254, 255, 0, 1)]
        self.assertEqual(absolute, [254, 255, 256, 257])
        reassembler.feed(fragment(2, 2, 0, b"missing tail"), 0)
        for fid in range(3, 70):
            reassembler.feed(fragment(fid, 1, 0, b"ok"), 0)
        self.assertEqual(reassembler.stats["incomplete_frames"], 1)
        self.assertLessEqual(len(reassembler.completed), 64)

    def test_fec_and_invalid_indices_are_not_guessed(self):
        for packet in (fragment(1, 1, 0, b"x", 1 << 14), fragment(1, 2, 2, b"x"),
                       fragment(1, 0, 0, b"x"), fragment(1, 1, 0, b"x", 1 << 21)):
            with self.assertRaises(InvalidPacket):
                FrameReassembler().feed(packet, 0)

    def test_multimedia_checksum_length_and_codec_are_verified(self):
        body = mm(IDR, 123456)
        self.assertEqual(multimedia_frame(body), (123456, IDR))
        for bad in (body[:-1], body[:10] + bytes([body[10] ^ 1]) + body[11:], mm([b"\x64x"])):
            with self.assertRaises(InvalidPacket):
                multimedia_frame(bad)
        hevc = bytearray(body)
        hevc[9] = 0x24
        hevc[10] = xor(hevc[:10])
        with self.assertRaises(InvalidPacket):
            multimedia_frame(hevc)

    def test_duml_checks_both_crcs_against_observed_query(self):
        # Fixed request from the observed stream: no serials, keys, or media.
        message = bytes.fromhex("5511049202015a9340028e00010f000eb3")
        self.assertEqual(list(duml_messages(message + message)), [message, message])
        for position in (3, 11, len(message) - 1):
            bad = bytearray(message)
            bad[position] ^= 1
            with self.assertRaises(InvalidPacket):
                list(duml_messages(bad))

    def test_control_inside_ack_and_incomplete_control_rejection(self):
        message = bytes.fromhex("5511049202015a9340028e00010f000eb3")
        packet = SWPacket.parse(sw(1, bytes(24) + struct.pack("<H", len(message)) + message))
        self.assertEqual(control_payload(packet), message)
        with self.assertRaises(InvalidPacket):
            control_payload(SWPacket.parse(sw(1, bytes(26) + message)))

    def test_export_omits_metadata_and_waits_for_new_idr_after_gap(self):
        metadata = [b"\x06\x00\x19" + b"\x22" * 26, b"\x09\x10"]
        packets = [fragment(1, 1, 0, mm(IDR + metadata)),
                   fragment(3, 1, 0, mm(PFRAME, 1080)),
                   fragment(4, 1, 0, mm(IDR, 1120))]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.pcap"
            path.write_bytes(pcap(packets))
            output = io.BytesIO()
            report = analyze(path, output)
        self.assertEqual(report["errors"], {})
        self.assertEqual(report["stats"]["exported_video_frames"], 2)
        self.assertEqual(report["stats"]["frame_discontinuities"], 1)
        expected = b"".join(b"\0\0\0\1" + unit for unit in IDR) * 2
        self.assertEqual(output.getvalue(), expected)

    def test_late_frame_is_not_exported_and_clock_reset_is_reported(self):
        packets = [fragment(2, 1, 0, mm(IDR, 1040)),
                   fragment(1, 1, 0, mm(IDR, 1000)),
                   fragment(3, 1, 0, mm(PFRAME, 1080)),
                   fragment(4, 1, 0, mm(IDR, 0))]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.pcap"
            path.write_bytes(pcap(packets))
            output = io.BytesIO()
            report = analyze(path, output)
        self.assertEqual(report["stats"]["late_completed_frames"], 1)
        self.assertEqual(report["stats"]["exported_video_frames"], 3)
        self.assertEqual(report["clock_events"][0]["raw_delta_ticks"], -1080)
        self.assertTrue(report["clock_events"][0]["has_sps_pps_idr"])

    def test_cli_failure_cleans_its_outputs_and_never_overwrites(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            source, report, video = (path / name for name in ("input.pcap", "report.json", "video.h264"))
            source.write_bytes(pcap([fragment(1, 1, 0, mm(IDR))]) + b"\n")
            command = [sys.executable, "-B", str(Path(__file__).with_name("mimo_pcap.py")),
                       str(source), "--report", str(report), "--h264", str(video)]
            self.assertEqual(subprocess.run(command, capture_output=True, timeout=5).returncode, 2)
            self.assertFalse(report.exists())
            self.assertFalse(video.exists())
            report.write_text("keep existing result")
            self.assertEqual(subprocess.run(command, capture_output=True, timeout=5).returncode, 2)
            self.assertEqual(report.read_text(), "keep existing result")
            self.assertFalse(video.exists())


if __name__ == "__main__":
    unittest.main()
