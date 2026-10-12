package config

// secrets.go — 生产模式密钥启动断言（审计 2026-10-12 P1-5）。
//
// # 要修的问题
//
// cmd/server/main.go 原先对 crypto.master_key 只做 Warn：
//
//	if strings.TrimSpace(cfg.Crypto.MasterKey) == "" {
//	    logger.Warn("crypto.master_key not set; maclaw provisioning is unavailable")
//	}
//
// 结果是服务照常启动，只是「一个功能不可用」。但密钥缺失不是功能降级，
// 是**安全姿态降级**：一个本应加密存储凭据的部署，在没有密钥的情况下跑
// 起来了，而且没有任何东西阻止它接收真实流量。
//
// # 为什么断言只在 release 模式
//
// 本机没有 JWT_SECRET / CRYPTO_MASTER_KEY 也能跑测试与本地调试；
// 若无条件 Fatal，所有开发流程与 CI 单测都会被这道断言挡住。判据因此
// 取 cfg.Server.Mode == "release" —— 这是 main.go 里已经在用来切gin
// ReleaseMode 的同一个值（main.go:38），沿用它可以避免引入第二套
// 「什么算生产」的判定，那才是真正容易出事的分叉。
//
// # 判据
//
// 非空是底线，但**不够**。.env.example 里写着
// `JWT_SECRET=change-me-to-a-long-random-string`，而 configs/config.yaml
// 里是 `jwt_secret: "${JWT_SECRET}"` —— viper 不会展开 ${...}（本仓库
// 没有 os.ExpandEnv），所以环境变量缺失时该字面量会原样成为签名密钥。
// 只查非空，等于放任一个公开在仓库里的字符串当HMAC 密钥。
//
// 因此判据是「非空 + 非占位符 + 长度下限」：
//   - 非空：拒绝 "" 与纯空白。
//   - 非占位符：拒绝 ${VAR} 形式与 .env.example 里的 change-me-* 家族。
//     这些值一旦流入生产，等于没有密钥而且更难发现（因为「有值」）。
//   - 长度下限：HS256 的密钥强度依赖长度。32 字符是业界对 HMAC-SHA256
//     密钥的通用下限建议，取它作为闸门既拦得住弱口令，又不会误伤
//     正常生成的长随机串。
//
// master_key 额外要求能被解析成 32 字节（AES-256），因为一个长度不对的
// master_key 到了加密那一步才失败，那时已经有数据写进去了。
//
// # 为什么不写「默认值黑名单」
//
// 黑名单只能拦住已知的坏值。本函数改为「白名单式」判据（必须不是占位符
// 形态、必须够长），新增一种占位写法不需要改这里。

import (
	"fmt"
	"strings"
)

// MinJWTSecretLength 是 release 模式下 auth.jwt_secret 的最小长度（字符）。
//
// 取 32 的依据：HMAC-SHA256 的安全性依赖于密钥长度，低于 256 bit 的密钥
// 可被暴力枚举。openssl rand -base64 48 之类的标准做法天然产出 64 字符，
// 不会被这道闸门误伤；而 "secret" / "changeme" 这类会被挡住。
const MinJWTSecretLength = 32

// releaseMode 是 main.go 判定生产的同一个字面量（见 main.go:38）。
const releaseMode = "release"

// ValidateProductionSecrets 在 release 模式下校验必需密钥，不满足即返回错误。
//
// 调用点必须把错误当作致命错误处理（log.Fatalf 拒绝启动），因为「带着
// 弱密钥/占位密钥运行的生产服务」比「起不来」危险得多。
//
// 非 release 模式一律返回 nil：本机开发与 CI 单测没有这些环境变量。
func (c *Config) ValidateProductionSecrets() error {
	if c == nil {
		return fmt.Errorf("validate production secrets: nil config")
	}
	if strings.TrimSpace(c.Server.Mode) != releaseMode {
		return nil
	}
	var problems []string
	if err := validateJWTSecret(c.Auth.JWTSecret); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateMasterKey(c.Crypto.MasterKey); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) == 0 {
		return nil
	}
	// 一次性报全部问题：运维不必修一个重启一次。
	return fmt.Errorf("production secret validation failed (server.mode=%s): %s",
		releaseMode, strings.Join(problems, "; "))
}

// placeholderSecret 报告value 是否属于「看起来有值、实际等于没有」的占位符。
//
// 覆盖三类：
//   - ${VAR} —— configs/config.yaml 用的就是这一形式；viper 不展开它，
//     环境变量缺失时字面量会直接成为密钥。
//   - change-me / changeme / change_me / replace-me / your-secret ——
//     .env.example 的写法，是最容易被原样带进生产的「看起来配好了」。
//   - 纯 x 串（sk-xxxxxxxxxxxxxxxxxxxx）—— 打码后的示例值。
//     注意只匹配「整串都是 x」，而不是「包含 xxx」：后者会误杀
//     "xxx-generated-by-keygen-9f8a..." 这类合法密钥，误杀同样危险 ——
//     运维的解法会是把断言注释掉。
func placeholderSecret(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") {
		return true
	}
	lowered := strings.ToLower(trimmed)
	for _, marker := range []string{"change-me", "changeme", "change_me", "replace-me", "replaceme", "your-secret"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	// 整串都是 x（可含分隔符，如 xxxx-xxxx）。
	var sawX bool
	for _, r := range lowered {
		switch {
		case r == 'x':
			sawX = true
		case r == '-' || r == '_' || r == ' ':
			// 分隔符跳过。
		default:
			return false
		}
	}
	return sawX
}

// validateJWTSecret 校验 JWT 签名密钥。
func validateJWTSecret(secret string) error {
	if placeholderSecret(secret) {
		return fmt.Errorf("auth.jwt_secret is empty or still a placeholder (set JWT_SECRET)")
	}
	if n := len(strings.TrimSpace(secret)); n < MinJWTSecretLength {
		return fmt.Errorf("auth.jwt_secret is too short: %d chars, need >= %d", n, MinJWTSecretLength)
	}
	return nil
}

// validateMasterKey 校验 AES-256 主密钥。
//
// 除占位符外，还要求**恰为 64 个 hex 字符**：既锁长度（32 字节 = AES-256），
// 也锁字符集（纯 hex）。长度不对的密钥若拖到第一次加密才失败，那时已经有
// 数据写进了库；而非 hex 的同长度串会拖到 crypto.ParseHexKey 才失败 ——
// 那时服务已经启动、已经开始接收流量了。启动期拦住它才是 fail-closed。
func validateMasterKey(masterKey string) error {
	if placeholderSecret(masterKey) {
		return fmt.Errorf("crypto.master_key is empty or still a placeholder (set CRYPTO_MASTER_KEY)")
	}
	trimmed := strings.TrimSpace(masterKey)
	// 32 字节 hex 编码 = 64 个 hex 字符。
	if n := len(trimmed); n != 64 {
		return fmt.Errorf("crypto.master_key must be 64 hex chars (32 bytes for AES-256), got %d", n)
	}
	for _, r := range trimmed {
		isHexDigit := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHexDigit {
			return fmt.Errorf("crypto.master_key must be hex-encoded; found %q", string(r))
		}
	}
	return nil
}
