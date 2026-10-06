<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { Button } from '@/components/ui/button'
import { connectionHealthMessageKey, formatConnectionHealthTime } from '../../composables/useConnectionHealth'
import { applyHealthRulePresetToAll, deleteHealthRulePreset, getWorkspaceHealthSettings, listHealthRulePresets, saveHealthRulePreset, saveWorkspaceProbeConcurrency, switchWorkspaceHealthRule } from '../../api/connectionHealth'
import type { ConnectionHealthPolicy, HealthRulePreset, HealthRulePresetInput, WorkspaceHealthSettings } from '../../types/connectionHealth'
import { copyHealthRulePresetInput, defaultHealthRulePreset, isReadOnlyHealthRulePreset, validHealthRulePreset } from '../../utils/healthRulePresets'
import HealthRulePresetFields from './HealthRulePresetFields.vue'

const props = defineProps<{ active: boolean; workspaceId?: string; policies: ConnectionHealthPolicy[]; hideWorkspaceSettings?: boolean }>()
const emit = defineEmits<{ (event: 'presets-loaded', presets: HealthRulePreset[]): void; (event: 'settings-loaded', settings: WorkspaceHealthSettings | null): void; (event: 'rules-changed'): void }>()
const settings = ref<WorkspaceHealthSettings | null>(null)
const presets = ref<HealthRulePreset[]>([])
const loading = ref(false), busy = ref(false), error = ref(''), managerOpen = ref(false)
const concurrency = ref<number>(6)
const editor = ref<HealthRulePresetInput | null>(null), editingId = ref('')
type Confirmation = { action: 'save' | 'delete' | 'apply-all' | 'restore-legacy' | 'switch-v2'; title: string; description: string; policies: Array<{ id: string; name: string }>; presetId?: string; input?: HealthRulePresetInput }
const confirmation = ref<Confirmation | null>(null), confirmationError = ref('')
let epoch = 0
let readRevision = 0
const contextCurrent = (current: number) => current === epoch && props.active
const message = (cause: unknown) => t(connectionHealthMessageKey(cause instanceof Error ? cause.message : '', te))
const validConcurrency = computed(() => Number.isInteger(concurrency.value) && concurrency.value >= 1 && concurrency.value <= 10)
const canSave = computed(() => editor.value && validHealthRulePreset(editor.value))
const allPolicies = computed(() => props.policies.map(({ id, name }) => ({ id, name })))
const kindName = (kind: HealthRulePreset['kind']) => ({ recommended: '内置 · 推荐（可修改）', legacy_snapshot: '升级前快照 · 只读', legacy_default: '内置 · 旧规则默认值 · 只读', custom: '自建' })[kind]

const reload = async (preserveDraft = false) => {
  const current = epoch
  const revision = ++readRevision
  loading.value = true; error.value = ''
  try {
    const [nextSettings, nextPresets] = await Promise.all([getWorkspaceHealthSettings(), listHealthRulePresets()])
    if (!contextCurrent(current) || revision !== readRevision) return
    settings.value = nextSettings; presets.value = nextPresets
    emit('settings-loaded', nextSettings)
    if (!preserveDraft) concurrency.value = nextSettings.probeConcurrency
    emit('presets-loaded', nextPresets)
  } catch (cause) { if (contextCurrent(current) && revision === readRevision) error.value = message(cause) }
  finally { if (contextCurrent(current) && revision === readRevision) loading.value = false }
}
watch(() => [props.active, props.workspaceId] as const, ([active]) => {
  epoch++; settings.value = null; presets.value = []; editor.value = null; confirmation.value = null
  managerOpen.value = false; busy.value = false; error.value = ''; confirmationError.value = ''; loading.value = false
  emit('presets-loaded', [])
  emit('settings-loaded', null)
  if (active) void reload()
}, { immediate: true })

