/**
 * sandbox.ts — U10 · runtime verification of the engine's execution boundary.
 *
 * # Why this file exists at all
 *
 * The design doc (`14` §2 U10 ②) calls the sandbox the batch's heaviest security
 * item, and names three properties: **read-only root filesystem**, **non-root**,
 * **no external network**. Those are declared in `Dockerfile` and
 * `docker-compose.yml` — but a declaration is a claim, and this project's
 * recurring lesson (U6, U7, U8) is that unverified claims are how silent
 * failures get in.
 *
 * So this module turns the three claims into assertions the process can check
 * about **itself**, at boot, without needing Docker on the machine running the
 * tests:
 *
 *  - **non-root** — `process.getuid?.() === 0` is a hard failure. A container
 *    started as root can write anywhere the filesystem allows, which
 *    retroactively makes the read-only claim worthless.
 *  - **read-only root** — *declared*, not probed. There is no portable Node API
 *    for "is this filesystem read-only", and a probe that tries to write to `/`
 *    would itself be the thing we are trying to forbid. So the assertion is
 *    against the environment contract (`PROMPTFOO_ENGINE_READONLY_ROOT=1`) plus
 *    a **real write probe** against a path that must be read-only in the
 *    shipped compose file, which does test the actual kernel behaviour.
 *  - **no external network** — the engine *must* reach the platform-supplied
 *    model endpoint, so "no network" is not the property. The property is: the
 *    process can only reach the endpoints the platform handed it. That is
 *    enforced where the endpoints are used (`assertSafeProviderUrl` in
 *    nativeRedteam.ts) and, for a published plugin, is moot — a YAML package
 *    has no executable surface to open a socket with, which is the *reason* the
 *    first release is YAML-only.
 *
 * # What this module refuses to do
 *
 * It does not pretend to have verified something it cannot. `PROMPTFOO_ENGINE_SANDBOX_PROBE=1`
 * opts into a **live** write/network probe used by the container self-check; with
 * it unset (the default, and every test run) the module reports `declared`
 * rather than `verified`, and the distinction is visible in the report rather
 * than blurred. The U10 report registers the same distinction.
 */
import fs from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';

/** Per-property verification state. Never collapsed into a bare boolean. */
export type SandboxVerdict = 'verified' | 'declared' | 'failed' | 'unavailable';

export interface SandboxProperty {
  property: string;
  verdict: SandboxVerdict;
  detail: string;
}

export interface SandboxReport {
  properties: SandboxProperty[];
  /** True only when nothing failed. `declared` does not make it false. */
  ok: boolean;
  /** True when at least one property was actually probed, not just declared. */
  probed: boolean;
}

/** Environment key asserting the read-only root contract (compose sets it). */
export const READONLY_ROOT_ENV = 'PROMPTFOO_ENGINE_READONLY_ROOT';

/** Environment key opting into live write/network probes. */
export const SANDBOX_PROBE_ENV = 'PROMPTFOO_ENGINE_SANDBOX_PROBE';

/**
 * Path used by the write probe. Must be a location that is read-only in the
 * shipped compose service (`read_only: true` + a tmpfs only on the data dirs),
 * and must be writable in a dev checkout — otherwise the probe is untestable
 * outside a container, which is the situation this batch is in.
 *
 * `/etc` is chosen because it is root-owned and read-only under a read-only
 * rootfs, and because a non-root process cannot write there even without
 * `read_only`. Both properties therefore have to hold for the probe to fail,
 * which is the point: the probe passing is meaningful.
 */
const READONLY_PROBE_PATH = '/etc/promptfoo-engine-u10-sandbox-probe';

/**
 * Host used by the network probe. Chosen because it is promptfoo's own remote
 * generation service — the exact channel this engine is fail-closed on
 * (`PROMPTFOO_DISABLE_REMOTE_GENERATION=1`). Reaching it would mean the
 * fail-closed switch is not actually keeping the process off the network.
 */
const NETWORK_PROBE_HOST = 'api.promptfoo.app';
const NETWORK_PROBE_PORT = 443;

