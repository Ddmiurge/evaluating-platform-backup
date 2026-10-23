package maclaw

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/config"
)

func TestResourceStoreServiceSavesSafeSummaryAndMaterializesServerSide(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	store := newFakeResourceStore()
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	service := NewResourceStoreService(store, keyStore)

	summary, err := service.SaveResource(ctx, userID, "tenant_1", EvaluationResourceInput{
		Name:         "Classical jailbreak samples",
		Kind:         EvaluationResourceKindSample,
		Version:      "v1",
		Status:       EvaluationResourceStatusPublished,
		Enabled:      true,
		Summary:      "Classical Chinese jailbreak prompts",
		Payload:      "SECRET_PAYLOAD_SHOULD_NOT_LEAK",
		Tags:         []string{"jailbreak", "classical_chinese"},
		Metadata:     map[string]string{"local_path": "C:/secret/path", "source": "expert"},
		HealthStatus: EvaluationResourceHealthHealthy,
	})
	if err != nil {
		t.Fatalf("SaveResource: %v", err)
	}
	if summary.Metadata["local_path"] != "" {
		t.Fatalf("summary leaked local path: %#v", summary.Metadata)
	}
	if store.last.EncryptedPayload == nil || strings.Contains(string(store.last.EncryptedPayload), "SECRET_PAYLOAD") {
		t.Fatalf("payload was not encrypted at rest: %q", string(store.last.EncryptedPayload))
	}

	materialized, err := service.MaterializeResource(ctx, userID, summary.Handle)
	if err != nil {
		t.Fatalf("MaterializeResource: %v", err)
	}
	if materialized.Payload != "SECRET_PAYLOAD_SHOULD_NOT_LEAK" {
		t.Fatalf("unexpected materialized payload: %#v", materialized)
	}
	if materialized.Metadata["local_path"] != "" {
		t.Fatalf("materialized metadata leaked local path: %#v", materialized.Metadata)
	}
}

func TestPlatformResourceGatewayOverridesUnsupportedRuntimeResourceAPIs(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	store := newFakeResourceStore()
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	service := NewResourceStoreService(store, keyStore)
	gateway := NewPlatformResourceGateway(nil, service, userID, "tenant_1")

	summary, err := gateway.SaveEvaluationResource(ctx, EvaluationResourceInput{
		Name:    "Prompt template",
		Kind:    EvaluationResourceKindTemplate,
		Enabled: true,
		Payload: "template body",
	})
	if err != nil {
		t.Fatalf("SaveEvaluationResource: %v", err)
	}
	items, err := gateway.SearchEvaluationResources(ctx, EvaluationResourceQuery{Query: "prompt"})
	if err != nil {
		t.Fatalf("SearchEvaluationResources: %v", err)
	}
	if len(items) != 1 || items[0].ID != summary.ID {
		t.Fatalf("unexpected search items: %#v", items)
	}
	preview, err := gateway.PreviewEvaluationResource(ctx, summary.ID)
	if err != nil {
		t.Fatalf("PreviewEvaluationResource: %v", err)
	}
	if preview.PayloadSHA256 == "" || preview.PayloadBytes == 0 {
		t.Fatalf("preview missing safe payload stats: %#v", preview)
	}
	materialized, err := gateway.MaterializeEvaluationResource(ctx, summary.Handle)
	if err != nil {
		t.Fatalf("MaterializeEvaluationResource: %v", err)
	}
	if materialized.Payload != "template body" {
		t.Fatalf("unexpected payload: %#v", materialized)
	}
}

type fakeResourceStore struct {
	items map[string]model.MaclawResourceRecord
	last  model.MaclawResourceRecord
}

func newFakeResourceStore() *fakeResourceStore {
	return &fakeResourceStore{items: map[string]model.MaclawResourceRecord{}}
}

func (s *fakeResourceStore) UpsertResource(_ context.Context, record model.MaclawResourceRecord) (*model.MaclawResourceRecord, error) {
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	s.last = record
	s.items[record.ID.String()] = record
	s.items[record.Handle] = record
	return &record, nil
}

