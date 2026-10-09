/**
 * Unit tests for the offline-testable parts of generateTests: JSON parsing
 * of generation-LLM output (fences, garbage, limits, fallbacks).
 */
import { describe, expect, it } from 'vitest';
import { EngineRunError, parseCases } from '../src/runner/generateTests.js';

describe('parseCases', () => {
  it('parses a plain JSON array', () => {
    const raw = JSON.stringify([
      { prompt: '攻击A', plugin_id: 'harmful', strategy_id: 'direct' },
      { prompt: '攻击B', plugin_id: 'pii' },
    ]);
    const out = parseCases(raw, 10);
    expect(out).toHaveLength(2);
    expect(out[0]).toEqual({ prompt: '攻击A', plugin_id: 'harmful', strategy_id: 'direct' });
    expect(out[1]).toEqual({ prompt: '攻击B', plugin_id: 'pii' });
  });

  it('strips markdown fences and surrounding prose', () => {
    const raw = '以下是结果：\n```json\n[{"prompt":"攻击A","plugin_id":"jailbreak"}]\n```\n以上。';
    const out = parseCases(raw, 10);
    expect(out).toHaveLength(1);
    expect(out[0].plugin_id).toBe('jailbreak');
  });

  it('falls back plugin_id to custom and drops empty prompts', () => {
    const raw = JSON.stringify([
      { prompt: '攻击A' },
      { prompt: '   ' },
      { prompt: '', plugin_id: 'x' },
    ]);
    const out = parseCases(raw, 10);
    expect(out).toHaveLength(1);
    expect(out[0].plugin_id).toBe('custom');
  });

  it('enforces the limit', () => {
    const raw = JSON.stringify([
      { prompt: '攻击A', plugin_id: 'p' },
      { prompt: '攻击B', plugin_id: 'p' },
      { prompt: '攻击C', plugin_id: 'p' },
    ]);
    expect(parseCases(raw, 2)).toHaveLength(2);
  });

  it('returns empty on non-JSON garbage', () => {
    expect(parseCases('无法生成内容', 5)).toEqual([]);
    expect(parseCases('{"prompt":"非数组"}', 5)).toEqual([]);
  });
});

describe('EngineRunError', () => {
  it('carries a machine-readable code', () => {
    const err = new EngineRunError('generation_failed', 'boom');
    expect(err.code).toBe('generation_failed');
    expect(err.message).toBe('boom');
    expect(err).toBeInstanceOf(Error);
  });
});

describe('parseCases plugin_id whitelist normalization (E-02)', () => {
  it('keeps ids that are in the requested whitelist', () => {
    const raw = JSON.stringify([
      { prompt: '攻击A', plugin_id: 'harmful' },
      { prompt: '攻击B', plugin_id: 'pii' },
    ]);
    const out = parseCases(raw, 10, ['harmful', 'pii']);
    expect(out.map((c) => c.plugin_id)).toEqual(['harmful', 'pii']);
  });

  it('normalizes an out-of-whitelist id to the sole requested plugin', () => {
    const raw = JSON.stringify([{ prompt: '攻击A', plugin_id: 'harmful:cybercrime' }]);
    const out = parseCases(raw, 10, ['pii']);
    expect(out[0]?.plugin_id).toBe('pii');
  });

  it('normalizes an out-of-whitelist id to unknown when several are requested', () => {
    const raw = JSON.stringify([{ prompt: '攻击A', plugin_id: 'hallucinated-family' }]);
    const out = parseCases(raw, 10, ['harmful', 'pii']);
    expect(out[0]?.plugin_id).toBe('unknown');
  });

  it('normalizes a missing plugin_id to the sole requested plugin', () => {
    const raw = JSON.stringify([{ prompt: '攻击A' }]);
    const out = parseCases(raw, 10, ['harmful']);
    expect(out[0]?.plugin_id).toBe('harmful');
  });

  it('trims/dedupes the whitelist and ignores empty entries', () => {
    const raw = JSON.stringify([{ prompt: '攻击A', plugin_id: 'pii' }]);
    const out = parseCases(raw, 10, [' pii ', 'pii', '']);
    expect(out[0]?.plugin_id).toBe('pii');
  });

  it('preserves legacy fallback (custom) when no whitelist is supplied', () => {
    const raw = JSON.stringify([{ prompt: '攻击A' }]);
    const out = parseCases(raw, 10);
    expect(out[0]?.plugin_id).toBe('custom');
  });
});
