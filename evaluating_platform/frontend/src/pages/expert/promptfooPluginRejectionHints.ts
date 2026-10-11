/**
 * U10 · 自定义插件发布拒绝码 → 专家可读解读。
 *
 * # 为什么要单独一个模块
 *
 * 这张表同时被**组件**和**测试**需要。若测试从组件文件里复制一份，两个副本
 * 会各自演化 —— 后端新增一个拒绝码而前端没加解读时，测试仍然全绿，而这正是
 * DoD「清晰提示」会悄悄失效的方式。所以表在这里，组件与测试都从这里导入。
 *
 * 覆盖完整性由 `promptfooPluginRejectionHints.test.ts` 断言：它手里有一份后端
 * 拒绝码清单，两边对不上时测试变红。
 */

/** 后端 `internal/maclaw/promptfoo_custom_plugin.go` 的全部发布拒绝码。 */
export const BACKEND_REJECTION_CODES = [
  'pf_plugin_form_unsupported',
  'pf_plugin_definition_invalid',
  'pf_plugin_id_conflicts_catalog',
  'plugin_requires_remote_generation',
  'pf_plugin_executable_reference_forbidden',
  'pf_plugin_payload_too_large',
  'pf_plugin_remote_reference_forbidden',
] as const

export type PromptfooPluginRejectionCode = (typeof BACKEND_REJECTION_CODES)[number]

/**
 * 拒绝码 → 专家能据以行动的一句话。
 *
 * 每条都必须给出**下一步**，而不只是描述失败：「自定义插件仅支持 YAML」说的是
 * 规则，专家会怀疑是自己写错了；而「JS/脚本形态会在引擎进程内执行任意代码」
 * 说明的是**为什么**，专家才知道这不是他的问题、也没有绕过的办法。
 */
export const PROMPTFOO_PLUGIN_REJECTION_HINTS: Record<PromptfooPluginRejectionCode, string> = {
  pf_plugin_form_unsupported:
    'JS/脚本形态在首期不被支持：它会在引擎进程内执行任意代码。请改用 YAML。',
  pf_plugin_definition_invalid:
    'YAML 结构不符合 promptfoo 插件定义（generator 与 grader 必填，且不允许额外字段）。',
  pf_plugin_executable_reference_forbidden: '插件内容里不允许出现 file:// 之类的可执行引用。',
  pf_plugin_remote_reference_forbidden: '插件内容里不允许出现外部地址：生成与判定只走平台下发的模型端点。',
  pf_plugin_id_conflicts_catalog:
    '插件 id 与内置目录同名，会让报告借用内置插件的分类与判定标准。请换一个 id。',
  plugin_requires_remote_generation:
    '插件声明了依赖 promptfoo 远程生成的插件/策略，而本环境已强制关闭远程生成。请改为不依赖远程生成。',
  pf_plugin_payload_too_large:
    '插件内容超过 256KB 上限。请只保留 generator 模板与 grader 标准，删掉内嵌的大段语料或示例。',
}

/**
 * 取解读文案；未知码返回 undefined，由调用方回落到后端原文。
 *
 * 刻意**不**在这里兜底成一句通用文案：后端原文（如「自定义插件 id "harmful"
 * 与内置目录中的插件同名…」）比任何通用提示都具体，兜底会把它替换掉。
 */
export function promptfooPluginRejectionHint(code: string | undefined): string | undefined {
  if (!code) return undefined
  return (PROMPTFOO_PLUGIN_REJECTION_HINTS as Record<string, string | undefined>)[code]
}