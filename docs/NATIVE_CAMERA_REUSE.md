Action Control 原生相机复用进度，版本 0.1.9，已部署，2026-09-18。

**已接入相机本地 Binder 服务的只读录像状态，不需要 Mimo，也不建立另一套采集或录像状态机。原生拍照、录像控制及网页原生预览仍未实现。** 原生网络继续由相机自身服务管理。用户已允许断开手机后继续开发，并将成果独立记录；需要 Mimo 会话的项目保留 pending。

| 已实现内容 | 具体行为 |
| --- | --- |
| 原生录像状态 | `GET /api/native_camera_status` 使用相机原有 `libdcam_camera_service_client.so`，经 Binder 调用状态 getter；已确认状态 1 为录像中、3 为空闲，其他代码不推测 |
| 原生服务状态 | 同一接口通过带 3 秒超时的 `systemctl show` 读取相机、媒体、双屏界面和 Mimo 通信服务的生命周期及 PID |
| 独立诊断命令 | `action-control native-status` 输出同一份 JSON，不创建应用配置或启动网页服务；只读状态不完整时返回非零退出码 |
| 进程隔离与超时 | 内嵌 Python 适配器通过设备已有 Python 3 / ctypes 执行，原生库不加载到 Go 服务进程；单次读取 3 秒超时，由 Go 终止自己的子进程组 |
| 固件兼容检查 | 在加载原生库前核对产品、架构、整数/指针宽度和客户端/服务端库 SHA-256；不匹配时返回不可用，不试探 ABI |
| 并发与失败处理 | 同一服务 PID 的成功结果共享 2 秒；并发请求等待同一次读取；失败清空旧值并退避 30 秒；PID 变化使缓存失效；Linux 抽象套接字防止服务器和 CLI 同时建立读取客户端 |
| 默认页面 | 显示原生录像状态、读取时间及服务状态，每 5 秒刷新；原生预览仍标“未接入”，独立采集在默认折叠的高级入口内 |
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
  "control_available": false,
  "preview_available": false,
  "native_state": {
    "status": "ok",
    "record_state": 3,
    "capture_state": 0,
    "observed_at": "实际读取完成时间，UTC"
  }
}
```

`native_state.status` 可为 `ok`、`unavailable`、`unsupported_firmware`、`busy`、`error`、`timeout`。失败时 `record_state`、`capture_state` 均为 `null`，没有成功样本时间，`reason` 保留原因；不沿用之前的“录像中”或“未录像”。成功但状态代码未识别时保留原始整数，`recording` 仍为 `null`，网页显示“状态待识别”。`capture_state` 目前仅作为诊断原始值，未将其转换成拍照状态或新的拍照功能。

读取时间来自一次完成的读取，缓存复用不改写时间戳。两个 getter 是先后调用，不是原生服务提供的原子快照。网页轮询会漏掉短于轮询间隔的状态变化，不是逐帧状态订阅。预览状态不由编码线程、流量或录像状态推导。

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
| `camera_manager_destroy`，`0xb6390` | 断开本管理器持有的设备、释放包装对象、注销管理器监听，释放客户端对象 |
| 服务端 disconnect / unregister / binder death | 按客户端名称或 Binder 身份移除该客户端的监听和引用；已检查正常退出及死亡通知处理函数，没有进入拍摄启停路径 |

原生录像状态的语义不是按数值大小猜测的：GUI 初始化函数在 getter 失败时使用原始值 `3`，日志明确写“treat as idle”。GUI 的状态映射表 `0x36d7ef8` 将原始值 `1` 映射为界面枚举 `3`，`ArchwayCamera_IsRecording`（`0x5553a0`）以该界面枚举判断录像中；状态回调 `dji_gui_mw_on_record_status_changed`（`0x9f2b80`）也使用相同映射。其他值虽然存在映射，尚未充分验证其启动、收尾、预录等含义，因此暂不转换成布尔值。

适配器允许加载的固件库为：

| 设备文件 | SHA-256 |
| --- | --- |
| `/usr/lib/libdcam_camera_service_client.so` | `98731a8252ee1d1e557af05e3369d33192b9c25142e97513c774f47c16bf27fa` |
| `/usr/lib/libdcam_camera_service_server.so` | `da7713234f19353054fd9faede2fa9feb211a99564ba4352d796d538cf9c859f` |

本机 Python 为 3.10.13，已验证 ctypes 可用。发布包内嵌我们编写的适配器源代码，运行 `python3 -I -B -c ...`，不分发相机私有库、不写临时脚本或 Python 字节码。不存在拍照、录像、工作模式或预览订阅的可选命令参数，读取器只绑定已核实的六个 C 入口。

验证记录：

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

没有为了验证而触发拍照、录像或独立采集，也没有重启相机或原生服务。录像中 `1` 的解释已有静态证据和映射测试；本次设备实测是空闲状态，**尚未实测一次“实体键开始录像 → 网页显示录像中 → 实体键停止 → 网页显示未录像”的完整变化**。也没有故意挂死或杀死相机上的原生服务来制造超时；超时终止通过离线假子进程验证。

不依赖 Mimo、后续仍可推进的本地项目：

| 本地后续工作 | 当前边界 |
| --- | --- |
| 录像状态变化验收 | 已有可用只读路径；在一次有意的实体按键录像过程中对照页面，不要求 Mimo |
| 拍照状态、工作模式和其他录像代码 | 保留已取得的原始证据；继续核对枚举及其实际变化后再公开明确语义 |
| 原生拍照/录像控制 | 仍需核对请求结果、重复操作和按键并发；本次没有调用 setter 或发布控制按钮 |
| 本地预览消费接口 | 继续离线分析原生编码输出与订阅生命周期；仅发现导出函数不能算已实现 |

必须等 Mimo 重新连接的项目保持 **pending**：

| pending 项 | 当前已有证据 | 恢复条件与验证目标 |
| --- | --- | --- |
| 初始配对、认证和蓝牙引导 | 已连接会话、Bluetooth/WMS 路由相关代码 | 从连接前观察一次完整过程，核对热点建立及认证顺序 |
| 当前 Mimo 的实际拍摄指令 | 原生拍照/录像处理函数和响应逻辑 | 对齐用户有意拍摄时的请求、响应和状态变化 |
| Mimo 预览订阅协议 | UDP 9004、订阅/传输管理器、短时流量元数据 | 观察相册和预览切换、订阅/退订、心跳，恢复会话与报文布局 |
| Mimo 与第二预览消费者共存 | 通道分配限制、握手与心跳 | 在已有 Mimo 会话下验证第二订阅者是否被允许、是否影响画面或按键 |
| Mimo 与本地状态读取共存 | 本地单独读取已成功；不接管原生服务 | 在 Mimo 正常预览和拍摄期间观察状态读取与客户端释放，不把离线结果当作双客户端实测 |
| 手机浏览器访问控制台 | 原生热点地址及应用监听端口 | 手机重新连接热点后验证网页和文件 API |
| Mimo 预览实际编码规格 | 插件支持 H.264/H.265，曾采到候选 SPS | 有预览源时完成拆包和帧重组，核对格式、时间戳与关键帧 |

网页原生预览仍需取得原生编码数据并选择浏览器支持的封装；当前没有已验证的 RTSP 地址，也不能承诺多客户端订阅或任意格式免转码。

接管保护针对两条会批量停止原生服务的快捷 API；已有 root 命令执行与进程管理仍属于独立管理员功能，这不是系统权限隔离。展开/关闭高级面板不改变网络和采集状态。

实现文件：[原生读取及并发处理](../cmd/action-control/native_camera_reader.go)、[内嵌适配器](../cmd/action-control/native_camera_reader.py)、[服务状态与接管校验](../cmd/action-control/native_camera.go)、[相机页面](../web/src/pages/CameraPage.tsx)。静态证据保存在本地 `.build-tools/mimo-native-analysis/disasm/`；设备验证记录保存在 `.build-tools/native-reuse-implementation/binder-first-probe.json` 及前后快照。完整 Mimo 分析另存于工作区根目录 `MIMO_NATIVE_ANALYSIS.md`，私有固件副本不纳入仓库或发布包。

部署与网页检查收尾时，原生相机 PID 1250、媒体 PID 1291、主屏 PID 15303、副屏 PID 1359、Mimo 通信 PID 1065、原生网络 PID 2427 全部保持不变，设备启动 ID 仍为 `e5adc1fd-87f6-48ee-a0eb-7d1ff20d0a9f`。只更新了 Action Control 自身服务。无线仍为 `control=native`、`owner=native`、`role=closed`、`state=down`，没有为读取状态打开热点。

本次控制台地址为 `http://127.0.0.1:57556/#/camera`，浏览器已保留该页面。完整部署核验记录为 `.build-tools/native-reuse-implementation/native-019-deployment-check.json`，候选程序结果为 `native-019-candidate-status.json`，收尾原生服务快照为 `native-019-final-services.txt`。
