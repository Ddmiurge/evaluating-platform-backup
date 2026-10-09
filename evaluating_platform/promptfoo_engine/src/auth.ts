/**
 * Bearer-token authentication middleware for the engine REST API.
 * The engine is only reachable from the compose internal network and every
 * request must carry the shared secret issued by the platform (BFF).
 */
import type { NextFunction, Request, RequestHandler, Response } from 'express';
import { timingSafeEqual } from 'node:crypto';

export function bearerAuth(bearerToken: string): RequestHandler {
  return (req: Request, res: Response, next: NextFunction): void => {
    const header = req.headers.authorization ?? '';
    const expected = `Bearer ${bearerToken}`;
    const a = Buffer.from(header, 'utf8');
    const b = Buffer.from(expected, 'utf8');
    const ok = a.length === b.length && timingSafeEqual(a, b);
    if (!ok) {
      res.status(401).json({ error: 'unauthorized' });
      return;
    }
    next();
  };
}
