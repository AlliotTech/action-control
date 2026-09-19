"""Offline negative-path tests; never load a camera library."""

import ctypes as C
import errno
import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import Mock, patch


path = Path(__file__).resolve().parents[1] / "cmd/action-control/native_camera_reader.py"
spec = importlib.util.spec_from_file_location("native_camera_reader", path)
reader = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reader)


class FakeLibrary:
    def __init__(self, fail=None, amount=1, empty=None):
        self.calls = []
        self.fail, self.amount, self.empty = fail, amount, empty

    def call(self, name):
        self.calls.append(name)
        return -1001 if self.fail == name else 0

    def camera_manager_create(self, params, output):
        self.params = params._obj
        self.assert_null_params = not self.params.user_data and not self.params.on_disconnect
        if self.fail != "create" and self.empty != "manager":
            output._obj.value = 101
        return self.call("create")

    def camera_manager_get_camera_amount(self, manager, output):
        assert manager.value == 101
        output._obj.value = self.amount
        return self.call("amount")

    def camera_manager_connect_camera(self, manager, camera_id, output):
        assert manager.value == 101 and camera_id == 0
        if self.fail != "connect" and self.empty != "camera":
            output._obj.value = 202
        return self.call("connect")

    def camera_get_record_state(self, camera, output):
        assert camera.value == 202
        if self.empty != "record_state":
            output._obj.value = 3
        return self.call("record_state")

    def camera_get_capture_state(self, camera, output):
        assert camera.value == 202
        output._obj.value = 0
        return self.call("capture_state")

    def camera_manager_get_workmode(self, manager, output):
        assert manager.value == 101
        output._obj.value = 3
        return self.call("workmode")

    def camera_manager_destroy(self, manager):
        assert manager.value == 101
        return self.call("destroy")


class ReaderTests(unittest.TestCase):
    def test_opaque_handles_and_null_callbacks(self):
        lib = FakeLibrary()
        self.assertEqual(reader.query(lib), {
            "camera_id": 0, "camera_amount": 1, "workmode": 3, "record_state": 3, "capture_state": 0,
        })
        self.assertTrue(lib.assert_null_params)
        self.assertEqual(C.sizeof(reader.CreateParams), 16)
        self.assertEqual(lib.calls, ["create", "amount", "connect", "workmode", "record_state", "capture_state", "destroy"])

    def test_failure_never_uses_missing_handles_and_always_releases_manager(self):
        for failure in ("create", "amount", "connect", "record_state", "capture_state", "destroy"):
            with self.subTest(failure=failure):
                lib = FakeLibrary(fail=failure)
                with self.assertRaises(reader.ProbeError):
                    reader.query(lib)
                if failure == "create":
                    self.assertEqual(lib.calls, ["create"])
                else:
                    self.assertEqual(lib.calls[-1], "destroy")
                if failure in ("create", "amount", "connect"):
                    self.assertNotIn("record_state", lib.calls)

    def test_invalid_device_count_never_connects(self):
        for amount in (-1, 0, 2, 100):
            lib = FakeLibrary(amount=amount)
            with self.assertRaises(reader.ProbeError):
                reader.query(lib)
            self.assertEqual(lib.calls, ["create", "amount", "destroy"])

    def test_empty_success_outputs_remain_errors(self):
        for empty in ("manager", "camera", "record_state"):
            lib = FakeLibrary(empty=empty)
            with self.assertRaises(reader.ProbeError):
                reader.query(lib)
            if empty == "manager":
                self.assertEqual(lib.calls, ["create"])
            else:
                self.assertEqual(lib.calls[-1], "destroy")

    def test_environment_failure_precedes_native_load(self):
        with patch.object(reader, "check_environment", side_effect=reader.ProbeError("unsupported_firmware", "changed")), patch.object(reader, "bind_library") as load:
            result = reader.observe()
        self.assertEqual(result["status"], "unsupported_firmware")
        self.assertNotIn("record_state", result)
        load.assert_not_called()

    def test_changed_firmware_is_rejected_before_loading_library(self):
        def file_contents(path, *args, **kwargs):
            if path == "/build.prop":
                return io.StringIO("ro.product.name=qcs8550_ac204\n")
            return io.BytesIO(b"changed firmware")

        with patch.object(reader.sys, "platform", "linux"), patch.object(reader.platform, "machine", return_value="aarch64"), patch.object(reader.os, "geteuid", return_value=0), patch("builtins.open", side_effect=file_contents), patch.object(reader, "bind_library") as load:
            result = reader.observe()
        self.assertEqual(result["status"], "unsupported_firmware")
        load.assert_not_called()

    def test_other_reader_prevents_native_connection(self):
        with patch.object(reader, "check_environment"), patch.object(reader, "bind_library") as load, patch.object(reader.socket, "socket") as socket:
            socket.return_value.__enter__.return_value.bind.side_effect = OSError(errno.EADDRINUSE, "busy")
            result = reader.observe()
        self.assertEqual(result["status"], "busy")
        load.assert_not_called()

    def test_native_failure_never_publishes_partial_sample(self):
        lib = FakeLibrary(fail="capture_state")
        with patch.object(reader, "check_environment"), patch.object(reader, "bind_library", return_value=lib), patch.object(reader.socket, "socket"):
            result = reader.observe()
        self.assertEqual(result["status"], "error")
        self.assertNotIn("record_state", result)
        self.assertEqual(lib.calls[-1], "destroy")


