package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"evaluating_platform/internal/maclaw"
)

type maclawSkillHubConfigProvider interface {
	GetHubConfig(context.Context) (*maclaw.MaclawHubConfig, error)
}

type MaclawSkillHandler struct {
	gateway    maclaw.SkillGateway
	provider   maclaw.GatewayProvider
	projection *maclaw.SkillProjectionService
	hubConfig  maclawSkillHubConfigProvider
}

func NewMaclawSkillHandler(gateway maclaw.SkillGateway) *MaclawSkillHandler {
	return &MaclawSkillHandler{gateway: gateway}
}

func NewMaclawSkillHandlerWithProviderAndProjection(provider maclaw.GatewayProvider, projection *maclaw.SkillProjectionService) *MaclawSkillHandler {
	return &MaclawSkillHandler{provider: provider, projection: projection}
}

func NewMaclawSkillHandlerWithProviderProjectionAndHub(provider maclaw.GatewayProvider, projection *maclaw.SkillProjectionService, hubConfig maclawSkillHubConfigProvider) *MaclawSkillHandler {
	return &MaclawSkillHandler{provider: provider, projection: projection, hubConfig: hubConfig}
}

func (h *MaclawSkillHandler) List(c *gin.Context) {
	session, gateway, ok := h.skillGatewaySession(c)
	if !ok {
		return
	}
	limit, err := parseOptionalPositiveInt(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
		return
	}
	h.backfillExpertHubSkills(c, session)
	h.backfillEnterpriseHubSkills(c, session)
	items, err := gateway.ListSkills(c.Request.Context(), limit)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	if h.projection != nil && isMaclawEnterpriseLike(c) {
		items, err = h.projection.ListEnterpriseCatalog(c.Request.Context(), items, maclaw.SkillSearchInput{})
		if err != nil {
			log.Printf("[WARN] maclaw skill enterprise catalog failed: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw skill projection failed", "code": "maclaw_skill_projection_failed"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *MaclawSkillHandler) Search(c *gin.Context) {
	session, gateway, ok := h.skillGatewaySession(c)
	if !ok {
		return
	}
	var in maclaw.SkillSearchInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.applySearchSourcePolicy(c.Request.Context(), &in); err != nil {
		if errors.Is(err, errSkillSourceNotAllowed) {
			c.JSON(http.StatusForbidden, gin.H{"error": "maclaw skill source is not allowed", "code": "maclaw_skill_source_not_allowed"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw hub config unavailable", "code": "maclaw_hub_config_unavailable"})
		return
	}
	h.backfillExpertHubSkills(c, session)
	h.backfillEnterpriseHubSkills(c, session)
	items, err := gateway.SearchSkills(c.Request.Context(), in)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	if h.projection != nil && isMaclawEnterpriseLike(c) {
		items, err = h.projection.SearchEnterpriseCatalog(c.Request.Context(), items, in)
		if err != nil {
			log.Printf("[WARN] maclaw skill enterprise search catalog failed: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw skill projection failed", "code": "maclaw_skill_projection_failed"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *MaclawSkillHandler) backfillExpertHubSkills(c *gin.Context, session *maclaw.GatewaySession) {
	if h == nil || h.projection == nil || strings.TrimSpace(c.GetString("user_role")) != "expert" {
		return
	}
	if err := h.projection.SyncExpertPublishedHubSkillsBestEffort(c.Request.Context(), maclawRuntimeIdentity(c), session); err != nil {
		log.Printf("[WARN] maclaw expert hub skill backfill failed: %v", err)
	}
}

func (h *MaclawSkillHandler) backfillEnterpriseHubSkills(c *gin.Context, session *maclaw.GatewaySession) {
	if h == nil || h.projection == nil || !isMaclawEnterpriseLike(c) {
		return
	}
	if err := h.projection.SyncEnterprisePublishedHubSkillsBestEffort(c.Request.Context(), maclawRuntimeIdentity(c), session); err != nil {
		log.Printf("[WARN] maclaw enterprise hub skill backfill failed: %v", err)
	}
}

func (h *MaclawSkillHandler) Import(c *gin.Context) {
	session, gateway, ok := h.skillGatewaySession(c)
	if !ok {
		return
	}
	var in maclaw.SkillImportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hubURL, err := h.defaultHubURL(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw hub config unavailable", "code": "maclaw_hub_config_unavailable"})
		return
	}
	if strings.TrimSpace(hubURL) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw hub is required for skill distribution", "code": "maclaw_hub_required"})
		return
	}
	skillID, err := h.submitSkillArchiveToHub(c.Request.Context(), hubURL, session, in)
	if err != nil {
		log.Printf("[WARN] maclaw skill hub publish failed: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw skill hub publish failed", "code": "maclaw_skill_hub_publish_failed"})
		return
	}
	items, err := gateway.InstallSkill(c.Request.Context(), maclaw.SkillInstallInput{
		Source:      maclaw.DefaultHubSkillSource,
		SkillHubURL: hubURL,
		SkillID:     skillID,
		Overwrite:   in.Overwrite,
	})
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	attachHubSkillID(items, skillID)
	if !h.recordSkillPublication(c, session, items) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{"items": items})
}

type hubSkillSubmissionStatus struct {
	Status   string `json:"status"`
	SkillID  string `json:"skill_id"`
	ErrorMsg string `json:"error_msg"`
}

var skillHubHTTPClient = &http.Client{Timeout: 60 * time.Second}

func (h *MaclawSkillHandler) submitSkillArchiveToHub(ctx context.Context, hubURL string, session *maclaw.GatewaySession, in maclaw.SkillImportInput) (string, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.ZipBase64))
	if err != nil {
		return "", fmt.Errorf("decode zip_base64: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("zip archive is empty")
	}
	data, err = normalizeSkillArchiveForHub(data)
	if err != nil {
		return "", err
	}
	submissionID, err := submitSkillHubArchive(ctx, hubURL, skillHubSubmitEmail(session), firstNonEmpty(in.ArchiveName, "skill.zip"), data)
	if err != nil {
		return "", err
	}
	status, err := pollSkillHubSubmission(ctx, hubURL, submissionID, 60*time.Second)
	if err != nil {
		return "", err
	}
	if strings.ToLower(strings.TrimSpace(status.Status)) != "success" {
		if strings.TrimSpace(status.ErrorMsg) != "" {
			return "", fmt.Errorf("hub submission %s %s: %s", submissionID, status.Status, status.ErrorMsg)
		}
		return "", fmt.Errorf("hub submission %s ended with status %s", submissionID, status.Status)
	}
	if strings.TrimSpace(status.SkillID) == "" {
		return "", fmt.Errorf("hub submission %s succeeded without skill_id", submissionID)
	}
	return strings.TrimSpace(status.SkillID), nil
}

type normalizedSkillArchiveEntry struct {
	name string
	data []byte
}

func normalizeSkillArchiveForHub(data []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return data, nil
	}
	entries := make([]normalizedSkillArchiveEntry, 0, len(reader.File))
	exists := map[string]bool{}
	for _, file := range reader.File {
		name, ok := normalizeSkillArchivePath(file.Name)
		if !ok {
			return nil, fmt.Errorf("unsafe skill archive path %q", file.Name)
		}
		if name == "" || file.FileInfo().IsDir() || exists[name] {
			continue
		}
		content, err := readZipFileContent(file)
		if err != nil {
			return nil, err
		}
		entries = append(entries, normalizedSkillArchiveEntry{name: name, data: content})
		exists[name] = true
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.name, "data/") {
			continue
		}
		runtimeName := "runtime/" + entry.name
		if exists[runtimeName] {
			continue
		}
		entries = append(entries, normalizedSkillArchiveEntry{name: runtimeName, data: entry.data})
		exists[runtimeName] = true
	}
	if !exists["runtime/embedded_data.py"] {
		if embedded := embeddedSkillDataPython(entries); len(embedded) > 0 {
			entries = append(entries, normalizedSkillArchiveEntry{name: "runtime/embedded_data.py", data: embedded})
			exists["runtime/embedded_data.py"] = true
		}
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, entry := range entries {
		w, err := writer.Create(entry.name)
		if err != nil {
			_ = writer.Close()
			return nil, err
		}
		if _, err := w.Write(entry.data); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func embeddedSkillDataPython(entries []normalizedSkillArchiveEntry) []byte {
	var body strings.Builder
	body.WriteString("# Generated by evaluating_platform before Hub submission.\n")
	body.WriteString("EMBEDDED_FILES = {\n")
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.name, "data/") {
			continue
		}
		body.WriteString(fmt.Sprintf("    %q: %q,\n", entry.name, base64.StdEncoding.EncodeToString(entry.data)))
		count++
	}
	body.WriteString("}\n")
	if count == 0 {
		return nil
	}
	return []byte(body.String())
}

func normalizeSkillArchivePath(name string) (string, bool) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	name = strings.TrimLeft(name, "/")
	if name == "" {
		return "", true
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", true
	}
	if strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean) {
		return "", false
	}
	return clean, true
}

func readZipFileContent(file *zip.File) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func submitSkillHubArchive(ctx context.Context, hubURL, email, fileName string, data []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("zip", safeSkillArchiveName(fileName))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	_ = writer.WriteField("email", email)
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(hubURL, "/")+"/api/v1/skills/submit", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := skillHubHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("hub submit failed: %s", strings.TrimSpace(string(respBody)))
	}
	var out struct {
		SubmissionID string `json:"submission_id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.SubmissionID) == "" {
		return "", fmt.Errorf("hub submit response missing submission_id")
	}
	return strings.TrimSpace(out.SubmissionID), nil
}

func pollSkillHubSubmission(ctx context.Context, hubURL, submissionID string, timeout time.Duration) (*hubSkillSubmissionStatus, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := fetchSkillHubSubmission(ctx, hubURL, submissionID)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(strings.TrimSpace(status.Status)) {
		case "success", "failed":
			return status, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("hub submission %s did not finish before timeout", submissionID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func fetchSkillHubSubmission(ctx context.Context, hubURL, submissionID string) (*hubSkillSubmissionStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(hubURL, "/")+"/api/v1/skill-submissions/"+submissionID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := skillHubHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hub submission status failed: %s", strings.TrimSpace(string(respBody)))
	}
	var out hubSkillSubmissionStatus
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func skillHubSubmitEmail(session *maclaw.GatewaySession) string {
	if session != nil && session.Mapping != nil && strings.TrimSpace(session.Mapping.PlatformEmail) != "" {
		return strings.TrimSpace(session.Mapping.PlatformEmail)
	}
	if session != nil && session.Mapping != nil && strings.TrimSpace(session.Mapping.PlatformUserID.String()) != "" {
		return session.Mapping.PlatformUserID.String() + "@evaluating-platform.local.invalid"
	}
	return "unknown@evaluating-platform.local.invalid"
}

func safeSkillArchiveName(v string) string {
	v = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(v, "\\", "_"), "/", "_"))
	if v == "" {
		v = "skill.zip"
	}
	if !strings.HasSuffix(strings.ToLower(v), ".zip") {
		v += ".zip"
	}
	return v
}

func (h *MaclawSkillHandler) Install(c *gin.Context) {
	session, gateway, ok := h.skillGatewaySession(c)
	if !ok {
		return
	}
	var in maclaw.SkillInstallInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.applyInstallSourcePolicy(c.Request.Context(), &in); err != nil {
		if errors.Is(err, errSkillSourceNotAllowed) {
			c.JSON(http.StatusForbidden, gin.H{"error": "maclaw skill source is not allowed", "code": "maclaw_skill_source_not_allowed"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw hub config unavailable", "code": "maclaw_hub_config_unavailable"})
		return
	}
	items, err := gateway.InstallSkill(c.Request.Context(), in)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	if strings.ToLower(strings.TrimSpace(in.Source)) == maclaw.DefaultHubSkillSource {
		attachHubSkillID(items, in.SkillID)
	}
	if !h.recordSkillPublication(c, session, items) {
		return
	}
	c.JSON(http.StatusCreated, gin.H{"items": items})
}

func attachHubSkillID(items []maclaw.SkillSummary, hubSkillID string) {
	hubSkillID = strings.TrimSpace(hubSkillID)
	if hubSkillID == "" {
		return
	}
	for i := range items {
		items[i].HubSkillID = hubSkillID
		if items[i].Metadata == nil {
			items[i].Metadata = map[string]string{}
		}
		items[i].Metadata["hub_skill_id"] = hubSkillID
	}
}

// recordSkillPublication 统一 Import/Install 的发布投影记录块（P2-05）。
// 返回 false 表示记录失败（已写响应）。
func (h *MaclawSkillHandler) recordSkillPublication(c *gin.Context, session *maclaw.GatewaySession, items []maclaw.SkillSummary) bool {
	if h.projection == nil {
		return true
	}
	identity := maclawRuntimeIdentity(c)
	if err := h.projection.RecordExpertSkills(c.Request.Context(), identity, session, items); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw skill publication failed", "code": "maclaw_skill_publication_failed"})
		return false
	}
	if strings.TrimSpace(identity.Role) == "expert" {
		if err := h.projection.SyncPublishedSkillsToAllEnterpriseMappings(c.Request.Context()); err != nil {
			log.Printf("[WARN] maclaw skill enterprise distribution failed: %v", err)
		}
	}
	return true
}

func (h *MaclawSkillHandler) available(c *gin.Context) bool {
	if h == nil || h.gateway == nil || !h.gateway.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw runtime is not configured"})
		return false
	}
	return true
}

func (h *MaclawSkillHandler) skillGatewaySession(c *gin.Context) (*maclaw.GatewaySession, maclaw.SkillGateway, bool) {
	if h != nil && h.provider != nil {
		session, ok := resolveMaclawGatewaySession(c, h.provider)
		if !ok {
			return nil, nil, false
		}
		return session, session.Client, true
	}
	if !h.available(c) {
		return nil, nil, false
	}
	return nil, h.gateway, true
}

var errSkillSourceNotAllowed = errors.New("maclaw skill source is not allowed")
var errSkillHubConfigUnavailable = errors.New("maclaw hub config is unavailable")

func (h *MaclawSkillHandler) applySearchSourcePolicy(ctx context.Context, in *maclaw.SkillSearchInput) error {
	if in == nil {
		return nil
	}
	cfg, err := h.resolveSkillHubPolicy(ctx)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	allowed := allowedSkillSourceSet(cfg.AllowedSources)
	if len(in.Sources) == 0 {
		in.Sources = append([]string(nil), cfg.AllowedSources...)
	} else if !allSkillSourcesAllowed(in.Sources, allowed) {
		return errSkillSourceNotAllowed
	}
	if needsSkillHubURL(in.Sources) && strings.TrimSpace(in.SkillHubURL) == "" {
		in.SkillHubURL = cfg.HubURL
	}
	return nil
}

func (h *MaclawSkillHandler) applyInstallSourcePolicy(ctx context.Context, in *maclaw.SkillInstallInput) error {
	if in == nil {
		return nil
	}
	cfg, err := h.resolveSkillHubPolicy(ctx)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	source := strings.ToLower(strings.TrimSpace(in.Source))
	if source == "" {
		source = maclaw.DefaultHubSkillSource
		in.Source = source
	}
	if !allowedSkillSourceSet(cfg.AllowedSources)[source] {
		return errSkillSourceNotAllowed
	}
	if source == maclaw.DefaultHubSkillSource && strings.TrimSpace(in.SkillHubURL) == "" {
		in.SkillHubURL = cfg.HubURL
	}
	return nil
}

func (h *MaclawSkillHandler) defaultHubURL(ctx context.Context) (string, error) {
	cfg, err := h.normalizedHubConfig(ctx)
	if err != nil || cfg == nil {
		return "", err
	}
	return cfg.HubURL, nil
}

// resolveSkillHubPolicy 统一 search/install 源策略的 hub 配置解析前奏（P2-05）。
// (nil, nil) = 无 hub 约束；(nil, err) = 配置不可用。
func (h *MaclawSkillHandler) resolveSkillHubPolicy(ctx context.Context) (*maclaw.MaclawHubConfig, error) {
	cfg, err := h.normalizedHubConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		if h == nil || h.hubConfig == nil {
			return nil, nil
		}
		return nil, errSkillHubConfigUnavailable
	}
	return cfg, nil
}

func (h *MaclawSkillHandler) normalizedHubConfig(ctx context.Context) (*maclaw.MaclawHubConfig, error) {
	if h == nil || h.hubConfig == nil {
		return nil, nil
	}
	cfg, err := h.hubConfig.GetHubConfig(ctx)
	if err != nil || cfg == nil || !cfg.Enabled {
		if err != nil {
			return nil, err
		}
		return nil, errSkillHubConfigUnavailable
	}
	allowed := normalizeAllowedSkillSources(cfg.AllowedSources)
	if len(allowed) == 0 {
		allowed = []string{maclaw.DefaultHubSkillSource}
	}
	hubURL := strings.TrimSpace(cfg.HubURL)
	if hubURL == "" {
		return nil, errSkillHubConfigUnavailable
	}
	return &maclaw.MaclawHubConfig{
		Enabled:        cfg.Enabled,
		HubURL:         hubURL,
		AllowedSources: allowed,
		UpdatedAt:      cfg.UpdatedAt,
	}, nil
}

func needsSkillHubURL(sources []string) bool {
	if len(sources) == 0 {
		return true
	}
	for _, source := range sources {
		if strings.ToLower(strings.TrimSpace(source)) == maclaw.DefaultHubSkillSource {
			return true
		}
	}
	return false
}

func normalizeAllowedSkillSources(sources []string) []string {
	out := make([]string, 0, len(sources))
	seen := map[string]bool{}
	for _, source := range sources {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "" || seen[source] {
			continue
		}
		seen[source] = true
		out = append(out, source)
	}
	return out
}

func allowedSkillSourceSet(sources []string) map[string]bool {
	out := map[string]bool{}
	for _, source := range normalizeAllowedSkillSources(sources) {
		out[source] = true
	}
	return out
}

func allSkillSourcesAllowed(sources []string, allowed map[string]bool) bool {
	for _, source := range sources {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "" {
			continue
		}
		if !allowed[source] {
			return false
		}
	}
	return true
}

func isMaclawEnterpriseLike(c *gin.Context) bool {
	role := c.GetString("user_role")
	return role == "enterprise" || role == "admin"
}
