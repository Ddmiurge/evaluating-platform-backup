package maclaw

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

func TestCapabilityCatalogSearchesPublishedResourcesAndSkills(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	resources := &fakeCapabilityResourceStore{items: []model.MaclawResourcePublication{
		{
			SourceExpertUserID:   expertID,
			SourceResourceID:     "res_prompt",
			SourceResourceHandle: "expert_handle",
			SourceVersion:        "v1",
			Name:                 "Prompt injection samples",
			Kind:                 string(EvaluationResourceKindSample),
			Status:               string(EvaluationResourceStatusPublished),
			Enabled:              true,
			Summary:              "Samples for prompt injection and jailbreak tests",
			AssessmentTypes:      []string{"prompt_injection"},
			Tags:                 []string{"llm", "jailbreak"},
			Metadata: map[string]string{
				"payload":           "SECRET_PAYLOAD",
				"credential_secret": "SECRET",
				"languages":         "zh,en",
				"use_when":          "Need prompt injection samples",
			},
		},
		{
			SourceExpertUserID:   expertID,
			SourceResourceID:     "res_disabled",
			SourceResourceHandle: "disabled_handle",
			SourceVersion:        "v1",
			Name:                 "Disabled resource",
			Status:               string(EvaluationResourceStatusArchived),
			Enabled:              false,
		},
	}}
	skills := &fakeCapabilitySkillStore{items: []model.MaclawSkillPublication{
		{
			SourceExpertUserID: expertID,
			SourceSkillName:    "ccbos-classical-chinese-skill",
			SourceVersion:      "1.0.0",
			Name:               "CCBOS classical Chinese jailbreak",
			Description:        "Generate classical Chinese jailbreak payloads for LLM red-team tests",
			Status:             "active",
			Enabled:            true,
			Triggers:           []string{"文言文", "越狱", "CCBOS", "classical chinese", "jailbreak"},
			Tags:               []string{"jailbreak", "classical_chinese", "llm"},
			AssessmentTypes:    []string{"jailbreak"},
			Metadata: map[string]string{
				"archive_base64": "SECRET_ARCHIVE",
				"token":          "SECRET_TOKEN",
				"outputs":        "payload_dataset",
			},
		},
	}}
	service := NewCapabilityCatalogService(resources, skills)

	cards, err := service.Search(ctx, CapabilityCatalogQuery{Query: "请用文言文越狱测试我的目标LLM", Limit: 5})
	if err != nil {
		t.Fatalf("search capability catalog: %v", err)
	}
	if len(cards) == 0 {
		t.Fatal("expected at least one capability card")
	}
	if cards[0].SourceType != CapabilitySourceSkill || cards[0].SourceRef != "ccbos-classical-chinese-skill" {
		t.Fatalf("top card = %#v, want CCBOS skill", cards[0])
	}
	for _, card := range cards {
		if card.SourceRef == "disabled_handle" || card.Name == "Disabled resource" {
			t.Fatalf("disabled capability leaked into search results: %#v", card)
		}
		body := strings.ToLower(card.Name + " " + card.Summary + " " + strings.Join(card.Tags, " ") + " " + strings.Join(mapValues(card.SafeMetadata), " "))
		for _, forbidden := range []string{"secret_payload", "secret_archive", "secret_token", "credential_secret", "archive_base64"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("capability card leaked %q: %#v", forbidden, card)
			}
		}
	}
}

