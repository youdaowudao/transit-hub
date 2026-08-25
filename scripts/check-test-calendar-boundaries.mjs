import { createRequire } from 'node:module'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const typescriptPath = fileURLToPath(new URL('../frontend/node_modules/typescript/lib/typescript.js', import.meta.url))
const ts = require(typescriptPath)
const scriptDir = dirname(fileURLToPath(import.meta.url))

const args = process.argv.slice(2)
let root = resolve(scriptDir, '..')
for (let index = 0; index < args.length; index += 1) {
  if (args[index] === '--root' && args[index + 1]) {
    root = resolve(args[index + 1])
    index += 1
    continue
  }
  throw new Error(`unsupported argument: ${args[index]}`)
}

const ignoredDirectories = new Set(['.git', 'node_modules', 'dist', 'coverage'])
const fixedCalendarStringPattern = /20\d{2}-\d{2}-\d{2}/
const goFixedCalendarConstructorPattern = /\btime\s*\.\s*Date\s*\(\s*20\d{2}\b/
const goLiveCalendarPatterns = [
  /\bbusinesstime\s*\.\s*Today\s*\(/,
  /\bbusinesstime\s*\.\s*DateAt\s*\(\s*time\s*\.\s*Now\s*\(/,
  /\btime\s*\.\s*Now\s*\(\s*\)[^=;\n]{0,240}?\.\s*Format\s*\(\s*["`]2006-01-02["`]\s*\)/,
]
const vitestCallableModifiers = new Set(['each', 'for', 'runIf', 'skipIf'])
const vitestPropertyModifiers = new Set(['concurrent', 'fails', 'only', 'sequential', 'skip', 'todo'])

const stripComments = (source) => {
  let result = ''
  let state = 'code'
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index]
    const next = source[index + 1]

    if (state === 'line-comment') {
      if (char === '\n') {
        result += char
        state = 'code'
      } else result += ' '
      continue
    }
    if (state === 'block-comment') {
      if (char === '*' && next === '/') {
        result += '  '
        state = 'code'
        index += 1
      } else result += char === '\n' ? '\n' : ' '
      continue
    }
    if (state !== 'code') {
      result += char
      if (char === '\\' && state !== 'raw' && index + 1 < source.length) {
        result += source[index + 1]
        index += 1
        continue
      }
      if ((state === 'single' && char === "'") || (state === 'double' && char === '"') || (state === 'raw' && char === '`')) {
        state = 'code'
      }
      continue
    }

    if (char === '/' && next === '/') {
      result += '  '
      state = 'line-comment'
      index += 1
    } else if (char === '/' && next === '*') {
      result += '  '
      state = 'block-comment'
      index += 1
    } else {
      result += char
      if (char === "'") state = 'single'
      else if (char === '"') state = 'double'
      else if (char === '`') state = 'raw'
    }
  }
  return result
}

const replaceStringLiteralsForCodeScan = (source) => {
  let result = ''
  for (let index = 0; index < source.length; index += 1) {
    const quote = source[index]
    if (quote !== "'" && quote !== '"' && quote !== '`') {
      result += quote
      continue
    }

    let literal = quote
    for (index += 1; index < source.length; index += 1) {
      const char = source[index]
      literal += char
      if (char === '\\' && quote !== '`' && index + 1 < source.length) {
        literal += source[index + 1]
        index += 1
      } else if (char === quote) break
    }
    result += /^["'`]2006-01-02["'`]$/.test(literal) ? literal : '__string__'
  }
  return result
}

const hasGoLiveCalendar = (source) => {
  if (goLiveCalendarPatterns.some(pattern => pattern.test(source))) return true

  const capturedClock = /\b([A-Za-z_][A-Za-z0-9_]*)\s*(?::=|=(?!=))\s*time\s*\.\s*Now\s*\(\s*\)/g
  for (let match = capturedClock.exec(source); match; match = capturedClock.exec(source)) {
    const escapedName = match[1].replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const remainingSource = source.slice(capturedClock.lastIndex)
    const formattedDate = new RegExp(`\\b${escapedName}\\b[^=;\\n]{0,240}?\\.\\s*Format\\s*\\(\\s*["\`]2006-01-02["\`]\\s*\\)`)
    const businessDate = new RegExp(`\\bbusinesstime\\s*\\.\\s*DateAt\\s*\\(\\s*${escapedName}\\b`)
    if (formattedDate.test(remainingSource) || businessDate.test(remainingSource)) return true
  }
  return false
}

const collectFiles = (directory, predicate, files = []) => {
  if (!existsSync(directory)) return files
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.isDirectory() && ignoredDirectories.has(entry.name)) continue
    const path = join(directory, entry.name)
    if (entry.isDirectory()) collectFiles(path, predicate, files)
    else if (entry.isFile() && predicate(entry.name)) files.push(path)
  }
  return files
}

const findClosingBrace = (source, openIndex) => {
  let depth = 0
  let state = 'code'
  for (let index = openIndex; index < source.length; index += 1) {
    const char = source[index]
    const next = source[index + 1]

    if (state === 'line-comment') {
      if (char === '\n') state = 'code'
      continue
    }
    if (state === 'block-comment') {
      if (char === '*' && next === '/') {
        state = 'code'
        index += 1
      }
      continue
    }
    if (state !== 'code') {
      if (char === '\\') {
        index += 1
        continue
      }
      if ((state === 'single' && char === "'") || (state === 'double' && char === '"') || (state === 'raw' && char === '`')) {
        state = 'code'
      }
      continue
    }

    if (char === '/' && next === '/') {
      state = 'line-comment'
      index += 1
    } else if (char === '/' && next === '*') {
      state = 'block-comment'
      index += 1
    } else if (char === "'") state = 'single'
    else if (char === '"') state = 'double'
    else if (char === '`') state = 'raw'
    else if (char === '{') depth += 1
    else if (char === '}') {
      depth -= 1
      if (depth === 0) return index
    }
  }
  return -1
}

const goTestBlocks = (source) => {
  const blocks = []
  const declaration = /\bfunc\s+(Test[A-Za-z0-9_]+)\s*\([^)]*\)\s*\{/g
  for (let match = declaration.exec(source); match; match = declaration.exec(source)) {
    const openIndex = declaration.lastIndex - 1
    const closeIndex = findClosingBrace(source, openIndex)
    if (closeIndex < 0) continue
    blocks.push({ name: match[1], source: source.slice(match.index, closeIndex + 1) })
    declaration.lastIndex = closeIndex + 1
  }
  return blocks
}

const vitestCallName = (node, testNames, namespaceNames) => {
  if (ts.isIdentifier(node) && testNames.has(node.text)) return node.text
  if (ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression) &&
      namespaceNames.has(node.expression.text) && (node.name.text === 'it' || node.name.text === 'test')) {
    return `${node.expression.text}.${node.name.text}`
  }
  if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) &&
      vitestCallableModifiers.has(node.expression.name.text)) {
    return vitestCallName(node.expression.expression, testNames, namespaceNames)
  }
  if (ts.isTaggedTemplateExpression(node) && ts.isPropertyAccessExpression(node.tag) && node.tag.name.text === 'each') {
    return vitestCallName(node.tag.expression, testNames, namespaceNames)
  }
  if (ts.isPropertyAccessExpression(node) && vitestPropertyModifiers.has(node.name.text)) {
    return vitestCallName(node.expression, testNames, namespaceNames)
  }
  return null
}

