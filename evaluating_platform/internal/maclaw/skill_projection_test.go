package maclaw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

func TestSkillProjectionListsPublishedExpertSkillsForEnterpriseCatalog(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()
	service := NewSkillProjectionService(&fakeProjectionProvider{}, store, shadows)

	err := service.RecordExpertSkills(ctx, RuntimeIdentity{UserID: expertID.String(), Role: "expert"}, &GatewaySession{
		Mapping: &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}, []SkillSummary{{
		Name:        "ccbos-classical-chinese-skill",
		Description: "Generate classical Chinese jailbreak payloads",
		Triggers:    []string{"文言文", "越狱", "CCBOS"},
		Status:      "active",
		Source:      "zip_import",
		Version:     "1.0.0",
	}})
	if err != nil {
		t.Fatalf("RecordExpertSkills: %v", err)
	}

	items, err := service.ListEnterpriseCatalog(ctx, []SkillSummary{{Name: "own-skill", Status: "active"}}, SkillSearchInput{})
	if err != nil {
		t.Fatalf("ListEnterpriseCatalog: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v, want own + one published expert skill", items)
	}
	expertItem := items[1]
	if expertItem.Name != "ccbos-classical-chinese-skill" {
		t.Fatalf("expert item = %#v", expertItem)
	}
	if expertItem.Metadata["requires_shadow_copy"] != "true" || expertItem.Metadata["source_expert_user_id"] != expertID.String() {
		t.Fatalf("expert metadata = %#v", expertItem.Metadata)
	}
}

func TestSkillProjectionDeduplicatesInstalledHubSkillsFromEnterpriseCatalog(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	store := newFakeSkillPublicationStore()
	service := NewSkillProjectionService(&fakeProjectionProvider{}, store, newFakeSkillShadowStore())
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Description:        "Generate classical Chinese jailbreak payloads",
		Status:             "active",
		Enabled:            true,
		Metadata:           map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	list, err := service.ListEnterpriseCatalog(ctx, []SkillSummary{{
		Name:       "ccbos-classical-chinese-skill",
		Source:     "skillhub",
		HubSkillID: "hub-ccbos",
		Status:     "active",
		Metadata:   map[string]string{"hub_skill_id": "hub-ccbos"},
	}}, SkillSearchInput{})
	if err != nil {
		t.Fatalf("ListEnterpriseCatalog: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %#v, want installed skill without duplicate publication", list)
	}

	search, err := service.SearchEnterpriseCatalog(ctx, []SkillSearchResult{{
		Source:    "skillhub",
		ID:        "hub-ccbos",
		Name:      "ccbos-classical-chinese-skill",
		Installed: true,
		Metadata:  map[string]string{"hub_skill_id": "hub-ccbos"},
	}}, SkillSearchInput{Query: "ccbos"})
	if err != nil {
		t.Fatalf("SearchEnterpriseCatalog: %v", err)
	}
	if len(search) != 1 {
		t.Fatalf("search = %#v, want installed skill without duplicate publication", search)
	}
}

