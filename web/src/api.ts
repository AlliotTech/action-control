import { QueryClient, useQuery } from "@tanstack/react-query";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false, staleTime: 3000 },
    mutations: { retry: false },
  },
});
type Params = Record<string, string | number | boolean | undefined>;
export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public code?: string,
  ) {
    super(message);
    this.name = "APIError";
  }
}
export function apiURL(name: string, params?: Params): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params ?? {}))
    if (value !== undefined) query.set(key, String(value));
  return `/api/${name}${query.size ? `?${query}` : ""}`;
}
export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
export async function responseValue<T>(response: Response): Promise<T> {
  if (!response.ok) {
    let message = `HTTP ${response.status}`;
    let code: string | undefined;
    try {
      const data = await response.json();
      message = data.error?.message ?? data.message ?? message;
      code = data.error?.code;
      if (data.files?.length) message += `；已保存：${data.files.join("、")}`;
    } catch {
      /* HTTP errors may have no JSON body. */
    }
    throw new APIError(message, response.status, code);
  }
  return response.json() as Promise<T>;
}
export function get<T>(
  name: string,
  params?: Params,
  signal?: AbortSignal,
): Promise<T> {
  return fetch(apiURL(name, params), {
    credentials: "same-origin",
    signal,
  }).then(responseValue<T>);
}
export function post<T = { ok: boolean }>(
  name: string,
  data: unknown = {},
  method: "POST" | "DELETE" = "POST",
): Promise<T> {
  return fetch(apiURL(name), {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  }).then(responseValue<T>);
}
export function upload<T>(
  name: string,
  data: FormData,
  params?: Params,
): Promise<T> {
  return fetch(apiURL(name, params), {
    method: "POST",
    credentials: "same-origin",
    body: data,
  }).then(responseValue<T>);
}

export function uploadFile(
  file: File,
  dir: string,
  signal: AbortSignal,
  onProgress: (fraction: number) => void,
): Promise<{ files: string[] }> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("上传已取消", "AbortError"));
      return;
    }
    const xhr = new XMLHttpRequest();
    const abort = () => xhr.abort();
    const cleanup = () => signal.removeEventListener("abort", abort);
    xhr.open("POST", apiURL("upload", { dir }));
    xhr.timeout = 2 * 60 * 60 * 1000;
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress(event.loaded / event.total);
    };
    xhr.onload = () => {
      cleanup();
      void responseValue<{ files: string[] }>(
        new Response(xhr.responseText, { status: xhr.status }),
      ).then(resolve, reject);
    };
    xhr.onerror = xhr.ontimeout = () => {
      cleanup();
      reject(
        new Error(
          "连接中断或超时，文件可能已保存；请先检查目标目录，再决定是否重试。",
        ),
      );
    };
    xhr.onabort = () => {
      cleanup();
      reject(
        new DOMException(
          "上传请求已取消；已保存的文件会保留，请检查目标目录。",
          "AbortError",
        ),
      );
    };
    signal.addEventListener("abort", abort, { once: true });
    const body = new FormData();
    body.append("files", file);
    xhr.send(body);
  });
}
export function useAPI<T>(name: string, params?: Params, interval?: number) {
  return useQuery<T, Error>({
    queryKey: [name, params ?? {}],
    queryFn: ({ signal }) => get<T>(name, params, signal),
    refetchInterval: interval ?? false,
  });
}
export async function invalidate(...names: string[]): Promise<void> {
  await Promise.all(
    names.map((name) => queryClient.invalidateQueries({ queryKey: [name] })),
  );
}