const numericCalendarYear = (node) => {
  if (!node || !ts.isNumericLiteral(node)) return null
  const year = Number(node.text)
  return Number.isInteger(year) && year >= 2000 && year <= 2099 ? year : null
}

const hasFixedClockValue = (node, fixedValues = new Map(), referencePosition = node.getStart()) => {
  if (ts.isIdentifier(node)) {
    const declarationPosition = fixedValues.get(node.text)
    return declarationPosition !== undefined && declarationPosition < referencePosition
  }
  if (ts.isNumericLiteral(node)) return true
  if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) && /20\d{2}-\d{2}-\d{2}/.test(node.text)) {
    return true
  }
  if (ts.isNewExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === 'Date') {
    const argumentsList = node.arguments ?? []
    if (argumentsList.length === 1) return hasFixedClockValue(argumentsList[0], fixedValues, referencePosition)
    return argumentsList.length >= 2 && numericCalendarYear(argumentsList[0]) !== null
  }
  return ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) &&
    ts.isIdentifier(node.expression.expression) && node.expression.expression.text === 'Date' &&
    node.expression.name.text === 'UTC' && node.arguments.length >= 2 && numericCalendarYear(node.arguments[0]) !== null
}

const isVitestTimerOwner = (node, timerNames, namespaceNames) => {
  if (ts.isIdentifier(node)) return timerNames.has(node.text)
  return ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression) &&
    namespaceNames.has(node.expression.text) && node.name.text === 'vi'
}

