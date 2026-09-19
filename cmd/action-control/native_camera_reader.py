"""One-shot state/recording adapter for the verified AC204 camera client ABI.

Executed by Action Control in an isolated Python process with an external
deadline. Recording requests are explicit; the default remains read-only.
No Python callbacks, mode setters, capture, playback, or preview subscription.
See docs/NATIVE_CAMERA_REUSE.md for the offline ABI and lifecycle evidence.
"""

from contextlib import contextmanager
import ctypes as C
import hashlib
import json
import os
import platform
import socket
import sys
import time


LIBRARIES = {
    "/usr/lib/libdcam_camera_service_client.so": "98731a8252ee1d1e557af05e3369d33192b9c25142e97513c774f47c16bf27fa",
    "/usr/lib/libdcam_camera_service_server.so": "da7713234f19353054fd9faede2fa9feb211a99564ba4352d796d538cf9c859f",
}


class CreateParams(C.Structure):
    # The C wrapper copies these 16 bytes. Its callback trampoline checks the
    # second pointer before calling it with the first pointer as user data.
    _fields_ = [("user_data", C.c_void_p), ("on_disconnect", C.c_void_p)]


class ProbeError(Exception):
    def __init__(self, status, reason):
        super().__init__(reason)
        self.status = status


def check_environment():
    if (
        sys.platform != "linux"
        or platform.machine() != "aarch64"
        or os.geteuid() != 0
        or C.sizeof(CreateParams) != 16
        or C.sizeof(C.c_int) != 4
    ):
        raise ProbeError("unavailable", "Native reader requires the ARM64 camera.")
    with open("/build.prop", encoding="utf-8") as f:
        if "ro.product.name=qcs8550_ac204" not in {line.strip() for line in f}:
            raise ProbeError("unavailable", "Unsupported camera product.")
    for path, expected in LIBRARIES.items():
        digest = hashlib.sha256()
        with open(path, "rb") as f:
            for block in iter(lambda: f.read(65536), b""):
                digest.update(block)
        if digest.hexdigest() != expected:
            raise ProbeError("unsupported_firmware", "Unverified camera client/server ABI.")


def bind_library():
    lib = C.CDLL(next(iter(LIBRARIES)), mode=os.RTLD_NOW | os.RTLD_LOCAL)
    signatures = {
        "camera_manager_create": [C.POINTER(CreateParams), C.POINTER(C.c_void_p)],
        "camera_manager_destroy": [C.c_void_p],
        "camera_manager_get_camera_amount": [C.c_void_p, C.POINTER(C.c_int)],
        "camera_manager_get_workmode": [C.c_void_p, C.POINTER(C.c_int)],
        "camera_manager_connect_camera": [C.c_void_p, C.c_int, C.POINTER(C.c_void_p)],
        "camera_get_record_state": [C.c_void_p, C.POINTER(C.c_int)],
        "camera_get_capture_state": [C.c_void_p, C.POINTER(C.c_int)],
        "camera_get_mode_profile": [C.c_void_p, C.POINTER(C.c_int)],
        # Struct-returning getters: a 64-byte buffer avoids the stack overflow a
        # 4-byte int would cause. Fields are decoded from raw bytes below.
        "camera_get_video_format": [C.c_void_p, C.POINTER(C.c_ubyte)],
        "camera_get_video_codec_type": [C.c_void_p, C.POINTER(C.c_ubyte)],
        "camera_get_video_storage_format": [C.c_void_p, C.POINTER(C.c_ubyte)],
        "camera_get_eis_status": [C.c_void_p, C.POINTER(C.c_ubyte)],
    }
    for name, args in signatures.items():
        fn = getattr(lib, name)
        fn.argtypes = args
        fn.restype = C.c_int
    return lib


def checked(name, fn, *args):
    rc = fn(*args)
    if rc != 0:
        raise ProbeError("error", "%s returned %d." % (name, rc))


@contextmanager
def camera_connection(lib):
    manager = C.c_void_p()
    device = C.c_void_p()
    params = CreateParams()  # Both optional callback fields are null.
    checked("create", lib.camera_manager_create, C.byref(params), C.byref(manager))
    if not manager.value:
        raise ProbeError("error", "create returned an empty manager.")
    try:
        amount = C.c_int(-1)
        checked("amount", lib.camera_manager_get_camera_amount, manager, C.byref(amount))
        # The verified camera has one device. Never guess another camera ID or
        # index the native wrapper's fixed device array for a different product.
        if amount.value != 1:
            raise ProbeError("unsupported_firmware", "Unexpected camera device count.")
        checked("connect", lib.camera_manager_connect_camera, manager, 0, C.byref(device))
        if not device.value:
            raise ProbeError("error", "connect returned an empty camera.")
        yield manager, device
    finally:
        # This API disconnects this manager's devices, removes its own listeners,
        # releases client objects, and frees wrappers. No handle offsets are used.
        checked("destroy", lib.camera_manager_destroy, manager)


