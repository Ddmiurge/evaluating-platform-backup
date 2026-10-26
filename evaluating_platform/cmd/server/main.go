package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/api/handler"
	"evaluating_platform/internal/api/middleware"
	"evaluating_platform/internal/billing"
	"evaluating_platform/internal/crypto"
	maclawpkg "evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/repository"
	"evaluating_platform/internal/sample"
	"evaluating_platform/pkg/config"
	"evaluating_platform/pkg/db"
	"evaluating_platform/pkg/logger"
	"evaluating_platform/pkg/storage"
)

func main() {
	configPath := "configs/config.yaml"
	if _, err := os.Stat("configs/config.local.yaml"); err == nil {
		configPath = "configs/config.local.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("[FATAL] load config: %v", err)
	}

	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
		logger.SetLevel(logger.INFO)
	}

	// P1-5（审计 2026-10-12）：release 模式下强制校验 JWT_SECRET /
	// CRYPTO_MASTER_KEY，不满足即拒绝启动（fail-closed）。
	//
	// 放在 logger 初始化之后、任何 DB / Redis / 存储连接之前：密钥不达标时
	// 不应该先把外部依赖拉起来再失败，那会在日志里留下「像是启动成功」的
	// 假象。原先这里对 master_key 只 Warn，服务照常启动，部署者不会知道自己
	// 在无密钥状态下运行 —— 见 pkg/config/secrets.go 的判据说明。
	if err := cfg.ValidateProductionSecrets(); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}

	logger.Info("config loaded", map[string]interface{}{
		"mode":  cfg.Server.Mode,
		"port":  cfg.Server.Port,
		"model": cfg.LLM.Model,
	})

	ctx := context.Background()
	pgPool, err := db.NewPostgresPool(ctx, cfg.Database.DSN, cfg.Database.MaxConns, cfg.Database.MinConns)
	if err != nil {
		log.Fatalf("[FATAL] connect postgres: %v", err)
	}
	defer pgPool.Close()
	logger.Info("postgres connected")

	if cfg.Database.MigrateOnStart {
		runMigrations(ctx, pgPool)
	}

	rdb, err := db.NewRedisClient(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		logger.Warn("redis unavailable, continuing without it", map[string]interface{}{"err": err.Error()})
	} else {
		defer rdb.Close()
		logger.Info("redis connected")
	}

	var minioClient *storage.MinIOClient
	if cfg.Storage.Endpoint != "" {
		mc, err := newMinIOClientWithRetry(cfg.Storage, 30, 2*time.Second)
		if err != nil {
			logger.Warn("MinIO unavailable, upload features disabled", map[string]interface{}{"err": err.Error()})
		} else {
			minioClient = mc
			logger.Info("MinIO connected", map[string]interface{}{"endpoint": cfg.Storage.Endpoint})
		}
	}

	userRepo := repository.NewUserRepository(pgPool)
	billingRepo := repository.NewBillingRepository(pgPool)
	sampleRepo := repository.NewAttackSampleRepository(pgPool)
	composedAttackRepo := repository.NewComposedAttackRepository(pgPool)
	tplRepo := repository.NewTemplateRepository(pgPool)
	maclawAccountMappingRepo := repository.NewMaclawAccountMappingRepository(pgPool)
	maclawResourcePublicationRepo := repository.NewMaclawResourcePublicationRepository(pgPool)
	maclawResourceShadowRepo := repository.NewMaclawResourceShadowRepository(pgPool)
	maclawSkillPublicationRepo := repository.NewMaclawSkillPublicationRepository(pgPool)
	maclawSkillShadowRepo := repository.NewMaclawSkillShadowRepository(pgPool)
	maclawModelConfigRepo := repository.NewMaclawModelConfigRepository(pgPool)
	maclawHubConfigRepo := repository.NewMaclawHubConfigRepository(pgPool)
	maclawTargetConfigRepo := repository.NewMaclawTargetConfigRepository(pgPool)
	maclawRedteamArtifactRepo := repository.NewMaclawRedteamArtifactRepository(pgPool)
	maclawResourceRecordRepo := repository.NewMaclawResourceRecordRepository(pgPool)

	var keyStore *crypto.KeyStore
	if strings.TrimSpace(cfg.Crypto.MasterKey) == "" {
		logger.Warn("crypto.master_key not set; maclaw provisioning is unavailable")
	} else {
		ks, err := crypto.NewKeyStore(cfg.Crypto)
		if err != nil {
			log.Fatalf("[FATAL] init crypto keystore: %v", err)
		}
		keyStore = ks
	}
	maclawRuntimeConfigService := maclawpkg.NewRuntimeConfigService(maclawModelConfigRepo, keyStore)
	maclawHubConfigService := maclawpkg.NewMaclawHubConfigService(maclawHubConfigRepo, keyStore)
	maclawTargetConfigService := maclawpkg.NewTargetConfigService(maclawTargetConfigRepo, keyStore)
	maclawRedteamArtifactService := maclawpkg.NewRedteamArtifactService(maclawRedteamArtifactRepo)
	maclawResourceStoreService := maclawpkg.NewResourceStoreService(maclawResourceRecordRepo, keyStore)

	var sampleManager *sample.Manager
	var sampleLoader *sample.Loader
	var composedAttackManager *sample.ComposedAttackManager
	var composedAttackLoader *sample.ComposedAttackLoader
	if minioClient != nil {
		sampleManager = sample.NewManager(sampleRepo, minioClient)
		sampleLoader = sample.NewLoader(sampleRepo, minioClient)
		composedAttackManager = sample.NewComposedAttackManager(composedAttackRepo, minioClient)
		composedAttackLoader = sample.NewComposedAttackLoader(composedAttackRepo, minioClient)
		logger.Info("portal resource upload managers initialized")
	}

	billingService := billing.NewService(userRepo, billingRepo)

	authHandler := handler.NewAuthHandler(cfg.Auth.JWTSecret, userRepo)
	billingHandler := handler.NewBillingHandler(billingService)
	adminHandler := handler.NewAdminHandler(userRepo, billingRepo)
	enterpriseWelcomeHandler := handler.NewEnterpriseWelcomeHandler()
	tplHandler := handler.NewTemplateHandler(tplRepo)
	sampleHandler := handler.NewAttackSampleHandler(sampleRepo, sampleManager, sampleLoader, minioClient)
	composedAttackHandler := handler.NewComposedAttackHandler(composedAttackRepo, composedAttackManager, composedAttackLoader, minioClient)
	maclawProvider, maclawProjection, maclawSkillProjection, maclawCapabilityCatalog := buildMaclawRuntime(
		cfg,
		userRepo,
		maclawAccountMappingRepo,
		maclawResourcePublicationRepo,
		maclawResourceShadowRepo,
		maclawSkillPublicationRepo,
		maclawSkillShadowRepo,
		keyStore,
		maclawRuntimeConfigService,
		maclawHubConfigService,
		maclawTargetConfigService,
		maclawRedteamArtifactService,
		maclawResourceStoreService,
	)
	maclawEvaluationHandler := handler.NewMaclawEvaluationHandlerWithProviderAndProjection(maclawProvider, maclawProjection)
	maclawEvaluationHandler.SetRuntimeMode(cfg.Maclaw.RuntimeMode)
	engineRunRepo := repository.NewEngineRunRepository(pgPool)
	engineRunService := maclawpkg.NewEngineRunService(engineRunRepo)
	engineClient := maclawpkg.NewPromptfooEngineClient(cfg.Maclaw.EngineBaseURL, cfg.Maclaw.EngineBearerToken, cfg.Maclaw.EngineTimeoutSeconds)
	engineRunHandler := handler.NewEngineRunHandler(engineClient, engineRunService, maclawTargetConfigService, maclawRuntimeConfigService)
	maclawSkillHandler := handler.NewMaclawSkillHandlerWithProviderProjectionAndHub(maclawProvider, maclawSkillProjection, maclawHubConfigService)
	maclawMCPHandler := handler.NewMaclawMCPHandler(maclawProvider)
	maclawCapabilityCatalog.SetPlatformDataStores(sampleRepo, tplRepo, composedAttackRepo)
	maclawRuntimeHandler := handler.NewMaclawRuntimeHandlerWithProviderProjectionsAndCatalog(maclawProvider, maclawProjection, maclawSkillProjection, maclawCapabilityCatalog)
	maclawRuntimeHandler.SetExecutionGrantSecret(cfg.Maclaw.RedteamMCPSecret)
	maclawRuntimeHandler.SetTargetConfigService(maclawTargetConfigService)
	redteamToolBridge := maclawpkg.NewRedteamToolBridge(maclawCapabilityCatalog, maclawProjection, maclawSkillProjection)
	redteamToolBridge.SetTargetConfigService(maclawTargetConfigService)
	redteamToolBridge.SetArtifactService(maclawRedteamArtifactService)
	redteamToolBridge.SetLLMAttackJudge(maclawpkg.NewRuntimeConfigLLMAttackJudge(maclawRuntimeConfigService))
	redteamToolBridge.SetTargetConcurrency(cfg.Maclaw.RedteamTargetConcurrency)
	// Phase 1: expose the promptfoo engine to MaClaw through the bridge MCP tools.
	redteamToolBridge.SetPromptfooEngine(engineClient, engineRunService, maclawTargetConfigService, maclawRuntimeConfigService)
	// U10: resolve expert-published YAML plugin packages and apply admin
	// governance (启停/下架/归档 → 引擎立即不可用).
	//
	// Wired here rather than inside SetPromptfooEngine so the two concerns
	// stay separable: a deployment that does not publish custom packages can
	// leave the resolver nil, and every run that names no package behaves
	// exactly as it did before U10.
	//
	// The publication repo filters on `enabled = true AND status =
	// 'published'`, so a package an admin took down resolves to nil and is
	// refused — that is the whole mechanism, and it is re-read per run rather
	// than cached, which is what makes the effect immediate.
	redteamToolBridge.SetCustomPluginResolver(maclawpkg.NewCustomPluginResolver(maclawResourcePublicationRepo, maclawResourceStoreService))
	// The WIZARD path resolves packages through the same function the confirm
	// path uses (2026-10-23). Wired after the resolver so a package is resolved
	// with governance applied on both doors.
	engineRunHandler.SetEngineCapabilityResolver(redteamToolBridge)
	// Phase 1 confirm fast path: shared platform engine job store.
	platformEngineJobs := maclawpkg.NewPlatformEngineJobStore()
	maclawRuntimeHandler.SetPromptfooEngine(redteamToolBridge, platformEngineJobs, engineRunService)
	maclawEvaluationHandler.SetPromptfooEngineJobs(platformEngineJobs, engineRunService)
	if sampleLoader != nil && composedAttackLoader != nil {
		payloadProvider := maclawpkg.NewPlatformRedteamPayloadProvider(sampleLoader, tplRepo, composedAttackLoader)
		payloadProvider.SetPublicationStores(sampleRepo, tplRepo, composedAttackRepo)
		redteamToolBridge.SetPayloadProvider(payloadProvider)
	}
	redteamMCPHandler := handler.NewRedteamMCPHandler(redteamToolBridge, cfg.Maclaw.RedteamMCPSecret)
	adminMaclawHandler := handler.NewAdminMaclawHandler(
		userRepo,
		maclawAccountMappingRepo,
		maclawResourcePublicationRepo,
		maclawResourceShadowRepo,
		maclawResourceRecordRepo,
		maclawProvider,
		maclawRuntimeConfigService,
		maclawHubConfigService,
		cfg.Maclaw.RuntimeMode,
	)

	legacyGone := func(c *gin.Context) {
		c.JSON(http.StatusGone, gin.H{
			"code":  "legacy_api_retired",
			"error": "legacy evaluating_platform execution APIs have been retired; use /api/v1/maclaw/... endpoints",
		})
	}

	r := gin.Default()
	r.Use(func(c *gin.Context) {
		applyCORSHeaders(c)
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	api := r.Group("/api/v1")
	{
		api.POST("/auth/login", authHandler.Login)
		api.POST("/auth/register", authHandler.Register)
		api.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "time": time.Now().Format(time.RFC3339)})
		})
		api.POST("/internal/maclaw/redteam-mcp", redteamMCPHandler.Handle)
		api.GET("/billing/prices", billingHandler.GetToolPrices)
		api.GET("/tools/schemas", legacyGone)
		api.GET("/assessments/:id/stream", legacyGone)
	}

	auth := api.Group("/")
	auth.Use(middleware.JWTAuth(cfg.Auth.JWTSecret))
	// The enterprise chat UI polls jobs/runs/sessions endpoints ~1/s each
	// while an evaluation is active (≈240 req/min per user). 100/min throttled
	// that polling and made the job tracker give up on healthy runs; keep a
	// limit well above the polling load for abuse protection only.
	auth.Use(middleware.RateLimit(600, time.Minute))
	{
		auth.GET("/auth/me", authHandler.Me)

		auth.POST("/assessments", legacyGone)
		auth.GET("/assessments", legacyGone)
		auth.DELETE("/assessments", legacyGone)
		auth.GET("/assessments/:id", legacyGone)
		auth.POST("/assessments/:id/cancel", legacyGone)
		auth.DELETE("/assessments/:id", legacyGone)
		auth.GET("/reports/:id", legacyGone)
		auth.GET("/reports/:id/download", legacyGone)

		auth.GET("/tools", legacyGone)
		auth.GET("/assets", legacyGone)

		auth.GET("/billing/balance", billingHandler.GetBalance)
		auth.GET("/billing/records", billingHandler.GetBillingRecords)
		auth.GET("/billing/transactions", billingHandler.GetTransactions)
		auth.POST("/billing/recharge", billingHandler.Recharge)

		auth.GET("/maclaw/skills", maclawSkillHandler.List)
		auth.POST("/maclaw/skills/search", maclawSkillHandler.Search)

		maclawMCP := auth.Group("/")
		maclawMCP.Use(middleware.RequireRole("expert", "admin"))
		{
			maclawMCP.GET("/maclaw/mcp/servers", maclawMCPHandler.ListServers)
			maclawMCP.POST("/maclaw/mcp/servers", maclawMCPHandler.CreateServer)
			maclawMCP.GET("/maclaw/mcp/servers/:id", maclawMCPHandler.GetServer)
			maclawMCP.PATCH("/maclaw/mcp/servers/:id", maclawMCPHandler.UpdateServer)
			maclawMCP.DELETE("/maclaw/mcp/servers/:id", maclawMCPHandler.DeleteServer)
			maclawMCP.POST("/maclaw/mcp/servers/:id/start", maclawMCPHandler.StartServer)
			maclawMCP.POST("/maclaw/mcp/servers/:id/stop", maclawMCPHandler.StopServer)
			maclawMCP.POST("/maclaw/mcp/servers/:id/health-check", maclawMCPHandler.HealthCheckServer)
			maclawMCP.GET("/maclaw/mcp/servers/:id/tools", maclawMCPHandler.ListServerTools)
		}

		maclawCatalog := auth.Group("/")
		maclawCatalog.Use(middleware.RequireRole("enterprise", "expert", "admin"))
		{
			maclawCatalog.GET("/maclaw/evaluation/resources", maclawEvaluationHandler.ListResources)
			maclawCatalog.GET("/maclaw/evaluation/resources/:id/preview", maclawEvaluationHandler.PreviewResource)
		}

		maclawEvaluation := auth.Group("/")
		maclawEvaluation.Use(middleware.RequireRole("enterprise", "admin"))
		{
			maclawEvaluation.GET("/maclaw/evaluation/targets", maclawEvaluationHandler.ListTargets)
			maclawEvaluation.POST("/maclaw/evaluation/targets", maclawEvaluationHandler.CreateTarget)
			maclawEvaluation.GET("/maclaw/evaluation/targets/:id", maclawEvaluationHandler.GetTarget)
			maclawEvaluation.POST("/maclaw/evaluation/targets/:id/health-check", maclawEvaluationHandler.ProbeTarget)
			maclawEvaluation.GET("/maclaw/evaluation/reports/:id", maclawEvaluationHandler.GetReport)
			maclawEvaluation.GET("/maclaw/evaluation/reports/:id/export", maclawEvaluationHandler.ExportReport)
			maclawEvaluation.GET("/maclaw/evaluation/evidence", maclawEvaluationHandler.ListEvidence)
			maclawEvaluation.GET("/maclaw/evaluation/evidence/:id", maclawEvaluationHandler.GetEvidence)
			maclawEvaluation.POST("/maclaw/evaluation/runs", maclawEvaluationHandler.StartRun)
			maclawEvaluation.GET("/maclaw/evaluation/jobs", maclawEvaluationHandler.ListJobs)
			maclawEvaluation.GET("/maclaw/evaluation/jobs/:id", maclawEvaluationHandler.GetJob)
			maclawEvaluation.GET("/maclaw/evaluation/jobs/:id/recovery", maclawEvaluationHandler.GetJobRecovery)
			maclawEvaluation.POST("/maclaw/evaluation/jobs/:id/cancel", maclawEvaluationHandler.CancelJob)
			maclawEvaluation.POST("/maclaw/evaluation/jobs/:id/retry", maclawEvaluationHandler.RetryJob)
			maclawEvaluation.POST("/maclaw/evaluation/jobs/:id/resume", maclawEvaluationHandler.ResumeJob)
			maclawEvaluation.GET("/maclaw/evaluation/sessions", maclawRuntimeHandler.ListSessions)
			maclawEvaluation.POST("/maclaw/evaluation/sessions", maclawRuntimeHandler.CreateSession)
			maclawEvaluation.GET("/maclaw/evaluation/sessions/:id", maclawRuntimeHandler.GetSession)
			maclawEvaluation.DELETE("/maclaw/evaluation/sessions/:id", maclawRuntimeHandler.DeleteSession)
			maclawEvaluation.POST("/maclaw/evaluation/sessions/:id/messages", maclawRuntimeHandler.PostMessage)
			maclawEvaluation.POST("/maclaw/evaluation/sessions/:id/confirm", maclawRuntimeHandler.ConfirmPlan)
			maclawEvaluation.GET("/maclaw/evaluation/runs/:id/events", maclawRuntimeHandler.StreamRunEvents)
			maclawEvaluation.POST("/maclaw/evaluation/runs/:id/cancel", maclawRuntimeHandler.CancelRun)
		}

		maclawEngine := auth.Group("/")
		maclawEngine.Use(middleware.RequireRole("enterprise", "admin"))
		{
			maclawEngine.GET("/maclaw/engine/health", engineRunHandler.Health)
			maclawEngine.POST("/maclaw/engine/runs", engineRunHandler.CreateRun)
			maclawEngine.GET("/maclaw/engine/runs", engineRunHandler.ListRuns)
			maclawEngine.GET("/maclaw/engine/runs/:id", engineRunHandler.GetRun)
			maclawEngine.POST("/maclaw/engine/runs/:id/cancel", engineRunHandler.CancelRun)
			maclawEngine.GET("/maclaw/engine/runs/:id/events", engineRunHandler.StreamRunEvents)
		}

		enterprise := auth.Group("/")
		enterprise.Use(middleware.RequireRole("enterprise", "admin"))
		{
			enterprise.GET("/orchestration-llm/config", legacyGone)
			enterprise.PUT("/orchestration-llm/config", legacyGone)
			enterprise.POST("/orchestration-llm/test", legacyGone)
			enterprise.GET("/target-llm/config", legacyGone)
			enterprise.PUT("/target-llm/config", legacyGone)
			enterprise.POST("/target-llm/test", legacyGone)
			enterprise.GET("/enterprise/welcome-capabilities", enterpriseWelcomeHandler.ListCapabilities)
			enterprise.GET("/enterprise/skills/:id/launch", legacyGone)
		}

		expert := auth.Group("/")
		expert.Use(middleware.RequireRole("expert", "admin"))
		{
			expert.POST("/tools", legacyGone)
			expert.PUT("/tools/:id/publish", legacyGone)
			expert.PUT("/tools/:id/submit", legacyGone)
			expert.PUT("/tools/:id/deprecate", legacyGone)
			expert.POST("/assets", legacyGone)
			expert.GET("/billing/earnings", billingHandler.GetExpertEarnings)
			expert.GET("/tools/categories", legacyGone)

			expert.POST("/samples", sampleHandler.Upload)
			expert.GET("/samples", sampleHandler.List)
			expert.DELETE("/samples/:id", sampleHandler.Delete)
			expert.GET("/samples/:id/preview", sampleHandler.Preview)
			expert.POST("/composed-attacks", composedAttackHandler.Upload)
			expert.GET("/composed-attacks", composedAttackHandler.List)
			expert.DELETE("/composed-attacks/:id", composedAttackHandler.Delete)
			expert.GET("/composed-attacks/:id/preview", composedAttackHandler.Preview)

			expert.POST("/templates", tplHandler.Create)
			expert.GET("/templates", tplHandler.List)
			expert.PUT("/templates/:id", tplHandler.Update)
			expert.DELETE("/templates/:id", tplHandler.Delete)
			expert.POST("/templates/upload-csv", tplHandler.UploadCSV)
			expert.GET("/auxiliary-llm/config", legacyGone)
			expert.PUT("/auxiliary-llm/config", legacyGone)
			expert.POST("/auxiliary-llm/test", legacyGone)

			expert.GET("/external-mcp-servers", legacyGone)
			expert.POST("/external-mcp-servers", legacyGone)
			expert.PUT("/external-mcp-servers/:id", legacyGone)
			expert.DELETE("/external-mcp-servers/:id", legacyGone)
			expert.POST("/external-mcp-servers/:id/test", legacyGone)
			expert.POST("/external-mcp-servers/:id/sync", legacyGone)
			expert.POST("/external-mcp-servers/:id/disconnect", legacyGone)
			expert.GET("/external-mcp-servers/:id/tools", legacyGone)

			expert.POST("/maclaw/evaluation/resources", maclawEvaluationHandler.CreateResource)
			expert.POST("/maclaw/evaluation/resources/:id/archive", maclawEvaluationHandler.ArchiveResource)
			expert.POST("/maclaw/skills/import", maclawSkillHandler.Import)
			expert.POST("/maclaw/skills/install", maclawSkillHandler.Install)

			expert.POST("/skills/import", legacyGone)
			expert.GET("/skills", legacyGone)
			expert.GET("/skills/:id", legacyGone)
			expert.GET("/skills/:id/config", legacyGone)
			expert.PUT("/skills/:id/config", legacyGone)
			expert.POST("/skills/:id/versions/:version_id/self-test", legacyGone)
			expert.POST("/skills/:id/publish", legacyGone)
			expert.POST("/skills/:id/disable", legacyGone)
			expert.POST("/skills/:id/enable", legacyGone)
			expert.POST("/skills/:id/deprecate", legacyGone)
			expert.DELETE("/skills/:id", legacyGone)
			expert.GET("/skills/:id/runs", legacyGone)
		}

		adminGroup := auth.Group("/admin")
		adminGroup.Use(middleware.RequireRole("admin"))
		{
			adminGroup.GET("/users", adminHandler.ListUsers)
			adminGroup.PUT("/users/:id/role", adminHandler.UpdateUserRole)
			adminGroup.PUT("/users/:id/active", adminHandler.SetUserActive)
			adminGroup.DELETE("/users/:id", adminHandler.DeleteUser)
			adminGroup.GET("/stats", adminHandler.GetStats)
			adminGroup.GET("/overview", adminMaclawHandler.Overview)
			adminGroup.GET("/maclaw/accounts", adminMaclawHandler.ListAccounts)
			adminGroup.GET("/maclaw/accounts/:userId/config", adminMaclawHandler.GetAccountConfig)
			adminGroup.PUT("/maclaw/accounts/:userId/config", adminMaclawHandler.UpdateAccountConfig)
			adminGroup.POST("/maclaw/accounts/:userId/config/validate", adminMaclawHandler.ValidateAccountConfig)
			adminGroup.POST("/maclaw/accounts/:userId/config/test", adminMaclawHandler.TestAccountConfig)
			adminGroup.GET("/maclaw/model-default", adminMaclawHandler.GetDefaultModelConfig)
			adminGroup.PUT("/maclaw/model-default", adminMaclawHandler.UpdateDefaultModelConfig)
			adminGroup.POST("/maclaw/model-default/validate", adminMaclawHandler.ValidateDefaultModelConfig)
			adminGroup.POST("/maclaw/model-default/test", adminMaclawHandler.TestDefaultModelConfig)
			adminGroup.GET("/maclaw/hub-config", adminMaclawHandler.GetHubConfig)
			adminGroup.PUT("/maclaw/hub-config", adminMaclawHandler.UpdateHubConfig)
			adminGroup.POST("/maclaw/hub-config/sync", adminMaclawHandler.SyncHubConfig)
			adminGroup.GET("/maclaw/hub-config/status", adminMaclawHandler.GetHubConfigStatus)
			adminGroup.GET("/maclaw/resources", adminMaclawHandler.ListResources)
			adminGroup.PATCH("/maclaw/resources/:sourceResourceId", adminMaclawHandler.UpdateResourceGovernance)
			adminGroup.GET("/maclaw/jobs", adminMaclawHandler.ListJobs)
		}

		chat := auth.Group("/chat")
		{
			chat.POST("/sessions", legacyGone)
			chat.GET("/sessions", legacyGone)
			chat.GET("/sessions/:id", legacyGone)
			chat.POST("/sessions/:id/messages", legacyGone)
			chat.POST("/sessions/:id/confirm", legacyGone)
			chat.DELETE("/sessions/:id", legacyGone)
		}
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logger.Info("server starting", map[string]interface{}{"addr": addr})
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}