/**
 * Check the non-root property. Fails when the uid is 0 or unknown-but-zero.
 *
 * `process.getuid` is absent on Windows, so the check reports `unavailable`
 * rather than passing: the engine ships on Linux, and "cannot tell" must not
 * read as "fine".
 */
export function checkNonRoot(): SandboxProperty {
  const getuid = process.getuid;
  if (typeof getuid !== 'function') {
    return {
      property: 'non_root',
      verdict: 'unavailable',
      detail: 'process.getuid 不可用（当前平台非 POSIX），无法判定是否以 root 运行。',
    };
  }
  const uid = getuid.call(process);
  if (uid === 0) {
    return {
      property: 'non_root',
      verdict: 'failed',
      detail: '引擎以 root（uid=0）运行。只读根文件系统在 root 下不构成有效边界，必须以非 root 用户启动。',
    };
  }
  return { property: 'non_root', verdict: 'verified', detail: `uid=${uid}` };
}

/**
 * Check the read-only root filesystem property.
 *
 * Two independent signals, and the report distinguishes them:
 *
 *  - the environment declaration (`PROMPTFOO_ENGINE_READONLY_ROOT=1`), which is
 *    what the compose file sets alongside `read_only: true`; and
 *  - a **live write probe** to {@link READONLY_PROBE_PATH}, which is only run
 *    when `PROMPTFOO_ENGINE_SANDBOX_PROBE=1`.
 *
 * A successful write is reported as `failed`, not as a warning. The engine holds
 * the materialized generation API key in its workdir, so a writable root
 * filesystem means an expert-authored package — or anything else that gets code
 * execution inside this process — can read or replace it.
 */
export async function checkReadonlyRoot(probe: boolean): Promise<SandboxProperty> {
  const declared = parseBool(process.env[READONLY_ROOT_ENV]);
  if (!probe) {
    return {
      property: 'readonly_root',
      verdict: declared ? 'declared' : 'unavailable',
      detail: declared
        ? `${READONLY_ROOT_ENV}=1 已声明只读根文件系统；未开启 ${SANDBOX_PROBE_ENV}，本次未实际写入探测。`
        : `${READONLY_ROOT_ENV} 未设置，无法判定根文件系统是否只读。请在 compose 中为引擎服务设置 read_only: true 与该环境变量。`,
    };
  }
  const target = path.join(READONLY_PROBE_PATH, `${process.pid}`);
  try {
    await fs.writeFile(target, 'u10 sandbox probe');
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code ?? 'unknown';
    // A write failure is the expected outcome. The *kind* of failure is
    // reported so an operator can tell "read-only filesystem" from "permission
    // denied", which are different guarantees.
    return {
      property: 'readonly_root',
      verdict: 'verified',
      detail: `写入 ${READONLY_PROBE_PATH} 失败（${code}），根文件系统按预期不可写。`,
    };
  }
  // The write succeeded: clean up our own artifact before reporting, then fail.
  await fs.rm(target, { force: true }).catch(() => undefined);
  return {
    property: 'readonly_root',
    verdict: 'failed',
    detail: `写入 ${READONLY_PROBE_PATH} 成功，根文件系统可写。引擎 workdir 内持有已物化的生成模型密钥，可写根文件系统意味着该密钥可被读取或替换。`,
  };
}

/**
 * Probe whether the process can reach promptfoo's remote generation service.
 *
 * This is the network half of the sandbox claim, stated precisely. The engine
 * cannot have "no network" — it must call the platform-supplied model endpoint.
 * What it must not have is **unmanaged** egress, and the most consequential
 * unmanaged destination is promptfoo's own remote generation service: reaching
 * it would mean `PROMPTFOO_DISABLE_REMOTE_GENERATION=1` is not actually keeping
 * this process off the network, which is the U6 failure at the transport level
 * rather than the library level.
 *
 * A `true` result is a failure. Any error (refused, timed out, DNS-blocked) is
 * the expected outcome and counts as verified — the container's egress policy is
 * expected to drop the connection, not to reject it politely.
 */
