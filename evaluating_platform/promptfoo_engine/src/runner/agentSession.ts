/**
 * agentSession.ts — U9 · multi-turn agent sessions (`conversation_id`).
 *
 * # What this module is for
 *
 * Agent targets were v1-single-turn: every probe was an independent HTTP call
 * with no memory of the previous one. Multi-turn attack strategies
 * (crescendo / goat / hydra) therefore could not form a real conversation
 * against an agent target — promptfoo re-sent a growing transcript as one
 * prompt, and a stateful agent endpoint either ignored it or answered
 * something unrelated. The report said "multi-turn" while nothing about the
 * run was multi-turn.
 *
 * Dify-style endpoints fix this the other way round: the **server** owns the
 * conversation. `POST /v1/chat-messages` with an empty `conversation_id` opens
 * a new conversation and returns a `conversation_id`; every later turn must
 * send that id back to stay in the same conversation.
 *
 * So U9's whole job is: **extract the id from turn 1's response, and send it
 * back on turn 2+.** Everything here exists to serve that.
 *
 * # Why the engine owns the mapping (design constraint, verified)
 *
 * The design note for U9 assumed "promptfoo's http provider is stateless, so
 * the engine must maintain `session_id ↔ conversation_id ↔ history` itself".
 * The first half of that is **false for promptfoo 0.123.0, and measurably so**
 * — the http provider has had native session support all along:
 *
 *   - `HttpProviderConfigSchema.sessionParser` (dist `providers-*.js`) — a JS
 *     expression evaluated against `{ headers, body }`; its return value becomes
 *     `ProviderResponse.sessionId`.
 *   - `HttpProviderConfigSchema.session` — an optional *separate* endpoint that
 *     pre-mints a session id. **Not needed for Dify**: Dify mints the id itself
 *     as a side effect of the first chat message, so there is nothing to fetch
 *     up front. We deliberately do not use it.
 *   - `callApi` puts `vars.sessionId` into the nunjucks render context, so
 *     `{{sessionId}}` in the body template resolves per request.
 *   - the multi-turn strategy providers (`crescendo/goat/hydra`) copy
 *     `response.sessionId` back into `vars.sessionId` for the next turn —
 *     **but only when `stateful: true`** (`crescendo-*.js`:
 *     `if (lastResponse.sessionId && this.stateful) vars.sessionId = …`).
 *
 * Measured (U9 e2e, real promptfoo, local Dify-alike): with `sessionParser`
 * + `stateful: true` + `{{sessionId}}`, turn 2's HTTP body carries turn 1's
 * `conversation_id`. Without any of the three it does not.
 *
 * So the engine's job is **not** to re-implement a session store that promptfoo
 * already has — that would be a second, worse copy of upstream state (U8 §2.10.1
 * is precisely about guards pinned to the wrong seam). The engine's job is:
 *
 *   1. render `{{conversation_id}}` — the operator-facing spelling — into
 *      `{{sessionId}}`, the name promptfoo actually binds;
 *   2. derive `sessionParser` from the operator-configured response path
 *      instead of making them write a JS expression;
 *   3. force `stateful: true` on the multi-turn strategies **when** a
 *      conversation id is in play, because promptfoo silently drops the id
 *      otherwise — a green multi-turn run that is secretly N independent calls;
 *   4. refuse the combination that cannot work, instead of running it.
 *
 * # Why the mapping is safe (no unbounded growth, no leak)
 *
 * There is deliberately **no engine-side session map**. promptfoo's session
 * state is per-`callApi`-context: `vars` for one test case, replaced each turn,
 * discarded when the provider returns. Nothing accumulates on the engine, so
 * there is nothing to leak and nothing to clean up. The engine's only
 * residue-free obligations are:
 *
 *   - never copy the id into `SafeRunResult`, progress events, or logs
 *     (`redact.ts` / the reducer are the exits that enforce this);
 *   - never let it reach the browser (the BFF has no field for it).
 *
 * The one place an id *can* outlive a turn is promptfoo's own SQLite, because
 * Plan C persists rows (U7 §4.8.5, 方案 A). That is already handled by
 * `promptfooStateCleanup.ts` on every terminal path — U9 adds no new persistence
 * and does not widen that surface.
 */

