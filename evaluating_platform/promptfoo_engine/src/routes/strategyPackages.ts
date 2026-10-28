/**
 * Publication-time validation endpoint for CODE strategy packages.
 *
 * `POST /strategy-packages/validate { source }` → `{ ok, code?, message? }`.
 *
 * Called by the BFF when an expert publishes `pf_strategy_form=js`, BEFORE the
 * publication record is written. The reply carries a verdict and a content-free
 * message: never the module's output, never a stack, never a path (design §6.1).
 */
import { Router } from 'express';

import { codeStrategyPackagesEnabled } from '../env.js';
import { probeCodeStrategyModule } from '../runner/codeStrategyProbe.js';

/** Cap mirrors the BFF's `CustomStrategyCodeMaxBytes`. */
const MAX_SOURCE_BYTES = 256 * 1024;

export function createStrategyPackagesRouter(): Router {
  const router = Router();
  router.post('/strategy-packages/validate', async (req, res) => {
    if (!codeStrategyPackagesEnabled()) {
      res.status(409).json({
        ok: false,
        code: 'code_strategy_disabled',
        message: '本部署未开放策略代码包（MACLAW_STRATEGY_CODE_PACKAGES 未开启）。',
      });
      return;
    }
    const source = typeof req.body?.source === 'string' ? req.body.source : '';
    if (source.trim() === '') {
      res.status(400).json({ ok: false, code: 'code_strategy_empty', message: '缺少模块内容。' });
      return;
    }
    if (Buffer.byteLength(source, 'utf8') > MAX_SOURCE_BYTES) {
      res.status(400).json({ ok: false, code: 'code_strategy_too_large', message: '模块超过大小上限。' });
      return;
    }
    const result = await probeCodeStrategyModule({ source });
    res.status(result.ok ? 200 : 422).json(result);
  });
  return router;
}
