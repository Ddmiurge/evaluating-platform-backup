/**
 * Publication-time probe for an EMBEDDED plugin package (P28).
 *
 * # What it answers
 *
 * "Do this package's own tests pass, offline, in seconds?" — the upload gate's
 * last check before the package enters the store. The BFF has already refused
 * unsafe zips, forbidden capability tokens and malformed manifests; this probe
 * re-validates the zip independently (both sides check — the U10 doctrine) and
 * then actually RUNS `node <testEntry>` in a bounded child process.
 *
 * # What comes back
 *
 * ONLY a verdict plus a bounded test summary (exit code + duration, or the
 * tail of the failure output with the temp directory prefix stripped). Never
 * file contents, never absolute paths, never a stack trace: the caller is an
 * expert portal and the message must be actionable without being an oracle.
 *
 * # Why a child process
 *
 * Same reason as the code-strategy probe: a package's test file runs arbitrary
 * code, and it must not run inside the engine server process. The child gets a
 * fresh temp dir per call, a wall-clock timeout, and no network guarantee
 * beyond the static scan that already refused fetch/http tokens at upload.
 */
import { spawn } from 'node:child_process';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

import AdmZip from 'adm-zip';

export interface EmbeddedPackageProbeInput {
  /** base64 of the uploaded zip — the exact bytes the BFF validated. */
  zipB64: string;
  /** The manifest's tests entry; must equal tests/run.mjs (contract). */
  testEntry: string;
  timeoutMs?: number;
}

export interface EmbeddedPackageProbeResult {
  ok: boolean;
  errors?: Array<{ code: string; message: string }>;
  test_summary?: string;
}

const DEFAULT_TIMEOUT_MS = 30_000;
const MAX_FILES = 64;
const MAX_UNCOMPRESSED = 4 * 1024 * 1024;

const fail = (code: string, message: string): EmbeddedPackageProbeResult => ({
  ok: false,
  errors: [{ code, message }],
});

export async function probeEmbeddedPackage(
  input: EmbeddedPackageProbeInput,
): Promise<EmbeddedPackageProbeResult> {
  let zip: Buffer;
  try {
    zip = Buffer.from(input.zipB64, 'base64');
  } catch {
    return fail('embedded_payload_encoding', '包正文不是合法 base64。');
  }
  if (zip.length === 0 || zip.length > 512 * 1024) {
    return fail('embedded_too_large', '压缩包超过 512KB 上限。');
  }
  if (input.testEntry !== 'tests/run.mjs') {
    return fail('embedded_tests_contract', '包内测试必须是 tests/run.mjs（零依赖、离线可跑）。');
  }

  let entries: AdmZip.IZipEntry[];
  try {
    entries = new AdmZip(zip).getEntries();
  } catch {
    return fail('embedded_not_zip', '不是有效的 zip 文件。');
  }
  if (entries.length === 0 || entries.length > MAX_FILES) {
    return fail('embedded_file_count', `包内文件数必须在 1..${MAX_FILES} 之间。`);
  }

  const workDir = await mkdtemp(path.join(os.tmpdir(), 'embedded-probe-'));
  try {
    let total = 0;
    const testEntry = entries.find((e) => e.entryName.replace(/\\/g, '/') === input.testEntry && !e.isDirectory);
    if (!testEntry) {
      return fail('embedded_tests_contract', `包内缺少 ${input.testEntry}。`);
    }
    for (const entry of entries) {
      const name = entry.entryName.replace(/\\/g, '/');
      if (name.startsWith('/') || name.includes('..')) {
        return fail('embedded_zip_slip', `包内路径非法（${name}）。`);
      }
      if (entry.isDirectory) {
        continue;
      }
      total += entry.header.size;
      if (total > MAX_UNCOMPRESSED) {
        return fail('embedded_zip_bomb', '解压总大小超过上限。');
      }
      const target = path.join(workDir, name);
      // path.join normalises; re-assert containment after join (belt).
      if (!target.startsWith(workDir)) {
        return fail('embedded_zip_slip', `包内路径非法（${name}）。`);
      }
      await mkdir(path.dirname(target), { recursive: true });
      await writeFile(target, entry.getData());
    }

    const started = Date.now();
    const outcome = await runTests(workDir, input.testEntry, input.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    const durationMs = Date.now() - started;
    if (outcome.exitCode === 0) {
      return { ok: true, test_summary: `tests passed · ${durationMs}ms` };
    }
    if (outcome.timedOut) {
      return fail('embedded_tests_timeout', `包内测试超过 ${Math.round((input.timeoutMs ?? DEFAULT_TIMEOUT_MS) / 1000)}s 未结束——必须离线秒级可跑。`);
    }
    return {
      ok: false,
      errors: [{ code: 'embedded_tests_failed', message: `包内测试未通过（exit ${outcome.exitCode}）：${outcome.tail}` }],
      test_summary: `tests failed · ${durationMs}ms`,
    };
  } finally {
    await rm(workDir, { recursive: true, force: true }).catch(() => {});
  }
}

interface TestRunOutcome {
  exitCode: number;
  timedOut: boolean;
  tail: string;
}

function runTests(workDir: string, testEntry: string, timeoutMs: number): Promise<TestRunOutcome> {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [testEntry], {
      cwd: workDir,
      env: { ...process.env, NODE_ENV: 'test' },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let output = '';
    const capture = (chunk: Buffer) => {
      // Bounded: the last 2KB is what a failing test usually prints last.
      output = (output + chunk.toString('utf8')).slice(-2048);
    };
    child.stdout.on('data', capture);
    child.stderr.on('data', capture);
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill('SIGKILL');
    }, timeoutMs);
    child.on('error', (err) => {
      clearTimeout(timer);
      resolve({ exitCode: -1, timedOut, tail: `无法执行测试：${String(err.message).slice(0, 200)}` });
    });
    child.on('close', (code) => {
      clearTimeout(timer);
      if (timedOut) {
        resolve({ exitCode: -1, timedOut: true, tail: 'tests timed out' });
        return;
      }
      const tail = output
        .split('\n')
        .map((l) => l.trim())
        .filter(Boolean)
        .slice(-3)
        .join(' · ')
        // the temp path is ours, not the submitter's business:
        .replace(/\/tmp\/embedded-probe-[^\s]*/g, '<tmp>');
      resolve({ exitCode: code ?? -1, timedOut: false, tail: tail.slice(0, 500) || `exit ${code}` });
    });
  });
}
