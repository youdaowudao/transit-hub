<script setup lang="ts">
import { computed, ref } from 'vue'
import { AlertTriangle, ChevronDown, ChevronRight, Eye } from 'lucide-vue-next'

import { t } from '@/locales'
import type {
  AdminGroupAccount,
  AdminGroupHealth,
  PriorityCandidateHealthBand,
  PriorityCandidateProjection,
  PriorityCandidateRegion,
  PriorityCandidateSummary,
} from '../../types/connectionHealth'

const props = defineProps<{
  groups: AdminGroupHealth[]
  loaded: boolean
  platform: string
}>()

const excludedOpen = ref(false)
const emptySub2APISummary = (): PriorityCandidateSummary => ({
  mode: 'safety_lock',
  candidatePriorityReady: false,
  safetyReason: 'first_layer_unavailable',
  candidateCount: 0,
  outOfScopeCount: 0,
  blockerCount: 0,
  capacities: [
    { region: 'normal', healthBand: 'healthy', start: 10, end: 99, capacity: 90, actual: 0, remaining: 90, overflow: false },
    { region: 'normal', healthBand: 'recovering', start: 100, end: 999, capacity: 900, actual: 0, remaining: 900, overflow: false },
    { region: 'normal', healthBand: 'degraded', start: 1000, end: 9999, capacity: 9000, actual: 0, remaining: 9000, overflow: false },
    { region: 'hot_standby', healthBand: 'healthy', start: 10000, end: 39999, capacity: 30000, actual: 0, remaining: 30000, overflow: false },
    { region: 'hot_standby', healthBand: 'recovering', start: 40000, end: 69999, capacity: 30000, actual: 0, remaining: 30000, overflow: false },
    { region: 'hot_standby', healthBand: 'degraded', start: 70000, end: 99999, capacity: 30000, actual: 0, remaining: 30000, overflow: false },
  ],
})
const summary = computed(() => {
  const projected = props.groups.find(group => group.priorityCandidateSummary)?.priorityCandidateSummary
  if (projected) return projected
  return props.loaded && props.groups.length === 0 && props.platform.toLowerCase() === 'sub2api'
    ? emptySub2APISummary()
    : null
})

const accounts = computed(() => {
  const byTarget = new Map<string, AdminGroupAccount>()
  for (const group of props.groups) {
    for (const account of group.accounts) {
      const existing = byTarget.get(account.targetId)
      if (!existing || (!existing.priorityCandidate && account.priorityCandidate)) {
        byTarget.set(account.targetId, account)
      }
    }
  }
  return [...byTarget.values()]
})

const candidates = computed(() => accounts.value
  .filter(account => account.priorityCandidate?.state === 'candidate')
  .sort((left, right) => {
    const leftRank = left.priorityCandidate?.rank ?? Number.MAX_SAFE_INTEGER
    const rightRank = right.priorityCandidate?.rank ?? Number.MAX_SAFE_INTEGER
    return leftRank - rightRank || left.targetId.localeCompare(right.targetId)
  }))

const excluded = computed(() => accounts.value
  .filter(account => account.priorityCandidate && account.priorityCandidate.state !== 'candidate')
  .sort((left, right) => left.targetId.localeCompare(right.targetId)))

const modeLabel = computed(() => {
  switch (summary.value?.mode) {
    case 'first_active': return t('admin.connectionHealth.priorityCandidate.modes.firstActive')
    case 'second_active': return t('admin.connectionHealth.priorityCandidate.modes.secondActive')
    default: return t('admin.connectionHealth.priorityCandidate.modes.safetyLock')
  }
})

const healthBandLabel = (band?: PriorityCandidateHealthBand): string => {
  if (!band) return '-'
  return t(`admin.connectionHealth.priorityCandidate.healthBands.${band}`)
}

const regionLabel = (region?: PriorityCandidateRegion): string => {
  if (!region) return '-'
  return t(`admin.connectionHealth.priorityCandidate.regions.${region}`)
}

const reasonLabel = (reason?: string): string =>
  t(`admin.connectionHealth.priorityCandidate.reasons.${reason || 'unknown'}`)

const accountTierLabel = (account: AdminGroupAccount): string =>
  t(`admin.connectionHealth.accountTier.${account.accountTier === 2 ? 'second' : 'first'}`)

const priorityEvidenceLabel = (evidence?: string): string =>
  t(`admin.connectionHealth.priorityCandidate.evidence.${evidence || 'unknown'}`)

const currentPriorityLabel = (account: AdminGroupAccount): string =>
  account.priority == null
    ? t('admin.connectionHealth.priorityCandidate.currentPriorityUnknown')
    : t('admin.connectionHealth.priorityCandidate.currentPriority', { priority: account.priority })

const multiplierLabel = (account: AdminGroupAccount): string => {
  const multiplier = account.priorityCandidate?.multiplier
  return multiplier == null || !Number.isFinite(multiplier) ? '-' : `${multiplier}x`
}

const candidatePriorityLabel = (candidate?: PriorityCandidateProjection): string => {
  if (!summary.value?.candidatePriorityReady || candidate?.priority == null) return '-'
  return t('admin.connectionHealth.priorityCandidate.candidatePriority', { priority: candidate.priority })
}
</script>

