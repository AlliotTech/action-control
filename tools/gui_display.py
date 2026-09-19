#!/usr/bin/env python3
"""Read-only AC204 display evidence: EW pixels, KMS scanout, and retire times.

Does not attach a debugger, create a Wayland client, change display properties,
or restart services. Private EW offsets are gated by complete ELF fingerprints.
DRM GETFB2/PRIME handles belong to a separately opened, read-only DRM fd.
"""
import argparse
import base64
import ctypes
import errno
import hashlib
import json
import os
import pathlib
import re
import shlex
import struct
import subprocess
import time
import uuid
import zlib

FINGERPRINTS = {
    "/usr/bin/dji_gui_on_disp0": "30eb933b1c2315984e150885124d277c13c1cef3194ba6c08cb3d4d1f6656db1",
    "/usr/bin/dji_gui_on_disp1": "e965fbc8588445f5fa922fd1df38ed7051198fe28a50c33e51c94246c00fdb27",
    "/usr/lib/libewplatform_unity.so": "576288a82c6005cbe3afe17d650349da97ee3d0573716138503a35b7303862a6",
    "/usr/lib/libewrte_gfx_soft_ttfe.11.00.so": "c56a94a131ece3bdcb1bfae77559723b6dfed8425e56a0b2e686ac694bb00916",
}
PLATFORM_AGENT = 0x367A0
EW_UPDATE_CYCLE = 0x1081D4
MAX_BUFFER_BYTES = 4 * 1024 * 1024
DRM_STATE = "/sys/kernel/debug/dri/0/state"


def drm_planes(state):
    """Parse only complete plane blocks; CRTC/connector tails are separate."""
    planes = []
    for block in re.split(r"(?m)(?=^(?:plane|crtc|connector)\[)", state):
        match = re.match(r"plane\[(\d+)\]: ([^\n]+)\n", block)
        if not match:
            continue
        crtc = re.search(r"^\tcrtc=([^\n]+)$", block, re.M)
        fb = re.search(r"^\tfb=(\d+)$", block, re.M)
        if not crtc or not fb or int(fb[1]) == 0:
            continue
        fmt = re.search(r"^\t\tformat=(\S+)", block, re.M)
        planes.append({"plane_id": int(match[1]), "crtc": crtc[1],
                       "fb_id": int(fb[1]), "format": fmt[1] if fmt else None})
    return planes


def packed_pixels(data, pitch, width=400, height=712, offset=0):
    if not (0 < width <= 2048 and 0 < height <= 2048 and width * 4 <= pitch <= 8192):
        raise ValueError("Invalid pixel geometry")
    if offset < 0 or offset + (height - 1) * pitch + width * 4 > len(data):
        raise ValueError("Short pixel buffer")
    return b"".join(data[offset + y * pitch:offset + y * pitch + width * 4]
                    for y in range(height))


class Memory:
    def __init__(self, pid):
        self.pid = pid
        self.maps = []
        for line in pathlib.Path(f"/proc/{pid}/maps").read_text().splitlines():
            fields = line.split(maxsplit=5)
            start, end = (int(n, 16) for n in fields[0].split("-"))
            self.maps.append({"start": start, "end": end, "permissions": fields[1],
                              "offset": int(fields[2], 16), "dev": fields[3],
                              "inode": int(fields[4]), "path": fields[5] if len(fields) == 6 else ""})
        self.fd = os.open(f"/proc/{pid}/mem", os.O_RDONLY | os.O_CLOEXEC)

    def close(self):
        os.close(self.fd)

    def mapping(self, address, size):
        if size <= 0 or size > MAX_BUFFER_BYTES:
            raise ValueError("Invalid memory read size")
        return next(m for m in self.maps if "r" in m["permissions"]
                    and m["start"] <= address and address + size <= m["end"])

    def base(self, path):
        return next(m["start"] for m in self.maps if m["path"] == path and m["offset"] == 0)

    def read(self, address, size):
        self.mapping(address, size)
        data = os.pread(self.fd, size, address)
        if len(data) != size:
            raise ValueError("Short process memory read")
        return data

    def u32(self, address):
        return struct.unpack("<I", self.read(address, 4))[0]

    def u64(self, address):
        return struct.unpack("<Q", self.read(address, 8))[0]