def read_state(lib, device):
    result = {}
    for field, fn in (
        ("record_state", lib.camera_get_record_state),
        ("capture_state", lib.camera_get_capture_state),
    ):
        value = C.c_int(-2147483648)
        checked(field, fn, device, C.byref(value))
        if value.value == -2147483648:
            raise ProbeError("error", field + " was not written.")
        result[field] = value.value
    return result


def read_workmode(lib, manager):
    value = C.c_int(-2147483648)
    checked("workmode", lib.camera_manager_get_workmode, manager, C.byref(value))
    if value.value == -2147483648:
        raise ProbeError("error", "workmode was not written.")
    return value.value


def read_mode_profile(lib, device):
    value = C.c_int(-2147483648)
    checked("mode_profile", lib.camera_get_mode_profile, device, C.byref(value))
    if value.value == -2147483648:
        raise ProbeError("error", "mode_profile was not written.")
    return value.value


def read_int(lib, name, device):
    # Struct getters write >4 bytes; read into 64 bytes and take the leading int.
    buf = (C.c_ubyte * 64)()
    rc = getattr(lib, name)(device, buf)
    if rc != 0:
        return None  # -1015 (not this mode) / -1009 (not applicable) are expected.
    return int.from_bytes(bytes(buf[:4]), "little", signed=True)


def read_video_settings(lib, device):
    # Only the video profile exposes these; other modes reject with -1015. Fields
    # are reported raw; labels are applied from the documented enum tables.
    fmt_buf = (C.c_ubyte * 64)()
    settings = {"resolution": None, "resolution_raw": None, "fps": None, "codec": None, "storage": None, "eis": None}
    if getattr(lib, "camera_get_video_format")(device, fmt_buf) == 0:
        word0 = int.from_bytes(bytes(fmt_buf[0:4]), "little", signed=True)
        # The resolution enum is the low byte; the upper bytes carry flags (e.g.
        # aspect/HDR bits) that vary independently. Documented enum values are all
        # < 256, so mask to the low byte and keep the raw word for evidence.
        settings["resolution"] = word0 & 0xFF
        settings["resolution_raw"] = word0
        settings["fps"] = int.from_bytes(bytes(fmt_buf[4:8]), "little", signed=True)
    settings["codec"] = read_int(lib, "camera_get_video_codec_type", device)
    settings["storage"] = read_int(lib, "camera_get_video_storage_format", device)
    settings["eis"] = read_int(lib, "camera_get_eis_status", device)
    return settings


def query(lib):
    with camera_connection(lib) as (manager, device):
        return {
            "camera_id": 0,
            "camera_amount": 1,
            "workmode": read_workmode(lib, manager),
            "mode_profile": read_mode_profile(lib, device),
            "video_settings": read_video_settings(lib, device),
            **read_state(lib, device),
        }


@contextmanager
def native_library():
    # Hashes are checked before dlopen or any private native call.
    check_environment()
    # Shared by readers and recording requests, including other CLI processes.
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as guard:
        try:
            guard.bind("\0action-control.native-camera-state.v1")
        except OSError as exc:
            import errno

            if exc.errno == errno.EADDRINUSE:
                raise ProbeError("busy", "A native camera client is already active.") from exc
            raise
        yield bind_library()


def observe():
    result = {"schema": 1, "status": "error"}
    try:
        with native_library() as lib:
            result.update(query(lib))
        result["status"] = "ok"
    except ProbeError as exc:
        result = {"schema": 1, "status": exc.status, "reason": str(exc)}
    except (OSError, AttributeError) as exc:
        result = {"schema": 1, "status": "unavailable", "reason": str(exc)}
    return result


def record(action):
    result = {
        "schema": 1, "action": action, "outcome": "not_sent",
        "dispatched": False, "native_code": None,
        "before": None, "after": None, "reason": "",
    }
    # No dynamic symbol name or unverified operation can reach the native API.
    symbols = {
        "start_recording": ("camera_start_recording", 1),
        "stop_recording": ("camera_stop_recording", 3),
    }
    if action not in symbols:
        result["reason"] = "Unsupported recording action."
        return result
    name, target = symbols[action]
    try:
        with native_library() as lib:
            fn = getattr(lib, name)
            fn.argtypes, fn.restype = [C.c_void_p], C.c_int
            with camera_connection(lib) as (_manager, device):
                before = read_state(lib, device)
                result["before"] = before
                if before["record_state"] == target:
                    result.update(outcome="already", after=before)
                elif (
                    before["record_state"] != (3 if target == 1 else 1)
                    or (target == 1 and before["capture_state"] != 0)
                ):
                    result.update(outcome="blocked", reason="Native camera is not in a verified state for this action.")
                else:
                    # Set uncertainty BEFORE the call: a native request cannot
                    # be undone by killing this process, and must not be retried.
                    result.update(dispatched=True, outcome="unknown")
                    rc = fn(device)
                    result["native_code"] = rc
                    if rc != 0:
                        result.update(outcome="rejected", reason="%s returned %d." % (name, rc))
                    else:
                        result["outcome"] = "accepted"
                        # Only observe; no toggle, retry, mode change, or
                        # compensating stop. Native service owns all transitions.
                        deadline = time.monotonic() + 2.0
                        while True:
                            result["after"] = read_state(lib, device)
                            if result["after"]["record_state"] == target:
                                result["outcome"] = "confirmed"
                                break
                            if time.monotonic() >= deadline:
                                break
                            time.sleep(0.1)
    except (ProbeError, OSError, AttributeError) as exc:
        result["outcome"] = "unknown" if result["dispatched"] else "not_sent"
        result["reason"] = str(exc)
        result["after"] = None
    return result


