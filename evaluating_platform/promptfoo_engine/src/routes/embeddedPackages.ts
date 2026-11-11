/**
 * Publication-time validation endpoint for EMBEDDED plugin packages (P28).
 *
 * `POST /embedded-packages/validate { zip, test_entry }` →
 *   200 `{ ok: true, test_summary }`
 *   422 `{ ok: false, errors: [{code, message}], test_summary? }`
 *
 * Mounted AFTER bearerAuth (same as the strategy probe): an unauthenticated
 * caller can never run package code on the engine. The transport carries the
 * original zip base64 (fits the global 1MB JSON limit at the 512KB zip cap)
 * and the engine re-validates it independently — both sides check, the U10
 * doctrine. Replies carry verdicts and bounded summaries only: never file
 * contents, never absolute paths.
 */
import { Router } from 'express';

import { probeEmbeddedPackage } from '../runner/embeddedPackageProbe.js';

export function createEmbeddedPackagesRouter(): Router {
  const router = Router();
  router.post('/embedded-packages/validate', async (req, res) => {
    const zip = typeof req.body?.zip === 'string' ? req.body.zip : '';
    const testEntry = typeof req.body?.test_entry === 'string' ? req.body.test_entry : '';
    const entry = typeof req.body?.entry === 'string' ? req.body.entry : '';
    if (zip === '' || testEntry === '' || entry === '') {
      res.status(400).json({ ok: false, errors: [{ code: 'embedded_bad_request', message: '缺少 zip、test_entry 或 entry。' }] });
      return;
    }
    try {
      const result = await probeEmbeddedPackage({ zipB64: zip, testEntry, entry });
      res.status(result.ok ? 200 : 422).json(result);
    } catch (err) {
      // The probe is defensive, but a throw here must still be a verdict —
      // never a stack, never a path.
      res.status(422).json({
        ok: false,
        errors: [{ code: 'embedded_probe_failed', message: `校验失败：${String((err as Error)?.message ?? err).slice(0, 200)}` }],
      });
    }
  });
  return router;
}
