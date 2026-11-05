import { describe, expect, it } from 'vitest'

import { ENGINE_URL_MAX_LEN, engineInstanceHealthTag, normalizeEngineBaseUrl, validateEngineBaseUrl } from './engineInstanceForm'

describe('validateEngineBaseUrl', () => {
  it('接受 compose 内网与 https 地址', () => {
    expect(validateEngineBaseUrl('http://promptfoo-engine-acme:8090')).toBeNull()
    expect(validateEngineBaseUrl('https://engine.example.com/v1')).toBeNull()
  })

  it('拒绝后端同款的坏输入', () => {
    expect(validateEngineBaseUrl('')).not.toBeNull()
    expect(validateEngineBaseUrl('promptfoo-engine:8090')).not.toBeNull()
    expect(validateEngineBaseUrl('http://user:pass@host')).not.toBeNull()
    expect(validateEngineBaseUrl('http://host?token=x')).not.toBeNull()
    expect(validateEngineBaseUrl('http://host#f')).not.toBeNull()
    expect(validateEngineBaseUrl('ftp://host')).not.toBeNull()
    expect(validateEngineBaseUrl('x'.repeat(ENGINE_URL_MAX_LEN + 1))).not.toBeNull()
  })

  it('归一化与后端同步：去尾部斜杠', () => {
    expect(normalizeEngineBaseUrl(' http://h:1/ ')).toBe('http://h:1')
  })
})

describe('engineInstanceHealthTag', () => {
  it('token_mismatch 是 51 §8.1 的静默 401，必须有可执行的处置提示', () => {
    const tag = engineInstanceHealthTag('token_mismatch')
    expect(tag?.color).toBe('orange')
    expect(tag?.label).toContain('token')
    expect(tag?.hint).toContain('.env')
    expect(tag?.hint).toContain('401')
  })

  it('ok/不可达/半登记各有对应色与文案', () => {
    expect(engineInstanceHealthTag('ok')?.color).toBe('green')
    expect(engineInstanceHealthTag('unreachable')?.color).toBe('red')
    expect(engineInstanceHealthTag('incomplete')?.label).toBe('半登记')
  })

  it('未探活（undefined/未知值）返回 null，不显示误导标签', () => {
    expect(engineInstanceHealthTag(undefined)).toBeNull()
    expect(engineInstanceHealthTag('something-new')).toBeNull()
  })
})
