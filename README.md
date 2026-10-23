# ai-coding2026

线下多人围坐的海龟汤语音游戏：一台 `yunliao-s3` 作为 AI 主持人，玩家按 BOOT 提问，每局 10 分钟。

- Linx 负责 OTA、入口对话和设备本地 MCP；进入游戏后切换到 Go 服务的 Game WSS。
- Go 服务负责房间 1/2/3、题库、manual 收音、计时、判题和 TTS。
- 玩家之间现场直接交流；服务端不转发玩家音频，首版不做声纹和说话人分离。
- 设备网络链路使用 Opus，服务端内部统一 PCM 16 kHz、单声道、16-bit。
- 猜中或超时后朗读固定汤底并进入讨论态；说“游戏继续”才开下一局。
- 当前不做 AI 玩家、真人主持和掉线恢复。

方案与演示：[docs/design/turtle-soup-game-simplified.md](docs/design/turtle-soup-game-simplified.md)。

产品定位与商业设计：[docs/design/turtle-soup-product-positioning.md](docs/design/turtle-soup-product-positioning.md)。

原方案：[docs/design/turtle-soup-game.md](docs/design/turtle-soup-game.md)。

## AI 工程效率

仓库内置可重复使用的研发 skills，减少每次从零排查环境与回归链路：

- `.agents/skills/xiaozhi-yunliao-s3-firmware/`：`yunliao-s3` 构建、烧录、串口日志与真机验收。
- `.agents/skills/go-test-engineering/`：Go 单测/E2E 分层、race 与异步测试约束。
- `.agents/skills/go-provider-debugging/`：ASR/LLM/TTS provider 与完整语音链路定位。
- `.agents/skills/quick-pr/`：限定路径的格式化、静态检查、提交和 PR 流程。

常用回归命令：

```bash
cd server
go run ./cmd/eval -config config.local.json
go run ./cmd/eval -config config.local.json -e2e
go test -race ./...
```
