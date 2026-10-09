# 依据验证证据选择播放路线与投入

Type: grilling
Label: wayfinder:grilling
Mode: HITL
Status: open
Assignee: unassigned
Parent: ../map.md
Blocked by: 05, 06

## Question

以实际验收结果、增量收益和一年维护成本为依据，应继续 MA + Bridge、进入 Navidrome + Go SDK，还是有限采用其他可复用 SDK；哪些候选应停止投入？

- 比较同一硬件、音源和已批准政策下的质量，不把一个音频互通 demo 与另一路生产闭环混比。
- 共用输出生命周期成本必须计入各路线；原生页面收益不等于蓝牙稳定性收益。
- 计入应用层、Bridge 管理功能适配、状态/权限、SDK 版本兼容、构建、升级回归、物理故障定位与上游补丁维护。
- 若 Go SDK 可以直接复用，不按从零实现估价；若必须补整代 wire/安全/角色，不按直接 import 估价。证据不足的路线不能成为确定投产排期。
- 明确第一阶段的单一播放权威、受支持版本、功能边界、实际实施预算和止损条件；不同时保留两个对同一输出负责的音乐引擎。
- 原型因安全/预算被取消时，由用户确认范围变化，按 Wayfinder 关闭并记录 Out of scope，不能伪造失败或成功。

调用 grilling 和 domain-modeling，与用户决定选型后关闭。决策只存本票，地图仅保留 gist。
