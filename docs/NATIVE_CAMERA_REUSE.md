Action Control 原生相机复用进度，已部署 0.1.13。更新于 2026-09-19。

2026-09-19 补充：用户重新连接 Mimo 后，已完成本地原生读取、录像启停与该会话共存的服务/传输层验证，并完成 Mimo 预览离线重组和解码，见 [Mimo 预览协议与验收](MIMO_PREVIEW_PROTOCOL.md)。随后已实现 [Mimo 网页预览转发](MIMO_WEB_PREVIEW.md)，复用已有编码视频、处理录像切换时钟并按网页连接数释放观察进程；实机部署验收待完成。[独立预览的生命周期](NATIVE_PREVIEW_LIFECYCLE.md) 仍有未决项。

**已复用相机本地 Binder 服务实现原生录像状态读取和开始/停止录像，不需要 Mimo。设备实测完成 `3 → 1 → 3` 的状态变化，并由相机保存了一段短片。** 相机自身继续管理采集、编码、拍摄设置、文件保存和网络；没有另建录像或无线状态机。网页视频转发依赖 Mimo 保持实时预览，原生拍照和独立预览仍未接入。用户已允许断开手机后继续开发，并将成果独立记录；尚未完成的 Mimo 会话项目保留 pending。

| 已实现内容 | 具体行为 |
| --- | --- |
| 原生录像状态 | `GET /api/native_camera_status` 使用相机原有 `libdcam_camera_service_client.so`，经 Binder 调用状态 getter；已确认状态 1 为录像中、3 为空闲，其他代码不推测 |
| 原生录像控制 | `POST /api/native_recording` 只支持明确的开始、停止动作；复用同一客户端的原生入口，沿用当前拍摄设置，由相机处理存储和拍摄限制 |
| 原生拍照 | `POST /api/native_capture` 只支持明确的 `capture` 动作，复用同一客户端的 `camera_start_capture` 入口；仅在空闲（`record_state=3`、`capture_state=0`）时下发。拍照为一次性动作、无稳定目标状态，故只报 `accepted/blocked/rejected/not_sent/unknown`，不臆造"拍照完成"码；照片是否落盘由相册/文件层确认。与录像共用操作锁，两者不会同时下发。不切换工作模式：相机不在拍照模式时原生返回错误码，如实报 `rejected` |
| 原生服务状态 | 同一接口通过带 3 秒超时的 `systemctl show` 读取相机、媒体、双屏界面和 Mimo 通信服务的生命周期及 PID |
| 独立诊断命令 | `action-control native-status` 输出同一份 JSON，不创建应用配置或启动网页服务；只读状态不完整时返回非零退出码 |
| 进程隔离与超时 | 内嵌 Python 适配器通过设备已有 Python 3 / ctypes 执行，原生库不加载到 Go 服务进程；读取 3 秒、录像操作 8 秒超时，仅终止自己的子进程组，不能据此认为原生服务已撤销请求 |
| 固件兼容检查 | 在加载原生库前核对产品、架构、整数/指针宽度和客户端/服务端库 SHA-256；不匹配时返回不可用，不试探 ABI |
| 并发与失败处理 | 同一服务 PID 的成功读取共享 2 秒；读取失败退避 30 秒；PID 变化和录像操作前后均使缓存失效，包括尚未写回的在途读取；共享访问锁及 Linux 抽象套接字限制本应用的并发原生客户端 |
| 请求去重 | 同一请求 ID 复用进行中的操作或最终结果；HTTP 断开只取消等待，操作仍受应用生命周期和 8 秒期限约束，不自动重发、反向补偿或维持目标录像状态 |
| 默认页面 | 显示录像状态、读取时间、开始/停止按钮和上次操作结果，每 5 秒刷新；新增按需连接的 Mimo 视频转发，独立采集在默认折叠的高级入口内 |
| Mimo 预览转发 | `GET /api/mimo_preview_stream` 读取已有原生视频副本；最多 4 个网页共用观察进程，最后一个退出即回收。单独状态接口标记 `source=native_mimo_mirror`、`requires_mimo=true`，不代表独立订阅已实现 |
| 独立采集状态 | 原有 `GET /api/camera_status` 继续使用 `source=action-control`、`scope=independent_capture`；它的分辨率、帧率、流量和运行状态只属于自有管线 |
| 接管保护 | 继承 0.1.8：`camera_start` 和 `dji_kill_all` 必须同时带 `confirm=true`、`takeover_native=true`；旧请求在配置、网络、屏幕和服务操作前返回 409 |
| 网络共用 | 继承 0.1.7 的原生网络策略，没有新增热点、DHCP 或另一套无线状态机 |

