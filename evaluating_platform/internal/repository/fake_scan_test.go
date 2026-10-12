package repository

// fake_scan_test.go — 测试用的反射赋值与 fake 基础设施（审计 2026-10-12 P1-4）。
//
// # 为什么用 reflect 而不是手写 switch
//
// engine_runs 有 27 列、maclaw_resources 有 19 列、templates 有 13 列。
// 手写 switch 会有三个问题：
//  1. 增删列时测试会**静默错位**（值顺序对不上但仍能Scan 成功），
//     变成一个永远不会失败的假护栏；
//  2. 每个 fakeRow 要维护几十行样板；
//  3. 类型断言失败时的报错信息不指向具体列。
//
// reflect 版本在数量不匹配时立即报错，且按目标指针的实际类型赋值 ——
// 列顺序写错会立刻暴露，而不是悄悄产出错数据。

import (
	"fmt"
	"reflect"
)

// assignValue 把 src 写入 dest 指向的值。
//
// 支持 scan 目标里实际出现的类型：string、[]byte、int、int64、bool、
// time.Time、*time.Time、uuid.UUID（底层 [16]byte）以及它们的命名类型
// （如 maclaw.EngineRunPhase）。命名类型通过 ConvertibleTo 覆盖，
// 这样测试不必为每个领域类型写特例。
func assignValue(dest, src any) error {
	if dest == nil {
		return fmt.Errorf("assignValue: nil destination")
	}
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Pointer {
		return fmt.Errorf("assignValue: destination must be a pointer, got %T", dest)
	}
	if dv.IsNil() {
		return fmt.Errorf("assignValue: nil destination pointer %T", dest)
	}
	elem := dv.Elem()

	// src 为 nil 时写入目标类型的零值（对应可空列）。
	if src == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}

	sv := reflect.ValueOf(src)
	if sv.Type().AssignableTo(elem.Type()) {
		elem.Set(sv)
		return nil
	}
	// 命名类型（如 EngineRunPhase ← string）走可转换路径。
	if sv.Type().ConvertibleTo(elem.Type()) {
		elem.Set(sv.Convert(elem.Type()))
		return nil
	}
	return fmt.Errorf("assignValue: cannot assign %T to %T", src, dest)
}