func applyCORSHeaders(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// migrationsDir 是迁移 SQL 文件目录，相对进程工作目录。
// 生产容器内 WORKDIR=/app，且 docker-compose 将宿主 ./migrations 只读挂载到
// /app/migrations（见 docker-compose.yml backend.volumes），因此该相对路径
// 在容器内与本地（仓库根目录起进程）下一致。
const migrationsDir = "migrations"

// migrationFiles 是启动时按序执行的迁移清单（含目录前缀的相对路径）。
//
// 这份清单与 migrationsDir 下的 .sql 文件必须保持 1:1 —— 二者漂移的后果
// 曾是真实的生产故障：U5 新增 migrations/027_engine_run_job_link.sql 与
// 写 job_id 的持久层代码，但漏登记本清单；pkg/db.MigrateUp 无版本跟踪、
// 只执行清单内文件，线上库因此停留在 026 结构（engine_runs 无 job_id 列），
// 「确认执行」全线 502 engine_submit_failed
// （column "job_id" of relation "engine_runs" does not exist，SQLSTATE 42703，
// 见仓库根 audit-2026-10-12-engine-confirm-failure.md）。
//
// 除 027 外，本清单还补登记了 002/004/006/007/008/011~015 共 10 个历史
// 遗留文件：它们自最早的备份快照起就未入清单（清单与文件系统长期漂移），
// 但其建表结果（chat_sessions、app_detectors、*_llm_configs、
// external_mcp_*、skills 等）经 docs/architecture/legacy-schema-registry.md
// 登记为现存于生产 schema 的历史表（DD-8=B 保留不 drop），且全部幂等，
// 补登记后对存量库为无操作，对全新库则补全文档所述 schema。
//
// 防漂移机制（fail-closed，两层）：
//  1. 启动断言 assertMigrationRegistry：目录与清单任一方向漂移即拒绝启动，
//     漏登记在动库之前就暴露，而非等到运行期某条 SQL 缺列才炸；
//  2. 回归测试 TestMigrationRegistryCoversDirectory（cmd/server/main_test.go）：
//     CI 侧等价断言，漏登记直接测试红。
var migrationFiles = []string{
	"migrations/001_init.sql",
	"migrations/002_chat.sql",
	"migrations/003_tool_categories.sql",
	"migrations/004_app_detector.sql",
	"migrations/005_fix_attack_samples_columns.sql",
	"migrations/006_auxiliary_llm_configs.sql",
	"migrations/007_llm_configs.sql",
	"migrations/008_remove_collecting_api_state.sql",
	"migrations/009_template_upload_batches.sql",
	"migrations/010_composed_attacks.sql",
	"migrations/011_external_mcp_servers.sql",
	"migrations/012_external_mcp_upstream_config.sql",
	"migrations/013_skills.sql",
	"migrations/014_skill_status_disabled.sql",
	"migrations/015_skill_config_values.sql",
	"migrations/016_maclaw_account_mappings.sql",
	"migrations/017_maclaw_resource_projection.sql",
	"migrations/018_maclaw_model_defaults.sql",
	"migrations/019_maclaw_skill_projection.sql",
	"migrations/020_maclaw_hub_configs.sql",
	"migrations/021_maclaw_target_configs.sql",
	"migrations/022_maclaw_redteam_artifacts.sql",
	"migrations/023_maclaw_resources.sql",
	"migrations/024_template_custom_categories.sql",
	"migrations/025_attack_sample_categories.sql",
	"migrations/026_promptfoo_engine.sql",
	"migrations/027_engine_run_job_link.sql",
}

// runMigrations 按 migrationFiles 清单顺序执行迁移。
//
// 先跑清单一致性断言再动库：漏登记的迁移必须在连接数据库执行 SQL 之前
// 就暴露（拒绝启动），而不是像 027 那样等到运行期 INSERT 缺列才炸。
func runMigrations(ctx context.Context, pgPool *pgxpool.Pool) {
	if err := assertMigrationRegistry(migrationsDir, migrationFiles); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	for _, path := range migrationFiles {
		if err := db.MigrateUp(ctx, pgPool, path); err != nil {
			log.Fatalf("[FATAL] run migration %s: %v", path, err)
		}
	}
	logger.Info("database migration completed")
}

// assertMigrationRegistry 双向校验迁移清单与磁盘目录一致：
//
//   - 目录下有 .sql 文件不在 files 清单中 → 漏登记（027 故障根因）；
//   - files 清单引用了目录中不存在的文件 → 清单陈旧（迁移被删/改名未同步）。
//
// 两个方向都视为 drift：前者意味着该迁移永不执行，后者意味着部署包与代码
// 不是同一版本。返回 nil 表示 1:1 一致。files 以参数注入以便测试构造漂移
// 场景（生产调用方传入 migrationFiles）。
func assertMigrationRegistry(dir string, files []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	onDisk := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		onDisk[filepath.ToSlash(filepath.Join(dir, entry.Name()))] = true
	}

	var unregistered, stale []string
	for _, path := range files {
		key := filepath.ToSlash(path)
		if !onDisk[key] {
			stale = append(stale, path)
			continue
		}
		delete(onDisk, key)
	}
	for path := range onDisk {
		unregistered = append(unregistered, path)
	}
	sort.Strings(unregistered)
	sort.Strings(stale)

	if len(unregistered) > 0 || len(stale) > 0 {
		return fmt.Errorf("migration registry drift: present in %s/ but not registered: %v; "+
			"registered but missing on disk: %v — fix migrationFiles in cmd/server/main.go "+
			"to match %s/ 1:1", dir, unregistered, stale, dir)
	}
	return nil
}

