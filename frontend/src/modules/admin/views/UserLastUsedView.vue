<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { AlertCircle, Check, Copy, Loader2, Plus, RefreshCw, X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tooltip } from '@/components/ui/tooltip'
import { t } from '@/locales'
import { listMassEmailUsers } from '../api/massEmail'
import type { MassEmailUser } from '../types/massEmail'
import {
  USER_LAST_USED_TIMEZONE,
  defaultSelectedDates,
  groupUsersByLastUsedDate,
  normalizeSelectedDates,
  pageContainsDateBefore,
  pageIsLastUsedTimeTail,
} from '../utils/userLastUsed'

const pageSize = 100
const initialDates = defaultSelectedDates()
const selectedDates = ref(initialDates)
const dateDraft = ref(initialDates[0] ?? '')
const users = ref<MassEmailUser[]>([])
const loading = ref(false)
const errorKey = ref('')
const copyError = ref(false)
const copiedUserId = ref('')
let copyTimer: number | undefined

const groups = computed(() => groupUsersByLastUsedDate(users.value, selectedDates.value))
const totalUsers = computed(() => groups.value.reduce((total, group) => total + group.items.length, 0))

const loadUsers = async () => {
  if (selectedDates.value.length === 0) return
  loading.value = true
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
        { preserveUpstreamAuthError: true },
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

    users.value = fetched
  } catch (error) {
    const message = error instanceof Error ? error.message : ''
    errorKey.value = message === 'auth.errors.unauthorized'
      ? message
      : message === 'admin.massEmail.errors.upstreamAuth'
        ? 'admin.userLastUsed.errors.upstreamAuth'
        : 'admin.userLastUsed.errors.request'
    users.value = []
  } finally {
    loading.value = false
  }
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

const copyUsername = async (userId: string, username: string) => {
  copyError.value = false
  try {
    await navigator.clipboard.writeText(username)
    copiedUserId.value = userId
    if (copyTimer !== undefined) window.clearTimeout(copyTimer)
    copyTimer = window.setTimeout(() => {
      copiedUserId.value = ''
    }, 1600)
  } catch {
    copyError.value = true
  }
}

onMounted(() => {
  void loadUsers()
})

onBeforeUnmount(() => {
  if (copyTimer !== undefined) window.clearTimeout(copyTimer)
})
</script>

<template>
  <div class="space-y-5">
    <section class="rounded-lg border border-border/60 bg-surface p-4 sm:p-5">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
        <div class="w-full max-w-sm space-y-2">
          <label for="user-last-used-date" class="block text-sm font-medium text-foreground">
            {{ t('admin.userLastUsed.dateLabel') }}
          </label>
          <div class="grid grid-cols-[minmax(0,1fr)_auto] gap-2">
            <Input id="user-last-used-date" v-model="dateDraft" type="date" :disabled="loading" />
            <Button :disabled="loading || !dateDraft" @click="addDate">
              <Plus class="h-4 w-4" aria-hidden="true" />
              {{ t('admin.userLastUsed.addDate') }}
            </Button>
          </div>
        </div>

        <Button variant="secondary" :disabled="loading" @click="loadUsers">
          <Loader2 v-if="loading" class="h-4 w-4 animate-spin" aria-hidden="true" />
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
              :disabled="loading || selectedDates.length <= 1"
              :aria-label="t('admin.userLastUsed.removeDate', { date })"
              @click="removeDate(date)"
            >
              <X class="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          </Tooltip>
        </span>
      </div>
    </section>

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
      {{ t('admin.userLastUsed.loading') }}
    </div>

    <div v-else-if="!errorKey" class="space-y-4">
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
                <th class="w-1/2 px-4 py-3">{{ t('admin.userLastUsed.username') }}</th>
                <th class="w-1/2 px-4 py-3">{{ t('admin.userLastUsed.lastUsedAt') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="user in group.items" :key="user.id" class="hover:bg-surface-elevated/60">
                <td class="px-4 py-3">
                  <div class="flex min-w-0 items-center gap-2">
                    <span class="min-w-0 break-all font-medium text-foreground">{{ user.username }}</span>
                    <Tooltip :text="copiedUserId === user.id ? t('admin.userLastUsed.copied') : t('admin.userLastUsed.copyUsername')">
                      <Button
                        variant="ghost"
                        size="sm"
                        class="h-8 w-8 shrink-0 px-0"
                        :aria-label="t('admin.userLastUsed.copyUsername')"
                        @click="copyUsername(user.id, user.username)"
                      >
                        <Check v-if="copiedUserId === user.id" class="h-4 w-4 text-emerald-600" aria-hidden="true" />
                        <Copy v-else class="h-4 w-4" aria-hidden="true" />
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
  </div>
</template>
