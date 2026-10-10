import { expect, test } from 'vitest'
import { parseChatMarkdown } from './chatMarkdown'

test('parseChatMarkdown parses heading, paragraph and list blocks', () => {
  const blocks = parseChatMarkdown(`# 能力说明

你好，我可以帮助你完成：
* **模板样本评估**
* *越狱风险检测*
`)

  expect(blocks[0]?.type).toBe('heading')
  if (blocks[0]?.type !== 'heading') throw new Error('Expected first block to be a heading')
  expect(blocks[0].level).toBe(1)
  expect(blocks[0].children[0]?.text).toBe('能力说明')
  expect(blocks[1]?.type).toBe('paragraph')
  expect(blocks[2]?.type).toBe('list')
  if (blocks[2]?.type !== 'list') throw new Error('Expected third block to be a list')
  expect(blocks[2].items.length).toBe(2)
  expect(blocks[2].items[0]?.[0]?.strong).toBe(true)
  expect(blocks[2].items[0]?.[0]?.text).toBe('模板样本评估')
  expect(blocks[2].items[1]?.[0]?.emphasis).toBe(true)
  expect(blocks[2].items[1]?.[0]?.text).toBe('越狱风险检测')
})
