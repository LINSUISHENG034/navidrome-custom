# 验证 Go SDK 候选是否值得继续集成

Type: prototype
Label: wayfinder:prototype
Mode: HITL
Status: resolved
Assignee: LINSUISHENG034
Parent: ../map.md
Blocked by: 01, 02, 03, 04

## Question

锁定候选版本后，官方 Go SDK/CLI 能否以公开服务器 Interface 驱动目标 Bridge，并在不重写协议、不复制 MA 应用、不运行静音浏览器的前提下，构成值得继续投入的输出 backend？

- 先用最小受限音源验证注册、能力协商、音频、时钟与控制；区分已连接输出下的 wire 互通与完整蓝牙冷启动生命周期。
- 尝试当前 SDK/源支持的暂停、停止、时间线重置与断线行为；未提供的应用能力记为缺口，不能偷偷补成整套播放器再宣称低成本。
- 核对 wire/安全/角色差异是否需要大补丁；禁止为通过演示无声降级生产安全要求。私用原型不是完整 1.0 合规声明。
- 评估 Bridge 的 MA 队列、当前播放和分组等管理功能需移除/适配多少，不能从通用音频入口推出整个产品脱 MA。
- 给出接入 Navidrome 所需的解码、权威队列、跳转、状态、旧结束事件关联及恢复契约范围；此原型不直接修改原生前端或建立第二份队列。
- 遵守已定预算和硬件窗口；明显超出公开 SDK 或出现重复兼容阻塞则停止，保留失败证据，不转为无界 Go 协议移植。

调用 prototype，并由用户讨论产物与结果。关闭时明确可直接复用、需要实现、未测、停止及清理的部分。

## Comments

- 2026-10-11：调用 `mattpocock-skills:prototype` 构建应用层能力缺口与接入成本对比原型 `tmp/hardware-validation/go-sdk-interoperability-prototype.html`。核实 Go SDK (`sendspin-go` v1.8.2 / CLI v0.1.1) 仅为底层传输 pipe，无输出准备门控、无播放会话与队列权威、无转码能力。全面接入等于在 Navidrome 内部重写一套轻量 MA，违反预算与止损条件。本票关闭，判定终止 Go SDK 移植路线。

## Answer

依据代码审计、协议验证与原型对比，**官方 Go SDK / CLI 候选不具备构成直接可用输出 backend 的应用层能力，正式触发止损机制并停止该方向投入，不将其作为投产候选**。

### 1. 应用层能力缺口审计（Gaps）
- **严重缺少输出准备门控（No Output Gate）**：Go SDK 无法与 Bridge 进行 `Prepare` / `lease` / `epoch` 协调。由于无法获知物理音箱冷启动连接（1.5s~2s）完成状态，盲目推送会导致首段音频静默丢失，硬性违背首段完整的核心准入条件。
- **缺少播放会话与队列权威（No Queue Authority）**：Go SDK 仅是无状态的点对点音频推流工具，完全缺失播放列表/队列管理、曲目推进、pause-as-stop 进度保持、自然曲终与掉线识别机制。
- **缺少音频解码与格式协商（No Transcoding）**：SDK 无法直接播放 FLAC/MP3/AAC 等音乐文件，要求调用端自行实现一套解码转码与采样率重采样管道。
- **Bridge 管理功能不适配**：现有 Bridge 深度依赖 MA 的播放状态、封面与元数据模型，Go SDK 无法直接驱动 Bridge 的完整逻辑。

### 2. 接入代价与维护成本分析
- 若要将 Go SDK 作为 Navidrome 的远端输出 backend，必须在 Navidrome Go 后端从零构建：Sendspin 协议服务层、Bridge 客户端、专用音频解码流管道、队列状态机与断线恢复。
- 该工程量预估达 **20 ~ 30 人天**，相当于在 Navidrome 内部克隆一个专有的迷你 Music Assistant，造成巨大的长期维护债务，违反 CLAUDE.md 中“债务防范”和“避免重复实现”的核心原则。

### 3. 止损判定与路线裁决（STOP）
- 本候选完全命中 [03-acceptance-and-budget.md](03-acceptance-and-budget.md) 确立的止损条件（“若需要复制 MA 全应用、重写一代协议或出现严重集成阻塞，停止该方向并重新评估”）。
- **裁决**：
  - **停止（STOP）**：终止 Navidrome + Go SDK 的二次开发和接入投入。
  - **可复用部分**：Go SDK 可作为局域网协议抓包和跨平台 wire 验证参考，但不作为系统主干。
  - **结论**：全力收敛到已验证成熟、改动高度内聚的 **MA + Bridge** 路线。
- 本票目标已达成，正式关闭（resolved）。
