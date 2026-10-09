# promptfoo-engine: red-team engine adapter service.
#
# Build context is the promptfoo_engine/ directory itself (the runtime
# dependency on promptfoo is pinned to a registry version 0.123.0, so no
# local checkout is needed inside the image).
#
# Stages:
#   deps   - install dependencies (promptfoo@0.123.0 from registry) with cache
#   build  - compile TS -> dist
#   runtime - minimal image, non-root user, no build tooling

FROM node:24-alpine AS deps
WORKDIR /app
# Copy only manifests first for layer caching.
COPY package.json package-lock.json* ./
# Use npmmirror for builds inside CN networks; falls back to the default
# registry automatically when the mirror is unreachable.
RUN npm config set registry https://registry.npmmirror.com && \
    npm install --omit=dev --no-audit --no-fund

FROM node:24-alpine AS build
WORKDIR /app
ENV NODE_ENV=development
COPY --from=deps /app ./
COPY . .
RUN npm install --no-audit --no-fund && npm run build

FROM node:24-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
# Security switches are asserted at boot by src/env.ts (fail-closed).
ENV PROMPTFOO_DISABLE_REMOTE_GENERATION=1 \
    PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION=1 \
    PROMPTFOO_DISABLE_TELEMETRY=1 \
    PROMPTFOO_DISABLE_SHARE=1 \
    PROMPTFOO_ENGINE_PORT=8090 \
    PROMPTFOO_ENGINE_DATA_ROOT=/data/runs \
    PROMPTFOO_ENGINE_WORKERS=2 \
    PROMPTFOO_ENGINE_MAX_QUEUE=100
# Prepare the writable data directory as root, then drop privileges to the
# non-root "node" user (uid 1000) shipped by the alpine node image.
RUN mkdir -p /data/runs && chown node:node /data/runs
USER node
VOLUME ["/data/runs"]
COPY --from=build --chown=node:node /app/dist ./dist
COPY --from=build --chown=node:node /app/node_modules ./node_modules
COPY --from=build --chown=node:node /app/package.json ./package.json
EXPOSE 8090
HEALTHCHECK --interval=15s --timeout=5s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8090/health || exit 1
CMD ["node", "dist/server.js"]
