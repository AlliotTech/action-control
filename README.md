# Action Control

面向 DJI Osmo Action 5 Pro（AC204）的自托管 Web 控制台。后端直接运行在相机的 Linux 系统中，通过浏览器管理实时画面、媒体文件、无线网络、进程和系统操作。

> 本项目需要已解锁且启用 **root ADB** 的相机。它不会解锁 Bootloader、开启 ADB 或移除 Factory Mode。

## 功能

- 查看设备、存储、网络、进程和服务状态
- 复用相机本地服务，开始/停止原生录像并查看实际录像状态；高级独立采集可输出 MPEG-TS / H.264 实时画面并保存快照
- Mimo 保持实时预览时，网页可同步观看原生视频；多个网页共用转发，最后一个连接关闭后自动释放资源
- 使用常用采集组合，保存参数草稿并复用本机最近成功输出画面的配置
- 浏览、上传、下载、重命名和删除 SD 卡及内置存储文件；上传队列支持进度、取消和重试
- 相册支持分页、搜索、类型筛选、排序、按修改日期分组、大图缩放和文件位置跳转
- 检测、备份并清理 SD 卡 `AC004.db` 中的失效媒体索引；删除文件时若原生服务正占用 `AC004.db`，会重试同步索引，仍占用则文件照常删除并提示稍后清理，不再报删除失败
- 媒体详情附带相机原生索引（`AC004.db`）的只读元数据：星标、精彩标记、时长、分辨率/帧率、编码与防抖原始值、ND/EV；文件系统扫描或 ffprobe 无法提供这些
- 默认复用相机原生网络；独立管理模式可扫描并连接 Wi-Fi、管理已保存网络和热点
- 执行受管命令、终止进程、清理缓存或日志、重启系统
- 安装、更新、卸载及导入配置时保留恢复记录

侧栏可收起；文件目录、相册筛选和浏览位置会在当前浏览器标签页中保留。上传期间可切换控制台页面，关闭或刷新浏览器会中断上传；同名文件不会被覆盖，连接中断后应先确认目标文件是否已保存。

网络默认使用 `network_control: "native"`：由相机原生服务管理无线，控制台使用已有连接提供网页和文件访问。在相机上开启原生热点后，手机或电脑连接该热点即可访问 `http://192.168.2.1:8080`；已有局域网连接也可使用。网络页区分原生管理与无线实际状态，原生服务运行不代表 Wi-Fi 已开启。

网络页的“原生热点”按钮会让已校验固件中的 `dji_network` 进程调用其自身的 AP 启动/停止入口，不启动第二套 hostapd/dnsmasq，也不切换到独立管理模式。该能力仅在目标型号、目标 `dji_network` SHA-256 和 root ARM64 环境同时匹配时启用；固件不匹配直接拒绝，不猜测地址。操作会切换 wlan0，当前浏览器可能断开；HTTP 接口没有登录认证或传输加密。

原生模式下，后端仍拒绝主动扫描、独立连接/热点、原生网络服务启停、共享 Wi-Fi/DNS 配置写入和自动接管。网络页新增的原生热点控制是已验证的原生 AP 入口例外；原生 Wi-Fi 控制协议尚未作为独立 DUSS 客户端接入。该模式不保证熄屏后保持联网，也尚未验证与 Mimo 的完整并发使用。

旧配置没有 `network_control` 字段时按原生模式读取，旧的自动连接设置不再触发接管，其他配置保留；读取不会重写原文件。首次升级时若旧版独立网络进程仍在运行，更新会在替换程序前拒绝，避免破坏 DHCP 续租；请先用旧版交还原生网络再更新。配置导入遵守同一策略，原生模式不会导入到共享 Wi-Fi 凭据文件。

