import { useEffect } from "react";
import { Check, RotateCcw, Upload, X } from "lucide-react";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";
import { Progress } from "./ui/progress";
import { EmptyState, Modal, formatBytes } from "../ui";
import {
  activeUpload,
  cancelUpload,
  clearUploads,
  retryUpload,
  showUploads,
  useUploads,
} from "../lib/uploads";
import { pageLink } from "../lib/view-state";

const labels = {
  queued: "等待上传",
  uploading: "上传中",
  saving: "正在保存",
  succeeded: "已保存",
  failed: "上传失败",
  cancelled: "已取消",
};

export function UploadCenter() {
  const { tasks, open } = useUploads();
  const active = tasks.filter(activeUpload).length;
  useEffect(() => {
    if (!active) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [active]);
  if (!tasks.length && !open) return null;
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        onClick={() => showUploads(true)}
        aria-label={`上传任务${active ? `，${active} 项进行中` : ""}`}
      >
        <Upload />
        <span className="hidden sm:inline">上传任务</span>
        {active > 0 && <span className="tabular-nums">{active}</span>}
      </Button>
      <Modal
        open={open}
        onOpenChange={showUploads}
        title="上传任务"
        description="切换页面可继续上传。关闭或刷新浏览器会中断任务，同名文件不会被覆盖。"
        size="wide"
      >
        {!tasks.length ? (
          <EmptyState title="没有上传任务" />
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
              <span className="text-muted-foreground">
                {tasks.filter((task) => task.status === "succeeded").length} /{" "}
                {tasks.length} 项已保存{active ? ` · ${active} 项进行中` : ""}
              </span>
              <Button
                size="sm"
                variant="ghost"
                onClick={clearUploads}
                disabled={tasks.every(activeUpload)}
              >
                清除已结束记录
              </Button>
            </div>
            <ul className="space-y-3">
              {tasks.map((task) => (
                <li key={task.id} className="min-w-0 rounded-lg border p-3">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0 flex-1">
                      <p className="break-all text-sm font-medium">
                        {task.name}
                      </p>
                      <a
                        className="mt-1 block break-all text-xs text-muted-foreground underline underline-offset-2"
                        href={pageLink("files", {
                          dir: task.dir,
                          search: task.name,
                        })}
                        onClick={() => showUploads(false)}
                      >
                        {task.dir}
                      </a>
                    </div>
                    <Badge
                      variant={
                        task.status === "failed" ? "destructive" : "secondary"
                      }
                    >
                      {labels[task.status]}
                    </Badge>
                  </div>
                  <Progress
                    className="mt-3"
                    aria-label={`${task.name} 上传进度`}
                    value={
                      task.status === "succeeded"
                        ? 100
                        : task.status === "saving"
                          ? 99
                          : Math.min(
                              99,
                              task.size ? (task.loaded / task.size) * 100 : 0,
                            )
                    }
                  />
                  <div className="mt-2 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                    <span>
                      {formatBytes(task.loaded)} / {formatBytes(task.size)}
                      {task.status === "uploading" && task.speed > 0
                        ? ` · ${formatBytes(task.speed)}/s`
                        : ""}
                    </span>
                    {activeUpload(task) ? (
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        aria-label={`取消上传 ${task.name}`}
                        onClick={() => cancelUpload(task.id)}
                      >
                        <X />
                      </Button>
                    ) : task.status === "succeeded" ? (
                      <Check className="size-4 text-emerald-600" />
                    ) : (
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`重试上传 ${task.name}`}
                        onClick={() => retryUpload(task.id)}
                      >
                        <RotateCcw /> 重试
                      </Button>
                    )}
                  </div>
                  {task.error && (
                    <p
                      role="status"
                      className="mt-2 break-words text-xs text-destructive"
                    >
                      {task.error}
                    </p>
                  )}
                </li>
              ))}
            </ul>
          </>
        )}
      </Modal>
    </>
  );
}
