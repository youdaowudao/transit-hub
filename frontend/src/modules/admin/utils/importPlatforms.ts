const importAccountPlatforms = new Set(['openai', 'anthropic', 'gemini', 'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax'])

export const isImportAccountPlatform = (platform: string): boolean => importAccountPlatforms.has(platform.trim().toLowerCase())
