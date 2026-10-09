# Docker Desktop Proxy Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore enterprise portal messaging by replacing Docker Desktop's dead manual proxy with the active Windows System proxy and proving the original 502 symptom is gone.

**Architecture:** Keep the application and tenant configuration unchanged. Repair the host-level Docker egress path, restart Docker Desktop, recover the existing Compose stack without deleting volumes, then verify the same path from containers through MaClawSrv to the configured model provider.

**Tech Stack:** Docker Desktop for Windows, Docker Compose, MaClawSrv, Go BFF, curl, Windows System proxy (`mihomo.exe`).

---

## File and State Boundaries

- No production source file will be modified.
- Docker Desktop owns `%APPDATA%\Docker\settings-store.json`; change it through the Docker Desktop settings surface, not by editing the JSON file directly.
- Preserve all named Docker volumes and existing containers. Do not run prune, reset, remove-volume, or destructive recovery commands.
- Existing untracked directories `evaluating_platform/tmp_/` and `reports/` are out of scope.
- The execution evidence must contain only status codes, service names, timings, and sanitized error classes—never tokens, model keys, prompts, responses, payloads, or credentials.

### Task 1: Capture the Red Baseline

**Files:**
- Read only: `C:\Users\wangboyang\AppData\Roaming\Docker\settings-store.json`
- Read only: `C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform\docker-compose.yml`

- [ ] **Step 1: Confirm Docker Desktop is running and only the platform stack is active**

Run:

```powershell
docker desktop status
docker ps --format "{{.Names}}"
```

Expected: Docker Desktop reports running; the active containers are the seven `ep_*` platform services.

- [ ] **Step 2: Reconfirm the stale proxy and active Windows proxy without printing unrelated settings**

Inspect only these safe facts:

```text
Docker Desktop proxy mode: manual
Docker Desktop HTTP/HTTPS proxy: 127.0.0.1:17890
Windows ProxyServer: 127.0.0.1:8960
127.0.0.1:17890: no listener
127.0.0.1:8960: mihomo.exe listener
```

Expected: all five facts match. If they do not, stop and return to root-cause analysis before changing settings.

- [ ] **Step 3: Run the failing container probe and record the expected RED result**

Run:

```powershell
docker exec ep_maclaw_runtime curl -sS -o /dev/null -w "%{http_code} %{ssl_verify_result}" https://api.deepseek.com/chat/completions
```

Expected before repair: curl exit 35 with `unexpected eof while reading`, proving the test still detects the original egress failure.

### Task 2: Apply the Root Configuration Repair

**Files:**
- Modify through Docker Desktop UI: `%APPDATA%\Docker\settings-store.json`

- [ ] **Step 1: Open Docker Desktop proxy settings**

Use the Docker Desktop Settings surface and navigate to Proxies. Verify the visible manual proxy still points to port 17890 before changing it.

- [ ] **Step 2: Select System proxy**

Change both Docker/engine and container proxy behavior from the stale manual proxy to System proxy. Do not enter model credentials or change any application settings.

- [ ] **Step 3: Apply the setting**

Apply the Docker Desktop setting. If Docker Desktop does not restart automatically, run:

```powershell
docker desktop restart
```

Expected: Docker Desktop restarts or reconfigures its engine. A temporary outage of the platform containers is expected.

### Task 3: Recover the Existing Compose Stack

**Files:**
- Read only: `C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform\docker-compose.yml`

- [ ] **Step 1: Wait on engine readiness, not a fixed sleep**

Poll:

```powershell
docker desktop status
docker version --format "{{.Server.Version}}"
```

Expected: status is running and the server version command exits 0. If readiness does not return, report the actual Docker status without resetting Docker data.

- [ ] **Step 2: Inspect platform service recovery**

Run from `C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform`:

```powershell
docker compose ps
```

Expected: all seven long-running services are up/healthy. `minio-init` may be exited successfully because it is a one-shot initializer.

- [ ] **Step 3: Start only missing platform services if needed**

Only when Step 2 shows missing services, run:

```powershell
docker compose up -d
```

Expected: existing volumes are reused and all long-running services become healthy.

### Task 4: Verify the Green Network and Service State

**Files:**
- No file changes.

- [ ] **Step 1: Verify MaClawSrv container HTTPS egress**

Run:

```powershell
docker exec ep_maclaw_runtime curl -sS -o /dev/null -w "%{http_code} %{ssl_verify_result}" https://api.deepseek.com/chat/completions
```

Expected after repair: HTTP 401 and SSL verify result 0. The 401 is correct because this probe deliberately sends no model credential.

- [ ] **Step 2: Verify backend container HTTPS egress**

Run:

```powershell
docker exec ep_backend curl -sS -o /dev/null -w "%{http_code} %{ssl_verify_result}" https://api.deepseek.com/chat/completions
```

Expected: HTTP 401 and SSL verify result 0.

- [ ] **Step 3: Verify internal service links**

Run:

```powershell
curl.exe -sS -o NUL -w "%{http_code}" http://localhost:8080/api/v1/health
docker exec ep_backend curl -sS -o /dev/null -w "%{http_code}" http://maclaw-runtime:18080/health
```

Expected: both return HTTP 200.

- [ ] **Step 4: Confirm the proxy mode no longer references the dead port**

Read only the Docker proxy-mode and proxy-endpoint fields again.

Expected: Docker Desktop is in System proxy mode and no active Docker proxy field references 17890.

### Task 5: Verify the Original Enterprise Message Symptom

**Files:**
- Read only: `C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform\internal\api\handler\maclaw_errors.go`

- [ ] **Step 1: Ask the user to send one benign enterprise-portal message**

Use an existing enterprise session and send a short greeting or capability question. Browser automation is not used because the local portal is blocked by the browser-control security policy.

- [ ] **Step 2: Correlate only sanitized access and MaClaw error metadata**

Inspect the newest backend access entry for:

```text
POST /api/v1/maclaw/evaluation/sessions/{session}/messages
```

Expected: HTTP 200, not 502. Inspect the MaClaw log only for transport/error markers; do not print message content or model output. Expected: no new TLS EOF or `LLM call failed` for that request.

- [ ] **Step 3: Handle any newly exposed provider-level error separately**

If TLS succeeds but the model provider returns an authenticated HTTP error (for example invalid model or credential), record only the safe status/error class and diagnose that as a separate configuration problem. Do not classify the Docker proxy repair as failed when the original TLS EOF is gone, but do not claim enterprise messaging is fixed until the portal request returns 200.

- [ ] **Step 4: Run final workspace hygiene checks**

Run:

```powershell
cd C:\Users\wangboyang\Desktop\evaluating_platform
git status --short
git diff --check
```

Expected: no application source changes; only the approved design/plan commits plus the pre-existing untracked `evaluating_platform/tmp_/` and `reports/` directories.
