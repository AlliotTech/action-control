import { useEffect, useState } from "react";
import {
  Database,
  Eraser,
  Info,
  Play,
  RotateCcw,
  Settings2,
  ShieldAlert,
  Square,
  Terminal,
  Trash2,
} from "lucide-react";
import { errorText, invalidate, post, useAPI } from "../api";
import type { Config, MediaIndexStatus, WiFiStatus } from "../types";
import { routeParam } from "../lib/view-state";
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
import { Textarea } from "@/components/ui/textarea";
import { Card, ConfirmButton, Notice, PageHeader, Spinner } from "../ui";

type CommandResult = {
  stdout: string;
  stderr: string;
  exit_code: number;
  cwd: string;
};
function MediaIndexCard({
  storage,
  label,
  hardware,
  busy,
  perform,
}: {
  storage: "sd" | "emulated";
  label: string;
  hardware: boolean;
  busy: boolean;
  perform: (name: string, data: unknown, success: string) => Promise<void>;
}) {
  const index = useAPI<MediaIndexStatus>("media_index_status", { storage });
  return (
    <div className="rounded-lg border p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="font-medium">{label}媒体索引</p>
          <p className="mt-1 text-sm text-muted-foreground">
            检测 AC004.db 中已经不存在的文件记录；清理前自动创建数据库备份。
          </p>
        </div>
        <ConfirmButton
          title={`清理${label}失效媒体索引？`}
          description={`将删除 ${index.data?.stale ?? 0} 条文件已不存在的记录。操作前会在${label} MISC 目录创建 AC004.db 备份；不会执行 VACUUM。`}
          variant="destructive"
          disabled={
            !hardware || busy || !index.data?.available || !index.data.stale
          }
          onConfirm={() =>
            perform(
              "clean_media_index",
              { storage, confirm: true },
              `${label}失效媒体索引已清理，数据库备份保留在 MISC 目录。`,
            )
          }
        >
          <Database />
          清理媒体索引
        </ConfirmButton>
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        {index.isPending ? (
          <Spinner />
        ) : index.isError ? (
          <Notice tone="warning">{index.error.message}</Notice>
        ) : !index.data?.available ? (
          <Badge>{index.data?.reason ?? "媒体索引不可用"}</Badge>
        ) : (
          <>
            <Badge>总记录：{index.data.total}</Badge>
            <Badge>失效：{index.data.stale}</Badge>
            {index.data.invalid > 0 && (
              <Badge>异常路径：{index.data.invalid}</Badge>
            )}
            {Object.entries(index.data.by_type)
              .filter(([, count]) => count > 0)
              .map(([type, count]) => (
                <Badge key={type}>
                  {type}：{count}
                </Badge>
              ))}
          </>
        )}
      </div>
    </div>
  );
}

