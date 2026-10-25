# ai-coding2026

线下多人围坐的海龟汤语音游戏：一台 `yunliao-s3` 作为 AI 主持人，玩家按 BOOT 提问，每局 10 分钟。

- `xiaozhi-esp32` 是设备使用的开源 XiaoZhi 固件项目；本仓库在其基础上增加海龟汤 MCP 入口、Game WSS 切换和 manual 收音支持。
- Linx 可以理解为 XiaoZhi 固件配置连接的服务端，负责 OTA、设备激活、普通入口对话和设备本地 MCP；进入游戏后设备临时切换到独立 Go 服务的 Game WSS。
- Go 服务负责房间 1/2/3、可配置题库、manual 收音、计时、判题和 TTS。
- 一台设备供多人围坐共玩；进入房间先播放欢迎提示，玩家说“开始游戏”后才出题。
- AI 播报题面后，玩家之间可以直接讨论，只有需要向 AI 提问、猜测汤底或发出继续/退出指令时才按 BOOT 发起一次 manual 收音。
- manual 收音期间设备流式上传语音，按键结束后服务端获取 ASR 最终文本并判题；豆包 ASR 将说话人映射为本局“玩家 1、2、3”，无需报名字，不做独立声纹注册或玩家音频转发。
- 设备网络链路使用 Opus，服务端内部统一 PCM 16 kHz、单声道、16-bit。
- 猜中或超时后朗读固定汤底并进入讨论态；说“游戏继续”才开下一局，说“退出游戏”释放房间。
- 当前不做 AI 玩家、真人主持和掉线恢复。

## 交付入口

- [源码、运行与交付清单](docs/project-delivery.md)
- [Demo 录制脚本与视频状态](docs/demo/README.md)（视频待录制）
- [服务端启动、配置与验证命令](server/README.md)

方案与演示：[docs/design/turtle-soup-game-simplified.md](docs/design/turtle-soup-game-simplified.md)。

产品定位与商业设计：[docs/design/turtle-soup-product-positioning.md](docs/design/turtle-soup-product-positioning.md)。

原方案：[docs/design/turtle-soup-game.md](docs/design/turtle-soup-game.md)。

## AI 工程效率

仓库内置可重复使用的研发 skills，减少每次从零排查环境与回归链路：

- `.agents/skills/xiaozhi-yunliao-s3-firmware/`：`yunliao-s3` 构建、烧录、串口日志与真机验收。
- `.agents/skills/go-test-engineering/`：Go 单测/E2E 分层、race 与异步测试约束。
- `.agents/skills/go-provider-debugging/`：ASR/LLM/TTS provider 与完整语音链路定位。
- `.agents/skills/quick-pr/`：强制 `codex/*` 分支、限定路径检查、提交和 PR 流程。

常用回归命令：

```bash
cd server
go run ./cmd/eval -config config.local.json
go run ./cmd/eval -config config.local.json -e2e
go test -race ./...
```
