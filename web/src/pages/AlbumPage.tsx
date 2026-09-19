import { useEffect, useRef, useState } from "react";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  Download,
  Folder,
  Image,
  Maximize,
  Minimize,
  Pencil,
  Play,
  RefreshCw,
  Trash2,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import { apiURL, errorText, get, invalidate, post, useAPI } from "../api";
import type {
  MediaIndexStatus,
  MediaInfo,
  MediaItem,
  MediaListing,
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
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  ConfirmButton,
  EmptyState,
  Modal,
  Notice,
  PageHeader,
  Spinner,
  formatBytes,
  formatDate,
} from "../ui";
import {
  pageLink,
  parentPath,
  routeParam,
  storageRoot,
  usePageScroll,
  useViewState,
} from "../lib/view-state";

function infoKey(item: MediaItem) {
  return ["media_info", { file: item.path, mtime: item.mtime }] as const;
}

function Thumbnail({ item }: { item: MediaItem }) {
  const [failed, setFailed] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const info = useQuery<MediaInfo>({
    queryKey: infoKey(item),
    queryFn: ({ signal }) => get("media_info", { file: item.path }, signal),
    enabled: item.type === "video" && loaded,
    staleTime: Infinity,
  });
  const duration = info.data?.duration_sec;
  return (
    <>
      {failed ? (
        <span className="flex h-full flex-col items-center justify-center gap-2 text-xs text-muted-foreground">
          <Image className="size-7" />
          无缩略图
        </span>
      ) : (
        <img
          loading="lazy"
          decoding="async"
          src={apiURL("thumbnail", { file: item.path, v: item.mtime })}
          alt=""
          className="h-full w-full object-cover transition-transform group-hover:scale-[1.03]"
          onLoad={() => setLoaded(true)}
          onError={() => setFailed(true)}
        />
      )}
      {item.type === "video" && (
        <span className="pointer-events-none absolute bottom-2 left-2 flex items-center gap-1 rounded bg-black/70 px-2 py-1 text-xs text-white">
          <Play className="size-3" />
          {duration == null
            ? "视频"
            : `${Math.floor(duration / 60)}:${String(Math.floor(duration % 60)).padStart(2, "0")}`}
        </span>
      )}
    </>
  );
}

