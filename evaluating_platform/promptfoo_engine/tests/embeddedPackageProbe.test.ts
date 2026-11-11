/**
 * P28 embedded-plugin probe: the upload gate's engine-side half.
 *
 * Contract under test (design §3.3 + §11):
 *   - runs ONLY tests/run.mjs, offline, in a bounded child process;
 *   - returns a verdict + bounded summary, never file contents/paths;
 *   - zip safety is re-checked here even though the BFF already checked
 *     (both sides validate — the U10 doctrine).
 */
import { describe, expect, it } from 'vitest';
import AdmZip from 'adm-zip';

import { probeEmbeddedPackage } from '../src/runner/embeddedPackageProbe';

function makeZip(files: Record<string, string>): string {
  const zip = new AdmZip();
  for (const [name, content] of Object.entries(files)) {
    zip.addFile(name, Buffer.from(content, 'utf8'));
  }
  return zip.toBuffer().toString('base64');
}

// adm-zip SANITISES entry names at write time (`../evil.js` → `evil.js`), so a
// traversal entry can only be produced by hand — which is exactly the threat
// model (a malicious zip is not built with a polite library). Minimal
// store-only zip writer for that case.
function crc32(buf: Buffer): number {
  let crc = 0xffffffff;
  for (const byte of buf) {
    crc ^= byte;
    for (let k = 0; k < 8; k++) {
      crc = crc & 1 ? (crc >>> 1) ^ 0xedb88320 : crc >>> 1;
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function makeRawZip(entries: { name: string; content: string }[]): string {
  const enc = new TextEncoder();
  const locals: Buffer[] = [];
  const centrals: Buffer[] = [];
  let offset = 0;
  for (const { name, content } of entries) {
    const nameBuf = Buffer.from(enc.encode(name));
    const dataBuf = Buffer.from(enc.encode(content));
    const crc = crc32(dataBuf);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(dataBuf.length, 18);
    local.writeUInt32LE(dataBuf.length, 22);
    local.writeUInt16LE(nameBuf.length, 26);
    locals.push(local, nameBuf, dataBuf);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(dataBuf.length, 20);
    central.writeUInt32LE(dataBuf.length, 24);
    central.writeUInt16LE(nameBuf.length, 28);
    central.writeUInt32LE(offset, 42);
    centrals.push(central, nameBuf);
    offset += 30 + nameBuf.length + dataBuf.length;
  }
  const centralBuf = Buffer.concat(centrals);
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(entries.length, 8);
  eocd.writeUInt16LE(entries.length, 10);
  eocd.writeUInt32LE(centralBuf.length, 12);
  eocd.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, centralBuf, eocd]).toString('base64');
}

const MANIFEST = 'id: probe-demo\nname: Probe Demo\ncategory: injection\nseverity: high\n';

describe('probeEmbeddedPackage', () => {
  it('passes a package whose tests exit 0, and reports a bounded summary', async () => {
    const zip = makeZip({
      'manifest.yaml': MANIFEST,
      'src/plugin.ts': 'export default { async action() { return []; } };',
      'tests/run.mjs': "console.log('12 passed');\nprocess.exit(0);\n",
    });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: 'src/plugin.ts' });
    expect(result.ok).toBe(true);
    expect(result.test_summary).toContain('passed');
    expect(result.errors).toBeUndefined();
  });

  it('fails a package whose tests exit non-zero, quoting only the tail', async () => {
    const zip = makeZip({
      'manifest.yaml': MANIFEST,
      'tests/run.mjs': "console.error('assertion failed at case 1');\nprocess.exit(1);\n",
    });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: 'src/plugin.ts' });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_tests_failed');
    expect(result.errors?.[0].message).toContain('assertion failed at case 1');
    // no absolute temp path may leak into the message
    expect(result.errors?.[0].message).not.toContain('/tmp/embedded-probe-');
  });

  it('refuses a package without the agreed test entry file', async () => {
    const zip = makeZip({ 'manifest.yaml': MANIFEST, 'src/plugin.ts': 'export default {}' });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: 'src/plugin.ts' });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_tests_contract');
  });

  it('refuses a non-contract test entry before doing anything', async () => {
    const zip = makeZip({ 'tests/run.mjs': 'process.exit(0)' });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'evil.mjs', entry: 'src/plugin.ts' });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_tests_contract');
  });

  it('refuses zip path traversal on extraction (belt under the BFF check)', async () => {
    const zip = makeRawZip([
      { name: 'manifest.yaml', content: MANIFEST },
      { name: '../evil.js', content: 'process.exit(0)' },
      { name: 'tests/run.mjs', content: 'process.exit(0)' },
    ]);
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: 'src/plugin.ts' });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_zip_slip');
  });

  it('kills a hanging test and reports tests_timeout, not tests_failed', async () => {
    const zip = makeZip({
      'manifest.yaml': MANIFEST,
      'tests/run.mjs': 'while (true) {}',
    });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: 'src/plugin.ts', timeoutMs: 500 });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_tests_timeout');
  }, 10_000);

  it('refuses a package whose entry is missing after tests pass', async () => {
    const zip = makeZip({
      'manifest.yaml': MANIFEST,
      'tests/run.mjs': 'process.exit(0)',
    });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: 'src/plugin.ts' });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_entry_contract');
    expect(result.errors?.[0].message).toContain('src/plugin.ts');
  });

  it('refuses an entry that does not default-export { action }', async () => {
    const zip = makeZip({
      'manifest.yaml': MANIFEST,
      'src/plugin.ts': 'export default {}',
      'tests/run.mjs': 'process.exit(0)',
    });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: 'src/plugin.ts' });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_entry_contract');
    expect(result.errors?.[0].message).not.toContain('/tmp/embedded-probe-');
  });

  it('refuses a traversal manifest entry before extraction', async () => {
    const zip = makeZip({ 'manifest.yaml': MANIFEST, 'tests/run.mjs': 'process.exit(0)' });
    const result = await probeEmbeddedPackage({ zipB64: zip, testEntry: 'tests/run.mjs', entry: '../evil.ts' });
    expect(result.ok).toBe(false);
    expect(result.errors?.[0].code).toBe('embedded_entry_contract');
  });
});