func TestSkillProjectionSearchMatchesSpaceSeparatedChineseAndEnglishTerms(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	store := newFakeSkillPublicationStore()
	service := NewSkillProjectionService(&fakeProjectionProvider{}, store, newFakeSkillShadowStore())
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Description:        "Generate classical Chinese jailbreak payloads",
		Status:             "active",
		Enabled:            true,
		Triggers:           []string{"文言文越狱", "CCBOS"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	items, err := service.SearchEnterpriseCatalog(ctx, nil, SkillSearchInput{Query: "文言文 越狱 CCBOS"})
	if err != nil {
		t.Fatalf("SearchEnterpriseCatalog: %v", err)
	}
	if len(items) != 1 || items[0].Name != "ccbos-classical-chinese-skill" {
		t.Fatalf("items = %#v", items)
	}
}

func TestSkillProjectionCreatesEnterpriseShadowSkillOnce(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	enterpriseID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()

	var installCount int
	var installed SkillInstallInput
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected target request %s %s", r.Method, r.URL.Path)
		}
		installCount++
		if err := json.NewDecoder(r.Body).Decode(&installed); err != nil {
			t.Fatalf("decode install input: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:    "ccbos-classical-chinese-skill",
			Status:  "active",
			Source:  "skillhub",
			Version: "1.0.0",
		}}})
	}))
	defer target.Close()

	targetClient, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	provider := &fakeProjectionProvider{sessions: map[string]*GatewaySession{
		enterpriseID.String(): {Client: targetClient, InstanceID: "inst_enterprise", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseID, MaclawTenantID: "tenant_enterprise"}},
	}}
	service := NewSkillProjectionService(provider, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID:   expertID,
		SourceMaclawTenantID: "tenant_expert",
		SourceSkillName:      "ccbos-classical-chinese-skill",
		SourceVersion:        "1.0.0",
		Name:                 "ccbos-classical-chinese-skill",
		Description:          "Generate classical Chinese jailbreak payloads",
		Status:               "active",
		Enabled:              true,
		Triggers:             []string{"文言文", "越狱", "CCBOS"},
		Metadata:             map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	if err := service.SyncEnterprisePublishedSkills(ctx, RuntimeIdentity{UserID: enterpriseID.String(), Role: "enterprise"}, provider.sessions[enterpriseID.String()]); err != nil {
		t.Fatalf("SyncEnterprisePublishedSkills: %v", err)
	}
	if installCount != 1 {
		t.Fatalf("installCount = %d, want 1", installCount)
	}
	if installed.Source != "skillhub" || installed.SkillHubURL != "https://hub.internal" || installed.SkillID != "hub-ccbos" || !installed.Overwrite {
		t.Fatalf("installed = %#v", installed)
	}

	if err := service.SyncEnterprisePublishedSkills(ctx, RuntimeIdentity{UserID: enterpriseID.String(), Role: "enterprise"}, provider.sessions[enterpriseID.String()]); err != nil {
		t.Fatalf("SyncEnterprisePublishedSkills second: %v", err)
	}
	if installCount != 1 {
		t.Fatalf("installCount after idempotent reuse = %d, want 1", installCount)
	}
}

func TestSkillProjectionInstallsOnlyLatestSameExpertSkillPublication(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	enterpriseID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()

	var installCount int
	var installed SkillInstallInput
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected target request %s %s", r.Method, r.URL.Path)
		}
		installCount++
		if err := json.NewDecoder(r.Body).Decode(&installed); err != nil {
			t.Fatalf("decode install input: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:    "ccbos-classical-chinese-skill",
			Status:  "active",
			Source:  DefaultHubSkillSource,
			Version: "2.0.0",
		}}})
	}))
	defer target.Close()

	targetClient, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	provider := &fakeProjectionProvider{sessions: map[string]*GatewaySession{
		enterpriseID.String(): {Client: targetClient, InstanceID: "inst_enterprise", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseID, MaclawTenantID: "tenant_enterprise"}},
	}}
	service := NewSkillProjectionService(provider, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Status:             "active",
		Enabled:            true,
		Metadata:           map[string]string{"hub_skill_id": "hub-ccbos-old"},
	}); err != nil {
		t.Fatalf("seed old publication: %v", err)
	}
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "2.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Status:             "active",
		Enabled:            true,
		Metadata:           map[string]string{"hub_skill_id": "hub-ccbos-new"},
	}); err != nil {
		t.Fatalf("seed new publication: %v", err)
	}

	if err := service.SyncEnterprisePublishedSkills(ctx, RuntimeIdentity{UserID: enterpriseID.String(), Role: "enterprise"}, provider.sessions[enterpriseID.String()]); err != nil {
		t.Fatalf("SyncEnterprisePublishedSkills: %v", err)
	}
	if installCount != 1 {
		t.Fatalf("installCount = %d, want latest publication installed once", installCount)
	}
	if installed.SkillID != "hub-ccbos-new" {
		t.Fatalf("installed = %#v, want latest hub skill", installed)
	}
	if shadow, err := shadows.GetSkillShadow(ctx, enterpriseID, expertID, "ccbos-classical-chinese-skill", "1.0.0"); err != nil {
		t.Fatalf("GetSkillShadow old: %v", err)
	} else if shadow != nil {
		t.Fatalf("old shadow = %#v, want no old publication shadow", shadow)
	}
	if shadow, err := shadows.GetSkillShadow(ctx, enterpriseID, expertID, "ccbos-classical-chinese-skill", "2.0.0"); err != nil {
		t.Fatalf("GetSkillShadow new: %v", err)
	} else if shadow == nil {
		t.Fatalf("new shadow = nil, want latest publication shadow")
	}
}