func newMinIOClientWithRetry(cfg config.StorageConfig, attempts int, delay time.Duration) (*storage.MinIOClient, error) {
	if attempts <= 0 {
		attempts = 1
	}
	if delay <= 0 {
		delay = time.Second
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		client, err := storage.NewMinIOClient(cfg)
		if err == nil {
			if attempt > 1 {
				logger.Info("MinIO connected after retry", map[string]interface{}{"attempt": attempt, "endpoint": cfg.Endpoint})
			}
			return client, nil
		}
		lastErr = err
		if attempt < attempts {
			logger.Warn("MinIO not ready, retrying", map[string]interface{}{"attempt": attempt, "err": err.Error()})
			time.Sleep(delay)
		}
	}
	return nil, lastErr
}

func buildMaclawRuntime(
	cfg *config.Config,
	userRepo *repository.UserRepository,
	mappingRepo *repository.MaclawAccountMappingRepository,
	publicationRepo *repository.MaclawResourcePublicationRepository,
	shadowRepo *repository.MaclawResourceShadowRepository,
	skillPublicationRepo *repository.MaclawSkillPublicationRepository,
	skillShadowRepo *repository.MaclawSkillShadowRepository,
	keyStore *crypto.KeyStore,
	runtimeConfigService *maclawpkg.RuntimeConfigService,
	hubConfigService *maclawpkg.MaclawHubConfigService,
	targetConfigService *maclawpkg.TargetConfigService,
	artifactService *maclawpkg.RedteamArtifactService,
	resourceStoreService *maclawpkg.ResourceStoreService,
) (maclawpkg.GatewayProvider, *maclawpkg.ResourceProjectionService, *maclawpkg.SkillProjectionService, *maclawpkg.CapabilityCatalogService) {
	capabilities := maclawpkg.RuntimeCapabilitiesFromProfile(cfg.Maclaw.CapabilityProfile)
	if maclawpkg.NormalizeRuntimeKind(cfg.Maclaw.RuntimeKind) != maclawpkg.RuntimeKindMaclawSrv {
		log.Fatalf("[FATAL] unsupported maclaw runtime kind %q; legacy evaluation runtime has been retired", cfg.Maclaw.RuntimeKind)
	}
	if !cfg.Maclaw.ProvisioningEnabled {
		log.Fatalf("[FATAL] maclaw provisioning cannot be disabled; global token/default-instance fallback has been retired")
	}
	if keyStore == nil {
		log.Fatalf("[FATAL] maclaw provisioning requires crypto.master_key")
	}
	adminClient, err := maclawpkg.NewAdminClient(maclawpkg.AdminConfig{
		BaseURL:        cfg.Maclaw.BaseURL,
		AdminSecret:    cfg.Maclaw.AdminSecret,
		TimeoutSeconds: cfg.Maclaw.TimeoutSeconds,
	})
	if err != nil {
		log.Fatalf("[FATAL] init maclaw admin client: %v", err)
	}
	provisioner, err := maclawpkg.NewProvisioner(maclawpkg.ProvisionerConfig{
		BaseURL:        cfg.Maclaw.BaseURL,
		RuntimeKind:    cfg.Maclaw.RuntimeKind,
		TimeoutSeconds: cfg.Maclaw.TimeoutSeconds,
	}, userRepo, mappingRepo, adminClient, keyStore)
	if err != nil {
		log.Fatalf("[FATAL] init maclaw provisioner: %v", err)
	}
	provisioner.SetDefaultConfigProvider(runtimeConfigService)
	provisioner.SetHubConfigProvider(hubConfigService)
	provisioner.SetRedteamMCPBridgeConfig(maclawpkg.RedteamMCPBridgeProvisioningConfig{
		EndpointURL: cfg.Maclaw.RedteamMCPEndpoint,
		AuthSecret:  cfg.Maclaw.RedteamMCPSecret,
	})
	provider := maclawpkg.NewProvisioningGatewayProvider(provisioner)
	provider.SetTargetConfigService(targetConfigService)
	provider.SetArtifactService(artifactService)
	provider.SetResourceStoreService(resourceStoreService)
	resourceProjection := maclawpkg.NewResourceProjectionService(provider, publicationRepo, shadowRepo)
	resourceProjection.SetRuntimeCapabilities(capabilities)
	skillProjection := maclawpkg.NewSkillProjectionService(provider, skillPublicationRepo, skillShadowRepo)
	skillProjection.SetRuntimeCapabilities(capabilities)
	skillProjection.SetHubConfigProvider(hubConfigService)
	skillProjection.SetAccountMappingStore(mappingRepo)
	capabilityCatalog := maclawpkg.NewCapabilityCatalogService(publicationRepo, skillPublicationRepo)
	return provider, resourceProjection, skillProjection, capabilityCatalog
}
