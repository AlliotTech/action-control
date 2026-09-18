#!/usr/bin/env python3
"""Offline analysis of the observed AC204 / Mimo UDP transport.

No device access, packet transmission, subscription, or camera control. Input is
an Ethernet/IPv4 pcap captured separately. See docs/MIMO_PREVIEW_PROTOCOL.md.
Only the observed, non-FEC H.264 variant is exported. Export omits SEI and AUD;
the report records this, and exports resume at SPS/PPS/IDR after missing frames.
"""

import argparse
from collections import Counter, defaultdict
from dataclasses import dataclass, field
from functools import reduce
import hashlib
import json
import operator
from pathlib import Path
import re
import struct


class InvalidPacket(ValueError):
    pass


def xor(data):
    return reduce(operator.xor, data, 0)


def crc(data, initial, polynomial):
    value = initial
    for byte in data:
        value ^= byte
        for _ in range(8):
            value = (value >> 1) ^ (polynomial if value & 1 else 0)
    return value


def pcap_packets(stream):
    header = stream.read(24)
    formats = {
        b"\xd4\xc3\xb2\xa1": ("<", 1_000_000),
        b"\xa1\xb2\xc3\xd4": (">", 1_000_000),
        b"\x4d\x3c\xb2\xa1": ("<", 1_000_000_000),
        b"\xa1\xb2\x3c\x4d": (">", 1_000_000_000),
    }
    if len(header) != 24 or header[:4] not in formats:
        raise InvalidPacket("Expected a classic pcap header, not pcapng.")
    endian, resolution = formats[header[:4]]
    major, minor, _, _, snaplen, linktype = struct.unpack(endian + "HHIIII", header[4:])
    if (major, minor) != (2, 4) or linktype != 1 or not 14 <= snaplen <= 1_048_576:
        raise InvalidPacket("Only Ethernet pcap 2.4 with a bounded snaplen is supported.")
    while True:
        record = stream.read(16)
        if not record:
            return
        if len(record) != 16:
            raise InvalidPacket("Truncated pcap record header.")
        seconds, fraction, size, original = struct.unpack(endian + "IIII", record)
        if size > snaplen or original < size or fraction >= resolution:
            raise InvalidPacket("Invalid pcap record lengths or timestamp.")
        packet = stream.read(size)
        if len(packet) != size:
            raise InvalidPacket("Truncated pcap record data.")
        yield seconds + fraction / resolution, packet


def ipv4_udp(packet):
    if len(packet) < 14:
        raise InvalidPacket("Short Ethernet header.")
    offset, protocol = 14, int.from_bytes(packet[12:14], "big")
    for _ in range(2):
        if protocol not in (0x8100, 0x88A8):
            break
        if len(packet) < offset + 4:
            raise InvalidPacket("Short VLAN header.")
        protocol = int.from_bytes(packet[offset + 2:offset + 4], "big")
        offset += 4
    if protocol != 0x0800:
        return None
    ip = packet[offset:]
    if len(ip) < 20 or ip[0] >> 4 != 4:
        raise InvalidPacket("Short or invalid IPv4 header.")
    ihl, total = (ip[0] & 15) * 4, int.from_bytes(ip[2:4], "big")
    if ihl < 20 or total < ihl or len(ip) < total:
        raise InvalidPacket("Truncated or invalid IPv4 packet.")
    if ip[9] != 17:
        return None
    if int.from_bytes(ip[6:8], "big") & 0x3FFF:
        raise InvalidPacket("Fragmented IPv4 UDP is unsupported.")
    udp = ip[ihl:total]
    if len(udp) < 8:
        raise InvalidPacket("Short UDP header.")
    source_port, dest_port, size = struct.unpack("!HHH", udp[:6])
    if size < 8 or size != len(udp):
        raise InvalidPacket("UDP length mismatch.")
    source = ".".join(map(str, ip[12:16]))
    dest = ".".join(map(str, ip[16:20]))
    return (source, source_port, dest, dest_port), udp[8:]