const edit = (preset?: HealthRulePreset, copy = false) => {
  if (busy.value || (preset && !copy && isReadOnlyHealthRulePreset(preset))) return
  editor.value = preset ? copyHealthRulePresetInput(preset) : defaultHealthRulePreset()
  editingId.value = preset && !copy ? preset.id : ''
  if (copy && preset) editor.value.name = `${preset.name}（副本）`
  error.value = ''
}
const requestSave = () => {
  if (!canSave.value || busy.value || !editor.value) return
  const selected = presets.value.find(preset => preset.id === editingId.value)
  if (selected && isReadOnlyHealthRulePreset(selected)) return
  const candidate: Confirmation = { action: 'save', title: '保存判定预设', description: '此修改从下一次探活结果起生效，以下策略会同时使用修改后的数字；不换算状态。', policies: selected?.policies ?? [], presetId: editingId.value, input: copyHealthRulePresetInput(editor.value) }
  if (candidate.policies.length) { confirmation.value = candidate; confirmationError.value = '' }
  else void execute(candidate)
}
const requestPresetAction = (action: 'delete' | 'apply-all', preset: HealthRulePreset) => {
  if (busy.value || isReadOnlyHealthRulePreset(preset)) return
  if (action === 'delete' && (preset.kind !== 'custom' || (preset.policies ?? []).length)) return
  confirmationError.value = ''
  confirmation.value = {
    action, presetId: preset.id, title: action === 'delete' ? `删除预设“${preset.name}”` : `全部策略使用“${preset.name}”`,
    description: action === 'delete' ? '仅删除此未被引用的自建预设。' : '以下全部策略将选用此预设，从下一次探活结果起生效；不换算状态，探活间隔、预算和动作开关保持原值。',
    policies: action === 'apply-all' ? allPolicies.value : [],
  }
}
const requestSwitch = (action: 'restore-legacy' | 'switch-v2') => {
  if (busy.value || !settings.value) return
  confirmationError.value = ''
  confirmation.value = { action, title: action === 'restore-legacy' ? '全部退回升级前' : '切换到新规则', policies: allPolicies.value,
    description: `一次操作将切换本工作区规则，并换算本工作区全部 Sub2API 状态。${action === 'restore-legacy' ? '每个策略回到自己的升级前预设，升级后新建的策略使用旧规则（默认值）。' : '各策略当前选用的预设保持不变。'}NewAPI 账号固定使用旧规则。此操作不直接发送主站动作。` }
}
const execute = async (candidate: Confirmation) => {
  if (busy.value) return
  const current = epoch
  readRevision++; loading.value = false
  busy.value = true; error.value = ''; confirmationError.value = ''
  try {
    if (candidate.action === 'save') {
      const saved = await saveHealthRulePreset(candidate.input!, candidate.presetId || undefined)
      if (!contextCurrent(current)) return
      presets.value = candidate.presetId ? presets.value.map(preset => preset.id === saved.id ? saved : preset) : [...presets.value, saved]
      editor.value = null; editingId.value = ''; emit('presets-loaded', presets.value)
    } else if (candidate.action === 'delete') {
      await deleteHealthRulePreset(candidate.presetId!)
      if (!contextCurrent(current)) return
      presets.value = presets.value.filter(preset => preset.id !== candidate.presetId); emit('presets-loaded', presets.value)
    } else {
      const result = candidate.action === 'apply-all' ? await applyHealthRulePresetToAll(candidate.presetId!) : await switchWorkspaceHealthRule(candidate.action)
      if (!contextCurrent(current)) return
      settings.value = result
      // 实际策略指向和状态由父页面重新读取，不在页面猜测换算结果。
    }
    if (!contextCurrent(current)) return
    confirmation.value = null; emit('rules-changed')
    if (candidate.action !== 'save' && candidate.action !== 'delete') await reload(true)
  } catch (cause) {
    if (!contextCurrent(current)) return
    if (confirmation.value) confirmationError.value = message(cause)
    else error.value = message(cause)
  } finally { if (contextCurrent(current)) busy.value = false }
}
const saveConcurrency = async () => {
  if (busy.value || !settings.value || !validConcurrency.value) return
  const current = epoch; busy.value = true; error.value = ''
  readRevision++; loading.value = false
  try {
    const saved = await saveWorkspaceProbeConcurrency(concurrency.value, settings.value.probeConcurrencyVersion)
    if (contextCurrent(current)) { settings.value = saved; concurrency.value = saved.probeConcurrency }
  } catch (cause) { if (contextCurrent(current)) error.value = message(cause) }
  finally { if (contextCurrent(current)) busy.value = false }
}
</script>

