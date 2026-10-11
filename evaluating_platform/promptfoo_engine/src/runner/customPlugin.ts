/**
 * customPlugin.ts — U10 · loading expert-published YAML plugin packages.
 *
 * # Why a file, and why the engine writes it itself
 *
 * promptfoo's custom-plugin contract is a **file path**, not an inline object
 * (`CustomPlugin` constructor takes `filePath` and calls
 * `loadCustomPluginDefinition` → `maybeLoadFromExternalFile`,
 * `dist/src/index.js:15007`/`14992`). The plugin ref in the redteam config is
 * therefore `file://<path>.yaml`, and `plugins[].id.startsWith("file://")` is
 * the branch that reaches `CustomPlugin`.
 *
 * So the package has to exist as a file before promptfoo is called. The engine
 * writes it, into the **per-run workdir** — never a shared temp dir, never a
 * persistent directory. Two reasons, in order of importance:
 *
 *  1. The workdir is created per run and `rm -rf`'d on the terminal path
 *     (`WorkdirManager`). A package therefore cannot outlive its run, and a
 *     disabled package leaves nothing behind to be picked up by a later run.
 *  2. `nativeRedteam.ts` already writes its config document there *because*
 *     that document embeds the materialized generation API key. Putting the
 *     expert's YAML in the same place keeps every per-run artifact in one
 *     directory with one lifecycle.
 *
 * # Why the BFF ships the definition inline
 *
 * The engine has no database access and no business having one. The BFF has
 * already (a) applied admin governance, (b) validated the YAML against
 * promptfoo's schema, and (c) re-serialized it canonically — so the string that
 * arrives here is byte-identical to the string that was validated. Shipping a
 * handle instead would mean the engine resolving it, which would move the
 * governance decision into a service that cannot see the publication table.
 *
 * # The naming rule
 *
 * The file name is derived from the plugin's own id, sanitized to
 * `[a-z0-9._-]`. Two properties matter and both are security properties, not
 * cosmetics:
 *
 *  - **No traversal.** A package id of `../../etc/passwd` must not be able to
 *    name a path outside the workdir. Sanitizing (rather than rejecting) is
 *    deliberate: the id is expert-authored text, and rejecting the whole run
 *    over a stray slash would be a worse failure than writing a file with a
 *    mangled name. Anything that sanitizes to empty falls back to a hash.
 *  - **No collision.** Two packages whose ids sanitize to the same name would
 *    silently overwrite each other inside one workdir, and the run would report
 *    one plugin's probes under the other's id. A short hash of the *original* id
 *    is appended, so distinct ids cannot collide.
 */
import { createHash } from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
// The same YAML library promptfoo itself uses, so the two agree on what a
// document means (anchors, multi-line scalars, quoting). Using a second parser
// here would mean validating against one dialect and executing another.
import { parse as parseYaml } from 'yaml';

/** One package as it arrives on RunRequest. Mirrors Go's EngineCustomPlugin. */
export interface EngineCustomPlugin {
  handle: string;
  plugin_id: string;
  definition: string;
  source_expert_user_id?: string;
  version?: string;
}

/** A package after it has been written into the run's workdir. */
export interface MaterializedCustomPlugin {
  /** promptfoo's own plugin id (the package's `id`, or promptfoo's default). */
  pluginId: string;
  /** Absolute path of the written YAML file, inside the per-run workdir. */
  filePath: string;
  /** The `file://` reference to put in the redteam config's plugins array. */
  fileRef: string;
  /** The publication handle, for error messages. Never a local path. */
  handle: string;
}

/** Machine-readable failure codes. Mirrored by the BFF; part of the contract. */
export const CUSTOM_PLUGIN_ERR_INVALID = 'custom_plugin_definition_invalid';

/** Directory (inside the workdir) that packages are written into. */
const CUSTOM_PLUGIN_DIR = 'custom-plugins';

/** promptfoo's fallback id when a package declares none (measured, dist:15015). */
export const PROMPTFOO_CUSTOM_DEFAULT_ID = 'promptfoo:redteam:custom';

/** Id prefix the BFF uses to mark a plugin ref as a published package. */
export const CUSTOM_PLUGIN_MARKER = 'promptfoo-custom:';

/**
 * promptfoo's `CustomPluginDefinitionSchema` is a **strictObject** with exactly
 * these keys (dist/src/index.js:14985). Duplicated here as a *runtime* check
 * rather than trusted from the BFF, because the engine is a separately
 * deployable service with its own REST surface — a request that reaches it
 * directly (older BFF, replayed capture, future caller) must not be able to
 * write an arbitrary file into the workdir and have promptfoo `import()` it.
 *
 * This is the same two-gate argument U6 makes for the remote-generation guard.
 */
