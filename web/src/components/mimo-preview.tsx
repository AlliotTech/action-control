import { useEffect, useRef, useState } from "react";
import type Mpegts from "mpegts.js";
import { Maximize, Play, Square } from "lucide-react";
import { apiURL, errorText, useAPI } from "../api";
import type { MimoPreviewStatus } from "../types";
import { Card, Notice } from "../ui";
import { Button } from "./ui/button";

export function MimoPreview({ enabled }: { enabled: boolean }) {
  const video = useRef<HTMLVideoElement>(null);
  const [connected, setConnected] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [playback, setPlayback] = useState("未连接");
  const [error, setError] = useState("");
  const [size, setSize] = useState("");
  const status = useAPI<MimoPreviewStatus>(
    "mimo_preview_status",
    undefined,
    connected ? 2000 : undefined,
  );

  useEffect(() => {
    if (!connected) return;
    if (!enabled) {
      setConnected(false);
      setPlayback("原生服务未就绪");
      return;
    }
    const controller = new AbortController();
    const element = video.current;
    let player: Mpegts.Player | undefined;
    const destroy = () => {
      controller.abort();
      if (player) {
        const current = player;
        player = undefined;
        current.destroy();
      }
      element?.removeAttribute("src");
      element?.load();
    };
    const fail = (message: string) => {
      if (controller.signal.aborted) return;
      destroy();
      setError(message);
      setPlayback("预览已断开");
      setConnected(false);
    };
    setPlayback("等待 Mimo 画面…");
    setError("");
    setSize("");
    void import("mpegts.js")
      .then(async ({ default: mpegts }) => {
        if (controller.signal.aborted || !element) return;
        if (!mpegts.getFeatureList().mseLivePlayback) {
          throw new Error(
            "此浏览器不支持实时视频播放，请使用支持 MediaSource 的浏览器。",
          );
        }
        player = mpegts.createPlayer(
          {
            type: "mpegts",
            isLive: true,
            hasAudio: false,
            url: apiURL("mimo_preview_stream"),
          },
          {
            enableWorker: false,
            enableStashBuffer: false,
            liveBufferLatencyChasing: true,
            liveBufferLatencyMaxLatency: 1.5,
            liveBufferLatencyMinRemain: 0.5,
            autoCleanupSourceBuffer: true,
          },
        );
        player.on(mpegts.Events.ERROR, (kind: string, detail: string) => {
          fail(
            `无法继续接收画面（${kind}: ${detail}）。请让 Mimo 停留在实时预览页，然后重新连接。`,
          );
        });
        player.attachMediaElement(element);
        player.load();
        try {
          await player.play();
        } catch (error) {
          if (!controller.signal.aborted) {
            setPlayback("等待手动播放");
            setError(
              `浏览器未自动播放：${errorText(error)}。可点击视频中的播放按钮。`,
            );
          }
        }
      })
      .catch((error) => fail(errorText(error)));
    return destroy;
  }, [connected, attempt, enabled]);

  return (
    <Card title="Mimo 预览转发">
      <p className="mb-4 text-sm leading-6 text-muted-foreground">
        在手机上用 Mimo 连接相机并保持实时预览，即可在这里同步观看无声画面。
        手机切回浏览器后，Mimo 可能暂停视频；建议在另一台设备上打开此页。
      </p>
      <div className="relative aspect-video overflow-hidden rounded-lg bg-black">
        <video
          ref={video}
          muted
          playsInline
          controls={connected}
          className="h-full w-full object-contain"
          onPlaying={() => {
            if (connected) {
              setPlayback("正在播放");
              setError("");
            }
          }}
          onWaiting={() => {
            if (connected) setPlayback("等待画面…");
          }}
          onPause={() => {
            if (connected) setPlayback("播放已暂停");
          }}
          onLoadedMetadata={(event) => {
            const element = event.currentTarget;
            if (element.videoWidth && element.videoHeight)
              setSize(`${element.videoWidth} × ${element.videoHeight}`);
          }}
        />
        {!connected && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center px-5 text-center text-sm text-white/70">
            {enabled
              ? "连接 Mimo 预览后，画面将在这里显示"
              : "相机原生服务就绪后可连接预览"}
          </div>
        )}
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button
          disabled={!enabled}
          onClick={() => {
            setConnected(true);
            setAttempt((value) => value + 1);
          }}
        >
          <Play />
          {connected ? "重新连接" : "连接预览"}
        </Button>
        <Button
          variant="outline"
          disabled={!connected}
          onClick={() => {
            setConnected(false);
            setPlayback("已断开");
            setError("");
            setSize("");
          }}
        >
          <Square />
          断开预览
        </Button>
        <Button
          variant="ghost"
          disabled={!connected || !size}
          onClick={() =>
            void video.current
              ?.requestFullscreen?.()
              .catch((error) => setError(errorText(error)))
          }
        >
          <Maximize />
          全屏
        </Button>
      </div>
      <p aria-live="polite" className="mt-3 text-sm text-muted-foreground">
        {playback}
        {connected && size ? ` · ${size}` : ""}
        {connected && status.data?.state === "streaming"
          ? ` · ${status.data.clients} 个网页连接`
          : ""}
      </p>
      {error && (
        <div className="mt-3">
          <Notice tone="warning">{error}</Notice>
        </div>
      )}
      <p className="mt-3 text-xs leading-5 text-muted-foreground">
        断开预览或离开本页会释放本网页的转发连接。独立于 Mimo
        的原生网页预览仍在调查中。
      </p>
    </Card>
  );
}