class RecordingLibrary(FakeLibrary):
    def __init__(self, states, code=0, fail=None, fail_after=None):
        super().__init__(fail=fail)
        self.states = iter(states)
        self.current = (3, 0)
        self.sent = False
        self.code, self.fail_after = code, fail_after
        self.camera_start_recording = Mock(side_effect=lambda camera: self.dispatch("start", camera))
        self.camera_stop_recording = Mock(side_effect=lambda camera: self.dispatch("stop", camera))

    def dispatch(self, name, camera):
        assert camera.value == 202
        self.calls.append(name)
        self.sent = True
        if self.fail_after == "dispatch":
            raise OSError("native connection failed during dispatch")
        return self.code

    def camera_get_record_state(self, camera, output):
        assert camera.value == 202
        self.current = next(self.states, self.current)
        output._obj.value = self.current[0]
        return self.call("record_state")

    def camera_get_capture_state(self, camera, output):
        assert camera.value == 202
        output._obj.value = self.current[1]
        rc = self.call("capture_state")
        return -1001 if self.sent and self.fail_after == "read" else rc


class RecordingTests(unittest.TestCase):
    def run_action(self, lib, action):
        with patch.object(reader, "check_environment"), patch.object(reader, "bind_library", return_value=lib), patch.object(reader.socket, "socket"), patch.object(reader.time, "sleep"):
            return reader.record(action)

    def test_start_and_stop_require_observed_target_after_one_request(self):
        for action, states, call in (
            ("start_recording", [(3, 0), (0, 0), (1, 0)], "start"),
            ("stop_recording", [(1, 0), (2, 0), (3, 0)], "stop"),
        ):
            with self.subTest(action=action):
                lib = RecordingLibrary(states)
                result = self.run_action(lib, action)
                self.assertEqual(result["outcome"], "confirmed")
                self.assertEqual(result["native_code"], 0)
                self.assertEqual(result["before"]["record_state"], states[0][0])
                self.assertEqual(result["after"]["record_state"], states[-1][0])
                self.assertEqual(lib.calls.count(call), 1)
                self.assertEqual(lib.calls[-1], "destroy")
                self.assertNotIn("stop" if call == "start" else "start", lib.calls)

    def test_already_in_target_state_does_not_dispatch(self):
        for action, state in (("start_recording", (1, 0)), ("stop_recording", (3, 0))):
            lib = RecordingLibrary([state])
            result = self.run_action(lib, action)
            self.assertEqual(result["outcome"], "already")
            self.assertFalse(result["dispatched"])
            self.assertIsNone(result["native_code"])
            lib.camera_start_recording.assert_not_called()
            lib.camera_stop_recording.assert_not_called()

    def test_unknown_transition_and_capture_states_block_start(self):
        for state in [(-1, 0), (0, 0), (2, 0), (4, 0), (5, 0), (99, 0), (3, 1), (3, -1)]:
            lib = RecordingLibrary([state])
            result = self.run_action(lib, "start_recording")
            self.assertEqual(result["outcome"], "blocked")
            self.assertFalse(result["dispatched"])
            lib.camera_start_recording.assert_not_called()
            self.assertEqual(lib.calls[-1], "destroy")

    def test_unknown_recording_state_blocks_stop_but_capture_does_not(self):
        lib = RecordingLibrary([(2, 0)])
        self.assertEqual(self.run_action(lib, "stop_recording")["outcome"], "blocked")
        lib.camera_stop_recording.assert_not_called()
        lib = RecordingLibrary([(1, 1), (3, 0)])
        self.assertEqual(self.run_action(lib, "stop_recording")["outcome"], "confirmed")
        lib.camera_stop_recording.assert_called_once()

    def test_native_rejection_keeps_code_and_never_retries(self):
        lib = RecordingLibrary([(3, 0)], code=-1021)
        result = self.run_action(lib, "start_recording")
        self.assertEqual(result["outcome"], "rejected")
        self.assertEqual(result["native_code"], -1021)
        lib.camera_start_recording.assert_called_once()
        lib.camera_stop_recording.assert_not_called()

    def test_accepted_does_not_mean_recording_started(self):
        lib = RecordingLibrary([(3, 0)])
        with patch.object(reader.time, "monotonic", side_effect=[0, 3]):
            result = self.run_action(lib, "start_recording")
        self.assertEqual(result["outcome"], "accepted")
        self.assertEqual(result["after"]["record_state"], 3)
        lib.camera_start_recording.assert_called_once()
        lib.camera_stop_recording.assert_not_called()

    def test_uncertain_dispatch_read_or_cleanup_never_claims_success(self):
        for fail, fail_after in ((None, "dispatch"), (None, "read"), ("destroy", None)):
            lib = RecordingLibrary([(3, 0), (1, 0)], fail=fail, fail_after=fail_after)
            result = self.run_action(lib, "start_recording")
            self.assertEqual(result["outcome"], "unknown")
            self.assertTrue(result["dispatched"])
            self.assertIsNone(result["after"])
            self.assertEqual(lib.calls[-1], "destroy")
            lib.camera_start_recording.assert_called_once()
            lib.camera_stop_recording.assert_not_called()

    def test_read_failure_prevents_any_command(self):
        lib = RecordingLibrary([(3, 0)], fail="capture_state")
        result = self.run_action(lib, "start_recording")
        self.assertEqual(result["outcome"], "not_sent")
        self.assertFalse(result["dispatched"])
        lib.camera_start_recording.assert_not_called()
        self.assertEqual(lib.calls[-1], "destroy")

    def test_invalid_action_and_firmware_never_load_native_library(self):
        with patch.object(reader, "check_environment") as check, patch.object(reader, "bind_library") as load:
            for action in ("capture", "toggle", "camera_start_recording", "__getattr__", ""):
                self.assertEqual(reader.record(action)["outcome"], "not_sent")
            check.assert_not_called()
            load.assert_not_called()
        with patch.object(reader, "check_environment", side_effect=reader.ProbeError("unsupported_firmware", "changed")), patch.object(reader, "bind_library") as load:
            result = reader.record("start_recording")
            self.assertFalse(result["dispatched"])
            self.assertEqual(result["outcome"], "not_sent")
            load.assert_not_called()

    def test_other_native_client_blocks_command_before_loading(self):
        with patch.object(reader, "check_environment"), patch.object(reader, "bind_library") as load, patch.object(reader.socket, "socket") as socket:
            socket.return_value.__enter__.return_value.bind.side_effect = OSError(errno.EADDRINUSE, "busy")
            result = reader.record("stop_recording")
        self.assertFalse(result["dispatched"])
        self.assertEqual(result["outcome"], "not_sent")
        load.assert_not_called()


