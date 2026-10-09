# 确定控制入口与原生页面的保留范围

Type: grilling
Label: wayfinder:grilling
Mode: HITL
Status: open
Assignee: unassigned
Parent: ../map.md
Blocked by: 07

## Question

选定播放路线后，第一阶段使用哪个真正的远端控制入口；保留 Navidrome 曲库页面或增加受限控制面板是否值得额外投入？

- 若由 MA 持有播放权威，界面只发送意图和展示其状态，Navidrome 不镜像维护另一份可推进队列。
- 若由 Navidrome 持有权威，远端模式不得通过挂载静音音频维持原播放器。界面只控制和观察服务器，纯控制能力需核实或实现。
- 当前安装库与锁定版本存在差异，未确认 headless/playing/position 受控能力；不能把现成 flag 当作预算依据。
- 明确必须的播放、暂停、停止、跳转、音量、队列与错误显示，以及媒体标识、鉴权、重连状态和控制页面关闭的契约；不默认完整原播放器体验等价。
- 原生页面不是硬约束。若其增量回报不足，选择现有或独立薄控制入口，并明确后续视觉原型哪些区域仍需讨论。

调用 grilling 和 domain-modeling，与用户确认控制范围。此票决定范围，不直接开始前端重写。
