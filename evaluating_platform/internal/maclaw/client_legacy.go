package maclaw

// client_legacy.go —— Skill 服务域与共享纯函数辅助（T4.2 client.go 分层）。
//
// 承载技能（skill）相关的 DTO 与网关方法，以及两个跨域复用的纯函数：
// summarizeSkills（上游原始条目 →对外 DTO）与 cloneStringList（防切片别名）。
// 另含 Content-Disposition 文件名解析 filenameFromContentDisposition，
// 它服务于 Evaluation 域的 ExportEvaluationReport，故随纯函数一并置于本文件。
//
// 由 U3-b T4.2 从原 client.go 物理拆分而来：仅搬运，未改逻辑或签名。

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type SkillSummary struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description,omitempty"`
	Triggers                []string          `json:"triggers,omitempty"`
	Status                  string            `json:"status,omitempty"`
	Source                  string            `json:"source,omitempty"`
	Version                 string            `json:"version,omitempty"`
	Type                    string            `json:"type,omitempty"`
	Mode                    string            `json:"mode,omitempty"`
	Platforms               []string          `json:"platforms,omitempty"`
	RequiresGUI             bool              `json:"requires_gui,omitempty"`
	RequiredArgs            []string          `json:"required_args,omitempty"`
	RequiredEnv             []string          `json:"required_env,omitempty"`
	RequiredCredentialFiles []string          `json:"required_credential_files,omitempty"`
	HubSkillID              string            `json:"hub_skill_id,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

type SkillSearchInput struct {
	Query            string   `json:"query"`
	Sources          []string `json:"sources,omitempty"`
	TopN             int      `json:"top_n,omitempty"`
	SkillHubURL      string   `json:"skill_hub_url,omitempty"`
	SkillMarketURL   string   `json:"skill_market_url,omitempty"`
	GitHubToken      string   `json:"github_token,omitempty"`
	IncludeInstalled bool     `json:"include_installed,omitempty"`
}

type SkillSearchResult struct {
	Source         string            `json:"source"`
	ID             string            `json:"id,omitempty"`
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Version        string            `json:"version,omitempty"`
	Author         string            `json:"author,omitempty"`
	TrustLevel     string            `json:"trust_level,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Downloads      int               `json:"downloads,omitempty"`
	AvgRating      float64           `json:"avg_rating,omitempty"`
	Price          int               `json:"price,omitempty"`
	RepoFullName   string            `json:"repo_full_name,omitempty"`
	RepoURL        string            `json:"repo_url,omitempty"`
	RawURL         string            `json:"raw_url,omitempty"`
	FilePath       string            `json:"file_path,omitempty"`
	Branch         string            `json:"branch,omitempty"`
	DefinitionType string            `json:"definition_type,omitempty"`
	Installed      bool              `json:"installed,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type SkillImportInput struct {
	ZipBase64   string `json:"zip_base64"`
	Overwrite   bool   `json:"overwrite,omitempty"`
	ArchiveName string `json:"archive_name,omitempty"`
}

type SkillInstallInput struct {
	Source         string `json:"source"`
	RepoURL        string `json:"repo_url,omitempty"`
	RawURL         string `json:"raw_url,omitempty"`
	RepoFullName   string `json:"repo_full_name,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	Branch         string `json:"branch,omitempty"`
	DefinitionType string `json:"definition_type,omitempty"`
	ZipBase64      string `json:"zip_base64,omitempty"`
	SkillHubURL    string `json:"skill_hub_url,omitempty"`
	SkillID        string `json:"skill_id,omitempty"`
	Overwrite      bool   `json:"overwrite,omitempty"`
	GitHubToken    string `json:"github_token,omitempty"`
}

type SkillExport struct {
	Name          string `json:"name"`
	FileName      string `json:"file_name"`
	ArchiveBase64 string `json:"archive_base64"`
	SizeBytes     int64  `json:"size_bytes"`
}
type rawSkillEntry struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	Triggers                []string          `json:"triggers"`
	Status                  string            `json:"status"`
	Source                  string            `json:"source"`
	Version                 string            `json:"version"`
	HubVersion              string            `json:"hub_version"`
	Type                    string            `json:"type"`
	Mode                    string            `json:"mode"`
	Platforms               []string          `json:"platforms"`
	RequiresGUI             bool              `json:"requires_gui"`
	RequiredArgs            []string          `json:"required_args"`
	RequiredEnv             []string          `json:"required_env"`
	RequiredCredentialFiles []string          `json:"required_credential_files"`
	HubSkillID              string            `json:"hub_skill_id"`
	Metadata                map[string]string `json:"metadata"`
}

func (c *Client) ListSkills(ctx context.Context, limit int) ([]SkillSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v1/skills"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []rawSkillEntry `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return summarizeSkills(out.Items), nil
}

func (c *Client) SearchSkills(ctx context.Context, in SkillSearchInput) ([]SkillSearchResult, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out struct {
		Items []SkillSearchResult `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/skills/search", in, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) InstallSkill(ctx context.Context, in SkillInstallInput) ([]SkillSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out struct {
		Items []rawSkillEntry `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/skills/install", in, &out); err != nil {
		return nil, err
	}
	return summarizeSkills(out.Items), nil
}
func summarizeSkills(items []rawSkillEntry) []SkillSummary {
	out := make([]SkillSummary, 0, len(items))
	for _, item := range items {
		metadata := cloneMetadata(item.Metadata)
		if strings.TrimSpace(item.HubSkillID) != "" {
			metadata["hub_skill_id"] = strings.TrimSpace(item.HubSkillID)
		}
		out = append(out, SkillSummary{
			Name:                    strings.TrimSpace(item.Name),
			Description:             strings.TrimSpace(item.Description),
			Triggers:                cloneStringList(item.Triggers),
			Status:                  strings.TrimSpace(item.Status),
			Source:                  strings.TrimSpace(item.Source),
			Version:                 firstNonEmptyString(item.Version, item.HubVersion),
			Type:                    strings.TrimSpace(item.Type),
			Mode:                    strings.TrimSpace(item.Mode),
			Platforms:               cloneStringList(item.Platforms),
			RequiresGUI:             item.RequiresGUI,
			RequiredArgs:            cloneStringList(item.RequiredArgs),
			RequiredEnv:             cloneStringList(item.RequiredEnv),
			RequiredCredentialFiles: cloneStringList(item.RequiredCredentialFiles),
			HubSkillID:              strings.TrimSpace(item.HubSkillID),
			Metadata:                metadata,
		})
	}
	return out
}

func cloneStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filenameFromContentDisposition(raw, fallback string) string {
	_, params, err := mime.ParseMediaType(raw)
	if err == nil {
		if filename := strings.TrimSpace(params["filename"]); filename != "" {
			return filename
		}
	}
	return fallback
}
