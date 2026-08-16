export interface RefreshCoordinator {
  begin: () => number | null
  complete: (generation: number) => boolean
  isManualRefreshActive: () => boolean
  shouldRunAutomaticRefresh: () => boolean
}

export const createRefreshCoordinator = (): RefreshCoordinator => {
  let nextGeneration = 0
  let activeGeneration: number | null = null

  return {
    begin: () => {
      if (activeGeneration !== null) return null
      const generation = ++nextGeneration
      activeGeneration = generation
      return generation
    },
    complete: (generation: number) => {
      if (activeGeneration !== generation) return false
      activeGeneration = null
      return true
    },
    isManualRefreshActive: () => activeGeneration !== null,
    shouldRunAutomaticRefresh: () => activeGeneration === null,
  }
}
