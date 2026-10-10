package maclaw

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
	"evaluating_platform/internal/maclaw/redteam"
)

type samplePreviewer interface {
	Preview(context.Context, uuid.UUID, int) ([]model.AttackPayload, error)
}

type samplePublicationGetter interface {
	GetByID(context.Context, uuid.UUID) (*model.AttackSample, error)
}

type composedAttackPreviewer interface {
	Preview(context.Context, uuid.UUID, int) ([]model.AttackPayload, error)
}

type composedAttackPublicationGetter interface {
	GetByID(context.Context, uuid.UUID) (*model.ComposedAttack, error)
}

type templateGetter interface {
	GetByID(context.Context, uuid.UUID) (*model.Template, error)
}

type PlatformRedteamPayloadProvider struct {
	samples          samplePreviewer
	sampleMetadata   samplePublicationGetter
	templates        templateGetter
	composed         composedAttackPreviewer
	composedMetadata composedAttackPublicationGetter
}

func NewPlatformRedteamPayloadProvider(samples samplePreviewer, templates templateGetter, composed composedAttackPreviewer) *PlatformRedteamPayloadProvider {
	p := &PlatformRedteamPayloadProvider{samples: samples, templates: templates, composed: composed}
	if getter, ok := samples.(samplePublicationGetter); ok {
		p.sampleMetadata = getter
	}
	if getter, ok := composed.(composedAttackPublicationGetter); ok {
		p.composedMetadata = getter
	}
	return p
}

func (p *PlatformRedteamPayloadProvider) SetPublicationStores(samples samplePublicationGetter, templates templateGetter, composed composedAttackPublicationGetter) {
	if p == nil {
		return
	}
	if samples != nil {
		p.sampleMetadata = samples
	}
	if templates != nil {
		p.templates = templates
	}
	if composed != nil {
		p.composedMetadata = composed
	}
}

func (p *PlatformRedteamPayloadProvider) LoadSamplePayloads(ctx context.Context, ref string, limit int) ([]model.AttackPayload, error) {
	if p == nil || p.samples == nil {
		return nil, ErrNotConfigured
	}
	id, err := redteamDataUUID(ref, CapabilitySourceSample)
	if err != nil {
		return nil, err
	}
	if err := p.requirePublishedSample(ctx, id); err != nil {
		return nil, err
	}
	return p.samples.Preview(ctx, id, redteam.NormalizePayloadLimit(limit))
}

func (p *PlatformRedteamPayloadProvider) LoadComposedPayloads(ctx context.Context, ref string, limit int) ([]model.AttackPayload, error) {
	if p == nil || p.composed == nil {
		return nil, ErrNotConfigured
	}
	id, err := redteamDataUUID(ref, CapabilitySourceComposed)
	if err != nil {
		return nil, err
	}
	if err := p.requirePublishedComposedAttack(ctx, id); err != nil {
		return nil, err
	}
	return p.composed.Preview(ctx, id, redteam.NormalizePayloadLimit(limit))
}

func (p *PlatformRedteamPayloadProvider) GetTemplate(ctx context.Context, ref string) (*model.Template, error) {
	if p == nil || p.templates == nil {
		return nil, ErrNotConfigured
	}
	id, err := redteamDataUUID(ref, CapabilitySourceTemplate)
	if err != nil {
		return nil, err
	}
	tpl, err := p.templates.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if tpl == nil {
		return nil, errors.New("template not found")
	}
	if !isPublishedVisible(tpl.Status, tpl.Visibility) {
		return nil, errors.New("template is not published for enterprise use")
	}
	return tpl, nil
}

func (p *PlatformRedteamPayloadProvider) requirePublishedSample(ctx context.Context, id uuid.UUID) error {
	if p == nil || p.sampleMetadata == nil {
		return errors.New("sample publication metadata is unavailable")
	}
	item, err := p.sampleMetadata.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if item == nil {
		return errors.New("sample not found")
	}
	if !isPublishedVisible(item.Status, item.Visibility) {
		return errors.New("sample is not published for enterprise use")
	}
	return nil
}

func (p *PlatformRedteamPayloadProvider) requirePublishedComposedAttack(ctx context.Context, id uuid.UUID) error {
	if p == nil || p.composedMetadata == nil {
		return errors.New("composed attack publication metadata is unavailable")
	}
	item, err := p.composedMetadata.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if item == nil {
		return errors.New("composed attack not found")
	}
	if !isPublishedVisible(item.Status, item.Visibility) {
		return errors.New("composed attack is not published for enterprise use")
	}
	return nil
}

func redteamDataUUID(ref, expectedPrefix string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, expectedPrefix+":")
	if expectedPrefix == CapabilitySourceComposed {
		ref = strings.TrimPrefix(ref, "composed:")
	}
	id, err := uuid.Parse(ref)
	if err != nil {
		return uuid.Nil, errors.New("invalid " + expectedPrefix + " ref")
	}
	return id, nil
}
