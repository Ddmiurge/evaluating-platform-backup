package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	LLM      LLMConfig      `mapstructure:"llm"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Auth     AuthConfig     `mapstructure:"auth"`
	Billing  BillingConfig  `mapstructure:"billing"`
	Crypto   CryptoConfig   `mapstructure:"crypto"`
	Maclaw   MaclawConfig   `mapstructure:"maclaw"`
}

type CryptoConfig struct {
	MasterKey string `mapstructure:"master_key"`
	KeyID     string `mapstructure:"key_id"`
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

type LLMConfig struct {
	Provider    string  `mapstructure:"provider"`
	BaseURL     string  `mapstructure:"base_url"`
	APIKey      string  `mapstructure:"api_key"`
	Model       string  `mapstructure:"model"`
	MaxTokens   int     `mapstructure:"max_tokens"`
	Temperature float64 `mapstructure:"temperature"`
}

type DatabaseConfig struct {
	DSN            string `mapstructure:"dsn"`
	MaxConns       int    `mapstructure:"max_conns"`
	MinConns       int    `mapstructure:"min_conns"`
	MigrateOnStart bool   `mapstructure:"migrate_on_start"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type StorageConfig struct {
	Endpoint  string `mapstructure:"endpoint"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	Bucket    string `mapstructure:"bucket"`
	UseSSL    bool   `mapstructure:"use_ssl"`
}

type AuthConfig struct {
	JWTSecret        string `mapstructure:"jwt_secret"`
	TokenExpiryHours int    `mapstructure:"token_expiry_hours"`
}

type BillingConfig struct {
	Mode            string  `mapstructure:"mode"`
	ExpertShareRate float64 `mapstructure:"expert_share_rate"`
}

type MaclawConfig struct {
	BaseURL                  string `mapstructure:"base_url"`
	RuntimeKind              string `mapstructure:"runtime_kind"`
	RuntimeMode              string `mapstructure:"runtime_mode"`
	CapabilityProfile        string `mapstructure:"capability_profile"`
	AdminSecret              string `mapstructure:"admin_secret"`
	ProvisioningEnabled      bool   `mapstructure:"provisioning_enabled"`
	TimeoutSeconds           int    `mapstructure:"timeout_seconds"`
	RedteamMCPEndpoint       string `mapstructure:"redteam_mcp_endpoint"`
	RedteamMCPSecret         string `mapstructure:"redteam_mcp_secret"`
	RedteamTargetConcurrency int    `mapstructure:"redteam_target_concurrency"`
	EngineBaseURL            string `mapstructure:"engine_base_url"`
	EngineBearerToken        string `mapstructure:"engine_bearer_token"`
	EngineTimeoutSeconds     int    `mapstructure:"engine_timeout_seconds"`
}

func Load(path string) (*Config, error) {
	v := viper.New()
	setDefaults(v)

	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	_ = v.BindEnv("server.port", "SERVER_PORT")
	_ = v.BindEnv("database.dsn", "DATABASE_DSN")
	_ = v.BindEnv("redis.addr", "REDIS_ADDR")
	_ = v.BindEnv("storage.endpoint", "STORAGE_ENDPOINT")
	_ = v.BindEnv("llm.base_url", "LLM_BASE_URL")
	_ = v.BindEnv("llm.api_key", "LLM_API_KEY")
	_ = v.BindEnv("llm.model", "LLM_MODEL")
	_ = v.BindEnv("auth.jwt_secret", "JWT_SECRET")
	_ = v.BindEnv("storage.access_key", "MINIO_ACCESS_KEY")
	_ = v.BindEnv("storage.secret_key", "MINIO_SECRET_KEY")
	_ = v.BindEnv("crypto.master_key", "CRYPTO_MASTER_KEY")
	_ = v.BindEnv("maclaw.base_url", "MACLAW_BASE_URL")
	_ = v.BindEnv("maclaw.runtime_kind", "MACLAW_RUNTIME_KIND")
	_ = v.BindEnv("maclaw.runtime_mode", "MACLAW_RUNTIME_MODE")
	_ = v.BindEnv("maclaw.capability_profile", "MACLAW_CAPABILITY_PROFILE")
	_ = v.BindEnv("maclaw.admin_secret", "MACLAW_ADMIN_SECRET")
	_ = v.BindEnv("maclaw.provisioning_enabled", "MACLAW_PROVISIONING_ENABLED")
	_ = v.BindEnv("maclaw.timeout_seconds", "MACLAW_TIMEOUT_SECONDS")
	_ = v.BindEnv("maclaw.redteam_mcp_endpoint", "MACLAW_REDTEAM_MCP_ENDPOINT")
	_ = v.BindEnv("maclaw.redteam_mcp_secret", "MACLAW_REDTEAM_MCP_SECRET")
	_ = v.BindEnv("maclaw.redteam_target_concurrency", "MACLAW_REDTEAM_TARGET_CONCURRENCY")
	_ = v.BindEnv("maclaw.engine_base_url", "PROMPTFOO_ENGINE_BASE_URL")
	_ = v.BindEnv("maclaw.engine_bearer_token", "PROMPTFOO_ENGINE_BEARER_TOKEN")
	_ = v.BindEnv("maclaw.engine_timeout_seconds", "PROMPTFOO_ENGINE_TIMEOUT_SECONDS")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.mode", "debug")
	v.SetDefault("llm.provider", "openai")
	v.SetDefault("llm.base_url", "https://api.openai.com/v1")
	v.SetDefault("llm.model", "gpt-4o")
	v.SetDefault("llm.max_tokens", 4096)
	v.SetDefault("llm.temperature", 0.7)
	v.SetDefault("database.max_conns", 20)
	v.SetDefault("database.min_conns", 2)
	v.SetDefault("database.migrate_on_start", true)
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.db", 0)
	v.SetDefault("auth.token_expiry_hours", 24)
	v.SetDefault("billing.mode", "prepaid")
	v.SetDefault("billing.expert_share_rate", 0.3)
	v.SetDefault("crypto.master_key", "")
	v.SetDefault("crypto.key_id", "v1")
	v.SetDefault("maclaw.base_url", "http://127.0.0.1:18080")
	v.SetDefault("maclaw.runtime_kind", "maclawsrv")
	v.SetDefault("maclaw.runtime_mode", "compose")
	v.SetDefault("maclaw.capability_profile", "shadow")
	v.SetDefault("maclaw.admin_secret", "")
	v.SetDefault("maclaw.provisioning_enabled", true)
	v.SetDefault("maclaw.timeout_seconds", 360)
	v.SetDefault("maclaw.redteam_mcp_endpoint", "")
	v.SetDefault("maclaw.redteam_mcp_secret", "")
	v.SetDefault("maclaw.redteam_target_concurrency", 5)
	v.SetDefault("maclaw.engine_base_url", "")
	v.SetDefault("maclaw.engine_bearer_token", "")
	v.SetDefault("maclaw.engine_timeout_seconds", 15)
}