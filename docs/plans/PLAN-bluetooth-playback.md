# Navidrome 蓝牙音频播放功能 - 实施计划

## 1. 项目背景与目标

### 当前状态
Navidrome 已有 **Jukebox 模式**（服务器端播放），通过 mpv 子进程在服务器本地音频设备上播放音乐。核心架构：

- `core/playback/playbackserver.go` — PlaybackServer 单例，管理音频设备
- `core/playback/device.go` — playbackDevice，实现播放控制（play/pause/stop/skip/volume/queue）
- `core/playback/mpv/track.go` — MpvTrack，每首歌启动一个 mpv 进程，通过 IPC socket 控制
- `core/playback/mpv/mpv.go` — mpv 命令构建，默认模板：`mpv --audio-device=%d --no-audio-display %f --input-ipc-server=%s`
- `conf/configuration.go` — Jukebox 配置：`Jukebox.Enabled`, `Jukebox.Devices[]`, `Jukebox.Default`, `MPVCmdTemplate`

### 目标
在 Navidrome 服务器上实现通过蓝牙设备播放音乐的能力，用户可以在 Web UI 或 Subsonic 客户端中选择蓝牙音频设备作为 Jukebox 输出。

---

## 2. 可行性分析

### 2.1 技术栈分析

#### Linux 蓝牙音频链路
```
应用程序 → PulseAudio/PipeWire → BlueZ (D-Bus) → 内核 HCI 驱动 → 蓝牙硬件
```

关键组件：
- **BlueZ**: Linux 官方蓝牙协议栈，通过 D-Bus 系统总线通信
- **PulseAudio/PipeWire**: 音频路由层，将蓝牙设备暴露为虚拟音频 sink
- **A2DP**: 蓝牙立体声音频传输协议

#### mpv 与蓝牙的兼容性
mpv 支持 PulseAudio 和 PipeWire 作为音频输出后端。当蓝牙设备通过 PulseAudio/PipeWire 连接后，mpv 可以直接使用该蓝牙 sink 播放音频：
```bash
# 列出可用音频设备（包括蓝牙）
mpv --audio-device=help

# 指定 PulseAudio sink 播放
mpv --audio-device=pulse/bluez_sink.XX_XX_XX_XX_XX_XX.a2dp_sink file.mp3
```

**结论：mpv 原生支持蓝牙音频输出，无需修改播放引擎。**

### 2.2 Docker 容器挑战分析

#### 核心挑战

| 挑战 | 说明 | 严重程度 |
|------|------|----------|
| D-Bus 隔离 | 容器默认无法访问宿主 D-Bus 系统总线，BlueZ 完全依赖 D-Bus | 高 |
| 网络命名空间 | 蓝牙使用 AF_BLUETOOTH socket family，容器默认隔离 | 高 |
| 音频服务器隔离 | 容器内无法直接访问宿主的 PulseAudio/PipeWire | 高 |
| 内核设备访问 | 蓝牙 HCI 适配器需要设备直通 | 中 |
| 权限限制 | BlueZ 操作需要 NET_ADMIN 等 capabilities | 中 |

#### 可行方案对比

**方案 A：共享宿主音频服务器（推荐）**
- 将宿主的 PulseAudio/PipeWire socket 挂载到容器内
- 容器内的 mpv 作为音频客户端连接宿主音频服务器
- 蓝牙配对和管理在宿主完成
- 容器只需要音频 socket 访问，无需特权模式

```yaml
volumes:
  - /run/user/1000/pulse/native:/run/pulse/native:ro
  - ~/.config/pulse/cookie:/root/.config/pulse/cookie:ro
environment:
  - PULSE_SERVER=unix:/run/pulse/native
```

**方案 B：完全特权模式**
- `privileged: true` + `network_mode: host` + D-Bus socket 共享
- 容器内运行完整蓝牙栈
- 安全性差，但配置简单

**方案 C：混合方案（推荐用于需要容器内管理蓝牙的场景）**
- D-Bus socket 共享 + 定向设备直通
- 容器可以发现和管理蓝牙设备
- 比方案 B 安全性更好

### 2.3 可行性结论

| 维度 | 评估 | 说明 |
|------|------|------|
| 技术可行性 | ✅ 高 | mpv 原生支持 PulseAudio/PipeWire 蓝牙 sink |
| 代码改动量 | ✅ 低-中 | 主要是配置层和设备发现，播放引擎无需改动 |
| Docker 可行性 | ⚠️ 中 | 需要额外的 volume 挂载和环境变量配置 |
| 裸机部署 | ✅ 高 | 直接使用系统蓝牙，几乎零额外配置 |
| 用户体验 | ⚠️ 中 | 蓝牙配对仍需在宿主/系统层面完成 |

---

## 3. 实施方案

### 架构选择：方案 A（共享宿主音频服务器）

理由：
1. 安全性最好 — 不需要特权模式
2. 与 Navidrome 现有 Jukebox 架构完美契合 — mpv 只需指定正确的 audio-device
3. 改动最小 — 主要是增加蓝牙设备发现和 Docker 配置文档
4. 蓝牙配对在宿主管理，更稳定可靠

---

## 4. 分阶段实施计划

### Phase 1: 蓝牙音频设备发现（后端）
**目标**: 让 Navidrome 能够发现并列出可用的蓝牙音频设备

#### 任务 1.1: 创建蓝牙设备发现模块
- 新建 `core/playback/bluetooth/` 包
- 通过解析 `pactl list sinks short` 或 PulseAudio D-Bus 接口发现蓝牙 sink
- 蓝牙 sink 命名规则：`bluez_sink.<MAC>.a2dp_sink`
- 提供 `DiscoverBluetoothSinks() []AudioSink` 接口

