<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { AlertCircle, Copy, Loader2, Plus, RefreshCw, X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tooltip } from '@/components/ui/tooltip'
import { t } from '@/locales'
import { listMassEmailUsers, listSelfRechargeUsers } from '../api/massEmail'
import type { MassEmailUser, SelfRechargeUser } from '../types/massEmail'
import {
  USER_LAST_USED_COPIED_STORAGE_KEY,
  USER_LAST_USED_TIMEZONE,
  copiedUserKey,
  defaultSelectedDates,
  formatLastUsedAt,
  groupUsersByLastUsedDate,
  normalizeSelectedDates,
  pageContainsDateBefore,
  pageIsLastUsedTimeTail,
  parseCopiedUserKeys,
} from '../utils/userLastUsed'

type QueryMode = 'lastUsed' | 'recharge'

const pageSize = 100
const initialDates = defaultSelectedDates()
const activeMode = ref<QueryMode>('lastUsed')
const selectedDates = ref(initialDates)
const dateDraft = ref(initialDates[0] ?? '')
const users = ref<MassEmailUser[]>([])
const rechargeUsers = ref<SelfRechargeUser[]>([])
const rechargeTotalUsers = ref(0)
const rechargeTotalRecords = ref(0)
const rechargeTotalAmount = ref(0)
const rechargeLoaded = ref(false)
const lastUsedLoading = ref(false)
const rechargeLoading = ref(false)
const errorKey = ref('')
const copyError = ref(false)
const copiedUserKeys = ref(parseCopiedUserKeys(window.localStorage.getItem(USER_LAST_USED_COPIED_STORAGE_KEY)))
let lastUsedController: AbortController | null = null
let rechargeController: AbortController | null = null

const groups = computed(() => groupUsersByLastUsedDate(users.value, selectedDates.value))
const totalUsers = computed(() => groups.value.reduce((total, group) => total + group.items.length, 0))
const loading = computed(() => activeMode.value === 'lastUsed' ? lastUsedLoading.value : rechargeLoading.value)

const isAbortError = (error: unknown): boolean => error instanceof Error && error.name === 'AbortError'

const loadUsers = async () => {
  if (selectedDates.value.length === 0) return
  lastUsedController?.abort()
  const controller = new AbortController()
  lastUsedController = controller
  lastUsedLoading.value = true
  errorKey.value = ''
  copyError.value = false

  try {
    const fetched: MassEmailUser[] = []
    const earliestDate = selectedDates.value[selectedDates.value.length - 1]
    let page = 1

    while (true) {
      const response = await listMassEmailUsers(
        {
          page,
          pageSize,
          status: '',
          role: '',
          search: '',
          sortBy: 'last_used_at',
          sortOrder: 'desc',
          timezone: USER_LAST_USED_TIMEZONE,
        },
        { preserveUpstreamAuthError: true, signal: controller.signal },
      )
      fetched.push(...response.items)

      const reachedLastPage = response.items.length < pageSize
        || (response.totalPages > 0 && page >= response.totalPages)
      if (
        reachedLastPage
        || pageContainsDateBefore(response.items, earliestDate)
        || pageIsLastUsedTimeTail(response.items)
      ) break
      page += 1
    }

    if (lastUsedController === controller) users.value = fetched
  } catch (error) {
    if (isAbortError(error)) return
    const message = error instanceof Error ? error.message : ''
    errorKey.value = message === 'auth.errors.unauthorized'
      ? message
      : message === 'admin.massEmail.errors.upstreamAuth'
        ? 'admin.userLastUsed.errors.upstreamAuth'
        : 'admin.userLastUsed.errors.request'
    users.value = []
  } finally {
    if (lastUsedController === controller) {
      lastUsedController = null
      lastUsedLoading.value = false
    }
  }
}

