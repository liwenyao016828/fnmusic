// Package assistant 曲率的本地 AI 助手运行时。
//
// 架构参照 msf（github.com/liwenyao016828/msf）的 assistant 包移植而来，
// 并针对曲率的领域做了裁剪：
//
// ┌─────────────────────────────────────────────────────────────────┐
// │ 用户在网页助手抽屉里提问                                          │
// │        │                                                        │
// │        ▼                                                        │
// │ POST /api/assistant/chat ─────────► runtime.go（工具调用循环）   │
// │        │                             │                          │
// │        │ SSE                        │ ① 模型决定调哪个能力       │
// │        ▼                           ▼                          │
// │ 前端抽屉渲染 ◄── ② policy.Decide 策略裁决（只读放行/写入待确认）│
// │                     ③ executor 进程内直调 routeTable handler     │
// │                     ④ 结果回填 messages，循环到最终文本          │
// └─────────────────────────────────────────────────────────────────┘
//
// 能力目录在 catalog.go 里登记（映射曲率真实路由），提示词在 prompt 包。
package assistant