const ALLOWED_DEFINITION_KEYS = new Set(['generator', 'grader', 'threshold', 'metric', 'id']);

/** Raised when a package cannot be materialized. Carries a readable message. */
export class CustomPluginError extends Error {
  constructor(
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'CustomPluginError';
  }
}

/**
 * Reject a package the engine will not write.
 *
 * The two checks that matter for safety:
 *  - **extension**: only `.yaml` is written. promptfoo's loader treats a
 *    `file://x.js` reference as an **import** (`loadCallbackFromFileUrl` →
 *    `importModule`), so a package must never be able to choose its own
 *    extension. The name is derived from the plugin id and the extension is
 *    appended here, so this holds by construction — the check is the assertion
 *    that says so.
 *  - **shape**: keys outside the five promptfoo declares. promptfoo would throw
 *    per-plugin and continue, leaving a green run with the package silently
 *    absent — the exact failure U10 exists to prevent, so it is caught before
 *    anything is written.
 */
export function assertPublishableDefinition(plugin: EngineCustomPlugin): void {
  const definition = (plugin.definition ?? '').trim();
  if (!definition) {
    throw new CustomPluginError(CUSTOM_PLUGIN_ERR_INVALID, `自定义插件 ${plugin.handle} 的内容为空，拒绝加载。`);
  }
  let parsed: unknown;
  try {
    parsed = parseYaml(definition);
  } catch {
    throw new CustomPluginError(
      CUSTOM_PLUGIN_ERR_INVALID,
      `自定义插件 ${plugin.handle} 不是合法的 YAML，拒绝加载。请让专家修正后重新发布。`,
    );
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new CustomPluginError(
      CUSTOM_PLUGIN_ERR_INVALID,
      `自定义插件 ${plugin.handle} 的顶层必须是键值映射（generator / grader / threshold / metric / id）。`,
    );
  }
  const extra = Object.keys(parsed as Record<string, unknown>).filter((key) => !ALLOWED_DEFINITION_KEYS.has(key));
  if (extra.length > 0) {
    throw new CustomPluginError(
      CUSTOM_PLUGIN_ERR_INVALID,
      `自定义插件 ${plugin.handle} 包含未声明字段 ${extra.join(', ')}。` +
        'promptfoo 以 strictObject 解析插件定义，未声明字段会在运行时被直接拒绝（且只记一条 error 日志、整次评测仍返回成功），因此在加载前拦下。',
    );
  }
  const record = parsed as Record<string, unknown>;
  if (typeof record.generator !== 'string' || !record.generator.trim()) {
    throw new CustomPluginError(CUSTOM_PLUGIN_ERR_INVALID, `自定义插件 ${plugin.handle} 缺少 generator。`);
  }
  if (typeof record.grader !== 'string' || !record.grader.trim()) {
    throw new CustomPluginError(CUSTOM_PLUGIN_ERR_INVALID, `自定义插件 ${plugin.handle} 缺少 grader。`);
  }
}

/**
 * Derive a collision-free, traversal-proof file name for a package.
 *
 * `handle` is preferred over the plugin id because it is the platform-issued
 * stable key (admin governance acts on it) and is already constrained by the
 * publication path. The plugin id is appended as a short hash of the *original*
 * value so two ids that sanitize identically cannot overwrite each other.
 */
export function customPluginFileName(plugin: EngineCustomPlugin): string {
  const basis = `${plugin.handle ?? ''}:${plugin.plugin_id ?? ''}`;
  const digest = createHash('sha256').update(basis).digest('hex').slice(0, 12);
  const sanitized = (plugin.handle ?? '')
    .toLowerCase()
    // Anything outside the allow-list becomes a single dash, so a path
    // separator cannot survive.
    .replace(/[^a-z0-9._-]+/g, '-')
    // Then collapse dot-runs. This step is what actually removes traversal:
    // `a/../../b` becomes `a-..-..-b` after the first replace, and a `..`
    // anywhere in a filename is a hazard for any future consumer that joins it
    // onto a base path without re-checking. Cheaper to never produce one.
    .replace(/\.{2,}/g, '.')
    // A leading dot would hide the file; a leading dash is noise.
    .replace(/^[.-]+/, '')
    .slice(0, 48);
  // `sanitized` can legitimately be empty (a handle of "///" or ".."), so the
  // hash is the real identity and the readable part is a convenience.
  return `${sanitized || 'plugin'}-${digest}.yaml`;
}

