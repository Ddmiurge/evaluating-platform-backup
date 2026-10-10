// Package redteam 是红队评测桥的**无状态纯逻辑**包。
//
// 依赖方向：本包只被 internal/maclaw（父包）调用，父包依赖本包 —— 单向，无循环。
// 【重要】本包【不】包含 ToolBridge 类型。原因是 Go 要求一个类型的方法必须与
// 类型定义同包，而 ToolBridge 有 34 个方法在父包（含 promptfoo_engine_bridge.go
// 的 8 个引擎方法），若 ToolBridge 移入本包必然形成 redteam <-> maclaw 循环依赖。
// 类型别名无法打破该循环。
//
// 因此本包只承载【无状态纯函数】：判定档位表与推断、payload 组合策略、
// 脱敏与摘要工具、批量结果统计、handle 加盐哈希、图片归一化。
// ToolBridge 本体与其方法、Input/Output DTO 留在父包。
//
// 划分理由：redteam 原为 maclaw 包内 3042 行的单文件，与网关传输层
// （client.go）、引擎桥等无关关注点混平，使红队链路的改动面无法按文件隔离。
package redteam
