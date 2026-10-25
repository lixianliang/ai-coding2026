# 架构设计

## 设计原则

架构采用“Linx 对话负责调起，独立游戏连接负责运行”：

- Linx 负责 XiaoZhi 的通用设备服务、入口对话和设备 MCP；
- Go 服务只负责指定房间内的游戏状态、语音处理和主持播报；
- 固件复用录音、Opus、播放和按键能力；
- 游戏结束时关闭 Game WSS，设备回到 idle，下一次唤醒再连接 Linx。

`xiaozhi-esp32` 是设备使用的开源 XiaoZhi ESP32 固件项目。本仓库在其基础上增加 `self.game.start`、临时 Game WSS URL 和 manual 收音支持。

## 服务关系

```text
yunliao-s3
  ├── Linx WSS：OTA、激活、普通对话、设备 MCP
  └── Go Game WSS：指定房间、manual 音频、AI 主持

Go 游戏服务
  ├── Gin / WebSocket
  ├── 房间 actor（1/2/3）
  ├── SQLite 题库与局记录
  ├── 豆包 ASR 2.0（转写和说话人编号）
  ├── DeepSeek（结构化判题）
  └── MiniMax TTS + Opus 编解码
```

Linx 与 Go 是两个独立服务端角色。Go 不替换 XiaoZhi，也不要求 Linx 在游戏期间转发连续音频。

## 连接切换

```text
校验 room_id（1/2/3）
        ↓
立即回复 MCP：已接受启动请求
        ↓
关闭 Linx 音频连接
        ↓
使用设备配置的 Game WSS 地址连接 Go
        ↓
携带 room_id 申请加入房间
        ↓
Go 校验房间容量并播放欢迎语
```

`room_id` 只是申请加入的目标，实际房间归属和容量由 Go 保存。MCP 的“已接受启动请求”只表示设备已接受异步任务，不表示已经连接成功。连接失败时设备清理游戏连接并回到 idle，后续唤醒恢复 Linx。

## 设备 MCP

设备注册普通 MCP 工具 `self.game.start`，由 Linx 智能体调用：

```json
{
  "name": "self.game.start",
  "description": "进入指定房间开始海龟汤游戏",
  "inputSchema": {
    "type": "object",
    "properties": {
      "room_id": {
        "type": "integer",
        "description": "房间号：1、2、3",
        "minimum": 1,
        "maximum": 3
      }
    },
    "required": ["room_id"],
    "additionalProperties": false
  }
}
```

设备本地入口为 `StartGame(int room_id)`。工具回调先返回受理结果，再异步关闭 Linx 并切换连接；游戏地址和认证信息由固件配置管理，不接受用户传入的地址。

## Game WSS 协议

Game WSS 复用 XiaoZhi 的 JSON 控制和 Opus 音频格式，不额外定义一套设备音频编解码：

- URL：`GET /v1/game?room_id=1`；
- 请求头：`Device-Id`、`Client-Id`、`Protocol-Version: 1|2|3`，可选 `Authorization: Bearer <device_token>`；
- 首帧为 `hello`，声明 Opus、16 kHz、单声道；
- 设备发送 `listen state=start mode=manual` 后上传 Opus；
- 设备发送 `listen state=stop` 后，Go 等待 ASR final；
- Go 发送 `tts state=start`、`sentence_start`、Opus 帧，最后发送 `tts state=stop`；
- `stt` 和 `sentence_start` 仅用于字幕和屏幕显示。

Go 内部统一使用 PCM 16 kHz、单声道、16-bit。TTS 采用 60 ms Opus 帧，先缓冲 5 帧再按节奏发送，避免设备解码队列被突发音频填满。

## 服务端状态机

每个房间由一个 actor 串行处理加入、离开、音频、ASR、判题、播报和计时器事件。典型状态为：

```text
idle → lobby → hosting → awaiting_question
                         ↓
                    listening → judging → hosting
                         ↓
                      finished
```

房间最多一个设备。`lobby` 是入房后的等待开局状态，不是独立大厅服务。设备断开即离房，不保存断线快照。

## 语音链路

```text
设备 Opus
   ↓
Go 解码 PCM
   ↓
豆包 ASR（partial/final + speaker_id）
   ↓
DeepSeek verdict
   ↓
MiniMax TTS PCM
   ↓
Go 编码 Opus，匀速发给设备
```

只有 manual start/stop 之间的音频进入 ASR。服务端不接收或转发玩家之间的普通讨论。ASR final 后，控制指令由房间状态机处理，普通提问交给 DeepSeek；固定汤底始终从题库读取。

## 固件职责

固件侧新增内容：

1. `mcp_server.cc` 注册 `self.game.start(room_id)`；
2. `application.cc/.h` 实现 `StartGame`；
3. `WebsocketProtocol` 支持临时 URL override，不改写 Linx 的持久化配置；
4. 游戏连接设置为 `kListeningModeManualStop`，yunliao-s3 BOOT 控制开始/结束收音；
5. 通过 `CONFIG_TURTLE_SOUP_GAME_URL` 配置 Game WSS 地址。

ES8388、Opus 编解码、AEC、屏幕和分区表沿用固件能力。

## 失败和恢复

- `room_id` 不在 1–3：MCP 参数校验失败，不切换连接；
- Game WSS 地址为空或连接失败：提示错误、清理临时连接、回到 idle；
- 房间已被占用：Go 返回 `room is full`，设备结束游戏连接；
- 游戏期间连接断开：Go 释放房间，设备回到 idle，下一次唤醒重新连接 Linx；
- 不做自动重连和断线续局。