def libc_handle():
    lib = ctypes.CDLL(None, use_errno=True)
    lib.syscall.restype = ctypes.c_long
    lib.mmap.restype = ctypes.c_void_p
    lib.mmap.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_int,
                        ctypes.c_int, ctypes.c_int, ctypes.c_long]
    lib.munmap.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
    lib.ioctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_void_p]
    return lib


def ioctl(lib, fd, request, argument):
    if lib.ioctl(fd, request, ctypes.byref(argument)) < 0:
        raise OSError(ctypes.get_errno(), f"readback ioctl {request:#x}")


def read_dma(lib, fd, size):
    if not 0 < size <= MAX_BUFFER_BYTES:
        raise ValueError("Unexpected DMA buffer size")
    address = lib.mmap(None, size, 1, 1, fd, 0)  # PROT_READ, MAP_SHARED
    if address == ctypes.c_void_p(-1).value:
        raise OSError(ctypes.get_errno(), "Read-only DMA mapping")
    try:
        flags = ctypes.c_uint64(1)  # DMA_BUF_SYNC_READ | START
        ioctl(lib, fd, 0x40086200, flags)
        try:
            return ctypes.string_at(address, size)
        finally:
            flags.value = 5  # DMA_BUF_SYNC_READ | END
            ioctl(lib, fd, 0x40086200, flags)
    finally:
        lib.munmap(address, size)


def read_gui_pixels(lib, memory, address, size):
    mapping = memory.mapping(address, size)
    if not mapping["path"].startswith("/dmabuf:"):
        raise ValueError("GUI buffer is not a DMA mapping")
    dma_fd = None
    for path in pathlib.Path(f"/proc/{memory.pid}/fd").iterdir():
        try:
            st = path.stat()
            device = f"{os.major(st.st_dev):02x}:{os.minor(st.st_dev):02x}"
            if st.st_ino == mapping["inode"] and device == mapping["dev"]:
                dma_fd = int(path.name)
                break
        except OSError:
            continue
    if dma_fd is None:
        raise ValueError("No fd for GUI DMA mapping")
    pid_fd = lib.syscall(434, memory.pid, 0)  # pidfd_open, Linux AArch64
    if pid_fd < 0:
        raise OSError(ctypes.get_errno(), "pidfd_open")
    try:
        duplicate = lib.syscall(438, pid_fd, dma_fd, 0)  # pidfd_getfd
        if duplicate < 0:
            raise OSError(ctypes.get_errno(), "pidfd_getfd")
    finally:
        os.close(pid_fd)
    try:
        # Revalidate the duplicated fd: the target may close/reuse it concurrently.
        if os.fstat(duplicate).st_ino != mapping["inode"]:
            raise ValueError("GUI DMA fd changed during capture")
        offset = address - mapping["start"] + mapping["offset"]
        return read_dma(lib, duplicate, size + offset)[offset:]
    finally:
        os.close(duplicate)


