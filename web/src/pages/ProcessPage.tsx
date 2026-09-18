import { useEffect, useMemo, useState } from "react";
import { ArrowDownUp, FileUp, Play, RefreshCw, Search, Square } from "lucide-react";
import { errorText, invalidate, post, useAPI } from "../api";
import type { ProcessInfo } from "../types";
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Card,
  ConfirmButton,
  EmptyState,
  Modal,
  Notice,
  PageHeader,
  Spinner,
  formatBytes,
} from "../ui";

const pageSize = 20;

export function ProcessPage() {
  const health = useAPI<{ device: boolean }>("health", undefined, 5000);
  const processes = useAPI<{ processes: ProcessInfo[]; count: number }>(
    "process_list",
    undefined,
    5000,
  );
  const [filter, setFilter] = useState(""),
    [sort, setSort] = useState<"pid" | "cpu" | "rss" | "name">("rss"),
    [descending, setDescending] = useState(true),
    [page, setPage] = useState(1);
  const [selected, setSelected] = useState<ProcessInfo | null>(null);
  const [startOpen, setStartOpen] = useState(false),
    [startCommand, setStartCommand] = useState(""),
    [startCwd, setStartCwd] = useState("/blackbox");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [message, setMessage] = useState("");
  const hardware = health.data?.device === true && !health.isError;
  const visible = useMemo(() => {
    const needle = filter.toLocaleLowerCase();
    return (processes.data?.processes ?? [])
      .filter((process) =>
        `${process.pid} ${process.name} ${process.cmd} ${process.user}`
          .toLocaleLowerCase()
          .includes(needle),
      )
      .sort((a, b) => {
        const order =
          sort === "name"
            ? a.name.localeCompare(b.name)
            : sort === "rss"
              ? a.rss_bytes - b.rss_bytes
              : sort === "cpu"
                ? (a.cpu_percent ?? -1) - (b.cpu_percent ?? -1)
                : a.pid - b.pid;
        return descending ? -order : order;
      });
  }, [processes.data, filter, sort, descending]);
  const pageCount = Math.max(1, Math.ceil(visible.length / pageSize));
  const currentPage = Math.min(page, pageCount);
  const pageProcesses = visible.slice(
    (currentPage - 1) * pageSize,
    currentPage * pageSize,
  );

  useEffect(() => {
    setPage((current) => Math.min(current, pageCount));
  }, [pageCount]);

  async function perform(name: string, data: unknown, success: string) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await post(name, data);
      setMessage(success);
      await invalidate("process_list");
    } catch (error) {
      setError(errorText(error));
      throw error;
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHeader
        title="进程"
        description="查看和管理设备进程。这里的操作具有 root 权限。"
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
      <Card description="CPU 为两次采样之间的整机占比；未知值不按零显示。终止时校验 PID 的启动身份。">
        <div className="mb-4 flex flex-wrap items-end gap-3">
          <div className="min-w-40 flex-1">
            <Label htmlFor="process-filter">筛选名称 / 命令 / PID / 用户</Label>
            <div className="relative">
              <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
              <Input
                className="pl-9"
                id="process-filter"
                value={filter}
                onChange={(event) => {
                  setFilter(event.target.value);
                  setPage(1);
                }}
              />
            </div>
          </div>
          <div>
            <Label htmlFor="process-sort">排序</Label>
            <Select
              value={sort}
              onValueChange={(value) => {
                setSort(value as typeof sort);
                setPage(1);
              }}
            >
              <SelectTrigger id="process-sort" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="rss">内存</SelectItem>
                <SelectItem value="cpu">CPU</SelectItem>
                <SelectItem value="pid">PID</SelectItem>
                <SelectItem value="name">名称</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <Button
            variant="outline"
            aria-label={descending ? "改为升序排列" : "改为降序排列"}
            onClick={() => {
              setDescending((value) => !value);
              setPage(1);
            }}
          >
            <ArrowDownUp />
            {descending ? "降序" : "升序"}
          </Button>
          <Button
            variant="outline"
            onClick={() => void processes.refetch()}
            disabled={processes.isFetching}
          >
            <RefreshCw />
            刷新
          </Button>
          <Button
            disabled={!hardware || busy}
            onClick={() => setStartOpen(true)}
          >
            <Play />
            启动进程
          </Button>
        </div>
        {processes.isPending ? (
          <Spinner />
        ) : processes.isError ? (
          <Notice tone="warning">无法读取进程：{processes.error.message}</Notice>
        ) : visible.length === 0 ? (
          <EmptyState title="没有匹配的进程" />
        ) : (
          <>
            <Table className="min-w-[620px]">
              <TableHeader>
                <TableRow>
                  <TableHead>PID</TableHead>
                  <TableHead>进程 / 命令</TableHead>
                  <TableHead>用户</TableHead>
                  <TableHead>CPU</TableHead>
                  <TableHead>内存</TableHead>
                  <TableHead>操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {pageProcesses.map((process) => (
                  <TableRow key={`${process.pid}:${process.start_time}`}>
                    <TableCell className="font-mono">{process.pid}</TableCell>
                    <TableCell className="max-w-sm">
                      <div className="font-medium">{process.name}</div>
                      <div
                        className="truncate font-mono text-xs text-muted-foreground"
                        title={process.cmd}
                      >
                        {process.cmd}
                      </div>
                    </TableCell>
                    <TableCell>{process.user || "—"}</TableCell>
                    <TableCell className="font-mono">
                      {process.cpu_percent == null
                        ? "—"
                        : `${process.cpu_percent.toFixed(1)}%`}
                    </TableCell>
                    <TableCell className="font-mono">
                      {formatBytes(process.rss_bytes)}
                    </TableCell>
                    <TableCell>
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={
                          !hardware ||
                          busy ||
                          process.pid <= 1 ||
                          process.name === "adbd" ||
                          process.name === "systemd"
                        }
                        onClick={() => setSelected(process)}
                      >
                        管理
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <div className="mt-4 flex items-center justify-between gap-3 text-sm">
              <span className="text-muted-foreground">
                共 {visible.length} 个 · 第 {currentPage} / {pageCount} 页
              </span>
              <div className="flex gap-2">
                <Button
                  variant="outline"
                  disabled={currentPage === 1}
                  onClick={() => setPage((value) => value - 1)}
                >
                  上一页
                </Button>
                <Button
                  variant="outline"
                  disabled={currentPage === pageCount}
                  onClick={() => setPage((value) => value + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          </>
        )}
      </Card>
      <Modal
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
        title="管理进程"
        description="请核对命令与启动身份。未知进程可能属于固件或其他应用。"
      >
        {selected && (
          <div className="space-y-4">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
              <dt>PID / 父 PID</dt>
              <dd className="font-mono">
                {selected.pid} / {selected.ppid}
              </dd>
              <dt>名称</dt>
              <dd>{selected.name}</dd>
              <dt>命令</dt>
              <dd className="break-all font-mono">{selected.cmd}</dd>
              <dt>启动身份</dt>
              <dd className="font-mono">{selected.start_time}</dd>
              <dt>虚拟内存</dt>
              <dd>{formatBytes(selected.vsz_bytes)}</dd>
            </dl>
            <div className="flex flex-wrap gap-2">
              {[15, 9].map((signal) => (
                <ConfirmButton
                  key={signal}
                  title={`向 PID ${selected.pid} 发送 SIG${signal === 15 ? "TERM" : "KILL"}？`}
                  description={
                    signal === 15
                      ? "请求进程退出，可能中断正在进行的操作。"
                      : "强制终止可能导致未保存数据丢失。只在明确需要时使用。"
                  }
                  disabled={busy}
                  variant={signal === 9 ? "destructive" : "outline"}
                  onConfirm={async () => {
                    await perform(
                      "process_kill",
                      {
                        pid: selected.pid,
                        start_time: selected.start_time,
                        signal,
                        confirm: true,
                      },
                      "信号已发送，请刷新进程状态。",
                    );
                    setSelected(null);
                  }}
                >
                  <Square />
                  {signal === 15 ? "请求退出" : "强制终止"}
                </ConfirmButton>
              ))}
            </div>
          </div>
        )}
      </Modal>
      <Modal
        open={startOpen}
        onOpenChange={setStartOpen}
        title="启动受管进程"
        description="命令在 Action Control 主服务进程组中运行；面板退出时会停止。不要在这里启动要跨面板重启存活的无线服务。"
      >
        <div className="space-y-4">
          <div>
            <Label htmlFor="start-cwd">工作目录</Label>
            <Input
              id="start-cwd"
              value={startCwd}
              onChange={(event) => setStartCwd(event.target.value)}
              className="font-mono"
            />
          </div>
          <div>
            <Label htmlFor="start-command">后台命令</Label>
            <Input
              id="start-command"
              value={startCommand}
              onChange={(event) => setStartCommand(event.target.value)}
              className="font-mono"
            />
          </div>
          <ConfirmButton
            title="以 root 启动此命令？"
            description={startCommand}
            disabled={busy || !startCommand.trim() || !startCwd.startsWith("/")}
            onConfirm={async () => {
              await perform(
                "process_start",
                { cmd: startCommand, cwd: startCwd, confirm: true },
                "后台进程已启动，请通过进程列表查看状态。",
              );
              setStartOpen(false);
            }}
          >
            <FileUp />
            启动
          </ConfirmButton>
        </div>
      </Modal>
    </>
  );
}