/** Operator-facing placeholder for the server-minted conversation id. */
export const AGENT_CONVERSATION_PLACEHOLDER = '{{conversation_id}}';

/**
 * promptfoo's variable name for "the id to send back this turn". Not an
 * operator-facing spelling — the engine translates into it, so a promptfoo
 * rename would be a one-line change here rather than a config migration.
 */
const PROMPTFOO_SESSION_VAR = '{{sessionId}}';

/**
 * promptfoo strategy ids whose provider threads a session id across turns.
 *
 * Verified against promptfoo 0.123.0's own strategy list at runtime by
 * `assertMultiTurnStrategyIdsAreCurrent` — the same anti-drift guard U7's
 * `assertNativeStrategyMapIsCurrent` provides for the strategy *vocabulary*.
 * `goat`/`hydra` carry the same `stateful` + `sessionId` handling as
 * `crescendo` (verified in dist: `goat-*.js`, `hydra-*.js`).
 */
export const MULTI_TURN_STRATEGY_IDS: readonly string[] = ['crescendo', 'goat', 'jailbreak:hydra', 'jailbreak:goblin'];

/** Error codes, part of the engine↔BFF contract so the UI can explain a refusal. */
export const AGENT_SESSION_ERR_NO_EXTRACT_PATH =
  'agent_session_requires_response_path';
export const AGENT_SESSION_ERR_MULTITURN_MISSING =
  'agent_session_requires_multiturn_strategy';
export const AGENT_SESSION_ERR_STATEFUL_FALSE =
  'agent_session_stateful_false_contradiction';

/** True when the agent config opts into a server-owned conversation. */
export function wantsAgentConversation(target: {
  agent_body_template?: string;
  agent_headers_template?: string;
}): boolean {
  return (
    containsPlaceholder(target.agent_body_template) || containsPlaceholder(target.agent_headers_template)
  );
}

function containsPlaceholder(template?: string): boolean {
  return typeof template === 'string' && template.includes(AGENT_CONVERSATION_PLACEHOLDER);
}

/**
 * Rewrite `{{conversation_id}}` into the name promptfoo binds (`{{sessionId}}`).
 *
 * Only the two template fields are touched. The endpoint is deliberately NOT
 * rewritten: a conversation id in a URL would end up in access logs and
 * `Referer` headers on the target side, which is exactly where a
 * conversation-scoped handle must not appear. A body/header placement works and
 * is what Dify documents.
 */
export function renderConversationPlaceholders(template: string): string {
  return template.split(AGENT_CONVERSATION_PLACEHOLDER).join(PROMPTFOO_SESSION_VAR);
}

/**
 * Translate an operator response path into the JS expression promptfoo's
 * `sessionParser` evaluates against `{ headers, body }`.
 *
 * The operator already wrote this path once, for `agent_response_path`
 * (`reply.text`, `choices[0].message.content`, `conversation_id`). Reusing that
 * one spelling for both jobs is the point: asking them to also write a JS
 * expression would be a second, undocumented dialect for the same concept, and
 * D-04's target user is configuring a Dify app, not writing JavaScript.
 *
 * `data` is promptfoo's `{ headers, body }` object — verified in dist:
 * `createSessionParser` compiles `new Function('data', 'return (' + parser + ')')`
 * and `callApi` invokes it as `sessionParser({ headers, body: parsedData })`.
 */
export function buildSessionParser(responsePath: string): string {
  return `data.body${toJsAccessor(responsePath)} ?? ''`;
}

