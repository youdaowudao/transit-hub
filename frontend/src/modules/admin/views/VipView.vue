<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Copy, Loader2, RefreshCw, Search, X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { t } from '@/locales'
import { copyText } from '../utils/clipboard'
import { useAdminAccounts } from '../composables/useAdminAccounts'
import { getVipSummary, restoreVipRecommendations, saveVipSettings, searchVipUsers, unwatchVipUser, watchVipUser } from '../api/vip'
import type { VipCandidate, VipMutationResponse, VipRow, VipScope, VipSettings, VipSummary } from '../types/vip'

const { currentAccount, isSwitching, loadCurrentAccount } = useAdminAccounts()
const scope = computed<VipScope | null>(() => currentAccount.value ? { workspaceAdminAccountId: currentAccount.value.id, workspaceCreatedAt: currentAccount.value.createdAt } : null)
const emptySettings = (): VipSettings => ({ dailyTop: 20, lifetimeTop: 20, alertDays: 2, alertBalance: 20, alertsEnabled: false, botIds: [], reminderText: '', emptyReminderText: '' })
const cloneSettings = (value: VipSettings): VipSettings => ({ ...value, botIds: [...value.botIds] })
const summary = ref<VipSummary | null>(null)
const draft = ref<VipSettings>(emptySettings())
const loading = ref(false)
const searching = ref(false)
const searchDone = ref(false)
const writing = ref(false)
const error = ref('')
const notice = ref('')
const searchText = ref('')
const candidates = ref<VipCandidate[]>([])
const editing = ref<{ id: string; name: string } | null>(null)
const note = ref('')
const copied = ref('')
const filter = ref('all')
let epoch = 0
let searchSequence = 0
let disposed = false
let loadController: AbortController | null = null
let searchController: AbortController | null = null
let writeController: AbortController | null = null
const current = (generation: number) => !disposed && generation === epoch
const aborted = (cause: unknown) => cause instanceof Error && cause.name === 'AbortError'
const reason = (cause: unknown) => cause instanceof Error ? cause.message : t('admin.vip.readError')
const number = (value: number | null | undefined) => value == null ? t('admin.vip.unknown') : value.toLocaleString('zh-CN', { maximumFractionDigits: 2 })
const percentage = (value: number) => Number.isFinite(value * 100) ? `${(value * 100).toFixed(1)}%` : t('admin.vip.unknown')
const time = (value?: string | null) => value ? new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value)) : t('admin.vip.unknown')
const selectedBot = computed({ get: () => draft.value.botIds[0] ?? '', set: (id: string) => { draft.value.botIds = id ? [id] : [] } })
const draftBot = computed(() => summary.value?.bots.find(bot => bot.id === selectedBot.value))
const targetInvalid = computed(() => draft.value.alertsEnabled && (!draftBot.value || !draftBot.value.enabled))
const dirty = computed(() => summary.value && JSON.stringify(draft.value) !== JSON.stringify(summary.value.settings))
const validDraft = computed(() => Number.isInteger(draft.value.dailyTop) && draft.value.dailyTop >= 0 && draft.value.dailyTop <= 100
  && Number.isInteger(draft.value.lifetimeTop) && draft.value.lifetimeTop >= 0 && draft.value.lifetimeTop <= 100
  && Number.isFinite(draft.value.alertDays) && draft.value.alertDays >= 0 && draft.value.alertDays <= 30
  && Number.isFinite(draft.value.alertBalance) && draft.value.alertBalance >= 0 && draft.value.alertBalance <= 1000000
  && [...draft.value.reminderText].length <= 500 && [...draft.value.emptyReminderText].length <= 500 && !targetInvalid.value)
const rows = computed(() => summary.value?.items.filter(row => filter.value === 'all' || row.status === filter.value) ?? [])

