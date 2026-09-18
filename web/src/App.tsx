import {
  Component,
  lazy,
  Suspense,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Camera,
  Folder,
  Images,
  LayoutDashboard,
  ListTree,
  Menu,
  PanelLeft,
  Settings2,
  Wifi,
  X,
} from "lucide-react";
import { get, invalidate, post, useAPI } from "./api";
import type { CameraStatus, Config } from "./types";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ThemeSwitch } from "@/components/theme-switch";
import { Notice, Spinner } from "./ui";
import { UploadCenter } from "./components/upload-center";
import { useViewState } from "./lib/view-state";
import { Badge } from "./components/ui/badge";
import { OverviewPage } from "./pages/OverviewPage";

const CameraPage = lazy(() =>
  import("./pages/CameraPage").then((page) => ({ default: page.CameraPage })),
);
const FilesPage = lazy(() =>
  import("./pages/FilesPage").then((page) => ({ default: page.FilesPage })),
);
const AlbumPage = lazy(() =>
  import("./pages/AlbumPage").then((page) => ({ default: page.AlbumPage })),
);
const NetworkPage = lazy(() =>
  import("./pages/NetworkPage").then((page) => ({ default: page.NetworkPage })),
);
const SystemPage = lazy(() =>
  import("./pages/SystemPage").then((page) => ({ default: page.SystemPage })),
);
const ProcessPage = lazy(() =>
  import("./pages/ProcessPage").then((page) => ({ default: page.ProcessPage })),
);

const navigation = [
  { id: "overview", name: "概览", icon: LayoutDashboard, page: OverviewPage },
  { id: "camera", name: "实时画面", icon: Camera, page: CameraPage },
  { id: "album", name: "相册", icon: Images, page: AlbumPage },
  { id: "files", name: "文件", icon: Folder, page: FilesPage },
  { id: "network", name: "网络", icon: Wifi, page: NetworkPage },
  { id: "processes", name: "进程", icon: ListTree, page: ProcessPage },
  { id: "system", name: "系统", icon: Settings2, page: SystemPage },
];
class PageBoundary extends Component<
  { children: ReactNode },
  { error: string }
