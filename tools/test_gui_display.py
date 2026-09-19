"""Regression checks for display evidence that previously missed a frozen screen."""
import copy
import struct
import unittest
import zlib
from unittest import mock

import gui_display
import gui_trial_guard


class DisplayEvidenceTests(unittest.TestCase):
    def observation(self):
        first = {
            "screens": {"rear": {"active": True, "brightness": 178, "retire_ns": 100, "underrun": 0}},
            "gui": {"gui.service": {"pid": 1234, "ew_update_cycle": 10,
                                    "capture": {"stable": True, "sha256": "a" * 64}}},
            "gui_scanout_matches": True,
        }
        last = copy.deepcopy(first)
        last["screens"]["rear"]["retire_ns"] = 200
        return {"samples": [first, last]}

    def test_active_callback_is_not_proof_of_display_progress(self):
        value = self.observation()
        value["samples"][-1]["gui"]["gui.service"]["ew_update_cycle"] += 20
        value["samples"][-1]["screens"]["rear"]["retire_ns"] = 100
        with self.assertRaisesRegex(ValueError, "retirement did not advance"):
            gui_display.verify_observation(value, require_progress=True, require_pixels=True)

    def test_static_gui_can_be_displayed_without_repainting(self):
        checks = gui_display.verify_observation(self.observation(), True, True)
        self.assertTrue(checks["gui_scanout_matches"])
        self.assertFalse(checks["physical_display_verified"])

    def test_pixels_must_be_in_the_selected_scanout(self):
        value = self.observation()
        value["samples"][0]["gui_scanout_matches"] = False
        value["samples"][-1]["gui_scanout_matches"] = False
        with self.assertRaisesRegex(ValueError, "selected scanout"):
            gui_display.verify_observation(value, True, True)

    def test_buffer_flip_can_use_an_identical_verified_sample(self):
        value = self.observation()
        value["samples"][-1]["gui_scanout_matches"] = False
        checks = gui_display.verify_observation(value, True, True)
        self.assertEqual(checks["matching_sample_indices"], [0])
        # An older picture must never verify a newly drawn menu.
        value["samples"][-1]["gui"]["gui.service"]["capture"]["sha256"] = "b" * 64
        with self.assertRaisesRegex(ValueError, "selected scanout"):
            gui_display.verify_observation(value, True, True)

    def test_screen_off_and_process_restart_are_not_success(self):
        value = self.observation()
        value["samples"][-1]["screens"]["rear"]["brightness"] = 0
        with self.assertRaisesRegex(ValueError, "screen is off"):
            gui_display.verify_observation(value)
        value = self.observation()
        value["samples"][-1]["gui"]["gui.service"]["pid"] += 1
        with self.assertRaisesRegex(ValueError, "GUI restarted"):
            gui_display.verify_observation(value)

    def test_plane_parser_does_not_absorb_connector_tail(self):
        state = ("plane[158]: plane-15\n\tcrtc=crtc-0\n\tfb=299\n\t\tformat=AR24 little-endian\n"
                 "plane[170]: plane-19\n\tcrtc=(null)\n\tfb=0\n"
                 "crtc[173]: crtc-0\n\tactive=1\nconnector[61]: DSI-1\n\tcrtc=crtc-0\n")
        self.assertEqual(gui_display.drm_planes(state), [
            {"plane_id": 158, "fb_id": 299, "crtc": "crtc-0", "format": "AR24"}])

    def test_pixel_bounds_padding_and_rotation(self):
        self.assertEqual(gui_display.packed_pixels(b"abcd----efgh----", 8, 1, 2), b"abcdefgh")
        with self.assertRaises(ValueError):
            gui_display.packed_pixels(b"abc", 4, 1, 1)
        png = gui_display.png_from_bgra(bytes([1, 2, 3, 4, 5, 6, 7, 8]), 2, 1)
        at, compressed = 8, b""
        while at < len(png):
            size = struct.unpack_from(">I", png, at)[0]
            if png[at + 4:at + 8] == b"IDAT":
                compressed += png[at + 8:at + 8 + size]
            at += 12 + size
        self.assertEqual(zlib.decompress(compressed), bytes([0, 7, 6, 5, 8, 0, 3, 2, 1, 4]))

    def test_recording_detection_does_not_depend_on_process_name(self):
        def paths(pattern):
            return ["/proc/12"] if pattern == "/proc/[0-9]*" else ["/proc/12/fd/6", "/proc/12/fd/7"]

        def fdinfo(path):
            return "flags:\t0100002\n" if str(path).endswith("/6") else "flags:\t0100000\n"

        with mock.patch.object(gui_trial_guard.glob, "glob", side_effect=paths), \
                mock.patch.object(gui_trial_guard.os, "readlink", return_value="/mnt/media_rw/sd/DCIM/test.MP4"), \
                mock.patch.object(gui_trial_guard.pathlib.Path, "read_text", autospec=True, side_effect=fdinfo):
            writes = gui_trial_guard.writable_media()
        self.assertEqual(writes, [{"pid": 12, "file": "/mnt/media_rw/sd/DCIM/test.MP4"}])


if __name__ == "__main__":
    unittest.main()