状态接口成功读取 Binder 状态时返回 `source=native_binder`，服务信息仍明确标记 `service_source=systemd`。没有原生拍摄状态时退回 `source=systemd`，`recording=null`。例如本次空闲相机读取结果的关键部分：

```json
{
  "source": "native_binder",
  "service_source": "systemd",
  "service_state": "running",
  "recording": false,
  "previewing": null,
  "control_available": true,
  "recording_controls": { "start": true, "stop": false },
  "preview_available": false,
  "native_state": {
    "status": "ok",
    "record_state": 3,
    "capture_state": 0,
    "observed_at": "实际读取完成时间，UTC"
  }
}
```

`native_state.status` 可为 `ok`、`unavailable`、`unsupported_firmware`、`busy`、`error`、`timeout`。失败时 `record_state`、`capture_state` 均为 `null`，没有成功样本时间，`reason` 保留原因；不沿用之前的“录像中”或“未录像”。成功但状态代码未识别时保留原始整数，`recording` 仍为 `null`，网页显示“状态待识别”。`capture_state` 未转换成拍照功能；开始录像仅允许已验证的 `record_state=3`、`capture_state=0` 组合，非零拍照状态会阻止开始请求。

读取时间来自一次完成的读取，缓存复用不改写时间戳。两个 getter 是先后调用，不是原生服务提供的原子快照。网页轮询会漏掉短于轮询间隔的状态变化，不是逐帧状态订阅。预览状态不由编码线程、流量或录像状态推导。

`control_available` 表示原生适配器可用；`recording_controls.start/stop` 表示根据最近一次观测可启用的按钮，不承诺相机一定接受指令。`record_state=1` 时启用停止，`record_state=3` 且 `capture_state=0` 时启用开始，未识别状态不开放按钮。服务端执行操作时重新读取原生状态，不使用这份可能缓存的按钮状态决定是否下发。

录像接口示例：

```json
POST /api/native_recording
{
  "action": "start_recording",
  "request_id": "a-unique-request-id-0001"
}
```

`action` 仅允许 `start_recording` 和 `stop_recording`；`request_id` 必须是 8–80 位字母、数字、下划线或连字符。相同 ID 不可更换动作。结果在当前服务进程中保留 10 分钟，最多 256 条，满额拒绝新请求；服务重启会清空去重记录。网页使用 `crypto.getRandomValues` 生成请求 ID，兼容相机热点上的普通 HTTP。

每个有效操作返回一个报告，包含 `action`、`request_id`、`outcome`、`dispatched`、`native_code`、`before`、`after`、`completed_at`、`replayed` 和说明。HTTP 200 表示报告可用，**必须检查 `outcome` 才能判断执行结果**。请求格式、ID 冲突、操作冲突、频率限制和设备校验仍使用对应的 HTTP 错误码。

