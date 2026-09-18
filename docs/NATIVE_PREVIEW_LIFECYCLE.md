原生预览生命周期调查，2026-09-19。

**目前不能把 `camera_start_stream` 当作任意网页消费者的订阅接口。** 已找到它在原生界面中的实际用途，以及与 Mimo 订阅不同的事件路径；独立消费者的数据出口和清理规则尚未闭环。本轮没有调用这些流控制入口。

| 已核对位置 | 证据及结论 |
| --- | --- |
| `dji_gui_on_disp0` 的 `gui_set_liveview_stream_state`，`0xa059c0` | GUI 使用字符串 `front` / `rear` 和 terminal `0` 调用 `camera_start_stream` / `camera_stop_stream`；其中 rear 分支还检查 `dji.gui_disp1`。这是原生显示通道的实际调用证据，不是允许任意字符串创建消费者的证据 |
| 客户端 C 包装入口，`0x66fb0` / `0x67550` | 参数为相机句柄、非空字符串、terminal；经代理进入 Binder。空句柄等错误参数可进入 fatal 路径，不能试填参数探测 |
| 服务端 `camera::start_stream`，`0x14e780` | 把字符串转交 `cam_event_handler_start_stream`，没有在这个接口中返回视频句柄、文件描述符或数据回调 |
| `cam_event_handler_start_stream`，`0x95500` | 接受 terminal `0/1`，分别写入请求类型 `0/2`；key 长度必须小于 64；操作字节为 `0`，进入内部事件 `0x14` |
| `cam_event_handler_stop_stream`，`0x963d0` | 同样携带 terminal 和 key，经内部事件 `0x14`；停止操作字节随 bootmode 等条件使用 `1` 或 `2`，不是无条件的“释放我的一个引用” |
| 两个事件包装器的完成等待 | 一条分支安装 looper/co-message 完成指针并等待返回。调用返回、输出停止、消费者引用释放分别需要验证，不能把一个返回 0 当作全链路验收 |
| Mimo 的 `cs_cam_duss_proxy_on_liveview_subscribe`，`0x36950` | 检查消息至少 11 字节，把载荷交给内部 `0x08020001` 事件；它与上述 `0x14` 不能仅凭“预览”名称视作同一个协议 |
| `cs_camera_state_on_liveview_subscribe`，`0x1ae80` 附近 | 可见 `liveview_subscribe_manager_on_event`、随后读取并发布订阅状态；已有输出配置和通道分配管理逻辑仍由相机持有 |
| 已检查的客户端断开及 Binder death 路径 | 移除该客户端监听/引用；尚未发现可据以保证“任意 stream key 在进程死亡时自动退订”的证据。录像读取客户端释放已验证，不代表流订阅释放也已验证 |

`libdcam_liveview_subscribe_manager.so` 中的 `/system/etc/lsm_config[_suffix].json` 和 `/tmp/lsm_status[_suffix].json` 等字符串是候选路径。此前设备目录清单没有找到这些文件；后缀的实际来源及是否生成仍待核对。库中的多通道数组也不能证明允许第二个独立消费者。

独立预览仍需完成：

1. key 和 terminal 对应的资源、引用计数及允许的消费者身份。
2. 编码视频如何交付本地消费者，是否需要传输握手，以及格式协商。
3. 正常退订、异常断开、进程死亡后的清理；是否会影响屏幕或 Mimo。
4. 连接第二个消费者、录像切换、退出后的实机共存验收。

这些项目没有全部完成，因此本轮开发的是 [Mimo 网页预览转发](MIMO_WEB_PREVIEW.md)：只读取已有输出的副本，不创建第二个原生订阅。它依赖 Mimo 保持实时预览，不能替代上面的独立消费者调查。

静态证据保存在 `.build-tools/mimo-native-analysis/disasm/`，入口和哈希清单为 `preview-lifecycle-evidence.json`。GUI 副本 SHA-256 为 `30eb933b1c2315984e150885124d277c13c1cef3194ba6c08cb3d4d1f6656db1`；服务端库哈希与 [原生复用文档](NATIVE_CAMERA_REUSE.md) 中固定的固件一致。私有二进制及反汇编只保存在忽略的研究目录。
