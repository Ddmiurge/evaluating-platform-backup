package maclaw

// data_corruption_test.go — 降级契约的护栏（审计 2026-10-12 P1-1/2/3/6）。
//
// # 为什么这个判定必须有测试
//
// IsPartialCorruption 是「一条脏数据不把整个列表打成 500」这条策略的
// **唯一授权点**。它一旦失准，两个方向都会出事：
//
//	假阴性：真故障被当成降级吞掉 → 数据库断了，日志里一切正常，
//	        用户看到的是「列表少了点东西」，排查方向从一开始就跑偏。
//	假阳性：普通 error 被当成降级 → 真实的 JSON 解析错误被降级处理，
//	        我们刚修好的 fail-closed 又被绕回去了。
//
// 两条断言都必须存在，缺一条护栏就只剩半边。

import (
	"errors"
	"fmt"
	"testing"
)

// stubPartialCorruption 是满足 PartialCorruption 接口的最小实现。
type stubPartialCorruption struct{ msg string }

func (s stubPartialCorruption) Error() string        { return s.msg }
func (s stubPartialCorruption) IsPartialCorruption() {}

// wrappedPartial 演示降级错误被包在fmt.Errorf 里时的识别能力。
type wrappedPartial struct{ inner error }

func (w wrappedPartial) Error() string { return "wrapped: " + w.inner.Error() }
func (w wrappedPartial) Unwrap() error { return w.inner }

// TestIsPartialCorruptionAcceptsMarkedErrors 覆盖正向识别。
func TestIsPartialCorruptionAcceptsMarkedErrors(t *testing.T) {
	partial := stubPartialCorruption{msg: "1 row skipped"}

	if !IsPartialCorruption(partial) {
		t.Fatal("a marked error must be recognised as partial corruption")
	}
	// 指针形态同样要认。
	if !IsPartialCorruption(&partial) {
		t.Fatal("a marked pointer error must be recognised")
	}
	// 被包装过也要认 —— service 层普遍用 fmt.Errorf 包装错误。
	wrapped := fmt.Errorf("list resources: %w", partial)
	if !IsPartialCorruption(wrapped) {
		t.Fatal("a wrapped marked error must be recognised")
	}
	// 自定义 Unwrap 链也要认。
	if !IsPartialCorruption(wrappedPartial{inner: partial}) {
		t.Fatal("a marked error behind a custom Unwrap must be recognised")
	}
}

// unmarkedCorruption 演示「长得像降级、但没有标记方法」的错误。
//
// 这是最容易误判的形态：Error() 文本里同样写着 "corruption"/"skipped"，
// 但它没有IsPartialCorruption() 标记，因此**不能**被放行降级 —— 否则
// 任何第三方错误只要文案相似就能骗过授权点。
type unmarkedCorruption struct{ msg string }

func (u unmarkedCorruption) Error() string { return u.msg }

// TestIsPartialCorruptionRejectsEverythingElse 是更重要的一条。
//
// 覆盖「不该降级的一律不降级」：nil、普通 error、以及看起来很像的
// 未标记错误都不能被放行。
func TestIsPartialCorruptionRejectsEverythingElse(t *testing.T) {
	cases := map[string]error{
		"nil":                nil,
		"plain error":        errors.New("connection refused"),
		"wrapped plain":      fmt.Errorf("query: %w", errors.New("boom")),
		"error without mark": unmarkedCorruption{msg: "corrupt row skipped, looks like degradation"},
	}
	for name, err := range cases {
		if IsPartialCorruption(err) {
			t.Fatalf("IsPartialCorruption(%s) = true, want false", name)
		}
	}
}

// TestIsPartialCorruptionIsNotConfusedByUnrelatedWrapper 覆盖混合包装：
// 外层是普通 error、内层是降级 —— 仍应识别为降级（errors.As 会穿透）。
//
// 这条与上一条方向相反但同样必要：包装顺序在真实调用链里不受控，
// 判定不能依赖「降级一定在最外层」。
func TestIsPartialCorruptionIsNotConfusedByUnrelatedWrapper(t *testing.T) {
	inner := stubPartialCorruption{msg: "skipped"}
	outer := fmt.Errorf("service layer: %w", fmt.Errorf("repo layer: %w", inner))
	if !IsPartialCorruption(outer) {
		t.Fatal("deeply wrapped partial corruption must still be recognised")
	}
}
