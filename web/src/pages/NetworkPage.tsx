import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  KeyRound,
  Radio,
  RefreshCw,
  Save,
  Signal,
  Trash2,
  Wifi,
  WifiOff,
} from "lucide-react";
import { errorText, get, invalidate, post, useAPI } from "../api";
import { useViewState } from "../lib/view-state";
import type {
  Config,
  HotspotConfig,
  KnownNetwork,
  WiFiNetwork,
  WiFiStatus,
} from "../types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
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
  EmptyState,
  Modal,
  Notice,
  PageHeader,
  Spinner,
} from "../ui";

type NetworkRow = {
  ssid: string;
  bssid: string;
  signal: number | null;
  band?: string;
  encrypted: boolean;
  saved: boolean;
  connected: boolean;
  lastConnected?: string;
};

const stateNames: Record<string, string> = {
  connected: "已连接",
  disconnected: "未连接",
  down: "无线已关闭",
  unknown: "状态未知",
};

export function NetworkPage() {
  const health = useAPI<{ device: boolean; http_port: string }>(
    "health",
    undefined,
    5000,
  );
  const status = useAPI<WiFiStatus>("wifi_status", undefined, 3000);
  const config = useAPI<Config>("config");
  const managed = config.data?.network_control === "managed";
  const hardware = health.data?.device === true && !health.isError;
  const canChange =
    managed && hardware && Boolean(status.data) && !status.isError;
  const httpPort = health.data?.http_port || "8080";
  const hotspotAddress = `http://192.168.2.1:${httpPort}/#/network`;
  const [reconnect, setReconnect] = useViewState<{
    mode: "client" | "hotspot" | "off";
    ssid: string;
    address: string;
    previous: string;
    time: string;
  } | null>("network.reconnect.v2", null);
  const known = useQuery<{ networks: KnownNetwork[] }>({
    queryKey: ["known_list"],
    queryFn: ({ signal }) => get("known_list", undefined, signal),
    enabled: managed,
  });
  const hotspot = useQuery<HotspotConfig>({
    queryKey: ["hotspot_config"],
    queryFn: ({ signal }) => get("hotspot_config", undefined, signal),
    enabled: managed,
  });
  const queryClient = useQueryClient();
  const scan = useQuery<{ networks: WiFiNetwork[] }>({
    queryKey: ["wifi_scan"],
    queryFn: ({ signal }) => get("wifi_scan", undefined, signal),
    enabled: false,
    retry: false,
  });
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [connect, setConnect] = useState(false);
  const [ssid, setSSID] = useState("");
  const [confirmHotspot, setConfirmHotspot] = useState(false);
  const [bssid, setBSSID] = useState("");
  const [password, setPassword] = useState("");
  const [open, setOpen] = useState(false);
  const [reuse, setReuse] = useState(false);
  const [connectError, setConnectError] = useState("");
  const [apSSID, setAPSSID] = useState("");
  const [apPassword, setAPPassword] = useState("");
  const [apOpen, setAPOpen] = useState(false);
  const [band, setBand] = useState("2.4G");
  const [channel, setChannel] = useState("6");
  const [apLoaded, setAPLoaded] = useState(false);
  const [hiddenSuccess, setHiddenSuccess] = useState("");
  const previousMode = useRef<string | undefined>(undefined);

  useEffect(() => {
    if (!managed) {
      setConnect(false);
      setConfirmHotspot(false);
      setPassword("");
      setAPPassword("");
    }
  }, [managed]);

  function loadHotspot(value: HotspotConfig) {
    setAPSSID(value.ssid);
    setBand(value.band);
    setChannel(String(value.channel));
    setAPOpen(value.open);
    setAPPassword("");
  }

  useEffect(() => {
    if (hotspot.data && !apLoaded) {
      loadHotspot(hotspot.data);
      setAPLoaded(true);
    }
  }, [hotspot.data, apLoaded]);

  useEffect(() => {
    const mode = status.data?.mode;
    if (!mode) return;
    if (previousMode.current && previousMode.current !== mode) {
      queryClient.removeQueries({ queryKey: ["wifi_scan"], exact: true });
    }
    previousMode.current = mode;
  }, [status.data?.mode, queryClient]);

  useEffect(() => {
    const operation = status.data?.operation;
    if (operation?.status !== "succeeded" || hiddenSuccess === operation.id)
      return;
    const timer = window.setTimeout(() => setHiddenSuccess(operation.id), 4000);
    return () => window.clearTimeout(timer);
  }, [status.data?.operation, hiddenSuccess]);

  const operating =
    status.data?.operation?.status === "queued" ||
    status.data?.operation?.status === "running";
  const apDirty = Boolean(
    hotspot.data &&
    (apSSID !== hotspot.data.ssid ||
      band !== hotspot.data.band ||
      Number(channel) !== hotspot.data.channel ||
      apOpen !== hotspot.data.open ||
      apPassword),
  );

  const networks = useMemo<NetworkRow[]>(() => {
    const saved = new Map(
      (known.data?.networks ?? []).map((item) => [item.ssid, item]),
    );
    const rows = new Map<string, NetworkRow>();
    for (const network of scan.data?.networks ?? []) {
      const key = network.ssid || network.bssid;
      const current = rows.get(key);
      if (
        current &&
        (current.signal ?? -Infinity) >= (network.signal ?? -Infinity)
      )
        continue;
      const remembered = saved.get(network.ssid);
      rows.set(key, {
        ssid: network.ssid,
        bssid: network.bssid,
        signal: network.signal,
        band: network.band,
        encrypted: network.encrypted,
        saved: Boolean(remembered),
        connected:
          (status.data?.role ?? status.data?.mode) === "client" &&
          status.data?.ssid === network.ssid,
        lastConnected: remembered?.last_connected,
      });
    }
    for (const network of known.data?.networks ?? []) {
      if (!rows.has(network.ssid)) {
        rows.set(network.ssid, {
          ssid: network.ssid,
          bssid: network.bssid ?? "",
          signal: null,
          encrypted: network.has_password,
          saved: true,
          connected:
            (status.data?.role ?? status.data?.mode) === "client" &&
            status.data?.ssid === network.ssid,
          lastConnected: network.last_connected,
        });
      }
    }
    return [...rows.values()].sort(
      (a, b) =>
        Number(b.connected) - Number(a.connected) ||
        Number(b.saved) - Number(a.saved) ||
        (b.signal ?? -Infinity) - (a.signal ?? -Infinity) ||
        a.ssid.localeCompare(b.ssid),
    );
  }, [
    known.data?.networks,
    scan.data?.networks,
    status.data?.mode,
    status.data?.role,
    status.data?.ssid,
  ]);

  const refresh = () =>
    invalidate(
      "wifi_status",
      "known_list",
      "hotspot_config",
      "config",
      "dji_network",
    );

  async function queue(name: string, body: object = {}) {
    setBusy(true);
    setError("");
    setMessage("");
    if (name === "wifi_connect" || name === "wifi_off") {
      setReconnect({
        mode: name === "wifi_connect" ? "client" : "off",
        ssid: name === "wifi_connect" ? ssid : "关闭无线",
        address: "",
        previous: location.href,
        time: new Date().toLocaleString("zh-CN"),
      });
    }
    try {
      await post(name, { ...body, confirm: true });
      setMessage("操作已受理。连接可能暂时中断；响应中断不代表操作失败。");
      queryClient.removeQueries({ queryKey: ["wifi_scan"], exact: true });
      await refresh();
    } finally {
      setBusy(false);
    }
  }

  function showConnect(network?: NetworkRow) {
    setSSID(network?.ssid ?? "");
    setBSSID(network?.bssid ?? "");
    setOpen(network?.encrypted === false);
    setReuse(network?.saved ?? false);
    setPassword("");
    setConnectError("");
    setConnect(true);
  }

  async function applyHotspot() {
    setBusy(true);
    setError("");
    setMessage("");
    setReconnect({
      mode: "hotspot",
      ssid: apSSID,
      address: hotspotAddress,
      previous: location.href,
      time: new Date().toLocaleString("zh-CN"),
    });
    try {
      const body: Record<string, unknown> = {
        ssid: apSSID,
        band,
        channel: Number(channel),
        confirm: true,
      };
      if (apOpen) body.password = "";
      else if (apPassword) body.password = apPassword;
      await post("hotspot_apply", body);
      setAPPassword("");
      setMessage(
        "热点切换已受理。断线后请连接目标热点，再打开下面的访问地址。",
      );
      queryClient.removeQueries({ queryKey: ["wifi_scan"], exact: true });
      await refresh();
    } catch (cause) {
      setError(
        `切换未确认完成：${errorText(cause)}。请先查看连接与恢复状态，再决定是否重试。`,
      );
    } finally {
      setBusy(false);
    }
  }

  const mode = status.data?.mode;
  const role = status.data?.role ?? mode;
  const primaryState = !status.data
    ? "未取得状态"
    : role === "hotspot"
      ? "热点运行中"
      : role === "closed"
        ? "无线已关闭"
        : (stateNames[status.data.state] ?? "状态未知");
  const operation = status.data?.operation;

  return (
    <>
      <PageHeader
        title="网络"
        description={
          managed
            ? "独立管理无线连接与热点。"
            : "沿用相机原生网络访问控制台、相册和文件。"
        }
        actions={
          <Button
            variant="outline"
            disabled={status.isFetching}
            onClick={() => void refresh()}
          >
            <RefreshCw />
            刷新
          </Button>
        }
      />
      <div className="mb-5 space-y-3">
        {error && <Notice tone="error">{error}</Notice>}
        {message && <Notice tone="success">{message}</Notice>}
        {config.error && <Notice tone="error">{config.error.message}</Notice>}
        {status.error && hardware && (
          <Notice tone="error">无线状态不可用：{status.error.message}</Notice>
        )}
        {managed && operation?.status === "failed" && (
          <Notice
            tone={
              operation.recovery_status === "restored" ? "warning" : "error"
            }
          >
            {operation.recovery_status === "restored"
              ? `切换失败，已回退到${operation.recovery_ssid ? `「${operation.recovery_ssid}」` : operation.recovery_mode === "native" ? "原生网络" : operation.recovery_mode === "closed" ? "无线关闭状态" : "先前网络"}。`
              : "上次网络操作失败。"}
            {operation.recovery_status === "failed" &&
              " 自动恢复也未完成，请通过 USB ADB 访问。"}
            <details className="mt-2">
              <summary className="cursor-pointer">失败详情</summary>
              <p className="mt-2 break-all">{operation.error}</p>
            </details>
          </Notice>
        )}
        {managed && operating && (
          <Notice>
            <Spinner /> 网络操作
            {operation?.recovery_status === "restoring"
              ? "恢复先前连接中"
              : operation?.status === "queued"
                ? "排队中"
                : "执行中"}
            ，请勿重复提交。
          </Notice>
        )}
        {managed &&
          operation?.status === "succeeded" &&
          hiddenSuccess !== operation.id && (
            <Notice tone="success">网络操作已完成。</Notice>
          )}
      </div>

      {managed && reconnect && (
        <Card className="mb-5" title="切换后的访问方式">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 space-y-2 text-sm">
              <p className="break-all">
                目标：{reconnect.ssid}{" "}
                <span className="text-xs text-muted-foreground">
                  · {reconnect.time}
                </span>
              </p>
              {reconnect.mode === "off" ? (
                <p className="text-muted-foreground">
                  关闭无线后，请通过 USB ADB
                  转发访问控制台并重新开启网络，或重启相机恢复原生网络。
                </p>
              ) : reconnect.address ? (
                <p>
                  连接此热点后打开：
                  <a
                    className="break-all font-medium underline underline-offset-4"
                    href={reconnect.address}
                  >
                    {reconnect.address}
                  </a>
                </p>
              ) : (
                <p className="text-muted-foreground">
                  连接到同一 WiFi 后，可在路由器中查看相机的新 IP 地址；也可使用
                  USB ADB 转发。
                </p>
              )}
              {reconnect.mode !== "off" && (
                <p className="text-muted-foreground">
                  切换失败且先前配置可用时，设备会尝试恢复先前连接。恢复后可
                  <a
                    className="mx-1 underline underline-offset-4"
                    href={reconnect.previous}
                  >
                    重试原地址
                  </a>
                  。
                </p>
              )}
              <details>
                <summary className="cursor-pointer">使用 USB 恢复访问</summary>
                <p className="mt-2">连接 USB 后在电脑运行：</p>
                <pre className="mt-2 max-w-full overflow-x-auto rounded-md bg-muted p-3 text-xs">
                  adb forward tcp:8080 tcp:{httpPort}
                </pre>
                <a
                  className="mt-2 inline-block underline underline-offset-4"
                  href="http://127.0.0.1:8080/#/network"
                >
                  打开 USB 访问地址
                </a>
              </details>
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setReconnect(null)}
            >
              关闭提示
            </Button>
          </div>
        </Card>
      )}

      <Card className="mb-5" title="当前网络">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex min-w-0 items-center gap-3">
            <Wifi className="size-7 shrink-0 text-primary" />
            <div className="min-w-0">
              <p className="text-lg font-semibold">{primaryState}</p>
              {(status.data?.owner === "native" || mode === "native") && (
                <Badge className="mt-1" variant="secondary">
                  DJI 原生管理
                </Badge>
              )}
              <p className="break-all text-sm text-muted-foreground">
                {status.data?.ssid || "未连接网络"}
              </p>
            </div>
          </div>
          <div className="text-right text-sm">
            <p className="text-muted-foreground">IP 地址</p>
            <p className="select-all font-mono">{status.data?.ip || "—"}</p>
            {status.data?.ip && (
              <a
                className="mt-1 inline-block text-xs underline underline-offset-4"
                href={`http://${status.data.ip}:${httpPort}/#/network`}
              >
                通过此地址访问
              </a>
            )}
          </div>
        </div>
        {!managed && (
          <p className="mt-4 text-sm text-muted-foreground">
            请使用相机本身的无线设置。手机或电脑连接相机原生热点后，可通过上方地址访问控制台；相机已有局域网连接时也可直接使用。
          </p>
        )}
        <details className="mt-5 border-t pt-4 text-sm">
          <summary className="cursor-pointer font-medium">详细信息</summary>
          <dl className="mt-4 grid grid-cols-2 gap-4 md:grid-cols-4">
            <div>
              <dt className="text-muted-foreground">底层状态</dt>
              <dd>{status.data?.state ?? "—"}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">信号</dt>
              <dd>
                {status.data?.signal == null
                  ? "—"
                  : `${status.data.signal} dBm`}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">频率</dt>
              <dd>
                {status.data?.freq_mhz == null
                  ? "—"
                  : `${status.data.freq_mhz} MHz`}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">发送 / 接收</dt>
              <dd>
                {status.data?.tx_bitrate_mbps ?? "—"} /{" "}
                {status.data?.rx_bitrate_mbps ?? "—"} Mbps
              </dd>
            </div>
          </dl>
        </details>
      </Card>

      <details className="mb-5 rounded-xl border bg-card px-6 py-5 shadow-sm">
        <summary className="cursor-pointer font-semibold">高级网络管理</summary>
        <div className="mt-4 max-w-2xl space-y-3">
          <Label htmlFor="network-control">管理方式</Label>
          <Select
            value={config.data?.network_control ?? "native"}
            disabled={!config.data || busy || operating}
            onValueChange={async (value) => {
              setBusy(true);
              setError("");
              setMessage("");
              try {
                await post("config", { network_control: value });
                await refresh();
                setMessage(
                  value === "native"
                    ? "已使用原生网络。"
                    : "已启用独立管理，执行网络操作时生效。",
                );
              } catch (cause) {
                setError(errorText(cause));
              } finally {
                setBusy(false);
              }
            }}
          >
            <SelectTrigger id="network-control">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="native">使用原生网络（推荐）</SelectItem>
              <SelectItem value="managed">独立管理（高级）</SelectItem>
            </SelectContent>
          </Select>
          <p className="text-sm text-muted-foreground">
            默认由相机管理无线连接。独立管理模式会在切换网络时接管无线，可能中断
            DJI Mimo 连接。
          </p>
          {managed && (
            <p className="text-sm text-muted-foreground">
              返回原生模式前，请先到{" "}
              <a className="underline underline-offset-4" href="#/system">
                系统页
              </a>{" "}
              交还原生网络，等待操作完成。
            </p>
          )}
        </div>
      </details>

      {managed && (
        <>
          <div className="grid grid-cols-1 items-start gap-5 xl:grid-cols-2">
            <Card
              title="连接 WiFi"
              description="选择已保存网络，或扫描附近网络。切换网络可能使当前页面断线。"
            >
              <div className="mb-4 flex flex-wrap gap-2">
                <Button
                  disabled={!canChange || scan.isFetching || busy || operating}
                  onClick={() => void scan.refetch()}
                >
                  <RefreshCw />
                  {scan.isFetching ? "扫描中…" : "扫描附近网络"}
                </Button>
                <Button
                  variant="outline"
                  disabled={!canChange || busy || operating}
                  onClick={() => showConnect()}
                >
                  连接其他网络
                </Button>
              </div>
              {scan.error && (
                <Notice tone="error">扫描失败：{scan.error.message}</Notice>
              )}
              {known.isPending ? (
                <Spinner />
              ) : known.error ? (
                <Notice tone="error">{known.error.message}</Notice>
              ) : !networks.length ? (
                <EmptyState
                  title="尚无已保存网络"
                  description="扫描附近网络，或手动输入网络名称。"
                />
              ) : (
                <div className="divide-y">
                  {networks.map((network) => (
                    <div
                      key={network.ssid || network.bssid}
                      className="flex items-center justify-between gap-3 py-4"
                    >
                      <div className="flex min-w-0 items-center gap-3">
                        <Signal className="size-5 shrink-0 text-primary" />
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2">
                            <p className="break-all text-sm font-medium">
                              {network.ssid || "隐藏网络"}
                            </p>
                            {network.connected && <Badge>当前连接</Badge>}
                            {network.saved && (
                              <Badge variant="secondary">已保存</Badge>
                            )}
                          </div>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {network.signal == null
                              ? "信号未知"
                              : `${network.signal} dBm`}{" "}
                            · {network.encrypted ? "需要密码" : "开放网络"}
                          </p>
                          {(network.bssid || network.band) && (
                            <details className="mt-2 text-xs text-muted-foreground">
                              <summary className="cursor-pointer">
                                网络详情
                              </summary>
                              <p className="mt-1 font-mono">
                                {network.bssid || "BSSID 未知"}
                                {network.band ? ` · ${network.band}` : ""}
                              </p>
                            </details>
                          )}
                        </div>
                      </div>
                      <div className="flex shrink-0 items-center gap-2">
                        {!network.connected && (
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={!canChange || busy || operating}
                            onClick={() => showConnect(network)}
                          >
                            连接
                          </Button>
                        )}
                        {network.saved && (
                          <details className="relative">
                            <summary className="cursor-pointer list-none rounded-md px-2 py-1 text-sm text-muted-foreground hover:bg-accent">
                              更多
                            </summary>
                            <div className="absolute right-0 z-10 mt-1 rounded-md border bg-popover p-1 shadow-md">
                              <ConfirmButton
                                title="忘记此网络？"
                                description={`将删除「${network.ssid}」的保存凭据，不会主动断开当前连接。`}
                                disabled={!canChange || busy || operating}
                                onConfirm={async () => {
                                  await post("known_forget", {
                                    ssid: network.ssid,
                                    confirm: true,
                                  });
                                  await refresh();
                                }}
                              >
                                <Trash2 /> 忘记
                              </ConfirmButton>
                            </div>
                          </details>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              )}
              <details className="mt-5 border-t pt-4">
                <summary className="cursor-pointer text-sm font-medium">
                  高级设置
                </summary>
                <label className="mt-4 flex items-start gap-3 text-sm">
                  <Checkbox
                    className="mt-1"
                    checked={config.data?.auto_connect ?? false}
                    disabled={!canChange || busy || operating}
                    onCheckedChange={async (checked) => {
                      setBusy(true);
                      setError("");
                      try {
                        await post("auto_connect", {
                          enabled: checked === true,
                        });
                        await refresh();
                        setMessage("自动连接设置已保存，下次整机启动时生效。");
                      } catch (cause) {
                        setError(errorText(cause));
                      } finally {
                        setBusy(false);
                      }
                    }}
                  />
                  <span>
                    启动时扫描并连接最近使用的已保存网络
                    <span className="mt-1 block text-xs text-muted-foreground">
                      开启后在下次整机启动时接管无线并尝试连接。
                    </span>
                  </span>
                </label>
                {config.error && (
                  <div className="mt-3">
                    <Notice tone="error">{config.error.message}</Notice>
                  </div>
                )}
              </details>
            </Card>

            <Card
              title="相机热点"
              description="配置并开启供手机或电脑连接的相机热点，不提供互联网转发。"
            >
              {hotspot.error && (
                <Notice tone="error">
                  无法读取热点配置：{hotspot.error.message}
                </Notice>
              )}
              <form
                className="space-y-4"
                onSubmit={(event) => {
                  event.preventDefault();
                  setConfirmHotspot(true);
                }}
              >
                <div>
                  <Label htmlFor="hotspot-ssid">热点名称</Label>
                  <Input
                    id="hotspot-ssid"
                    value={apSSID}
                    required
                    onChange={(event) => setAPSSID(event.target.value)}
                  />
                  <p className="mt-1 text-xs text-muted-foreground">
                    1–32 个 UTF-8 字节。
                  </p>
                </div>
                {!apOpen && (
                  <div>
                    <Label htmlFor="hotspot-password">新密码</Label>
                    <Input
                      id="hotspot-password"
                      type="password"
                      autoComplete="new-password"
                      placeholder={
                        hotspot.data?.has_password
                          ? "留空则保留当前密码"
                          : "请输入热点密码"
                      }
                      value={apPassword}
                      required={!hotspot.data?.has_password}
                      onChange={(event) => setAPPassword(event.target.value)}
                    />
                    <p className="mt-1 text-xs text-muted-foreground">
                      8–63 字节或 64 位十六进制 PSK。
                    </p>
                  </div>
                )}
                <details className="border-t pt-4">
                  <summary className="cursor-pointer text-sm font-medium">
                    高级设置
                  </summary>
                  <div className="mt-4 space-y-4">
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <Label htmlFor="hotspot-band">频段</Label>
                        <Select
                          value={band}
                          onValueChange={(value) => {
                            setBand(value);
                            setChannel(value === "5G" ? "149" : "6");
                          }}
                        >
                          <SelectTrigger id="hotspot-band" className="w-full">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="2.4G">2.4 GHz</SelectItem>
                            <SelectItem value="5G">5 GHz</SelectItem>
                          </SelectContent>
                        </Select>
                      </div>
                      <div>
                        <Label htmlFor="hotspot-channel">信道</Label>
                        <Select value={channel} onValueChange={setChannel}>
                          <SelectTrigger
                            id="hotspot-channel"
                            className="w-full"
                          >
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {(band === "5G"
                              ? [
                                  36, 40, 44, 48, 52, 56, 60, 64, 100, 104, 108,
                                  112, 116, 120, 124, 128, 132, 136, 140, 144,
                                  149, 153, 157, 161, 165,
                                ]
                              : Array.from(
                                  { length: 14 },
                                  (_, index) => index + 1,
                                )
                            ).map((value) => (
                              <SelectItem key={value} value={String(value)}>
                                {value}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                    </div>
                    <p className="text-xs leading-5 text-muted-foreground">
                      实际可用信道受固件、区域和无线环境约束。
                    </p>
                    <label className="flex items-center gap-3 text-sm">
                      <Checkbox
                        checked={apOpen}
                        onCheckedChange={(checked) => {
                          setAPOpen(checked === true);
                          setAPPassword("");
                        }}
                      />
                      开放热点（不设密码）
                    </label>
                    {apOpen && (
                      <Notice tone="warning">
                        开放热点没有 WiFi 加密，只应在可信环境使用。
                      </Notice>
                    )}
                  </div>
                </details>
                <div className="flex flex-wrap items-center gap-2">
                  {(mode !== "hotspot" || apDirty) && (
                    <Button
                      type="submit"
                      disabled={
                        !canChange || busy || operating || !hotspot.data
                      }
                    >
                      {mode === "hotspot" ? <Save /> : <Radio />}
                      {mode === "hotspot" ? "保存并重启热点" : "保存并开启热点"}
                    </Button>
                  )}
                  {mode === "hotspot" && !apDirty && (
                    <Badge>热点正在运行</Badge>
                  )}
                  {apDirty && hotspot.data && (
                    <Button
                      type="button"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => loadHotspot(hotspot.data!)}
                    >
                      放弃修改
                    </Button>
                  )}
                </div>
              </form>
            </Card>
          </div>

          <details className="mt-5 rounded-xl border bg-card px-6 py-5 shadow-sm">
            <summary className="cursor-pointer font-semibold text-destructive">
              危险操作
            </summary>
            <div className="mt-4 flex flex-wrap items-center justify-between gap-4 border-t pt-4">
              <p className="max-w-2xl text-sm text-muted-foreground">
                关闭客户端与热点。之后只能通过 USB ADB
                转发或整机重启恢复网页访问。
              </p>
              <ConfirmButton
                title="关闭无线网络？"
                description="将断开 WiFi 和热点。之后需要 USB ADB 转发或整机重启才能恢复访问。"
                disabled={!canChange || busy || operating}
                variant="destructive"
                onConfirm={() => queue("wifi_off")}
              >
                <WifiOff /> 关闭无线网络
              </ConfirmButton>
            </div>
          </details>
          <Modal
            open={confirmHotspot}
            onOpenChange={(value) => {
              if (!busy) setConfirmHotspot(value);
            }}
            title={mode === "hotspot" ? "保存并重启热点？" : "保存并开启热点？"}
            description={`将切换到热点「${apSSID}」。当前页面可能断线；失败时设备会尝试恢复先前连接与设置。`}
          >
            <div className="flex justify-end gap-2">
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => setConfirmHotspot(false)}
              >
                取消
              </Button>
              <Button
                disabled={!canChange || busy || operating}
                onClick={async () => {
                  await applyHotspot();
                  setConfirmHotspot(false);
                }}
              >
                {busy ? "正在提交…" : "确认切换"}
              </Button>
            </div>
          </Modal>

          <Modal
            open={connect}
            onOpenChange={(value) => {
              if (!busy) {
                setConnect(value);
                if (!value) setPassword("");
              }
            }}
            title={ssid ? `连接到「${ssid}」` : "连接其他网络"}
            description="确认后将切换到客户端模式，当前浏览器可能断线；连接失败时会尝试恢复先前网络。"
          >
            <form
              className="space-y-4"
              onSubmit={async (event) => {
                event.preventDefault();
                setConnectError("");
                try {
                  const body: Record<string, unknown> = { ssid, bssid };
                  if (!reuse) body.password = open ? "" : password;
                  await queue("wifi_connect", body);
                  setConnect(false);
                  setPassword("");
                } catch (cause) {
                  setConnectError(
                    `${errorText(cause)}。若连接中断，请等待状态刷新，不要立即重复提交。`,
                  );
                }
              }}
            >
              {reuse ? (
                <Notice>
                  将使用已保存的凭据连接。切换网络可能使当前页面断线。
                </Notice>
              ) : (
                <>
                  <div>
                    <Label htmlFor="wifi-ssid">网络名称</Label>
                    <Input
                      id="wifi-ssid"
                      autoFocus
                      value={ssid}
                      onChange={(event) => setSSID(event.target.value)}
                      required
                    />
                  </div>
                  {!open && (
                    <div>
                      <Label htmlFor="wifi-password">密码</Label>
                      <Input
                        id="wifi-password"
                        type="password"
                        autoComplete="current-password"
                        value={password}
                        onChange={(event) => setPassword(event.target.value)}
                        required
                      />
                      <p className="mt-1 text-xs text-muted-foreground">
                        8–63 字节或 64 位十六进制 PSK。
                      </p>
                    </div>
                  )}
                </>
              )}
              <details className="border-t pt-4">
                <summary className="cursor-pointer text-sm font-medium">
                  高级设置
                </summary>
                <div className="mt-4 space-y-4">
                  <div>
                    <Label htmlFor="wifi-bssid">指定 BSSID（可选）</Label>
                    <Input
                      id="wifi-bssid"
                      className="font-mono"
                      placeholder="aa:bb:cc:dd:ee:ff"
                      value={bssid}
                      onChange={(event) => setBSSID(event.target.value)}
                    />
                  </div>
                  {!reuse && (
                    <label className="flex items-center gap-3 text-sm">
                      <Checkbox
                        checked={open}
                        onCheckedChange={(checked) => setOpen(checked === true)}
                      />
                      开放网络（不使用密码）
                    </label>
                  )}
                </div>
              </details>
              {connectError && (
                <Notice tone="error">
                  {connectError}
                  <Button
                    type="button"
                    variant="outline"
                    className="mt-2"
                    onClick={() => setConnect(false)}
                  >
                    查看恢复方式
                  </Button>
                </Notice>
              )}
              <div className="flex justify-end">
                <Button
                  type="submit"
                  disabled={!canChange || busy || operating}
                >
                  <KeyRound /> {busy ? "正在提交…" : "确认连接"}
                </Button>
              </div>
            </form>
          </Modal>
        </>
      )}
    </>
  );
}
