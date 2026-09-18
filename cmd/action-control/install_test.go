package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func testPayload(t *testing.T, variant byte) string {
	t.Helper()
	directory := t.TempDir()
	// A real minimal ARM64 ELF: exit(0), with one executable PT_LOAD segment.
	data := make([]byte, 133)
	copy(data, []byte{127, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)
	binary.LittleEndian.PutUint16(data[18:], 183)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint64(data[24:], 0x400078)
	binary.LittleEndian.PutUint64(data[32:], 64)
	binary.LittleEndian.PutUint16(data[52:], 64)
	binary.LittleEndian.PutUint16(data[54:], 56)
	binary.LittleEndian.PutUint16(data[56:], 1)
	binary.LittleEndian.PutUint32(data[64:], 1)
	binary.LittleEndian.PutUint32(data[68:], 5)
	binary.LittleEndian.PutUint64(data[80:], 0x400000)
	binary.LittleEndian.PutUint64(data[96:], uint64(len(data)))
	binary.LittleEndian.PutUint64(data[104:], uint64(len(data)))
	binary.LittleEndian.PutUint64(data[112:], 4096)
	binary.LittleEndian.PutUint32(data[120:], 0xd2800000)
	binary.LittleEndian.PutUint32(data[124:], 0xd2800ba8)
	binary.LittleEndian.PutUint32(data[128:], 0xd4000001)
	data[132] = variant
	manifest := payloadManifest{Schema: 1, Version: Version}
	for _, name := range []string{"action-control", "bin/ffmpeg", "bin/ffprobe"} {
		putTestFile(t, filepath.Join(directory, name), data, 0755)
		manifest.Files = append(manifest.Files, payloadFile{Path: name, SHA256: bytesDigest(data), Size: int64(len(data)), Mode: 0755})
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	putTestFile(t, filepath.Join(directory, "manifest.json"), b, 0600)
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
func testInstallation(t *testing.T) (*App, *installState, string) {
	t.Helper()
	a, err := appAt(testFilesystem(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.cancel)
	payload := testPayload(t, 0)
	if err = installNew(a, payload); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(a)
	if err != nil {
		t.Fatal(err)
	}
	// Offline preparation never starts services. Materialize its unit records to
	// exercise activated restoration without running anything on the host.
	for i, entry := range state.Owned {
		if strings.HasPrefix(entry.Path, AppDir+"/") {
			continue
		}
		if entry.Kind == "link" {
			if err = os.Symlink(entry.Target, a.Path(entry.Path)); err != nil {
				t.Fatal(err)
			}
		} else {
			putTestFile(t, a.Path(entry.Path), unitContent(filepath.Base(entry.Path)), os.FileMode(entry.Mode))
		}
		state.Owned[i], err = describePath(a, entry.Path)
		if err != nil {
			t.Fatal(err)
		}
	}
	state.Activated = true
	state.Phase = "active"
	if err = writeState(a, state); err != nil {
		t.Fatal(err)
	}
	return a, state, payload
}

func TestInstallerRestoresBaselineAndPreservesMedia(t *testing.T) {
	a, state, _ := testInstallation(t)
	before := slices.Clone(state.Backups)
	if err := saveCameraPreset(a, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	presetBefore, err := os.ReadFile(filepath.Join(a.Dir, cameraPresetFile))
	if err != nil {
		t.Fatal(err)
	}
	putTestFile(t, filepath.Join(a.Dir, "background.image"), []byte("legacy background"), 0600)
	putTestFile(t, filepath.Join(a.Dir, ".background-interrupted"), []byte("legacy partial upload"), 0600)
	for _, entry := range []struct {
		path string
		data []byte
	}{{knownPath, []byte("[]\n")}, {"/etc/resolv.conf", []byte("nameserver 198.51.100.1\n")}, {"/data/misc/wifi/wpa_supplicant.conf", []byte("managed WPA\n")}, {bootScriptPath, []byte("#!/bin/sh\necho stock\n" + bootHook)}} {
		if err := writeManagedShared(a, entry.path, entry.data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if target, err := os.Readlink(a.Path("/etc/resolv.conf")); err != nil || target != "/run/native/resolv.conf" {
		t.Fatal("managed DNS replaced symlink", target, err)
	}
	if err := os.Remove(a.RunDir); err != nil {
		t.Fatal(err)
	}
	if err := updateApp(a, testPayload(t, 1), false); err != nil {
		t.Fatal(err)
	}
	if presetAfter, err := os.ReadFile(filepath.Join(a.Dir, cameraPresetFile)); err != nil || !bytes.Equal(presetAfter, presetBefore) {
		t.Fatal("update lost the successful camera preset", err)
	}
	after, err := loadState(a)
	if err != nil || !reflect.DeepEqual(after.Backups, before) {
		t.Fatal("update changed baseline", err)
	}
	if err = uninstallApp(a, false); err == nil {
		t.Fatal("active uninstall allowed without reboot consent")
	}
	if err = uninstallApp(a, true); err != nil {
		t.Fatal(err)
	}
	if err = verifyInstallation(a, true); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		path string
		data []byte
	}{{"/run/native/resolv.conf", []byte("nameserver 192.0.2.53\n")}, {"/data/misc/wifi/wpa_supplicant.conf", []byte("original WPA\n")}, {bootScriptPath, []byte("#!/bin/sh\necho stock\n")}, {"/mnt/media_rw/sd/用户媒体.bin", []byte{0, 255, 13, 10, 10}}} {
		got, err := os.ReadFile(a.Path(entry.path))
		if err != nil || !bytes.Equal(got, entry.data) {
			t.Fatal("restoration/media mismatch", entry.path, err)
		}
	}
	if _, err = os.Stat(a.Path(knownPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("originally absent configuration remains", err)
	}
	if st, err := os.Stat(a.Path("/data/misc/wifi/wpa_supplicant.conf")); err != nil || st.Mode().Perm() != 0640 {
		t.Fatal("original permissions not restored", err)
	}
	if target, err := os.Readlink(a.Path("/etc/resolv.conf")); err != nil || target != "/run/native/resolv.conf" {
		t.Fatal("DNS link not restored", err)
	}
}

func TestUninstallRefusesDamagedEvidence(t *testing.T) {
	for _, damage := range []string{"record", "backup", "unit"} {
		t.Run(damage, func(t *testing.T) {
			a, state, _ := testInstallation(t)
			switch damage {
			case "record":
				if err := os.Remove(filepath.Join(a.Dir, stateFile)); err != nil {
					t.Fatal(err)
				}
			case "backup":
				for _, entry := range state.Backups {
					if entry.Backup != "" {
						putTestFile(t, filepath.Join(a.Dir, entry.Backup), []byte("damaged"), 0600)
						break
					}
				}
			case "unit":
				putTestFile(t, a.Path(unitPath(ServiceName)), []byte("external edit"), 0644)
			}
			if err := uninstallApp(a, true); err == nil {
				t.Fatal("destructive uninstall accepted damaged", damage)
			}
			if _, err := os.Stat(filepath.Join(a.Dir, "action-control")); err != nil {
				t.Fatal("program removed despite failed evidence", err)
			}
			if got, err := os.ReadFile(a.Path("/data/misc/wifi/wpa_supplicant.conf")); err != nil || string(got) != "original WPA\n" {
				t.Fatal("shared data changed during refusal", err)
			}
		})
	}
}

func TestUpdateRefusalAllowsExplicitRetry(t *testing.T) {
	a, state, payload := testInstallation(t)
	unit := unitPath(NetworkServiceName)
	putTestFile(t, a.Path(unit), append(unitContent(NetworkServiceName), []byte("# earlier release\n")...), 0644)
	for i, entry := range state.Owned {
		if entry.Path == unit {
			var err error
			state.Owned[i], err = describePath(a, unit)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writeState(a, state); err != nil {
		t.Fatal(err)
	}
	if err := updateApp(a, payload, false); err == nil {
		t.Fatal("network unit update did not require reboot consent")
	}
	if err := updateApp(a, payload, true); err != nil {
		t.Fatalf("refused update stranded staging and prevents explicit retry: %v", err)
	}
}

func TestInterruptedUpdateRefusesForeignOverwrite(t *testing.T) {
	a, state, _ := testInstallation(t)
	name := AppDir + "/action-control"
	old, err := describePath(a, name)
	if err != nil {
		t.Fatal(err)
	}
	old.Backup = ".update/" + bytesDigest([]byte(name)) + ".bin"
	original, err := os.ReadFile(a.Path(name))
	if err != nil {
		t.Fatal(err)
	}
	putTestFile(t, filepath.Join(a.Dir, old.Backup), original, 0600)
	state.Update = &updateRecord{Phase: "replacing", OldVersion: state.Version, OldOwned: slices.Clone(state.Owned), NewOwned: slices.Clone(state.Owned), Rollback: []pathRecord{old}}
	state.Phase = "updating"
	if err = writeState(a, state); err != nil {
		t.Fatal(err)
	}
	putTestFile(t, a.Path(name), []byte("unrecorded external replacement"), 0755)
	if err = rollbackUpdate(a, state); err == nil {
		t.Fatal("rollback silently overwrote unknown data")
	}
	if got, err := os.ReadFile(a.Path(name)); err != nil || string(got) != "unrecorded external replacement" {
		t.Fatal("foreign replacement was changed", err)
	}
}

func TestInterruptedUpdateRecoveryPhases(t *testing.T) {
	for _, phase := range []string{"preparing", "replacing", "complete"} {
		t.Run(phase, func(t *testing.T) {
			a, state, _ := testInstallation(t)
			name := AppDir + "/action-control"
			old, err := describePath(a, name)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(a.Path(name))
			if err != nil {
				t.Fatal(err)
			}
			replacement := append(slices.Clone(original), 42)
			update := &updateRecord{Phase: phase, OldVersion: state.Version, OldOwned: slices.Clone(state.Owned), NewOwned: slices.Clone(state.Owned)}
			old.Backup = ".update/" + bytesDigest([]byte(name)) + ".bin"
			update.Rollback = []pathRecord{old}
			for i, entry := range update.NewOwned {
				if entry.Path == name {
					update.NewOwned[i].SHA256 = bytesDigest(replacement)
				}
			}
			if err = os.Mkdir(filepath.Join(a.Dir, ".update"), 0700); err != nil {
				t.Fatal(err)
			}
			if phase == "replacing" {
				putTestFile(t, filepath.Join(a.Dir, old.Backup), original, 0600)
			}
			if phase != "preparing" {
				putTestFile(t, a.Path(name), replacement, 0755)
			}
			if phase == "complete" {
				state.Owned = update.NewOwned
			}
			state.Update = update
			state.Phase = "updating"
			if err = writeState(a, state); err != nil {
				t.Fatal(err)
			}
			if err = rollbackUpdate(a, state); err != nil {
				t.Fatal(err)
			}
			expected := original
			if phase == "complete" {
				expected = replacement
			}
			actual, err := os.ReadFile(a.Path(name))
			if err != nil || !bytes.Equal(actual, expected) {
				t.Fatal("wrong version recovered", err)
			}
			if _, err = os.Stat(filepath.Join(a.Dir, ".update")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed recovery prevents next update", err)
			}
			if err = verifyInstallation(a, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRebootMarkerSurvivesServiceRestartNotBoot(t *testing.T) {
	a, _ := testApplication(t)
	if err := a.MarkRebootRequired("wireless", false); err != nil {
		t.Fatal(err)
	}
	if !a.RebootRequired() || a.ScreenRequired() {
		t.Fatal("wireless operation incorrectly requires screen mask")
	}
	other, err := NewApp(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.cancel()
	if err = other.MarkRebootRequired("camera", true); err != nil {
		t.Fatal(err)
	}
	if err = a.MarkRebootRequired("wireless again", false); err != nil {
		t.Fatal(err)
	}
	if !a.RebootRequired() || !a.ScreenRequired() {
		t.Fatal("service restart or network operation lost camera marker")
	}
	putTestFile(t, a.Path("/proc/sys/kernel/random/boot_id"), []byte("second-boot"), 0600)
	if a.RebootRequired() || a.ScreenRequired() {
		t.Fatal("stale marker carried into new boot")
	}
}