def gui_sample(service, capture, lib):
    pid = int(subprocess.check_output(["systemctl", "show", service, "-p", "MainPID", "--value"]))
    if not pid:
        raise ValueError(f"{service} has no process")
    executable = "/usr/bin/dji_gui_on_disp0" if service == "gui.service" else "/usr/bin/dji_gui_on_disp1"
    if os.readlink(f"/proc/{pid}/exe") != executable:
        raise ValueError("Unexpected native GUI executable")
    stat = pathlib.Path(f"/proc/{pid}/stat").read_text().split(") ", 1)[1].split()
    mem = Memory(pid)
    try:
        cycle_address = mem.base("/usr/lib/libewrte_gfx_soft_ttfe.11.00.so") + EW_UPDATE_CYCLE
        agent = mem.u64(mem.base("/usr/lib/libewplatform_unity.so") + PLATFORM_AGENT)
        cycle = mem.u32(cycle_address)
        frame = mem.u64(agent + 0x820)
        free = mem.u64(agent + 0x838)
        if free > 2:
            raise ValueError("Unexpected GUI buffer queue size")
        result = {"pid": pid, "starttime": stat[19], "ew_update_cycle": cycle, "free_buffers": free}
        if capture:
            if frame not in (agent + 0x190, agent + 0x4D8):
                raise ValueError("Unexpected GUI frame context")
            header = mem.read(frame, 0x68)
            address = struct.unpack_from("<Q", header, 8)[0]
            width, height, pitch = struct.unpack_from("<III", header, 0x48)
            size = struct.unpack_from("<I", header, 0x60)[0]
            if (width, height, pitch, size) != (448, 712, 1792, 1275904):
                raise ValueError("Unsupported rear GUI buffer geometry")
            data = read_gui_pixels(lib, mem, address, size)
            pixels = packed_pixels(data, pitch)
            stable = mem.u32(cycle_address) == cycle and mem.u64(agent + 0x820) == frame
            result["capture"] = {"width": 400, "height": 712, "stable": stable,
                                 "sha256": hashlib.sha256(pixels).hexdigest(),
                                 "pixels_zlib": base64.b64encode(zlib.compress(pixels)).decode()}
        return result
    finally:
        mem.close()


def capture_scanout(lib, plane):
    fd = os.open("/dev/dri/card0", os.O_RDONLY | os.O_CLOEXEC)
    # drm_mode_fb_cmd2: 17 u32, alignment padding, 4 u64. Linux UAPI, 104 bytes.
    fb = (ctypes.c_uint64 * 13)()
    values = ctypes.cast(fb, ctypes.POINTER(ctypes.c_uint32))
    values[0] = plane["fb_id"]
    acquired = False
    try:
        ioctl(lib, fd, 0xC06864CE, fb)  # DRM_IOCTL_MODE_GETFB2
        acquired = True
        width, height, fmt = values[1], values[2], values[3]
        pitch, offset = values[9], values[13]
        if fmt != 0x34325241 or fb[9] != 0 or not (400 <= width <= 448 and 712 <= height <= 736):
            raise ValueError("Unsupported rear AR24 scanout layout")
        prime = (ctypes.c_int32 * 3)(values[5], os.O_CLOEXEC, -1)
        ioctl(lib, fd, 0xC00C642D, prime)  # DRM_IOCTL_PRIME_HANDLE_TO_FD, read only
        try:
            data = read_dma(lib, prime[2], offset + pitch * height)
        finally:
            os.close(prime[2])
        # Check selection immediately after the copy. PNG compression and other
        # plane captures can span several normal double-buffer flips.
        copied_ns = time.monotonic_ns()
        selected = {(p["plane_id"], p["fb_id"]) for p in drm_planes(pathlib.Path(DRM_STATE).read_text())}
        still_selected = (plane["plane_id"], plane["fb_id"]) in selected
        pixels = packed_pixels(data, pitch, offset=offset)
        return {**plane, "width": 400, "height": 712,
                "copied_ns": copied_ns, "still_selected": still_selected,
                "sha256": hashlib.sha256(pixels).hexdigest(),
                "pixels_zlib": base64.b64encode(zlib.compress(pixels)).decode()}
    finally:
        try:
            if acquired:
                for handle in set(values[5:9]) - {0}:
                    ioctl(lib, fd, 0x40086409, (ctypes.c_uint32 * 2)(handle, 0))  # GEM_CLOSE
        finally:
            os.close(fd)


