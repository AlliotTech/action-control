"""One-shot native-hotspot control for the verified AC204 dji_network.

Executed by Action Control in an isolated Python process with an external
deadline. Drives the camera's own dji_network daemon to bring the SoftAP up or
down by calling its already-initialised wifi manager, via a ptrace function
call into the live process. No new network stack, no takeover, no DUSS client
rebuild. See NETWORK_HOTSPOT_FEASIBILITY.md for the reversed evidence.

Actions: "start" -> wifi_mgmt_start_wifi(0)  (mode 0 = AP)
         "stop"  -> wifi_mgmt_stop_wifi(0)

Symbol addresses are valid ONLY for the pinned dji_network SHA-256; a mismatch
disables control rather than guessing an address for another firmware.
"""

import ctypes as C
import ctypes.util
import hashlib
import json
import os
import platform
import struct
import sys
import time

# dji_network is non-PIE (ELF EXEC, base 0x400000): file symbol == runtime addr.
BINARY = "/usr/bin/dji_network"
BINARY_SHA256 = "a6134da5cfa564e6f712fb978fb907f2a2a99509e6abf724acdf4ea147d050aa"
# wifi_mgmt_start_wifi / wifi_mgmt_stop_wifi, verified against the pinned SHA.
# keepalive resets dji_network's adaptive low-power timer so it does not tear
# the SoftAP down after ~60s without a DJI App/BT session. Zero-arg, only sets
# an activity flag + logs; safe to inject periodically while the AP is up.
FUNC = {"start": 0x444650, "stop": 0x4447F0, "keepalive": 0x481600}
MODE_AP = 0

PTRACE_ATTACH, PTRACE_DETACH, PTRACE_CONT = 16, 17, 7
PTRACE_GETREGSET, PTRACE_SETREGSET = 0x4204, 0x4205
NT_PRSTATUS = 1
# aarch64 user_pt_regs: x0..x30 (31), sp, pc, pstate = 34 * u64
NREGS = 34
REGS_SIZE = NREGS * 8


class ProbeError(Exception):
    def __init__(self, status, reason):
        super().__init__(reason)
        self.status = status


def check_environment():
    if sys.platform != "linux" or platform.machine() != "aarch64" or os.geteuid() != 0:
        raise ProbeError("unavailable", "Native hotspot requires the ARM64 camera as root.")
    try:
        with open("/build.prop", encoding="utf-8") as f:
            if "ro.product.name=qcs8550_ac204" not in {line.strip() for line in f}:
                raise ProbeError("unavailable", "Unsupported camera product.")
    except FileNotFoundError:
        raise ProbeError("unavailable", "Unsupported camera product.")
    digest = hashlib.sha256()
    try:
        with open(BINARY, "rb") as f:
            for block in iter(lambda: f.read(65536), b""):
                digest.update(block)
    except FileNotFoundError:
        raise ProbeError("unavailable", "dji_network not present.")
    if digest.hexdigest() != BINARY_SHA256:
        raise ProbeError("unsupported_firmware", "Unverified dji_network; control disabled.")


def find_pid():
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            with open("/proc/%s/comm" % name, encoding="utf-8") as f:
                if f.read().strip() == "dji_network":
                    return int(name)
        except OSError:
            continue
    raise ProbeError("unavailable", "dji_network not running.")


def _errcheck(res, func, args):
    if res == -1:
        e = C.get_errno()
        raise OSError(e, os.strerror(e))
    return res


def load_libc():
    libc = C.CDLL(ctypes.util.find_library("c") or "libc.so.6", use_errno=True)
    libc.ptrace.restype = C.c_long
    libc.ptrace.argtypes = [C.c_long, C.c_int, C.c_void_p, C.c_void_p]
    libc.ptrace.errcheck = _errcheck
    libc.waitpid.restype = C.c_int
    libc.waitpid.argtypes = [C.c_int, C.POINTER(C.c_int), C.c_int]
    return libc


