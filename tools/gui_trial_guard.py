#!/usr/bin/env python3
"""Independent, bounded recovery for a temporary rear-GUI trial (camera side)."""
import glob
import json
import os
import pathlib
import subprocess
import sys
import time

from gui_display import collect_device, verify_observation


def writable_media():
    writes = []
    for proc in glob.glob("/proc/[0-9]*"):
        for fd in glob.glob(proc + "/fd/*"):
            try:
                target = os.readlink(fd)
                name = target.lower().removesuffix(" (deleted)")
                if not name.endswith((".mp4", ".mov", ".m4v", ".lrf", ".dng", ".jpg", ".jpeg", ".wav", ".aac")):
                    continue
                info = pathlib.Path(proc + "/fdinfo/" + fd.rsplit("/", 1)[1]).read_text()
                flags = next(int(line.split()[1], 8) for line in info.splitlines() if line.startswith("flags:"))
                if flags & 3:
                    writes.append({"pid": int(proc.rsplit("/", 1)[1]), "file": target})
            except (OSError, StopIteration):
                pass
    return writes


def run(directory):
    directory = pathlib.Path(directory)
    if directory.parent != pathlib.Path("/run") or not directory.name.startswith("action-control-ui-"):
        raise ValueError("Invalid trial directory")
    st = directory.lstat()
    if not directory.is_dir() or directory.is_symlink() or st.st_uid or st.st_mode & 0o022:
        raise ValueError("Untrusted trial directory")
    config = json.loads((directory / "guard.json").read_text())
    state = {"phase": "restoring", "boot_id": pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()}

    def report(phase, **details):
        state.update(phase=phase, **details)
        temp = directory / "recovery.tmp"
        temp.write_text(json.dumps(state, indent=2) + "\n")
        os.chmod(temp, 0o600)
        temp.replace(directory / "recovery.json")
        print(json.dumps(state), flush=True)

    def command(*args):
        subprocess.run(args, check=True, capture_output=True, text=True, timeout=35)

    report("restoring")
    dropin = pathlib.Path(config["dropin"])
    expected_dropin = pathlib.Path("/run/systemd/system/gui.service.d") / ("90-" + directory.name + ".conf")
    if dropin != expected_dropin:
        raise ValueError("Unexpected override ownership")
    restart = False
    if dropin.exists():
        if dropin.read_bytes() != (directory / "dropin.conf").read_bytes():
            raise ValueError("Override changed outside this trial")
        dropin.unlink()
        try:
            dropin.parent.rmdir()
        except OSError:
            pass
        command("systemctl", "daemon-reload")
        restart = True
    # A process can still map the plugin if a prior recovery was interrupted.
    pid = int(subprocess.check_output(["systemctl", "show", "gui.service", "-p", "MainPID", "--value"]))
    if not pid:
        restart = True
    if pid and str(directory) in pathlib.Path(f"/proc/{pid}/maps").read_text():
        restart = True
    writes = writable_media()
    if writes:
        report("media_busy", writable_media=writes)
        return 2  # Leave the recording alone; the original configuration is restored.
    try:
        if restart:
            command("systemctl", "reset-failed", "gui.service")
            command("systemctl", "restart", "gui.service")
        report("checking_display")
        last_error = None
        for attempt in range(5):
            try:
                evidence = collect_device(3, 0.4, True)
                if evidence["samples"][-1]["screens"]["rear"]["brightness"] == 0:
                    report("screen_off", reason="Original GUI restored; rear screen is off, so presentation is unverified")
                    return 3
                checks = verify_observation(evidence, require_progress=True, require_pixels=True)
                if pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip() != state["boot_id"]:
                    raise ValueError("Boot identity changed during recovery")
                report("restored", display_checks=checks)
                return 0
            except Exception as exc:
                last_error = str(exc)
                time.sleep(0.5)
        raise RuntimeError(last_error)
    except Exception as exc:
        report("display_recovery_failed", reason=str(exc))
        # Check again immediately before a fallback reboot, including all processes.
        writes = writable_media()
        if writes:
            report("media_busy", writable_media=writes)
            return 2
        # Keep the reason across /run being cleared, inside the application's area.
        diagnostic = pathlib.Path("/blackbox/upgrade/action-control/diagnostics")
        diagnostic.mkdir(parents=True, exist_ok=True)
        report("rebooting")
        (diagnostic / (directory.name + "-recovery.json")).write_text(json.dumps(state, indent=2) + "\n")
        command("systemctl", "reboot", "--no-block")
        return 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1]))