const fakeTimerOptionsHaveFixedNow = (node, fixedValues, referencePosition) => {
  if (!node || !ts.isObjectLiteralExpression(node)) return false
  return node.properties.some(property => {
    if (!ts.isPropertyAssignment(property)) return false
    const name = ts.isIdentifier(property.name) || ts.isStringLiteral(property.name) ? property.name.text : ''
    return name === 'now' && hasFixedClockValue(property.initializer, fixedValues, referencePosition)
  })
}

const collectFixedConstValues = (statements, inheritedValues = new Map()) => {
  const fixedValues = new Map(inheritedValues)
  for (const statement of statements) {
    if (!ts.isVariableStatement(statement) || !(statement.declarationList.flags & ts.NodeFlags.Const)) continue
    for (const declaration of statement.declarationList.declarations) {
      if (ts.isIdentifier(declaration.name) && declaration.initializer &&
          hasFixedClockValue(declaration.initializer, fixedValues, declaration.getStart())) {
        fixedValues.set(declaration.name.text, declaration.getStart())
      }
    }
  }
  return fixedValues
}

const javascriptCalendarFlags = (
  callback,
  timerNames,
  namespaceNames,
  inheritedFixedSystemTime = false,
  inheritedFixedValues = new Map(),
) => {
  let hasFixedCalendar = false
  const timerEvents = []
  const callbackBody = ts.isBlock(callback.body) ? callback.body : null
  const fixedValues = collectFixedConstValues(callbackBody?.statements ?? [], inheritedFixedValues)
  const visit = (node) => {
    if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) &&
        /20\d{2}-\d{2}-\d{2}/.test(node.text)) {
      hasFixedCalendar = true
    } else if (ts.isTemplateExpression(node) && /20\d{2}-\d{2}-\d{2}/.test(node.getText())) {
      hasFixedCalendar = true
    }

    if (ts.isNewExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === 'Date') {
      const argumentsList = node.arguments ?? []
      if (argumentsList.length === 0) timerEvents.push({ position: node.getStart(), type: 'live-clock' })
      else if (argumentsList.length >= 2 && numericCalendarYear(argumentsList[0]) !== null) hasFixedCalendar = true
    }
    if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) &&
        ts.isIdentifier(node.expression.expression) && node.expression.expression.text === 'Date') {
      if (node.expression.name.text === 'now' && node.arguments.length === 0) {
        timerEvents.push({ position: node.getStart(), type: 'live-clock' })
      }
      if (node.expression.name.text === 'UTC' && node.arguments.length >= 2 && numericCalendarYear(node.arguments[0]) !== null) {
        hasFixedCalendar = true
      }
    }
    if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) &&
        isVitestTimerOwner(node.expression.expression, timerNames, namespaceNames) && callbackBody &&
        ts.isExpressionStatement(node.parent) && node.parent.parent === callbackBody) {
      const method = node.expression.name.text
      if (method === 'useFakeTimers') {
        const fixedNow = fakeTimerOptionsHaveFixedNow(node.arguments[0], fixedValues, node.getStart())
        if (fixedNow) hasFixedCalendar = true
        timerEvents.push({ position: node.getStart(), type: fixedNow ? 'fixed-fake-timers' : 'fake-timers' })
      }
      else if (method === 'useRealTimers') timerEvents.push({ position: node.getStart(), type: 'real-timers' })
      else if (method === 'setSystemTime' && node.arguments[0] &&
          hasFixedClockValue(node.arguments[0], fixedValues, node.getStart())) {
        hasFixedCalendar = true
        timerEvents.push({ position: node.getStart(), type: 'fixed-system-time' })
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(callback)

  let fakeTimersActive = inheritedFixedSystemTime
  let fixedSystemTimeActive = inheritedFixedSystemTime
  let hasLiveCalendar = false
  timerEvents.sort((left, right) => left.position - right.position)
  for (const event of timerEvents) {
    if (event.type === 'fake-timers' || event.type === 'fixed-fake-timers') {
      fakeTimersActive = true
      fixedSystemTimeActive = event.type === 'fixed-fake-timers'
    } else if (event.type === 'fixed-system-time') {
      if (fakeTimersActive) fixedSystemTimeActive = true
    } else if (event.type === 'real-timers') {
      fakeTimersActive = false
      fixedSystemTimeActive = false
    } else if (!fakeTimersActive || !fixedSystemTimeActive) {
      hasLiveCalendar = true
    }
  }
  return { hasFixedCalendar, hasLiveCalendar, endsWithFixedSystemTime: fakeTimersActive && fixedSystemTimeActive }
}

const vitestSuiteCallName = (node, suiteNames, namespaceNames) => {
  if (ts.isIdentifier(node) && suiteNames.has(node.text)) return node.text
  if (ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression) &&
      namespaceNames.has(node.expression.text) && (node.name.text === 'describe' || node.name.text === 'suite')) {
    return `${node.expression.text}.${node.name.text}`
  }
  if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) &&
      vitestCallableModifiers.has(node.expression.name.text)) {
    return vitestSuiteCallName(node.expression.expression, suiteNames, namespaceNames)
  }
  if (ts.isPropertyAccessExpression(node) && vitestPropertyModifiers.has(node.name.text)) {
    return vitestSuiteCallName(node.expression, suiteNames, namespaceNames)
  }
  return null
}

