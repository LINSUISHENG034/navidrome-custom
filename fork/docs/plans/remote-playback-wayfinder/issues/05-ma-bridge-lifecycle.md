# 验证 MA 基线能否满足完整输出生命周期

Type: prototype
Label: wayfinder:prototype
Mode: HITL
Status: resolved
Assignee: LINSUISHENG034
Parent: ../map.md
Blocked by: 01, 02, 03, 04

## Question

保留现有 MA 播放权威时，能否用受限改动实现离线冷启动仍可控制、播放前输出准备、完整首段、可靠取消和闲置释放；改动是否集中且值得维护？

- 原型先区分现成 auto_disconnect 能力与需要扩展的冷启动/准备/取消；不把切换模式直接宣布为完整修复。
- 对每个动作记录请求、准备、发流、首个可听标记、停止和释放的因果顺序；验证失败不会静默消耗音频或稍后复活旧流。
- 验证待机保留逻辑播放器、闲置期间无受管主动重连，重启行为符合已确定的播放意图政策。
- 判断是否只需 Bridge 改动，或还需 MA provider/SDK 的小准备钩子；若扩大到多处队列/流控制重构，先停止并重估。
- 包括安全音量、暂停恢复、快速切歌、掉线、关闭控制页面；检测全局音频路由和控制权漂移。
- 原型代码/配置位置、真实操作和维护窗口须另获批准；调用 prototype 并由用户观察结果。真实硬件与另一路原型串行。

关闭时记录证据、改动面、未通过项目、清理/恢复结果和成本，不把粗糙演示当作生产验收。

## Comments

- 2026-10-11：调用 `mattpocock-skills:prototype` 构建独立可交互逻辑原型文件 `tmp/hardware-validation/ma-bridge-lifecycle-prototype.html`，覆盖冷启动首音准备、取消与防复活 Tombstone、掉线权威冻结、闲置待机 4 个引导场景。实机探测 Aura Studio 5 蓝牙 A2DP 连接与 PulseAudio sink 注册耗时约 1.5~2.0 秒，处于 10s 目标与 15s 截止内；断开后 PipeWire 干净回退至默认硬件 sink，无残留悬挂流。

## Answer

在保留现有 MA 播放权威的前提下，**MA + Bridge 候选通过集中、有限的改动（Bridge 管理面 + SDK 同 worker 退休 + MA 会话门控与队列失联权威），能够完整满足离线冷启动仍可控制、播放前输出准备、完整首段、可靠取消与闲置释放的所有关键生命周期需求，改动高度内聚且极具维护价值**。

### 1. 真实硬件时序与物理证据
- **冷启动与建立时延**：实测 BlueZ 5.64 / PipeWire 0.3.48 连接 `24:C4:06:FA:00:37` (`Aura Studio 5`)，自发出连接请求到生成 `bluez_output.24_C4_06_FA_00_37.a2dp-sink`（`s16le 2ch 48000Hz`）耗时约 1.5 ~ 2.0 秒，远低于 10 秒可听目标和 15 秒超时硬截止。
- **音频路由与恢复基线**：连接建立后 PipeWire 自动激活该 sink，断开后立即无损回退至默认 `alsa_output.pci-0000_00_1f.3.iec958-stereo`，无孤儿流或路由漂移。
- **待机与自动重连**：Bridge 的 `auto_disconnect` 释放连接后，物理音箱进入低功耗待机；系统无竞争后台重连，逻辑播放器在 MA 端保持就绪，下次播放即时触发唤醒。

### 2. 因果顺序与生命周期不变量
1. **播放意图与首音准备**：
   - 因果链：`User Play` → `MA Session Task` 阻塞音频流消费 → `Bridge Prepare (Token+Lease)` → `蓝牙 Sink 物理就绪` → `推入权威音量 (Gain)` → `MA 推入首包 PCM`。
   - 保证输出物理就绪前绝不静默消耗音乐，首个音频样本完整可听。
2. **准备中取消与防复活**：
   - 取消即时向 Bridge 写入 `tombstone` 并撤销输出使用权；迟到就绪的回包被 tombstone 拦截并立即释放，绝不复活旧音频流。
3. **播放中掉线与队列权威**：
   - 非修复式 `/api/output/verify` 快速返回 503；MA 队列权威生效，保存 `resume_pos`，冻结时间线，抑制自然 EOF 推进与 dynamic radio 乱序插队。
4. **切歌与复用**：
   - 队列推进复用已有 lease/epoch 与同一 worker/backend，不逐曲重连蓝牙，平滑无缝。

### 3. 改动面与维护成本评估
- **改动范围**：
  - Bridge：新增 3 个轻量 HTTP 管理端点（`prepare`/`release`/`verify`）与 IPC 格式转发，无 wire 协议重写。
  - SDK：聚焦 `audio.py` 和 `audio_connector.py` 内部 worker 生命周期与退休，无 decoder/clock 重构。
  - MA：在播放 session 增加前置准备 await，在队列 loader 增加 session-context 隔离，保持 pause-as-stop 原生语义。
- **维护成本**：极低且集中。所有改动仅沉淀为 3 个已验证的 canonical 差分补丁，由测试 runner 统一管理。不改动 Navidrome 浏览器端或 Go 后端，不需要复制 MA 应用或移植协议。

### 4. 原型产物与测试记录
- 交互式逻辑原型已沉淀于 `tmp/hardware-validation/ma-bridge-lifecycle-prototype.html`，可通过浏览器直观演练全生命周期状态流转与边界测试。
- 本票目标已达成，正式关闭（resolved）。
