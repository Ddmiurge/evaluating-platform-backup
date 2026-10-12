package config

// secrets_test.go — 生产密钥启动断言的护栏（审计 2026-10-12 P1-5）。
//
// 断言的判据不止「非空」：configs/config.yaml 里写的是
// `jwt_secret: "${JWT_SECRET}"`，而本仓库**没有** os.ExpandEnv ——
// viper 不会展开 ${...}。所以环境变量缺失时，这个字面量会原样成为
// HMAC 签名密钥。若断言只查非空，一个公开在仓库里的字符串就能当密钥
// 上线且不触发任何告警。
//
// 这也是为什么本文件里`${...}` 形态必须被显式列为拒绝对象。

import (
	"strings"
	"testing"
)

// releaseCfg 构造一个 release 模式、其余字段齐全的配置。
func releaseCfg(jwtSecret, masterKey string) *Config {
	return &Config{
		Server: ServerConfig{Mode: "release", Port: 8080},
		Auth:   AuthConfig{JWTSecret: jwtSecret},
		Crypto: CryptoConfig{MasterKey: masterKey, KeyID: "v1"},
	}
}

const (
	goodJWTSecret = "3f8a1c9e2b7d4056a8c1e9f2b3d4a5c6e7f8091a2b3c4d5e6f708192a3b4c5d6e"
	goodMasterKey = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
)

// TestValidateProductionSecretsRejectsWeakValues 覆盖 release 模式下的
// fail-closed 判据。
//
// 每一条都是「看起来配了、实际等于没配」的真实形态：占位符未展开、
// .env.example 的 change-me 家族、过短的密钥、长度不对的 master_key。
func TestValidateProductionSecretsRejectsWeakValues(t *testing.T) {
	cases := []struct {
		name       string
		jwtSecret  string
		masterKey  string
		wantSubstr string
	}{
		{
			name: "unexpanded placeholder from config.yaml",
			// configs/config.yaml 里的字面量，viper 不展开它。
			jwtSecret:  "${JWT_SECRET}",
			masterKey:  goodMasterKey,
			wantSubstr: "jwt_secret",
		},
		{
			name:       "unexpanded master key placeholder",
			jwtSecret:  goodJWTSecret,
			masterKey:  "${CRYPTO_MASTER_KEY}",
			wantSubstr: "master_key",
		},
		{
			name:       "env.example change-me jwt",
			jwtSecret:  "change-me-to-a-long-random-string",
			masterKey:  goodMasterKey,
			wantSubstr: "jwt_secret",
		},
		{
			name:       "empty jwt secret",
			jwtSecret:  "",
			masterKey:  goodMasterKey,
			wantSubstr: "jwt_secret",
		},
		{
			name:       "whitespace-only jwt secret",
			jwtSecret:  "   ",
			masterKey:  goodMasterKey,
			wantSubstr: "jwt_secret",
		},
		{
			name:       "jwt secret too short",
			jwtSecret:  "short-but-not-a-placeholder",
			masterKey:  goodMasterKey,
			wantSubstr: "too short",
		},
		{
			name:       "master key wrong length",
			jwtSecret:  goodJWTSecret,
			masterKey:  "abcd",
			wantSubstr: "master_key",
		},
		{
			name:       "both weak",
			jwtSecret:  "${JWT_SECRET}",
			masterKey:  "${CRYPTO_MASTER_KEY}",
			wantSubstr: "jwt_secret",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := releaseCfg(c.jwtSecret, c.masterKey).ValidateProductionSecrets()
			if err == nil {
				t.Fatalf("ValidateProductionSecrets() = nil, want error (jwt=%q key=%q)", c.jwtSecret, c.masterKey)
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Fatalf("error %q must mention %q", err.Error(), c.wantSubstr)
			}
			// 错误必须点明这是生产模式的校验，避免运维误以为是别的问题。
			if !strings.Contains(err.Error(), "release") {
				t.Fatalf("error %q must state the production mode context", err.Error())
			}
		})
	}
}

// TestValidateProductionSecretsReportsAllProblems 覆盖「一次性报全部问题」。
//
// 运维修一个重启一次是糟糕的体验，也容易在赶工时漏掉另一个。
func TestValidateProductionSecretsReportsAllProblems(t *testing.T) {
	err := releaseCfg("", "").ValidateProductionSecrets()
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "jwt_secret") || !strings.Contains(msg, "master_key") {
		t.Fatalf("error must mention both secrets, got %q", msg)
	}
}

