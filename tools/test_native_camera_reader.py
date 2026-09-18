"""Offline negative-path tests; never load a camera library."""

import ctypes as C
import errno
import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import patch


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

    def camera_manager_destroy(self, manager):
        assert manager.value == 101
        return self.call("destroy")


class ReaderTests(unittest.TestCase):
    def test_opaque_handles_and_null_callbacks(self):
        lib = FakeLibrary()
        self.assertEqual(reader.query(lib), {
            "camera_id": 0, "camera_amount": 1, "record_state": 3, "capture_state": 0,
        })
        self.assertTrue(lib.assert_null_params)
        self.assertEqual(C.sizeof(reader.CreateParams), 16)
        self.assertEqual(lib.calls, ["create", "amount", "connect", "record_state", "capture_state", "destroy"])

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


if __name__ == "__main__":
    unittest.main()
