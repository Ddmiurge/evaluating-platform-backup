import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { describe, expect, it } from 'vitest';
import { ENGINE_WORKER_COUNT, assertNoRemoteGenerationConsent, assertSecuritySwitches, loadEnv } from '../src/env.js';

function env(overrides: Record<string, string | undefined>): NodeJS.ProcessEnv {
  return {
    PROMPTFOO_DISABLE_REMOTE_GENERATION: '1',
    PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION: '1',
    PROMPTFOO_DISABLE_TELEMETRY: '1',
    PROMPTFOO_DISABLE_SHARE: '1',
    PROMPTFOO_ENGINE_BEARER_TOKEN: 'x'.repeat(24),
    PROMPTFOO_ENGINE_PORT: '8090',
    PROMPTFOO_ENGINE_DATA_ROOT: '/data/runs',
    PROMPTFOO_ENGINE_WORKERS: '2',
    PROMPTFOO_ENGINE_MAX_QUEUE: '100',
    ...overrides,
  } as NodeJS.ProcessEnv;
}

describe('assertSecuritySwitches (fail-closed)', () => {
  it('passes when all four switches are disabled', () => {
    expect(() => assertSecuritySwitches(env({}))).not.toThrow();
  });

  it('accepts alternative truthy spellings', () => {
    expect(() =>
      assertSecuritySwitches(
        env({
          PROMPTFOO_DISABLE_REMOTE_GENERATION: 'true',
          PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION: 'yes',
          PROMPTFOO_DISABLE_TELEMETRY: 'on',
          PROMPTFOO_DISABLE_SHARE: 'TRUE',
        }),
      ),
    ).not.toThrow();
  });

  it('throws when remote generation is enabled', () => {
    expect(() => assertSecuritySwitches(env({ PROMPTFOO_DISABLE_REMOTE_GENERATION: '0' }))).toThrow(
      /remote generation/,
    );
  });

  it('throws when a switch is missing entirely', () => {
    expect(() =>
      assertSecuritySwitches(env({ PROMPTFOO_DISABLE_TELEMETRY: undefined })),
    ).toThrow(/telemetry/);
  });

  it('lists every offending channel at once', () => {
    try {
      assertSecuritySwitches(
        env({
          PROMPTFOO_DISABLE_TELEMETRY: '0',
          PROMPTFOO_DISABLE_SHARE: 'false',
        }),
      );
      expect.unreachable('should have thrown');
    } catch (err) {
      const msg = (err as Error).message;
      expect(msg).toContain('telemetry');
      expect(msg).toContain('share/cloud export');
    }
  });
});

describe('loadEnv', () => {
  it('rejects a short bearer token', () => {
    expect(() => loadEnv(env({ PROMPTFOO_ENGINE_BEARER_TOKEN: 'short' }))).toThrow(
      /PROMPTFOO_ENGINE_BEARER_TOKEN/,
    );
  });

  it('clamps worker count and queue depth into range', () => {
    const out = loadEnv(
      env({
        PROMPTFOO_ENGINE_WORKERS: '99',
        PROMPTFOO_ENGINE_MAX_QUEUE: '0',
        PROMPTFOO_ENGINE_PORT: '70000',
      }),
    );
    expect(out.workerCount).toBe(8);
    expect(out.maxQueueDepth).toBe(1);
    expect(out.port).toBe(65535);
  });

  it('falls back on defaults for missing numeric values', () => {
    const out = loadEnv(
      env({
        PROMPTFOO_ENGINE_WORKERS: undefined,
        PROMPTFOO_ENGINE_MAX_QUEUE: undefined,
        PROMPTFOO_ENGINE_PORT: undefined,
      }),
    );
    expect(out.workerCount).toBe(2);
    expect(out.maxQueueDepth).toBe(100);
    expect(out.port).toBe(8090);
  });
});

// P3-3 删除了 `PROMPTFOO_ENGINE_NATIVE_GENERATION` 开关与它的测试块。那个开关只
// 用来在 Plan A / Plan C 之间选择，而 Plan A 已随本批删除。

