export interface IdempotentSubmission {
  fingerprint: string
  key: string
}

const generateRequestKey = (prefix: string) => `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2)}`

export function prepareIdempotentSubmission(
  current: IdempotentSubmission | null,
  prefix: string,
  payload: unknown,
  generate: (prefix: string) => string = generateRequestKey,
): IdempotentSubmission {
  const fingerprint = JSON.stringify(payload)
  if (current?.fingerprint === fingerprint) return current
  return { fingerprint, key: generate(prefix) }
}