func TestSkillProjectionDoesNotReuseShadowFromPreviousEnterpriseTenant(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	enterpriseID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()
	if err := shadows.UpsertSkillShadow(ctx, &model.MaclawSkillShadow{
		EnterpriseUserID:         enterpriseID,
		EnterpriseMaclawTenantID: "tenant_old",
		SourceExpertUserID:       expertID,
		SourceSkillName:          "ccbos-classical-chinese-skill",
		SourceVersion:            "1.0.0",
		ShadowSkillName:          "ccbos-classical-chinese-skill",
		SyncStatus:               "ready",
	}); err != nil {
		t.Fatalf("seed stale shadow: %v", err)
	}

	var installCount int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected target request %s %s", r.Method, r.URL.Path)
		}
		installCount++
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:    "ccbos-classical-chinese-skill",
			Status:  "active",
			Source:  DefaultHubSkillSource,
			Version: "1.0.0",
		}}})
	}))
	defer target.Close()
	targetClient, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	service := NewSkillProjectionService(&fakeProjectionProvider{}, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID:   expertID,
		SourceMaclawTenantID: "tenant_expert",
		SourceSkillName:      "ccbos-classical-chinese-skill",
		SourceVersion:        "1.0.0",
		Name:                 "ccbos-classical-chinese-skill",
		Status:               "active",
		Enabled:              true,
		Metadata:             map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	session := &GatewaySession{Client: targetClient, Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseID, MaclawTenantID: "tenant_new"}}
	if err := service.SyncEnterprisePublishedHubSkillsBestEffort(ctx, RuntimeIdentity{UserID: enterpriseID.String(), Role: "enterprise"}, session); err != nil {
		t.Fatalf("SyncEnterprisePublishedHubSkillsBestEffort: %v", err)
	}
	if installCount != 1 {
		t.Fatalf("installCount = %d, want stale tenant shadow to be refreshed", installCount)
	}
	shadow, err := shadows.GetSkillShadow(ctx, enterpriseID, expertID, "ccbos-classical-chinese-skill", "1.0.0")
	if err != nil {
		t.Fatalf("GetSkillShadow: %v", err)
	}
	if shadow == nil || shadow.EnterpriseMaclawTenantID != "tenant_new" {
		t.Fatalf("shadow = %#v, want refreshed tenant_new shadow", shadow)
	}
}

