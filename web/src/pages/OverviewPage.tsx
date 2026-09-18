import {
  ArrowRight,
  BatteryMedium,
  Cpu,
  HardDrive,
  MemoryStick,
  RefreshCw,
  Thermometer,
  Wifi,
} from "lucide-react";
import { useAPI, invalidate } from "../api";
import type { CameraStatus, DiskUsage, SystemInfo, WiFiStatus } from "../types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { Card, Notice, PageHeader, Spinner, formatBytes } from "../ui";

function StorageCard({ name, path }: { name: string; path: string }) {
  const query = useAPI<DiskUsage>("disk_usage", { path }, 15000);
  return (
    <Card>
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium">
          <HardDrive className="size-4 text-muted-foreground" />
          {name}
        </div>
        <span className="font-mono text-xs text-muted-foreground">{path}</span>
      </div>
      {query.isPending ? (
        <div className="mt-5">
          <Spinner />
        </div>
      ) : query.isError ? (
        <p className="mt-5 text-sm text-muted-foreground">
          不可用 · {query.error.message}
        </p>
      ) : (
        <>
          <p className="mt-5 text-xl font-semibold">
            {formatBytes(query.data.free)}{" "}
            <span className="text-xs font-normal text-muted-foreground">
              可用 / {formatBytes(query.data.total)}
            </span>
          </p>
          <Progress
            aria-label={`${name}使用率`}
            value={Math.max(0, Math.min(100, query.data.pct))}
            className="mt-4"
          />
        </>
      )}
    </Card>
  );
}
export function OverviewPage() {
  const system = useAPI<SystemInfo>("sysinfo", undefined, 5000),
    camera = useAPI<CameraStatus>("camera_status", undefined, 3000),
    wifi = useAPI<WiFiStatus>("wifi_status", undefined, 5000);
  const metrics = [
    {
      label: "电池电量",
      value:
        system.data?.battery_percent == null
          ? "—"
          : `${system.data.battery_percent}%`,
      detail: system.data?.battery_status || "电源状态不可用",
      icon: BatteryMedium,
    },
    {
      label: "核心温度",
      value:
        system.data?.cpu_temp_c == null
          ? "—"
          : `${system.data.cpu_temp_c.toFixed(1)}°C`,
      detail:
        system.data?.battery_temp_c == null
          ? "电池温度不可用"
          : `电池 ${system.data.battery_temp_c.toFixed(1)}°C`,
      icon: Thermometer,
    },
    {
      label: "CPU 使用率",
      value:
        system.data?.cpu_percent == null
          ? "—"
          : `${system.data.cpu_percent.toFixed(1)}%`,
      detail: "采样值，不代表编码器负载",
      icon: Cpu,
    },
    {
      label: "运行内存",
      value: formatBytes(system.data?.mem_used),
      detail: `总计 ${formatBytes(system.data?.mem_total)}`,
      icon: MemoryStick,
    },
  ];
  return (
    <>
      <PageHeader
        title="设备概览"
        description="状态一目了然。保持对采集、网络与存储的掌握。"
        actions={
          <Button
            variant="outline"
            onClick={() =>
              invalidate(
                "sysinfo",
                "camera_status",
                "wifi_status",
                "disk_usage",
              )
            }
          >
            <RefreshCw />
            刷新
          </Button>
        }
      />
      {(camera.data?.reboot_required || system.data?.reboot_required) && (
        <div className="mb-5">
          <Notice tone="warning">
            原生相机服务已受影响。停止推流不会恢复原生拍摄；需要整机重启。
          </Notice>
        </div>
      )}
      {system.isError && (
        <div className="mb-5">
          <Notice tone="error">{system.error.message}</Notice>
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {metrics.map((metric) => (
          <Card key={metric.label}>
            <div className="flex items-center justify-between">
              <span className="metric-label">{metric.label}</span>
              <metric.icon className="size-4 text-muted-foreground" />
            </div>
            <p className="metric-value">{metric.value}</p>
            <p className="mt-2 text-xs text-muted-foreground">
              {metric.detail}
            </p>
          </Card>
        ))}
      </div>
      <div className="mt-6">
        <Card
          title={
            <span className="flex items-center gap-2">
              <Wifi className="size-4" />
              无线连接
            </span>
          }
        >
          {wifi.isError ? (
            <Notice>无线状态不可用：{wifi.error.message}</Notice>
          ) : wifi.isPending ? (
            <Spinner />
          ) : (
            <>
              <div className="flex items-center justify-between gap-3">
                <p className="truncate text-xl font-semibold">
                  {wifi.data.ssid || "未连接到网络"}
                </p>
                <Badge>
                  {
                    {
                      client: "客户端",
                      hotspot: "热点",
                      closed: "已关闭",
                      native: "原生管理",
                      unknown: "未知",
                    }[wifi.data.role ?? wifi.data.mode]
                  }
                </Badge>
              </div>
              <div className="mt-5 grid grid-cols-2 gap-4 text-sm">
                <div>
                  <p className="metric-label">IP 地址</p>
                  <p className="mt-2 break-all font-mono">
                    {wifi.data.ip || "—"}
                  </p>
                </div>
                <div>
                  <p className="metric-label">信号 / 频率</p>
                  <p className="mt-2">
                    {wifi.data.signal == null ? "—" : `${wifi.data.signal} dBm`}{" "}
                    / {wifi.data.freq_mhz ?? "—"} MHz
                  </p>
                </div>
              </div>
              <a
                href="#/network"
                className="mt-6 inline-flex items-center gap-2 text-sm font-medium text-primary"
              >
                管理网络
                <ArrowRight className="size-4" />
              </a>
            </>
          )}
        </Card>
      </div>
      <div className="mb-4 mt-8 flex items-center justify-between">
        <h2 className="text-base font-semibold">存储空间</h2>
        <a className="text-sm text-primary" href="#/files">
          浏览文件 →
        </a>
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        <StorageCard name="Blackbox" path="/" />
        <StorageCard name="内部存储" path="/emulated" />
        <StorageCard name="SD 卡" path="/sd" />
      </div>
      <Card title="详细系统信息" className="mt-7 text-sm">
        <dl className="grid gap-4 sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">内核</dt>
            <dd className="mt-1 break-all font-mono">
              {system.data?.kernel || "—"}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">运行时间</dt>
            <dd className="mt-1">
              {system.data?.uptime_sec == null
                ? "—"
                : `${Math.floor(system.data.uptime_sec / 3600)} 小时 ${Math.floor((system.data.uptime_sec % 3600) / 60)} 分钟`}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">电池电流</dt>
            <dd className="mt-1">
              {system.data?.battery_current_ma == null
                ? "—"
                : `${system.data.battery_current_ma} mA`}
            </dd>
          </div>
        </dl>
      </Card>
    </>
  );
}
