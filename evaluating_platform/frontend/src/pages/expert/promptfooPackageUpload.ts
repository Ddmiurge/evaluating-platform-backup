/**
 * 策略包文件上传的纯校验（P24，2026-10-30）。
 *
 * 设计稿 strategy-code-packages.md §1.2 定的第一步形态是「文件上传 + 纯文本回显」
 * （编辑器另议）：专家从本地选 .js/.mjs（代码包）或 .yaml/.yml（文本包），文本读进
 * 现有正文框，发布链路一字不改 —— BFF 存储出口校验与引擎实测校验才是真边界，
 * 这里的客户端检查只把「注定被拒的文件」挡在填表阶段。
 *
 * 上限与后端同源（CustomStrategyCodeMaxBytes / CustomStrategyMaxPayloadBytes），
 * 由 TestFrontendPackageUploadLimitsMatchBackend 钉住；按 UTF-8 字节计，与后端
 * len(source) 同一口径（按字符数计会放过 3 倍超标的中文文件）。
 */

export type PackageFormKind = 'yaml' | 'js'

export const PACKAGE_UPLOAD_LIMITS: Record<PackageFormKind, number> = {
  yaml: 128 * 1024, // == 后端 CustomStrategyMaxPayloadBytes
  js: 256 * 1024, // == 后端 CustomStrategyCodeMaxBytes
}

const PACKAGE_EXTENSIONS: Record<PackageFormKind, ReadonlySet<string>> = {
  yaml: new Set(['yaml', 'yml']),
  js: new Set(['js', 'mjs']),
}

export function acceptForForm(kind: PackageFormKind): string {
  return kind === 'js' ? '.js,.mjs' : '.yaml,.yml'
}

// 去 BOM：记事本存出来的 YAML/JS 常带 BOM，正文不该为此被拒。
export function stripBom(text: string): string {
  return text.charCodeAt(0) === 0xfeff ? text.slice(1) : text
}

function extensionOf(name: string): string {
  const match = /\.([A-Za-z0-9]+)$/.exec(String(name ?? ''))
  return (match?.[1] ?? '').toLowerCase()
}

export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

export interface PackageFileCheck {
  ok: boolean
  error?: string
}

export function validatePackageFile(name: string, text: string, kind: PackageFormKind): PackageFileCheck {
  const ext = extensionOf(name)
  const wanted = acceptForForm(kind)
  if (!PACKAGE_EXTENSIONS[kind].has(ext)) {
    const otherSide = PACKAGE_EXTENSIONS[kind === 'js' ? 'yaml' : 'js'].has(ext)
    if (otherSide) {
      return {
        ok: false,
        error:
          '「' + name + '」是另一种形态的扩展名，当前形态是「' +
          (kind === 'js' ? '代码策略' : '文本策略') + '」。切换上方形态选择器，或改用 ' + wanted + ' 文件。',
      }
    }
    return {
      ok: false,
      error: '策略包只接受 ' + wanted + ' 文件（代码包 .js/.mjs，文本包 .yaml/.yml）。「' + name + '」不符合，请换文件。',
    }
  }
  if (text.trim().length === 0) {
    return { ok: false, error: '「' + name + '」的内容是空的。请确认文件已保存，或换一份内容。' }
  }
  if (text.indexOf(String.fromCharCode(0)) >= 0) {
    return { ok: false, error: '「' + name + '」不像 UTF-8 文本（含空字节），请导出为纯文本后重试。' }
  }
  const limit = PACKAGE_UPLOAD_LIMITS[kind]
  const bytes = byteLength(text)
  if (bytes > limit) {
    return {
      ok: false,
      error: '「' + name + '」共 ' + bytes + ' 字节，超过该形态上限 ' + limit + ' 字节。请精简后再上传。',
    }
  }
  return { ok: true }
}

// slugFromName 的同一条规则，集中在这里：文件名建议与发布名推导必须一致。
export function slugifyVariant(raw: string): string {
  const ascii = String(raw ?? '')
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48)
  return ascii
}

// 代码包 variant 建议：去扩展名后 slug 化；纯中文名会退化成空串——
// 返回空表示「给不出可信建议」，由必填校验逼用户自己写，而不是塞一个无意义的。
export function variantSuggestionFromFilename(name: string): string {
  const base = String(name ?? '').replace(/\.[^.]+$/, '')
  return slugifyVariant(base)
}