class CaptureLibrary(FakeLibrary):
    def __init__(self, states, code=0, fail=None, fail_after=None):
        super().__init__(fail=fail)
        self.states = iter(states)
        self.current = (3, 0)
        self.sent = False
        self.code, self.fail_after = code, fail_after
        self.camera_start_capture = Mock(side_effect=lambda camera: self.dispatch(camera))

    def dispatch(self, camera):
        assert camera.value == 202
        self.calls.append("capture")
        self.sent = True
        if self.fail_after == "dispatch":
            raise OSError("native connection failed during dispatch")
        return self.code

    def camera_get_record_state(self, camera, output):
        assert camera.value == 202
        self.current = next(self.states, self.current)
        output._obj.value = self.current[0]
        return self.call("record_state")

    def camera_get_capture_state(self, camera, output):
        assert camera.value == 202
        output._obj.value = self.current[1]
        rc = self.call("capture_state")
        return -1001 if self.sent and self.fail_after == "read" else rc


class CaptureTests(unittest.TestCase):
    def run_capture(self, lib):
        with patch.object(reader, "check_environment"), patch.object(reader, "bind_library", return_value=lib), patch.object(reader.socket, "socket"), patch.object(reader.time, "sleep"):
            return reader.capture()

    def test_idle_capture_is_accepted_never_confirmed(self):
        lib = CaptureLibrary([(3, 0)])
        with patch.object(reader.time, "monotonic", side_effect=[0, 3]):
            result = self.run_capture(lib)
        self.assertEqual(result["outcome"], "accepted")
        self.assertEqual(result["native_code"], 0)
        self.assertEqual(result["before"], {"record_state": 3, "capture_state": 0})
        lib.camera_start_capture.assert_called_once()
        self.assertEqual(lib.calls[-1], "destroy")

    def test_non_idle_states_block_capture(self):
        for state in [(1, 0), (2, 0), (3, 1), (0, 0), (3, -1)]:
            lib = CaptureLibrary([state])
            result = self.run_capture(lib)
            self.assertEqual(result["outcome"], "blocked")
            self.assertFalse(result["dispatched"])
            lib.camera_start_capture.assert_not_called()
            self.assertEqual(lib.calls[-1], "destroy")

    def test_native_rejection_keeps_code_and_never_retries(self):
        lib = CaptureLibrary([(3, 0)], code=-1021)
        result = self.run_capture(lib)
        self.assertEqual(result["outcome"], "rejected")
        self.assertEqual(result["native_code"], -1021)
        lib.camera_start_capture.assert_called_once()

    def test_uncertain_dispatch_read_or_cleanup_never_claims_success(self):
        for fail, fail_after in ((None, "dispatch"), (None, "read"), ("destroy", None)):
            lib = CaptureLibrary([(3, 0), (3, 0)], fail=fail, fail_after=fail_after)
            result = self.run_capture(lib)
            self.assertEqual(result["outcome"], "unknown")
            self.assertTrue(result["dispatched"])
            self.assertIsNone(result["after"])
            self.assertEqual(lib.calls[-1], "destroy")
            lib.camera_start_capture.assert_called_once()

    def test_read_failure_prevents_any_command(self):
        lib = CaptureLibrary([(3, 0)], fail="capture_state")
        result = self.run_capture(lib)
        self.assertEqual(result["outcome"], "not_sent")
        self.assertFalse(result["dispatched"])
        lib.camera_start_capture.assert_not_called()

    def test_other_native_client_blocks_capture_before_loading(self):
        with patch.object(reader, "check_environment"), patch.object(reader, "bind_library") as load, patch.object(reader.socket, "socket") as socket:
            socket.return_value.__enter__.return_value.bind.side_effect = OSError(errno.EADDRINUSE, "busy")
            result = reader.capture()
        self.assertFalse(result["dispatched"])
        self.assertEqual(result["outcome"], "not_sent")
        load.assert_not_called()


if __name__ == "__main__":
    unittest.main()
