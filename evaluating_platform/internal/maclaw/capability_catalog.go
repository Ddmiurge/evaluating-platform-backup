package maclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"evaluating_platform/internal/model"
	"evaluating_platform/internal/maclaw/redteam"
)

const (
	CapabilitySourceResource = "resource"
	CapabilitySourceSkill    = "skill"
	CapabilitySourceSample   = "sample"
	CapabilitySourceTemplate = "template"
	CapabilitySourceComposed = "composed_attack"

	DefaultCapabilityCatalogLimit = 5
	MaxCapabilityCatalogLimit     = 8
)

type CapabilityCard struct {
	SourceType         string            `json:"source_type"`
	SourceExpertUserID string            `json:"source_expert_user_id,omitempty"`
	SourceRef          string            `json:"source_ref"`
	SourceVersion      string            `json:"source_version,omitempty"`
	Name               string            `json:"name"`
	Summary            string            `json:"summary,omitempty"`
	RiskTypes          []string          `json:"risk_types,omitempty"`
	TargetTypes        []string          `json:"target_types,omitempty"`
	Languages          []string          `json:"languages,omitempty"`
	UseWhen            []string          `json:"use_when,omitempty"`
	DoNotUseWhen       []string          `json:"do_not_use_when,omitempty"`
	InputsRequired     []string          `json:"inputs_required,omitempty"`
	Outputs            []string          `json:"outputs,omitempty"`
	Tags               []string          `json:"tags,omitempty"`
	Enabled            bool              `json:"enabled"`
	Status             string            `json:"status,omitempty"`
	SafeMetadata       map[string]string `json:"safe_metadata,omitempty"`
}

type CapabilityCatalogQuery struct {
	Query       string
	RiskTypes   []string
	TargetTypes []string
	Languages   []string
	Limit       int
}

type RuntimeCapabilityContext struct {
	AgentProfile string           `json:"agent_profile,omitempty"`
	Query        string           `json:"query,omitempty"`
	Cards        []CapabilityCard `json:"capability_cards,omitempty"`
}

type CapabilityCatalogService struct {
	resources ResourcePublicationStore
	skills    SkillPublicationStore
	samples   publishedSampleStore
	templates publishedTemplateStore
	composed  publishedComposedAttackStore
}

type scoredCapabilityCard struct {
	card  CapabilityCard
	score int
}

func NewCapabilityCatalogService(resources ResourcePublicationStore, skills SkillPublicationStore) *CapabilityCatalogService {
	return &CapabilityCatalogService{resources: resources, skills: skills}
}

type publishedSampleStore interface {
	ListPublished(context.Context, string, int, int) ([]model.AttackSample, int, error)
}

type publishedTemplateStore interface {
	ListPublished(context.Context, string, int, int) ([]model.Template, int, error)
}

type publishedComposedAttackStore interface {
	ListPublished(context.Context, string, int, int) ([]model.ComposedAttack, int, error)
}

func (s *CapabilityCatalogService) SetPlatformDataStores(samples publishedSampleStore, templates publishedTemplateStore, composed publishedComposedAttackStore) {
	if s == nil {
		return
	}
	s.samples = samples
	s.templates = templates
	s.composed = composed
}

