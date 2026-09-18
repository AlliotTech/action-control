import { useSyncExternalStore } from "react";
import { APIError, errorText, invalidate, uploadFile } from "../api";

export type UploadStatus =
  "queued" | "uploading" | "saving" | "succeeded" | "failed" | "cancelled";
export interface UploadTask {
  id: number;
  name: string;
  dir: string;
  size: number;
  loaded: number;
  speed: number;
  status: UploadStatus;
  error?: string;
  file?: File;
}

const listeners = new Set<() => void>();
let snapshot: { tasks: UploadTask[]; open: boolean } = {
  tasks: [],
  open: false,
};
let sequence = 0;
let pumping = false;
let current: { id: number; controller: AbortController } | undefined;

export function activeUpload(task: UploadTask) {
  return (
    task.status === "queued" ||
    task.status === "uploading" ||
    task.status === "saving"
  );
}

function publish(tasks = snapshot.tasks, open = snapshot.open) {
  snapshot = { tasks, open };
  for (const listener of listeners) listener();
}

function update(id: number, patch: Partial<UploadTask>) {
  publish(
    snapshot.tasks.map((task) =>
      task.id === id ? { ...task, ...patch } : task,
    ),
  );
}

async function pump() {
  if (pumping) return;
  pumping = true;
  try {
    let task: UploadTask | undefined;
    while ((task = snapshot.tasks.find((item) => item.status === "queued"))) {
      const { id, dir, file, size } = task;
      if (!file) {
        update(id, { status: "failed", error: "请重新选择文件。" });
        continue;
      }
      const controller = new AbortController();
      current = { id, controller };
      const started = performance.now();
      update(id, {
        status: "uploading",
        loaded: 0,
        speed: 0,
        error: undefined,
      });
      try {
        await uploadFile(file, dir, controller.signal, (fraction) => {
          const loaded = Math.min(size, Math.round(size * fraction));
          update(id, {
            loaded,
            speed: loaded / Math.max(0.1, (performance.now() - started) / 1000),
            status: fraction >= 1 ? "saving" : "uploading",
          });
        });
        update(id, { status: "succeeded", loaded: size, file: undefined });
      } catch (error) {
        update(id, {
          status:
            error instanceof DOMException && error.name === "AbortError"
              ? "cancelled"
              : "failed",
          error:
            error instanceof APIError && error.status === 409
              ? "目标目录已有同名文件。请检查已有文件，或更改文件名后重新上传。"
              : errorText(error),
        });
      } finally {
        current = undefined;
        await invalidate(
          "list",
          "disk_usage",
          "media_list",
          "media_index_status",
        );
      }
    }
  } finally {
    pumping = false;
  }
}

export function enqueueUploads(files: File[], dir: string) {
  if (!files.length) return;
  publish(
    [
      ...snapshot.tasks,
      ...files.map((file): UploadTask => ({
        id: ++sequence,
        name: file.name,
        dir,
        size: file.size,
        loaded: 0,
        speed: 0,
        status: "queued",
        file,
      })),
    ],
    true,
  );
  void pump();
}

export function cancelUpload(id: number) {
  if (current?.id === id) current.controller.abort();
  else
    update(id, { status: "cancelled", error: "已从等待队列取消，尚未上传。" });
}

export function retryUpload(id: number) {
  const task = snapshot.tasks.find((item) => item.id === id);
  if (!task?.file || activeUpload(task) || task.status === "succeeded") return;
  update(id, { status: "queued", loaded: 0, speed: 0, error: undefined });
  void pump();
}

export function showUploads(open: boolean) {
  publish(snapshot.tasks, open);
}

export function clearUploads() {
  publish(snapshot.tasks.filter(activeUpload));
}

export function useUploads() {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => snapshot,
  );
}