> {
  state = { error: "" };
  static getDerivedStateFromError(error: Error) {
    return { error: error.message };
  }
  render() {
    return this.state.error ? (
      <Notice tone="error">
        页面出错：{this.state.error}
        <Button
          className="ml-3"
          variant="outline"
          onClick={() => location.reload()}
        >
          重新载入
        </Button>
      </Notice>
    ) : (
      this.props.children
    );
  }
}
export function App() {
  const [mobile, setMobile] = useState(false);
  const [collapsed, setCollapsed] = useViewState("sidebar.collapsed", false);
  const [theme, setTheme] = useState<Config["theme_mode"]>("auto");
  const [themeBusy, setThemeBusy] = useState(false);
  const [route, setRoute] = useState(
    location.hash.replace(/^#\/?/, "") || "overview",
  );
  const health = useAPI<{ ok: boolean; version: string; device: boolean }>(
    "health",
    undefined,
    5000,
  );
  const camera = useAPI<CameraStatus>("camera_status", undefined, 3000);
  const config = useQuery<Config>({
    queryKey: ["config", {}],
    queryFn: ({ signal }) => get<Config>("config", undefined, signal),
  });
  useEffect(() => {
    const onHash = () => {
      setRoute(location.hash.replace(/^#\/?/, "") || "overview");
      setMobile(false);
    };
    window.addEventListener("hashchange", onHash);
    return () => {
      window.removeEventListener("hashchange", onHash);
    };
  }, []);
  useEffect(() => {
    window.scrollTo({ top: 0, behavior: "instant" });
  }, [route]);
  useEffect(() => {
    if (!mobile) return;
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMobile(false);
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [mobile]);
  useEffect(() => {
    if (config.data) setTheme(config.data.theme_mode);
  }, [config.data?.theme_mode]);
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    const update = () =>
      document.documentElement.classList.toggle(
        "dark",
        theme === "dark" || (theme === "auto" && media.matches),
      );
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [theme]);
  async function changeTheme(next: Config["theme_mode"]) {
    const previous = theme;
    setTheme(next);
    setThemeBusy(true);
    try {
      await post("config", { theme_mode: next });
      await invalidate("config");
    } catch {
      setTheme(previous);
    } finally {
      setThemeBusy(false);
    }
  }
  const active =
    navigation.find((item) => item.id === route.split("?")[0]) ?? navigation[0];
  const Page = active.page;
  return (
    <div className="min-h-dvh">
      {mobile && (
        <button
          aria-label="关闭导航"
          className="fixed inset-0 z-30 bg-black/40 md:hidden"
          onClick={() => setMobile(false)}
        />
      )}
      <aside
        id="app-navigation"
        className={cn(
          "fixed inset-y-0 left-0 z-40 flex w-64 flex-col bg-sidebar text-sidebar-foreground shadow-[inset_-1px_0_0_var(--sidebar-border)] transition-[width,transform] md:translate-x-0",
          mobile ? "translate-x-0" : "-translate-x-full invisible md:visible",
          collapsed && "md:w-16",
        )}
      >
        <div className="p-2">
          <a
            href="#/overview"
            aria-label="Action Control 设备概览"
            className="flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-sidebar-accent"
          >
            <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
              <Camera className="size-4" />
            </span>
            <div className={cn("min-w-0 flex-1", collapsed && "md:hidden")}>
              <span className="block truncate text-sm font-semibold">
                Action Control
              </span>
              <span className="block truncate text-xs text-muted-foreground">
                Osmo Action 5 Pro
              </span>
            </div>
          </a>
        </div>
        <div className="flex-1 overflow-y-auto p-2 pt-0">
          <p
            className={cn(
              "px-2 py-1.5 text-xs font-medium text-sidebar-foreground/70",
              collapsed && "md:sr-only",
            )}
          >
            控制台
          </p>
          <nav aria-label="主导航" className="space-y-1">
            {navigation.map((item) => (
              <a
                key={item.id}
                href={`#/${item.id}`}
                title={collapsed ? item.name : undefined}
                aria-label={item.name}
                aria-current={active.id === item.id ? "page" : undefined}
                className={cn(
                  "flex h-10 items-center gap-2 rounded-md px-2 text-sm font-medium outline-none transition-[width,height,padding] hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring md:h-9",
                  collapsed && "md:justify-center",
                  active.id === item.id
                    ? "bg-sidebar-accent text-sidebar-accent-foreground"
                    : "text-sidebar-foreground",
                )}
              >
                <item.icon className="size-4" />
                <span className={collapsed ? "md:sr-only" : undefined}>
                  {item.name}
                </span>
              </a>
            ))}
          </nav>
        </div>
        <div className="p-2">
          <div className="flex items-center gap-3 rounded-md px-2 py-2">
            <span className="relative flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted text-xs font-semibold">
              A5
              <span
                className={cn(
                  "absolute -bottom-0.5 -right-0.5 size-2.5 rounded-full border-2 border-sidebar",
                  health.isPending
                    ? "bg-muted-foreground"
                    : health.isError
                      ? "bg-amber-500"
                      : "bg-emerald-500",
                )}
              />
            </span>
            <div className={cn("min-w-0 flex-1", collapsed && "md:hidden")}>
              <p className="truncate text-sm font-medium">
                {health.isPending
                  ? "正在连接"
                  : health.isError
                    ? "连接中断"
                    : health.data?.device
                      ? "设备在线"
                      : "本地文件模式"}
              </p>
              <p className="truncate text-xs text-muted-foreground">
                v{health.data?.version ?? "—"} · {location.host}
              </p>
            </div>
          </div>
        </div>
      </aside>
      <div
        className={cn(
          "relative min-h-svh transition-[margin]",
          collapsed ? "md:ml-16" : "md:ml-64",
        )}
      >
        <header className="sticky top-0 z-20 h-16 border-b bg-background/95 backdrop-blur">
          <div className="relative flex h-full items-center gap-3 p-4 sm:gap-4">
            <Button
              variant="ghost"
              size="icon"
              className="size-11 md:hidden"
              aria-expanded={mobile}
              aria-controls="app-navigation"
              aria-label={mobile ? "关闭导航" : "打开导航"}
              onClick={() => setMobile(!mobile)}
            >
              {mobile ? <X /> : <Menu />}
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="hidden size-9 md:flex"
              aria-label={collapsed ? "展开侧栏" : "收起侧栏"}
              aria-expanded={!collapsed}
              aria-controls="app-navigation"
              onClick={() => setCollapsed(!collapsed)}
            >
              <PanelLeft className="size-4" />
            </Button>
            <span className="h-6 w-px bg-border" />
            <span className="truncate text-sm font-medium">{active.name}</span>
            <div className="ml-auto flex items-center gap-2">
              <UploadCenter />
              <ThemeSwitch
                theme={theme}
                disabled={themeBusy || config.isPending}
                onThemeChange={(value) => void changeTheme(value)}
              />
            </div>
          </div>
        </header>
        <main className="mx-auto max-w-7xl px-4 py-6">
          {health.isError ? (
            <div className="mb-6">
              <Notice tone="warning">
                连接暂时中断。设备可能仍在采集；请勿根据断连重复执行控制操作。
              </Notice>
            </div>
          ) : health.data?.device === false ? (
            <div className="mb-6">
              <Notice>
                本地文件模式 ·
                文件与网页功能可用，相机、无线和系统控制未连接真实设备。
              </Notice>
            </div>
          ) : null}
          {health.data?.device && active.id !== "camera" && (
            <div className="mb-5 flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-card px-4 py-3 text-sm">
              <div className="flex flex-wrap items-center gap-3">
                <Badge
                  variant={
                    camera.isError || camera.data?.state === "error"
                      ? "destructive"
                      : "secondary"
                  }
                >
                  {camera.isPending
                    ? "读取采集状态…"
                    : camera.isError
                      ? "采集状态未知"
                      : camera.data?.running
                        ? "正在采集"
                        : camera.data?.state === "starting"
                          ? "正在启动采集"
                          : camera.data?.state === "stopping"
                            ? "正在停止采集"
                            : camera.data?.state === "error"
                              ? "采集异常"
                              : "采集已停止"}
                </Badge>
                {camera.data?.running && !camera.isError && (
                  <span className="tabular-nums text-muted-foreground">
                    已运行 {Math.floor(camera.data.uptime_sec / 60)} 分{" "}
                    {Math.floor(camera.data.uptime_sec % 60)} 秒
                  </span>
                )}
                {camera.data?.reboot_required && (
                  <a
                    className="text-amber-700 underline underline-offset-4 dark:text-amber-300"
                    href="#/system?section=maintenance"
                  >
                    恢复原生拍摄需要整机重启
                  </a>
                )}
              </div>
              <a
                href="#/camera"
                className="font-medium underline underline-offset-4"
              >
                查看画面与控制采集
              </a>
            </div>
          )}
          <PageBoundary key={route}>
            <Suspense fallback={<Spinner />}>
              <Page />
            </Suspense>
          </PageBoundary>
        </main>
      </div>
    </div>
  );
}