@dataclass(frozen=True)
class SWPacket:
    data: bytes
    kind: int
    session: int

    @classmethod
    def parse(cls, data):
        if len(data) < 8:
            raise InvalidPacket("Short SW header.")
        word = int.from_bytes(data[:2], "little")
        if word >> 14 != 2 or word & 0x3FFF != len(data):
            raise InvalidPacket("Unsupported SW version or length mismatch.")
        if xor(data[:7]) != data[7]:
            raise InvalidPacket("SW header checksum mismatch.")
        return cls(data, data[6], int.from_bytes(data[2:4], "little"))


@dataclass
class PendingFrame:
    count: int
    time: float
    parts: dict = field(default_factory=dict)
    invalid: bool = False


class FrameReassembler:
    """One transport flow/session. Window is smaller than half the 8-bit cycle."""

    def __init__(self):
        self.highest = None
        self.pending = {}
        self.completed = {}
        self.stats = Counter()

    def feed(self, packet, timestamp):
        if packet.kind != 2 or len(packet.data) < 21:
            raise InvalidPacket("Not a video data fragment.")
        word = int.from_bytes(packet.data[16:20], "little")
        fid, count, index = word & 255, (word >> 8) & 63, (word >> 15) & 63
        if word & (1 << 14) or word >> 21:
            raise InvalidPacket("FEC or unverified video fragment flags.")
        if count == 0 or index >= count or len(packet.data) > 1472:
            raise InvalidPacket("Invalid video fragment count, index, or size.")
        absolute = fid
        if self.highest is not None:
            absolute = self.highest + ((fid - self.highest + 128) % 256 - 128)
        if self.highest is None or absolute > self.highest:
            self.highest = absolute
        if absolute < self.highest - 63:
            self.stats["stale_fragments"] += 1
            return None
        for key in list(self.pending):
            if key < self.highest - 63:
                del self.pending[key]
                self.stats["incomplete_frames"] += 1
        for key in list(self.completed):
            if key < self.highest - 63:
                del self.completed[key]
        payload = packet.data[20:]
        if absolute in self.completed:
            digest = hashlib.sha256(payload).digest()
            old_count, old_parts = self.completed[absolute]
            if old_count != count or old_parts.get(index) != digest:
                raise InvalidPacket("Conflicting fragment after completed frame.")
            self.stats["duplicate_fragments"] += 1
            return None
        frame = self.pending.setdefault(absolute, PendingFrame(count, timestamp))
        if frame.count != count or (index in frame.parts and frame.parts[index] != payload):
            frame.invalid = True
            raise InvalidPacket("Conflicting video fragments.")
        if frame.invalid:
            return None
        if index in frame.parts:
            self.stats["duplicate_fragments"] += 1
            return None
        frame.parts[index] = payload
        if len(frame.parts) != frame.count:
            return None
        body = b"".join(frame.parts[i] for i in range(frame.count))
        self.completed[absolute] = (count, {i: hashlib.sha256(p).digest() for i, p in frame.parts.items()})
        del self.pending[absolute]
        self.stats["complete_frames"] += 1
        return absolute, frame.time, body


def multimedia_frame(body):
    if len(body) < 12 or body[:4] != b"\x00\x00\x01\xff":
        raise InvalidPacket("Missing native multimedia header.")
    if xor(body[:11]) != 0 or body[11] != 0:
        raise InvalidPacket("Native multimedia header checksum/reserved byte mismatch.")
    if body[8] != 0x90 or body[9] != 0x11:
        raise InvalidPacket("Only the observed timestamped H.264 multimedia variant is supported.")
    size = int.from_bytes(body[4:8], "little")
    if len(body) != 16 + size:
        raise InvalidPacket("Native multimedia payload length mismatch.")
    timestamp = int.from_bytes(body[12:16], "little")
    payload = body[16:]
    matches = list(re.finditer(b"\x00\x00\x00?\x01", payload))
    if not matches or matches[0].start() != 0:
        raise InvalidPacket("Missing Annex-B access unit.")
    units = []
    for index, match in enumerate(matches):
        end = matches[index + 1].start() if index + 1 < len(matches) else len(payload)
        unit = payload[match.end():end]
        if not unit or unit[0] & 0x80 or unit[0] & 31 not in (1, 5, 6, 7, 8, 9):
            raise InvalidPacket("Unverified H.264 NAL unit.")
        units.append(unit)
    if not any(unit[0] & 31 in (1, 5) for unit in units):
        raise InvalidPacket("Access unit has no video slice.")
    return timestamp, units


