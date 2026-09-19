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

## Join / console QR codes

`GET /api/hotspot_qr?kind=wifi|url` returns a QR module matrix the rear-GUI panel paints as rectangles (the EW framework has no bitmap loader). Encoding is the real go-qrcode algorithm; the plugin only draws the grid.

- `kind=wifi` reads the SoftAP SSID/password from `/data/misc/wifi/user_config.conf` and encodes the standard join payload `WIFI:S:<ssid>;T:WPA;P:<pass>;;` (`T:nopass` with an empty password; `\ ; , : "` are backslash-escaped). A phone that scans it joins the AP automatically.
- `kind=url` encodes the fixed console address `http://192.168.2.1:8080`.

Response:

```json
{"size":41,"matrix":["0101…","…"]}
```

`size` is the square module count including the 4-module quiet zone; each `matrix` row is `size` characters of `0` (light) / `1` (dark). The Wi-Fi response omits the plaintext payload so the password never lands in logs — the matrix itself is the code. Both codes are verified scannable from the 712×400 rear scanout at 7 px per module.