/**
 * Write every package into the run's workdir and return the `file://` refs.
 *
 * Called from `buildNativeRedteamConfig` — the single seam that produces the
 * promptfoo config document (U8's placement rule: a guard or a transform that
 * lives at a call site can be deleted with the suite still green).
 */
export async function materializeCustomPlugins(
  plugins: EngineCustomPlugin[] | undefined,
  workdir: string,
): Promise<MaterializedCustomPlugin[]> {
  if (!plugins || plugins.length === 0) return [];
  // Validate **every** package before creating the directory or writing
  // anything. Validating inside the write loop would leave an empty
  // `custom-plugins/` behind when the first package is bad, and — worse — would
  // leave the earlier packages of a partially-valid batch on disk for a later
  // run to pick up.
  for (const plugin of plugins) {
    assertPublishableDefinition(plugin);
  }
  // Refuse a name collision up front, for the same reason: two packages mapping
  // to one filename means one silently overwrites the other inside a single
  // workdir, and the run reports one package's probes under the other's id.
  const names = new Set<string>();
  for (const plugin of plugins) {
    const name = customPluginFileName(plugin);
    if (names.has(name)) {
      throw new CustomPluginError(
        CUSTOM_PLUGIN_ERR_INVALID,
        `自定义插件 ${plugin.handle} 与本次运行中的另一个插件映射到同一文件名，拒绝加载以免互相覆盖。`,
      );
    }
    names.add(name);
  }
  const dir = path.join(workdir, CUSTOM_PLUGIN_DIR);
  await fs.mkdir(dir, { recursive: true, mode: 0o700 });
  const out: MaterializedCustomPlugin[] = [];
  for (const plugin of plugins) {
    const name = customPluginFileName(plugin);
    const filePath = path.join(dir, name);
    // mode 0600: the workdir also holds the materialized generation API key, so
    // the packages are written with the same restriction rather than relying on
    // the directory's permissions alone.
    await fs.writeFile(filePath, ensureTrailingNewline(plugin.definition), { encoding: 'utf8', mode: 0o600 });
    out.push({
      pluginId: (plugin.plugin_id ?? '').trim() || PROMPTFOO_CUSTOM_DEFAULT_ID,
      filePath,
      fileRef: `file://${filePath}`,
      handle: plugin.handle,
    });
  }
  return out;
}

/**
 * Replace the BFF's `promptfoo-custom:` marker ids with the materialized
 * `file://` refs, leaving every built-in plugin id untouched.
 *
 * Returns a new array rather than mutating: the caller's array is the parsed
 * request body, and the same ids are still needed for the run record.
 */
export function applyCustomPluginRefs(
  plugins: Array<{ id: string; config?: Record<string, unknown> }>,
  materialized: MaterializedCustomPlugin[],
): Array<{ id: string; config?: Record<string, unknown> }> {
  if (materialized.length === 0) return plugins;
  // Marker id → file ref. Keyed on the full marker string so a plugin id that
  // merely *contains* the marker as a substring cannot be rewritten.
  const byMarker = new Map<string, string>();
  for (const item of materialized) {
    byMarker.set(`${CUSTOM_PLUGIN_MARKER}pf_plugin:${item.handle}`, item.fileRef);
  }
  return plugins.map((plugin) => {
    const fileRef = byMarker.get(plugin.id);
    return fileRef ? { id: fileRef, ...(plugin.config ? { config: plugin.config } : {}) } : plugin;
  });
}

/**
 * Map a materialized package's plugin id back onto the ids promptfoo reports.
 *
 * promptfoo stamps `metadata.pluginId = plugin.id` on every generated test
 * (`addLanguageToPluginMetadata`, dist:16461) — and on the custom path that
 * `plugin.id` is the **`file://` path**. So a report built straight from
 * `metadata.pluginId` would carry the engine's local filesystem path, which
 * AGENTS.md forbids outright ("must never include … local paths") and which
 * would also be useless to a reader.
 *
 * This is why the ref substitution above is not enough on its own: the id has to
 * be translated in **both** directions. `reportPluginID` is applied to every row
 * before reduction.
 */
export function reportPluginID(pluginId: string, materialized: MaterializedCustomPlugin[]): string {
  for (const item of materialized) {
    if (pluginId === item.fileRef || pluginId === item.filePath) return item.pluginId;
  }
  return pluginId;
}

/** Ensure the written document ends with a newline, like a hand-written file. */
function ensureTrailingNewline(text: string): string {
  return text.endsWith('\n') ? text : `${text}\n`;
}