const loadRechargeUsers = async () => {
  rechargeController?.abort()
  const controller = new AbortController()
  rechargeController = controller
  rechargeLoading.value = true
  rechargeLoaded.value = false
  errorKey.value = ''
  copyError.value = false

  try {
    const response = await listSelfRechargeUsers({
      preserveUpstreamAuthError: true,
      signal: controller.signal,
    })
    if (rechargeController !== controller) return
    rechargeUsers.value = response.items
    rechargeTotalUsers.value = response.totalUsers
    rechargeTotalRecords.value = response.totalRecords
    rechargeTotalAmount.value = response.totalAmount
    rechargeLoaded.value = true
  } catch (error) {
    if (isAbortError(error)) return
    const message = error instanceof Error ? error.message : ''
    if (message === 'auth.errors.unauthorized') {
      errorKey.value = message
    } else if (message === 'admin.massEmail.errors.upstreamAuth') {
      errorKey.value = 'admin.userRecharge.errors.upstreamAuth'
    } else if (message === 'admin.userRecharge.errors.limitReached' || message === 'admin.userRecharge.errors.dataChanged') {
      errorKey.value = message
    } else {
      errorKey.value = 'admin.userRecharge.errors.request'
    }
    rechargeUsers.value = []
    rechargeTotalUsers.value = 0
    rechargeTotalRecords.value = 0
    rechargeTotalAmount.value = 0
  } finally {
    if (rechargeController === controller) {
      rechargeController = null
      rechargeLoading.value = false
    }
  }
}

const switchMode = (mode: QueryMode) => {
  if (activeMode.value === mode) return
  if (activeMode.value === 'lastUsed') lastUsedController?.abort()
  else rechargeController?.abort()
  activeMode.value = mode
  errorKey.value = ''
  copyError.value = false
  if (mode === 'recharge' && !rechargeLoaded.value) void loadRechargeUsers()
}

const addDate = async () => {
  if (!dateDraft.value) return
  const nextDates = normalizeSelectedDates([...selectedDates.value, dateDraft.value])
  if (nextDates.length === selectedDates.value.length) return
  selectedDates.value = nextDates
  await loadUsers()
}

const removeDate = async (date: string) => {
  if (selectedDates.value.length <= 1) return
  selectedDates.value = selectedDates.value.filter((item) => item !== date)
  await loadUsers()
}

const copyEmail = async (userId: string, email: string) => {
  copyError.value = false
  try {
    await navigator.clipboard.writeText(email)
  } catch {
    copyError.value = true
    return
  }

  const nextKeys = new Set(copiedUserKeys.value).add(copiedUserKey(userId, email))
  copiedUserKeys.value = nextKeys
  try {
    window.localStorage.setItem(USER_LAST_USED_COPIED_STORAGE_KEY, JSON.stringify([...nextKeys]))
  } catch {
    // 浏览器禁用或耗尽本地存储时，当前页面仍保留已复制状态。
  }
}

const isCopied = (userId: string, email: string): boolean => (
  copiedUserKeys.value.has(copiedUserKey(userId, email))
)

const amountFormatter = new Intl.NumberFormat('zh-CN', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 6,
})
const formatAmount = (value: number): string => amountFormatter.format(value)

onMounted(() => {
  void loadUsers()
})

onBeforeUnmount(() => {
  lastUsedController?.abort()
  rechargeController?.abort()
})
</script>

