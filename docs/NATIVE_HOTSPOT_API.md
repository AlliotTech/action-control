# Native hotspot API

`POST /api/native_hotspot` controls the camera's native SoftAP without enabling Action Control's managed-network mode.

Request:

```json
{"action":"start"}
```

or:

```json
{"action":"stop"}
```

The endpoint is available only on the verified ARM64 AC204 firmware. The backend checks the camera model and the SHA-256 of `/usr/bin/dji_network` before calling its native Wi-Fi manager through an isolated Python process. A firmware mismatch returns `unsupported_firmware`; it never guesses symbol addresses.

A successful start returns the raw native return code and read-back state, including `role=AP`, `operstate=up`, and the configured SSID. The native service starts its own hostapd/DHCP path; Action Control does not start a second network stack. The camera AP uses `192.168.2.1` on the verified firmware, so a phone can open `http://192.168.2.1:8080` after joining it.

The operation changes `wlan0` and can disconnect the current browser. HTTP has no authentication or encryption; use only over a trusted direct connection. Stop the hotspot before handing control back to other network workflows.

## Keepalive

Left alone, `dji_network`'s adaptive low-power logic tears the SoftAP down after about 60 seconds without an active DJI App / Bluetooth session (`feature_lowpower_sw_adaptive_timer: because APP disconnect timeout, Stop wifi`), deauthenticating any connected client. After a successful `start`, the backend runs a background loop that resets that native timer every 25 seconds (`feature_lowpower_sw_adaptive_timer_reset`, zero-arg, sets an activity flag) so the AP stays up for browser use. `stop` cancels the loop before tearing the AP down. If the Action Control service restarts, the loop stops and the AP drops on the native timeout — a failsafe, not an orphan.
