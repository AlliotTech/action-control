# 原生 GUI 实验原型

状态：**后屏菜单原型已跑通；未持久安装，未对接 Action Control 后端。**

2026-09-18 补充显示验证后，已在原生“系统设置”末尾显示 `Action Control` 项，打开含“测试点击”和“返回”的原生面板。连续三轮进入、点击、返回，以及两次退出设置页再进入，均取得实际显示图层的像素证据。用户对面板显示与操作反馈的询问回复“可以”。

试验结束后已恢复原生配置和显示。此前发生过物理显示冻结，本轮没有复现，也没有触发整机重启；历史故障的确切触发点仍未确定。

## 已实现的代码

- 包装 `gui_image_loader_create`，完整转发原图像插件的参数、返回值和接口表。
- 校验后屏 GUI 的完整 SHA-256；接口、固件或回调槽不匹配时保留原插件行为。
- 通过 `EwGuiRegisterUserEvent` 在 `ew_gui_thread` 中操作 EW 对象。
- 从 `TestTestApp.Application` 找到实际的 `OsmoGuiOsmoApp`；区分 712×712 根画布与当时有效的 712×400 界面，并按原生方向字段转换触摸坐标。
- 在设置列表末尾增加 `Action Control` 项，保留原列表加载槽；页面引用使用 EW 的锁定/解锁机制管理。
- 创建原生面板：两个二维码（Wi-Fi 加入、控制台 URL）、热点开关按钮、返回按钮和背景触摸拦截控件。工作线程通过 HTTP 从后端 `GET /api/hotspot_qr` 取回模块矩阵，界面线程用矩形网格绘制（EW 无位图加载器），7 px/模块在 712×400 后屏经 scanout 解码验证可扫。
- 修正列表项的文字开关：`P2_Option` 的 bit 1 为主文字，bit 4 为右箭头，组合为 `0x12`。原先的 `0x11` 只启用了图标和箭头，导致空白行仍可点击。
- 列表项使用原生语言字体，独立中文提示使用 `FontsSize32ZH`；重新打开面板、点击后的数字和中文均已查看图像核验。
- 由工作线程写入状态和诊断文件；界面线程不等待文件或网络操作。

仅针对本次取得的后屏程序：

```text
30eb933b1c2315984e150885124d277c13c1cef3194ba6c08cb3d4d1f6656db1
```

其他固件、前屏、旋转过程、正常/调试模式切换、长时间运行和录制共存尚未验证。当前设备原列表为 32 项；29 项普通模式只有适配代码，尚无该模式的实测结果。原型使用了从当前二进制恢复的私有字段，属于固件专用适配。

## 验证结果与边界

| 检查 | 结果 |
|---|---|
| AArch64/glibc 动态库构建 | 通过；Zig 0.16.0，警告视为错误 |
| SHA-256 实现 | 18 组分块/填充边界向量、真实 GUI 文件、不存在文件的错误路径通过 |
| 动态导出 | 仅导出原图像插件创建入口 |
| 原生 UI 线程回调 | 实机确认线程名为 `ew_gui_thread`，计数持续增长 |
| 原 GUI 重启对照 | 未加载扩展时短时重启通过，显示提交继续，原生图层像素匹配 |
| 仅加载回调 | 显示验证通过，恢复原 GUI 后再次通过 |
| 菜单与面板 | 三轮入口、测试点击、返回通过；原列表 32 项，扩展后 33 项 |
| 设置页生命周期 | 两次点击原生返回按钮退出设置，再进入；扩展行共加载三次，未重复追加 |
| 显示图层 | 每次转换同时验证 EW 实际绘制计数、像素变化、KMS 图层像素匹配及显示帧完成时间 |
| 图像查看 | 已确认菜单文字、中文按钮、计数和返回布局可见；捕获的是 GUI 图层，不含独立视频预览平面 |
| 人工反馈 | 用户回复“可以”；原话单独保存，自动记录仍标明人工验证不由脚本完成 |
| 恢复 | 后屏原 GUI、原配置和显示图层恢复；前屏、拍摄、媒体进程和 boot ID 未变 |
| 诊断回归检查 | 8 项通过，覆盖“绘制仍进行但显示停滞”、正常换帧、熄屏、旧图不能验证新菜单、媒体写入检查 |

早期证据中的 `passed: true` / `restored: true` 只覆盖事件和进程，不能据此证明当时显示正常。本轮新证据也区分自动事件、显示图层和人工反馈。

最终文字修正版的 120 秒人工观察窗口末尾后屏已熄灭，旧结果汇总因此把整轮标为失败；该目录的 `exercise.json` 仍完整记录三轮事件/像素及两次设置页重建通过，恢复检查也通过。`assessment.json` 保留这一边界，未改写原始结果。脚本已修正为保留已完成的分项结果，并仅在人工观察窗口结束时把熄屏单独记录；预检和菜单转换仍要求亮屏与像素匹配。

证据保存在 Git 忽略的 `.build-tools/` 中：

- `gui-trial-be51bb9aa28e/`：仅回调的试验。
- `gui-trial-9d6d6da3a4b0/`：面板事件链路。
- `gui-trial-4615ddb2194a/`：设置菜单入口事件链路。
- `gui-recovery/`：显示异常后的进程、日志和重启前后检查。
- `ui-display-research/verified-baseline/`：恢复后的只读显示基线。
- `gui-trial-d082a42b5d83/`：不加载扩展的原 GUI 重启对照。
- `gui-trial-4b1ae0897ca5/`：仅注册回调的显示复测。
- `gui-trial-e180e5800090/`：图层验证发现了空白标签，随后修复。
- `gui-trial-5f256aa85f00/`：文字修正、三轮操作、设置页重建、人工反馈及恢复记录。