/**
 * Convert a dot/bracket path (`a.b[0].c`) into the accessor suffix promptfoo
 * appends to `data.body`. Bracket indices become `.n` because JS strings index
 * with square brackets only for computed members; `.0` is equivalent here and
 * survives the YAML round trip without quoting surprises.
 */
export function toJsAccessor(responsePath: string): string {
  const path = (responsePath ?? '')
    .trim()
    .replace(/^json\./i, '')
    .replace(/^\.+/, '')
    .replace(/\.+$/, '');
  if (!path) return '';
  let out = '';
  for (const rawSegment of path.split('.')) {
    const segment = rawSegment.trim();
    if (!segment) continue;
    const match = /^([^[\]]+)((\[\d+\])*)$/.exec(segment);
    if (!match) {
      // Not a path we can compile. Emitting it would produce a parser that
      // throws at response time, mid-run. `toSessionAccessor` is validated
      // upstream by ValidateAgentTargetInput, so this is a belt-and-braces
      // fallback rather than a reachable branch.
      return '';
    }
    out += `.${match[1]}`;
    for (const index of (match[2] ?? '').match(/\d+/g) ?? []) out += `.${index}`;
  }
  return out;
}

/**
 * Fail closed on a conversation config that cannot possibly work.
 *
 * Three refusals, all cases where promptfoo would produce a **green run that is
 * not what it claims** — the failure shape this engine keeps working to
 * eliminate (U6/U7/U8 all exist for it):
 *
 *  1. `{{conversation_id}}` present but no `agent_response_path`. There would
 *     be no way to learn the id, so every turn would send an empty one and the
 *     agent would open a fresh conversation per turn — silently single-turn,
 *     while the operator asked for multi-turn and the report said multi-turn.
 *  2. `{{conversation_id}}` present but no multi-turn strategy selected. Same
 *     outcome for the same reason: one prompt per conversation.
 *  3. `{{conversation_id}}` present **and** an explicit `stateful: false` on the
 *     multi-turn strategy. This one is a genuine contradiction rather than a
 *     gap: the operator has asked for a server-owned conversation and, in the
 *     same breath, for promptfoo not to thread it. Honoring the flag yields the
 *     fake-multi-turn run above; silently overriding it would make the platform
 *     ignore an explicit instruction without saying so. Refusing names the
 *     conflict and leaves the choice with the operator.
 *
 * All three are refused with a readable code rather than downgraded, because the
 * operator's intent is unambiguous and the failure is invisible in the numbers.
 */
export function assertAgentConversationIsExecutable(input: {
  wantsConversation: boolean;
  agentResponsePath: string;
  promptfooStrategyIds: readonly string[];
  /** Explicit per-strategy config, when the caller has any (see refusal 3). */
  promptfooStrategyConfigs?: ReadonlyMap<string, Record<string, unknown> | undefined>;
}): void {
  if (!input.wantsConversation) return;
  if (!input.agentResponsePath.trim()) {
    throw new Error(
      `[${AGENT_SESSION_ERR_NO_EXTRACT_PATH}] 请求体模板里用了 ${AGENT_CONVERSATION_PLACEHOLDER}，但没有配置「响应提取路径」——` +
        '平台无法知道会话 id 放在响应的哪个字段，每一轮都会发空 id，Agent 每次都新开一段对话：' +
        '这会得到一份「多轮」报告，而实际每轮都是独立单轮。请填写响应提取路径（如 conversation_id）。',
    );
  }
  const multiTurn = input.promptfooStrategyIds.filter((id) => MULTI_TURN_STRATEGY_IDS.includes(id));
  if (multiTurn.length === 0) {
    throw new Error(
      `[${AGENT_SESSION_ERR_MULTITURN_MISSING}] 请求体模板里用了 ${AGENT_CONVERSATION_PLACEHOLDER}，但本次运行没有选择多轮策略` +
        `（${MULTI_TURN_STRATEGY_IDS.join(' / ')}）——单轮策略每条用例只发一次请求，会话 id 没有回传的机会。` +
        '请为本次评测选择多轮策略，或从请求体模板里去掉该占位符（去掉即保持 v1 单轮行为）。',
    );
  }
  const configs = input.promptfooStrategyConfigs;
  if (configs) {
    const contradicted = multiTurn.filter((id) => configs.get(id)?.stateful === false);
    if (contradicted.length > 0) {
      throw new Error(
        `[${AGENT_SESSION_ERR_STATEFUL_FALSE}] 多轮策略 ${contradicted.join(' / ')} 被显式配置为 stateful: false，` +
          `同时请求体模板又要求回传 ${AGENT_CONVERSATION_PLACEHOLDER}——这两项互相矛盾：` +
          'stateful:false 会让引擎每轮都发空会话 id，Agent 每次新开对话，报告却显示多轮。' +
          '请去掉 stateful:false（平台会自动置为 true 以回传会话 id），或从请求体模板里去掉该占位符。',
      );
    }
  }
}

