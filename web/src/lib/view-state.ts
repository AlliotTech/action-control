import { useEffect, useLayoutEffect, useRef, useState } from "react";

const memory = new Map<string, unknown>();
const prefix = "action-control.view.";

function read<T>(key: string, fallback: T): T {
  try {
    const value = memory.has(key)
      ? memory.get(key)
      : JSON.parse(sessionStorage.getItem(prefix + key) ?? "null");
    if (value !== null && typeof value === typeof fallback) {
      if (typeof value === "number" && !Number.isFinite(value)) return fallback;
      return value as T;
    }
  } catch {
    // Browsing still works when storage is unavailable or has old data.
  }
  return fallback;
}

function save(key: string, value: unknown) {
  memory.set(key, value);
  try {
    sessionStorage.setItem(prefix + key, JSON.stringify(value));
  } catch {
    // Keep the in-memory state for navigation in this tab.
  }
}

export function useViewState<T>(key: string, fallback: T, initial?: T) {
  const [value, setValue] = useState<T>(() => initial ?? read(key, fallback));
  useEffect(() => save(key, value), [key, value]);
  return [value, setValue] as const;
}

export function usePageScroll(key: string, ready: boolean) {
  const position = useRef(read(`scroll.${key}`, 0));
  const restored = useRef(false);
  useLayoutEffect(() => {
    if (!ready || restored.current) return;
    const frame = requestAnimationFrame(() => {
      window.scrollTo({
        top: Math.max(0, position.current),
        behavior: "instant",
      });
      restored.current = true;
    });
    return () => cancelAnimationFrame(frame);
  }, [ready]);
  useLayoutEffect(() => {
    const remember = () => {
      if (restored.current) save(`scroll.${key}`, window.scrollY);
    };
    window.addEventListener("scroll", remember, { passive: true });
    return () => {
      remember();
      window.removeEventListener("scroll", remember);
    };
  }, [key]);
}

export function routeParam(name: string): string | undefined {
  return (
    new URLSearchParams(location.hash.split("?")[1]).get(name) ?? undefined
  );
}

export function pageLink(
  page: "files" | "album",
  params: Record<string, string>,
) {
  return `#/${page}?${new URLSearchParams(params)}`;
}

export function storageRoot(path: string) {
  return path === "/sd" || path.startsWith("/sd/")
    ? "/sd"
    : path === "/emulated" || path.startsWith("/emulated/")
      ? "/emulated"
      : "/";
}

export function parentPath(path: string) {
  return path.slice(0, path.lastIndexOf("/")) || "/";
}

export function isMediaFile(name: string) {
  return /\.(jpe?g|png|gif|webp|bmp|svg|mp4|webm|avi|mov|mkv|mpg|mpeg|3gp)$/i.test(
    name,
  );
}
