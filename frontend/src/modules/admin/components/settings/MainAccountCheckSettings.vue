<script setup lang="ts">
import { onBeforeUnmount, ref, watch, computed } from 'vue'
import { Button } from '@/components/ui/button'
import { Timer } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { readMainAccountCheckSettings, saveMainAccountCheckSettings, type MainAccountCheckSettings, type MainAccountCheckScope } from '../../api/settings'
import { safeScopedErrorText } from '../../utils/scopedErrorText'
const props = defineProps<{ scope: MainAccountCheckScope; switching?: boolean }>()
const enabled = ref(false)
const times = ref<string[]>([])
const loaded = ref(false)
const savedEnabled = ref<boolean | null>(null)
const savedTimes = ref<string[]>([])
const dirty = computed(() => enabled.value !== savedEnabled.value || JSON.stringify(times.value) !== JSON.stringify(savedTimes.value))
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const configError = ref('')
const success = ref('')
const recent = ref<MainAccountCheckSettings['recentScheduled']>(null)
const recentError = ref('')
let generation = 0
let controller: AbortController | null = null
const scopeKey = () => `${props.scope.workspaceAdminAccountId}\u0000${props.scope.workspaceCreatedAt}`
const valid = computed(() => times.value.length === 4 && times.value.every((v, i) => /^([01]\d|2[0-3]):[0-5]\d$/.test(v) && (i === 0 || times.value[i - 1]! < v)))
const reset = () => { generation++; controller?.abort(); controller = null; loaded.value = false; loading.value = false; saving.value = false; error.value = ''; configError.value = ''; success.value = ''; recent.value = null; recentError.value = ''; times.value = []; savedEnabled.value = null; savedTimes.value = [] }
const apply = (data: MainAccountCheckSettings, scope: MainAccountCheckScope) => {
  if (data.workspaceAdminAccountId !== scope.workspaceAdminAccountId || data.workspaceCreatedAt !== scope.workspaceCreatedAt || typeof data.enabled !== 'boolean' || !Array.isArray(data.times) || data.times.length !== 4) throw new Error('核对设置返回的工作区或内容无法确认，请重新读取。')
  enabled.value = data.enabled; times.value = [...data.times]; savedEnabled.value = data.enabled; savedTimes.value = [...data.times]; configError.value = data.configError || ''; recent.value = data.recentScheduled; recentError.value = data.recentReadError || ''; loaded.value = true
}
const errorText = (err: unknown) => { const message = err instanceof Error ? err.message : ''; return safeScopedErrorText(te(message) ? t(message) : message) || '原因无法确认，请检查 Hub 后端记录后重试。' }
const load = async () => {
  if (props.switching || loading.value || saving.value) return
  const key = scopeKey(), captured = { ...props.scope }, seq = ++generation
  controller?.abort(); const request = new AbortController(); controller = request
  loading.value = true; error.value = ''; loaded.value = false
  const current = () => seq === generation && key === scopeKey() && !props.switching && !request.signal.aborted
  try { const data = await readMainAccountCheckSettings(captured, request.signal); if (current()) apply(data, captured) }
  catch (err) { if (current()) error.value = `主站账号核对设置未能读取：${errorText(err)}；请重新读取。` }
  finally { if (seq === generation) { loading.value = false; controller = null } }
}
const save = async () => {
  if (!loaded.value || props.switching || !valid.value || saving.value || loading.value) return
  const captured = { ...props.scope }, key = scopeKey(), seq = ++generation
  controller?.abort(); const request = new AbortController(); controller = request
  saving.value = true; error.value = ''; success.value = ''
  const current = () => seq === generation && key === scopeKey() && !props.switching && !request.signal.aborted
  try {
    const saved = await saveMainAccountCheckSettings(captured, { enabled: enabled.value, times: [...times.value] }, request.signal)
    if (!current()) return
    apply(saved, captured); success.value = '核对设置已保存，不会立即发起核对。'
    try { const data = await readMainAccountCheckSettings(captured, request.signal); if (current()) apply(data, captured) }
    catch (err) { if (current()) recentError.value = `设置已保存，但最新定时核对记录未能读取：${errorText(err)}；请重新读取。` }
  } catch (err) { if (current()) { error.value = `主站账号核对设置未能确认保存：${errorText(err)}；请先重新读取当前设置。`; loaded.value = false } }
  finally { if (seq === generation) { saving.value = false; controller = null } }
}
watch(() => [scopeKey(), props.switching], () => { reset(); if (!props.switching) void load() }, { immediate: true, flush: 'sync' })
onBeforeUnmount(reset)
const statusText = computed(() => recent.value ? ({ running: '正在核对', success: '核对成功', partial: '部分确认', failed: '核对未完成', skipped: '本次未执行检查', unknown: '上次核对结果尚未确认，请手动核对，不会自动重跑。' }[recent.value.status] || '执行结果尚未确认') : '')
const displayTime = (value: string) => { const date = new Date(value); return Number.isNaN(date.getTime()) ? '时间未确认' : new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', dateStyle: 'short', timeStyle: 'short' }).format(date) }
</script>

<template>
  <section class="rounded-2xl border border-border/50 bg-card shadow-sm p-5 space-y-4" data-testid="main-account-check-settings">
    <div class="flex items-start gap-3">
      <div class="p-2 bg-blue-500/10 text-blue-500 rounded-xl shrink-0"><Timer class="w-5 h-5" /></div>
      <div class="min-w-0 flex-1 space-y-1">
        <h4 class="text-sm font-semibold text-foreground">主站账号定时核对</h4>
        <p class="text-xs leading-5 text-muted-foreground">每天在以下四个时间（新加坡时间）核对主站账号，仅更新调度站对接状态，不探活，不修改主站或上游 Key。关闭后仍可手动核对。</p>
      </div>
    </div>
    <p v-if="loading" role="status" class="text-sm text-muted-foreground">正在读取核对设置…</p>
    <p v-if="error" role="alert" class="text-sm text-warning break-words">{{ error }}</p>
    <p v-if="configError" role="alert" class="text-sm text-warning">{{ configError }}</p>
    <fieldset v-if="loaded" :disabled="saving || switching" class="space-y-3">
      <label class="flex items-center gap-2 text-sm"><input v-model="enabled" type="checkbox" class="accent-primary" />启用定时核对</label>
      <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <label v-for="(_, i) in times" :key="i" class="space-y-1 text-xs text-muted-foreground"><span>第 {{ i + 1 }} 次</span><input v-model="times[i]" type="time" class="h-9 w-full rounded-lg border border-border bg-surface px-2 text-foreground" :aria-label="`第${i + 1}次核对时间`" /></label>
      </div>
      <p v-if="!valid" class="text-sm text-warning">请填写四个不重复、按时间从早到晚排列的时间。</p>
      <p class="text-xs leading-5 text-muted-foreground">保存后只影响还未开始的时段；服务未运行时错过的不补跑，已执行的时段今天不重复。多个工作区同一时间核对时会依次执行。</p>
      <Button type="button" :disabled="!valid || saving || switching" @click="save">{{ saving ? '正在保存…' : '保存核对设置' }}</Button>
    </fieldset>
    <Button type="button" variant="secondary" size="sm" :disabled="loading || saving || switching" @click="load">重新读取核对设置</Button>
    <p v-if="success" role="status" class="text-sm text-emerald-600">{{ success }}</p>
    <p v-if="loaded && dirty" class="text-xs text-warning">设置尚未保存，定时核对仍按上次保存的配置运行。</p>
    <p v-if="loaded && !dirty && savedEnabled === false && !configError" class="text-xs text-muted-foreground">已关闭定时核对，不影响手动核对。</p>
    <p v-if="recentError" role="alert" class="text-xs text-warning">{{ recentError }}</p>
    <div v-else-if="recent" class="text-xs leading-5 text-muted-foreground" role="status">
      <p>最近定时核对：{{ displayTime(recent.startedAt) }}，{{ statusText }}</p>
      <p v-if="recent.status !== 'running' && recent.status !== 'unknown'">本次确认 {{ recent.confirmedActive }} 条对接的主站账号仍在、{{ recent.confirmedMissing }} 条已删除；{{ recent.unconfirmed }} 条未确认，{{ recent.skipped }} 条未检查。未删除任何记录、未修改主站或上游 Key。</p>
      <p v-if="recent.reason">{{ recent.reason }} {{ recent.next }}</p>
    </div>
    <p v-else-if="loaded" class="text-xs text-muted-foreground">近期没有可显示的定时核对记录。</p>
  </section>
</template>
