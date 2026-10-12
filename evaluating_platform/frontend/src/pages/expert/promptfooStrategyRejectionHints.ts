/**
 * Phase 3 开篇 · 任务 2 · 自定义策略发布拒绝码 → 专家可读解读。
 *
 * # 为什么和插件那张表分开而不是合并
 *
 * 两者码空间不同（`pf_strategy_*` vs `pf_plugin_*`），语义也不同：插件是攻击
 * **生成器**（generator/grader），策略是攻击手法的**包装维度**（strategyText）。
 * 合并成一张表会让 `pf_strategy_form_unsupported` 显示成插件的文案，而两者的
 * 拒绝理由其实不一样 —— 对策略来说，JS 形态不是「暂不支持」，而是**根本不存在
 * 这种形态**：promptfoo 的 file:// 策略只接受 .js/.ts。这条区别正是 D-03 把
 * 策略也拒在 YAML 之外时最需要说清楚的理由，所以文案必须分开。
 *
 * 覆盖完整性由 `promptfooStrategyRejectionHints.test.ts` 断言：它手里有一份
 * 后端拒绝码清单，两边对不上时测试变红。
 */

/** 后端 `internal/maclaw/promptfoo_custom_strategy.go` 的全部发布拒绝码。 */
export const BACKEND_STRATEGY_REJECTION_CODES = [
  'pf_strategy_form_unsupported',
  'pf_strategy_definition_invalid',
  'pf_strategy_id_invalid',
  'pf_strategy_id_conflicts_catalog',
  'plugin_requires_remote_generation',
  'pf_strategy_executable_reference_forbidden',
  'pf_strategy_payload_too_large',
  'pf_strategy_remote_reference_forbidden',
] as const

export type PromptfooStrategyRejectionCode = (typeof BACKEND_STRATEGY_REJECTION_CODES)[number]

/**
 * 拒绝码 → 专家能据以行动的一句话。
 *
 * 每条都必须给出**下一步**，而不只是描述失败。
 */
export const PROMPTFOO_STRATEGY_REJECTION_HINTS: Record<PromptfooStrategyRejectionCode, string> = {
  pf_strategy_form_unsupported:
    '策略包只支持 YAML：promptfoo 的 file:// 策略只接受 .js/.ts，那会在引擎进程内执行任意代码。请把 strategyText 写成 YAML。',
  pf_strategy_definition_invalid:
    'YAML 不符合策略定义（strategyText 必填且不能为空，且不允许未声明字段）。promptfoo 不会校验字段名，拼错的字段不会报错也不会生效。',
  pf_strategy_executable_reference_forbidden: '策略内容里不允许出现 file:// 之类的可执行引用。',
  pf_strategy_remote_reference_forbidden:
    '策略内容里不允许出现外部地址：攻击模型的调用端点由平台注入（redteamProvider 不接受包内指定）。',
  pf_strategy_id_invalid: '策略 id 必须是 custom:<变体名> 形式，变体名限 [A-Za-z0-9._-] 且 1~64 字符。',
  pf_strategy_id_conflicts_catalog:
    '策略 id 与内置目录同名，会让报告把该策略的探针归到内置策略名下。请换一个变体名。',
  plugin_requires_remote_generation:
    '策略声明了依赖 promptfoo 远程生成的插件，而本环境已强制关闭远程生成。请改为不依赖远程生成。',
  pf_strategy_payload_too_large: '策略内容超过 128KB 上限。请只保留攻击方法说明，删掉内嵌的大段语料。',
}

/**
 * 取解读文案；未知码返回 undefined，由调用方回落到后端原文。
 *
 * 与插件表同一取舍：刻意**不**兜底成通用文案，后端原文比通用提示具体。
 */
export function promptfooStrategyRejectionHint(code: string | undefined): string | undefined {
  if (!code) return undefined
  return (PROMPTFOO_STRATEGY_REJECTION_HINTS as Record<string, string | undefined>)[code]
}