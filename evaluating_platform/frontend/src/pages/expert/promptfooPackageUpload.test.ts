import { describe, expect, it } from 'vitest'

import {
  PACKAGE_UPLOAD_LIMITS,
  acceptForForm,
  byteLength,
  slugifyVariant,
  stripBom,
  validatePackageFile,
  variantSuggestionFromFilename,
} from './promptfooPackageUpload'

describe('validatePackageFile 扩展名', () => {
  it('代码形态只吃 .js/.mjs（大小写不敏感）', () => {
    expect(validatePackageFile('a.js', 'export default {}', 'js').ok).toBe(true)
    expect(validatePackageFile('A.MJS', 'export default {}', 'js').ok).toBe(true)
    expect(validatePackageFile('a.ts', 'x', 'js').ok).toBe(false)
    expect(validatePackageFile('a.cjs', 'x', 'js').ok).toBe(false)
  })

  it('拿文本包的扩展名打代码形态：错误文案点名「形态」，不是笼统「不支持」', () => {
    const check = validatePackageFile('strategy.yaml', 'a: 1', 'js')
    expect(check.ok).toBe(false)
    expect(check.error).toContain('形态')
  })

  it('未知扩展名给出接受清单', () => {
    const check = validatePackageFile('pack.zip', 'x', 'yaml')
    expect(check.ok).toBe(false)
    expect(check.error).toContain('.yaml')
  })
})

describe('validatePackageFile 内容', () => {
  it('空文件与纯空白都被拒', () => {
    expect(validatePackageFile('a.js', '', 'js').ok).toBe(false)
    expect(validatePackageFile('a.yaml', '   \n ', 'yaml').ok).toBe(false)
  })

  it('含空字节的二进制不像文本', () => {
    const check = validatePackageFile('a.js', 'ok' + String.fromCharCode(0) + 'bin', 'js')
    expect(check.ok).toBe(false)
    expect(check.error).toContain('UTF-8')
  })
})

describe('validatePackageFile 体积', () => {
  it('上限按 UTF-8 字节：中文按 3 倍膨胀计入，不是按字符数', () => {
    const limit = PACKAGE_UPLOAD_LIMITS.yaml
    const chinese = '策'.repeat(Math.floor(limit / 3) + 1)
    expect(chinese.length).toBeLessThan(limit) // 按字符数看「没超标」
    expect(byteLength(chinese)).toBeGreaterThan(limit)
    const check = validatePackageFile('a.yaml', chinese, 'yaml')
    expect(check.ok).toBe(false)
    expect(check.error).toContain('字节')
  })

  it('恰好在限值上的文件放行（后端是 > 拒绝，同口径）', () => {
    const exact = 'a'.repeat(PACKAGE_UPLOAD_LIMITS.js)
    expect(validatePackageFile('a.js', exact, 'js').ok).toBe(true)
  })

  it('BOM 会被剥掉，不影响判定', () => {
    const withBom = String.fromCharCode(0xfeff) + 'a: 1'
    expect(stripBom(withBom)).toBe('a: 1')
  })
})

describe('variant 建议', () => {
  it('文件名去扩展名后 slug 化，规则与 slugFromName 同源', () => {
    expect(variantSuggestionFromFilename('My Strategy-v2.MJS')).toBe('my-strategy-v2')
    expect(slugifyVariant('  Trim Me  ')).toBe('trim-me')
  })

  it('纯中文名给不出可信建议：返回空串，由必填校验逼用户显式写', () => {
    expect(variantSuggestionFromFilename('中文策略.mjs')).toBe('')
  })

  it('建议值限长 48', () => {
    expect(variantSuggestionFromFilename('x'.repeat(80) + '.js').length).toBeLessThanOrEqual(48)
  })
})

describe('acceptForForm', () => {
  it('两种形态的 accept 串互不重叠', () => {
    expect(acceptForForm('js')).toBe('.js,.mjs')
    expect(acceptForForm('yaml')).toBe('.yaml,.yml')
  })
})