<template>
  <section
    v-if="summary"
    class="overflow-hidden rounded-lg border border-border/60 bg-card text-card-foreground shadow-sm"
    :aria-label="t('admin.connectionHealth.priorityCandidate.title')"
  >
    <header class="flex flex-col gap-3 border-b border-border/50 px-5 py-4 lg:flex-row lg:items-start lg:justify-between">
      <div>
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="text-base font-semibold text-foreground">{{ t('admin.connectionHealth.priorityCandidate.title') }}</h2>
          <span class="rounded-md bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">{{ modeLabel }}</span>
        </div>
        <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.connectionHealth.priorityCandidate.readonly') }}</p>
      </div>
      <div v-if="!summary.candidatePriorityReady" class="flex max-w-xl items-start gap-2 text-sm text-amber-700 dark:text-amber-400">
        <AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />
        <div>
          <p class="font-medium">{{ t('admin.connectionHealth.priorityCandidate.notReady') }}</p>
          <p class="mt-0.5 text-xs">{{ reasonLabel(summary.safetyReason) }}</p>
        </div>
      </div>
    </header>

    <div class="grid gap-px bg-border/50 lg:grid-cols-[minmax(0,1.35fr)_minmax(24rem,1fr)]">
      <div class="min-w-0 bg-background px-5 py-4">
        <h3 class="text-xs font-semibold text-foreground">{{ t('admin.connectionHealth.priorityCandidate.orderTitle') }}</h3>
        <div v-if="candidates.length === 0" class="mt-3 text-sm text-muted-foreground">
          {{ t('admin.connectionHealth.priorityCandidate.noCandidates') }}
        </div>
        <ol v-else class="mt-3 divide-y divide-border/50 border-y border-border/50">
          <li
            v-for="account in candidates"
            :key="account.targetId"
            data-testid="priority-candidate-row"
            class="grid gap-2 py-3 text-xs sm:grid-cols-[3rem_minmax(0,1.2fr)_minmax(0,1fr)] sm:items-center"
          >
            <span class="font-semibold tabular-nums text-foreground">#{{ account.priorityCandidate?.rank ?? '-' }}</span>
            <span class="min-w-0">
              <span class="block truncate font-medium text-foreground">{{ account.name || account.id }}</span>
              <span class="block truncate text-[11px] text-muted-foreground">{{ account.targetId }}</span>
            </span>
            <span class="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground">
              <span class="font-medium text-foreground">{{ candidatePriorityLabel(account.priorityCandidate) }}</span>
              <span>{{ regionLabel(account.priorityCandidate?.region) }}</span>
              <span>{{ healthBandLabel(account.priorityCandidate?.healthBand) }}</span>
              <span>{{ accountTierLabel(account) }}</span>
              <span>{{ priorityEvidenceLabel(account.priorityCandidate?.priorityEvidence) }}</span>
              <span>{{ multiplierLabel(account) }}</span>
              <span>{{ account.priorityCandidate?.successLatencyMs ?? '-' }} ms</span>
            </span>
          </li>
        </ol>
      </div>

      <div class="min-w-0 bg-background px-5 py-4">
        <h3 class="text-xs font-semibold text-foreground">{{ t('admin.connectionHealth.priorityCandidate.capacityTitle') }}</h3>
        <div class="mt-3 divide-y divide-border/50 border-y border-border/50">
          <div
            v-for="capacity in summary.capacities"
            :key="`${capacity.region}:${capacity.healthBand}`"
            data-testid="priority-capacity-row"
            class="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-2 text-xs"
          >
            <span class="min-w-0 text-muted-foreground">
              <span class="font-medium text-foreground">{{ regionLabel(capacity.region) }} · {{ healthBandLabel(capacity.healthBand) }}</span>
              <span class="ml-2 tabular-nums">{{ capacity.start }}-{{ capacity.end }}</span>
            </span>
            <span class="text-right tabular-nums" :class="capacity.overflow ? 'text-amber-700 dark:text-amber-400' : 'text-muted-foreground'">
              {{ capacity.actual }} / {{ capacity.capacity }} · {{ t('admin.connectionHealth.priorityCandidate.remaining', { count: capacity.remaining }) }}
            </span>
          </div>
        </div>
      </div>
    </div>

    <div class="border-t border-border/50 px-5 py-3">
      <button
        type="button"
        class="inline-flex items-center gap-2 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        aria-label="查看未纳入账号"
        :aria-expanded="excludedOpen"
        @click="excludedOpen = !excludedOpen"
      >
        <ChevronDown v-if="excludedOpen" class="h-4 w-4" />
        <ChevronRight v-else class="h-4 w-4" />
        <Eye class="h-4 w-4" />
        {{ t('admin.connectionHealth.priorityCandidate.excludedTitle', { count: excluded.length }) }}
      </button>
      <div v-if="excludedOpen" class="mt-3 divide-y divide-border/50 border-y border-border/50">
        <div
          v-for="account in excluded"
          :key="account.targetId"
          data-testid="priority-excluded-row"
          class="flex flex-col gap-1 py-2.5 text-xs sm:flex-row sm:items-center sm:justify-between sm:gap-4"
        >
          <span class="min-w-0">
            <span class="block truncate font-medium text-foreground">{{ account.name || account.id }}</span>
            <span class="block truncate text-[11px] text-muted-foreground">{{ account.targetId }}</span>
          </span>
          <span class="flex shrink-0 flex-wrap items-center gap-2 text-muted-foreground">
            <span>{{ currentPriorityLabel(account) }}</span>
            <span>{{ priorityEvidenceLabel(account.priorityCandidate?.priorityEvidence) }}</span>
            <span>{{ reasonLabel(account.priorityCandidate?.reason) }}</span>
            <span v-if="account.priorityCandidate?.blocksTakeover" class="font-medium text-amber-700 dark:text-amber-400">
              {{ t('admin.connectionHealth.priorityCandidate.blocksTakeover') }}
            </span>
          </span>
        </div>
      </div>
    </div>
  </section>
</template>