const reset = () => {
  epoch++; searchSequence++
  loadController?.abort(); searchController?.abort(); writeController?.abort()
  summary.value = null; draft.value = emptySettings(); candidates.value = []; editing.value = null; note.value = ''
  error.value = ''; notice.value = ''; copied.value = ''; loading.value = false; searching.value = false; writing.value = false
  searchText.value = ''; searchDone.value = false; filter.value = 'all'
}
const accept = (value: VipSummary, updateDraft: boolean) => {
  summary.value = value
  if (updateDraft) draft.value = cloneSettings(value.settings)
}
const load = async () => {
  if (!scope.value || isSwitching.value || writing.value) return
  const generation = epoch; const captured = { ...scope.value }
  loadController?.abort(); const controller = new AbortController(); loadController = controller
  loading.value = true; error.value = ''; notice.value = ''
  try {
    const value = await getVipSummary(captured, controller.signal)
    if (current(generation) && loadController === controller && !controller.signal.aborted) accept(value, true)
  } catch (cause) {
    if (current(generation) && loadController === controller && !controller.signal.aborted && !aborted(cause)) error.value = reason(cause)
  } finally {
    if (current(generation) && loadController === controller) loading.value = false
  }
}
const search = async () => {
  if (!scope.value || isSwitching.value || writing.value || [...searchText.value].length > 100) return
  const generation = epoch; const request = ++searchSequence; const captured = { ...scope.value }
  searchController?.abort(); const controller = new AbortController(); searchController = controller
  searching.value = true; searchDone.value = false; candidates.value = []; error.value = ''
  try {
    const value = await searchVipUsers(captured, searchText.value.trim(), controller.signal)
    if (current(generation) && request === searchSequence && !controller.signal.aborted) {
      candidates.value = value.items
      searchDone.value = true
    }
  } catch (cause) {
    if (current(generation) && request === searchSequence && !controller.signal.aborted && !aborted(cause)) error.value = reason(cause)
  } finally {
    if (current(generation) && request === searchSequence) searching.value = false
  }
}
const mutate = async (operation: (captured: VipScope, signal: AbortSignal) => Promise<VipMutationResponse>, updateDraft: boolean, success: string) => {
  if (!scope.value || isSwitching.value || loading.value || writing.value) return
  const generation = epoch; const captured = { ...scope.value }; const controller = new AbortController(); writeController = controller
  searchSequence++; searchController?.abort(); searching.value = false; searchDone.value = false; candidates.value = []
  writing.value = true; error.value = ''; notice.value = ''
  try {
    const result = await operation(captured, controller.signal)
    if (!current(generation) || writeController !== controller || controller.signal.aborted) return
    if (result.summary) accept(result.summary, updateDraft)
    notice.value = result.refreshMessage || (success === 'restore' ? t('admin.vip.restored', { count: result.restored }) : success)
    editing.value = null; note.value = ''
  } catch (cause) {
    if (current(generation) && writeController === controller && !controller.signal.aborted && !aborted(cause)) error.value = reason(cause)
  } finally {
    if (current(generation) && writeController === controller) writing.value = false
  }
}
const edit = (candidate: VipCandidate | VipRow) => {
  if (writing.value || loading.value) return
  editing.value = { id: candidate.siteUserId, name: candidate.email || candidate.username || candidate.siteUserId }
  note.value = summary.value?.items.find(row => row.siteUserId === candidate.siteUserId)?.source.note ?? ''
}
const saveNote = () => {
  if (!editing.value || [...note.value].length > 200) return
  const id = editing.value.id; const text = note.value
  void mutate((captured, signal) => watchVipUser(captured, id, text, signal), false, t('admin.vip.watchSaved'))
}
const remove = (id: string) => { void mutate((captured, signal) => unwatchVipUser(captured, id, signal), false, t('admin.vip.removed')) }
const restore = () => { void mutate((captured, signal) => restoreVipRecommendations(captured, signal), false, 'restore') }
const saveSettings = () => {
  if (!validDraft.value) return
  const value = cloneSettings(draft.value)
  void mutate((captured, signal) => saveVipSettings(captured, value, signal), true, t('admin.vip.settingsSaved'))
}
const copy = async (row: VipRow) => {
  const generation = epoch
  copied.value = ''
  if (error.value === t('admin.vip.copyError')) error.value = ''
  try { await copyText(row.reminder); if (current(generation)) copied.value = row.siteUserId }
  catch { if (current(generation)) error.value = t('admin.vip.copyError') }
}
watch(() => [scope.value?.workspaceAdminAccountId, scope.value?.workspaceCreatedAt, isSwitching.value], () => {
  reset()
  if (scope.value && !isSwitching.value) void load()
}, { immediate: true })
onMounted(() => { if (!currentAccount.value) void loadCurrentAccount() })
onBeforeUnmount(() => { disposed = true; reset() })
</script>

