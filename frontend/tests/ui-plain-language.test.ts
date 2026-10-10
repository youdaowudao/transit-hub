import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import ts from 'typescript'
import { parse as parseSfc } from '@vue/compiler-sfc'
import { parse as parseTemplate, NodeTypes, type RootNode, type TemplateChildNode } from '@vue/compiler-dom'
import zh from '@/locales/zh'
const root = path.resolve(import.meta.dirname, '../src')
const files = (directory: string): string[] => readdirSync(directory, { withFileTypes: true }).flatMap(entry => entry.isDirectory() ? files(path.join(directory, entry.name)) : /\.(vue|ts)$/.test(entry.name) ? [path.join(directory, entry.name)] : [])
const chinese = ['租约', '回执', '收口', '对账', '权威', '矩阵', '契约', '宽限', '准确批次']
const english = ['source=', 'schedulable=', 'multiplier_only']
const withoutScriptComments = (source: string) => ts.createPrinter({ removeComments: true }).printFile(ts.createSourceFile('ui-copy.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS))
const templateTextNodes = (node: RootNode | TemplateChildNode): string[] => {
 if (node.type === NodeTypes.TEXT) return [node.content]
 if (node.type === NodeTypes.ROOT || node.type === NodeTypes.ELEMENT) return node.children.flatMap(templateTextNodes)
 return []
}
const stringValues = (value: unknown): string[] => typeof value === 'string' ? [value] : value && typeof value === 'object' ? Object.values(value).flatMap(stringValues) : []
describe('page wording uses plain explanations', () => {
 it('excludes internal Chinese terms from templates and all script strings, while ignoring comments', () => {
  const violations: string[] = []
  for (const file of files(root)) {
   const source = readFileSync(file, 'utf8')
   const descriptor = file.endsWith('.vue') ? parseSfc(source).descriptor : null
   const text = descriptor ? [descriptor.template?.content.replace(/<!--[\s\S]*?-->/g, '') ?? '', withoutScriptComments(descriptor.script?.content ?? ''), withoutScriptComments(descriptor.scriptSetup?.content ?? '')].join('\n') : withoutScriptComments(source)
   for (const term of chinese) if (text.includes(term)) violations.push(`${path.relative(root, file)}: ${term}`)
  }
  expect(violations).toEqual([])
 })
 it('excludes internal English phrases from Chinese string values and template text nodes', () => {
  const violations: string[] = []
  const localeText = stringValues(zh).join('\n')
  for (const term of english) if (localeText.includes(term)) violations.push(`locales/zh.ts: ${term}`)
  for (const file of files(root).filter(file => file.endsWith('.vue'))) {
   const source = parseSfc(readFileSync(file, 'utf8')).descriptor.template?.content
   if (!source) continue
   const text = templateTextNodes(parseTemplate(source)).join('\n')
   for (const term of english) if (text.includes(term)) violations.push(`${path.relative(root, file)}: ${term}`)
  }
  expect(violations).toEqual([])
 })
 it('keeps comment-like sequences inside strings and ignores code identifiers', () => {
  expect(withoutScriptComments("const message = 'https://example.test/收口'; // 权威\n/* 租约 */")).toContain('收口')
  expect(withoutScriptComments('// 收口\nconst message = "说明"')).not.toContain('收口')
  expect(templateTextNodes(parseTemplate('<span :value="multiplier_only">说明</span><!-- schedulable= -->'))).toEqual(['说明'])
 })
})