## 显示验证与恢复工具

`tools/gui_display.py` 不创建 Wayland 客户端、不暂停进程、不调用 GUI 私有函数。它在完整文件指纹匹配后读取 EW 绘制计数和双缓冲状态，通过只读 DMA 映射取得 GUI 像素；另开只读 DRM 描述符，用 `GETFB2` / `PRIME_HANDLE_TO_FD` 读取当前 AR24 显示平面，并释放自己取得的句柄。图片转为 PNG，保存在主机证据目录。

后屏缓冲区为 448×712，行距 1792 字节；比较可见的 400×712 区域，并转向为便于查看的 712×400 图片。核对帧编号在复制结束时仍被选中，允许观察窗口内另一张内容完全一致的已核对样本，避免正常双缓冲翻转造成误报。不同像素哈希不能相互替代。

`callback_ticks` 只表示回调次数，`ew_update_cycle` 才是 EW 绘制计数。静止菜单无需持续重画，因此持续帧完成检查用于已知活动预览；菜单操作则要求前后像素、绘制计数和帧完成时间一起改变。

`tools/gui_trial_guard.py` 由独立 systemd 计时器启动。它先恢复属于本次试验的配置，再检查全部进程的媒体写入描述符。空闲时恢复原 GUI 并核对显示；只有显示恢复失败且再次确认无媒体写入时才正常重启整机。熄屏单独报告，录制开始时保留录像并报告恢复待处理。整机重启分支本轮未触发；原 GUI 恢复与显示核对已实测。

## 发生了什么，以及如何恢复

试验曾把插件与克隆配置放到私有 `/run/action-control-ui-…` 目录，通过临时 systemd drop-in 的 `BindReadOnlyPaths` 只向后屏服务提供替代配置，并在重启后屏前安排独立恢复计时器。原生程序和磁盘上的原配置没有被改写。

这一恢复设计只覆盖配置和进程，遗漏了显示服务与 GUI 客户端之间的状态。发生冻结后，ADB、内核触摸事件、EW 回调和拍摄后台仍有响应，因此进程存活和点击计数都没有检出故障。具体触发点及显示链路失效原因尚未确定，不能断言单独由菜单控件、GUI 热重启或抓图工具造成。

恢复时发现相机正在写入一段录像。先发送一次已确认的原生快门短按，核对写入文件已关闭，再用设备上的 ffprobe 确认 MP4 可解析（242.64 秒，视频/音频流存在）。随后再次检查无媒体写入、无临时 GUI 配置，才执行正常系统重启。视频保留在原存储卡中，没有删除或改写。

重启后前后屏 GUI、拍摄和媒体服务正常启动，原生 GUI/图像插件/配置指纹保持不变，用户确认恢复。后续复测均沿用该次启动，没有再执行整机重启。本轮没有再次运行 `weston-screenshooter`；不能仅凭本轮成功断定旧抓图调用就是历史冻结的原因。

## 本地构建与只读检查

```sh
node tools/build-gui-plugin.mjs
python3 tools/test-gui-plugin.py --serial 123456789ABCDEF --mode probe
python3 tools/gui_display.py --serial 123456789ABCDEF --capture --require-progress
python3 -m unittest discover -s tools -p test_gui_display.py -v
```

产物在 `.build-tools/gui-plugin/`。未加入正式安装包或升级流程。

不带 `--run` 只做预检和主机证据保存。受控试验须亮屏、处于正常活动预览、无媒体写入及其他 GUI 配置覆盖，并通过显示基线检查：

```sh
python3 tools/test-gui-plugin.py --serial 123456789ABCDEF --mode native --seconds 6 --run
python3 tools/test-gui-plugin.py --serial 123456789ABCDEF --mode menu --exercise menu --cycles 3 --seconds 15 --run
```

`--hold-panel --seconds 120` 可在自动检查后留出有时限的人工观察窗口，结束后恢复原界面。试验材料和覆盖配置均放在 `/run`，未修改磁盘上的 GUI 程序或原插件配置。若整机重启恢复分支触发，只在应用目录内保留诊断 JSON。

## 持久安装

插件文件放 `/blackbox/upgrade/action-control/gui/`,通过**直接改 `/etc/disp0_plugins_config.json`** 把 `gui_image_loader_create` 条目的 `plugin` 指向该 `.so`,并在其 `parameter` 数组加 `ac_mode=menu;` `ac_state=/run/action-control-ui-persist;`。

mode/state 走 config parameter 串(`dji_gui` 运行时读取,此时 `/etc` overlay 已挂),而非 systemd 环境变量——本机 systemd 在 `/etc` overlay 挂载前就解析了 `gui.service`,冷启动时 drop-in/主单元的 `Environment=` 与 `ExecStartPre=` 都不会应用到运行实例。插件在 attach 时自建 state 目录(root 0700),不依赖 `ExecStartPre`;env 仍作为试验 harness 的回退。fingerprint SHA 校验保留为安全闸。

卸载:运行 `/blackbox/upgrade/action-control/gui/uninstall.sh`(从 `disp0_plugins_config.orig.json` 恢复原配置并重启 `gui.service`)。冷启动已验证:插件 5 秒内 attach、ticks 持续推进、面板两码经 scanout 解码可扫。