/**
 * Force `stateful: true` on every multi-turn strategy in the list.
 *
 * This is the load-bearing bit. promptfoo's crescendo/goat/hydra only copy
 * `response.sessionId` into `vars.sessionId` when `stateful` is true
 * (`crescendo-*.js:190`). Left unset it defaults to **false**, and the run
 * completes normally — full strategy transcript, plausible report, and a
 * stateful agent that received a fresh conversation on every single turn. The
 * number of probes would even look right.
 *
 * So the flag is not an optimisation, it is the difference between a real
 * multi-turn run and a convincingly-reported single-turn one. It is set here
 * rather than left to the operator because there is no correct way to ask for
 * `{{conversation_id}}` and *not* want the id threaded.
 *
 * A caller-supplied `stateful: true` is preserved (nothing to change). An
 * explicit `stateful: false` alongside a conversation placeholder is a
 * contradiction and is **refused upstream** by
 * `assertAgentConversationIsExecutable` — so by the time this runs, forcing the
 * value here cannot silently overrule an operator.
 */
export function applyStatefulToMultiTurn<T extends { id: string; config?: Record<string, unknown> }>(
  strategies: readonly T[],
): Array<{ id: string; config?: Record<string, unknown> }> {
  let changed = false;
  const out = strategies.map((strategy): { id: string; config?: Record<string, unknown> } => {
    if (!MULTI_TURN_STRATEGY_IDS.includes(strategy.id)) return strategy;
    const config = strategy.config ?? {};
    if (config.stateful === true) return strategy;
    changed = true;
    return { ...strategy, config: { ...config, stateful: true } };
  });
  // Returning the input array unchanged when nothing changed keeps the common
  // case allocation-free and, more importantly, keeps the "no conversation
  // configured ⇒ byte-identical config document" property that U9's backward
  // compatibility rests on.
  return changed ? out : (strategies as unknown as Array<{ id: string; config?: Record<string, unknown> }>);
}

/**
 * Anti-drift guard: every id in MULTI_TURN_STRATEGY_IDS must still exist in the
 * installed promptfoo.
 *
 * Without it, a promptfoo upgrade that drops `jailbreak:goblin` would leave
 * this module claiming a session guarantee for a strategy that no longer
 * exists — and `assertAgentConversationIsExecutable` would then *refuse* runs
 * for a capability the operator can no longer select anyway. Fail loud instead.
 */
export function assertMultiTurnStrategyIdsAreCurrent(available: readonly string[]): void {
  const set = new Set(available);
  const missing = MULTI_TURN_STRATEGY_IDS.filter((id) => !set.has(id));
  if (missing.length > 0) {
    throw new Error(
      `[U9] multi-turn strategy map is stale: the installed promptfoo does not expose ${missing.join(', ')}. ` +
        'A promptfoo upgrade changed its strategy vocabulary — update src/runner/agentSession.ts before relying on ' +
        'conversation_id multi-turn targets.',
    );
  }
}