func TestSkillProjectionDoesNotReuseShadowFromDifferentExpertWithSameSkillName(t *testing.T) {
	ctx := context.Background()
	oldExpertID := uuid.New()
	newExpertID := uuid.New()
	enterpriseID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()
	if err := shadows.UpsertSkillShadow(ctx, &model.MaclawSkillShadow{
		EnterpriseUserID:   enterpriseID,
		SourceExpertUserID: oldExpertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		ShadowSkillName:    "ccbos-classical-chinese-skill",
		SyncStatus:         "ready",
	}); err != nil {
		t.Fatalf("seed old shadow: %v", err)
	}

	var installCount int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected target request %s %s", r.Method, r.URL.Path)
		}
		installCount++
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:    "ccbos-classical-chinese-skill",
			Status:  "active",
			Source:  "skillhub",
			Version: "1.0.0",
		}}})
	}))
	defer target.Close()

	targetClient, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	provider := &fakeProjectionProvider{sessions: map[string]*GatewaySession{
		enterpriseID.String(): {Client: targetClient, InstanceID: "inst_enterprise", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseID, MaclawTenantID: "tenant_enterprise"}},
	}}
	service := NewSkillProjectionService(provider, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: newExpertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Status:             "active",
		Enabled:            true,
		Metadata:           map[string]string{"hub_skill_id": "hub-ccbos-new"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	if err := service.SyncEnterprisePublishedSkills(ctx, RuntimeIdentity{UserID: enterpriseID.String(), Role: "enterprise"}, provider.sessions[enterpriseID.String()]); err != nil {
		t.Fatalf("SyncEnterprisePublishedSkills: %v", err)
	}
	if installCount != 1 {
		t.Fatalf("installCount = %d, want install for the new expert publication", installCount)
	}
}

func TestSkillProjectionPrepareSkipsOlderPublicationWithoutHubID(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	enterpriseID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()

	var installed SkillInstallInput
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected target request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&installed); err != nil {
			t.Fatalf("decode install input: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:    "ccbos-classical-chinese-skill",
			Status:  "active",
			Source:  "skillhub",
			Version: "1.0.0",
		}}})
	}))
	defer target.Close()
	targetClient, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	provider := &fakeProjectionProvider{sessions: map[string]*GatewaySession{
		enterpriseID.String(): {Client: targetClient, InstanceID: "inst_enterprise", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseID, MaclawTenantID: "tenant_enterprise"}},
	}}
	service := NewSkillProjectionService(provider, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "legacy",
		Name:               "ccbos-classical-chinese-skill",
		Status:             "active",
		Enabled:            true,
	}); err != nil {
		t.Fatalf("seed legacy publication: %v", err)
	}
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Status:             "active",
		Enabled:            true,
		Metadata:           map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed hub publication: %v", err)
	}

	if err := service.PrepareEnterprisePublishedSkills(ctx, RuntimeIdentity{UserID: enterpriseID.String(), Role: "enterprise"}, provider.sessions[enterpriseID.String()], []string{"ccbos-classical-chinese-skill"}); err != nil {
		t.Fatalf("PrepareEnterprisePublishedSkills: %v", err)
	}
	if installed.SkillID != "hub-ccbos" {
		t.Fatalf("installed = %#v", installed)
	}
}