| `outcome` | 含义 |
| --- | --- |
| `confirmed` | 原生入口返回 0，后续 getter 已观察到目标录像状态；不等同于完成文件完整性校验 |
| `accepted` | 原生入口返回 0，但随后最多 2 秒的观察尚未确认目标状态 |
| `already` | 执行前已经处于目标状态，`dispatched=false`，没有重复调用原生启停入口 |
| `blocked` | 执行前为未验证的录像状态，或开始时拍照状态不为 0，没有下发 |
| `rejected` | 原生入口返回非零错误码，保留原始数值；不猜测错误枚举 |
| `not_sent` | 设备、原生服务、访问锁、固件、创建或读取等前置步骤未完成，没有下发 |
| `unknown` | 调用、后续读取、清理或输出未完整完成；不能证明指令未发送，也不能自动重试 |

`dispatched=true` 表示已进入原生调用；如果整个子进程失联、超时或结果无法解析，则为 `null`，而不是错误地填写 `false`。`before/after` 是当次操作的顺序读取，`completed_at` 是报告生成时间，不是帧时间或录像时长。失败不会把旧状态重新写入读取缓存。

读取和控制共用原生访问锁；控制还与独立采集启停、两条服务接管快捷 API 共用操作锁，新操作冲突时拒绝而不排队。已经下发的请求不会因浏览器断线而自动撤销。程序没有维护“必须继续录像”的目标，也不会在后续轮询中恢复录像，所以实体键停止后不会被它重新启动。读取与下发之间仍存在原生按键或其他客户端改变状态的窗口；最终处理由原生服务决定，尚未实测实体键同时交错操作。

服务生命周期继续使用下列值：

| `service_state` | 含义 |
| --- | --- |
| `running` | 五个服务均为 loaded / active / running，且主进程 PID 非零 |
| `stopped` | 五个服务均为 inactive / dead，且 PID 为零 |
| `degraded` | 状态完整，但部分服务停止或失败 |
| `transitioning` | 状态完整，至少一个服务在启动或停止 |
| `unknown` | 查询失败、超时、输出不完整或矛盾 |
| `unavailable` | 非目标相机或本地文件模式，不连接硬件 |

`services` 保留逐项 `load_state`、`active_state`、`sub_state`、`main_pid` 和观察状态。只有原生相机服务本身确认为运行中才调用本地客户端；服务查询失败或原生相机停止时不调用 Binder。Mimo 通信服务运行只表示进程可用，不表示手机在线。

本地 ABI 与生命周期的证据如下。地址均来自相同哈希的 ARM64 ELF 副本，反汇编只在主机离线进行：