func (s *fakeResourceStore) ListResources(_ context.Context, userID uuid.UUID, q EvaluationResourceQuery) ([]model.MaclawResourceRecord, error) {
	out := []model.MaclawResourceRecord{}
	for key, item := range s.items {
		if key != item.ID.String() || item.OwnerUserID != userID {
			continue
		}
		if q.Kind != "" && item.Kind != string(q.Kind) {
			continue
		}
		if strings.TrimSpace(q.Query) != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.Summary), strings.ToLower(strings.TrimSpace(q.Query))) {
			continue
		}
		if !q.IncludeInactive && (!item.Enabled || item.Status == string(EvaluationResourceStatusArchived)) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *fakeResourceStore) GetResource(_ context.Context, userID uuid.UUID, idOrHandle string) (*model.MaclawResourceRecord, error) {
	item, ok := s.items[strings.TrimSpace(idOrHandle)]
	if !ok || item.OwnerUserID != userID {
		return nil, nil
	}
	return &item, nil
}

// Republishing a package must UPDATE it, not create a second one — and the
// update must not cross the expert boundary.
//
// Measured 2026-10-23: every publish generated a fresh id and handle, so
// republishing produced a second package, the expert's list showed both, and
// the enterprise picker offered both. The page had been promising versioning,
// which only works if the identity is stable. Sending the existing id makes the
// store update the row and the publication follows it (keyed by resource id).
//
// The owner check is what makes that safe: UpsertResource never rewrites
// owner_user_id, so without the owner-scoped lookup an expert could replace
// another expert's package content while the publication stayed owned by the
// victim.
func TestRepublishingUpdatesTheSamePackage(t *testing.T) {
	store := newFakeResourceStore()
	service := NewResourceStoreService(store, agentTestKeyStore(t))
	ctx := context.Background()
	owner := uuid.New()

	// A custom handle on purpose: a generated one is `platform_resource_<id>`
	// and would coincide across republication, hiding the reuse rule entirely.
	const stableHandle = "expert_sample_1"
	first, err := service.SaveResource(ctx, owner, "tenant-1", EvaluationResourceInput{
		Name:     "我的样本",
		Kind:     EvaluationResourceKindSample,
		Status:   EvaluationResourceStatusPublished,
		Enabled:  true,
		Payload:  "第一版",
		Metadata: map[string]string{"handle": stableHandle},
	})
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}

	second, err := service.SaveResource(ctx, owner, "tenant-1", EvaluationResourceInput{
		ID:      first.ID,
		Name:    "我的样本 v2",
		Kind:    EvaluationResourceKindSample,
		Status:  EvaluationResourceStatusPublished,
		Enabled: true,
		Payload: "第二版",
	})
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("id = %s, want the same id %s (a new id means a second package)", second.ID, first.ID)
	}
	if second.Handle != stableHandle {
		t.Errorf("handle = %q, want the original %q — the republish sent no handle and must not mint a new one", second.Handle, stableHandle)
	}
	distinct := map[string]bool{}
	for key, item := range store.items {
		if key == item.ID.String() {
			distinct[item.ID.String()] = true
		}
	}
	if len(distinct) != 1 {
		t.Errorf("stored packages = %d, want 1", len(distinct))
	}

	// Another account naming the same id must be refused, and the owner's row
	// must be untouched.
	if _, err := service.SaveResource(ctx, uuid.New(), "tenant-2", EvaluationResourceInput{
		ID:      first.ID,
		Name:    "被劫持",
		Kind:    EvaluationResourceKindSample,
		Status:  EvaluationResourceStatusPublished,
		Enabled: true,
		Payload: "恶意内容",
	}); err == nil {
		t.Fatal("another account updated a package it does not own")
	}
	kept, err := service.SaveResource(ctx, owner, "tenant-1", EvaluationResourceInput{
		ID:      first.ID,
		Name:    "我的样本 v3",
		Kind:    EvaluationResourceKindSample,
		Status:  EvaluationResourceStatusPublished,
		Enabled: true,
		Payload: "第三版",
	})
	if err != nil {
		t.Fatalf("owner republish after the rejected attempt: %v", err)
	}
	if kept.Name != "我的样本 v3" {
		t.Errorf("name = %q, want the owner's own update to still work", kept.Name)
	}
}