func (s *CapabilityCatalogService) Search(ctx context.Context, q CapabilityCatalogQuery) ([]CapabilityCard, error) {
	if s == nil {
		return nil, nil
	}
	limit := normalizeCapabilityLimit(q.Limit)
	cards := []CapabilityCard{}
	if s.resources != nil {
		items, err := s.resources.ListPublished(ctx, EvaluationResourceQuery{Limit: 200})
		if err != nil {
			return nil, fmt.Errorf("list resource capabilities: %w", err)
		}
		for _, item := range items {
			cards = append(cards, resourceCapabilityCard(item))
		}
	}
	if s.skills != nil {
		items, err := s.skills.ListPublishedSkills(ctx, SkillSearchInput{TopN: 200})
		if err != nil {
			return nil, fmt.Errorf("list skill capabilities: %w", err)
		}
		for _, item := range items {
			cards = append(cards, skillCapabilityCard(item))
		}
	}
	if s.samples != nil {
		items, _, err := s.samples.ListPublished(ctx, "", 200, 0)
		if err != nil {
			return nil, fmt.Errorf("list sample capabilities: %w", err)
		}
		for _, item := range items {
			cards = append(cards, sampleCapabilityCard(item))
		}
	}
	if s.templates != nil {
		items, _, err := s.templates.ListPublished(ctx, "", 200, 0)
		if err != nil {
			return nil, fmt.Errorf("list template capabilities: %w", err)
		}
		for _, item := range items {
			cards = append(cards, templateCapabilityCard(item))
		}
	}
	if s.composed != nil {
		items, _, err := s.composed.ListPublished(ctx, "", 200, 0)
		if err != nil {
			return nil, fmt.Errorf("list composed attack capabilities: %w", err)
		}
		for _, item := range items {
			cards = append(cards, composedAttackCapabilityCard(item))
		}
	}
	// Built-in promptfoo engine plugin/strategy catalog (static, Phase 1).
	for _, card := range PromptfooPluginCatalogCards() {
		cards = append(cards, card)
	}
	scored := make([]scoredCapabilityCard, 0, len(cards))
	for _, card := range cards {
		if !card.Enabled {
			continue
		}
		score := scoreCapabilityCard(card, q)
		if score <= 0 && hasCapabilityQuery(q) {
			continue
		}
		scored = append(scored, scoredCapabilityCard{card: card, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].card.Name < scored[j].card.Name
		}
		return scored[i].score > scored[j].score
	})
	scored = selectCapabilityResults(scored, limit, q)
	out := make([]CapabilityCard, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.card)
	}
	return out, nil
}

func sampleCapabilityCard(item model.AttackSample) CapabilityCard {
	return CapabilityCard{
		SourceType:         CapabilitySourceSample,
		SourceExpertUserID: item.ExpertID.String(),
		SourceRef:          "sample:" + item.ID.String(),
		SourceVersion:      normalizeVersion(item.UpdatedAt.Format("20060102150405")),
		Name:               item.Name,
		Summary:            firstNonEmptyString(item.Description, "专家上传的原始测试问题样本，可与模板拼接后用于大模型安全评估。"),
		RiskTypes:          inferRiskTypes(item.SubType),
		TargetTypes:        []string{"llm"},
		Languages:          []string{"zh"},
		UseWhen:            []string{"需要原始问题、测试样本或与越狱模板组合时使用。"},
		DoNotUseWhen:       []string{"需要可直接执行的完整攻击载荷时，优先选择已组合攻击。"},
		InputsRequired:     []string{"template_ref 或直接执行策略", "test_count"},
		Outputs:            []string{"sample_question", "payload_handle"},
		Tags:               uniqueStrings([]string{"sample", item.SubType, "专家样本"}),
		Enabled:            isPublishedVisible(item.Status, item.Visibility),
		Status:             item.Status,
		SafeMetadata: map[string]string{
			"data_type":    CapabilitySourceSample,
			"category":     strings.TrimSpace(item.SubType),
			"sample_count": redteam.IntString(item.SampleCount),
		},
	}
}