<template>
  <section class="mb-4 space-y-3" :data-testid="hideWorkspaceSettings ? 'policy-preset-management' : 'workspace-rule-settings'">
    <p v-if="loading" class="text-xs text-muted-foreground">正在读取判定规则与预设…</p>
    <p v-if="error" class="rounded-lg bg-destructive/10 p-3 text-xs text-destructive" role="alert">{{ error }} <button type="button" class="underline" :disabled="busy || loading" @click="reload(true)">重新读取</button></p>
    <div v-if="!hideWorkspaceSettings && settings" class="space-y-3 rounded-lg border border-border/40 bg-surface/30 p-4">
      <h4 class="text-sm font-semibold">工作区判定规则：{{ settings.ruleVersion === 'v2' ? '新规则' : '旧规则' }}</h4>
      <p class="text-xs text-muted-foreground">上次切换：{{ settings.ruleSwitchedAt ? formatConnectionHealthTime(settings.ruleSwitchedAt) : '尚未切换' }}</p>
      <div class="flex flex-wrap gap-2">
        <Button data-testid="switch-v2" size="sm" variant="secondary" :disabled="busy" @click="requestSwitch('switch-v2')">切换到新规则</Button>
        <Button data-testid="restore-legacy" size="sm" variant="secondary" :disabled="busy" @click="requestSwitch('restore-legacy')">全部退回升级前</Button>
      </div>
      <label class="flex flex-wrap items-center gap-2 text-xs">同时探活数
        <input v-model.number="concurrency" data-testid="probe-concurrency" type="number" min="1" max="10" step="1" class="h-8 w-20 rounded-md border border-border/60 bg-background px-2" :disabled="busy">
        <Button data-testid="save-probe-concurrency" size="sm" :disabled="busy || !validConcurrency" @click="saveConcurrency">保存</Button>
        <span class="text-muted-foreground">当前已保存：{{ settings.probeConcurrency }}</span>
      </label>
      <p class="text-xs text-muted-foreground">只决定同时跑几个，不增加请求次数。范围 1–10，保存后立即生效，已在运行的探活继续完成。</p>
    </div>
    <button data-testid="manage-rule-presets" type="button" class="text-xs font-medium text-primary hover:underline disabled:opacity-50" :disabled="loading || !settings" @click="managerOpen = true">管理预设</button>
  </section>

  <Teleport to="body">
    <div v-if="managerOpen" class="fixed inset-0 z-[170] flex items-center justify-center p-4">
      <div class="absolute inset-0 bg-background/60 backdrop-blur-sm" @click="!busy && (managerOpen = false)" />
      <div role="dialog" aria-modal="true" aria-label="管理判定预设" class="relative flex max-h-[calc(100dvh-2rem)] w-full max-w-3xl flex-col overflow-hidden rounded-2xl border border-border/60 bg-card shadow-2xl">
        <header class="flex items-center justify-between border-b border-border/60 px-5 py-4"><h3 class="text-sm font-semibold">管理判定预设</h3><button aria-label="关闭预设管理" :disabled="busy" @click="managerOpen = false"><X class="h-4 w-4" /></button></header>
        <div class="space-y-4 overflow-y-auto p-5">
          <p v-if="error" role="alert" class="text-xs text-destructive">{{ error }}</p>
          <Button size="sm" variant="secondary" :disabled="busy" @click="edit()">新增预设</Button>
          <ul class="space-y-2">
            <li v-for="preset in presets" :key="preset.id" :data-testid="'rule-preset-' + preset.id" class="space-y-2 rounded-lg border border-border/40 p-3">
              <p class="text-sm font-medium">{{ preset.name }} <span class="text-xs font-normal text-muted-foreground">{{ kindName(preset.kind) }}</span></p>
              <p class="text-xs text-muted-foreground">引用策略：{{ (preset.policies ?? []).map(policy => policy.name || policy.id).join('、') || '无' }}</p>
              <div class="flex flex-wrap gap-3 text-xs font-medium text-primary">
                <button data-action="copy" :disabled="busy" @click="edit(preset, true)">复制</button>
                <template v-if="!isReadOnlyHealthRulePreset(preset)">
                  <button data-action="edit" :disabled="busy" @click="edit(preset)">修改</button>
                  <button data-action="apply-all" :disabled="busy" @click="requestPresetAction('apply-all', preset)">全部策略使用此预设</button>
                  <button data-action="delete" class="text-destructive disabled:text-muted-foreground disabled:opacity-50" :disabled="busy || preset.kind !== 'custom' || (preset.policies ?? []).length > 0" :title="preset.kind !== 'custom' ? '内置预设不能删除' : (preset.policies ?? []).length ? '正在被策略引用，不能删除' : '删除未被引用的预设'" @click="requestPresetAction('delete', preset)">删除</button>
                </template>
              </div>
            </li>
          </ul>
          <div v-if="editor && settings" data-testid="rule-preset-editor" class="space-y-4 border-t border-border/40 pt-4">
            <h4 class="text-sm font-semibold">{{ editingId ? '修改预设' : '新增 / 复制预设' }}</h4>
            <label class="block space-y-1 text-xs">预设名称<input v-model="editor.name" aria-label="预设名称" maxlength="120" class="h-9 w-full rounded-lg border border-border/60 bg-background px-3 text-sm" :disabled="busy"></label>
            <fieldset :disabled="busy"><HealthRulePresetFields v-model="editor" :rule-version="settings.ruleVersion" /></fieldset>
            <p class="text-xs text-muted-foreground">当前规则下不适用的参数会保留；NewAPI 始终使用旧规则相关参数。预设重名时自动加（2）、（3）。</p>
            <div class="flex justify-end gap-2"><Button size="sm" variant="secondary" :disabled="busy" @click="editor = null">取消</Button><Button data-testid="save-rule-preset" size="sm" :disabled="busy || !canSave" @click="requestSave">保存预设</Button></div>
          </div>
        </div>
      </div>
    </div>
    <div v-if="confirmation" class="fixed inset-0 z-[180] flex items-center justify-center p-4">
      <div class="absolute inset-0 bg-background/80 backdrop-blur-sm" @click="!busy && (confirmation = null)" />
      <div role="alertdialog" aria-modal="true" :aria-label="confirmation.title" class="relative max-h-[calc(100dvh-2rem)] w-full max-w-lg overflow-y-auto rounded-lg border border-border/60 bg-card p-5 shadow-2xl">
        <h3 class="text-sm font-semibold">{{ confirmation.title }}</h3>
        <p class="mt-2 text-xs leading-6 text-muted-foreground">{{ confirmation.description }}</p>
        <p class="mt-3 text-xs font-medium">涉及策略（{{ confirmation.policies.length }} 个）：</p>
        <ul class="mt-1 space-y-1 text-xs"><li v-for="policy in confirmation.policies" :key="policy.id">{{ policy.name || policy.id }}</li><li v-if="!confirmation.policies.length">无</li></ul>
        <p v-if="confirmationError" role="alert" class="mt-3 text-xs text-destructive">{{ confirmationError }}</p>
        <div class="mt-5 flex justify-end gap-2"><Button size="sm" variant="secondary" :disabled="busy" @click="confirmation = null">取消</Button><Button data-testid="confirm-health-rule-action" size="sm" :disabled="busy" @click="execute(confirmation)">确认</Button></div>
      </div>
    </div>
  </Teleport>
</template>
