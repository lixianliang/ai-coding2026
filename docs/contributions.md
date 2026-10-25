# 项目贡献记录

以下内容按 GitHub Issue 和已合并 PR 的作者、标题及改动范围整理，用于说明项目分工。它不代表工时统计，也不替代各 PR 中的代码审查记录。

## Issue 设计输入

| 作者 | 设计输入 | 记录 |
| --- | --- | --- |
| `cffa-as` | 海龟汤 MVP、门店场景、AI 主持、设备和 Go 服务目标 | [Issue #1](https://github.com/lixianliang/ai-coding2026/issues/1) |
| `lixianliang` | 早期多设备、自动收音、发言权和房间音频方案 | [Issue #2](https://github.com/lixianliang/ai-coding2026/issues/2) |

Issue #2 中的自动收音和多设备方案属于早期设计输入；当前实现以仓库最新产品、架构和部署文档为准。

## PR 交付记录

| 作者 | 交付内容 | PR |
| --- | --- | --- |
| `undauanted` | `self.game.start`、`StartGame` 入口、P4 manual 按键、TTS 发送节奏、积分与结算页 | [#3](https://github.com/lixianliang/ai-coding2026/pull/3)、[#10](https://github.com/lixianliang/ai-coding2026/pull/10)、[#12](https://github.com/lixianliang/ai-coding2026/pull/12) |
| `lixianliang` | Linx/XiaoZhi 架构说明、manual 交互文档、项目文档重组 | [#8](https://github.com/lixianliang/ai-coding2026/pull/8)、[#9](https://github.com/lixianliang/ai-coding2026/pull/9)、[#19](https://github.com/lixianliang/ai-coding2026/pull/19) |
| `cffa-as` | Go 单设备游戏服务、设备游戏链路、产品定位、题库和大厅、流式 TTS、显示和汤面重播、匿名说话人、音频/ASR 修复、质量评估与研发 skills | [#4](https://github.com/lixianliang/ai-coding2026/pull/4)、[#5](https://github.com/lixianliang/ai-coding2026/pull/5)、[#6](https://github.com/lixianliang/ai-coding2026/pull/6)、[#7](https://github.com/lixianliang/ai-coding2026/pull/7)、[#11](https://github.com/lixianliang/ai-coding2026/pull/11)、[#13](https://github.com/lixianliang/ai-coding2026/pull/13)、[#14](https://github.com/lixianliang/ai-coding2026/pull/14)、[#15](https://github.com/lixianliang/ai-coding2026/pull/15)、[#16](https://github.com/lixianliang/ai-coding2026/pull/16)、[#17](https://github.com/lixianliang/ai-coding2026/pull/17) |

## 当前交付物

- 源码：`server/`、`xiaozhi-esp32/`。
- 架构、产品和部署说明：见 [文档索引](README.md)，由最新项目文档统一维护。
- Demo：[`turtle-soup-demo.mp4`](demo/turtle-soup-demo.mp4)，由本项目实际设备录制，约 99 秒、1280×720、H.264/AAC。
