import { useRef, useState } from "react";
import {
  ArrowUp,
  Download,
  File,
  Folder,
  FolderPlus,
  Images,
  HardDrive,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  Trash2,
  Upload,
} from "lucide-react";
import { apiURL, errorText, invalidate, post, useAPI } from "../api";
import { enqueueUploads } from "../lib/uploads";
import {
  isMediaFile,
  pageLink,
  parentPath,
  routeParam,
  usePageScroll,
  useViewState,
} from "../lib/view-state";
import type { DiskUsage, FileItem, FileListing } from "../types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
  formatDate,
} from "../ui";

export function FilesPage() {
  const [dir, setDir] = useViewState("files.dir", "/", routeParam("dir"));
  const [search, setSearch] = useViewState(
    "files.search",
    "",
    routeParam("search"),
  );
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<{
    kind: "rename" | "mkdir";
    item?: FileItem;
  } | null>(null);
  const [name, setName] = useState("");
  const [formError, setFormError] = useState("");
  const [removeItem, setRemoveItem] = useState<FileItem | null>(null);
  const [deleteError, setDeleteError] = useState("");
  const picker = useRef<HTMLInputElement>(null);
  const files = useAPI<FileListing>("list", { dir });
  usePageScroll("files", !files.isPending);
  const disk = useAPI<DiskUsage>("disk_usage", { path: dir });
  const items =
    files.data?.items.filter((item) =>
      item.name.toLocaleLowerCase().includes(search.toLocaleLowerCase()),
    ) ?? [];
  const refresh = () => invalidate("list", "disk_usage", "media_list");

  return (
    <>
      <PageHeader
        title="文件"
        description="管理相机文件，上传进度可在右上角查看。"
        actions={
          <>
            <Button
              variant="outline"
              disabled={busy || files.isFetching}
              onClick={() => void refresh()}
            >
              <RefreshCw />
              刷新
            </Button>
            <Button
              variant="outline"
              disabled={busy || !files.data}
              onClick={() => {
                setDialog({ kind: "mkdir" });
                setName("");
                setFormError("");
              }}
            >
              <FolderPlus />
              新建目录
            </Button>
            <Button
              disabled={busy || !files.data}
              onClick={() => picker.current?.click()}
            >
              <Upload />
              上传文件
            </Button>
          </>
        }
      />
      <input
        ref={picker}
        type="file"
        multiple
        className="sr-only"
        aria-label="选择上传文件"
        onChange={(event) => {
          const selected = Array.from(event.target.files ?? []);
          event.target.value = "";
          enqueueUploads(selected, dir);
        }}
      />
      <div className="mb-5 flex flex-wrap gap-2" aria-label="存储位置">
        {[
          ["/", "Blackbox"],
          ["/sd", "SD 卡"],
          ["/emulated", "内部存储"],
        ].map(([path, title]) => (
          <Button
            key={path}
            variant={
              dir === path || (path !== "/" && dir.startsWith(`${path}/`))
                ? "secondary"
                : "outline"
            }
            disabled={busy}
            onClick={() => {
              setDir(path);
              setSearch("");
              setError("");
              setMessage("");
            }}
          >
            <HardDrive />
            {title}
          </Button>
        ))}
      </div>
      <div className="mb-5 space-y-3">
        {error && <Notice tone="error">{error}</Notice>}
        {message && <Notice tone="success">{message}</Notice>}
        {busy && <Notice>操作进行中…</Notice>}
        {disk.data && (
          <div className="rounded-xl border bg-card px-4 py-3">
            <div className="mb-2 flex flex-wrap justify-between gap-2 text-sm">
              <span>
                已用 {formatBytes(disk.data.used)} /{" "}
                {formatBytes(disk.data.total)}
              </span>
              <span className="text-muted-foreground">
                可用 {formatBytes(disk.data.free)}
              </span>
            </div>
            <progress
              className="h-1.5 w-full accent-primary"
              max={100}
              value={disk.data.pct}
              aria-label="存储使用率"
            />
          </div>
        )}
        {disk.error && (
          <Notice tone="warning">容量读取失败：{disk.error.message}</Notice>
        )}
      </div>
      <div className="flex flex-1 flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <nav
            aria-label="目录路径"
            className="flex min-w-0 flex-wrap items-center gap-1 text-sm"
          >
            <Button
              variant="ghost"
              size="icon"
              aria-label="返回上级目录"
              disabled={
                busy || !files.data || files.data.parent === files.data.path
              }
              onClick={() => files.data && setDir(files.data.parent)}
            >
              <ArrowUp />
            </Button>
            <ol className="flex min-w-0 flex-wrap items-center gap-1">
              {files.data?.crumbs.map((crumb, index) => (
                <li
                  key={crumb.path}
                  className="inline-flex min-w-0 max-w-full items-center gap-1"
                >
                  {index > 0 && (
                    <span aria-hidden="true" className="text-muted-foreground">
                      /
                    </span>
                  )}
                  {index === files.data!.crumbs.length - 1 ? (
                    <span
                      aria-current="page"
                      className="max-w-full truncate px-2 font-medium"
                      title={crumb.name}
                    >
                      {crumb.name}
                    </span>
                  ) : (
                    <Button
                      className="max-w-full truncate"
                      variant="ghost"
                      size="sm"
                      disabled={busy}
                      onClick={() => setDir(crumb.path)}
                    >
                      {crumb.name}
                    </Button>
                  )}
                </li>
              ))}
            </ol>
          </nav>
          <div className="w-full sm:w-64">
            <Label htmlFor="file-search" className="sr-only">
              筛选当前目录
            </Label>
            <Input
              id="file-search"
              placeholder="筛选当前目录…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
        </div>
        {!!files.data?.warnings.length && (
          <div className="mb-4">
            <Notice tone="warning">
              部分条目无法读取，列表不完整：
              <ul className="mt-1 break-all">
                {files.data.warnings.map((warning) => (
                  <li key={warning}>{warning}</li>
                ))}
              </ul>
            </Notice>
          </div>
        )}
        {files.isPending ? (
          <Spinner />
        ) : files.error ? (
          <Notice tone="error">无法读取目录：{files.error.message}</Notice>
        ) : items.length === 0 ? (
          <EmptyState
            title={
              search
                ? "没有匹配的文件"
                : files.data?.warnings.length
                  ? "没有可显示的文件"
                  : "目录为空"
            }
            description={
              search
                ? "试试其他文件名。"
                : files.data?.warnings.length
                  ? "仍有条目读取失败，不能判定目录为空。"
                  : "可以上传文件或新建目录。"
            }
          />
        ) : (
          <div className="overflow-hidden rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead className="hidden sm:table-cell">大小</TableHead>
                  <TableHead className="hidden lg:table-cell">
                    修改时间
                  </TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((item) => (
                  <TableRow key={item.path}>
                    <TableCell>
                      <div className="flex items-center gap-3">
                        {item.is_dir ? (
                          <Folder className="size-5 shrink-0 text-primary" />
                        ) : (
                          <File className="size-5 shrink-0 text-muted-foreground" />
                        )}
                        <div className="min-w-0">
                          {item.is_dir ? (
                            <button
                              disabled={busy}
                              className="max-w-[40vw] truncate text-left font-medium hover:underline sm:max-w-xs"
                              title={item.name}
                              onClick={() => setDir(item.path)}
                            >
                              {item.name}
                            </button>
                          ) : (
                            <span
                              className="block max-w-[40vw] truncate sm:max-w-xs"
                              title={item.name}
                            >
                              {item.name}
                            </span>
                          )}
                          <span className="block text-xs text-muted-foreground sm:hidden">
                            {item.is_dir ? "目录" : formatBytes(item.size)}
                          </span>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className="hidden sm:table-cell">
                      {item.is_dir ? (
                        <Badge>目录</Badge>
                      ) : (
                        formatBytes(item.size)
                      )}
                    </TableCell>
                    <TableCell className="hidden text-muted-foreground lg:table-cell">
                      {formatDate(item.mtime)}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        {!item.is_dir && (
                          <Button asChild variant="ghost" size="icon">
                            <a
                              href={apiURL("download", {
                                file: item.path,
                                dl: 1,
                              })}
                              download={item.name}
                              aria-label={`下载 ${item.name}`}
                            >
                              <Download />
                            </a>
                          </Button>
                        )}
                        <DropdownMenu modal={false}>
                          <DropdownMenuTrigger asChild>
                            <Button
                              variant="ghost"
                              size="icon"
                              disabled={busy}
                              aria-label={`${item.name} 的更多操作`}
                            >
                              <MoreHorizontal />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            {!item.is_dir && isMediaFile(item.name) && (
                              <DropdownMenuItem asChild>
                                <a
                                  href={pageLink("album", {
                                    dir: parentPath(item.path),
                                    file: item.path,
                                  })}
                                >
                                  <Images /> 在相册中查看
                                </a>
                              </DropdownMenuItem>
                            )}
                            <DropdownMenuItem
                              onClick={() => {
                                setDialog({ kind: "rename", item });
                                setName(item.name);
                                setFormError("");
                              }}
                            >
                              <Pencil />
                              重命名
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              variant="destructive"
                              onClick={() => {
                                setRemoveItem(item);
                                setDeleteError("");
                              }}
                            >
                              <Trash2 />
                              删除
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
      <AlertDialog
        open={removeItem !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setRemoveItem(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              删除{removeItem?.is_dir ? "目录" : "文件"}？
            </AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除「{removeItem?.name}」
              {removeItem?.is_dir ? "及其全部内容" : ""}，不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          {deleteError && <Notice tone="error">{deleteError}</Notice>}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={busy}
              onClick={async (event) => {
                event.preventDefault();
                if (!removeItem) return;
                setBusy(true);
                setDeleteError("");
                try {
                  const result = await post<{ ok: boolean; warning?: string }>(
                    "delete",
                    { path: removeItem.path },
                    "DELETE",
                  );
                  setRemoveItem(null);
                  if (result.warning) {
                    setMessage(`文件已删除。${result.warning}`);
                  }
                  await refresh();
                } catch (error) {
                  setDeleteError(errorText(error));
                } finally {
                  setBusy(false);
                }
              }}
            >
              {busy ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <Modal
        open={dialog !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setDialog(null);
        }}
        title={dialog?.kind === "rename" ? "重命名" : "新建目录"}
        description="名称不能包含路径分隔符。同名目标不会被覆盖。"
      >
        <form
          className="space-y-4"
          onSubmit={async (event) => {
            event.preventDefault();
            if (!dialog) return;
            setBusy(true);
            setFormError("");
            try {
              await post(dialog.kind, {
                path: dialog.kind === "rename" ? dialog.item!.path : dir,
                name,
              });
              setDialog(null);
              await refresh();
            } catch (e) {
              setFormError(errorText(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          <div>
            <Label htmlFor="file-name">名称</Label>
            <Input
              autoFocus
              id="file-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
              maxLength={255}
              disabled={busy}
            />
          </div>
          {formError && <Notice tone="error">{formError}</Notice>}
          <div className="flex justify-end">
            <Button type="submit" disabled={busy || !name}>
              {busy ? "保存中…" : "保存"}
            </Button>
          </div>
        </form>
      </Modal>
    </>
  );
}
