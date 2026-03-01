# 蓝牙设备自动发现与连接 — 体验增强计划

## 背景

当前蓝牙播放功能（Phase 1-4 已实现）采用方案 A：共享宿主音频服务器。容器通过 PulseAudio socket 透传访问宿主音频设备，蓝牙配对和连接需要在宿主机上手动完成（`bluetoothctl connect`）。

这带来的体验问题：
- 蓝牙设备断开后需要 SSH 到宿主机手动重连
- 用户无法从 Navidrome Web UI 直接管理蓝牙连接
- 对非技术用户不友好

## 目标

让用户可以直接在 Navidrome Web UI 中发现、配对、连接蓝牙音频设备，无需登录宿主机操作。

## 技术方案

### 核心思路：通过 D-Bus 控制 BlueZ

容器内通过共享宿主的 D-Bus system bus，调用 BlueZ D-Bus API 实现蓝牙设备管理。

```
Navidrome 容器 → D-Bus socket → 宿主 BlueZ 守护进程 → 蓝牙硬件
```

### Docker 配置变更

在现有蓝牙配置基础上增加 D-Bus system bus 透传：

```yaml
volumes:
  # 现有
  - /run/user/1000/pulse/native:/run/user/1000/pulse/native
  - /run/dbus:/run/dbus
  # 新增：D-Bus system bus socket
  - /var/run/dbus/system_bus_socket:/var/run/dbus/system_bus_socket
environment:
  - ND_JUKEBOX_BLUETOOTHMANAGEMENT=true
  - DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket
```

容器镜像需安装 `dbus` 包以提供 D-Bus 客户端工具链：

```dockerfile
RUN apk add -U --no-cache ffmpeg mpv sqlite pulseaudio-utils dbus
```

### BlueZ D-Bus API 关键接口

| 接口 | 用途 |
|------|------|
| `org.bluez.Adapter1.StartDiscovery` | 开始扫描附近蓝牙设备 |
| `org.bluez.Adapter1.StopDiscovery` | 停止扫描 |
| `org.bluez.Device1.Connect` | 连接已配对设备 |
| `org.bluez.Device1.Disconnect` | 断开设备 |
| `org.bluez.Device1.Pair` | 配对新设备 |
| `org.bluez.Device1.Trusted` | 设置信任（自动重连） |
| `org.freedesktop.DBus.ObjectManager.GetManagedObjects` | 列出所有已知设备 |

### Go 实现方案

使用 `godbus/dbus` 库与 BlueZ 通信：

```go
import "github.com/godbus/dbus/v5"

// 连接 D-Bus system bus
conn, _ := dbus.ConnectSystemBus()

// 列出已配对设备
obj := conn.Object("org.bluez", "/")
// call GetManagedObjects...

// 连接设备
device := conn.Object("org.bluez", "/org/bluez/hci0/dev_XX_XX_XX_XX_XX_XX")
device.Call("org.bluez.Device1.Connect", 0)
```

## 实施计划

### Phase 5.1: 后端 — BlueZ D-Bus 集成

1. 新建 `core/playback/bluetooth/bluez.go`
   - `ScanDevices(ctx, timeout)` — 扫描附近蓝牙设备
   - `ListPairedDevices(ctx)` — 列出已配对设备及连接状态
   - `ConnectDevice(ctx, mac)` — 连接指定设备
   - `DisconnectDevice(ctx, mac)` — 断开设备
   - `TrustDevice(ctx, mac)` — 设置信任以支持自动重连

2. 过滤条件：只返回支持 A2DP（Audio Sink UUID `0000110b`）的设备

3. 新增配置项：
   ```toml
   [Jukebox]
   BluetoothManagement = true  # 是否启用容器内蓝牙管理
   ```

### Phase 5.2: 后端 — REST API