func TestCapabilityCatalogDefaultsAndCapsLimit(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	store := &fakeCapabilitySkillStore{}
	for i := 0; i < 12; i++ {
		store.items = append(store.items, model.MaclawSkillPublication{
			SourceExpertUserID: expertID,
			SourceSkillName:    "skill-" + string(rune('a'+i)),
			SourceVersion:      "v1",
			Name:               "Skill " + string(rune('a'+i)),
			Description:        "LLM red team jailbreak helper",
			Status:             "active",
			Enabled:            true,
			Tags:               []string{"jailbreak", "llm"},
		})
	}
	service := NewCapabilityCatalogService(nil, store)

	cards, err := service.Search(ctx, CapabilityCatalogQuery{Query: "jailbreak llm"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(cards) != DefaultCapabilityCatalogLimit {
		t.Fatalf("default limit returned %d cards, want %d", len(cards), DefaultCapabilityCatalogLimit)
	}

	cards, err = service.Search(ctx, CapabilityCatalogQuery{Query: "jailbreak llm", Limit: 100})
	if err != nil {
		t.Fatalf("search with large limit: %v", err)
	}
	if len(cards) != MaxCapabilityCatalogLimit {
		t.Fatalf("max limit returned %d cards, want %d", len(cards), MaxCapabilityCatalogLimit)
	}
}

func TestCapabilityCatalogExposesExpertDataSemantics(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	samples := &fakePlatformSampleStore{items: []model.AttackSample{{
		ID:          uuid.New(),
		ExpertID:    expertID,
		SubType:     "原始问题",
		Name:        "原始风险问题样本",
		Description: "用于和越狱模板拼接的原始问题",
		SampleCount: 3,
		Status:      "published",
		Visibility:  "public",
	}}}
	templates := &fakePlatformTemplateStore{items: []model.Template{{
		ID:          uuid.New(),
		ExpertID:    expertID,
		SubType:     "角色身份扮演",
		Name:        "角色身份扮演模板",
		Description: "使用 {{sample}} 包装原始问题",
		Status:      "published",
		Visibility:  "public",
	}}}
	composed := &fakePlatformComposedStore{items: []model.ComposedAttack{{
		ID:          uuid.New(),
		ExpertID:    expertID,
		SubType:     "已组合攻击",
		Name:        "已组合文言文越狱攻击",
		Description: "可直接执行的已组合攻击载荷",
		SampleCount: 2,
		Status:      "published",
		Visibility:  "public",
	}}}
	service := NewCapabilityCatalogService(nil, nil)
	service.SetPlatformDataStores(samples, templates, composed)

	cards, err := service.Search(ctx, CapabilityCatalogQuery{Query: "角色身份扮演 样本 已组合攻击", Limit: 8})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	seen := map[string]bool{}
	for _, card := range cards {
		seen[card.SourceType] = true
		if strings.Contains(strings.Join(mapValues(card.SafeMetadata), " "), "payload") || strings.Contains(card.SourceRef, "storage") {
			t.Fatalf("data capability leaked unsafe details: %#v", card)
		}
	}
	for _, want := range []string{CapabilitySourceSample, CapabilitySourceTemplate, CapabilitySourceComposed} {
		if !seen[want] {
			t.Fatalf("missing %s capability in %#v", want, cards)
		}
	}
}

func TestCapabilityCatalogKeepsSampleAndTemplateForCompositionQueries(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	samples := &fakePlatformSampleStore{items: []model.AttackSample{{
		ID:          uuid.New(),
		ExpertID:    expertID,
		SubType:     "jailbreak_question",
		Name:        "Original risky questions",
		Description: "Base samples for template composition",
		SampleCount: 4,
		Status:      "published",
		Visibility:  "public",
	}}}
	templates := &fakePlatformTemplateStore{}
	for i := 0; i < 6; i++ {
		templates.items = append(templates.items, model.Template{
			ID:          uuid.New(),
			ExpertID:    expertID,
			SubType:     "DAN模式",
			Name:        "DAN jailbreak template",
			Description: "DAN template for LLM jailbreak composition",
			Status:      "published",
			Visibility:  "public",
		})
	}
	service := NewCapabilityCatalogService(nil, nil)
	service.SetPlatformDataStores(samples, templates, nil)

	cards, err := service.Search(ctx, CapabilityCatalogQuery{Query: "DAN jailbreak sample template composition", Limit: 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	seen := map[string]bool{}
	for _, card := range cards {
		seen[card.SourceType] = true
	}
	if !seen[CapabilitySourceSample] || !seen[CapabilitySourceTemplate] {
		t.Fatalf("composition search should keep both sample and template cards: %#v", cards)
	}
}

type fakePlatformSampleStore struct {
	items []model.AttackSample
}

func (s *fakePlatformSampleStore) ListPublished(_ context.Context, _ string, limit, _ int) ([]model.AttackSample, int, error) {
	out := s.items
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, len(s.items), nil
}

type fakePlatformTemplateStore struct {
	items []model.Template
}

func (s *fakePlatformTemplateStore) ListPublished(_ context.Context, _ string, limit, _ int) ([]model.Template, int, error) {
	out := s.items
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, len(s.items), nil
}

type fakePlatformComposedStore struct {
	items []model.ComposedAttack
}

func (s *fakePlatformComposedStore) ListPublished(_ context.Context, _ string, limit, _ int) ([]model.ComposedAttack, int, error) {
	out := s.items
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, len(s.items), nil
}

type fakeCapabilityResourceStore struct {
	items []model.MaclawResourcePublication
}

func (s *fakeCapabilityResourceStore) UpsertPublication(context.Context, *model.MaclawResourcePublication) error {
	return nil
}

func (s *fakeCapabilityResourceStore) MarkPublicationUnavailable(context.Context, uuid.UUID, string) error {
	return nil
}

func (s *fakeCapabilityResourceStore) ListPublished(_ context.Context, q EvaluationResourceQuery) ([]model.MaclawResourcePublication, error) {
	var out []model.MaclawResourcePublication
	for _, item := range s.items {
		if !item.Enabled || item.Status != string(EvaluationResourceStatusPublished) {
			continue
		}
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *fakeCapabilityResourceStore) GetPublishedByHandle(context.Context, string) (*model.MaclawResourcePublication, error) {
	return nil, nil
}

type fakeCapabilitySkillStore struct {
	items []model.MaclawSkillPublication
}

func (s *fakeCapabilitySkillStore) UpsertSkillPublication(context.Context, *model.MaclawSkillPublication) error {
	return nil
}

func (s *fakeCapabilitySkillStore) MarkSkillPublicationUnavailable(context.Context, uuid.UUID, string) error {
	return nil
}

func (s *fakeCapabilitySkillStore) ListPublishedSkills(_ context.Context, q SkillSearchInput) ([]model.MaclawSkillPublication, error) {
	var out []model.MaclawSkillPublication
	for _, item := range s.items {
		if !item.Enabled || !skillIsPublished(SkillSummary{Status: item.Status}) {
			continue
		}
		if q.TopN > 0 && len(out) >= q.TopN {
			break
		}
		out = append(out, item)
	}
	return out, nil
}

func mapValues(in map[string]string) []string {
	out := make([]string, 0, len(in))
	for _, value := range in {
		out = append(out, value)
	}
	return out
}

// A published package must reach a planner as a ref the engine can resolve.
//
// The card used to carry the bare resource handle, so a planner relaying it into
// `plan_confirm` produced a ref nothing downstream recognised — the package was
// publishable, executable, and unreachable from the product (2026-10-23).
func TestResourceCapabilityCardGivesPackagesTheirEngineRef(t *testing.T) {
	expert := uuid.New()
	cases := []struct {
		kind     string
		handle   string
		wantType string
		wantRef  string
	}{
		{"pf_plugin", "gen-1", CapabilitySourcePromptfooPlugin, "pf_plugin:gen-1"},
		{"pf_strategy", "wrap-1", CapabilitySourcePromptfooStrategy, "pf_strategy:wrap-1"},
		{"sample", "gen-1", CapabilitySourceResource, "gen-1"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			card := resourceCapabilityCard(model.MaclawResourcePublication{
				SourceExpertUserID:   expert,
				SourceResourceHandle: tc.handle,
				SourceResourceID:     "res-1",
				Kind:                 tc.kind,
				Name:                 "包",
				Status:               "published",
				Enabled:              true,
			})
			if card.SourceType != tc.wantType {
				t.Errorf("source_type = %q, want %q", card.SourceType, tc.wantType)
			}
			if card.SourceRef != tc.wantRef {
				t.Errorf("source_ref = %q, want %q", card.SourceRef, tc.wantRef)
			}
		})
	}
}