def display_sample(capture, lib):
    now = time.monotonic_ns()
    state = pathlib.Path(DRM_STATE).read_text()
    result = {"monotonic_ns": now, "planes": drm_planes(state), "screens": {}, "gui": {}}
    for name, index, encoder in (("rear", 0, 60), ("front", 1, 71)):
        retire = pathlib.Path(f"/sys/class/drm/sde-crtc-{index}/retire_frame_event").read_text()
        status = pathlib.Path(f"/sys/kernel/debug/dri/0/encoder{encoder}/status").read_text()
        active = re.search(rf"(?m)^crtc\[\d+\]: crtc-{index}\n((?:\t[^\n]*\n)*)", state)
        result["screens"][name] = {
            "active": bool(active and "\tactive=1\n" in active[1]),
            "brightness": int(pathlib.Path(f"/sys/class/backlight/panel{index}-backlight/brightness").read_text()),
            "retire_ns": int(re.search(r"RETIRE_FRAME_TIME=(\d+)", retire)[1]),
            "vsync": int(re.search(r"vsync:\s*(\d+)", status)[1]),
            "underrun": int(re.search(r"underrun:\s*(\d+)", status)[1]),
        }
    for service in ("gui.service", "sub_gui.service"):
        result["gui"][service] = gui_sample(service, capture and service == "gui.service", lib)
    if capture:
        scanout = []
        # Refresh the plane state after reading EW; a frame may have just changed.
        for plane in drm_planes(pathlib.Path(DRM_STATE).read_text()):
            if plane["crtc"] != "crtc-0" or plane["format"] != "AR24":
                continue
            # Earlier planes can take a frame interval to copy/compress. Resolve
            # this plane's current FB immediately before acquiring its GEM handle.
            current = next((p for p in drm_planes(pathlib.Path(DRM_STATE).read_text())
                            if p["plane_id"] == plane["plane_id"] and p["crtc"] == "crtc-0"), None)
            if not current or current["format"] != "AR24":
                continue
            plane = current
            try:
                scanout.append(capture_scanout(lib, plane))
            except OSError as exc:
                if exc.errno != errno.ENOENT:
                    raise
                # A compositor may retire a framebuffer between GETSTATE and GETFB2.
                scanout.append({**plane, "retired_during_capture": True})
        native = result["gui"]["gui.service"]["capture"]
        for item in scanout:
            item["matches_native_gui"] = bool(native["stable"] and item.get("still_selected")
                                               and item.get("sha256") == native["sha256"])
        result["scanout"] = scanout
        result["gui_scanout_matches"] = any(item["matches_native_gui"] for item in scanout)
    return result


def collect_device(samples=3, interval=0.5, capture=False):
    if os.getuid() != 0 or os.uname().machine != "aarch64":
        raise ValueError("Requires root on the supported AArch64 camera")
    for path, expected in FINGERPRINTS.items():
        if hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest() != expected:
            raise ValueError("Unsupported firmware: " + path)
    boot_path = pathlib.Path("/proc/sys/kernel/random/boot_id")
    boot = boot_path.read_text().strip()
    lib = libc_handle()
    result = {"schema": 1, "boot_id": boot, "samples": []}
    for i in range(samples):
        result["samples"].append(display_sample(capture, lib))
        if i + 1 < samples:
            time.sleep(interval)
    if boot_path.read_text().strip() != boot:
        raise ValueError("Camera rebooted during observation")
    return result


def read_device(serial, samples=3, interval=0.5, capture=False):
    if not 1 <= samples <= 30 or not 0.1 <= interval <= 2:
        raise ValueError("Invalid display sampling window")
    source = pathlib.Path(__file__).read_text().split('\nif __name__ == "__main__":')[0]
    source += f"\nprint(json.dumps(collect_device({samples!r}, {interval!r}, {capture!r})))\n"
    result = subprocess.run(["adb", "-s", serial, "shell", "python3 -c " + shlex.quote(source)],
                            capture_output=True, text=True, timeout=15 + samples * interval)
    # This ADB occasionally returns 255 after delivering complete output.
    # Read-only results are accepted only if a complete, expected JSON document arrived.
    if result.returncode not in (0, 255):
        raise RuntimeError(result.stderr + result.stdout[:2000])
    try:
        value = json.loads(result.stdout)
    except ValueError as exc:
        raise RuntimeError("Display observation failed: " + result.stderr + result.stdout[:2000]) from exc
    if value.get("schema") != 1 or len(value.get("samples", [])) != samples:
        raise ValueError("Incomplete display observation")
    return value