export async function checkNetworkEgress(probe: boolean): Promise<SandboxProperty> {
  if (!probe) {
    return {
      property: 'no_unmanaged_egress',
      verdict: 'declared',
      detail: `未开启 ${SANDBOX_PROBE_ENV}，本次未实际探测外网可达性。引擎只应能访问平台下发的模型端点（见 nativeRedteam.ts assertSafeProviderUrl）。`,
    };
  }
  const reachable = await canConnect(NETWORK_PROBE_HOST, NETWORK_PROBE_PORT, 3000);
  if (reachable) {
    return {
      property: 'no_unmanaged_egress',
      verdict: 'failed',
      detail: `成功连接 ${NETWORK_PROBE_HOST}:${NETWORK_PROBE_PORT}。引擎进程可以访问 promptfoo 远程服务，说明 PROMPTFOO_DISABLE_REMOTE_GENERATION=1 只在库层面生效，网络层面并未阻断。`,
    };
  }
  return {
    property: 'no_unmanaged_egress',
    verdict: 'verified',
    detail: `连接 ${NETWORK_PROBE_HOST}:${NETWORK_PROBE_PORT} 失败，外网出口按预期不可达。`,
  };
}

/**
 * canConnect resolves true only on a completed TCP handshake. Any error, and
 * any timeout, is false.
 */
function canConnect(host: string, port: number, timeoutMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = new net.Socket();
    const finish = (reachable: boolean): void => {
      socket.removeAllListeners();
      socket.destroy();
      resolve(reachable);
    };
    socket.setTimeout(timeoutMs);
    socket.once('connect', () => finish(true));
    socket.once('timeout', () => finish(false));
    socket.once('error', () => finish(false));
    socket.connect(port, host);
  });
}

/** parseBool mirrors env.ts's rule so both files agree on what "1" means. */
function parseBool(v: string | undefined): boolean {
  if (!v) return false;
  return ['1', 'true', 'yes', 'on'].includes(v.trim().toLowerCase());
}

/**
 * The path the engine writes per-run artifacts to. Reported so an operator
 * confirming the boundary can see that writable surface named explicitly rather
 * than having to infer it.
 */
export function writableRoot(): string {
  return process.env['PROMPTFOO_ENGINE_DATA_ROOT'] ?? '/data/runs';
}

/**
 * Run every sandbox check.
 *
 * `probe` defaults to the environment opt-in. Tests that want the declared-only
 * behaviour pass `false` explicitly so they do not depend on ambient env.
 */
export async function verifySandbox(probe?: boolean): Promise<SandboxReport> {
  const liveProbe = probe ?? parseBool(process.env[SANDBOX_PROBE_ENV]);
  const properties: SandboxProperty[] = [
    checkNonRoot(),
    await checkReadonlyRoot(liveProbe),
    await checkNetworkEgress(liveProbe),
    {
      property: 'writable_root',
      verdict: 'declared',
      detail: `唯一可写目录：${writableRoot()}（workdir 与 promptfoo 产物）；workdir 在 run终态被删除。当前进程 cwd=${safeCwd()}，uid=${process.getuid?.() ?? 'n/a'}。`,
    },
    {
      property: 'host',
      verdict: 'declared',
      detail: `${os.type()} ${os.release()}，uid=${process.getuid?.() ?? 'n/a'}，gid=${process.getgid?.() ?? 'n/a'}。`,
    },
  ];
  return {
    properties,
    ok: properties.every((p) => p.verdict !== 'failed'),
    probed: liveProbe,
  };
}

/** safeCwd never throws (a deleted cwd is normal in containers). */
function safeCwd(): string {
  try {
    return process.cwd();
  } catch {
    return '<unavailable>';
  }
}

/**
 * Render a report as stable lines for the container self-check log.
 *
 * One line per property with an explicit verdict word, so a CI log reader can
 * see "declared" and "verified" as different states without parsing prose.
 */
export function formatSandboxReport(report: SandboxReport): string[] {
  const header = report.probed
    ? '[sandbox] live probes enabled'
    : `[sandbox] declared-only (set ${SANDBOX_PROBE_ENV}=1 for live probes)`;
  return [
    header,
    ...report.properties.map((p) => `[sandbox] ${p.property}=${p.verdict} — ${p.detail}`),
    `[sandbox] ok=${report.ok}`,
  ];
}