func TestSkillProjectionInstallsPublishedHubSkillsForAllEnterpriseMappings(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	enterpriseA := uuid.New()
	enterpriseB := uuid.New()
	expertMappingID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()

	installCounts := map[string]int{}
	newInstallClient := func(userID string) GatewayClient {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
				t.Fatalf("unexpected install request %s %s", r.Method, r.URL.Path)
			}
			var in SkillInstallInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode install input: %v", err)
			}
			if in.Source != DefaultHubSkillSource || in.SkillHubURL != "https://hub.internal" || in.SkillID != "hub-ccbos" || !in.Overwrite {
				t.Fatalf("install input = %#v", in)
			}
			installCounts[userID]++
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
				Name:    "ccbos-classical-chinese-skill",
				Status:  "active",
				Source:  "skillhub",
				Version: "1.0.0",
			}}})
		}))
		t.Cleanup(target.Close)
		client, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
		if err != nil {
			t.Fatalf("target client: %v", err)
		}
		return client
	}
	provider := &fakeProjectionProvider{sessions: map[string]*GatewaySession{
		enterpriseA.String():     {Client: newInstallClient(enterpriseA.String()), InstanceID: "inst_enterprise_a", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseA, PlatformRole: model.RoleEnterprise, MaclawTenantID: "tenant_enterprise_a"}},
		enterpriseB.String():     {Client: newInstallClient(enterpriseB.String()), InstanceID: "inst_enterprise_b", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseB, PlatformRole: model.RoleEnterprise, MaclawTenantID: "tenant_enterprise_b"}},
		expertMappingID.String(): {Client: newInstallClient(expertMappingID.String()), InstanceID: "inst_expert", Mapping: &model.MaclawAccountMapping{PlatformUserID: expertMappingID, PlatformRole: model.RoleExpert, MaclawTenantID: "tenant_expert_other"}},
	}}
	service := NewSkillProjectionService(provider, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	service.SetAccountMappingStore(fakeSkillAccountMappingStore{items: []model.MaclawAccountMapping{
		{PlatformUserID: enterpriseA, PlatformRole: model.RoleEnterprise, ProvisioningStatus: model.MaclawProvisioningReady},
		{PlatformUserID: enterpriseB, PlatformRole: model.RoleEnterprise, ProvisioningStatus: model.MaclawProvisioningReady},
		{PlatformUserID: expertMappingID, PlatformRole: model.RoleExpert, ProvisioningStatus: model.MaclawProvisioningReady},
	}})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID:   expertID,
		SourceMaclawTenantID: "tenant_expert",
		SourceSkillName:      "ccbos-classical-chinese-skill",
		SourceVersion:        "1.0.0",
		Name:                 "ccbos-classical-chinese-skill",
		Description:          "Generate classical Chinese jailbreak payloads",
		Status:               "active",
		Enabled:              true,
		Triggers:             []string{"CCBOS"},
		Metadata:             map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	if err := service.SyncPublishedSkillsToAllEnterpriseMappings(ctx); err != nil {
		t.Fatalf("SyncPublishedSkillsToAllEnterpriseMappings: %v", err)
	}
	if installCounts[enterpriseA.String()] != 1 || installCounts[enterpriseB.String()] != 1 {
		t.Fatalf("installCounts = %#v, want each enterprise installed once", installCounts)
	}
	if installCounts[expertMappingID.String()] != 0 {
		t.Fatalf("expert install count = %d, want 0", installCounts[expertMappingID.String()])
	}

	if err := service.SyncPublishedSkillsToAllEnterpriseMappings(ctx); err != nil {
		t.Fatalf("SyncPublishedSkillsToAllEnterpriseMappings second: %v", err)
	}
	if installCounts[enterpriseA.String()] != 1 || installCounts[enterpriseB.String()] != 1 {
		t.Fatalf("installCounts after second sync = %#v, want idempotent reuse", installCounts)
	}
}

func TestSkillProjectionContinuesEnterpriseSyncAfterStaleMapping(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	staleEnterprise := uuid.New()
	readyEnterprise := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()

	var installCount int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected install request %s %s", r.Method, r.URL.Path)
		}
		installCount++
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:    "ccbos-classical-chinese-skill",
			Status:  "active",
			Source:  "skillhub",
			Version: "1.0.0",
		}}})
	}))
	defer target.Close()
	client, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	provider := &fakeProjectionProvider{sessions: map[string]*GatewaySession{
		readyEnterprise.String(): {Client: client, InstanceID: "inst_ready", Mapping: &model.MaclawAccountMapping{PlatformUserID: readyEnterprise, PlatformRole: model.RoleEnterprise, MaclawTenantID: "tenant_ready"}},
	}}
	service := NewSkillProjectionService(provider, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	service.SetAccountMappingStore(fakeSkillAccountMappingStore{items: []model.MaclawAccountMapping{
		{PlatformUserID: staleEnterprise, PlatformRole: model.RoleEnterprise, ProvisioningStatus: model.MaclawProvisioningReady},
		{PlatformUserID: readyEnterprise, PlatformRole: model.RoleEnterprise, ProvisioningStatus: model.MaclawProvisioningReady},
	}})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Status:             "active",
		Enabled:            true,
		Metadata:           map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	if err := service.SyncPublishedSkillsToAllEnterpriseMappings(ctx); err == nil {
		t.Fatalf("SyncPublishedSkillsToAllEnterpriseMappings error = nil, want partial failure")
	}
	if installCount != 1 {
		t.Fatalf("installCount = %d, want ready enterprise still installed", installCount)
	}
}

