const openAIImportPlatforms = new Set(['openai', 'kimi', 'zhipu', 'deepseek', 'minimax'])

export const isOpenAIImportPlatform = (platform: string): boolean => openAIImportPlatforms.has(platform.trim().toLowerCase())

// Account eligibility is separate; the server also validates its platform whitelist.
export const importPlatformsCompatible = (upstreamPlatform: string, accountPlatform: string): boolean => {
  const source = upstreamPlatform.trim().toLowerCase()
  const target = accountPlatform.trim().toLowerCase()
  return source === target || isOpenAIImportPlatform(source) && isOpenAIImportPlatform(target)
}
