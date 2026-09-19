import { useEffect, useRef, useState } from "react";
import type Mpegts from "mpegts.js";
import {
  Camera,
  Copy,
  Download,
  Maximize,
  Play,
  RefreshCw,
  RotateCcw,
  Square,
} from "lucide-react";
import {
  apiURL,
  errorText,
  invalidate,
  post,
  responseValue,
  useAPI,
} from "../api";
import { useViewState } from "../lib/view-state";
import { MimoPreview } from "../components/mimo-preview";
import type {
  CameraPreset,
  CameraStatus,
  Config,
  NativeCameraStatus,
  NativeAction,
  NativeRecordingResult,
  NativeServiceState,
} from "../types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Card,
  ConfirmButton,
  Notice,
  PageHeader,
  Spinner,
  formatBytes,
} from "../ui";

const resolutions = [
  "320x240",
  "640x480",
  "800x600",
  "1280x720",
  "1920x1080",
  "2560x1440",
  "3840x2160",
  "7680x4320",
];
const qualityRates = [2, 6, 15, 999];

const nativeStateLabels: Record<NativeServiceState, string> = {
  running: "服务运行中",
  stopped: "服务已停止",
  failed: "服务启动失败",
  transitioning: "服务切换中",
  degraded: "部分服务未运行",
  unknown: "服务状态未知",
  unavailable: "本地模式",
};
const nativeServiceLabels: Record<string, string> = {
  "dji_camera3.service": "原生拍摄服务",
  "dji_media.service": "媒体服务",
  "gui.service": "主屏界面",
  "sub_gui.service": "副屏界面",
  "dji_sw_uav.service": "Mimo 通信服务",
};

function nativeRecordingLabel(status: NativeCameraStatus) {
  if (status.recording === true) return "录像中";
  if (status.recording === false) return "未录像";
  if (status.native_state.status === "ok")
    return `状态待识别（代码 ${status.native_state.record_state}）`;
  if (status.native_state.status === "unsupported_firmware")
    return "当前固件尚未适配";
  if (status.service_state === "unavailable") return "本地模式不可用";
  return "未知";
}