相机页默认展示原生录像控制和服务状态，独立采集入口位于“高级：独立采集”。从 0.1.9 起，录像状态通过相机原生客户端库和本地 Binder 服务读取；0.1.10 增加原生开始/停止录像，均不需要 Mimo。沿用相机当前拍摄设置，由原生服务处理拍摄和保存。0.1.11 增加依赖 Mimo 的网页预览转发，0.1.12 修复其实时直播约 1 秒中止的问题；0.1.13 增加原生拍照，0.1.14 增加只读拍摄模式档，0.1.15 增加拍照/视频模式切换（均复用同一 Binder 客户端，仅空闲时下发、可切回，只暴露已验证档位），0.1.16 增加只读视频设置（分辨率/帧率/编码/防抖，用 DJI 文档枚举打标签）。脱离 Mimo 的独立预览仍未接入。全局的“独立采集未启动”仅描述 Action Control 自己的管线。

原生读取使用相机现有 Python 3 / ctypes，在独立子进程中执行，3 秒超时，只调用创建、连接、状态 getter 和释放入口。读取前核对已分析的客户端与服务端库 SHA-256；不匹配时禁用这项读取。相机页每 5 秒刷新，后端共享 2 秒内的结果并对失败退避 30 秒，观察时间保留在 API 中。

录像操作使用同一适配器中两个明确的原生入口，单次操作 8 秒超时，调用前重新读取状态。操作结果区分“已受理”和“已观察到目标状态”，失败或超时不自动重发。同一 `request_id` 在当前服务进程中保留结果 10 分钟；再次开始已经进行的录像、停止已经空闲的相机均不重复下发指令。操作前后清除状态缓存，与独立采集和接管操作互斥；相机按键和 Mimo 的实时并发操作仍需进一步验证。

独立采集仍会停止原生相机、屏幕及 Mimo 通信服务；网络复用不改变这一采集限制。从 0.1.8 起，`camera_start` 和 `dji_kill_all` 除 `confirm: true` 外还要求 `takeover_native: true`，旧请求会在触碰硬件前返回 `409 native_camera_protected`。停止独立采集后，恢复原生功能仍可能需要重启。

原生复用的实现范围、API 语义和 pending 项见 [独立说明文档](docs/NATIVE_CAMERA_REUSE.md)。相机上可运行 `action-control native-status` 输出只读诊断 JSON，无需启动网页服务或修改配置。

Mimo 连接期间的共存验证、预览协议、离线重组工具和实测限制见 [Mimo 预览分析](docs/MIMO_PREVIEW_PROTOCOL.md)。[网页预览转发](docs/MIMO_WEB_PREVIEW.md) 只读取已有视频副本并封装给现有播放器，不创建原生订阅、不切换网络或采集模式。手机需持续停留在 Mimo 实时预览页，建议在电脑或另一台设备观看。独立订阅的未决问题见 [原生预览生命周期调查](docs/NATIVE_PREVIEW_LIFECYCLE.md)。

## 安全说明

Action Control 以 root 权限运行，HTTP 管理接口和视频流**没有登录认证，也没有传输加密**。默认设备服务监听 `:8080`。

推荐只通过 ADB 端口转发访问：

```bash
adb forward tcp:8080 tcp:8080
```

然后打开 <http://127.0.0.1:8080>。不要把端口暴露到互联网或不可信网络。

## 安装

### 前置条件

- DJI Osmo Action 5 Pro（ARM64）
- 相机已连接，`adb shell id -u` 输出 `0`
- 主机已安装 Android Platform Tools（`adb`）
- 获取并完整解压发布包，或按下文从源码构建；目录中必须包含 `payload/` 和 `SHA256SUMS`

### macOS / Linux

```bash
./install.sh
```

连接多台设备时：

```bash
./install.sh --serial SERIAL
```

### Windows PowerShell

```powershell
.\install.ps1
```

连接多台设备时：

```powershell
.\install.ps1 -Serial SERIAL
```

安装脚本会校验 release 文件、安装并启动 systemd 服务、验证版本，然后建立一个本地 ADB 转发并打印控制台地址。

程序安装在 `/blackbox/upgrade/action-control`。不要放在 `/blackbox` 根目录：设备启动脚本会在该分区检查失败时格式化它。

## 更新与卸载

在新版本完整解压目录中更新：