const suiteScopesForNode = (node, suiteNames, namespaceNames, sourceFile) => {
  const scopes = []
  for (let ancestor = node.parent; ancestor; ancestor = ancestor.parent) {
    if ((ts.isArrowFunction(ancestor) || ts.isFunctionExpression(ancestor)) &&
        ts.isCallExpression(ancestor.parent) && ancestor.parent.arguments.includes(ancestor) &&
        vitestSuiteCallName(ancestor.parent.expression, suiteNames, namespaceNames)) {
      scopes.push(ancestor)
    }
  }
  scopes.push(sourceFile)
  return scopes
}

const isVitestHookCall = (node, hookNames, namespaceNames) => {
  if (ts.isIdentifier(node)) return hookNames.has(node.text)
  return ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression) &&
    namespaceNames.has(node.expression.text) && node.name.text === 'beforeEach'
}

const vitestTestBlocks = (filePath, source) => {
  const kind = filePath.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  const sourceFile = ts.createSourceFile(filePath, source, ts.ScriptTarget.Latest, true, kind)
  const testNames = new Set(['it', 'test'])
  const timerNames = new Set(['vi'])
  const suiteNames = new Set(['describe', 'suite'])
  const beforeEachNames = new Set(['beforeEach'])
  const namespaceNames = new Set()
  for (const statement of sourceFile.statements) {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier) ||
        statement.moduleSpecifier.text !== 'vitest' || !statement.importClause?.namedBindings) continue
    const bindings = statement.importClause.namedBindings
    if (ts.isNamespaceImport(bindings)) {
      namespaceNames.add(bindings.name.text)
      continue
    }
    for (const specifier of bindings.elements) {
      const importedName = specifier.propertyName?.text ?? specifier.name.text
      if (importedName === 'it' || importedName === 'test') testNames.add(specifier.name.text)
      if (importedName === 'vi') timerNames.add(specifier.name.text)
      if (importedName === 'describe' || importedName === 'suite') suiteNames.add(specifier.name.text)
      if (importedName === 'beforeEach') beforeEachNames.add(specifier.name.text)
    }
  }
  const fixedTimerHookScopes = new Set()
  const fixedValuesForScopes = (scopes) => {
    let fixedValues = new Map()
    for (const scope of [...scopes].reverse()) {
      const statements = scope === sourceFile
        ? sourceFile.statements
        : ts.isBlock(scope.body) ? scope.body.statements : []
      fixedValues = collectFixedConstValues(statements, fixedValues)
    }
    return fixedValues
  }
  const collectFixedTimerHooks = (node) => {
    if (ts.isCallExpression(node) && isVitestHookCall(node.expression, beforeEachNames, namespaceNames)) {
      const callback = node.arguments.find(argument => ts.isArrowFunction(argument) || ts.isFunctionExpression(argument))
      if (callback) {
        const scopes = suiteScopesForNode(node, suiteNames, namespaceNames, sourceFile)
        const scope = scopes[0]
        const scopeBody = scope === sourceFile ? sourceFile : scope.body
        if (ts.isExpressionStatement(node.parent) && node.parent.parent === scopeBody &&
            javascriptCalendarFlags(
              callback,
              timerNames,
              namespaceNames,
              false,
              fixedValuesForScopes(scopes),
            ).endsWithFixedSystemTime) {
          fixedTimerHookScopes.add(scope)
        }
      }
    }
    ts.forEachChild(node, collectFixedTimerHooks)
  }
  collectFixedTimerHooks(sourceFile)

  const blocks = []
  const visit = (node) => {
    if (ts.isCallExpression(node) && vitestCallName(node.expression, testNames, namespaceNames) && node.arguments.length >= 2) {
      const nameNode = node.arguments[0]
      const callback = node.arguments.slice(1).find(argument => ts.isArrowFunction(argument) || ts.isFunctionExpression(argument))
      if (callback) {
        const line = sourceFile.getLineAndCharacterOfPosition(nameNode.getStart(sourceFile)).line + 1
        const name = ts.isStringLiteral(nameNode) || ts.isNoSubstitutionTemplateLiteral(nameNode)
          ? nameNode.text
          : `dynamic test at line ${line}`
        const scopes = suiteScopesForNode(node, suiteNames, namespaceNames, sourceFile)
        const inheritedFixedSystemTime = scopes.some(scope => fixedTimerHookScopes.has(scope))
        blocks.push({
          name,
          source: callback.getText(sourceFile),
          ...javascriptCalendarFlags(
            callback,
            timerNames,
            namespaceNames,
            inheritedFixedSystemTime,
            fixedValuesForScopes(scopes),
          ),
        })
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)
  return blocks
}

const violations = []
let scannedFiles = 0
let scannedTests = 0

const inspect = (filePath, blocks, liveDetector) => {
  scannedFiles += 1
  for (const block of blocks) {
    scannedTests += 1
    const uncommentedSource = stripComments(block.source)
    const fixedCalendarSource = uncommentedSource.replace(/(["'`])2006-01-02\1/g, '')
    const liveCalendarSource = replaceStringLiteralsForCodeScan(uncommentedSource)
    const hasFixedCalendar = block.hasFixedCalendar ?? (
      fixedCalendarStringPattern.test(fixedCalendarSource) || goFixedCalendarConstructorPattern.test(liveCalendarSource)
    )
    const hasLiveCalendar = block.hasLiveCalendar ?? liveDetector(liveCalendarSource)
    if (hasFixedCalendar && hasLiveCalendar) {
      violations.push(`${relative(root, filePath)}:${block.name}`)
    }
  }
}

for (const filePath of collectFiles(join(root, 'backend'), name => name.endsWith('_test.go'))) {
  const source = readFileSync(filePath, 'utf8')
  inspect(filePath, goTestBlocks(source), hasGoLiveCalendar)
}

for (const filePath of collectFiles(join(root, 'frontend', 'tests'), name => /\.(?:test|spec)\.tsx?$/.test(name))) {
  const source = readFileSync(filePath, 'utf8')
  inspect(filePath, vitestTestBlocks(filePath, source), () => false)
}

if (violations.length > 0) {
  for (const violation of violations) {
    console.error(`test calendar boundary violation: ${violation} mixes a fixed calendar date with the live calendar clock`)
  }
  process.exitCode = 1
} else {
  console.log(`test calendar boundary guard: passed (${scannedTests} tests in ${scannedFiles} files)`)
}