// TestValidateProductionSecretsAcceptsStrongValues 覆盖放行路径。
//
// 没有这条，断言可能退化成「永远拒绝」—— 那会让 release 部署根本起不来，
// 运维便会把它注释掉，护栏名存实亡。
func TestValidateProductionSecretsAcceptsStrongValues(t *testing.T) {
	if err := releaseCfg(goodJWTSecret, goodMasterKey).ValidateProductionSecrets(); err != nil {
		t.Fatalf("strong secrets must be accepted, got %v", err)
	}
}

// TestValidateProductionSecretsIgnoresNonReleaseModes 覆盖开发/测试不受影响。
//
// 这是本次改动的关键约束：本机没有 JWT_SECRET / CRYPTO_MASTER_KEY 也要能
// 跑测试与本地调试。若断言在 debug 模式下也 Fatal，所有开发流程与 CI 单测
// 都会被挡住。
func TestValidateProductionSecretsIgnoresNonReleaseModes(t *testing.T) {
	for _, mode := range []string{"", "debug", "test", "development", "Release", "RELEASE "} {
		cfg := releaseCfg("", "")
		cfg.Server.Mode = mode
		if err := cfg.ValidateProductionSecrets(); err != nil {
			t.Fatalf("mode=%q must not require production secrets, got %v", mode, err)
		}
	}
}

// TestValidateProductionSecretsNilConfig 覆盖 nil 防御。
func TestValidateProductionSecretsNilConfig(t *testing.T) {
	var cfg *Config
	if err := cfg.ValidateProductionSecrets(); err == nil {
		t.Fatal("nil config must return an error, not panic")
	}
}

// TestPlaceholderSecret 覆盖占位符识别的边界。
func TestPlaceholderSecret(t *testing.T) {
	placeholders := []string{
		"",
		"   ",
		"${JWT_SECRET}",
		"${CRYPTO_MASTER_KEY}",
		"${anything}",
		"change-me-to-a-long-random-string",
		"CHANGE-ME",
		"changeme",
		"change_me",
		"replace-me",
		"your-secret-here",
		"xxxxxxxxxxxxxxxx",
	}
	for _, v := range placeholders {
		if !placeholderSecret(v) {
			t.Fatalf("placeholderSecret(%q) = false, want true", v)
		}
	}

	realSecrets := []string{
		goodJWTSecret,
		goodMasterKey,
		"a-perfectly-fine-random-secret",
		// 含 "xxx" 但不是占位形态的合法值不应被误杀。
		"xxx-generated-by-keygen-9f8a7b6c5d4e3f2a1b",
	}
	for _, v := range realSecrets {
		if placeholderSecret(v) {
			t.Fatalf("placeholderSecret(%q) = true, want false", v)
		}
	}
}

// TestValidateJWTSecretLengthBoundary 覆盖长度下限的精确边界。
func TestValidateJWTSecretLengthBoundary(t *testing.T) {
	atLimit := strings.Repeat("a", MinJWTSecretLength)
	if err := validateJWTSecret(atLimit); err != nil {
		t.Fatalf("exactly %d chars must be accepted, got %v", MinJWTSecretLength, err)
	}
	belowLimit := strings.Repeat("a", MinJWTSecretLength-1)
	if err := validateJWTSecret(belowLimit); err == nil {
		t.Fatalf("%d chars must be rejected", MinJWTSecretLength-1)
	}
}

// TestValidateMasterKeyLengthBoundary 覆盖 master_key 必须恰好 64 hex。
//
// 32 字节 hex = 64 字符。放宽到「至少 64」会让一个 128 字符的错值通过
// 启动断言、却在第一次加密时失败 —— 那时已经有数据落库。
func TestValidateMasterKeyLengthBoundary(t *testing.T) {
	if err := validateMasterKey(strings.Repeat("ab", 32)); err != nil {
		t.Fatalf("64 hex chars must be accepted, got %v", err)
	}
	for _, bad := range []string{
		strings.Repeat("ab", 31), // 62 chars
		strings.Repeat("ab", 33), // 66 chars
		strings.Repeat("zz", 32), // right length, but not hex
	} {
		if err := validateMasterKey(bad); err == nil {
			t.Fatalf("validateMasterKey(%d chars) must fail", len(bad))
		}
	}
}
