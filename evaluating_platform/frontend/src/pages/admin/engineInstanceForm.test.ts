import { describe, expect, it } from 'vitest'

import { ENGINE_URL_MAX_LEN, normalizeEngineBaseUrl, validateEngineBaseUrl } from './engineInstanceForm'

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