| 入口 / 位置 | 已核实的行为 |
| --- | --- |
| 客户端 `camera_manager_create`，`0xb5520` | C 签名为创建参数指针、管理器输出指针，返回 32 位错误码；复制输入的 16 字节，建立 `camera_manager_client_<pid>_<tid>` 独立名称 |
| 创建参数及回调，`0xb61a0` | 前 8 字节是用户数据指针，后 8 字节是可空的断连回调指针；回调转发前检查是否为空。本实现两者均置空，不猜测或注册 Python 回调 |
| 原生 GUI `dji_gui_mw_cam_manager_init`，`0x9f98c0` | 使用相同 16 字节参数并调用同一套 create / connect API，为参数布局提供独立调用方证据 |
| 管理器 `binder_init` / `register_binder_listener` | 获取已有 `camera_manager` 服务，注册当前客户端监听并自行启动 Binder 线程池，无需我们手动创建服务或初始化相机 |
| `camera_manager_get_camera_amount`，`0xb6d40` | 输出 32 位设备数量；本机实际为 1，适配器只在数量为 1 时连接 camera 0 |
| 客户端 `camera_manager_connect_camera`，`0xb7880` | 参数为管理器句柄、32 位相机 ID、相机句柄输出指针；句柄由原生库分配，适配器不访问其内部偏移 |
| 服务端 `camera_manager_server::connect`，`0x1d07b0` | 校验 ID 后返回现有相机对象的引用；没有启动另一条采集或切换拍摄模式 |
| `camera_get_record_state` / `camera_get_capture_state`，`0x87050` / `0x874d0` | C 参数均为原生相机句柄、32 位整数输出指针，零表示调用成功；通过原生 Binder 代理传递 |
| 服务端两个 getter，`0x16aed0` / `0x16aec0` | 只返回相机对象已有状态字段，并返回 Binder OK；没有拍摄副作用 |
| 客户端 `camera_start_recording` / `camera_stop_recording`，`0x646a0` / `0x658e0` | C 参数均为一个不透明相机句柄，返回 32 位错误码；经过客户端代理调用原生 Binder 服务，不修改工作模式 |
| 客户端 `camera_start_capture`，`0x63d80` | 单个不透明相机句柄，返回 32 位错误码；`x0→[+8]` 取设备代理、vtable `+0x40`，与 `camera_start_recording` 同形；错误串 `camera_device_client_start_capture fail` |
| 服务端拍照入口，`cam_event_handler_start_capture` / `dji::camera::start_capture` | 校验相机对象后送入已有拍照事件路径，返回码传回客户端；拍照许可/模式由相机的 topmode 逻辑决定，不由本客户端切换 |
| 客户端 `camera_manager_get_workmode`，`0xb8c80` | 参数为管理器句柄、32 位整数输出指针，返回 0 表示成功；`x0→[+0x10]` 取管理器代理、vtable `+0x60`。只读，原始值不映射为拍照/录像模式 |
| 服务端录像入口，`0x14bf00` / `0x14d100` | 校验原生相机对象后调用 `cam_event_handler_start_recording` / `cam_event_handler_stop_recording`，将返回码传回客户端 |
| 原生事件适配，`0x92780` / `0x94490` | 将开始/停止请求送入已有的相机事件处理路径，保留错误返回；内部事件常量不当作 Mimo 线上命令 ID |
| `libdcam_cs_topmode_common_video.so` 的 `_common_video_idle_start_record`，`0x6e2a0` | 可见原生存储初始化、拍摄许可、温度/CPU 负载限制及异步结果会话处理；这些条件继续交由相机管理 |
| 同库 `_common_video_recording_on_stop_record`，`0x8e3e0` | 使用原生停止录像及异步回调路径；原生库还保留启动失败、录像已开始和已停止的回调，不能只用“入口返回 0”代替状态确认 |
| `camera_manager_destroy`，`0xb6390` | 断开本管理器持有的设备、释放包装对象、注销管理器监听，释放客户端对象 |
| 服务端 disconnect / unregister / binder death | 按客户端名称或 Binder 身份移除该客户端的监听和引用；已检查正常退出及死亡通知处理函数，没有进入拍摄启停路径 |

原生录像状态的语义不是按数值大小猜测的：GUI 初始化函数在 getter 失败时使用原始值 `3`，日志明确写“treat as idle”。GUI 的状态映射表 `0x36d7ef8` 将原始值 `1` 映射为界面枚举 `3`，`ArchwayCamera_IsRecording`（`0x5553a0`）以该界面枚举判断录像中；状态回调 `dji_gui_mw_on_record_status_changed`（`0x9f2b80`）也使用相同映射。其他值虽然存在映射，尚未充分验证其启动、收尾、预录等含义，因此暂不转换成布尔值。

适配器允许加载的固件库为：

| 设备文件 | SHA-256 |
| --- | --- |
| `/usr/lib/libdcam_camera_service_client.so` | `98731a8252ee1d1e557af05e3369d33192b9c25142e97513c774f47c16bf27fa` |
| `/usr/lib/libdcam_camera_service_server.so` | `da7713234f19353054fd9faede2fa9feb211a99564ba4352d796d538cf9c859f` |

本机 Python 为 3.10.13，已验证 ctypes 可用。发布包内嵌我们编写的适配器源代码，运行 `python3 -I -B -c ...`，不分发相机私有库、不写临时脚本或 Python 字节码。默认读取和 `action-control native-status` 仍只使用六个已核实的创建、连接、查询及销毁入口。只有明确的录像动作才额外绑定对应的一个启停函数；不支持拍照、工作模式、任意符号或预览订阅参数。