// P3-3：这个不变量不再挂在开关上。它原本只在 native 路径开启时生效 —— 现在那是唯一
// 路径，所以它无条件生效：环境里出现 promptfoo 会当作「同意使用其远程服务」的变量时，
// 引擎拒绝启动，而不是带着一条外泄通道跑。
//
// 反证：把它改回 `if (!nativeGenerationEnabled(env)) return;` → 下面第二条会红。
describe('assertNoRemoteGenerationConsent (unconditional since P3-3)', () => {
  it('passes when no vendor variable is set', () => {
    expect(() => assertNoRemoteGenerationConsent(env({}))).not.toThrow();
  });

  // 行为变更（P3-3）：这些变量以前在「开关关闭」时是被容忍的（Plan A 不碰 promptfoo
  // 的生成路径）。只剩一条路径之后，它们**无条件**是启动失败条件 —— 也就是说
  // `OPENAI_API_KEY` 的存在本身就足以让容器起不来，这正是想要的严厉程度。
  it('refuses even with no switch set, because there is no other path left', () => {
    expect(() =>
      assertNoRemoteGenerationConsent(
        env({ OPENAI_API_KEY: 'sk-present', PROMPTFOO_REMOTE_GENERATION_URL: 'https://x' }),
      ),
    ).toThrow(/OPENAI_API_KEY/);
  });

  // Counter-proof: a stray OPENAI_API_KEY would make promptfoo's
  // shouldGenerateRemote() return true even with both remote switches disabled —
  // re-opening the exact channel this engine is fail-closed on. Refusing to boot
  // is the only honest response.
  it('refuses to boot with OPENAI_API_KEY set', () => {
    expect(() =>
      assertNoRemoteGenerationConsent(env({ OPENAI_API_KEY: 'sk-whatever' })),
    ).toThrow(/OPENAI_API_KEY/);
  });

  it('refuses to boot with PROMPTFOO_REMOTE_GENERATION_URL set', () => {
    expect(() =>
      assertNoRemoteGenerationConsent(
        env({ PROMPTFOO_REMOTE_GENERATION_URL: 'https://selfhosted.example' }),
      ),
    ).toThrow(/PROMPTFOO_REMOTE_GENERATION_URL/);
  });

  // P3-3：这一条从「禁止设置 PROMPTFOO_CONFIG_DIR」收窄为「禁止目录里存在 auth.json
  // （Promptfoo Cloud 登录）」。原因：前者是代理指标，而把配置目录指向隔离目录正是测试与
  // 挂载卷部署隔离状态的正当手段 —— 而**登录可见**才是真正的危害。
  it('allows an isolated config dir but refuses one holding a cloud login', () => {
    const isolated = mkdtempSync(join(tmpdir(), 'engine-env-noauth-'));
    try {
      expect(() => assertNoRemoteGenerationConsent(env({ PROMPTFOO_CONFIG_DIR: isolated }))).not.toThrow();
      writeFileSync(join(isolated, 'auth.json'), '{"token":"cloud-login"}', 'utf8');
      expect(() => assertNoRemoteGenerationConsent(env({ PROMPTFOO_CONFIG_DIR: isolated }))).toThrow(
        /PROMPTFOO_CONFIG_DIR/,
      );
    } finally {
      rmSync(isolated, { recursive: true, force: true });
    }
  });

  it('lists every offending variable at once', () => {
    try {
      assertNoRemoteGenerationConsent(
        env({ OPENAI_API_KEY: 'sk-x', PROMPTFOO_REMOTE_GENERATION_URL: 'https://x' }),
      );
      expect.unreachable('should have thrown');
    } catch (err) {
      const msg = (err as Error).message;
      expect(msg).toContain('OPENAI_API_KEY');
      expect(msg).toContain('PROMPTFOO_REMOTE_GENERATION_URL');
    }
  });

  // 四个 fail-closed 开关与本不变量相互独立：一个都不许被另一个放松。
  it('enforces the four fail-closed switches alongside it', () => {
    expect(() => assertSecuritySwitches(env({ PROMPTFOO_DISABLE_TELEMETRY: '0' }))).toThrow(/telemetry/);
    expect(() => loadEnv(env({ PROMPTFOO_DISABLE_SHARE: '0' }))).toThrow(/share\/cloud export/);
  });
});

// P3-3：原先是 `effectiveWorkerCount(configured, nativeGeneration)` 的钳位函数。只剩
// 一条路径之后它退化成一个常量 —— 但要钉住的**理由**没变：promptfoo 全进程一个
// SQLite 文件（`PROMPTFOO_CONFIG_DIR` 在 import 时 latch、`getDb()` memoize），而终态
// 清理会删掉它，所以并发 run 会互删结果。U7 实测：清理 + 重迁移之后，run 2 仍通过
// memoize 的句柄读到 run 1 的行，而路径上已经是另一个库。
//
// 反证：把 ENGINE_WORKER_COUNT 改成 2 → 第一条红。
describe('ENGINE_WORKER_COUNT — runs are serialized by construction', () => {
  it('is exactly one worker', () => {
    expect(ENGINE_WORKER_COUNT).toBe(1);
  });

  it('still parses the configured value so the boot log can say it is ignored', () => {
    // 配置值仍被解析，只为在启动日志里说明它被忽略 ——「设了没变化还不告诉我」比
    // 「设了没用」更糟。
    expect(loadEnv(env({ PROMPTFOO_ENGINE_WORKERS: '4' })).workerCount).toBe(4);
  });
});