def duml_messages(data):
    offset = 0
    while offset < len(data):
        remaining = data[offset:]
        if len(remaining) < 13 or remaining[0] != 0x55:
            raise InvalidPacket("Missing DUML header.")
        word = int.from_bytes(remaining[1:3], "little")
        size = word & 0x3FF
        if word >> 10 != 1 or size < 13 or size > len(remaining):
            raise InvalidPacket("Unverified DUML version or truncated message.")
        message = remaining[:size]
        if crc(message[:3], 0x77, 0x8C) != message[3]:
            raise InvalidPacket("DUML header CRC8 mismatch.")
        if crc(message[:-2], 0x3692, 0x8408) != int.from_bytes(message[-2:], "little"):
            raise InvalidPacket("DUML CRC16 mismatch.")
        yield message
        offset += size


def control_payload(packet):
    data = packet.data
    if packet.kind in (3, 5):
        if len(data) < 20:
            raise InvalidPacket("Short control fragment.")
        word = int.from_bytes(data[16:20], "little")
        if (word >> 8) & 63 != 1 or (word >> 15) & 63 != 0:
            raise InvalidPacket("Fragmented control messages are not implemented.")
        return data[20:]
    if packet.kind in (1, 4):
        if len(data) < 34 or int.from_bytes(data[32:34], "little") != len(data) - 34:
            raise InvalidPacket("Unverified acknowledgement payload layout.")
        return data[34:]
    return b""