基础读取验证记录（0.1.9，继续保留为历史证据）：

| 检查 | 结果 |
| --- | --- |
| 首次独立设备验证 | 创建、设备数量、工作模式、连接、录像/拍照 getter、销毁全部返回 0；设备数量 1、工作模式原始值 3、录像原始值 3、拍照原始值 0 |
| 单次读取耗时 | 首次诊断主机往返约 89.74 ms；其中创建、查询和销毁的设备端区间约 2.72 ms，不含 Python 启动和库哈希/加载 |
| 设备状态 | 首次验证前后启动 ID 相同；六个原生服务 PID 全部未变；临时诊断脚本已删除 |
| Go 测试 | `go test ./...` 通过；覆盖状态映射、损坏/缺失输出、失败不保留旧值、并发合并、请求取消、PID 缓存失效，以及假子进程输出 JSON 后挂起时的超时终止 |
| 并发检查 | `go test -race ./cmd/action-control -run TestNative -count=1` 通过 |
| Python 适配器 | 8 项离线测试通过，覆盖固件不匹配前置阻止加载、并发占用、空句柄、错误返回、设备数变化和失败路径释放；测试不加载相机库 |
| 前端 | 固定 Node.js 24.20.0 构建通过 |
| 完整 ARM64 候选程序 | 0.1.9 发布构建通过；从临时路径执行 `native-status` 成功，`source=native_binder`、`recording=false`、原始值 3/0；临时程序已删除，原生 PID 和启动 ID 未变 |
| 安装和部署 API | 标准更新安装器验证 0.1.9 成功并保留原始恢复基线；部署后的状态 API 为 `source=native_binder`、`recording=false`，原始状态为 3/0 |
| 并发设备请求 | 8 个同时发起的 HTTP 请求全部成功，使用同一 `native_state.observed_at`，未为每个请求重复创建客户端 |
| 接管保护回归 | 两条仅带 `confirm=true` 的旧接管请求仍返回 `409 native_camera_protected`；自有采集保持停止、`reboot_required=false` |
| 浏览器 | 显示版本 0.1.9、“未录像”及实际读取时间；390 像素宽度下滚动宽度同为 390、一个一级标题；高级入口默认折叠，未发现警告或错误；临时视口已复原 |

0.1.9 阶段只验证空闲读取，没有触发拍照、录像或独立采集。以下是 0.1.10 新增控制后的验证，不将历史只读结果混作控制验收：

| 检查 | 0.1.10 结果 |
| --- | --- |
| Go 与并发检查 | `go test ./...`、`go test -race ./cmd/action-control -run TestNative -count=1` 通过；新增覆盖不完整/矛盾结果、在途缓存失效、可取消访问等待、HTTP 等待者断开、请求去重、操作互斥、请求数量限制和超时结果不确定性 |
| Python 适配器 | 18 项离线测试通过；新增开始/停止、已达目标不下发、未知状态阻止、原生拒绝、仅受理未确认、调用后读取/清理失败不报成功、无自动重试或反向指令 |
| 发布构建与候选读取 | 固定 Node.js 24.20.0、Go 1.27.1 构建通过；完整 ARM64 候选 `native-status` 成功，临时候选程序已删除 |
| 更新部署 | 标准安装器验证 0.1.10 成功，保留原始恢复基线，仅更新 Action Control 自身服务 |
| 真实开始录像 | 原生返回码 0，`before=3/0`、`after=1/0`，`outcome=confirmed`；HTTP 往返约 1.373 秒 |
| 控制客户端退出后状态 | 独立的状态请求仍读到原生录像状态 1，说明一次性控制客户端正常退出没有结束相机录像 |
| 重复请求 | 重发同一开始 ID 返回同一完成时间及 `replayed=true`；另一个开始 ID 在录像中返回 `already`、`dispatched=false` |
| 真实停止录像 | 原生返回码 0，`before=1/0`、`after=3/0`，`outcome=confirmed`；HTTP 往返约 1.387 秒；同一停止 ID 重发复用结果 |
| 相机原生文件 | SD 卡新增 `DJI_20260918225834_0322_D.MP4`、对应 LRF 和 WAV；MP4 为 5,230,682 字节，ffprobe 可解析，主视频 HEVC 1920×1080 / 50 fps，容器时长 1.408 秒，含 AAC 音频；文件保留在相机上 |
| 原生服务及网络 | 测试前后启动 ID、六个原生服务 PID 均未变；独立采集保持停止，`reboot_required=false`；原生网络仍为 closed/down |
| 接管保护 | 两条仅带 `confirm=true` 的旧接管请求仍返回 `409 native_camera_protected` |
| 网页 | 显示 0.1.10、未录像、开始可用/停止禁用；390×844 视口下页面宽度和滚动宽度均为 390、一个一级标题、高级独立采集折叠；截图布局正常，无浏览器警告或错误；已复原视口并保留控制台页面 |

