/**
 * 包内自测（P28 上传期自动关卡实跑）：零依赖、离线、秒级。
 *
 * 直接静态导入 entry 并调用 action——引擎探针与关卡对包的全部要求就是
 * 「tests/run.mjs 存在、能跑、退出码为 0」。断言覆盖 loader 契约（非空
 * vars / 非空 assert）、配对结构（预热含 Canary、受害者不含）、Canary 唯一
 * 与三个业务面轮转。
 */
import plugin from '../src/plugin.ts';

const failures = [];
const check = (cond, msg) => {
  if (!cond) failures.push(msg);
};

// ── 1. 基本契约：n=6 返回 6 条，形状满足 loader 强检 ──
const six = await plugin.action({ injectVar: 'prompt', n: 6, config: {} });
check(Array.isArray(six) && six.length === 6, `n=6 应返回 6 条，实际 ${Array.isArray(six) ? six.length : typeof six}`);
for (const [i, testCase] of six.entries()) {
  const prompt = testCase?.vars?.prompt;
  check(typeof prompt === 'string' && prompt.trim().length > 0, `case #${i + 1}: vars.prompt 必须是非空字符串`);
  check(Array.isArray(testCase.assert) && testCase.assert.length > 0, `case #${i + 1}: assert 必须是非空数组`);
  for (const assertion of testCase.assert ?? []) {
    check(typeof assertion.type === 'string' && assertion.type.length > 0, `case #${i + 1}: assert.type 必须非空`);
    check(typeof assertion.value === 'string' && assertion.value.length > 0, `case #${i + 1}: assert.value 必须非空`);
  }
}

// ── 2. 配对结构：偶数条=预热（含自己的 Canary），奇数条=受害者（不含任何
//      Canary），受害者断言必须带 not-contains 与 llm-rubric ──
const canaryOf = (i) => `CACHE_CANARY_2026_CASE_${String(i + 1).padStart(2, '0')}`;
for (let i = 0; i < six.length; i++) {
  const prompt = six[i].vars.prompt;
  if (i % 2 === 0) {
    check(prompt.includes(canaryOf(i)), `预热条 #${i + 1} 应包含 ${canaryOf(i)}`);
    check(prompt.includes('simulated'), `预热条 #${i + 1} 应声明模拟内容`);
  } else {
    check(!prompt.includes('CACHE_CANARY'), `受害者条 #${i + 1} 不得出现任何 Canary`);
    const types = six[i].assert.map((a) => a.type);
    check(types.includes('not-contains'), `受害者条 #${i + 1} 必须有 not-contains 断言`);
    check(types.includes('llm-rubric'), `受害者条 #${i + 1} 必须有 llm-rubric 断言`);
    check(
      six[i].assert.find((a) => a.type === 'not-contains')?.value === canaryOf(i - 1),
      `受害者条 #${i + 1} 的 not-contains 应针对预热条的 ${canaryOf(i - 1)}`,
    );
  }
}

// ── 3. 三个业务面在 n=6 时全部轮到（每面对一条配对）──
const allPrompts = six.map((c) => c.vars.prompt);
check(allPrompts.some((p) => p.includes('customer support knowledge base')), '缺少 customer-support 面');
check(allPrompts.some((p) => p.includes('demo order')), '缺少 order-status 面');
check(allPrompts.some((p) => p.includes('demo report')), '缺少 report-summary 面');

// ── 4. n 语义：n=3 时返回 3 条；n 缺省返回 1 条 ──
const three = await plugin.action({ injectVar: 'prompt', n: 3 });
check(three.length === 3, `n=3 应返回 3 条，实际 ${three.length}`);
const one = await plugin.action({ injectVar: 'prompt' });
check(one.length === 1, `缺省 n 应返回 1 条，实际 ${one.length}`);

// ── 5. 注入变量名跟随参数（loader 按当次 injectVar 校验）──
const custom = await plugin.action({ injectVar: 'user_input', n: 2 });
for (const [i, testCase] of custom.entries()) {
  check(typeof testCase.vars.user_input === 'string' && testCase.vars.user_input.length > 0, `injectVar=user_input 时 case #${i + 1} 缺 vars.user_input`);
  check(!('prompt' in testCase.vars), `injectVar=user_input 时 case #${i + 1} 不应携带 prompt 键`);
}

if (failures.length > 0) {
  console.error(`FAIL (${failures.length}):`);
  for (const f of failures) console.error(` - ${f}`);
  process.exit(1);
}
console.log('semantic-cache-collision: all checks passed');
