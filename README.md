# Action Control

面向 DJI Osmo Action 5 Pro（AC204）的自托管 Web 控制台。后端直接运行在相机的 Linux 系统中，通过浏览器管理实时画面、媒体文件、无线网络、进程和系统操作。

> 本项目需要已解锁且启用 **root ADB** 的相机。它不会解锁 Bootloader、开启 ADB 或移除 Factory Mode。

## 功能

- 查看设备、存储、网络、进程和服务状态
- 启停相机采集，浏览 MPEG-TS / H.264 实时画面并保存快照；手机底部保留采集按钮
- 使用常用采集组合，保存参数草稿并复用本机最近成功输出画面的配置
- 浏览、上传、下载、重命名和删除 SD 卡及内置存储文件；上传队列支持进度、取消和重试
- 相册支持分页、搜索、类型筛选、排序、按修改日期分组、大图缩放和文件位置跳转
- 检测、备份并清理 SD 卡 `AC004.db` 中的失效媒体索引
- 默认复用相机原生网络；独立管理模式可扫描并连接 Wi-Fi、管理已保存网络和热点
- 执行受管命令、终止进程、清理缓存或日志、重启系统
- 安装、更新、卸载及导入配置时保留恢复记录

侧栏可收起；文件目录、相册筛选和浏览位置会在当前浏览器标签页中保留。上传期间可切换控制台页面，关闭或刷新浏览器会中断上传；同名文件不会被覆盖，连接中断后应先确认目标文件是否已保存。

网络默认使用 `network_control: "native"`：由相机原生服务管理无线，控制台使用已有连接提供网页和文件访问。在相机上开启原生热点后，手机或电脑连接该热点即可尝试访问 `http://相机IP:8080`；已有局域网连接也可使用。网络页区分原生管理与无线实际状态，原生服务运行不代表 Wi-Fi 已开启。

原生模式下，后端拒绝主动扫描、独立连接/热点、原生网络服务启停、共享 Wi-Fi/DNS 配置写入和自动接管，开机网络任务不配置网卡。原生 Wi-Fi 控制协议尚未完成接入，请使用相机自身的无线设置。该模式不保证熄屏后保持联网，也尚未验证与 Mimo 的完整并发使用。

旧网络功能保留在“网络 → 高级网络管理 → 独立管理”，对应 `network_control: "managed"`。选择该模式本身只保存策略；执行网络操作时才会接管无线，并可能中断 Mimo。返回原生模式前，先在系统页交还原生网络并等待完成。Wi-Fi / 热点切换失败时仍会尝试恢复先前连接，主动关闭无线不会自动恢复。

旧配置没有 `network_control` 字段时按原生模式读取，旧的自动连接设置不再触发接管，其他配置保留；读取不会重写原文件。首次升级时若旧版独立网络进程仍在运行，更新会在替换程序前拒绝，避免破坏 DHCP 续租；请先用旧版交还原生网络再更新。配置导入遵守同一策略，原生模式不会导入到共享 Wi-Fi 凭据文件。

自定义采集仍会停止原生相机、GUI 等服务；网络复用不改变这一采集限制。原生网络协议接入与采集共存需分别验证。

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