测试片的格式仅证明本次原生录像输出，不能据此推断 Mimo 预览编码。`confirmed` 是原生状态观察；这次另做了新增文件和 ffprobe 检查。没有读取测试画面的视觉内容，也没有删除测试文件。

没有重启相机、原生相机服务或屏幕服务。没有通过故意挂死相机来制造超时，故障路径使用离线假库和假子进程验证。**实体键交错控制、原生屏幕画面及按键本轮人工确认、特殊录像模式和 Mimo 同时操作仍未完成验收。**

不依赖 Mimo、后续仍可推进的本地项目：

| 本地后续工作 | 当前边界 |
| --- | --- |
| 实体键与网页交错操作 | 网页原生启停和状态变化已实测；实体键开始、网页停止以及反向组合仍需验收，不要求 Mimo |
| 拍照状态、工作模式和其他录像代码 | 保留已取得的原始证据；继续核对枚举及其实际变化后再公开明确语义 |
| 原生拍照 | 已检查 `camera_start_capture` 入口和原生处理路径；尚未完成不同拍照模式、保存完成及失败反馈的核对，不发布拍照按钮 |
| 特殊录像模式及异步失败原因 | 当前验证为一次普通录像；其他模式及原生异步错误通知尚未接入，不自动切换相机模式 |
| 本地预览消费接口 | 继续离线分析原生编码输出与订阅生命周期；仅发现导出函数不能算已实现 |

涉及 Mimo 会话的项目（已按 2026-09-19 再连接验证更新）：

| 项目 | 当前已有证据 | 进度与后续验证目标 |
| --- | --- | --- |
| 初始配对、认证和蓝牙引导 | 已连接会话、Bluetooth/WMS 路由相关代码 | 从连接前观察一次完整过程，核对热点建立及认证顺序 |
| 当前 Mimo 的实际拍摄指令 | 原生拍照/录像处理函数和响应逻辑 | 对齐用户有意拍摄时的请求、响应和状态变化 |
| Mimo 预览订阅协议 | UDP 9004、订阅/传输管理器、短时流量元数据 | 观察相册和预览切换、订阅/退订、心跳，恢复会话与报文布局 |
| Mimo 与第二预览消费者共存 | 通道分配限制、握手与心跳 | 在已有 Mimo 会话下验证第二订阅者是否被允许、是否影响画面或按键 |
| Mimo 与本地状态读取/录像控制共存 | 同一 Mimo 预览会话下，39 次定时本地读取及一次本地原生录像启停均成功；服务 PID 未变，后续视频继续输出 | 服务/传输层已验证；录像切换有约 0.36/0.61 秒帧间隔及时间戳归零，Mimo 自身录像对照和切换后手机界面明确验收仍待完成 |
| 手机浏览器访问控制台 | 原生热点地址及应用监听端口 | 手机重新连接热点后验证网页和文件 API |
| Mimo 预览实际编码规格 | 本次会话已完成带校验拆包、完整帧重组和严格解码，确认 H.264 High / Level 3.2、1280×720、约 25 fps | 已验证本次会话；其他设置、H.265、FEC 和独立订阅仍未覆盖，不推广为所有模式的固定规格 |