```bash
./install.sh --operation update
```

```powershell
.\install.ps1 -Operation update
```

卸载必须同时确认重启；重启用于恢复相机原生运行时：

```bash
./uninstall.sh --reboot
```

```powershell
.\uninstall.ps1 -Reboot
```

卸载会恢复安装时记录的 Wi-Fi 和 DNS 基线，但不会重新锁定 Bootloader、关闭 root ADB 或移除 Factory Mode。

## 本地开发

本地模式使用临时文件系统根目录；网页和文件 API 可用，相机、无线和系统硬件操作不可用。

要求：Node.js `24.20.0`、Go `1.27.1`。版本也记录在 [`.tool-versions`](.tool-versions)。

获取源码：

```bash
git clone https://github.com/AlliotTech/action-control.git
cd action-control
```

安装前端依赖并构建后端所需的嵌入资源：

```bash
npm --prefix web ci
npm --prefix web run build
mkdir -p cmd/action-control/web/dist
cp -R web/dist/. cmd/action-control/web/dist/
```

启动后端：

```bash
mkdir -p /tmp/action-control-dev
go run ./cmd/action-control serve \
  --root /tmp/action-control-dev \
  --listen 127.0.0.1:8080
```

另开终端启动前端开发服务器：

```bash
npm --prefix web run dev
```

打开 <http://127.0.0.1:5173>。Vite 会将 `/api` 代理到 `127.0.0.1:8080`。

运行检查：

```bash
go test ./...
npm --prefix web run build
```

### 相册分页 API

`GET /api/media_list` 接受 `dir`、`type=all|image|video`、`search`（文件名，不区分大小写）、`sort=newest|oldest|name|largest` 和 `limit`（默认 80，范围 1–200）。`newest` / `oldest` 使用文件修改时间。

响应包含当前页 `items`、筛选后的总数 `count`、`next_cursor`、`warnings` 和 `truncated`。继续浏览时将 `next_cursor` 作为 `cursor` 传回，并保持目录、筛选和排序不变；空游标表示结束。一次分页使用固定快照，避免浏览过程中新增文件造成重复或遗漏。

无游标请求最多复用 30 秒内的目录缓存；`refresh=true` 强制重新扫描，可发现相机自身产生的文件。快照最长保留 5 分钟，也可能因缓存容量或文件修改提前失效；失效游标返回 HTTP 410，需要从第一页刷新。筛选不匹配的游标返回 HTTP 400。目录扫描最多收录 100,000 个媒体文件，超过上限时 `truncated=true`，此时 `count` 仅代表已收录范围。

## 构建发布包

发布构建支持 macOS 或 Linux 的 ARM64 / x86-64 主机，需要固定版本的 Node.js、Go，以及 `curl`、`tar` 和 `make`。构建脚本会下载并校验固定版本的 Zig、FFmpeg 和 zlib，交叉编译静态 Linux ARM64 二进制。

```bash
node tools/build-ffmpeg.mjs
node tools/build-release.mjs
```

输出：

```text
release/action-control-<version>-linux-arm64.tar.gz
release/action-control-<version>-linux-arm64.tar.gz.sha256
```

`SHA256SUMS` 和 `.sha256` 只用于检测传输损坏，不能证明发布者身份。

## 项目结构

```text
cmd/action-control/   Go 后端、设备控制、安装事务与测试
web/                  React / Vite 前端
media/                媒体工具许可证与可复现性记录
tools/                FFmpeg 和 release 构建脚本
install.*             ADB 安装、更新与配置导入入口
uninstall.*           ADB 卸载入口
```

## 开源协议

项目原创代码采用 [MIT 许可证](LICENSE)。第三方组件遵循各自的许可证；相关声明保存在 [`media/licenses/`](media/licenses/) 中，完整发布包还会收录依赖组件的许可证。

FFmpeg / FFprobe 使用 LGPL-2.1-or-later，zlib 使用 Zlib 许可证，SQLite 属于公有领域。构建版本、配置及源码校验信息见 [`media/provenance.json`](media/provenance.json)。