<template>
  <div class="space-y-5">
    <div class="flex w-fit max-w-full items-center gap-1 overflow-x-auto rounded-lg border border-border/50 bg-surface p-1" role="tablist" :aria-label="t('admin.menu.userLastUsed')">
      <button
        v-for="mode in (['lastUsed', 'recharge'] as const)"
        :key="mode"
        type="button"
        role="tab"
        :aria-selected="activeMode === mode"
        aria-controls="user-query-panel"
        :class="[
          'shrink-0 rounded-md px-4 py-1.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
          activeMode === mode
            ? 'bg-primary text-primary-foreground shadow-sm'
            : 'text-muted-foreground hover:bg-surface-elevated hover:text-foreground',
        ]"
        @click="switchMode(mode)"
      >
        {{ t(`admin.userLastUsed.tabs.${mode}`) }}
      </button>
    </div>

    <div id="user-query-panel" role="tabpanel">
      <section v-if="activeMode === 'lastUsed'" class="rounded-lg border border-border/60 bg-surface p-4 sm:p-5">
        <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
          <div class="w-full max-w-sm space-y-2">
            <label for="user-last-used-date" class="block text-sm font-medium text-foreground">
              {{ t('admin.userLastUsed.dateLabel') }}
            </label>
            <div class="grid grid-cols-[minmax(0,1fr)_auto] gap-2">
              <Input id="user-last-used-date" v-model="dateDraft" type="date" :disabled="lastUsedLoading" />
              <Button :disabled="lastUsedLoading || !dateDraft" @click="addDate">
                <Plus class="h-4 w-4" aria-hidden="true" />
                {{ t('admin.userLastUsed.addDate') }}
              </Button>
            </div>
          </div>

          <Button variant="secondary" :disabled="lastUsedLoading" @click="loadUsers">
            <Loader2 v-if="lastUsedLoading" class="h-4 w-4 animate-spin" aria-hidden="true" />
            <RefreshCw v-else class="h-4 w-4" aria-hidden="true" />
            {{ t('admin.userLastUsed.refresh') }}
          </Button>
        </div>

        <div class="mt-4 flex flex-wrap items-center gap-2" aria-live="polite">
          <span class="mr-1 text-sm text-muted-foreground">{{ t('admin.userLastUsed.selectedDates') }}</span>
          <span
            v-for="date in selectedDates"
            :key="date"
            class="inline-flex h-9 items-center gap-2 rounded-lg border border-border/60 bg-surface-elevated px-3 text-sm font-medium text-foreground"
          >
            {{ date }}
            <Tooltip :text="selectedDates.length <= 1 ? t('admin.userLastUsed.keepOneDate') : t('admin.userLastUsed.removeDate', { date })">
              <button
                type="button"
                class="flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-surface-line hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:pointer-events-none disabled:opacity-40"
                :disabled="lastUsedLoading || selectedDates.length <= 1"
                :aria-label="t('admin.userLastUsed.removeDate', { date })"
                @click="removeDate(date)"
              >
                <X class="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </Tooltip>
          </span>
        </div>
      </section>

      <section v-else class="flex flex-col gap-3 rounded-lg border border-border/60 bg-surface p-4 sm:flex-row sm:items-center sm:justify-between sm:p-5">
        <span class="text-sm font-medium text-foreground">{{ t('admin.userRecharge.source') }}</span>
        <Button variant="secondary" :disabled="rechargeLoading" @click="loadRechargeUsers">
          <Loader2 v-if="rechargeLoading" class="h-4 w-4 animate-spin" aria-hidden="true" />
          <RefreshCw v-else class="h-4 w-4" aria-hidden="true" />
          {{ t('admin.userRecharge.refresh') }}
        </Button>
      </section>
    </div>

    <div v-if="errorKey" class="flex items-start gap-3 rounded-lg border border-red-300/60 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300" role="alert">
      <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
      <span>{{ t(errorKey) }}</span>
    </div>

    <div v-if="copyError" class="flex items-start gap-3 rounded-lg border border-amber-300/60 bg-amber-50 p-4 text-sm text-amber-800 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-300" role="alert">
      <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
      <span>{{ t('admin.userLastUsed.errors.copy') }}</span>
    </div>

    <div v-if="loading" class="flex min-h-48 items-center justify-center gap-2 text-sm text-muted-foreground">
      <Loader2 class="h-5 w-5 animate-spin" aria-hidden="true" />
      {{ t(activeMode === 'lastUsed' ? 'admin.userLastUsed.loading' : 'admin.userRecharge.loading') }}
    </div>

    <div v-else-if="!errorKey && activeMode === 'lastUsed'" class="space-y-4">
      <div class="flex items-center justify-between gap-4 text-sm text-muted-foreground">
        <span>{{ t('admin.userLastUsed.total', { count: totalUsers }) }}</span>
        <span>{{ USER_LAST_USED_TIMEZONE }}</span>
      </div>

      <div v-if="totalUsers === 0" class="rounded-lg border border-dashed border-border/70 px-4 py-12 text-center text-sm text-muted-foreground">
        {{ t('admin.userLastUsed.emptyAll') }}
      </div>

      <section v-for="group in groups" v-else :key="group.date" class="overflow-hidden rounded-lg border border-border/60 bg-surface">
        <div class="flex items-center justify-between gap-4 border-b border-border/60 bg-surface-elevated px-4 py-3">
          <h2 class="text-base font-semibold text-foreground">{{ group.date }}</h2>
          <span class="text-sm text-muted-foreground">{{ t('admin.userLastUsed.dayCount', { count: group.items.length }) }}</span>
        </div>

        <div v-if="group.items.length === 0" class="px-4 py-8 text-center text-sm text-muted-foreground">
          {{ t('admin.userLastUsed.emptyDay') }}
        </div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-[560px] table-fixed text-left text-sm">
            <thead class="border-b border-border/60 text-xs font-medium text-muted-foreground">
              <tr>
                <th class="w-1/2 px-4 py-3">{{ t('admin.userLastUsed.email') }}</th>
                <th class="w-1/2 px-4 py-3">{{ t('admin.userLastUsed.lastUsedAt') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/50">
              <tr
                v-for="user in group.items"
                :key="user.id"
                :class="isCopied(user.id, user.email) ? 'bg-emerald-500/10 hover:bg-emerald-500/15' : 'hover:bg-surface-elevated/60'"
              >
                <td class="px-4 py-3">
                  <div class="flex min-w-0 items-center gap-2">
                    <span class="min-w-0 break-all font-medium text-foreground">{{ user.email }}</span>
                    <span v-if="isCopied(user.id, user.email)" class="shrink-0 text-xs font-medium text-emerald-700 dark:text-emerald-300">
                      {{ t('admin.userLastUsed.copied') }}
                    </span>
                    <Tooltip :text="t('admin.userLastUsed.copyEmail')">
                      <Button variant="ghost" size="sm" class="h-8 w-8 shrink-0 px-0" :aria-label="t('admin.userLastUsed.copyEmail')" @click="copyEmail(user.id, user.email)">
                        <Copy class="h-4 w-4" aria-hidden="true" />
                      </Button>
                    </Tooltip>
                  </div>
                </td>
                <td class="whitespace-nowrap px-4 py-3 font-mono text-xs text-foreground sm:text-sm">{{ user.displayLastUsedAt }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <div v-else-if="!errorKey && activeMode === 'recharge' && rechargeLoaded" class="space-y-4">
      <div class="grid grid-cols-1 gap-3 text-sm sm:grid-cols-3">
        <div class="border-b border-border/60 pb-3">
          <div class="text-muted-foreground">{{ t('admin.userRecharge.totalUsers') }}</div>
          <div class="mt-1 text-lg font-semibold tabular-nums text-foreground">{{ rechargeTotalUsers }}</div>
        </div>
        <div class="border-b border-border/60 pb-3">
          <div class="text-muted-foreground">{{ t('admin.userRecharge.totalRecords') }}</div>
          <div class="mt-1 text-lg font-semibold tabular-nums text-foreground">{{ rechargeTotalRecords }}</div>
        </div>
        <div class="border-b border-border/60 pb-3">
          <div class="text-muted-foreground">{{ t('admin.userRecharge.totalAmount') }}</div>
          <div class="mt-1 text-lg font-semibold tabular-nums text-foreground">{{ formatAmount(rechargeTotalAmount) }}</div>
        </div>
      </div>

      <div v-if="rechargeUsers.length === 0" class="rounded-lg border border-dashed border-border/70 px-4 py-12 text-center text-sm text-muted-foreground">
        {{ t('admin.userRecharge.empty') }}
      </div>
      <div v-else class="overflow-x-auto rounded-lg border border-border/60 bg-surface">
        <table class="min-w-[760px] table-fixed text-left text-sm">
          <thead class="border-b border-border/60 text-xs font-medium text-muted-foreground">
            <tr>
              <th class="w-[40%] px-4 py-3">{{ t('admin.userRecharge.email') }}</th>
              <th class="w-[17%] px-4 py-3 text-right">{{ t('admin.userRecharge.rechargeCount') }}</th>
              <th class="w-[20%] px-4 py-3 text-right">{{ t('admin.userRecharge.totalAmount') }}</th>
              <th class="w-[23%] px-4 py-3">{{ t('admin.userRecharge.lastRechargedAt') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/50">
            <tr
              v-for="user in rechargeUsers"
              :key="user.userId"
              :class="isCopied(user.userId, user.email) ? 'bg-emerald-500/10 hover:bg-emerald-500/15' : 'hover:bg-surface-elevated/60'"
            >
              <td class="px-4 py-3">
                <div class="flex min-w-0 items-center gap-2">
                  <span class="min-w-0 break-all font-medium text-foreground">{{ user.email }}</span>
                  <span v-if="isCopied(user.userId, user.email)" class="shrink-0 text-xs font-medium text-emerald-700 dark:text-emerald-300">
                    {{ t('admin.userLastUsed.copied') }}
                  </span>
                  <Tooltip :text="t('admin.userLastUsed.copyEmail')">
                    <Button variant="ghost" size="sm" class="h-8 w-8 shrink-0 px-0" :aria-label="t('admin.userLastUsed.copyEmail')" @click="copyEmail(user.userId, user.email)">
                      <Copy class="h-4 w-4" aria-hidden="true" />
                    </Button>
                  </Tooltip>
                </div>
              </td>
              <td class="px-4 py-3 text-right tabular-nums text-foreground">{{ user.rechargeCount }}</td>
              <td class="px-4 py-3 text-right tabular-nums text-foreground">{{ formatAmount(user.totalAmount) }}</td>
              <td class="whitespace-nowrap px-4 py-3 font-mono text-xs text-foreground sm:text-sm">{{ formatLastUsedAt(user.lastRechargedAt) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
