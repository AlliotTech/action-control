Mimo 网页预览转发，已部署并实机验收 0.1.12，2026-09-19。0.1.12 修复了实时直播因分片头标志位被误拒而约 1 秒中止的问题。

**手机 Mimo 保持实时预览时，Action Control 可以把同一份原生 H.264 视频转发给网页。** 相机继续持有采集、编码、录像、存储和网络。当前功能没有建立独立原生订阅；关闭 Mimo、切到相册或手机后台停止发流时，网页也会断流。

使用方法：在手机上用 Mimo 连接相机，保持实时预览；在电脑或另一台设备打开 Action Control 的相机页，点击“Mimo 预览转发 → 连接预览”。画面无声，支持浏览器全屏。断开预览、切换控制台页面或关闭标签页都会释放该网页的连接。手机切回同机浏览器可能让 Mimo 暂停，因此推荐使用另一台设备观看。

原生开始/停止录像按钮继续走已经验证的 Binder 客户端，不需要 Mimo。预览转发期间不持有拍摄锁，允许这些原生录像请求；实体键和 Mimo 仍按原生规则操作相机。

实现方式：

```mermaid
flowchart LR
    C[原生相机与编码器] --> W[dji_sw_uav]
    W -->|UDP 9004| M[手机 Mimo]
    W -.->|已有视频报文副本| O[按需观察进程]
    O --> D[校验、重组与关键帧恢复]
    D --> T[MPEG-TS 封装]
    T --> B[网页现有播放器]
```

| 范围 | 行为 |
| --- | --- |
| 数据来源 | 设备现有 `tcpdump`，固定 `wlan0`、非混杂模式，只选相机 IPv4 发出的 UDP 9004 / SW 类型 2 视频；无控制消息解析、无报文注入、无 UDP 端口占用 |
| 原生复用 | 不调用 `camera_start_stream/stop_stream`，不操作相机拍摄模式、服务或无线设置；开网页也不会自动开启热点 |
| 按需运行 | 状态接口和页面初次加载不启动观察；只有明确连接视频时才启动。进程在内存管道中输出，不保存抓包或画面 |
| 多网页共用 | 一个应用进程最多 4 个网页，共用一个观察进程；各连接等到含 SPS/PPS/IDR 的完整关键帧再开始接收 |
| 资源退出 | 最后一个网页退出立即取消并回收观察进程；停机和接管独立采集前也会回收；Linux parent-death 信号覆盖应用异常死亡 |
| 等待与背压 | 首次最多等 8 秒可播放视频；已有输出静默超过 3 秒退出。每个网页最多排队 16 帧，慢连接被断开，不能阻塞其他网页或无限积累内存 |
| 协议检查 | 经典 Ethernet/IPv4 pcap、SW 长度/XOR、分片范围、原生媒体头长度/XOR、已验证的 H.264 NAL 类型；只接受 I/P 帧，不猜测 B 帧的解码时间 |
| 丢帧处理 | 每条流最多保留 64 个帧号；处理乱序、重传及 8 位回绕。缺参考帧后等待完整 SPS/PPS/IDR，不输出残帧或倒序帧 |
| 会话变化 | 连接地址、端口或会话标记变化，以及超过已验证窗口的报文时间间隔，结束本次转发并要求重新连接；不混合会话 |
| 浏览器格式 | 保留 SPS/PPS 和编码图像，移除原生 SEI/AUD，每个访问单元加一个标准前置 AUD；只封装 MPEG-TS，不解码、不重新编码 |
| 时间轴 | 从抓包时间间隔构造单调的 90 kHz PTS/PCR。回退或大于 250 ms 的跨度按本次观测的 25 fps 接续一帧；原生时钟重置只触发关键帧恢复，不直接作为播放时间 |
| 错误边界 | 不支持的协议、FEC、H.265、B 帧和冲突分片会停止转发；不尝试发送订阅、重传或模式切换命令。再次连接是用户操作 |

HTTP 接口：

- `GET /api/mimo_preview_stream`：按需启动共享转发，返回 `video/MP2T`；连接前不可用返回 503。HEAD 和跨来源浏览器请求不启动观察进程。
- `GET /api/mimo_preview_status`：只读转发状态、网页连接数、字节/帧统计、最近输出时间和错误。`source=native_mimo_mirror`、`requires_mimo=true`；统计属于本次或最近一次转发，不表示 Mimo 界面状态。

`native_camera_status.previewing` 仍为 `null`、`preview_available=false`：该接口尚没有独立原生预览订阅/状态适配。Mimo 转发的可用性及流量单独报告，不能由服务 PID 推断手机已经连接。

离线验证使用运行时同一套 Go 解析器和封装器：

```sh
go run ./tools/mimo_preview_replay.go -pcap input.pcap -out preview.ts
go test ./internal/mimopreview
go test -race ./cmd/action-control -run 'TestMimoPreview|TestNative'
ffmpeg -v error -xerror -i preview.ts -fps_mode passthrough -enc_time_base 1:90000 -f null -
```

输出文件必须不存在；输入损坏或无可用视频时删除本次不完整输出。网页运行路径与离线工具均不需要加载私有相机库。

本轮三段真实抓包的重放结果：

