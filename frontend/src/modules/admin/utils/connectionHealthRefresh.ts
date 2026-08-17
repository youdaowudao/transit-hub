export interface RefreshCoordinator {
  begin: () => number | null
  beginAutomatic: () => number | null
  complete: (generation: number) => boolean
  isManualRefreshActive: () => boolean
  isRefreshActive: () => boolean
  shouldRunAutomaticRefresh: () => boolean
}

export const createRefreshCoordinator = (): RefreshCoordinator => {
  let nextGeneration = 0
  let activeGeneration: number | null = null
  let activeMode: 'manual' | 'automatic' | null = null

  return {
    begin: () => {
      if (activeGeneration !== null) return null
      const generation = ++nextGeneration
      activeGeneration = generation
      activeMode = 'manual'
      return generation
    },
    beginAutomatic: () => {
      if (activeGeneration !== null) return null
      const generation = ++nextGeneration
      activeGeneration = generation
      activeMode = 'automatic'
      return generation
    },
    complete: (generation: number) => {
      if (activeGeneration !== generation) return false
      activeGeneration = null
      activeMode = null
      return true
    },
    isManualRefreshActive: () => activeMode === 'manual',
    isRefreshActive: () => activeGeneration !== null,
    shouldRunAutomaticRefresh: () => activeGeneration === null,
  }
}