func templateCapabilityCard(item model.Template) CapabilityCard {
	return CapabilityCard{
		SourceType:         CapabilitySourceTemplate,
		SourceExpertUserID: item.ExpertID.String(),
		SourceRef:          "template:" + item.ID.String(),
		SourceVersion:      normalizeVersion(item.UpdatedAt.Format("20060102150405")),
		Name:               item.Name,
		Summary:            firstNonEmptyString(item.Description, "专家上传的越狱模板，可通过 {{sample}} 或 {{question}} 占位符与样本拼接。"),
		RiskTypes:          inferRiskTypes(item.SubType),
		TargetTypes:        []string{"llm"},
		Languages:          []string{"zh"},
		UseWhen:            []string{"需要对原始样本进行角色扮演、指令注入、DAN 或格式混淆等包装时使用。"},
		DoNotUseWhen:       []string{"已有完整攻击载荷且不需要二次拼接时。"},
		InputsRequired:     []string{"sample_ref", "test_count"},
		Outputs:            []string{"payload_handle"},
		Tags:               uniqueStrings([]string{"template", item.SubType, "专家模板"}),
		Enabled:            isPublishedVisible(item.Status, item.Visibility),
		Status:             item.Status,
		SafeMetadata: map[string]string{
			"data_type": CapabilitySourceTemplate,
			"category":  strings.TrimSpace(item.SubType),
		},
	}
}

func composedAttackCapabilityCard(item model.ComposedAttack) CapabilityCard {
	return CapabilityCard{
		SourceType:         CapabilitySourceComposed,
		SourceExpertUserID: item.ExpertID.String(),
		SourceRef:          "composed_attack:" + item.ID.String(),
		SourceVersion:      normalizeVersion(item.UpdatedAt.Format("20060102150405")),
		Name:               item.Name,
		Summary:            firstNonEmptyString(item.Description, "专家上传的已组合攻击数据，可直接准备为 payload handle。"),
		RiskTypes:          inferRiskTypes(item.SubType),
		TargetTypes:        []string{"llm"},
		Languages:          []string{"zh"},
		UseWhen:            []string{"需要直接执行专家已组合好的攻击载荷时使用。"},
		DoNotUseWhen:       []string{"需要动态组合模板和样本时，选择 sample + template。"},
		InputsRequired:     []string{"test_count"},
		Outputs:            []string{"payload_handle"},
		Tags:               uniqueStrings([]string{"composed_attack", item.SubType, "已组合攻击"}),
		Enabled:            isPublishedVisible(item.Status, item.Visibility),
		Status:             item.Status,
		SafeMetadata: map[string]string{
			"data_type":    CapabilitySourceComposed,
			"category":     strings.TrimSpace(item.SubType),
			"sample_count": redteam.IntString(item.SampleCount),
		},
	}
}

func isPublishedVisible(status, visibility string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	visibility = strings.ToLower(strings.TrimSpace(visibility))
	return (status == "" || status == "published" || status == "active") && (visibility == "" || visibility == "public")
}

func inferRiskTypes(category string) []string {
	lower := strings.ToLower(strings.TrimSpace(category))
	risks := []string{"jailbreak"}
	if strings.Contains(lower, "prompt") || strings.Contains(category, "指令") || strings.Contains(category, "注入") {
		risks = append(risks, "prompt_injection")
	}
	if strings.Contains(lower, "dan") || strings.Contains(category, "DAN") {
		risks = append(risks, "jailbreak")
	}
	return uniqueStrings(risks)
}

func normalizeCapabilityLimit(limit int) int {
	if limit <= 0 {
		return DefaultCapabilityCatalogLimit
	}
	if limit > MaxCapabilityCatalogLimit {
		return MaxCapabilityCatalogLimit
	}
	return limit
}

func resourceCapabilityCard(item model.MaclawResourcePublication) CapabilityCard {
	metadata := safeCapabilityMetadata(item.Metadata)
	return CapabilityCard{
		SourceType:         CapabilitySourceResource,
		SourceExpertUserID: item.SourceExpertUserID.String(),
		SourceRef:          item.SourceResourceHandle,
		SourceVersion:      normalizeVersion(item.SourceVersion),
		Name:               item.Name,
		Summary:            item.Summary,
		RiskTypes:          capabilityListFromMetadata(metadata, "risk_types", item.AssessmentTypes),
		TargetTypes:        capabilityListFromMetadata(metadata, "target_types", []string{"llm"}),
		Languages:          capabilityListFromMetadata(metadata, "languages", nil),
		UseWhen:            capabilityListFromMetadata(metadata, "use_when", nil),
		DoNotUseWhen:       capabilityListFromMetadata(metadata, "do_not_use_when", nil),
		InputsRequired:     capabilityListFromMetadata(metadata, "inputs_required", nil),
		Outputs:            capabilityListFromMetadata(metadata, "outputs", []string{"payload_dataset"}),
		Tags:               append([]string(nil), item.Tags...),
		Enabled:            item.Enabled,
		Status:             item.Status,
		SafeMetadata:       metadata,
	}
}