def verify_observation(value, require_progress=False, require_pixels=False):
    samples = value["samples"]
    first, last = samples[0], samples[-1]
    def identity(sample):
        gui = sample["gui"]["gui.service"]
        return gui["pid"], gui.get("starttime")
    if any(identity(s) != identity(first) for s in samples):
        raise ValueError("GUI restarted during display observation")
    rear = last["screens"]["rear"]
    if not rear["active"] or rear["brightness"] <= 0:
        raise ValueError("Rear screen is off; cannot verify presentation")
    if require_progress and rear["retire_ns"] <= first["screens"]["rear"]["retire_ns"]:
        raise ValueError("Rear display retirement did not advance")
    if rear["underrun"] > first["screens"]["rear"]["underrun"]:
        raise ValueError("Display underrun counter increased")
    native = last["gui"]["gui.service"].get("capture", {})
    matching_samples = [i for i, sample in enumerate(samples)
                        if sample.get("gui_scanout_matches")
                        and native.get("stable")
                        and sample["gui"]["gui.service"].get("capture", {}).get("sha256") == native.get("sha256")]
    if require_pixels and not matching_samples:
        raise ValueError("Rendered GUI pixels are not verified in the selected scanout")
    return {"retire_advanced": rear["retire_ns"] > first["screens"]["rear"]["retire_ns"],
            "gui_scanout_matches": bool(matching_samples), "matching_sample_indices": matching_samples,
            "rear_on": True, "physical_display_verified": False}


def png_from_bgra(pixels, width, height):
    """Rotate the physical rear buffer 90 degrees CCW for readable evidence."""
    if len(pixels) != width * height * 4:
        raise ValueError("Invalid packed pixel length")
    rows = []
    for y in range(width):
        row = bytearray([0])
        for x in range(height):
            at = (x * width + width - 1 - y) * 4
            b, g, r, a = pixels[at:at + 4]
            row.extend((r, g, b, a))
        rows.append(row)

    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", height, width, 8, 6, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(b"".join(rows))) + chunk(b"IEND", b""))


def save_evidence(value, directory):
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    # Strip pixels from JSON while retaining their hashes and separate PNG artifacts.
    value = json.loads(json.dumps(value))
    for index, sample in enumerate(value["samples"]):
        items = [("native-gui", sample["gui"]["gui.service"].get("capture"))]
        items += [(f"scanout-plane-{item['plane_id']}", item) for item in sample.get("scanout", [])]
        for name, item in items:
            if not item or "pixels_zlib" not in item:
                continue
            pixels = zlib.decompress(base64.b64decode(item.pop("pixels_zlib")))
            if hashlib.sha256(pixels).hexdigest() != item["sha256"]:
                raise ValueError("Corrupt captured pixels")
            filename = f"{index:02d}-{name}.png"
            (directory / filename).write_bytes(png_from_bgra(pixels, item["width"], item["height"]))
            item["image"] = filename
    (directory / "display.json").write_text(json.dumps(value, indent=2) + "\n")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial", required=True)
    parser.add_argument("--samples", type=int, default=3)
    parser.add_argument("--interval", type=float, default=0.5)
    parser.add_argument("--capture", action="store_true")
    parser.add_argument("--require-progress", action="store_true", help="For a known active preview or deliberate animation")
    parser.add_argument("--output", type=pathlib.Path)
    args = parser.parse_args()
    directory = args.output or pathlib.Path(__file__).resolve().parents[1] / ".build-tools" / ("gui-display-" + uuid.uuid4().hex[:12])
    value = read_device(args.serial, args.samples, args.interval, args.capture)
    save_evidence(value, directory)
    checks = verify_observation(value, require_progress=args.require_progress, require_pixels=args.capture)
    print(json.dumps({"evidence": str(directory), **checks}, ensure_ascii=False))


if __name__ == "__main__":
    main()