/**
 * Tool-call signals detected in one target response.
 *
 * # Why the engine looks at this at all
 *
 * U9's fourth requirement is side-effect isolation *in the engine*, not only in
 * the docs. The docs tell the operator to point the evaluation at a sandbox;
 * this detects whether the target *looked like* it reached for a tool, so a run
 * against a production endpoint is visible as such instead of being
 * indistinguishable from a clean one.
 *
 * What is deliberately NOT done: nothing here blocks, and no tool arguments are
 * kept. Only the **count** and the **signal flag** survive reduction. Tool
 * arguments are attacker-influenced content — the exact category AGENTS.md
 * forbids from reaching reports — so a detector that kept them would be a leak
 * wearing a safety label.
 *
 * The shape is a small union of conventions seen in practice (Dify's
 * `agent_outputs`/tool nodes, OpenAI-style `tool_calls`, n8n-style `toolCalls`,
 * and the `metadata.toolCalls` promptfoo's own n8n provider produces). This is
 * a **heuristic for visibility**, and is documented as such: it is not a claim
 * that every tool invocation is detected, only that a response carrying any of
 * these markers is surfaced rather than silently ignored.
 */
export interface ToolCallSignal {
  /** Number of distinct tool-call markers found in this response. */
  count: number;
}

/**
 * Count tool-call markers in a raw target response.
 *
 * `raw` is whatever the http provider parsed out of the response body
 * (`ProviderResponse.raw`). Never called with a string: a string body would
 * mean the response was not JSON, in which case there is no structure to read
 * and the honest answer is "no signal" rather than a regex over prose.
 */
export function countToolCallSignals(raw: unknown): ToolCallSignal {
  if (raw === null || typeof raw !== 'object') return { count: 0 };
  let count = 0;
  const seen = new Set<unknown>();
  walk(raw, seen, (key, value) => {
    // Only *non-empty* markers count. `tool_calls: []` is how most frameworks
    // spell "no tools were used", and counting it would flag every clean run.
    if (value === null || value === undefined) return;
    if (Array.isArray(value)) {
      if (value.length > 0) count += 1;
      return;
    }
    if (typeof value === 'object') {
      count += 1;
      return;
    }
    if (typeof value === 'string' && value.trim().length > 0) count += 1;
    else if (typeof value === 'number' || typeof value === 'boolean') count += 1;
  });
  return { count };
}

/** Keys that indicate the target reached for a tool. */
const TOOL_CALL_KEYS: ReadonlySet<string> = new Set([
  'tool_calls',
  'toolCalls',
  'tool_call',
  'function_call',
  'functionCall',
  'tool_use',
  'toolUse',
  'actions',
  'agent_outputs',
  'iteration_steps',
]);

/**
 * Walk a parsed response, reporting values stored under a tool-call key.
 *
 * Depth is bounded: responses are attacker-influenced, and an unbounded walk
 * over a deeply nested (or cyclic, once something JSON-parsed it is acyclic but
 * the engine also sees objects built in-process) structure would be a cheap
 * way to burn CPU on a single probe.
 */
function walk(node: unknown, seen: Set<unknown>, onValue: (key: string, value: unknown) => void): void {
  const MAX_DEPTH = 12;
  const visit = (current: unknown, depth: number): void => {
    if (depth > MAX_DEPTH) return;
    if (current === null || typeof current !== 'object') return;
    if (seen.has(current)) return;
    seen.add(current);
    if (Array.isArray(current)) {
      for (const item of current) visit(item, depth + 1);
      return;
    }
    for (const [key, value] of Object.entries(current as Record<string, unknown>)) {
      if (TOOL_CALL_KEYS.has(key)) onValue(key, value);
      visit(value, depth + 1);
    }
  };
  visit(node, 0);
}