export interface Config {
  schema: number;
  version: string;
  theme_mode: "auto" | "light" | "dark";
  auto_connect: boolean;
  thumb_concurrent: number;
  cam_w: number;
  cam_h: number;
  cam_fps: number;
  cam_ext_port: number;
  cam_quality: number;
  cam_bitrate: number;
}
export interface SystemInfo {
  uptime_sec: number | null;
  kernel: string;
  cpu_percent: number | null;
  mem_total: number | null;
  mem_used: number | null;
  battery_percent: number | null;
  battery_status: string;
  battery_temp_c: number | null;
  battery_current_ma: number | null;
  cpu_temp_c: number | null;
  device: boolean;
  reboot_required: boolean;
}
export interface CameraStatus {
  state: "stopped" | "starting" | "running" | "stopping" | "error";
  running: boolean;
  width: number;
  height: number;
  fps: number;
  ext_port: number;
  codec: string;
  bitrate_kbps: number;
  bytes_sent: number;
  uptime_sec: number;
  clients: number;
  reboot_required: boolean;
  error?: string;
}
export interface CameraPreset {
  config: Pick<
    Config,
    | "cam_w"
    | "cam_h"
    | "cam_fps"
    | "cam_ext_port"
    | "cam_quality"
    | "cam_bitrate"
  >;
  saved_at: string;
  firmware_match: boolean;
}
export interface FileItem {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mtime: number;
}
export interface FileListing {
  items: FileItem[];
  path: string;
  parent: string;
  crumbs: { name: string; path: string }[];
  warnings: string[];
}
export interface MediaItem {
  name: string;
  path: string;
  size: number;
  mtime: number;
  type: "image" | "video";
  ext: string;
}
export interface MediaListing {
  items: MediaItem[];
  count: number;
  next_cursor: string;
  truncated?: boolean;
  warnings: string[];
}
export interface MediaInfo {
  width: number;
  height: number;
  codec: string;
  fps?: number;
  duration_sec?: number;
  bitrate_bps?: number;
  size: number;
}
export interface MediaIndexStatus {
  storage: "sd" | "emulated";
  available: boolean;
  database: string;
  database_bytes: number;
  total: number;
  stale: number;
  invalid: number;
  by_type: Record<string, number>;
  reason?: string;
}
export interface DiskUsage {
  path: string;
  total: number;
  used: number;
  free: number;
  pct: number;
}
export interface WiFiStatus {
  mode: "client" | "hotspot" | "closed" | "native" | "unknown";
  state: string;
  ssid: string;
  ip: string;
  signal: number | null;
  tx_bitrate_mbps: number | null;
  rx_bitrate_mbps: number | null;
  freq_mhz: number | null;
  operation?: {
    id: string;
    status: "queued" | "running" | "succeeded" | "failed";
    action: string;
    error?: string;
    recovery_status?: "restoring" | "restored" | "failed" | "unavailable";
    recovery_mode?: string;
    recovery_ssid?: string;
  };
}
export interface WiFiNetwork {
  ssid: string;
  bssid: string;
  signal: number | null;
  freq: number;
  band: "2.4G" | "5G" | "6G";
  encrypted: boolean;
}
export interface KnownNetwork {
  ssid: string;
  bssid?: string;
  last_connected?: string;
  has_password: boolean;
}
export interface HotspotConfig {
  ssid: string;
  band: "2.4G" | "5G";
  channel: number;
  open: boolean;
  has_password: boolean;
}
export interface ProcessInfo {
  pid: number;
  ppid: number;
  name: string;
  cmd: string;
  user: string;
  rss_bytes: number;
  vsz_bytes: number;
  cpu_percent: number | null;
  start_time: string;
}