0.1.11 已实现依赖 Mimo 的视频副本转发和浏览器封装，见 [网页转发说明](MIMO_WEB_PREVIEW.md)。它不创建第二个原生订阅者，独立原生消费接口及其生命周期继续 pending；没有已验证的 RTSP 地址，也不能承诺多原生客户端订阅或任意视频格式。

接管保护针对两条会批量停止原生服务的快捷 API；已有 root 命令执行与进程管理仍属于独立管理员功能，这不是系统权限隔离。展开/关闭高级面板不改变网络和采集状态。

实现文件：[原生录像接口与请求去重](../cmd/action-control/native_camera_recording.go)、[原生读取及并发处理](../cmd/action-control/native_camera_reader.go)、[内嵌适配器](../cmd/action-control/native_camera_reader.py)、[服务状态与接管校验](../cmd/action-control/native_camera.go)、[相机页面](../web/src/pages/CameraPage.tsx)。静态证据保存在本地 `.build-tools/mimo-native-analysis/disasm/`；本轮新增离线固件副本的哈希单独保存在 `native-control-manifest.json`。完整 Mimo 分析另存于工作区根目录 `MIMO_NATIVE_ANALYSIS.md`，私有固件副本不纳入仓库或发布包。

0.1.10 部署、短片验收以及中断恢复后的检查中，原生相机 PID 1250、媒体 PID 1291、主屏 PID 15303、副屏 PID 1359、Mimo 通信 PID 1065、原生网络 PID 2427 全部保持不变，设备启动 ID 仍为 `e5adc1fd-87f6-48ee-a0eb-7d1ff20d0a9f`。无线仍为 `control=native`、`owner=native`、`role=closed`、`state=down`。中断后恢复的是主机 ADB 端口转发，没有重启相机。

本次控制台地址为 `http://127.0.0.1:65296/#/camera`，浏览器已保留该页面。发布包为 `release/action-control-0.1.10-linux-arm64.tar.gz`，可执行文件 SHA-256 为 `7b32067b07cd2f2b22dfb275168579c43d42070448e378a9ec2c10fa4c33b56b`。

本轮完整记录在 `.build-tools/native-reuse-implementation/`：`native-0110-before.json`、`native-0110-candidate-status.json`、`native-0110-recording-cycle.json`、`native-0110-after.json`、`native-0110-test-clip.json`、`native-0110-takeover-check.json`。这些文件记录真实 API 结果、处理时间、前后服务快照和新增文件元数据；历史 0.1.9 记录继续保留。

0.1.13 原生拍照实机验收（设备 `123456789ABCDEF`，未重启相机）：状态读取新增 `workmode`（原始值 3），`capture_controls.capture` 随空闲态开放。相机处于视频拍摄档时 `POST /api/native_capture` 下发得 `outcome=rejected`、`native_code=-1009`，DCIM 文件数不变、无新文件——如实报告原生拒绝、不切模式、不重试。用户在相机上切到拍照档后重发得 `outcome=accepted`、`native_code=0`、`before/after` 均 `record_state=3/capture_state=0`，DCIM 由 335 增至 336，新增 `DJI_20260919120545_0329_D.JPG`（约 2.46 MB）。两档下 `workmode` 原始值均为 3，故该值不作为拍照/录像档指示，仅原样上报。离线：`go test ./...` 通过（新增 capture decode/controls/endpoint 测试），Python 适配器 24 项通过（新增 6 项 capture）。发布包 `release/action-control-0.1.13-linux-arm64.tar.gz`。
