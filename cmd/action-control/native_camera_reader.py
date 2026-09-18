"""One-shot read-only adapter for the verified AC204 camera client ABI.

Executed by Action Control in an isolated Python process with an external
deadline. No Python callbacks, camera setters, playback, or preview subscription.
See docs/NATIVE_CAMERA_REUSE.md for the offline ABI and lifecycle evidence.
"""

import ctypes as C
import hashlib
import json
import os
import platform
import socket
import sys


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
        "camera_manager_connect_camera": [C.c_void_p, C.c_int, C.POINTER(C.c_void_p)],
        "camera_get_record_state": [C.c_void_p, C.POINTER(C.c_int)],
        "camera_get_capture_state": [C.c_void_p, C.POINTER(C.c_int)],
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


def query(lib):
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
        result = {"camera_id": 0, "camera_amount": 1}
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
    finally:
        # This API disconnects this manager's devices, removes its own listeners,
        # releases client objects, and frees wrappers. No handle offsets are used.
        checked("destroy", lib.camera_manager_destroy, manager)


def observe():
    result = {"schema": 1, "status": "error"}
    try:
        # Hashes are checked before dlopen or any private native call.
        check_environment()
        # A Linux abstract socket limits concurrent readers across server and
        # CLI processes without creating configuration or lock files.
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as guard:
            try:
                guard.bind("\0action-control.native-camera-state.v1")
            except OSError as exc:
                import errno

                if exc.errno == errno.EADDRINUSE:
                    raise ProbeError("busy", "A native state reader is already active.") from exc
                raise
            lib = bind_library()
            result.update(query(lib))
        result["status"] = "ok"
    except ProbeError as exc:
        result = {"schema": 1, "status": exc.status, "reason": str(exc)}
    except (OSError, AttributeError) as exc:
        result = {"schema": 1, "status": "unavailable", "reason": str(exc)}
    return result


def main():
    # Native logging must not be mistaken for the JSON protocol. Keep the saved
    # output descriptor private; vendor output goes to the bounded stderr pipe.
    output = os.dup(1)
    os.dup2(2, 1)
    try:
        result = observe()
        os.write(output, (json.dumps(result, separators=(",", ":")) + "\n").encode())
    finally:
        os.close(output)


if __name__ == "__main__":
    main()
