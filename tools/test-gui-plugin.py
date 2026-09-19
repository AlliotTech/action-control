#!/usr/bin/env python3
"""Bounded rear-GUI trials with scanout evidence and independent display recovery."""
import argparse
import hashlib
import json
import pathlib
import shlex
import signal
import subprocess
import sys
import time
import uuid

from gui_display import read_device, save_evidence, verify_observation

ROOT = pathlib.Path(__file__).resolve().parents[1]
FINGERPRINTS = {
    "/usr/bin/dji_gui_on_disp0": "30eb933b1c2315984e150885124d277c13c1cef3194ba6c08cb3d4d1f6656db1",
    "/usr/bin/dji_gui_on_disp1": "e965fbc8588445f5fa922fd1df38ed7051198fe28a50c33e51c94246c00fdb27",
    "/usr/lib/libgui_image_loader.so": "4774cc379c3cdbcc11242b7bde1b7490f3061c11c09ed79f05e2f0afd7c53c82",
}
SERVICES = ("gui.service", "sub_gui.service", "dji_camera3.service", "dji_media.service")
CONFIG = "/etc/disp0_plugins_config.json"

SNAPSHOT = r'''
import glob, hashlib, json, os, pathlib, subprocess
assert os.getuid() == 0, "Root ADB is required"
files = FILES
services = SERVICES
result = {"boot_id": pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
          "hashes": {p: hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in files},
          "services": {}, "writable_media": [], "dropins": []}
for service in services:
    output = subprocess.check_output(["systemctl", "show", service, "-p", "MainPID", "-p", "ActiveState", "-p", "SubState"], text=True)
    values = dict(line.split("=", 1) for line in output.splitlines() if "=" in line)
    pid = int(values["MainPID"])
    values["starttime"] = pathlib.Path("/proc/%d/stat" % pid).read_text().split(") ", 1)[1].split()[19] if pid else None
    result["services"][service] = values
for proc in glob.glob("/proc/[0-9]*"):
    try:
        name = pathlib.Path(proc + "/comm").read_text().strip()
        for fd in glob.glob(proc + "/fd/*"):
            try:
                target = os.readlink(fd)
                if not target.lower().removesuffix(" (deleted)").endswith((".mp4", ".mov", ".m4v", ".lrf", ".dng", ".jpg", ".jpeg", ".wav", ".aac")):
                    continue
                info = pathlib.Path(proc + "/fdinfo/" + fd.rsplit("/", 1)[1]).read_text()
                flags = next(int(line.split()[1], 8) for line in info.splitlines() if line.startswith("flags:"))
                if flags & 3:
                    result["writable_media"].append({"process": name, "pid": proc.rsplit("/", 1)[1], "file": target})
            except (OSError, StopIteration):
                pass
    except OSError:
        pass
for directory in ("/run/systemd/system/gui.service.d", "/etc/systemd/system/gui.service.d"):
    result["dropins"].extend(glob.glob(directory + "/*.conf"))
print(json.dumps(result))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial", required=True)
    parser.add_argument("--mode", choices=("native", "probe", "menu"), default="probe")
    parser.add_argument("--seconds", type=int, default=15)
    parser.add_argument("--exercise", choices=("panel", "menu"), help="Exercise native touch handling")
    parser.add_argument("--cycles", type=int, default=3, help="Panel open/click/return cycles")
    parser.add_argument("--hold-panel", action="store_true", help="Leave the panel open during the bounded observation period")
    parser.add_argument("--run", action="store_true", help="Actually restart the rear GUI for the bounded trial")
    args = parser.parse_args()
    if not 5 <= args.seconds <= 180:
        parser.error("--seconds must be between 5 and 180")
    if args.exercise and args.mode != "menu":
        parser.error("--exercise requires --mode menu")
    if not 1 <= args.cycles <= 5:
        parser.error("--cycles must be between 1 and 5")
    if args.hold_panel and args.exercise != "menu":
        parser.error("--hold-panel requires --exercise menu")

    trial_id = uuid.uuid4().hex[:12]
    remote = "/run/action-control-ui-" + trial_id
    unit = "action-control-ui-rollback-" + trial_id
    dropin_dir = "/run/systemd/system/gui.service.d"
    dropin = dropin_dir + "/90-action-control-ui-" + trial_id + ".conf"
    evidence = ROOT / ".build-tools" / ("gui-trial-" + trial_id)
    evidence.mkdir(parents=True)
    adb = ["adb", "-s", args.serial]

    def call(arguments, read_only=False, **kwargs):
        for attempt in range(3 if read_only else 1):
            result = subprocess.run(adb + arguments, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, timeout=45, **kwargs)
            if result.returncode != 255:
                break
            if read_only and attempt < 2:
                time.sleep(0.1)
        if result.returncode:
            raise RuntimeError("ADB command failed (" + str(result.returncode) + "): "
                               + repr(arguments) + "\n" + result.stderr.decode(errors="replace")
                               + result.stdout.decode(errors="replace"))
        return result.stdout

    def shell(command, read_only=False):
        return call(["shell", command], read_only=read_only).decode().strip()

    def python(code, read_only=False):
        return shell("python3 -c " + shlex.quote(code), read_only=read_only)

    def save(name, value):
        (evidence / name).write_text(json.dumps(value, indent=2) + "\n")

    def snapshot():
        return json.loads(python(SNAPSHOT.replace("FILES", repr(list(FINGERPRINTS) + [CONFIG]))
                                          .replace("SERVICES", repr(SERVICES)), read_only=True))

    def send(command):
        python("import os, pathlib; p=pathlib.Path(" + repr(remote) + "); "
               "f=p/'command.tmp'; f.write_text(" + repr(command + "\n") + "); "
               "os.chmod(f, 0o600); f.replace(p/'command')")

    def await_status(predicate, label, timeout=8):
        deadline = time.monotonic() + timeout
        value = None
        while time.monotonic() < deadline:
            value = json.loads(shell("cat " + shlex.quote(remote + "/status.json"), read_only=True))
            if value.get("error"):
                raise RuntimeError("Plugin error during " + label + ": " + repr(value))
            if predicate(value):
                samples.append(value)
                return value
            time.sleep(0.25)
        raise RuntimeError(label + " timed out: " + repr(value))

    def display(name, previous=None, progress=False, allow_screen_off=False):
        observed = read_device(args.serial, samples=3, interval=0.35, capture=True)
        save_evidence(observed, evidence / name)
        if allow_screen_off and observed["samples"][-1]["screens"]["rear"]["brightness"] == 0:
            print(json.dumps({"display": name, "screen_off": True,
                              "presentation_verified": False,
                              "reason": "Screen off at the end of bounded manual observation"}), flush=True)
            return observed
        checks = verify_observation(observed, require_progress=progress, require_pixels=True)
        if previous:
            old, new = previous["samples"][-1], observed["samples"][-1]
            if previous["boot_id"] != observed["boot_id"]:
                raise RuntimeError("Camera rebooted during UI exercise")
            old_gui, new_gui = old["gui"]["gui.service"], new["gui"]["gui.service"]
            if old_gui["pid"] != new_gui["pid"] or new_gui["ew_update_cycle"] <= old_gui["ew_update_cycle"]:
                raise RuntimeError("GUI did not render the requested transition: " + name)
            if old_gui["capture"]["sha256"] == new_gui["capture"]["sha256"]:
                raise RuntimeError("GUI pixels did not change: " + name)
            if new["screens"]["rear"]["retire_ns"] <= old["screens"]["rear"]["retire_ns"]:
                raise RuntimeError("No completed display frame after UI transition: " + name)
            checks["pixel_transition_verified"] = True
        print(json.dumps({"display": name, **checks}), flush=True)
        return observed

    before = snapshot()
    save("before.json", before)
    for path, expected in FINGERPRINTS.items():
        if before["hashes"][path] != expected:
            raise RuntimeError("Unsupported firmware: " + path)
    for service, state in before["services"].items():
        if state["ActiveState"] != "active" or state["SubState"] != "running":
            raise RuntimeError("Native service is not running: " + service)
    if before["writable_media"]:
        raise RuntimeError("Recording/media writes detected; no restart performed")
    if before["dropins"]:
        raise RuntimeError("Existing rear-GUI overrides need review: " + repr(before["dropins"]))
    display("display-before", progress=True)

    build = ROOT / ".build-tools/gui-plugin"
    manifest = json.loads((build / "manifest.json").read_text())
    library = build / manifest["file"]
    if library.name != "libaction_control_gui.so" or library.parent != build:
        raise RuntimeError("Unexpected plugin manifest path")
    if hashlib.sha256(library.read_bytes()).hexdigest() != manifest["sha256"]:
        raise RuntimeError("Plugin build does not match its manifest")
    original_config = shell("cat " + shlex.quote(CONFIG), read_only=True)
    config = json.loads(original_config)
    entries = [item for item in config["plugins_configs"] if item["creator_func_name"] == "gui_image_loader_create"]
    if len(entries) != 1 or entries[0]["plugin"] != "/usr/lib/libgui_image_loader.so":
        raise RuntimeError("Unexpected native image plugin configuration")
    entries[0]["plugin"] = remote + "/libaction_control_gui.so"
    (evidence / "disp0_plugins_config.json").write_text(json.dumps(config, indent=2) + "\n")
    (evidence / "dropin.conf").write_text(
        "[Service]\n"
        f"BindReadOnlyPaths={remote}/disp0_plugins_config.json:{CONFIG}\n"
        f"Environment=ACTION_CONTROL_GUI_MODE={args.mode}\n"
        f"Environment=ACTION_CONTROL_GUI_STATE={remote}\n"
        "Restart=no\n"
    )
    # Host and independent timer trigger one oneshot unit. Recovery also verifies
    # KMS pixels/retirement and can reboot normally after a fresh media-idle check.
    (evidence / "guard.json").write_text(json.dumps({"dropin": dropin}) + "\n")
    save("trial.json", {"mode": args.mode, "remote": remote, "unit": unit, "dropin": dropin,
                        "manifest": manifest, "timeout_seconds": args.seconds + 120,
                        "recovery": "restore GUI; verify scanout; reboot if display recovery fails and media is idle"})
    print(json.dumps({"preflight": "passed", "evidence": str(evidence), "remote": remote,
                      "run": args.run}), flush=True)
    if not args.run:
        return

    def interrupted(signum, _frame):
        raise KeyboardInterrupt("Signal " + str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    staged = armed = activated = False
    samples = []
    error = None
    exercise_passed = False
    observation_ended_screen_off = False
    try:
        shell("install -d -m 0700 " + shlex.quote(remote))
        staged = True
        call(["push", str(library), remote + "/libaction_control_gui.so"])
        for name in ("disp0_plugins_config.json", "dropin.conf", "guard.json"):
            call(["push", str(evidence / name), remote + "/" + name])
        for name in ("gui_display.py", "gui_trial_guard.py"):
            call(["push", str(ROOT / "tools" / name), remote + "/" + name])
        # This firmware's ADB sync applies permissive modes, including to parents.
        python("import os, pathlib; p=pathlib.Path(" + repr(remote) + "); os.chmod(p, 0o700); "
               "[os.chmod(f, 0o600) for f in p.iterdir() if f.is_file()]; "
               "assert p.stat().st_mode & 0o777 == 0o700")
        if python("import hashlib; print(hashlib.sha256(open(" + repr(remote + "/libaction_control_gui.so") + ", 'rb').read()).hexdigest())") != manifest["sha256"]:
            raise RuntimeError("Staged plugin hash mismatch")
        shell("systemd-run --unit=" + unit + " --on-active=" + str(args.seconds + 120)
              + "s --timer-property=AccuracySec=1s --property=Type=oneshot --property=TimeoutStartSec=120s /usr/bin/python3 "
              + shlex.quote(remote + "/gui_trial_guard.py") + " " + shlex.quote(remote))
        armed = True
        shell("systemctl is-active --quiet " + unit + ".timer", read_only=True)
        # Repeat recording check immediately before the only native service restart.
        latest = snapshot()
        if latest["writable_media"] or latest["boot_id"] != before["boot_id"]:
            raise RuntimeError("Device state changed before activation")
        if args.mode != "native":
            python("import os, pathlib; p=pathlib.Path(" + repr(dropin_dir) + "); p.mkdir(exist_ok=True); "
                   "os.chmod(p, 0o755); f=open(" + repr(dropin) + ", 'x'); "
                   "f.write(pathlib.Path(" + repr(remote + "/dropin.conf") + ").read_text()); f.close(); "
                   "os.chmod(" + repr(dropin) + ", 0o644)")
        activated = True
        shell("systemctl daemon-reload")
        shell("systemctl restart --no-block gui.service")
        print("Rear GUI " + args.mode + " trial started; independent display recovery is armed.", flush=True)
        startup = time.monotonic() + 25
        while time.monotonic() < startup:
            try:
                if args.mode == "native":
                    pid = shell("systemctl show gui.service -p MainPID --value", read_only=True)
                    if int(pid) > 0 and pid != before["services"]["gui.service"]["MainPID"]:
                        time.sleep(2)
                        break
                else:
                    value = json.loads(shell("cat " + shlex.quote(remote + "/status.json"), read_only=True))
                    if value.get("attached") and value.get("root_ready") and value.get("callback_ticks", 0) > 5:
                        break
            except (RuntimeError, json.JSONDecodeError):
                pass
            time.sleep(0.5)
        else:
            raise RuntimeError("Plugin callback did not become ready")
        if args.mode != "native":
            callback_name = shell("cat /proc/" + str(value["pid"]) + "/task/" + str(value["callback_tid"]) + "/comm")
            if callback_name != "ew_gui_thread":
                raise RuntimeError("Callback is on an unexpected thread: " + callback_name)
            save("callback-thread.json", {"name": callback_name, "pid": value["pid"], "tid": value["callback_tid"]})
        # Do not interact with an extension until the restarted GUI is displayed.
        previous_display = display("display-startup", progress=True)
        if args.exercise:
            if args.exercise == "menu":
                value = await_status(lambda s:s["width"] > 0, "Application viewport")
                width, height = value["width"], value["height"]
                send("swipe %d 8 %d %d" % (width // 2, width // 2, height - 24))
                value = await_status(lambda s:s["control_found"] == 1, "Native control center")
                previous_display = display("display-control-center", previous_display)
                send("settings")
                value = await_status(lambda s:s["menu_attached"] == 1 and s["menu_loads"] > 0, "Appended settings row")
                send("entry")
                time.sleep(1)
                send("dump")
                time.sleep(0.6)
                (evidence / "menu-tree.txt").write_text(shell("cat " + shlex.quote(remote + "/tree.txt"), read_only=True))
                previous_display = display("display-settings-entry", previous_display)
            for cycle in range(1, args.cycles + 1):
                if args.exercise == "menu":
                    # Native list bottom padding centers the appended row here.
                    send("tap %d %d" % (width // 2, height - 56))
                else:
                    send("panel")
                value = await_status(lambda s:s["panel_visible"] == 1, "Open panel")
                previous_display = display("display-panel-%d" % cycle, previous_display)
                width, height = value["width"], value["height"]
                send("tap %d %d" % (width // 2, height // 2 + 20))
                value = await_status(lambda s:s["clicks"] == cycle, "Native test-button touch")
                previous_display = display("display-clicked-%d" % cycle, previous_display)
                send("tap %d %d" % (width // 2, height - 52))
                value = await_status(lambda s:s["panel_visible"] == 0 and s["closes"] == cycle, "Native return-button touch")
                previous_display = display("display-returned-%d" % cycle, previous_display)
                if args.exercise == "menu" and cycle < args.cycles:
                    # Exercise the actual native back button, then recreate the
                    # settings page. This covers hook detachment and EW GC locks.
                    send("tap 36 32")
                    await_status(lambda s:s["menu_attached"] == 0, "Leave native settings page")
                    previous_display = display("display-settings-closed-%d" % cycle, previous_display)
                    send("settings")
                    await_status(lambda s:s["menu_attached"] == 1 and s["menu_loads"] > cycle,
                                 "Recreated settings page")
                    send("entry")
                    time.sleep(1)
                    previous_display = display("display-settings-reopened-%d" % cycle, previous_display)
            save("exercise.json", {"exercise": args.exercise, "event_checks_passed": True,
                                   "scanout_transitions_verified": True, "cycles": args.cycles,
                                   "settings_recreations": args.cycles - 1 if args.exercise == "menu" else 0,
                                   "physical_display_verified": False, "status": value})
            exercise_passed = True
            print("Native UI events and selected scanout pixel transitions passed.", flush=True)
            if args.hold_panel:
                send("tap %d %d" % (width // 2, height - 56))
                await_status(lambda s:s["panel_visible"] == 1, "Panel for physical observation")
                previous_display = display("display-hold-panel", previous_display)
                print("Panel remains open for %d seconds of physical observation." % args.seconds, flush=True)
        finish = time.monotonic() + args.seconds
        previous = None
        observations = 0
        last_announced = None
        last_announced_time = 0
        while time.monotonic() < finish:
            if args.mode == "native":
                display("display-native-observation-%d" % observations, progress=True)
                observations += 1
                time.sleep(1)
                continue
            value = json.loads(shell("cat " + shlex.quote(remote + "/status.json"), read_only=True))
            if value.get("error"):
                raise RuntimeError("Plugin error: " + repr(value))
            if previous and (value["pid"] != previous["pid"] or value["callback_ticks"] <= previous["callback_ticks"]):
                raise RuntimeError("GUI callback stopped progressing")
            samples.append(value)
            previous = value
            changed = tuple(value[k] for k in ("pid", "panel_visible", "clicks", "entries", "closes", "error"))
            if changed != last_announced or time.monotonic() - last_announced_time >= 5:
                print(json.dumps(value), flush=True)
                last_announced = changed
                last_announced_time = time.monotonic()
            time.sleep(1)
        end_display = display("display-end", progress=args.mode != "menu", allow_screen_off=args.hold_panel)
        observation_ended_screen_off = end_display["samples"][-1]["screens"]["rear"]["brightness"] == 0
    except BaseException as exc:
        error = exc
        save("error.json", {"error": str(exc), "activated": activated})
        print("Trial error: " + str(exc), file=sys.stderr, flush=True)
    finally:
        save("samples.json", samples)
        if staged and args.mode != "native":
            try:
                save("last-status.json", json.loads(shell("cat " + shlex.quote(remote + "/status.json"), read_only=True)))
                (evidence / "last-service.txt").write_text(shell("systemctl show gui.service -p MainPID -p ActiveState -p SubState -p ExecMainStatus -p Result"))
                (evidence / "gui-journal.txt").write_text(shell("journalctl -u gui.service -n 160 --no-pager -o short-monotonic"))
                if args.mode == "menu":
                    (evidence / "tree.txt").write_text(shell("cat " + shlex.quote(remote + "/tree.txt")))
            except Exception as exc:
                print("Journal collection failed: " + str(exc), file=sys.stderr)
        rebooted = False
        recovery_screen_off = False
        if armed:
            shell("systemctl start --no-block " + unit + ".service")
            deadline = time.monotonic() + 120
            last_phase = None
            while time.monotonic() < deadline:
                try:
                    check_code = ("import pathlib,json; p=pathlib.Path(" + repr(remote + "/recovery.json") + "); "
                                  "print(json.dumps({'boot_id':pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip(),"
                                  "'recovery':json.loads(p.read_text()) if p.exists() else None}))")
                    response = subprocess.run(adb + ["shell", "python3 -c " + shlex.quote(check_code)],
                                              capture_output=True, text=True, timeout=4)
                    check = json.loads(response.stdout)
                    rebooted = check["boot_id"] != before["boot_id"]
                    if rebooted:
                        print("Recovery reboot completed; waiting for original display services.", flush=True)
                        break
                    recovery = check.get("recovery") or {}
                    phase = recovery.get("phase")
                    if phase != last_phase:
                        print("Recovery: " + str(phase), flush=True)
                        last_phase = phase
                        save("recovery.json", recovery)
                    if phase == "media_busy":
                        raise RuntimeError("Recovery deferred because recording/media writes began; original configuration restored, recording left running")
                    if phase == "screen_off":
                        recovery_screen_off = True
                        if error is None:
                            error = RuntimeError("Original GUI restored, but rear screen is off; presentation remains unverified")
                        break
                    if phase == "restored":
                        break
                except (subprocess.TimeoutExpired, json.JSONDecodeError):
                    pass
                time.sleep(0.5)
            else:
                raise RuntimeError("Display recovery did not complete; inspect independent unit " + unit)
            if not rebooted:
                shell("systemctl stop " + unit + ".timer")
            else:
                # The runtime override/timer disappear on reboot. Services may need
                # several seconds before private display buffers exist again.
                startup_deadline = time.monotonic() + 40
                while True:
                    try:
                        after_display = display("display-after-reboot", progress=True)
                        break
                    except Exception:
                        if time.monotonic() >= startup_deadline:
                            raise
                        time.sleep(1)
        after = snapshot()
        save("after.json", after)
        if before["hashes"] != after["hashes"]:
            raise RuntimeError("Original files changed during trial")
        for service in SERVICES:
            if after["services"][service]["ActiveState"] != "active":
                raise RuntimeError("Native service did not recover: " + service)
            if not rebooted and service != "gui.service" and before["services"][service] != after["services"][service]:
                raise RuntimeError("Other native service changed during trial: " + service)
        if after["dropins"] != before["dropins"]:
            raise RuntimeError("Trial override was not fully removed")
        if staged and not rebooted:
            # After process exit no callback or worker can access the owned RAM files.
            maps = shell("cat /proc/" + after["services"]["gui.service"]["MainPID"] + "/maps")
            if remote in maps:
                raise RuntimeError("Trial plugin is still mapped; runtime files retained")
            python("import shutil; shutil.rmtree(" + repr(remote) + ")")
        if armed and not rebooted and not recovery_screen_off:
            after_display = display("display-after-restore", progress=True)
        save("result.json", {"original_process_restored": True, "physical_display_verified": False,
                             "display_recovery_verified": armed and not recovery_screen_off, "recovery_rebooted": rebooted,
                             "activated": activated, "event_checks_passed": exercise_passed,
                             "scanout_transitions_verified": exercise_passed,
                             "observation_ended_screen_off": observation_ended_screen_off,
                             "trial_completed": error is None,
                             "error": str(error) if error else None})
        if recovery_screen_off:
            print("Original configuration and GUI restored; screen is off, so display verification is pending.", flush=True)
        else:
            print("Original configuration and selected display pixels restored; physical observation remains a separate check.", flush=True)
    if error:
        raise error


if __name__ == "__main__":
    main()
