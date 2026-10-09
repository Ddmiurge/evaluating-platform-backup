import { describe, expect, it } from 'vitest';
import { mkdtemp, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { WorkdirManager } from '../src/manager/workdir.js';

describe('WorkdirManager', () => {
  it('creates isolated directories per run and tracks them', async () => {
    const root = await mkdtemp(join(tmpdir(), 'engine-wd-'));
    try {
      const mgr = new WorkdirManager(root);
      const dir1 = await mgr.create('run-abc');
      const dir2 = await mgr.create('run-def');
      expect(dir1).not.toBe(dir2);
      expect(mgr.has('run-abc')).toBe(true);
      expect(mgr.has('run-def')).toBe(true);
      expect(mgr.size).toBe(2);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('rejects duplicate run ids', async () => {
    const root = await mkdtemp(join(tmpdir(), 'engine-wd-'));
    try {
      const mgr = new WorkdirManager(root);
      await mgr.create('run-dup');
      await expect(mgr.create('run-dup')).rejects.toThrow(/already exists/);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('cleanup removes the directory and is idempotent', async () => {
    const root = await mkdtemp(join(tmpdir(), 'engine-wd-'));
    try {
      const mgr = new WorkdirManager(root);
      const dir = await mgr.create('run-clean');
      await mgr.cleanup('run-clean');
      await expect(readdir(root)).resolves.toHaveLength(0);
      expect(mgr.has('run-clean')).toBe(false);
      // second cleanup must not throw
      await expect(mgr.cleanup('run-clean')).resolves.toBeUndefined();
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('sanitizes hostile run ids into safe directory names', async () => {
    const root = await mkdtemp(join(tmpdir(), 'engine-wd-'));
    try {
      const mgr = new WorkdirManager(root);
      const dir = await mgr.create('../../etc/passwd');
      expect(dir.startsWith(root)).toBe(true);
      const entries = await readdir(root);
      expect(entries).toHaveLength(1);
      expect(entries[0]).not.toContain('..');
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
});