func TestSkillProjectionNativeCatalogProfileFallsBackToShadowUntilGrantSyncExists(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	enterpriseID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()

	var installCount int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected target request %s %s", r.Method, r.URL.Path)
		}
		installCount++
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:    "ccbos-classical-chinese-skill",
			Status:  "active",
			Source:  "skillhub",
			Version: "1.0.0",
		}}})
	}))
	defer target.Close()

	targetClient, err := NewClient(Config{BaseURL: target.URL, APIToken: "target-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	provider := &fakeProjectionProvider{sessions: map[string]*GatewaySession{
		enterpriseID.String(): {Client: targetClient, InstanceID: "inst_enterprise", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseID, MaclawTenantID: "tenant_enterprise"}},
	}}
	service := NewSkillProjectionService(provider, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	service.SetRuntimeCapabilities(RuntimeCapabilities{
		Profile:                     RuntimeCapabilityProfileNativeCatalog,
		NativeSkillCatalog:          true,
		NativeCrossTenantReferences: true,
	})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID:   expertID,
		SourceMaclawTenantID: "tenant_expert",
		SourceSkillName:      "ccbos-classical-chinese-skill",
		SourceVersion:        "1.0.0",
		Name:                 "ccbos-classical-chinese-skill",
		Description:          "Generate classical Chinese jailbreak payloads",
		Status:               "active",
		Enabled:              true,
		Triggers:             []string{"CCBOS"},
		Metadata:             map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	if err := service.SyncEnterprisePublishedSkills(ctx, RuntimeIdentity{UserID: enterpriseID.String(), Role: "enterprise"}, provider.sessions[enterpriseID.String()]); err != nil {
		t.Fatalf("SyncEnterprisePublishedSkills: %v", err)
	}
	if installCount != 1 {
		t.Fatalf("installCount = %d, want fallback hub install once", installCount)
	}
}

func TestSkillProjectionBackfillsExpertHubSkillAfterTenantChanges(t *testing.T) {
	ctx := context.Background()
	expertID := uuid.New()
	store := newFakeSkillPublicationStore()
	shadows := newFakeSkillShadowStore()

	var installCount int
	var installed SkillInstallInput
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("unexpected target request %s %s", r.Method, r.URL.Path)
		}
		installCount++
		if err := json.NewDecoder(r.Body).Decode(&installed); err != nil {
			t.Fatalf("decode install input: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []SkillSummary{{
			Name:        "ccbos-classical-chinese-skill",
			Description: "Generate classical Chinese jailbreak payloads",
			Status:      "active",
			Source:      DefaultHubSkillSource,
			Version:     "1.0.0",
			HubSkillID:  "hub-ccbos",
			Metadata:    map[string]string{"hub_skill_id": "hub-ccbos"},
		}}})
	}))
	defer target.Close()

	targetClient, err := NewClient(Config{BaseURL: target.URL, APIToken: "expert-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("target client: %v", err)
	}
	service := NewSkillProjectionService(&fakeProjectionProvider{}, store, shadows)
	service.SetHubConfigProvider(fakeSkillProjectionHubConfig{url: "https://hub.internal"})
	if err := store.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
		SourceExpertUserID:   expertID,
		SourceMaclawTenantID: "tenant_old",
		SourceSkillName:      "ccbos-classical-chinese-skill",
		SourceVersion:        "1.0.0",
		Name:                 "ccbos-classical-chinese-skill",
		Status:               "active",
		Enabled:              true,
		Metadata:             map[string]string{"hub_skill_id": "hub-ccbos"},
	}); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	session := &GatewaySession{Client: targetClient, Mapping: &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_new"}}
	if err := service.SyncExpertPublishedHubSkillsBestEffort(ctx, RuntimeIdentity{UserID: expertID.String(), Role: "expert"}, session); err != nil {
		t.Fatalf("SyncExpertPublishedHubSkillsBestEffort: %v", err)
	}
	if installCount != 1 {
		t.Fatalf("installCount = %d, want 1", installCount)
	}
	if installed.Source != DefaultHubSkillSource || installed.SkillHubURL != "https://hub.internal" || installed.SkillID != "hub-ccbos" || !installed.Overwrite {
		t.Fatalf("installed = %#v", installed)
	}
	items, err := store.ListPublishedSkills(ctx, SkillSearchInput{Query: "ccbos"})
	if err != nil {
		t.Fatalf("ListPublishedSkills: %v", err)
	}
	if len(items) != 1 || items[0].SourceMaclawTenantID != "tenant_new" {
		t.Fatalf("items = %#v, want publication moved to tenant_new", items)
	}
}