| 样本 | 完整帧 / 导出帧 | 原生时钟重置 / 播放接续 | 严格解码 |
| --- | --- | --- | --- |
| 12 秒基准 | 298 / 279 | 0 / 0 | 279 帧，H.264 High、1280×720、Level 3.2，无错误 |
| 约 90 秒共存 | 2,249 / 2,225 | 0 / 0 | 2,225 帧，无错误 |
| 本地录像启停 | 320 / 300 | 2 / 2 | 300 帧，无错误 |

开头等待关键帧的帧没有导出，采样结尾不完整帧也没有拼入视频。三段的 PTS/DTS 相等且严格递增。较长样本在 FFmpeg 默认 null 输出的低精度时间基下出现“重复 DTS”提示；逐包验证不存在倒退，明确使用 1/90000 输出时间基后严格解码无错误。这一验证避免把输出时间基的量化误认为传输时间轴倒退。

Go 测试覆盖边界/截断、短读、大小端/纳秒 pcap、未知格式、片内乱序和冲突、回绕、缺帧及原生时钟重置后的关键帧恢复、TS 分包与时间戳、共享进程、慢连接、最后一个网页退出、等待超时、HTTP 取消和应用退出。5 秒模糊测试执行约 91 万次，没有崩溃。

实机验收（0.1.11，设备 `123456789ABCDEF`，未重启相机）：

- 部署：`0.1.10 → 0.1.11`，安装器自检通过，`/api/health` 报告 `version=0.1.11`。部署前后 `dji_camera3/dji_media/gui` 均 running，`recording=false`、`previewing=null`、`preview_available=false`；升级后 `control_available=true`。
- 接口（无 Mimo，本机 curl）：`/api/mimo_preview_status` 只读返回 `state=idle`、`clients=0`、`source=native_mimo_mirror`、`requires_mimo=true`；`GET /api/mimo_preview_stream` 返回 `503 mimo_preview_unavailable`，未启动观察进程（`clients=0`、`video_packets=0`）；`HEAD` 返回 `405 Allow: GET`；带 `Origin` 的跨来源 `GET` 返回 `403`。
- 浏览器（部署后的控制台页）：预览卡片正常渲染；原生服务就绪后“连接预览”可用；无 Mimo 时点击连接，服务端返回 503、不建观察进程，页面停在“等待画面…”不崩溃，点“断开预览”回到“预览已断开”并可重连。
- 离线管线本机复跑：`go test ./internal/mimopreview` ok、`go test -race ./cmd/action-control -run 'TestMimoPreview|TestNative'` ok；三段真实抓包 TS（基准／约 90 秒共存／本地录像启停）均通过 `ffmpeg -xerror ... -enc_time_base 1:90000` 严格解码，退出码 0。

手机侧实机验收（0.1.12，Mimo 保持实时预览，控制台 `http://127.0.0.1:59610`）：浏览器 `<video>` 持续播放，`currentTime` 递增、1280×720、`readyState=4`、未暂停；服务端 `state=streaming`、`clients=1`、`output_frames` 持续增长。断开／关闭最后一个网页后 `clients=0`、`state=idle`、无残留观察进程与错误。curl 拉 6 秒直播得 `200 video/MP2T`、3,353,732 字节，`output_frames=186`、`skipped=2`、`incomplete=0`、`discontinuities=0`、`native_clock_resets=0`、无错误；该 TS 经 `ffmpeg -xerror ... -enc_time_base 1:90000` 严格解码 186 帧无错。iOS/Safari、所有拍摄设置及完整配对协议不在当前验证范围。

直播中止根因与修复（0.1.12）：0.1.11 下真实直播先正常吐约 24 帧（406268 字节）随即停在 `FEC or unverified video fragment layout` 且不自恢复。抓 live 会话 `live9004.pcap`（1200 包 / 743 个 type2 视频包）逐包解分片头 `data[16:20]`：`bit14`（FEC 标志）**从未置位**——报错名不副实。真正命中的是 `decoder.go` 旧守卫里的 `word>>21 != 0`：743 包中 111 包（约 15%）的 byte2 高两位（word bit21–22，常见 `0x60`）或 byte3（如 `0x13`）带有离线三段抓包从未出现的标志位。将这些帧按 `count`/`index` 重组后经 `multimedia()` 校验，全部是合法的 I/P 帧（NAL 1/5，无坏帧），说明高位是与分片无关的标志，守卫对三段离线样本过拟合。修复：守卫改为只拒 `bit14`(FEC)、`count==0`、`index>=count`，把内容合法性交给 `multimedia()` 的原生媒体头/XOR/长度/NAL 校验（真正的内容闸门）。`live9004.pcap` 经修复后解码器重放得 63 完整帧 / 45 输出帧、无错误并严格解码通过；0.1.12 实机 6 秒直播得 186 输出帧、`state=streaming`、无 `FEC/unverified` 中止。新增回归测试 `TestLiveFragmentFlagsAreAccepted`（带标志帧须解码），并把原 `unknown flags` 用例替换为仍应拒绝的 `index past count`。

本轮证据目录为 `.build-tools/mimo-native-analysis/web-preview-20260919/`：`replay.json`、`decode.json`、`decode-with-explicit-timebase.json` 及三段 TS 文件。抓包及含画面的产物不进入仓库或发布包。独立消费者调查见 [原生预览生命周期](NATIVE_PREVIEW_LIFECYCLE.md)，协议与先前 Mimo 共存证据见 [Mimo 预览协议](MIMO_PREVIEW_PROTOCOL.md)。