def capture():
    # A photo is a one-shot action, not a toggle to a stable target state. We
    # only dispatch when idle, then report the native code and observed capture
    # state. We do not invent a "capture complete" code from the raw integer;
    # the saved photo file is confirmed separately by the media layer.
    result = {
        "schema": 1, "action": "capture", "outcome": "not_sent",
        "dispatched": False, "native_code": None,
        "before": None, "after": None, "reason": "",
    }
    try:
        with native_library() as lib:
            fn = lib.camera_start_capture
            fn.argtypes, fn.restype = [C.c_void_p], C.c_int
            with camera_connection(lib) as (_manager, device):
                before = read_state(lib, device)
                result["before"] = before
                if before["record_state"] != 3 or before["capture_state"] != 0:
                    result.update(outcome="blocked", reason="Native camera is not idle for capture.")
                else:
                    # Set uncertainty BEFORE the call: a native request cannot be
                    # undone by killing this process, and must not be retried.
                    result.update(dispatched=True, outcome="unknown")
                    rc = fn(device)
                    result["native_code"] = rc
                    if rc != 0:
                        result.update(outcome="rejected", reason="camera_start_capture returned %d." % rc)
                    else:
                        # Observe the transient capture state settle; never toggle
                        # or retry. Native service owns all transitions.
                        result["outcome"] = "accepted"
                        deadline = time.monotonic() + 2.0
                        while True:
                            result["after"] = read_state(lib, device)
                            if time.monotonic() >= deadline:
                                break
                            time.sleep(0.1)
    except (ProbeError, OSError, AttributeError) as exc:
        result["outcome"] = "unknown" if result["dispatched"] else "not_sent"
        result["reason"] = str(exc)
        result["after"] = None
    return result


def mode_state(lib, device):
    return {**read_state(lib, device), "mode_profile": read_mode_profile(lib, device)}


def set_mode(action):
    # Switch the native shooting mode to a mapped profile. Only the verified
    # profiles are allowed; unknown integers are never sent to the mode setter.
    profiles = {"mode_photo": 5, "mode_video": 1}
    result = {
        "schema": 1, "action": action, "outcome": "not_sent",
        "dispatched": False, "native_code": None,
        "before": None, "after": None, "reason": "",
    }
    if action not in profiles:
        result["reason"] = "Unsupported mode action."
        return result
    target = profiles[action]
    try:
        with native_library() as lib:
            fn = lib.camera_set_mode_profile
            fn.argtypes, fn.restype = [C.c_void_p, C.c_int], C.c_int
            with camera_connection(lib) as (_manager, device):
                before = mode_state(lib, device)
                result["before"] = before
                if before["mode_profile"] == target:
                    result.update(outcome="already", after=before)
                elif before["record_state"] != 3 or before["capture_state"] != 0:
                    result.update(outcome="blocked", reason="Native camera is not idle for a mode switch.")
                else:
                    # Set uncertainty BEFORE the call: a native request cannot be
                    # undone by killing this process, and must not be retried.
                    result.update(dispatched=True, outcome="unknown")
                    rc = fn(device, target)
                    result["native_code"] = rc
                    if rc != 0:
                        result.update(outcome="rejected", reason="camera_set_mode_profile returned %d." % rc)
                    else:
                        # Observe the mode settle at the target; never retry or
                        # send a compensating switch. Native service owns it.
                        result["outcome"] = "accepted"
                        deadline = time.monotonic() + 2.0
                        while True:
                            result["after"] = mode_state(lib, device)
                            if result["after"]["mode_profile"] == target:
                                result["outcome"] = "confirmed"
                                break
                            if time.monotonic() >= deadline:
                                break
                            time.sleep(0.1)
    except (ProbeError, OSError, AttributeError) as exc:
        result["outcome"] = "unknown" if result["dispatched"] else "not_sent"
        result["reason"] = str(exc)
        result["after"] = None
    return result


def main():
    # Native logging must not be mistaken for the JSON protocol. Keep the saved
    # output descriptor private; vendor output goes to the bounded stderr pipe.
    output = os.dup(1)
    os.dup2(2, 1)
    try:
        if len(sys.argv) == 1:
            result = observe()
        elif sys.argv[1] == "capture":
            result = capture()
        elif sys.argv[1] in ("mode_photo", "mode_video"):
            result = set_mode(sys.argv[1])
        else:
            result = record(sys.argv[1] if len(sys.argv) == 2 else "")
        os.write(output, (json.dumps(result, separators=(",", ":")) + "\n").encode())
    finally:
        os.close(output)


if __name__ == "__main__":
    main()