export function AlbumPage() {
  const linkedFile = routeParam("file");
  const [dir, setDir] = useViewState(
    "album.dir",
    "/emulated",
    routeParam("dir"),
  );
  const [filter, setFilter] = useViewState(
    "album.filter",
    "all",
    linkedFile ? "all" : undefined,
  );
  const [search, setSearch] = useViewState(
    "album.search",
    "",
    linkedFile?.split("/").pop(),
  );
  const [sort, setSort] = useViewState("album.sort", "newest");
  const [querySearch, setQuerySearch] = useState(search);
  const [selected, setSelected] = useState<Map<string, MediaItem>>(new Map());
  const [preview, setPreview] = useState<string | null>(linkedFile ?? null);
  const [previewError, setPreviewError] = useState("");
  const [rename, setRename] = useState<MediaItem | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [zoom, setZoom] = useState(1);
  const [fullscreen, setFullscreen] = useState(false);
  const stage = useRef<HTMLDivElement>(null);
  const touch = useRef<{ x: number; y: number } | null>(null);
  const queryClient = useQueryClient();
  useEffect(() => {
    const timer = window.setTimeout(() => setQuerySearch(search), 250);
    return () => window.clearTimeout(timer);
  }, [search]);
  const mediaKey = [
    "media_list",
    { dir, type: filter, search: querySearch, sort },
  ] as const;
  const pageParams = {
    dir,
    type: filter,
    search: querySearch,
    sort,
    limit: 80,
  };
  const media = useInfiniteQuery({
    queryKey: mediaKey,
    queryFn: ({ pageParam, signal }) =>
      get<MediaListing>(
        "media_list",
        { ...pageParams, cursor: pageParam || undefined },
        signal,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  usePageScroll("album", !media.isPending);
  const mediaIndex = useAPI<MediaIndexStatus>("media_index_status", {
    storage: storageRoot(dir) === "/sd" ? "sd" : "emulated",
  });
  const items = media.data?.pages.flatMap((page) => page.items) ?? [];
  const listing = media.data?.pages[0];
  const current = items.find((item) => item.path === preview);
  const index = items.findIndex((item) => item.path === preview);
  const selectedItems = [...selected.values()];
  const info = useQuery<MediaInfo>({
    queryKey: current ? infoKey(current) : ["media_info", { file: preview }],
    queryFn: ({ signal }) => get("media_info", { file: preview! }, signal),
    enabled: Boolean(current),
    staleTime: Infinity,
  });
  useEffect(() => {
    setSelected(new Map());
  }, [dir, filter, querySearch, sort]);
  useEffect(() => {
    setPreviewError("");
    setZoom(1);
  }, [preview]);
  useEffect(() => {
    const update = () =>
      setFullscreen(document.fullscreenElement === stage.current);
    document.addEventListener("fullscreenchange", update);
    return () => document.removeEventListener("fullscreenchange", update);
  }, []);

  async function refresh() {
    setRefreshing(true);
    setError("");
    try {
      await queryClient.cancelQueries({ queryKey: mediaKey, exact: true });
      const first = await get<MediaListing>("media_list", {
        ...pageParams,
        refresh: true,
      });
      queryClient.setQueryData(mediaKey, { pages: [first], pageParams: [""] });
      await invalidate("list", "disk_usage", "media_index_status");
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setRefreshing(false);
    }
  }

  function changeDir(value: string) {
    setDir(value);
    setSearch("");
    setQuerySearch("");
    setPreview(null);
  }
  function step(direction: number) {
    const item = items[index + direction];
    if (item) setPreview(item.path);
  }
  function toggle(item: MediaItem) {
    setSelected((previous) => {
      const next = new Map(previous);
      if (next.has(item.path)) next.delete(item.path);
      else next.set(item.path, item);
      return next;
    });
  }
  function download(files: MediaItem[]) {
    for (const item of files) {
      const link = document.createElement("a");
      link.href = apiURL("download", { file: item.path, dl: 1 });
      link.download = item.name;
      document.body.append(link);
      link.click();
      link.remove();
    }
  }
  async function remove(files: MediaItem[]) {
    setBusy(true);
    setError("");
    const warnings: string[] = [];
    try {
      for (const item of files) {
        let result: { ok: boolean; warning?: string };
        try {
          result = await post<{ ok: boolean; warning?: string }>(
            "delete",
            { path: item.path },
            "DELETE",
          );
        } catch (cause) {
          throw new Error(
            `「${item.name}」删除失败：${errorText(cause)}；已删除文件不会恢复，其余文件保持选中。`,
          );
        }
        // File is deleted even when the index sync is deferred; deselect it.
        if (result.warning) warnings.push(`「${item.name}」：${result.warning}`);
        setSelected((previous) => {
          const next = new Map(previous);
          next.delete(item.path);
          return next;
        });
        if (preview === item.path) setPreview(null);
      }
      if (warnings.length) {
        setError(
          `文件已删除。相机原生服务占用媒体索引，可稍后在系统页“清理失效索引”同步：${warnings.join("；")}`,
        );
      }
    } finally {
      setBusy(false);
      await refresh();
    }
  }
  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else if (stage.current?.requestFullscreen)
        await stage.current.requestFullscreen();
      else setPreviewError("此浏览器不支持全屏，可以使用图片缩放查看细节。");
    } catch {
      setPreviewError("浏览器未允许全屏，请使用图片缩放查看细节。");
    }
  }

  const groups: { name: string; items: MediaItem[] }[] = [];
  for (const item of items) {
    const date =
      sort === "newest" || sort === "oldest"
        ? new Date(item.mtime * 1000).toLocaleDateString("zh-CN", {
            year: "numeric",
            month: "long",
            day: "numeric",
          })
        : "";
    let group = groups[groups.length - 1];
    if (!group || group.name !== date) {
      group = { name: date, items: [] };
      groups.push(group);
    }
    group.items.push(item);
  }

  return (
    <>
      <PageHeader
        title="相册"
        description="按日期查找照片和视频，预览或下载原文件。"
        actions={
          <Button
            variant="outline"
            disabled={media.isFetching || busy || refreshing}
            onClick={() => void refresh()}
          >
            <RefreshCw className={refreshing ? "animate-spin" : undefined} />
            刷新
          </Button>
        }
      />
      <div className="mb-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-[160px_220px_minmax(160px,1fr)_150px]">
        <div>
          <Label htmlFor="album-storage">存储位置</Label>
          <Select
            value={storageRoot(dir)}
            disabled={busy}
            onValueChange={changeDir}
          >
            <SelectTrigger id="album-storage" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="/emulated">内部存储</SelectItem>
              <SelectItem value="/sd">SD 卡</SelectItem>
              <SelectItem value="/">Blackbox</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div>
          <Label id="album-kind-label">媒体类型</Label>
          <Tabs
            value={filter}
            onValueChange={setFilter}
            aria-labelledby="album-kind-label"
          >
            <TabsList className="w-full">
              <TabsTrigger value="all">全部</TabsTrigger>
              <TabsTrigger value="image">照片</TabsTrigger>
              <TabsTrigger value="video">视频</TabsTrigger>
            </TabsList>
          </Tabs>
        </div>
        <div>
          <Label htmlFor="album-search">文件名</Label>
          <Input
            id="album-search"
            placeholder="搜索媒体…"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </div>
        <div>
          <Label htmlFor="album-sort">排序</Label>
          <Select value={sort} onValueChange={setSort}>
            <SelectTrigger id="album-sort" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="newest">最近修改</SelectItem>
              <SelectItem value="oldest">最早修改</SelectItem>
              <SelectItem value="name">文件名称</SelectItem>
              <SelectItem value="largest">文件大小</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      {dir !== storageRoot(dir) && (
        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          <Folder className="size-4" />
          <span className="break-all">{dir}</span>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => changeDir(storageRoot(dir))}
          >
            查看整个存储
          </Button>
        </div>
      )}
      {error && (
        <div className="mb-4">
          <Notice tone="error">{error}</Notice>
        </div>
      )}
      {listing?.truncated && (
        <div className="mb-4">
          <Notice tone="warning">
            列表达到 100,000 项或目录深度限制。可从文件页进入具体文件夹浏览。
          </Notice>
        </div>
      )}
      {!!listing?.warnings.length && (
        <details className="mb-4 rounded-lg border p-3 text-sm">
          <summary className="cursor-pointer text-amber-700 dark:text-amber-300">
            部分目录无法读取，列表可能不完整
          </summary>
          <ul className="mt-2 break-all text-muted-foreground">
            {listing.warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        </details>
      )}
      {mediaIndex.data?.available && mediaIndex.data.stale > 0 && (
        <div className="mb-4">
          <Notice tone="warning">
            发现 {mediaIndex.data.stale} 条失效媒体记录。
            <a
              href="#/system?section=maintenance"
              className="ml-2 underline underline-offset-4"
            >
              前往备份与清理
            </a>
          </Notice>
        </div>
      )}
      {selectedItems.length > 0 && (
        <div className="sticky top-16 z-10 mb-5 flex flex-wrap items-center gap-2 rounded-lg border bg-card/95 p-3 shadow-sm backdrop-blur">
          <Badge>
            已选 {selectedItems.length} 项 ·{" "}
            {formatBytes(
              selectedItems.reduce((total, item) => total + item.size, 0),
            )}
          </Badge>
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => setSelected(new Map())}
          >
            取消选择
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => download(selectedItems)}
          >
            <Download />
            下载
          </Button>
          {selectedItems.length === 1 && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => {
                setRename(selectedItems[0]);
                setName(selectedItems[0].name);
                setError("");
              }}
            >
              <Pencil />
              重命名
            </Button>
          )}
          <ConfirmButton
            title={`删除 ${selectedItems.length} 个文件？`}
            description="永久删除所选媒体，不可撤销。遇到失败会停止，未删除文件保持选中。"
            variant="destructive"
            disabled={busy}
            onConfirm={() => remove(selectedItems)}
          >
            <Trash2 />
            删除
          </ConfirmButton>
          {selectedItems.length > 1 && (
            <span className="w-full text-xs text-muted-foreground">
              批量下载时，浏览器可能要求允许多个文件下载。
            </span>
          )}
        </div>
      )}
      {media.isPending ? (
        <Spinner />
      ) : media.isError && !items.length ? (
        <Notice tone="error">
          无法读取媒体：{media.error.message}
          <Button
            variant="outline"
            className="mt-2"
            onClick={() => void refresh()}
          >
            重新读取
          </Button>
        </Notice>
      ) : !items.length ? (
        <EmptyState
          title="没有找到媒体"
          description={
            search || filter !== "all"
              ? "调整文件名或媒体类型筛选。"
              : "此存储中尚无可浏览的照片或视频。"
          }
          action={
            search || filter !== "all" ? (
              <Button
                variant="outline"
                onClick={() => {
                  setSearch("");
                  setFilter("all");
                }}
              >
                清除筛选
              </Button>
            ) : undefined
          }
        />
      ) : (
        <>
          <div className="mb-4 flex flex-wrap items-center justify-between gap-2 text-sm text-muted-foreground">
            <span>
              已加载 {items.length} / {listing?.count ?? items.length} 个文件
              {search !== querySearch ? " · 筛选中…" : ""}
            </span>
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() =>
                setSelected(new Map(items.map((item) => [item.path, item])))
              }
            >
              选择已加载项
            </Button>
          </div>
          <div className="space-y-7">
            {groups.map((group) => (
              <section key={group.name}>
                {group.name && (
                  <h2 className="mb-3 text-sm font-medium">
                    {group.name}
                    <span className="ml-2 text-xs font-normal text-muted-foreground">
                      修改日期
                    </span>
                  </h2>
                )}
                <ul className="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-4">
                  {group.items.map((item) => (
                    <li
                      key={`${item.path}:${item.mtime}`}
                      className="group min-w-0 overflow-hidden rounded-lg border bg-card transition-shadow hover:shadow-md"
                    >
                      <div className="relative aspect-[4/3] bg-secondary">
                        <button
                          className="h-full w-full overflow-hidden focus-visible:outline-2 focus-visible:outline-primary"
                          aria-label={`预览 ${item.name}`}
                          onClick={() => setPreview(item.path)}
                        >
                          <Thumbnail item={item} />
                        </button>
                        <Checkbox
                          checked={selected.has(item.path)}
                          disabled={busy}
                          aria-label={`${selected.has(item.path) ? "取消选择" : "选择"} ${item.name}`}
                          className="absolute left-2 top-2 z-10 size-6 border-white/70 bg-black/30 text-white shadow-sm md:size-5"
                          onCheckedChange={() => toggle(item)}
                        />
                      </div>
                      <div className="p-3">
                        <p
                          className="truncate text-sm font-medium"
                          title={item.name}
                        >
                          {item.name}
                        </p>
                        <div className="mt-1 flex flex-wrap justify-between gap-1 text-xs text-muted-foreground">
                          <span>{formatBytes(item.size)}</span>
                          <span>{item.ext.slice(1).toUpperCase()}</span>
                        </div>
                      </div>
                    </li>
                  ))}
                </ul>
              </section>
            ))}
          </div>
          {media.isFetchNextPageError && (
            <div className="mt-4">
              <Notice tone="warning">
                {media.error?.message}
                <Button
                  variant="outline"
                  className="ml-2"
                  onClick={() => void refresh()}
                >
                  刷新列表
                </Button>
              </Notice>
            </div>
          )}
          {media.hasNextPage && (
            <div className="mt-6 text-center">
              <Button
                variant="outline"
                disabled={media.isFetching || refreshing}
                onClick={() => void media.fetchNextPage()}
              >
                {media.isFetchingNextPage
                  ? "正在加载…"
                  : `加载更多（剩余 ${(listing?.count ?? 0) - items.length} 项）`}
              </Button>
            </div>
          )}
        </>
      )}
      <Modal
        open={Boolean(current)}
        onOpenChange={(open) => {
          if (!open) setPreview(null);
        }}
        title={current?.name ?? "媒体预览"}
        description={
          current
            ? `${formatBytes(current.size)} · ${formatDate(current.mtime)}`
            : undefined
        }
        size="media"
      >
        {current && (
          <div
            className="min-w-0"
            onKeyDown={(event) => {
              if (
                (event.target as HTMLElement).closest(
                  "video,input,textarea,select,[role=combobox]",
                )
              )
                return;
              if (event.key === "ArrowLeft") {
                event.preventDefault();
                step(-1);
              }
              if (event.key === "ArrowRight") {
                event.preventDefault();
                step(1);
              }
            }}
          >
            <div
              ref={stage}
              className={`media-stage relative flex min-h-48 max-h-[65dvh] overflow-auto rounded-lg bg-black ${zoom === 1 ? "items-center justify-center" : "items-start justify-start"}`}
              onTouchStart={(event) => {
                touch.current =
                  zoom !== 1 || (event.target as HTMLElement).closest("video")
                    ? null
                    : {
                        x: event.touches[0].clientX,
                        y: event.touches[0].clientY,
                      };
              }}
              onTouchEnd={(event) => {
                if (touch.current) {
                  const dx = event.changedTouches[0].clientX - touch.current.x;
                  const dy = event.changedTouches[0].clientY - touch.current.y;
                  if (Math.abs(dx) > 70 && Math.abs(dx) > Math.abs(dy) * 1.5)
                    step(dx > 0 ? -1 : 1);
                  touch.current = null;
                }
              }}
            >
              {current.type === "video" ? (
                <video
                  key={current.path}
                  controls
                  playsInline
                  preload="metadata"
                  className="max-h-[65dvh] w-full"
                  src={apiURL("video_stream", { file: current.path })}
                  onError={() =>
                    setPreviewError(
                      "浏览器无法播放此视频，请下载原文件用支持相应编码的播放器打开。",
                    )
                  }
                />
              ) : (
                <img
                  key={current.path}
                  src={apiURL("download", {
                    file: current.path,
                    v: current.mtime,
                  })}
                  alt={current.name}
                  className={
                    zoom === 1
                      ? "max-h-[65dvh] max-w-full object-contain"
                      : "max-w-none shrink-0"
                  }
                  style={zoom === 1 ? undefined : { width: `${zoom * 100}%` }}
                  onError={() =>
                    setPreviewError("图片无法预览，请下载原文件查看。")
                  }
                />
              )}
              {fullscreen && (
                <Button
                  variant="secondary"
                  className="fixed right-4 top-4"
                  onClick={() => void toggleFullscreen()}
                >
                  <Minimize />
                  退出全屏
                </Button>
              )}
            </div>
            {previewError && (
              <div className="mt-3">
                <Notice tone="warning">{previewError}</Notice>
              </div>
            )}
            <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  variant="outline"
                  size="icon"
                  aria-label="上一项"
                  disabled={index <= 0}
                  onClick={() => step(-1)}
                >
                  <ChevronLeft />
                </Button>
                <span className="text-sm tabular-nums">
                  {index + 1} / {listing?.count ?? items.length}
                </span>
                <Button
                  variant="outline"
                  size="icon"
                  aria-label="下一项"
                  disabled={index < 0 || index >= items.length - 1}
                  onClick={() => step(1)}
                >
                  <ChevronRight />
                </Button>
                {index === items.length - 1 && media.hasNextPage && (
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={media.isFetching}
                    onClick={() => void media.fetchNextPage()}
                  >
                    加载后续媒体
                  </Button>
                )}
              </div>
              <div className="flex flex-wrap gap-2">
                {current.type === "image" && (
                  <>
                    <Button
                      variant="outline"
                      size="icon"
                      aria-label="缩小图片"
                      disabled={zoom === 1}
                      onClick={() =>
                        setZoom((value) => Math.max(1, value - 0.5))
                      }
                    >
                      <ZoomOut />
                    </Button>
                    <Button variant="ghost" onClick={() => setZoom(1)}>
                      {zoom === 1 ? "适应窗口" : `${zoom * 100}%`}
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      aria-label="放大图片"
                      disabled={zoom >= 4}
                      onClick={() =>
                        setZoom((value) => Math.min(4, value + 0.5))
                      }
                    >
                      <ZoomIn />
                    </Button>
                  </>
                )}
                <Button
                  variant="outline"
                  onClick={() => void toggleFullscreen()}
                >
                  <Maximize />
                  全屏
                </Button>
              </div>
            </div>
            <div className="mt-4 flex flex-wrap gap-2 border-t pt-4">
              <Button asChild variant="outline">
                <a
                  href={apiURL("download", { file: current.path, dl: 1 })}
                  download={current.name}
                >
                  <Download />
                  下载原文件
                </a>
              </Button>
              <Button asChild variant="outline">
                <a
                  href={pageLink("files", {
                    dir: parentPath(current.path),
                    search: current.name,
                  })}
                >
                  <Folder />
                  所在文件夹
                </a>
              </Button>
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => {
                  setRename(current);
                  setName(current.name);
                  setError("");
                }}
              >
                <Pencil />
                重命名
              </Button>
              <ConfirmButton
                title="删除此媒体？"
                description={`将永久删除「${current.name}」，不可撤销。`}
                disabled={busy}
                onConfirm={() => remove([current])}
              >
                <Trash2 />
                删除
              </ConfirmButton>
            </div>
            <details className="mt-4 rounded-lg border p-3 text-sm">
              <summary className="cursor-pointer font-medium">媒体信息</summary>
              <div className="mt-3 break-all text-muted-foreground">
                <p>{current.path}</p>
                {info.isPending ? (
                  <Spinner />
                ) : info.error ? (
                  <Notice tone="warning">{info.error.message}</Notice>
                ) : (
                  info.data && (
                    <dl className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
                      <div>
                        <dt>分辨率</dt>
                        <dd>
                          {info.data.width} × {info.data.height}
                        </dd>
                      </div>
                      <div>
                        <dt>编码</dt>
                        <dd>{info.data.codec}</dd>
                      </div>
                      {info.data.fps != null && (
                        <div>
                          <dt>帧率</dt>
                          <dd>{info.data.fps.toFixed(2)} fps</dd>
                        </div>
                      )}
                      {info.data.duration_sec != null && (
                        <div>
                          <dt>时长</dt>
                          <dd>{info.data.duration_sec.toFixed(1)} 秒</dd>
                        </div>
                      )}
                      {info.data.bitrate_bps != null && (
                        <div>
                          <dt>码率</dt>
                          <dd>
                            {(info.data.bitrate_bps / 1e6).toFixed(2)} Mbps
                          </dd>
                        </div>
                      )}
                    </dl>
                  )
                )}
              </div>
            </details>
          </div>
        )}
      </Modal>
      <Modal
        open={Boolean(rename)}
        onOpenChange={(open) => {
          if (!open && !busy) setRename(null);
        }}
        title="重命名媒体"
        description="请保留正确的文件扩展名。同名文件不会被覆盖。"
      >
        <form
          className="space-y-4"
          onSubmit={async (event) => {
            event.preventDefault();
            if (!rename) return;
            setBusy(true);
            setError("");
            try {
              await post("rename", { path: rename.path, name });
              setSelected((previous) => {
                const next = new Map(previous);
                next.delete(rename.path);
                return next;
              });
              setPreview(null);
              setRename(null);
              await refresh();
            } catch (cause) {
              setError(errorText(cause));
            } finally {
              setBusy(false);
            }
          }}
        >
          <Label htmlFor="media-name">名称</Label>
          <Input
            id="media-name"
            autoFocus
            required
            maxLength={255}
            value={name}
            disabled={busy}
            onChange={(event) => setName(event.target.value)}
          />
          {error && <Notice tone="error">{error}</Notice>}
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
