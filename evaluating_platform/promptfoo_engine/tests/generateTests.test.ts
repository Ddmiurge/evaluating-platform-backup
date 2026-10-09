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