def analyze(path, video=None):
    stats, errors, nal_types, command_counts = Counter(), Counter(), Counter(), Counter()
    flows, seconds, assemblers, timeline = {}, defaultdict(Counter), {}, []
    timing, last_frame, ready = {}, {}, set()
    commands, clock_events = [], []
    selected_video = None
    with Path(path).open("rb") as stream:
        for timestamp, ethernet in pcap_packets(stream):
            stats["pcap_packets"] += 1
            try:
                parsed = ipv4_udp(ethernet)
                if parsed is None:
                    stats["non_ipv4_udp"] += 1
                    continue
                flow, data = parsed
                if flow[1] != 9004 and flow[3] != 9004:
                    continue
                packet = SWPacket.parse(data)
                stats["valid_sw_packets"] += 1
                flow_name = f"{flow[0]}:{flow[1]}>{flow[2]}:{flow[3]}"
                entry = flows.setdefault(flow_name, Counter())
                entry["packets"] += 1
                entry["udp_bytes"] += len(data)
                entry[f"kind_{packet.kind}"] += 1
                if flow[1] == 9004:
                    seconds[int(timestamp)]["camera_udp_bytes"] += len(data)
                    seconds[int(timestamp)][f"kind_{packet.kind}"] += 1
                if packet.kind != 2:
                    for message in duml_messages(control_payload(packet)):
                        key = f"{message[9]:02x}:{message[10]:02x}"
                        reply = bool(message[8] & 0x80)
                        command_counts[f"{flow_name} {key} {'reply' if reply else 'request_or_push'}"] += 1
                        body = message[11:-2]
                        item = {"time": timestamp, "flow": flow_name, "command": key,
                                "sequence": int.from_bytes(message[6:8], "little"), "reply": reply,
                                "sender": message[4], "receiver": message[5], "flags": message[8],
                                "payload_bytes": len(body), "payload_sha256": hashlib.sha256(body).hexdigest()}
                        # Camera operation candidates only; other payloads stay private.
                        if key in ("02:01", "02:02", "02:03", "02:e0") and len(body) <= 32:
                            item["payload_hex"] = body.hex()
                        commands.append(item)
                        stats["crc_valid_duml_messages"] += 1
                    continue
                if flow[1] != 9004:
                    raise InvalidPacket("Video packet not originating at camera port 9004.")
                key = flow + (packet.session,)
                assembler = assemblers.setdefault(key, FrameReassembler())
                result = assembler.feed(packet, timestamp)
                if result is None:
                    continue
                fid, captured, body = result
                if key in last_frame and fid <= last_frame[key]:
                    stats["late_completed_frames"] += 1
                    continue
                native_time, units = multimedia_frame(body)
                types = [unit[0] & 31 for unit in units]
                nal_types.update(types)
                stats["validated_access_units"] += 1
                seconds[int(captured)]["video_frames"] += 1
                if key in timing:
                    previous_native, previous_capture = timing[key]
                    raw_delta = native_time - previous_native
                    delta = raw_delta & 0xFFFFFFFF
                    seconds[int(captured)]["native_interval_samples"] += 1
                    timeline.append(delta)
                    capture_gap = captured - previous_capture
                    if raw_delta <= 0 or raw_delta > 120 or capture_gap > 0.12:
                        stats["clock_step_events"] += 1
                        if len(clock_events) < 256:
                            clock_events.append({"flow": flow_name, "frame": fid,
                                                 "capture_time": captured, "capture_gap_seconds": capture_gap,
                                                 "previous_native_timestamp": previous_native,
                                                 "native_timestamp": native_time, "raw_delta_ticks": raw_delta,
                                                 "has_sps_pps_idr": all(t in types for t in (7, 8, 5))})
                timing[key] = native_time, captured
                if key in last_frame and fid != last_frame[key] + 1:
                    ready.discard(key)
                    stats["frame_discontinuities"] += 1
                last_frame[key] = fid
                if all(kind in types for kind in (7, 8, 5)):
                    ready.add(key)
                if video is not None and key in ready:
                    if selected_video is None:
                        selected_video = key
                    if key != selected_video:
                        raise InvalidPacket("Multiple preview sessions cannot share an H.264 export.")
                    video.write(b"".join(b"\x00\x00\x00\x01" + unit for unit in units if unit[0] & 31 in (1, 5, 7, 8)))
                    stats["exported_video_frames"] += 1
            except InvalidPacket as exc:
                errors[str(exc)] += 1
                # Any unverified packet may hide a missing reference frame.
                ready.clear()
    reassembly = Counter()
    for assembler in assemblers.values():
        reassembly.update(assembler.stats)
        reassembly["pending_frames_at_eof"] += len(assembler.pending)
    return {"schema": 1, "input": str(path), "stats": dict(stats), "errors": dict(errors),
            "flows": flows, "reassembly": dict(reassembly), "h264_nal_types": dict(nal_types),
            "native_timestamp_delta_counts": dict(Counter(timeline)),
            "clock_events": clock_events,
            "per_second": dict(seconds), "command_counts": dict(command_counts), "commands": commands,
            "export": {"enabled": video is not None, "omitted_nal_types": [6, 9],
                       "starts_at_sps_pps_idr": True, "contains_audio": False, "contains_timestamps": False},
            "limitations": ["Only the observed non-FEC H.264 transport variant is implemented.",
                            "No subscription, authentication, retransmission, or network commands are sent.",
                            "Capture omissions are possible even without kernel drops; incomplete frames are reported.",
                            "Transport frame counters are 8-bit; large gaps/session resets require a new capture.",
                            "H.264 export omits SEI/AUD, including native metadata; it is a video-only research artifact."]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("pcap", type=Path)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--h264", type=Path, help="Export video only from the first complete SPS/PPS/IDR.")
    args = parser.parse_args()
    # Refuse overwrites, including accidental reuse of the input path.
    paths = [args.pcap.resolve(), args.report.resolve()]
    if args.h264:
        paths.append(args.h264.resolve())
    if len(set(paths)) != len(paths) or any(path.exists() for path in paths[1:]):
        parser.error("Output paths must be distinct and must not already exist.")
    created = []
    try:
        with args.report.open("x") as report:
            created.append(args.report)
            if args.h264:
                with args.h264.open("xb") as video:
                    created.append(args.h264)
                    result = analyze(args.pcap, video)
            else:
                result = analyze(args.pcap)
            json.dump(result, report, ensure_ascii=False, indent=2)
            report.write("\n")
    except (InvalidPacket, OSError) as exc:
        for path in created:
            path.unlink(missing_ok=True)
        parser.exit(2, str(exc) + "\n")
    print(json.dumps({key: result[key] for key in ("stats", "errors", "reassembly", "native_timestamp_delta_counts")}, ensure_ascii=False))
    if result["errors"] or (args.h264 and not result["stats"].get("exported_video_frames")):
        parser.exit(2, "Analysis finished with unverified packets or no exportable video; inspect the report.\n")


if __name__ == "__main__":
    main()