export function SystemPage() {
  const [maintenanceOpen, setMaintenanceOpen] = useState(
    routeParam("section") === "maintenance",
  );
  const health = useAPI<{ version: string; device: boolean }>(
    "health",
    undefined,
    5000,
  );
  const config = useAPI<Config>("config");
  const native = useAPI<{ status: string }>("dji_network", undefined, 5000);
  const network = useAPI<WiFiStatus>("wifi_status", undefined, 3000);

  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [message, setMessage] = useState("");
  const [command, setCommand] = useState(""),
    [cwd, setCwd] = useState("/blackbox"),
    [timeout, setTimeoutValue] = useState(15),
    [result, setResult] = useState<CommandResult | null>(null);
  const [concurrency, setConcurrency] = useState<number | null>(null);
  const hardware = health.data?.device === true && !health.isError;
  const managedNetwork = config.data?.network_control === "managed";
  const networkBusy =
    network.data?.operation?.status === "queued" ||
    network.data?.operation?.status === "running";
  useEffect(() => {
    if (config.data)
      setConcurrency((current) => current ?? config.data!.thumb_concurrent);
  }, [config.data]);

  async function perform(name: string, data: unknown, success: string) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await post(name, data);
      setMessage(success);
      await invalidate(
        "sysinfo",
        "camera_status",
        "process_list",
        "dji_network",
        "wifi_status",
        "config",
        "media_index_status",
      );
    } catch (e) {
      setError(errorText(e));
      throw e;
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHeader
        title="系统"
        description="原生服务与维护工具。这里的操作具有 root 权限。"
      />
      {error && (
        <div className="mb-4">
          <Notice tone="error">{error}</Notice>
        </div>
      )}
      {message && (
        <div className="mb-4">
          <Notice tone="success">{message}</Notice>
        </div>
      )}
      <div className="space-y-6">
        <Card
          title="性能设置"
          description="调整相册缩略图的生成速度与资源占用。"
        >
          {config.isError ? (
            <Notice tone="error">{config.error.message}</Notice>
          ) : config.isPending ? (
            <Spinner />
          ) : (
            <form
              className="flex flex-wrap items-end gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                if (concurrency != null)
                  void perform(
                    "config",
                    { thumb_concurrent: concurrency },
                    "设置已保存；缩略图并发变更在下次面板启动后生效。",
                  ).catch(() => {});
              }}
            >
              <div className="min-w-0 flex-1 basis-64">
                <Label htmlFor="thumbnail-concurrency">
                  缩略图并发（重启面板后生效）
                </Label>
                <Input
                  id="thumbnail-concurrency"
                  type="number"
                  min={1}
                  max={8}
                  required
                  value={concurrency ?? ""}
                  onChange={(e) => setConcurrency(Number(e.target.value))}
                />
              </div>
              <Button type="submit" disabled={busy}>
                <Settings2 />
                保存设置
              </Button>
            </form>
          )}
        </Card>
        <Card
          title="原生服务"
          description={
            managedNetwork
              ? "独立管理模式可在这里交还原生网络。"
              : "无线连接由相机本身管理。"
          }
        >
          <div className="flex flex-wrap items-center gap-3">
            <Badge>DJI 网络：{native.data?.status ?? "未知"}</Badge>
            {networkBusy && <Badge>网络操作进行中</Badge>}
            <a
              href="#/network"
              className="text-sm text-primary underline underline-offset-4"
            >
              查看完整网络状态
            </a>
          </div>
          {native.isError && (
            <p className="mt-3 text-sm text-muted-foreground">
              {native.error.message}
            </p>
          )}
          {network.data?.operation?.error && (
            <div className="mt-3">
              <Notice tone="warning">{network.data.operation.error}</Notice>
            </div>
          )}
          <div className="mt-4 flex flex-wrap gap-2">
            {managedNetwork && (
              <>
                <ConfirmButton
                  title="交还原生网络？"
                  description="停止本应用拥有的无线进程并启动 DJI 原生网络服务。当前 WiFi 连接可能断开；返回排队成功不代表切换已经完成。"
                  disabled={!hardware || busy || networkBusy}
                  onConfirm={() =>
                    perform(
                      "dji_network",
                      { action: "start", confirm: true },
                      "已提交原生网络启动请求，请观察网络状态。",
                    )
                  }
                >
                  <Play />
                  启动原生网络
                </ConfirmButton>
                <ConfirmButton
                  title="停止原生网络？"
                  description="将通过独立网络单元停止 DJI 网络服务，可能断开当前连接。请确保 ADB 可用于恢复。"
                  disabled={!hardware || busy || networkBusy}
                  onConfirm={() =>
                    perform(
                      "dji_network",
                      { action: "stop", confirm: true },
                      "已提交原生网络停止请求。",
                    )
                  }
                >
                  <Square />
                  停止原生网络
                </ConfirmButton>
              </>
            )}
            <ConfirmButton
              title="停止原生相机服务？"
              description="停止冲突的 DJI 原生服务并显示双屏保护提示。原生网络单独管理。此操作不会自动开始采集，需整机重启恢复相机原生功能。"
              disabled={!hardware || busy}
              variant="destructive"
              onConfirm={() =>
                perform(
                  "dji_kill_all",
                  { confirm: true },
                  "原生相机服务已停止；需整机重启恢复。",
                )
              }
            >
              <ShieldAlert />
              停止原生相机服务
            </ConfirmButton>
          </div>
        </Card>
        <Card
          title="命令执行"
          description="请求—响应式 root Shell，不是 PTY。工作目录随本页面显式发送；不支持 vim 等交互程序。"
        >
          <form
            className="space-y-4"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              setError("");
              setResult(null);
              try {
                const output = await post<CommandResult>("shell_exec", {
                  cmd: command,
                  cwd,
                  timeout,
                });
                setResult(output);
                setCwd(output.cwd);
              } catch (e) {
                setError(errorText(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <div className="grid gap-4 sm:grid-cols-[1fr_160px]">
              <div>
                <Label htmlFor="shell-cwd">工作目录</Label>
                <Input
                  id="shell-cwd"
                  value={cwd}
                  onChange={(e) => setCwd(e.target.value)}
                  required
                  className="font-mono"
                />
              </div>
              <div>
                <Label htmlFor="shell-timeout">超时（秒）</Label>
                <Input
                  id="shell-timeout"
                  type="number"
                  min={1}
                  max={120}
                  value={timeout}
                  onChange={(e) => setTimeoutValue(Number(e.target.value))}
                  required
                />
              </div>
            </div>
            <div>
              <Label htmlFor="shell-command">命令</Label>
              <Textarea
                id="shell-command"
                className="min-h-28 font-mono"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                spellCheck={false}
                required
              />
            </div>
            <Button
              type="submit"
              disabled={!hardware || busy || !command.trim()}
            >
              <Terminal />
              {busy ? "操作进行中" : "执行命令"}
            </Button>
          </form>
          {result && (
            <div className="mt-4 space-y-3">
              <Badge>退出码：{result.exit_code}</Badge>
              <div>
                <p className="mb-2 text-xs text-muted-foreground">stdout</p>
                <pre className="terminal-output">
                  {result.stdout || "（无输出）"}
                </pre>
              </div>
              <div>
                <p className="mb-2 text-xs text-muted-foreground">stderr</p>
                <pre className="terminal-output">
                  {result.stderr || "（无输出）"}
                </pre>
              </div>
            </div>
          )}
        </Card>
        <details
          open={maintenanceOpen}
          onToggle={(event) => setMaintenanceOpen(event.currentTarget.open)}
          className="rounded-xl border bg-card p-5"
        >
          <summary className="cursor-pointer font-semibold">
            高级维护与重启
          </summary>
          <div className="mt-5 space-y-5">
            <Notice tone="warning">
              这些操作会中断采集或连接。Fastboot / EDL
              不是普通重启，网页无法完成后续恢复。卸载也不撤销 BL 解锁、root ADB
              或 Factory Mode。
            </Notice>
            <MediaIndexCard
              storage="emulated"
              label="相机存储"
              hardware={hardware}
              busy={busy}
              perform={perform}
            />
            <MediaIndexCard
              storage="sd"
              label="SD 卡"
              hardware={hardware}
              busy={busy}
              perform={perform}
            />
            <div className="flex flex-wrap gap-2">
              <ConfirmButton
                title="重启面板？"
                description="停止本面板拥有的采集和命令进程，然后重新启动 HTTP 服务。独立 WiFi 单元不会被面板重启停止；不会自动重新开始采集。"
                disabled={!hardware || busy}
                onConfirm={() =>
                  perform(
                    "restart_dashboard",
                    { confirm: true },
                    "面板重启请求已提交；请等待重新连接后刷新页面。",
                  )
                }
              >
                <RotateCcw />
                重启面板
              </ConfirmButton>
              <ConfirmButton
                title="清理缩略图缓存？"
                description="仅清理 Action Control 自己生成的缩略图，不删除相机照片或视频。"
                disabled={busy}
                onConfirm={() =>
                  perform(
                    "clear_cache",
                    { confirm: true },
                    "应用缩略图缓存已清理。",
                  )
                }
              >
                <Eraser />
                清理缓存
              </ConfirmButton>
              <ConfirmButton
                title="清理应用日志？"
                description="仅清理 Action Control 私有日志，不清理 DJI 原生日志或其他程序文件。"
                disabled={busy}
                onConfirm={() =>
                  perform(
                    "clear_logs",
                    { confirm: true },
                    "应用私有日志已清理；systemd journal 不在此范围内。",
                  )
                }
              >
                <Trash2 />
                清理日志
              </ConfirmButton>
            </div>
            <div className="flex flex-wrap gap-2">
              {[
                {
                  mode: "normal",
                  label: "整机重启",
                  detail: "将中断所有采集和连接；重新开机后恢复原生运行态。",
                },
                {
                  mode: "recovery",
                  label: "Recovery",
                  detail:
                    "进入恢复模式，网页会离线。不要在恢复环境中误清除媒体。",
                },
                {
                  mode: "bootloader",
                  label: "Fastboot",
                  detail:
                    "进入 Bootloader，网页和普通 ADB 将离线。需要主机 Fastboot 工具恢复。",
                },
                {
                  mode: "edl",
                  label: "EDL / 9008",
                  detail:
                    "进入 Qualcomm 9008 下载模式。需要对应主机工具；没有恢复准备请取消。",
                },
              ].map((item) => (
                <ConfirmButton
                  key={item.mode}
                  variant="destructive"
                  title={`确认${item.label}？`}
                  description={item.detail}
                  disabled={!hardware || busy}
                  onConfirm={() =>
                    perform(
                      "reboot",
                      { mode: item.mode, confirm: true },
                      `${item.label}请求已提交；连接中断不能证明启动过程已完成。`,
                    )
                  }
                >
                  <RotateCcw />
                  {item.label}
                </ConfirmButton>
              ))}
            </div>
          </div>
        </details>
        <Card title="关于 Action Control">
          <div className="flex items-start gap-3">
            <Info className="mt-1 size-5 shrink-0 text-primary" />
            <div className="space-y-2 text-sm">
              <p>
                版本{" "}
                <span className="font-mono">{health.data?.version ?? "—"}</span>{" "}
                · Go 后端与内嵌 React 网页
              </p>
              <p className="text-muted-foreground">
                HTTP 管理与媒体接口不设登录认证；这是 root 管理服务，请仅通过
                ADB 转发或可信网络访问。TCP 视频同样没有认证。
              </p>
              <p className="text-muted-foreground">
                系统：{hardware ? "相机 root 服务" : "本地文件模式或设备不可用"}{" "}
                · 推流协议：MPEG-TS / H.264
              </p>
            </div>
          </div>
        </Card>
      </div>
    </>
  );
}