新增 Native API 端点：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/bluetooth/devices` | 列出已配对 + 扫描到的设备 |
| POST | `/api/bluetooth/scan` | 触发蓝牙扫描（10s） |
| POST | `/api/bluetooth/connect` | 连接指定设备 |
| POST | `/api/bluetooth/disconnect` | 断开指定设备 |

### Phase 5.3: 前端 — 蓝牙管理 UI

在设备选择器菜单中增加：
- "Scan for devices" 按钮 — 触发蓝牙扫描
- 扫描结果列表 — 显示附近可用设备
- 连接/断开按钮 — 一键操作
- 连接状态实时更新

### Phase 5.4: Docker 配置与文档

- 更新 `docker-compose.bluetooth.yml` 增加 D-Bus 透传
- Dockerfile 增加 `dbus` 包
- 文档说明宿主机 D-Bus 权限配置

## 依赖

- `github.com/godbus/dbus/v5` — Go D-Bus 绑定
- 宿主机 BlueZ 守护进程运行中
- D-Bus system bus socket 可访问

## 风险

| 风险 | 缓解 |
|------|------|
| D-Bus 权限不足 | 提供 polkit 规则模板，或文档说明 `--privileged` 降级方案 |
| BlueZ 版本差异 | 仅使用稳定 API（Device1、Adapter1），兼容 BlueZ 5.50+ |
| 安全性 — 容器可控制宿主蓝牙 | 默认关闭，需显式启用 `BluetoothManagement=true` |
| 扫描干扰已有连接 | 扫描超时限制 10s，不影响已连接设备 |

## 优先级

中等 — 当前方案（宿主手动管理蓝牙）可用，此增强为锦上添花。

---

## Phase 6: Web UI 一键蓝牙播放（客户端→Jukebox 自动切换）

### 背景

当前 Navidrome Web UI 有两种播放模式：
- **客户端播放**（默认）：浏览器下载音频流，在用户本地设备播放
- **Jukebox 模式**：服务器端通过 MPV 播放，声音从服务器连接的设备输出

蓝牙设备选择器（DeviceSelector）目前只切换 Jukebox 的输出设备，但不会将播放模式从客户端切换到 Jukebox。用户点击蓝牙设备后，声音仍然从浏览器播放，体验不符合预期。

### 目标

用户在 Web UI 点击蓝牙设备后，音乐直接从服务器的蓝牙音箱播放出来，无需手动切换播放模式。

### 技术分析

#### 当前播放架构

```
客户端播放模式：
  浏览器 → GET /rest/stream → 浏览器 <audio> 元素 → 本地扬声器

Jukebox 模式：
  Subsonic 客户端 → POST /rest/jukeboxControl → PlaybackServer → MPV → 音频设备
```

Web UI 当前只实现了客户端播放，Jukebox 控制仅通过 Subsonic API 暴露给第三方客户端（Ultrasonic、Submariner 等）。

#### 需要实现的流程

```
用户点击蓝牙设备 → DeviceSelector 切换设备
                 → 将当前播放队列发送给 Jukebox
                 → 停止浏览器端 <audio> 播放
                 → Jukebox 通过 MPV 在蓝牙设备上播放
                 → UI 显示 Jukebox 播放状态（进度、音量控制代理到服务器）

用户点击"本地播放"  → 停止 Jukebox
                   → 恢复浏览器端 <audio> 播放
```

### 实施步骤

#### 6.1 后端 — Native API Jukebox 控制端点

当前 Jukebox 控制仅通过 Subsonic API（`/rest/jukeboxControl`）暴露。需要为 Native API 添加对应端点：

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/jukebox/play` | 开始/恢复 Jukebox 播放 |
| POST | `/api/jukebox/stop` | 停止 Jukebox 播放 |
| POST | `/api/jukebox/pause` | 暂停 |
| POST | `/api/jukebox/skip` | 跳到指定曲目 |
| POST | `/api/jukebox/set` | 设置播放队列（接收 song ID 列表） |
| GET | `/api/jukebox/status` | 返回当前播放状态（曲目、进度、音量） |
| POST | `/api/jukebox/volume` | 设置音量 |
| POST | `/api/jukebox/seek` | 跳转到指定位置 |

#### 6.2 前端 — 播放模式切换逻辑

修改 `DeviceSelector.jsx`：
- 选择蓝牙/远程设备时：
  1. 调用 `POST /api/jukebox/devices/switch` 切换输出设备
  2. 获取当前浏览器播放队列和进度
  3. 调用 `POST /api/jukebox/set` 发送队列
  4. 调用 `POST /api/jukebox/play` 从当前进度开始播放
  5. 暂停浏览器端 `<audio>` 元素
  6. 切换 UI 到 Jukebox 控制模式
- 选择"本地播放"时：
  1. 调用 `POST /api/jukebox/stop`
  2. 恢复浏览器端播放

#### 6.3 前端 — Jukebox 播放状态 UI

在 Jukebox 模式下，播放器工具栏需要：
- 进度条代理到服务器端状态（轮询 `GET /api/jukebox/status`）
- 音量控制代理到服务器端（`POST /api/jukebox/volume`）
- 上一曲/下一曲/暂停按钮调用 Jukebox API
- 视觉指示当前处于 Jukebox 模式（如设备图标高亮、提示文字）

#### 6.4 状态同步

- Jukebox 播放状态通过 SSE（`server/events/`）推送到前端，避免频繁轮询
- 曲目切换、播放结束等事件实时同步

### 关键文件

| 文件 | 变更 |
|------|------|
| `server/nativeapi/jukebox_control.go` | 新建 — Jukebox 控制 REST 端点 |
| `server/nativeapi/native_api.go` | 修改 — 注册新路由 |
| `ui/src/audioplayer/DeviceSelector.jsx` | 修改 — 设备切换时触发模式切换 |
| `ui/src/audioplayer/AudioPlayer.jsx` | 修改 — 支持 Jukebox 模式下的播放控制代理 |
| `ui/src/audioplayer/PlayerToolbar.jsx` | 修改 — Jukebox 模式视觉指示 |

### 优先级

高 — 这是蓝牙播放功能的核心用户体验，没有此功能蓝牙设备选择器实际上无法使用。