func skillCapabilityCard(item model.MaclawSkillPublication) CapabilityCard {
	metadata := safeCapabilityMetadata(item.Metadata)
	return CapabilityCard{
		SourceType:         CapabilitySourceSkill,
		SourceExpertUserID: item.SourceExpertUserID.String(),
		SourceRef:          item.SourceSkillName,
		SourceVersion:      normalizeVersion(item.SourceVersion),
		Name:               firstNonEmptyString(item.Name, item.SourceSkillName),
		Summary:            item.Description,
		RiskTypes:          capabilityListFromMetadata(metadata, "risk_types", item.AssessmentTypes),
		TargetTypes:        capabilityListFromMetadata(metadata, "target_types", []string{"llm"}),
		Languages:          capabilityListFromMetadata(metadata, "languages", nil),
		UseWhen:            capabilityListFromMetadata(metadata, "use_when", item.Triggers),
		DoNotUseWhen:       capabilityListFromMetadata(metadata, "do_not_use_when", nil),
		InputsRequired:     capabilityListFromMetadata(metadata, "inputs_required", []string{"target_connection", "test_count"}),
		Outputs:            capabilityListFromMetadata(metadata, "outputs", []string{"payload_dataset"}),
		Tags:               uniqueStrings(append(append([]string{}, item.Tags...), item.Triggers...)),
		Enabled:            item.Enabled,
		Status:             item.Status,
		SafeMetadata:       metadata,
	}
}

func safeCapabilityMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range in {
		cleanKey := strings.ToLower(strings.TrimSpace(key))
		if cleanKey == "" || isSensitiveCapabilityMetadataKey(cleanKey) {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || looksSensitiveCapabilityValue(value) {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isSensitiveCapabilityMetadataKey(key string) bool {
	for _, marker := range []string{"payload", "prompt", "response", "request", "content", "body", "secret", "token", "credential", "archive", "zip", "path", "dir", "key"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func looksSensitiveCapabilityValue(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential")
}

func capabilityListFromMetadata(metadata map[string]string, key string, fallback []string) []string {
	values := append([]string(nil), fallback...)
	if metadata != nil {
		if raw := strings.TrimSpace(metadata[key]); raw != "" {
			values = append(values, parseCapabilityList(raw)...)
		}
	}
	return uniqueStrings(values)
}

func parseCapabilityList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var items []string
	if strings.HasPrefix(raw, "[") && json.Unmarshal([]byte(raw), &items) == nil {
		return items
	}
	replacer := strings.NewReplacer("，", ",", "、", ",", ";", ",", "|", ",")
	parts := strings.Split(replacer.Replace(raw), ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{raw}
	}
	return out
}

func scoreCapabilityCard(card CapabilityCard, q CapabilityCatalogQuery) int {
	haystack := strings.ToLower(strings.Join(append([]string{
		card.SourceType,
		card.SourceRef,
		card.Name,
		card.Summary,
		card.Status,
	}, append(append(append(append(append(append([]string{}, card.RiskTypes...), card.TargetTypes...), card.Languages...), card.UseWhen...), card.Outputs...), card.Tags...)...), " "))
	score := 0
	for _, token := range queryTokens(q.Query) {
		if strings.Contains(haystack, token) {
			score += 3
		}
		for _, alias := range capabilityAliases(token) {
			if strings.Contains(haystack, alias) {
				score += 4
			}
		}
	}
	score += scoreListOverlap(haystack, q.RiskTypes, 5)
	score += scoreListOverlap(haystack, q.TargetTypes, 4)
	score += scoreListOverlap(haystack, q.Languages, 4)
	if strings.Contains(haystack, "ccbos") && (strings.Contains(q.Query, "文言文") || strings.Contains(strings.ToLower(q.Query), "classical")) {
		score += 8
	}
	if score == 0 && !hasCapabilityQuery(q) {
		return 1
	}
	return score
}

func selectCapabilityResults(scored []scoredCapabilityCard, limit int, q CapabilityCatalogQuery) []scoredCapabilityCard {
	if len(scored) <= limit && !shouldEnsureSampleTemplateResults(q) {
		return scored
	}
	if !shouldEnsureSampleTemplateResults(q) {
		if len(scored) > limit {
			return scored[:limit]
		}
		return scored
	}
	selected := make([]scoredCapabilityCard, 0, limit)
	seen := map[string]bool{}
	add := func(item scoredCapabilityCard) {
		if len(selected) >= limit {
			return
		}
		key := item.card.SourceType + "\x00" + item.card.SourceRef
		if seen[key] {
			return
		}
		seen[key] = true
		selected = append(selected, item)
	}
	addBestSource := func(sourceType string) {
		for _, item := range scored {
			if item.card.SourceType == sourceType {
				add(item)
				return
			}
		}
	}
	addBestSource(CapabilitySourceSample)
	addBestSource(CapabilitySourceTemplate)
	for _, item := range scored {
		add(item)
	}
	return selected
}

func shouldEnsureSampleTemplateResults(q CapabilityCatalogQuery) bool {
	query := strings.ToLower(strings.TrimSpace(q.Query))
	for _, marker := range []string{"sample", "samples", "template", "templates", "compose", "combination", "jailbreak", "dan"} {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return false
}

func hasCapabilityQuery(q CapabilityCatalogQuery) bool {
	return strings.TrimSpace(q.Query) != "" || len(q.RiskTypes) > 0 || len(q.TargetTypes) > 0 || len(q.Languages) > 0
}

func queryTokens(query string) []string {
	lower := strings.ToLower(strings.TrimSpace(query))
	replacer := strings.NewReplacer("-", " ", "_", " ", "/", " ", "，", " ", "。", " ", "、", " ")
	parts := strings.Fields(replacer.Replace(lower))
	out := make([]string, 0, len(parts)+4)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) >= 2 {
			out = append(out, part)
		}
	}
	for _, phrase := range []string{
		"文言文", "越狱", "提示注入", "指令注入", "大模型",
		"角色身份扮演", "虚拟叙事保护", "系统指令注入", "dan模式",
		"游戏化包装", "双重人格回答", "邪恶ai召唤", "编码", "格式混淆",
	} {
		if strings.Contains(lower, phrase) {
			out = append(out, phrase)
		}
	}
	return uniqueStrings(out)
}

func capabilityAliases(token string) []string {
	switch token {
	case "文言文":
		return []string{"classical_chinese", "classical chinese", "古文"}
	case "越狱":
		return []string{"jailbreak"}
	case "提示注入", "指令注入", "系统指令注入":
		return []string{"prompt_injection", "prompt injection"}
	case "大模型":
		return []string{"llm"}
	case "角色身份扮演":
		return []string{"role play", "role_play"}
	case "编码", "格式混淆", "编码／格式混淆":
		return []string{"encoding", "format obfuscation", "encoding_evasion"}
	case "引擎评测", "第二引擎", "promptfoo":
		return []string{"promptfoo", "engine_plugin", "promptfoo_plugin"}
	case "有害内容", "有害":
		return []string{"harmful"}
	case "隐私泄露", "隐私":
		return []string{"pii", "privacy"}
	default:
		return nil
	}
}

func scoreListOverlap(haystack string, items []string, weight int) int {
	score := 0
	for _, item := range items {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" && strings.Contains(haystack, item) {
			score += weight
		}
	}
	return score
}

func uniqueStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}