<template>
  <div class="mx-auto max-w-7xl space-y-6 pb-12">
    <header class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold text-foreground">{{ t('admin.vip.title') }}</h1>
        <p class="mt-2 max-w-3xl text-sm text-muted-foreground">{{ t('admin.vip.intro') }}</p>
      </div>
      <Button variant="secondary" :disabled="!scope || loading || writing || isSwitching" @click="load">
        <Loader2 v-if="loading" class="mr-2 h-4 w-4 animate-spin" />
        <RefreshCw v-else class="mr-2 h-4 w-4" />
        {{ t('admin.vip.refresh') }}
      </Button>
    </header>
    <p v-if="!scope" class="rounded-lg border border-border p-4 text-sm">{{ t('admin.vip.noWorkspace') }}</p>
    <p v-if="error" role="alert" class="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg border border-border p-4 text-sm">{{ notice }}</p>
    <p v-if="loading && !summary" role="status" class="text-sm text-muted-foreground">{{ t('admin.vip.loading') }}</p>
    <template v-if="summary">
      <section class="space-y-2 rounded-xl border border-border/50 bg-card p-5 text-sm">
        <h2 class="font-semibold">{{ t('admin.vip.localData') }}</h2>
        <p>{{ t('admin.vip.lastSynced') }}{{ time(summary.lastSyncedAt) }}</p>
        <p>{{ t('admin.vip.coverage') }}{{ summary.coverage.from || t('admin.vip.unknown') }} — {{ summary.coverage.to || t('admin.vip.unknown') }}</p>
        <p v-if="!summary.coverage.sevenDays" class="text-amber-700 dark:text-amber-300">{{ t('admin.vip.incomplete') }}</p>
        <p v-if="summary.pauseReason" role="status" class="text-amber-700 dark:text-amber-300">{{ summary.pauseReason }}</p>
        <p v-else class="text-muted-foreground">{{ summary.settings.alertsEnabled ? t('admin.vip.enabled') : t('admin.vip.off') }}</p>
        <p class="text-muted-foreground">{{ t('admin.vip.dataHelp') }} <RouterLink to="/admin/user-activity" class="underline">{{ t('admin.vip.openActivity') }}</RouterLink></p>
        <ul v-if="summary.coverage.warnings.length" class="space-y-2 text-muted-foreground"><li v-for="warning in summary.coverage.warnings" :key="warning">{{ warning }}</li></ul>
      </section>
      <section class="overflow-hidden rounded-xl border border-border/50 bg-card">
        <div class="space-y-3 p-5">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h2 class="font-semibold">{{ t('admin.vip.watchlist') }} · {{ summary.counts.all }}</h2>
            <label class="flex items-center gap-2 text-sm">{{ t('admin.vip.filter') }}
              <select v-model="filter" class="h-10 rounded-md border border-border bg-background px-3">
                <option v-for="status in ['all','empty','low','declining','normal','disabled']" :key="status" :value="status">{{ t(`admin.vip.status.${status}`) }}</option>
              </select>
            </label>
          </div>
          <p class="text-sm text-muted-foreground">{{ t('admin.vip.actionsHelp') }}</p>
          <p class="text-sm text-muted-foreground">{{ t('admin.vip.copyHelp') }} {{ t('admin.vip.savedCopyHelp') }}</p>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="border-y border-border/50 bg-muted/30 text-muted-foreground"><tr>
              <th class="px-4 py-3">{{ t('admin.vip.customer') }}</th><th class="px-4 py-3">{{ t('admin.vip.condition') }}</th>
              <th class="px-4 py-3">{{ t('admin.vip.balance') }}</th><th class="px-4 py-3">{{ t('admin.vip.average') }}</th>
              <th class="px-4 py-3">{{ t('admin.vip.daysLeft') }}</th><th class="px-4 py-3">{{ t('admin.vip.recorded') }}</th>
              <th class="px-4 py-3">{{ t('admin.vip.reason') }}</th><th class="px-4 py-3">{{ t('admin.vip.actions') }}</th>
            </tr></thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="row in rows" :key="row.siteUserId">
                <td class="px-4 py-3"><div class="break-all font-medium">{{ row.email || row.username || row.siteUserId }}</div><div class="mt-1 text-xs text-muted-foreground">{{ row.siteUserId }} · {{ row.role || t('admin.vip.unknown') }}</div><div class="mt-1 text-xs text-muted-foreground">{{ t('admin.vip.observed') }}{{ time(row.observedAt) }}</div></td>
                <td class="px-4 py-3"><span>{{ row.observedAt ? t(`admin.vip.status.${row.status}`) : t('admin.vip.profileMissing') }}</span><p v-if="row.lastAlertStatus === 'unknown'" class="mt-2 max-w-xs text-xs text-amber-700 dark:text-amber-300">{{ t('admin.vip.unknownDelivery') }} {{ time(row.lastAlertAt) }}</p><p v-else-if="row.lastAlertStatus === 'sent'" class="mt-2 text-xs text-muted-foreground">{{ t('admin.vip.confirmedDelivery') }} {{ time(row.lastAlertAt) }}</p></td>
                <td class="px-4 py-3 tabular-nums">{{ number(row.balance) }}</td>
                <td class="px-4 py-3 tabular-nums">{{ number(row.dailyAverage) }}<p class="mt-1 text-xs text-muted-foreground">{{ t('admin.vip.previous') }}{{ number(row.previousAverage) }}</p><p v-if="row.trend != null" class="mt-1 text-xs text-muted-foreground">{{ percentage(row.trend) }}</p></td>
                <td class="px-4 py-3 tabular-nums">{{ row.daysLeft == null ? t('admin.vip.unknown') : t('admin.vip.days', { days: number(row.daysLeft) }) }}</td>
                <td class="px-4 py-3 tabular-nums">{{ number(row.recordedConsumption) }}<p class="mt-1 whitespace-nowrap text-xs text-muted-foreground">{{ t('admin.vip.recordedFrom') }}{{ row.recordedFrom || t('admin.vip.unknown') }}</p></td>
                <td class="px-4 py-3"><span v-if="row.source.manual">{{ t('admin.vip.manual') }}</span><p v-if="row.source.dailyRank">{{ t('admin.vip.dailyRank', { rank: row.source.dailyRank }) }}</p><p v-if="row.source.lifetimeRank">{{ t('admin.vip.lifetimeRank', { rank: row.source.lifetimeRank }) }}</p><p v-if="row.source.note" class="mt-1 max-w-xs break-words text-muted-foreground">{{ row.source.note }}</p></td>
                <td class="px-4 py-3"><div class="flex flex-wrap gap-2"><Button size="sm" variant="secondary" :disabled="loading || writing" @click="edit(row)">{{ row.source.manual ? t('admin.vip.editNote') : t('admin.vip.watch') }}</Button><Button size="sm" variant="secondary" :disabled="loading || writing" @click="remove(row.siteUserId)">{{ t('admin.vip.remove') }}</Button><Button size="sm" variant="ghost" @click="copy(row)"><Copy class="mr-1 h-4 w-4" />{{ t('admin.vip.copy') }}</Button></div><p v-if="copied === row.siteUserId" class="mt-1 text-xs text-emerald-700 dark:text-emerald-300">{{ t('admin.vip.copied') }}</p></td>
              </tr>
              <tr v-if="!rows.length"><td colspan="8" class="px-4 py-8 text-center text-muted-foreground">{{ t('admin.vip.empty') }}</td></tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="rounded-xl border border-border/50 bg-card p-5">
        <h2 class="font-semibold">{{ t('admin.vip.add') }}</h2>
        <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.vip.searchHelp') }}</p>
        <form class="mt-4 flex gap-2" @submit.prevent="search"><Input v-model="searchText" maxlength="100" :disabled="writing" :placeholder="t('admin.vip.searchPlaceholder')" /><Button type="submit" :disabled="searching || writing || !scope"><Loader2 v-if="searching" class="mr-2 h-4 w-4 animate-spin" /><Search v-else class="mr-2 h-4 w-4" />{{ t('admin.vip.search') }}</Button></form>
        <p v-if="searchDone && !candidates.length" role="status" class="mt-4 text-sm text-muted-foreground">{{ t('admin.vip.searchEmpty') }}</p>
        <ul v-if="candidates.length" class="mt-4 divide-y divide-border/50"><li v-for="candidate in candidates" :key="candidate.siteUserId" class="flex flex-wrap items-center justify-between gap-3 py-3"><div class="min-w-0 text-sm"><p class="break-all font-medium">{{ candidate.email || candidate.username || candidate.siteUserId }}</p><p class="text-muted-foreground">{{ candidate.siteUserId }} · {{ t('admin.vip.balance') }} {{ number(candidate.balance) }} · {{ candidate.watched ? t('admin.vip.watched') : candidate.dismissed ? t('admin.vip.dismissed') : t('admin.vip.unwatched') }}</p></div><Button variant="secondary" size="sm" :disabled="loading || writing" @click="edit(candidate)">{{ candidate.watched ? t('admin.vip.editNote') : t('admin.vip.watch') }}</Button></li></ul>
      </section>
      <section v-if="editing" class="rounded-xl border border-border/50 bg-card p-5">
        <div class="flex items-center justify-between"><h2 class="font-semibold">{{ t('admin.vip.editTitle') }} · {{ editing.name }}</h2><Button variant="ghost" size="sm" :disabled="writing" :aria-label="t('admin.vip.close')" @click="editing = null"><X class="h-4 w-4" /></Button></div>
        <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.vip.actionsHelp') }}</p>
        <form class="mt-4 space-y-3" @submit.prevent="saveNote"><label class="block text-sm">{{ t('admin.vip.note') }}<Input v-model="note" maxlength="200" :disabled="writing" class="mt-2" /></label><Button type="submit" :disabled="loading || writing || [...note].length > 200">{{ t('admin.vip.saveWatch') }}</Button></form>
      </section>
      <section class="rounded-xl border border-border/50 bg-card p-5">
        <h2 class="font-semibold">{{ t('admin.vip.settings') }}</h2>
        <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.vip.alertHelp') }}</p>
        <p class="mt-1 text-sm text-muted-foreground">{{ t('admin.vip.oneTarget') }}</p>
        <p class="mt-1 text-sm text-muted-foreground">{{ t('admin.vip.frequencyHelp') }}</p>
        <form class="mt-5 space-y-5" @submit.prevent="saveSettings">
          <fieldset :disabled="loading || writing" class="space-y-5">
            <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <label class="text-sm">{{ t('admin.vip.dailyTop') }}<Input :model-value="String(draft.dailyTop)" @update:model-value="draft.dailyTop = Number.parseFloat($event)" type="number" min="0" max="100" class="mt-2" /></label>
              <label class="text-sm">{{ t('admin.vip.lifetimeTop') }}<Input :model-value="String(draft.lifetimeTop)" @update:model-value="draft.lifetimeTop = Number.parseFloat($event)" type="number" min="0" max="100" class="mt-2" /></label>
              <label class="text-sm">{{ t('admin.vip.alertDays') }}<Input :model-value="String(draft.alertDays)" @update:model-value="draft.alertDays = Number.parseFloat($event)" type="number" min="0" max="30" step="0.1" class="mt-2" /></label>
              <label class="text-sm">{{ t('admin.vip.alertBalance') }}<Input :model-value="String(draft.alertBalance)" @update:model-value="draft.alertBalance = Number.parseFloat($event)" type="number" min="0" max="1000000" step="0.01" class="mt-2" /></label>
            </div>
            <p class="text-xs text-muted-foreground">{{ t('admin.vip.thresholdHelp') }}</p>
            <label class="flex items-center gap-2 text-sm"><input v-model="draft.alertsEnabled" type="checkbox" class="h-4 w-4 rounded border-border" />{{ t('admin.vip.enable') }}</label>
            <label class="block text-sm">{{ t('admin.vip.bot') }}<select v-model="selectedBot" class="mt-2 h-10 w-full rounded-md border border-border bg-background px-3"><option value="">{{ t('admin.vip.noBot') }}</option><option v-if="selectedBot && !draftBot" :value="selectedBot" disabled>{{ t('admin.vip.missingBot') }}</option><option v-for="bot in summary.bots" :key="bot.id" :value="bot.id" :disabled="!bot.enabled">{{ bot.name || bot.id }}{{ bot.enabled ? '' : t('admin.vip.botDisabled') }}</option></select></label>
            <p v-if="targetInvalid" class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.vip.needBot') }}</p>
            <RouterLink to="/admin/settings" class="inline-block text-sm underline">{{ t('admin.vip.openNotifications') }}</RouterLink>
            <div class="grid gap-4 md:grid-cols-2"><label class="text-sm">{{ t('admin.vip.reminder') }}<textarea v-model="draft.reminderText" maxlength="500" rows="4" class="mt-2 w-full rounded-md border border-border bg-background px-3 py-2" /></label><label class="text-sm">{{ t('admin.vip.emptyReminder') }}<textarea v-model="draft.emptyReminderText" maxlength="500" rows="4" class="mt-2 w-full rounded-md border border-border bg-background px-3 py-2" /></label></div>
            <p class="text-xs text-muted-foreground">{{ t('admin.vip.templateHelp') }}</p>
            <p v-if="dirty" class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.vip.unsaved') }}</p>
            <Button type="submit" :disabled="!validDraft || loading || writing"><Loader2 v-if="writing" class="mr-2 h-4 w-4 animate-spin" />{{ t('admin.vip.saveSettings') }}</Button>
          </fieldset>
        </form>
        <div class="mt-6 border-t border-border/50 pt-5"><h3 class="text-sm font-medium">{{ t('admin.vip.restoreTitle') }} · {{ summary.dismissedCount }}</h3><p class="mt-2 text-sm text-muted-foreground">{{ t('admin.vip.actionsHelp') }}</p><Button variant="secondary" class="mt-3" :disabled="!summary.dismissedCount || loading || writing" @click="restore">{{ t('admin.vip.restore') }}</Button></div>
      </section>
    </template>
  </div>
</template>
