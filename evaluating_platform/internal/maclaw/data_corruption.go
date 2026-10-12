package maclaw

// data_corruption.go — 跨层契约：部分行因JSONB 损坏被跳过（审计 2026-10-12 P1-1/2/3/6）。
//
// # 为什么要放在这一层
//
// repository 层定义了具体的 PartialCorruptionError 类型，但 handler 层
// 不能直接 import repository 之外的判定入口 —— 更重要的是 **maclaw 不能
// import repository**：repository 已经 import maclaw，反向依赖会形成
// 循环。所以这里用 Go 的结构化接口声明契约：
//
//	repository.PartialCorruptionError  ──实现──▶  maclaw.PartialCorruption
//
// 具体类型在 repository，判定逻辑在 maclaw，两边都不需要认识对方的包。
//
// # 语义
//
// 携带本错误时，**同一次调用返回的 items 仍然有效**（好行），
// 只是有 N 行因为 DB 里已有的脏数据被跳过。调用方应当：
//   - 记日志 / 打点（让降级可观测，不要静默）
//   - 仍然把已取到的数据返回给用户，而不是整个请求 500
//
// 理由：让一条历史脏数据把整个列表页变成 500，代价（可用性）远大于
// 少显示一行；反过来，静默跳过又正是本次审计要消灭的「错误被吞掉」。
// 这个类型让两件事同时成立。

import "errors"

// PartialCorruption 是「部分行被跳过」的跨层标记接口。
//
// 实现方必须有无参方法 IsPartialCorruption()，这样只有**明确声明过**
// 「我是一次可控降级」的错误才会被识别 —— 普通的 error 永远不会误判为
// 降级，从而避免把真正的故障当降级吞掉。
type PartialCorruption interface {
	error
	// IsPartialCorruption 是标记方法，无实现逻辑。
	IsPartialCorruption()
}

// IsPartialCorruption 报告 err 是否（或是否包裹了）一次可控的降级。
//
// 使用方约定：
//
//	items, err := svc.List(...)
//	if err != nil && !maclaw.IsPartialCorruption(err) {
//	    return nil, err // 真故障
//	}
//	// 降级：items 仍可用
func IsPartialCorruption(err error) bool {
	if err == nil {
		return false
	}
	var partial PartialCorruption
	return errors.As(err, &partial)
}