export function CameraPage() {
  const native = useAPI<NativeCameraStatus>(
    "native_camera_status",
    undefined,
    5000,
  );
  const capture = useAPI<CameraStatus>("camera_status", undefined, 3000);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [nativeAction, setNativeAction] = useState<NativeAction | null>(null);
  const [nativeResult, setNativeResult] =
    useState<NativeRecordingResult | null>(null);
  const [nativeError, setNativeError] = useState("");
  const nativeBusy = useRef(false);

  async function runNative(action: NativeAction) {
    if (nativeBusy.current) return;
    nativeBusy.current = true;
    setNativeAction(action);
    setNativeResult(null);
    setNativeError("");
    try {
      // getRandomValues also works over the camera's plain HTTP connection.
      const requestID = Array.from(
        crypto.getRandomValues(new Uint8Array(16)),
        (byte) => byte.toString(16).padStart(2, "0"),
      ).join("");
      const endpoint = action === "capture" ? "native_capture" : "native_recording";
      setNativeResult(
        await post<NativeRecordingResult>(endpoint, {
          action,
          request_id: requestID,
        }),
      );
    } catch (error) {
      setNativeError(
        `原生请求未完整返回：${errorText(error)}。请先检查相机当前状态，再决定下一步操作。`,
      );
    } finally {
      await invalidate("native_camera_status");
      nativeBusy.current = false;
      setNativeAction(null);
    }
  }

  useEffect(() => {
    if (capture.data && capture.data.state !== "stopped") setAdvancedOpen(true);
  }, [capture.data?.state]);

  return (
    <>
      <PageHeader
        title="相机"
        description="复用相机原生录像控制，并同步 Mimo 的实时画面。"
        actions={
          <Button
            variant="outline"
            disabled={native.isFetching || capture.isFetching}
            onClick={() =>
              void invalidate("native_camera_status", "camera_status")
            }
          >
            <RefreshCw />
            刷新状态
          </Button>
        }
      />
      {capture.data?.reboot_required && (
        <div className="mb-4">
          <Notice tone="warning">
            原生服务已受影响，停止独立采集后仍需整机重启恢复。
            <a
              href="#/system?section=maintenance"
              className="ml-2 underline underline-offset-4"
            >
              前往重启
            </a>
          </Notice>
        </div>
      )}
      <Card title="原生相机">
        {native.isPending ? (
          <Spinner />
        ) : native.isError ? (
          <Notice tone="warning">
            无法读取原生服务状态：{native.error.message}
          </Notice>
        ) : (
          <>
            <Badge
              variant={
                native.data.service_state === "running"
                  ? "default"
                  : "secondary"
              }
            >
              {nativeStateLabels[native.data.service_state]}
            </Badge>
            {native.data.reason && (
              <p className="mt-3 text-sm text-muted-foreground">
                {native.data.reason}
              </p>
            )}
            <dl className="mt-5 grid grid-cols-2 gap-4 text-sm">
              <div>
                <dt className="text-muted-foreground">原生录像状态</dt>
                <dd className="mt-1">{nativeRecordingLabel(native.data)}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">原生工作模式</dt>
                <dd className="mt-1">
                  {native.data.native_state.status === "ok" &&
                  native.data.native_state.workmode !== null
                    ? `代码 ${native.data.native_state.workmode}`
                    : "—"}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">网页预览</dt>
                <dd className="mt-1">下方连接 Mimo 画面</dd>
              </div>
            </dl>
            <p className="mt-3 text-xs text-muted-foreground">
              {native.data.native_state.status === "ok"
                ? `相机原生状态 · 最近读取 ${new Date(native.data.native_state.observed_at!).toLocaleTimeString()} · 每 5 秒刷新`
                : "暂时无法读取原生录像状态，稍后自动重试。"}
            </p>
            <div className="mt-5 flex flex-wrap gap-3">
              <Button
                disabled={
                  nativeAction !== null ||
                  !native.data.control_available ||
                  !native.data.recording_controls.start
                }
                onClick={() => void runNative("start_recording")}
              >
                <Play />
                {nativeAction === "start_recording"
                  ? "正在请求开始…"
                  : "开始录像"}
              </Button>
              <Button
                variant="outline"
                disabled={
                  nativeAction !== null ||
                  !native.data.control_available ||
                  !native.data.recording_controls.stop
                }
                onClick={() => void runNative("stop_recording")}
              >
                <Square />
                {nativeAction === "stop_recording"
                  ? "正在请求停止…"
                  : "停止录像"}
              </Button>
              <Button
                variant="outline"
                disabled={
                  nativeAction !== null ||
                  !native.data.control_available ||
                  !native.data.capture_controls.capture
                }
                onClick={() => void runNative("capture")}
              >
                <Camera />
                {nativeAction === "capture" ? "正在请求拍照…" : "拍照"}
              </Button>
            </div>
            <p className="mt-3 text-sm leading-6 text-muted-foreground">
              沿用相机当前拍摄设置，录像和照片均由相机保存。拍照仅在相机空闲时可用。
            </p>
            {nativeResult && (
              <div className="mt-4">
                <Notice
                  tone={
                    nativeResult.outcome === "confirmed" ||
                    nativeResult.outcome === "already"
                      ? "success"
                      : "warning"
                  }
                >
                  <span className="block">{nativeResult.message}</span>
                  <span className="mt-1 block text-xs opacity-80">
                    上次操作 ·{" "}
                    {new Date(nativeResult.completed_at).toLocaleTimeString()}
                  </span>
                  {nativeResult.reason && (
                    <details className="mt-2 text-xs">
                      <summary className="cursor-pointer">查看操作详情</summary>
                      <p className="mt-1">{nativeResult.reason}</p>
                    </details>
                  )}
                </Notice>
              </div>
            )}
            {nativeError && (
              <div className="mt-4">
                <Notice tone="warning">{nativeError}</Notice>
              </div>
            )}
            {native.data.services.length > 0 && (
              <details className="mt-5 rounded-lg border p-3">
                <summary className="cursor-pointer text-sm font-medium">
                  查看原生服务详情
                </summary>
                <dl className="mt-4 space-y-3 text-sm">
                  {native.data.services.map((service) => (
                    <div
                      key={service.unit}
                      className="flex flex-wrap justify-between gap-2"
                    >
                      <dt>
                        {nativeServiceLabels[service.unit] ?? service.unit}
                      </dt>
                      <dd className="text-muted-foreground">
                        {nativeStateLabels[service.state]}
                      </dd>
                    </div>
                  ))}
                </dl>
                <p className="mt-4 text-xs text-muted-foreground">
                  Mimo 通信服务运行不代表手机已连接。
                </p>
                {native.data.native_state.reason && (
                  <p className="mt-3 break-words text-xs text-muted-foreground">
                    录像状态读取：{native.data.native_state.reason}
                  </p>
                )}
              </details>
            )}
          </>
        )}
      </Card>
      <div className="mt-6">
        <MimoPreview
          enabled={
            native.data?.service_state === "running" &&
            capture.data?.state === "stopped" &&
            !capture.data?.reboot_required
          }
        />
      </div>
      <details
        open={advancedOpen}
        onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}
        className="mt-6 rounded-xl border bg-card p-5"
      >
        <summary className="cursor-pointer font-medium">高级：独立采集</summary>
        <p className="mt-3 text-sm leading-6 text-muted-foreground">
          独立采集会中断原生拍摄、屏幕和 Mimo 连接，恢复原生功能需要重启相机。
          展开此处只查看设置；启动时才会接管相机。
        </p>
        {advancedOpen && (
          <div className="mt-5">
            <IndependentCapture />
          </div>
        )}
      </details>
    </>
  );
}