def getregs(libc, pid):
    buf = (C.c_ubyte * REGS_SIZE)()
    iov = struct.pack("PN", C.addressof(buf), REGS_SIZE)
    iovbuf = C.create_string_buffer(iov, len(iov))
    libc.ptrace(PTRACE_GETREGSET, pid, NT_PRSTATUS, C.cast(iovbuf, C.c_void_p))
    return list(struct.unpack("<%dQ" % NREGS, bytes(buf)))


def setregs(libc, pid, regs):
    packed = struct.pack("<%dQ" % NREGS, *regs)
    buf = C.create_string_buffer(packed, REGS_SIZE)
    iov = struct.pack("PN", C.addressof(buf), REGS_SIZE)
    iovbuf = C.create_string_buffer(iov, len(iov))
    libc.ptrace(PTRACE_SETREGSET, pid, NT_PRSTATUS, C.cast(iovbuf, C.c_void_p))


def inject_call(libc, pid, func_addr, arg0):
    libc.ptrace(PTRACE_ATTACH, pid, None, None)
    status = C.c_int()
    libc.waitpid(pid, C.byref(status), 0)
    saved = getregs(libc, pid)
    try:
        regs = list(saved)
        regs[0] = arg0 & 0xFFFFFFFFFFFFFFFF   # x0
        regs[30] = 0                          # lr = 0 -> return traps at pc 0
        regs[31] &= ~0xF                      # sp 16-byte aligned
        regs[32] = func_addr                  # pc
        setregs(libc, pid, regs)
        fwd, rc, guard = 0, None, 0
        while guard < 300:
            guard += 1
            libc.ptrace(PTRACE_CONT, pid, None, C.c_void_p(fwd))
            fwd = 0
            libc.waitpid(pid, C.byref(status), 0)
            st = status.value
            if not os.WIFSTOPPED(st):
                raise ProbeError("error", "tracee exited during call (0x%x)" % st)
            sig = os.WSTOPSIG(st)
            cur = getregs(libc, pid)
            if cur[32] == 0:                  # lr=0 return trap: call finished
                rc = cur[0] & 0xFFFFFFFF
                if rc >= 0x80000000:
                    rc -= 0x100000000
                break
            if sig in (11, 4, 7):             # SIGSEGV/SIGILL/SIGBUS not at trap
                raise ProbeError("error", "unexpected fault sig=%d pc=0x%x" % (sig, cur[32]))
            fwd = sig                         # forward SIGCHLD etc, keep call running
        if rc is None:
            raise ProbeError("error", "call did not return within guard")
        return rc
    finally:
        setregs(libc, pid, saved)
        libc.ptrace(PTRACE_DETACH, pid, None, None)


def read_wlan0():
    role = state = ssid = None
    try:
        with open("/sys/class/net/wlan0/operstate", encoding="utf-8") as f:
            state = f.read().strip()
    except OSError:
        pass
    try:
        import subprocess
        out = subprocess.run(["iw", "dev", "wlan0", "info"], capture_output=True,
                             text=True, timeout=2).stdout
        for line in out.splitlines():
            line = line.strip()
            if line.startswith("type "):
                role = line[5:].strip()
            elif line.startswith("ssid "):
                ssid = line[5:].strip()
    except Exception:
        pass
    return {"operstate": state, "role": role, "ssid": ssid}


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in FUNC:
        json.dump({"status": "error", "reason": "usage: native_hotspot.py start|stop"}, sys.stdout)
        return 2
    action = sys.argv[1]
    try:
        check_environment()
        pid = find_pid()
        libc = load_libc()
        before = read_wlan0()
        rc = inject_call(libc, pid, FUNC[action], MODE_AP)
        time.sleep(1.0)
        after = read_wlan0()
        json.dump({
            "status": "ok",
            "action": action,
            "native_code": rc,
            "pid": pid,
            "before": before,
            "after": after,
            "observed_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }, sys.stdout)
        return 0 if rc == 0 else 1
    except ProbeError as e:
        json.dump({"status": e.status, "action": action, "reason": str(e)}, sys.stdout)
        return 1
    except OSError as e:
        json.dump({"status": "error", "action": action, "reason": "ptrace: %s" % e}, sys.stdout)
        return 1


if __name__ == "__main__":
    sys.exit(main())