type fakeSkillPublicationStore struct {
	items map[string]*model.MaclawSkillPublication
}

func newFakeSkillPublicationStore() *fakeSkillPublicationStore {
	return &fakeSkillPublicationStore{items: make(map[string]*model.MaclawSkillPublication)}
}

func (s *fakeSkillPublicationStore) UpsertSkillPublication(_ context.Context, item *model.MaclawSkillPublication) error {
	now := time.Now()
	copied := *item
	copied.UpdatedAt = now
	if copied.CreatedAt.IsZero() {
		copied.CreatedAt = now
	}
	s.items[copied.SourceSkillName+"|"+copied.SourceVersion] = &copied
	return nil
}

func (s *fakeSkillPublicationStore) MarkSkillPublicationUnavailable(_ context.Context, sourceExpertUserID uuid.UUID, sourceSkillName string) error {
	for _, item := range s.items {
		if item.SourceExpertUserID == sourceExpertUserID && item.SourceSkillName == sourceSkillName {
			item.Enabled = false
			item.Status = "disabled"
		}
	}
	return nil
}

func (s *fakeSkillPublicationStore) ListPublishedSkills(_ context.Context, q SkillSearchInput) ([]model.MaclawSkillPublication, error) {
	out := make([]model.MaclawSkillPublication, 0, len(s.items))
	for _, item := range s.items {
		if item.Enabled && item.Status == "active" {
			out = append(out, *item)
		}
	}
	return out, nil
}

type fakeSkillShadowStore struct {
	items map[string]*model.MaclawSkillShadow
}

type fakeSkillAccountMappingStore struct {
	items []model.MaclawAccountMapping
}

func (s fakeSkillAccountMappingStore) List(_ context.Context, limit, offset int) ([]model.MaclawAccountMapping, error) {
	if offset >= len(s.items) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.items) {
		end = len(s.items)
	}
	return append([]model.MaclawAccountMapping(nil), s.items[offset:end]...), nil
}

type fakeSkillProjectionHubConfig struct {
	url string
}

func (p fakeSkillProjectionHubConfig) GetHubConfig(context.Context) (*MaclawHubConfig, error) {
	return &MaclawHubConfig{HubURL: p.url, Enabled: true, AllowedSources: []string{DefaultHubSkillSource}}, nil
}

func newFakeSkillShadowStore() *fakeSkillShadowStore {
	return &fakeSkillShadowStore{items: make(map[string]*model.MaclawSkillShadow)}
}

func (s *fakeSkillShadowStore) GetSkillShadow(_ context.Context, enterpriseUserID uuid.UUID, sourceExpertUserID uuid.UUID, sourceSkillName, sourceVersion string) (*model.MaclawSkillShadow, error) {
	item := s.items[enterpriseUserID.String()+"|"+sourceExpertUserID.String()+"|"+sourceSkillName+"|"+sourceVersion]
	if item == nil {
		return nil, nil
	}
	copied := *item
	return &copied, nil
}

func (s *fakeSkillShadowStore) UpsertSkillShadow(_ context.Context, item *model.MaclawSkillShadow) error {
	copied := *item
	s.items[item.EnterpriseUserID.String()+"|"+item.SourceExpertUserID.String()+"|"+item.SourceSkillName+"|"+item.SourceVersion] = &copied
	return nil
}