function IndependentCapture() {
  const status = useAPI<CameraStatus>("camera_status", undefined, 2000);
  const config = useAPI<Config>("config");
  const presets = useAPI<{ last_success: CameraPreset | null }>(
    "camera_presets",
  );
  const health = useAPI<{ device: boolean }>("health", undefined, 5000);
  const [draft, setDraft] = useViewState<Config | null>("camera.draft", null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const [preview, setPreview] = useState(true),
    [attempt, setAttempt] = useState(0);
  const [playback, setPlayback] = useState("预览未连接"),
    [playbackError, setPlaybackError] = useState("");
  const video = useRef<HTMLVideoElement>(null),
    form = useRef<HTMLFormElement>(null),
    address = useRef<HTMLInputElement>(null);
  const advanced = useRef<HTMLDetailsElement>(null);
  const running = status.data?.running === true;
  const hardware = health.data?.device === true && !health.isError;
  const transition =
    status.data?.state === "starting" || status.data?.state === "stopping";
  const hostname =
    location.hostname.includes(":") && !location.hostname.startsWith("[")
      ? `[${location.hostname}]`
      : location.hostname;
  const url = `tcp://${hostname}:${status.data?.ext_port ?? draft?.cam_ext_port ?? 8554}`;

  useEffect(() => {
    if (config.data) setDraft((current) => current ?? config.data!);
  }, [config.data]);
  useEffect(() => {
    if (!running || !preview) {
      setPlayback("预览未连接");
      setPlaybackError("");
      return;
    }
    const controller = new AbortController();
    let player: Mpegts.Player | undefined;
    const destroy = () => {
      controller.abort();
      if (player) {
        player.destroy();
        player = undefined;
      }
    };
    void import("mpegts.js")
      .then(async ({ default: mpegts }) => {
        if (controller.signal.aborted || !video.current) return;
        const features = mpegts.getFeatureList();
        if (!features.mseLivePlayback)
          throw new Error(
            "此浏览器不支持 MPEG-TS 实时播放，请使用外部 TCP 播放器。",
          );
        player = mpegts.createPlayer(
          {
            type: "mpegts",
            isLive: true,
            hasAudio: false,
            url: apiURL("camera_stream"),
          },
          {
            enableWorker: false,
            enableStashBuffer: false,
            liveBufferLatencyChasing: true,
            liveBufferLatencyMaxLatency: 1.5,
            liveBufferLatencyMinRemain: 0.5,
            autoCleanupSourceBuffer: true,
          },
        );
        player.on(mpegts.Events.ERROR, (kind: string, detail: string) => {
          if (!controller.signal.aborted) {
            setPlayback("预览错误");
            setPlaybackError(
              `${kind}: ${detail}。此错误不代表设备已停止采集。`,
            );
            destroy();
          }
        });
        player.attachMediaElement(video.current);
        player.load();
        try {
          await player.play();
        } catch (e) {
          if (!controller.signal.aborted) {
            setPlayback("等待手动播放");
            setPlaybackError(
              `浏览器未开始播放：${errorText(e)}。可点击视频播放按钮。`,
            );
          }
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) {
          setPlayback("预览不可用");
          setPlaybackError(errorText(e));
          destroy();
        }
      });
    return destroy;
  }, [running, preview, attempt]);

  async function control(name: string, data: unknown = {}) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await post(name, data);
      await invalidate(
        "camera_status",
        "native_camera_status",
        "config",
        "sysinfo",
        "camera_presets",
      );
    } catch (e) {
      setError(
        `${errorText(e)}。如果连接中断，先刷新采集状态，不要立即重复操作。`,
      );
      throw e;
    } finally {
      setBusy(false);
    }
  }
  function preset(patch: Partial<Config>) {
    setDraft((current) => {
      if (!current) return current;
      const next = { ...current, ...patch };
      next.cam_bitrate = Math.min(
        1000,
        Math.max(
          0.5,
          Math.round(
            qualityRates[next.cam_quality] *
              Math.max(
                0.3,
                Math.min(10, (next.cam_w * next.cam_h) / (1920 * 1080)),
              ) *
              10,
          ) / 10,
        ),
      );
      return next;
    });
  }
  async function snapshot() {
    setBusy(true);
    setError("");
    try {
      const response = await fetch(apiURL("camera_snapshot"), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      if (!response.ok) await responseValue(response);
      const object = URL.createObjectURL(await response.blob()),
        link = document.createElement("a");
      link.href = object;
      link.download = `snapshot-${new Date().toISOString().replaceAll(":", "-")}.jpg`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(object), 1000);
      setNotice("截图已交给浏览器下载。");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const knownState = Boolean(status.data) && !status.isError;
  const lastSuccess = presets.data?.last_success;
  const common = draft ? `${draft.cam_w}x${draft.cam_h}@${draft.cam_fps}` : "";
  const commonPresets = [
    {
      value: "1280x720@30",
      label: "720p · 30 fps",
      width: 1280,
      height: 720,
      bitrate: 4.4,
    },
    {
      value: "1920x1080@30",
      label: "1080p · 30 fps",
      width: 1920,
      height: 1080,
      bitrate: 6,
    },
  ];
  const captureState = status.isPending
    ? "正在读取"
    : status.isError
      ? "状态未知"
      : running
        ? "运行中"
        : transition
          ? "切换中"
          : status.data?.state === "error"
            ? "失败"
            : "已停止";

  return (
    <div className="pb-24 md:pb-0">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">独立采集</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            此处的参数和状态仅属于 Action Control 自己的采集。
          </p>
        </div>
        <Button
          variant="outline"
          onClick={() => void status.refetch()}
          disabled={status.isFetching}
        >
          <RefreshCw />
          刷新独立采集状态
        </Button>
      </div>
      {status.isError && (
        <div className="mb-4">
          <Notice tone="warning">
            采集状态未知：{status.error.message}。恢复连接后请先确认状态。
          </Notice>
        </div>
      )}
      {(error || status.data?.error) && (
        <div className="mb-4">
          <Notice tone="error">{error || status.data?.error}</Notice>
        </div>
      )}
      {notice && (
        <div className="mb-4">
          <Notice tone="success">{notice}</Notice>
        </div>
      )}
      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_320px]">
        <Card title="实时预览">
          <div className="mb-4 flex flex-wrap items-center gap-2">
            <Badge
              variant={running && !status.isError ? "default" : "secondary"}
            >
              独立采集：{captureState}
            </Badge>
            <Badge variant="secondary">浏览器：{playback}</Badge>
            {running && !status.isError && (
              <span className="text-xs tabular-nums text-muted-foreground">
                {Math.floor(status.data!.uptime_sec / 60)} 分{" "}
                {Math.floor(status.data!.uptime_sec % 60)} 秒
              </span>
            )}
          </div>
          <div className="relative flex aspect-video items-center justify-center overflow-hidden rounded-lg bg-zinc-950">
            <video
              ref={video}
              className="h-full w-full object-contain"
              controls={running && preview}
              muted
              playsInline
              autoPlay
              onPlaying={() => {
                setPlayback("正在播放");
                setPlaybackError("");
              }}
              onPause={() => {
                if (running && preview) setPlayback("预览暂停");
              }}
              onWaiting={() => {
                if (running && preview) setPlayback("预览缓冲中");
              }}
              onError={() => {
                if (running && preview)
                  setPlaybackError(
                    video.current?.error?.message || "浏览器无法解码当前视频。",
                  );
              }}
            />
            {(!running || !preview) && (
              <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center gap-3 text-zinc-400">
                <Camera className="size-10" />
                <p className="text-sm">
                  {status.isError
                    ? "等待设备状态恢复"
                    : running
                      ? "预览已关闭 · 设备继续采集"
                      : "启动采集后显示画面"}
                </p>
              </div>
            )}
          </div>
          <div className="fixed inset-x-0 bottom-0 z-30 flex flex-wrap items-center gap-2 border-t bg-background/95 px-4 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] shadow-lg backdrop-blur md:static md:mt-4 md:border-0 md:bg-transparent md:p-0 md:shadow-none [&_button]:min-h-11">
            <ConfirmButton
              variant="default"
              disabled={
                !hardware ||
                !knownState ||
                !draft ||
                config.isError ||
                busy ||
                running ||
                transition
              }
              title="接管相机并启动独立采集？"
              description="将停止原生相机、屏幕和 Mimo 通信服务。即使启动失败或随后停止，仍可能需要整机重启才能恢复原生功能。"
              onConfirm={async () => {
                if (!draft || !form.current)
                  throw new Error("采集参数尚未就绪。");
                if (!form.current.checkValidity()) {
                  if (advanced.current) advanced.current.open = true;
                  form.current.reportValidity();
                  throw new Error("请修正采集参数。");
                }
                await control("camera_start", {
                  width: draft.cam_w,
                  height: draft.cam_h,
                  fps: draft.cam_fps,
                  ext_port: draft.cam_ext_port,
                  bitrate: draft.cam_bitrate,
                  quality: draft.cam_quality,
                  confirm: true,
                  takeover_native: true,
                });
              }}
            >
              <Play />
              启动独立采集
            </ConfirmButton>
            <Button
              variant="outline"
              disabled={
                !hardware ||
                !knownState ||
                busy ||
                transition ||
                status.data?.state === "stopped"
              }
              onClick={() => void control("camera_stop").catch(() => {})}
            >
              <Square />
              停止独立采集
            </Button>
            <span className="ml-auto text-xs text-muted-foreground md:ml-1">
              {!hardware ? "未连接相机" : captureState}
            </span>
          </div>
          {playbackError && (
            <div className="mt-4">
              <Notice tone="warning">{playbackError}</Notice>
            </div>
          )}
          <div className="mt-4 flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={!running}
              onClick={() => {
                setPreview(true);
                setAttempt((n) => n + 1);
              }}
            >
              <Play />
              {preview && playback === "正在播放" ? "重新连接" : "连接预览"}
            </Button>
            <Button
              variant="ghost"
              disabled={!running || !preview}
              onClick={() => setPreview(false)}
            >
              关闭预览
            </Button>
            <Button
              variant="outline"
              disabled={!running || busy || !hardware}
              onClick={() => void snapshot()}
            >
              <Download />
              截图
            </Button>
            <Button
              variant="ghost"
              disabled={!running || !preview}
              onClick={() => {
                if (video.current?.requestFullscreen)
                  void video.current
                    .requestFullscreen()
                    .catch((cause) => setError(errorText(cause)));
                else setError("此浏览器不支持视频全屏。");
              }}
            >
              <Maximize />
              全屏
            </Button>
          </div>
          <dl className="mt-5 grid grid-cols-2 gap-4 border-t pt-5 text-sm sm:grid-cols-4">
            <div>
              <dt className="text-muted-foreground">采集参数</dt>
              <dd className="mt-1 font-mono">
                {status.data
                  ? `${status.data.width} × ${status.data.height}`
                  : "—"}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">配置帧率</dt>
              <dd className="mt-1 font-mono">{status.data?.fps ?? "—"} fps</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">平均输出码率</dt>
              <dd className="mt-1 font-mono">
                {running
                  ? `${(status.data!.bitrate_kbps / 1000).toFixed(2)} Mbps`
                  : "—"}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">输出 / 客户端</dt>
              <dd className="mt-1 font-mono">
                {formatBytes(status.data?.bytes_sent)} /{" "}
                {status.data?.clients ?? "—"}
              </dd>
            </div>
          </dl>
        </Card>
        <Card
          title="采集参数"
          description="先选择常用组合，需要时再调整详细参数。"
        >
          {config.isError ? (
            <Notice tone="error">{config.error.message}</Notice>
          ) : !draft ? (
            <Spinner />
          ) : (
            <form
              ref={form}
              onSubmit={(event) => event.preventDefault()}
              className="space-y-4"
            >
              <fieldset
                disabled={busy || running || transition}
                className="space-y-4 disabled:opacity-60"
              >
                <div>
                  <Label htmlFor="capture-profile">常用组合</Label>
                  <Select
                    value={
                      commonPresets.some((item) => item.value === common)
                        ? common
                        : "custom"
                    }
                    onValueChange={(value) => {
                      const preset = commonPresets.find(
                        (item) => item.value === value,
                      );
                      if (preset)
                        setDraft({
                          ...draft,
                          cam_w: preset.width,
                          cam_h: preset.height,
                          cam_fps: 30,
                          cam_quality: 1,
                          cam_bitrate: preset.bitrate,
                        });
                      else if (advanced.current) advanced.current.open = true;
                    }}
                  >
                    <SelectTrigger id="capture-profile" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {commonPresets.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                      <SelectItem value="custom">自定义参数</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="mt-2 text-xs leading-5 text-muted-foreground">
                    可用性以本机启动结果为准。
                  </p>
                </div>
                {lastSuccess && (
                  <div className="rounded-lg border p-3 text-sm">
                    <p className="font-medium">最近成功启动</p>
                    <p className="mt-1 text-muted-foreground">
                      {lastSuccess.config.cam_w} × {lastSuccess.config.cam_h} ·{" "}
                      {lastSuccess.config.cam_fps} fps
                    </p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {new Date(lastSuccess.saved_at).toLocaleString("zh-CN")}
                      {!lastSuccess.firmware_match
                        ? " · 当前固件未确认一致，需重新验证"
                        : ""}
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="mt-3"
                      onClick={() => {
                        setDraft({ ...draft, ...lastSuccess.config });
                        setNotice(
                          "已填入上次成功启动的参数，点击启动采集后生效。",
                        );
                      }}
                    >
                      <RotateCcw />
                      使用此参数
                    </Button>
                  </div>
                )}
                <details ref={advanced} className="rounded-lg border p-3">
                  <summary className="cursor-pointer text-sm font-medium">
                    高级与实验参数
                  </summary>
                  <p className="mt-3 text-xs leading-5 text-muted-foreground">
                    未验证的分辨率、帧率组合可能无法启动。码率参考值用于调整画质，实际输出见预览下方。
                  </p>
                  <div className="mt-4 space-y-4">
                    <div>
                      <Label htmlFor="camera-preset">分辨率</Label>
                      <Select
                        value={
                          resolutions.includes(`${draft.cam_w}x${draft.cam_h}`)
                            ? `${draft.cam_w}x${draft.cam_h}`
                            : "custom"
                        }
                        onValueChange={(value) => {
                          if (value !== "custom") {
                            const [cam_w, cam_h] = value.split("x").map(Number);
                            preset({ cam_w, cam_h });
                          }
                        }}
                      >
                        <SelectTrigger id="camera-preset" className="w-full">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="custom">自定义</SelectItem>
                          {resolutions.map((value) => (
                            <SelectItem key={value} value={value}>
                              {value.replace("x", " × ")}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <Label htmlFor="camera-width">宽度</Label>
                        <Input
                          id="camera-width"
                          type="number"
                          required
                          min={320}
                          max={7680}
                          step={2}
                          value={draft.cam_w}
                          onChange={(event) =>
                            setDraft({
                              ...draft,
                              cam_w: Number(event.target.value),
                            })
                          }
                        />
                      </div>
                      <div>
                        <Label htmlFor="camera-height">高度</Label>
                        <Input
                          id="camera-height"
                          type="number"
                          required
                          min={240}
                          max={4320}
                          step={2}
                          value={draft.cam_h}
                          onChange={(event) =>
                            setDraft({
                              ...draft,
                              cam_h: Number(event.target.value),
                            })
                          }
                        />
                      </div>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <Label htmlFor="camera-fps">配置帧率</Label>
                        <Input
                          id="camera-fps"
                          type="number"
                          required
                          min={1}
                          max={240}
                          value={draft.cam_fps}
                          onChange={(event) =>
                            setDraft({
                              ...draft,
                              cam_fps: Number(event.target.value),
                            })
                          }
                        />
                      </div>
                      <div>
                        <Label htmlFor="camera-port">TCP 端口</Label>
                        <Input
                          id="camera-port"
                          type="number"
                          required
                          min={1024}
                          max={65535}
                          value={draft.cam_ext_port}
                          onChange={(event) =>
                            setDraft({
                              ...draft,
                              cam_ext_port: Number(event.target.value),
                            })
                          }
                        />
                      </div>
                    </div>
                    <div>
                      <Label htmlFor="camera-quality">画质参考</Label>
                      <Select
                        value={String(draft.cam_quality)}
                        onValueChange={(value) =>
                          preset({ cam_quality: Number(value) })
                        }
                      >
                        <SelectTrigger id="camera-quality" className="w-full">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {["较低", "一般", "较高", "极高"].map((label, i) => (
                            <SelectItem key={i} value={String(i)}>
                              {label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div>
                      <Label htmlFor="camera-bitrate">码率参考值（Mbps）</Label>
                      <Input
                        id="camera-bitrate"
                        type="number"
                        required
                        min={0.5}
                        max={1000}
                        step={0.1}
                        value={draft.cam_bitrate}
                        onChange={(event) =>
                          setDraft({
                            ...draft,
                            cam_bitrate: Number(event.target.value),
                          })
                        }
                      />
                    </div>
                  </div>
                </details>
              </fieldset>
              {running && (
                <p className="text-xs text-muted-foreground">
                  停止采集后可修改参数。
                </p>
              )}
            </form>
          )}
        </Card>
      </div>
      <details className="mt-6 rounded-xl border bg-card p-5">
        <summary className="cursor-pointer font-medium">
          外部播放器与连接帮助
        </summary>
        <div className="mt-4 space-y-4">
          <p className="text-sm text-muted-foreground">
            使用支持 TCP MPEG-TS / H.264 的播放器。视频端口仅在采集期间开放。
          </p>
          <Notice tone="warning">
            TCP 视频端口没有访问认证或加密，仅供可信局域网使用。通过 ADB
            访问时，还需转发下面的视频端口。
          </Notice>
          <div>
            <Label htmlFor="tcp-address">播放地址</Label>
            <div className="flex gap-2">
              <Input
                ref={address}
                id="tcp-address"
                readOnly
                value={url}
                className="font-mono"
                onFocus={(event) => event.currentTarget.select()}
              />
              <Button
                variant="outline"
                aria-label="复制播放地址"
                onClick={async () => {
                  try {
                    if (!navigator.clipboard)
                      throw new Error("clipboard unavailable");
                    await navigator.clipboard.writeText(url);
                    setNotice("播放地址已复制。");
                  } catch {
                    address.current?.focus();
                    address.current?.select();
                    setNotice("地址已选中，请使用系统复制菜单或 Ctrl/Cmd+C。");
                  }
                }}
              >
                <Copy />
              </Button>
            </div>
          </div>
          {["127.0.0.1", "localhost", "[::1]", "::1"].includes(
            location.hostname,
          ) && (
            <pre className="overflow-x-auto rounded-lg bg-muted p-3 text-xs">
              adb forward tcp:
              {status.data?.ext_port ?? draft?.cam_ext_port ?? 8554} tcp:
              {status.data?.ext_port ?? draft?.cam_ext_port ?? 8554}
            </pre>
          )}
        </div>
      </details>
    </div>
  );
}