#### 任务 1.2: 扩展 Jukebox 设备配置
- 在 `conf/configuration.go` 中添加蓝牙相关配置项：
  ```toml
  [Jukebox]
  Enabled = true
  # 新增：自动发现蓝牙设备
  AutoDiscoverBluetooth = true
  ```
- 修改 `playbackserver.go` 的 `initDeviceStatus()`，在初始化时合并手动配置的设备和自动发现的蓝牙设备

#### 任务 1.3: 添加蓝牙设备列表 API
- 在 Subsonic API 或 Navidrome Native API 中添加端点，返回可用蓝牙设备列表
- 复用现有 `jukeboxControl` 的 `get` action 返回设备信息

### Phase 2: 蓝牙播放集成（后端）
**目标**: 通过 mpv 在蓝牙设备上播放音乐

#### 任务 2.1: 适配 mpv 音频设备参数
- 蓝牙设备的 mpv audio-device 格式：`pulse/bluez_sink.XX_XX_XX_XX_XX_XX.a2dp_sink`
- 确保 `createMPVCommand()` 正确处理蓝牙设备名称中的特殊字符
- 验证 mpv 的 `--audio-device` 参数对蓝牙 sink 的兼容性

#### 任务 2.2: 设备切换支持
- 扩展 `PlaybackServer` 接口，支持运行时切换音频输出设备
- 当用户选择蓝牙设备时，更新 `playbackDevice.DeviceName`
- 如果正在播放，需要重启当前 track 的 mpv 进程以应用新设备

#### 任务 2.3: 连接状态监控
- 监控蓝牙设备连接状态（通过 PulseAudio sink 存在性检查）
- 设备断开时暂停播放并通知前端
- 设备重连时恢复播放状态

### Phase 3: 前端 UI（Web 界面）
**目标**: 在 Navidrome Web UI 中展示蓝牙设备选择

#### 任务 3.1: 设备选择器组件
- 在 Jukebox 播放控制区域添加音频输出设备下拉选择器
- 显示设备名称、类型（本地/蓝牙）、连接状态
- 蓝牙设备用图标区分

#### 任务 3.2: 设备状态实时更新
- 定期轮询设备列表（或使用 WebSocket）
- 蓝牙设备连接/断开时更新 UI 状态

### Phase 4: Docker 部署支持
**目标**: 提供完整的 Docker 蓝牙音频配置方案

#### 任务 4.1: Docker Compose 蓝牙配置模板
- 创建 `contrib/docker-compose/docker-compose.bluetooth.yml`
- 包含 PulseAudio/PipeWire socket 挂载配置
- 提供 PulseAudio 和 PipeWire 两种配置示例

#### 任务 4.2: Dockerfile 增强
- 在 Alpine 最终镜像中添加 `pulseaudio-utils`（提供 `pactl` 命令）
- 可选：添加 `pipewire-tools`

#### 任务 4.3: 宿主环境准备脚本
- 提供宿主端蓝牙配对和音频配置的辅助脚本
- 包含 PulseAudio 和 PipeWire 的配置说明

### Phase 5: 测试与文档
**目标**: 确保功能稳定可靠

#### 任务 5.1: 单元测试
- 蓝牙设备发现模块的 mock 测试
- 设备切换逻辑测试
- 配置解析测试

#### 任务 5.2: 集成测试
- 裸机环境蓝牙播放测试
- Docker 容器蓝牙播放测试（PulseAudio 方案）
- Docker 容器蓝牙播放测试（PipeWire 方案）

#### 任务 5.3: 文档
- 更新 Navidrome 配置文档，添加蓝牙相关配置说明
- Docker 蓝牙部署指南
- 故障排除指南（常见蓝牙连接问题）

---

## 5. 关键文件变更清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `core/playback/bluetooth/discover.go` | 新建 | 蓝牙设备发现 |
| `core/playback/playbackserver.go` | 修改 | 集成蓝牙设备发现，设备切换 |
| `core/playback/device.go` | 修改 | 支持运行时设备切换 |
| `core/playback/mpv/mpv.go` | 修改 | 验证蓝牙设备名兼容性 |
| `conf/configuration.go` | 修改 | 添加蓝牙配置项 |
| `server/subsonic/jukebox.go` | 修改 | 扩展设备列表/切换 API |
| `Dockerfile` | 修改 | 添加 pulseaudio-utils |
| `contrib/docker-compose/docker-compose.bluetooth.yml` | 新建 | 蓝牙 Docker 配置模板 |
| `ui/src/audioplayer/` | 修改 | 设备选择器 UI |

---

## 6. 风险与缓解措施

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 蓝牙设备延迟导致音频卡顿 | 中 | 中 | mpv 内置缓冲机制，可调整 `--audio-buffer` 参数 |
| Docker 中 PulseAudio socket 权限问题 | 高 | 高 | 提供详细的权限配置文档，支持 cookie 和匿名认证两种模式 |
| 蓝牙设备意外断开 | 高 | 中 | 实现连接状态监控，自动暂停并通知用户 |
| 不同 Linux 发行版音频栈差异 | 中 | 中 | 同时支持 PulseAudio 和 PipeWire，PipeWire 提供 PulseAudio 兼容层 |
| mpv 蓝牙 sink 名称格式不一致 | 低 | 低 | 通过 pactl 动态发现而非硬编码格式 |

---

## 7. 建议实施顺序

1. **先验证**（Phase 0）: 在宿主机上手动测试 `mpv --audio-device=pulse/bluez_sink.XX.a2dp_sink file.mp3` 确认基础链路可行
2. **Phase 1 + 2**: 后端核心功能（设备发现 + 播放集成）
3. **Phase 4**: Docker 支持（与后端并行开发）
4. **Phase 3**: 前端 UI
5. **Phase 5**: 测试与文档
