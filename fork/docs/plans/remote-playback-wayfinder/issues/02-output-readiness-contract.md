# 确定输出准备与释放的最小协调位置

Type: research
Label: wayfinder:research
Mode: AFK
Status: claimed
Assignee: LINSUISHENG034
Parent: ../map.md
Blocked by: none
Research branch: research/remote-playback-output-contract
Research asset: ../research/output-readiness-contract.md

## Question

为实现「输出真正就绪后才消耗可听音频、停止不复活旧请求、待机不反复重连」，最小协调 Interface 应放在哪里，哪些事实可以从源码确定，哪些必须由真实设备原型回答？

- 追踪目标 Bridge 的启动注册、自动待机、唤醒触发、输出重定向、重连抑制及管理接口依赖；区分自动 standby 和停止 daemon 的手动 Release。
- 核对 MA 发流/时间线和 SDK 的最小接入位置，辨别既有 activation/准备钩子是否可用，不先假定必须修改整个 MA 或另造协议。
- 核对 available、lead-time、时钟同步与实际物理输出就绪的关系；识别循环等待和首段静默消耗风险。
- 说明停止/切歌/断线的取消与旧结果判定需要什么契约，但不得把未审计或未复现的 stop-cancel 风险写成已确认 bug。
- 记录共享音频栈的全局默认输出、rescue 和 mDNS/自动发现等副作用边界，以及未来实验的隔离要求。
- 给出 MA 基线和 Go 候选可共用的最小准备/释放模型、需要改动的项目范围、未经实测的部分及原型观察项；不实现或操作设备。

研究报告持有源码与规范证据；Answer 选择最值得验证的协调位置，而不宣布生产问题已经解决。
