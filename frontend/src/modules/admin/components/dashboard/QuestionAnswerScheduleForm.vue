<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { t, te } from '@/locales'
import { ConnectionHealthApiError, createQuestionAnswerSchedule, getQuestionAnswerSchedule, updateQuestionAnswerSchedule, listTestQuestions, previewQuestionAnswerSchedule } from '../../api/connectionHealth'
import { connectionHealthMessageKey } from '../../composables/useConnectionHealth'
import { collectQuestionAnswerBatchTargets } from '../../utils/questionAnswers'
import { questionAnswerScheduleReason, questionAnswerScheduleSlots, questionAnswerScheduleTime, validQuestionAnswerScheduleGrid } from '../../utils/questionAnswerSchedules'
import type { AdminGroupHealth, QuestionAnswerSchedule, QuestionAnswerScheduleInput, QuestionAnswerScheduleLimits, QuestionAnswerSchedulePreview, TestQuestion } from '../../types/connectionHealth'
import type { QuestionAnswerSelectionPreferences } from '../../utils/connectionHealthPreferences'

const props = defineProps<{ initial: QuestionAnswerSchedule | null; groups: AdminGroupHealth[]; workspace: string; preferences: QuestionAnswerSelectionPreferences; limits: QuestionAnswerScheduleLimits | null }>()
const emit = defineEmits<{ (event: 'saved', value: QuestionAnswerSchedule): void; (event: 'cancel'): void; (event: 'dirty', value: boolean): void; (event: 'run', value: QuestionAnswerSchedule): void }>()
const emptyDraft = (): QuestionAnswerScheduleInput => ({ name: '', targetMode: 'groups', selectedGroupIds: props.groups[0] ? [props.groups[0].id] : [], selectedAccountTargetIds: [], models: [...props.preferences.modelIds], questionIds: [...props.preferences.questionIds], reasoningEffort: props.preferences.reasoningEffort, repeatCount: props.preferences.repeatCount, peakStart: '08:00', peakEnd: '22:00', peakIntervalMinutes: 30, offPeakIntervalMinutes: 180, enabled: true })
const draft = ref<QuestionAnswerScheduleInput>(emptyDraft())
const questions = ref<TestQuestion[]>([]), preview = ref<QuestionAnswerSchedulePreview | null>(null)
const loading = ref(false), saving = ref(false), error = ref(''), previewError = ref(''), previewSignature = ref('')
const initializing = ref(false)
const conflict = ref<QuestionAnswerSchedule | null>(null), baseline = ref(''), baseVersion = ref(0)
const modelInput = ref(''), accountInput = ref(''), showTargets = ref(false)
let sequence = 0, controller: AbortController | null = null, saveController: AbortController | null = null
const readable = (key: string) => t(connectionHealthMessageKey(key, te))
const signature = computed(() => JSON.stringify({ targetMode: draft.value.targetMode, selectedGroupIds: draft.value.selectedGroupIds, selectedAccountTargetIds: draft.value.selectedAccountTargetIds, models: draft.value.models, questionIds: draft.value.questionIds, reasoningEffort: draft.value.reasoningEffort, repeatCount: draft.value.repeatCount }))
const accountCandidates = computed(() => {
  const result = collectQuestionAnswerBatchTargets(props.groups).map(target => ({ targetId: target.targetId, accountName: target.accountName, groupNames: target.groupNames }))
  for (const target of [...(props.initial?.targetSelectionSnapshot?.targetPreview ?? []), ...(preview.value?.targetPreview ?? [])]) {
    const existing = result.find(item => item.targetId === target.targetId)
    if (existing) { existing.accountName = target.accountName; existing.groupNames = target.matchedGroups?.map(group => group.name) ?? [] }
    else result.push({ targetId: target.targetId, accountName: target.accountName || target.targetId, groupNames: target.matchedGroups?.map(group => group.name) ?? [] })
  }
  for (const id of draft.value.selectedAccountTargetIds) if (!result.some(item => item.targetId === id)) result.push({ targetId: id, accountName: id, groupNames: [] })
  return result
})
const modelCandidates = computed(() => [...new Set([...draft.value.models, ...(preview.value?.modelCandidates ?? []), ...props.groups.flatMap(group => group.accounts.flatMap(account => (account.models ?? '').split(/[,\n]+/).map(value => value.trim()).filter(Boolean)))])])
const intervals = Array.from({ length: 48 }, (_, index) => (index + 1) * 30)
const futureSlots = computed(() => questionAnswerScheduleSlots(draft.value))
const dailyRequests = computed(() => futureSlots.value.length * (preview.value?.estimatedRequestsPerExecution ?? 0))
const selectionValid = computed(() => (draft.value.targetMode === 'groups' ? draft.value.selectedGroupIds.length : draft.value.selectedAccountTargetIds.length) > 0 && draft.value.models.length > 0 && draft.value.questionIds.length > 0 && Number.isInteger(draft.value.repeatCount) && draft.value.repeatCount >= 1 && draft.value.repeatCount <= 10)
const limitError = computed(() => {
  if (!preview.value || !props.limits) return ''
  if (preview.value.estimatedRequestsPerTarget > 50) return '单账号请求量超过 C1 的 50 条上限'
  if (draft.value.targetMode === 'accounts' && draft.value.selectedAccountTargetIds.length > props.limits.maxScheduleTargets) return '指定账号数量超过运行保护上限'
  if (draft.value.targetMode === 'accounts' && draft.value.selectedAccountTargetIds.length * preview.value.estimatedRequestsPerTarget > props.limits.maxScheduleRequestsPerExecution) return '预计每轮请求超过运行保护上限'
  return ''
})
const dirty = computed(() => JSON.stringify(draft.value) !== baseline.value)
const canSave = computed(() => draft.value.name.trim().length > 0 && draft.value.name.trim().length <= 120 && selectionValid.value && validQuestionAnswerScheduleGrid(draft.value) && !loading.value && !saving.value && !previewError.value && !conflict.value && previewSignature.value === signature.value && !limitError.value)
const toggle = (field: 'selectedGroupIds' | 'selectedAccountTargetIds' | 'models' | 'questionIds', id: string) => { const list = draft.value[field]; draft.value[field] = list.includes(id) ? list.filter(value => value !== id) : [...list, id] }
const setMode = (mode: 'groups' | 'accounts') => { if (draft.value.targetMode === mode) return; draft.value.targetMode = mode; draft.value.selectedGroupIds = []; draft.value.selectedAccountTargetIds = [] }
const addModel = () => { const value = modelInput.value.trim(); if (value && !draft.value.models.includes(value)) draft.value.models.push(value); modelInput.value = '' }
const addAccount = () => {
  const value = accountInput.value.trim()
  if (!value.startsWith(`sub2api:${props.workspace}:`) || value.split(':').length !== 3 || !value.split(':')[2]) { error.value = 'admin.connectionHealth.errors.questionAnswerTargetNotFound'; return }
  if (!draft.value.selectedAccountTargetIds.includes(value)) draft.value.selectedAccountTargetIds.push(value)
  accountInput.value = ''; error.value = ''
}
const refreshPreview = async () => {
  controller?.abort(); const own = ++sequence, workspace = props.workspace
  if (!selectionValid.value) return
  controller = new AbortController(); const signal = controller.signal, requestedSignature = signature.value
  loading.value = true; previewError.value = ''
  try {
    const { targetMode, selectedGroupIds, selectedAccountTargetIds, models, questionIds, reasoningEffort, repeatCount } = draft.value
    const result = await previewQuestionAnswerSchedule({ targetMode, selectedGroupIds: [...selectedGroupIds], selectedAccountTargetIds: [...selectedAccountTargetIds], models: [...models], questionIds: [...questionIds], reasoningEffort, repeatCount, ...(props.initial ? { scheduleId: props.initial.id } : {}) }, signal)
    if (own !== sequence || workspace !== props.workspace || signal.aborted || requestedSignature !== signature.value) return
    if (props.initial && result.scheduleVersion !== baseVersion.value) {
      const current = await getQuestionAnswerSchedule(props.initial.id, signal)
      if (own !== sequence || signal.aborted || workspace !== props.workspace) return
      conflict.value = current; previewError.value = 'admin.connectionHealth.errors.questionAnswerScheduleVersionConflict'; return
    }
    preview.value = result; previewSignature.value = requestedSignature
  } catch (cause) { if (own === sequence && workspace === props.workspace && !signal.aborted) previewError.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { if (own === sequence) loading.value = false }
}
const loadInitial = async () => {
  controller?.abort(); saveController?.abort(); const own = ++sequence, workspace = props.workspace
  const initial = props.initial
  initializing.value = true
  draft.value = initial ? { name: initial.name, targetMode: initial.targetMode, selectedGroupIds: [...initial.selectedGroupIds], selectedAccountTargetIds: [...initial.selectedAccountTargetIds], models: [...initial.models], questionIds: [...initial.questionIds], reasoningEffort: initial.reasoningEffort, repeatCount: initial.repeatCount, peakStart: initial.peakStart.slice(0, 5), peakEnd: initial.peakEnd.slice(0, 5), peakIntervalMinutes: initial.peakIntervalMinutes, offPeakIntervalMinutes: initial.offPeakIntervalMinutes, enabled: initial.enabled } : emptyDraft()
  baseVersion.value = initial?.version ?? 0; baseline.value = JSON.stringify(draft.value); preview.value = null; previewSignature.value = ''; error.value = ''; previewError.value = ''; conflict.value = null
  controller = new AbortController(); const signal = controller.signal; loading.value = true
  try {
    const result = await listTestQuestions(signal)
    if (own !== sequence || signal.aborted || workspace !== props.workspace) return
    questions.value = result
    if (!initial) {
      draft.value.questionIds = draft.value.questionIds.filter(id => result.some(question => question.id === id && question.enabled))
      if (draft.value.questionIds.length === 0) { const fallback = result.find(question => question.enabled && question.isDefault) ?? result.find(question => question.enabled); if (fallback) draft.value.questionIds = [fallback.id] }
      if (draft.value.models.length === 0 && modelCandidates.value.length > 0) draft.value.models = [modelCandidates.value[0]!]
      baseline.value = JSON.stringify(draft.value)
    }
    await refreshPreview()
  } catch (cause) { if (own === sequence && !signal.aborted) error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request' }
  finally { initializing.value = false; if (own === sequence) loading.value = false }
}
const save = async () => {
  if (!canSave.value) return
  saving.value = true; error.value = ''; const workspace = props.workspace, own = sequence
  saveController = new AbortController(); const signal = saveController.signal
  try {
    const { enabled, ...input } = draft.value
    const result = props.initial ? await updateQuestionAnswerSchedule(props.initial.id, input, baseVersion.value, signal) : await createQuestionAnswerSchedule({ ...input, enabled }, signal)
    if (workspace !== props.workspace || signal.aborted || own !== sequence) return
    emit('dirty', false); emit('saved', result)
  } catch (cause) {
    if (workspace !== props.workspace || signal.aborted || own !== sequence) return
    error.value = cause instanceof Error ? cause.message : 'admin.connectionHealth.errors.request'
    if (cause instanceof ConnectionHealthApiError && cause.current && typeof cause.current === 'object' && 'id' in cause.current) conflict.value = cause.current as QuestionAnswerSchedule
  } finally { if (own === sequence) saving.value = false }
}
const reloadConflict = async () => { if (!conflict.value) return; const current = conflict.value; draft.value = { ...draft.value, name: current.name, targetMode: current.targetMode, selectedGroupIds: [...current.selectedGroupIds], selectedAccountTargetIds: [...current.selectedAccountTargetIds], models: [...current.models], questionIds: [...current.questionIds], reasoningEffort: current.reasoningEffort, repeatCount: current.repeatCount, peakStart: current.peakStart.slice(0, 5), peakEnd: current.peakEnd.slice(0, 5), peakIntervalMinutes: current.peakIntervalMinutes, offPeakIntervalMinutes: current.offPeakIntervalMinutes }; baseVersion.value = current.version; baseline.value = JSON.stringify(draft.value); conflict.value = null; error.value = ''; await nextTick(); void refreshPreview() }
watch(() => [props.initial?.id, props.initial?.version, props.workspace], () => void loadInitial(), { immediate: true })
watch(() => JSON.stringify(draft.value), value => emit('dirty', value !== baseline.value), { immediate: true })
watch(signature, () => { if (initializing.value) return; sequence++; controller?.abort(); loading.value = false; previewSignature.value = ''; previewError.value = '' })
onBeforeUnmount(() => { sequence++; controller?.abort(); saveController?.abort() })
</script>

<template>
  <form data-testid="question-answer-schedule-form" class="space-y-5" @submit.prevent="save">
    <div class="flex items-center justify-between gap-3"><h3 class="text-sm font-semibold">{{ initial ? '编辑计划' : '新建计划' }}</h3><button type="button" class="text-xs text-muted-foreground" :disabled="saving || initializing" @click="emit('cancel')">返回计划</button></div>
    <div v-if="initial && !initial.deletedAt && !initial.blockedReason" class="space-y-1"><button type="button" class="rounded-lg border border-border/60 px-3 py-2 text-xs disabled:opacity-50" :disabled="dirty || saving || initializing" :title="dirty ? '请先保存修改' : '使用已保存配置'" @click="emit('run', initial)">立即执行</button><p v-if="dirty" class="text-xs text-muted-foreground">请先保存修改，再立即执行。</p></div>
    <label class="block text-xs font-medium">计划名称<input v-model="draft.name" aria-label="计划名称" maxlength="120" class="mt-2 w-full rounded-lg border border-border/60 bg-background px-3 py-2 text-sm" :disabled="saving || initializing"></label>
    <fieldset :disabled="saving || initializing" class="space-y-3"><legend class="mb-2 text-xs font-semibold">目标方式</legend><div class="flex gap-4"><label v-for="option in ([['groups', '按分组'], ['accounts', '指定账号']] as const)" :key="option[0]" class="flex items-center gap-2 text-sm"><input type="radio" name="schedule-target-mode" :checked="draft.targetMode === option[0]" @change="setMode(option[0])">{{ option[1] }}</label></div>
      <p class="text-xs leading-5 text-muted-foreground">只按选择范围测试，不会读取主力/后备、Priority、健康或调度资格。</p>
      <div v-if="draft.targetMode === 'groups'" class="max-h-52 space-y-2 overflow-y-auto rounded-lg border border-border/50 p-3"><label v-for="group in groups" :key="group.id" class="flex items-start gap-2 text-xs"><input type="checkbox" :checked="draft.selectedGroupIds.includes(group.id)" @change="toggle('selectedGroupIds', group.id)"><span class="break-words" :title="group.name">{{ group.name }} · {{ group.accountCount }} 个账号</span></label><label v-for="id in draft.selectedGroupIds.filter(id => !groups.some(group => group.id === id))" :key="id" class="flex gap-2 text-xs"><input type="checkbox" checked @change="toggle('selectedGroupIds', id)">{{ id }} · 保存的分组</label></div>
      <template v-else><div class="max-h-52 space-y-2 overflow-y-auto rounded-lg border border-border/50 p-3"><label v-for="account in accountCandidates" :key="account.targetId" class="flex items-start gap-2 text-xs"><input type="checkbox" :checked="draft.selectedAccountTargetIds.includes(account.targetId)" @change="toggle('selectedAccountTargetIds', account.targetId)"><span class="break-words" :title="account.targetId">{{ account.accountName }} · {{ account.groupNames.join('、') || '无当前分组' }}</span></label></div><div class="flex gap-2"><input v-model="accountInput" aria-label="补充账号 targetId" :placeholder="`sub2api:${workspace}:账号ID`" class="min-w-0 flex-1 rounded-lg border border-border/50 bg-background px-3 py-2 text-xs"><button type="button" class="rounded-lg border border-border/60 px-3 text-xs" @click="addAccount">加入</button></div><p class="text-[11px] text-muted-foreground">无分组账号可填写当前工作区的 canonical targetId；刷新预计后显示主站确认的名称和存在性。</p></template>
    </fieldset>
    <fieldset :disabled="saving || initializing" class="space-y-3"><legend class="mb-2 text-xs font-semibold">模型与题目</legend><div class="grid gap-2 sm:grid-cols-2"><label v-for="model in modelCandidates" :key="model" class="flex min-w-0 gap-2 rounded-md border border-border/40 p-2 text-xs"><input type="checkbox" :checked="draft.models.includes(model)" @change="toggle('models', model)"><span class="break-all" :title="model">{{ model }}</span></label></div><div class="flex gap-2"><input v-model="modelInput" aria-label="模型名称" placeholder="输入计划要求的模型名称" class="min-w-0 flex-1 rounded-lg border border-border/50 bg-background px-3 py-2 text-xs"><button type="button" class="rounded-lg border border-border/60 px-3 text-xs" @click="addModel">加入模型</button></div><p class="text-[11px] text-muted-foreground">候选来自账号声明及已保存名称；实际可用性只在执行开始时发现，暂时未声明的模型可以保留。</p>
      <div class="max-h-52 space-y-2 overflow-y-auto"><label v-for="question in questions" :key="question.id" class="flex items-start gap-2 rounded-md border border-border/40 p-2 text-xs"><input type="checkbox" :checked="draft.questionIds.includes(question.id)" :disabled="!question.enabled && !draft.questionIds.includes(question.id)" @change="toggle('questionIds', question.id)"><span class="break-words">{{ question.name }}{{ question.enabled ? '' : ' · 已禁用' }}<span class="mt-1 block text-muted-foreground">{{ question.keywords.length ? question.keywords.join('、') : '仅人工判断' }}</span></span></label><p v-if="questions.length === 0" class="text-xs text-muted-foreground">没有测试题目，请先在设置页添加启用题目。</p><label v-for="id in draft.questionIds.filter(id => !questions.some(question => question.id === id))" :key="id" class="flex gap-2 text-xs"><input type="checkbox" checked @change="toggle('questionIds', id)">{{ id }} · 保存的题目已失效</label></div>
      <div class="grid grid-cols-2 gap-3"><label class="text-xs">Reasoning effort<select v-model="draft.reasoningEffort" aria-label="Reasoning effort" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2"><option v-for="effort in ['low', 'medium', 'high', 'xhigh']" :key="effort">{{ effort }}</option></select></label><label class="text-xs">每题重复次数<select v-model.number="draft.repeatCount" aria-label="每题重复次数" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2"><option v-for="count in 10" :key="count" :value="count">{{ count }}</option></select></label></div>
    </fieldset>
    <fieldset :disabled="saving || initializing" class="space-y-3"><legend class="mb-2 text-xs font-semibold">每天一个高峰时段</legend><div class="grid grid-cols-2 gap-3"><label class="text-xs">高峰开始<input v-model="draft.peakStart" aria-label="高峰开始" type="time" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2"></label><label class="text-xs">高峰结束<input v-model="draft.peakEnd" aria-label="高峰结束" type="time" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2"></label><label class="text-xs">高峰内间隔<select v-model.number="draft.peakIntervalMinutes" aria-label="高峰内间隔" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2"><option v-for="minutes in intervals" :key="minutes" :value="minutes">{{ minutes }} 分钟</option></select></label><label class="text-xs">时段外间隔<select v-model.number="draft.offPeakIntervalMinutes" aria-label="时段外间隔" class="mt-2 w-full rounded-lg border border-border/50 bg-background p-2"><option v-for="minutes in intervals" :key="minutes" :value="minutes">{{ minutes }} 分钟</option></select></label></div><p class="text-xs text-muted-foreground">新加坡时间（UTC+8）；高峰开始含、结束不含，跨午夜支持。保存只安排严格未来时刻。</p><p v-if="!validQuestionAnswerScheduleGrid(draft)" class="text-xs text-destructive">开始和结束必须不同；间隔为 30–1440 分钟且是 30 的整数倍。</p><p data-testid="schedule-future-slots" class="break-words text-xs leading-5 text-muted-foreground">未来24小时：{{ futureSlots.map(questionAnswerScheduleTime).join('、') || '—' }}</p></fieldset>
    <section class="space-y-2 rounded-lg border border-border/50 p-3 text-xs"><div class="flex flex-wrap items-center justify-between gap-2"><span>当前预计 {{ preview?.estimatedTargetCount ?? '—' }} 个去重账号</span><button type="button" class="rounded-md border border-border/60 px-2.5 py-1.5 disabled:opacity-50" :disabled="!selectionValid || loading || saving" @click="refreshPreview"><Loader2 v-if="loading" class="mr-1 inline h-3 w-3 animate-spin" />刷新当前预计</button></div><p v-if="preview" class="text-muted-foreground">预计时间 {{ questionAnswerScheduleTime(preview.previewedAt) }} · 每账号 {{ preview.estimatedRequestsPerTarget }} 条（上限50） · 每轮 {{ preview.estimatedRequestsPerExecution }} 条（上限 {{ limits?.maxScheduleRequestsPerExecution ?? '—' }}） · 未来24小时 {{ dailyRequests }} 条（自然日日限 {{ limits?.dailyScheduledRequestLimit ?? '—' }}）</p><p v-if="preview && limits && (preview.estimatedTargetCount > limits.maxScheduleTargets || preview.estimatedRequestsPerExecution > limits.maxScheduleRequestsPerExecution)" class="text-amber-600">当前预计超过运行保护上限；动态范围在执行开始时按实际目标检查，超限整轮跳过。</p><p v-if="previewSignature !== signature" class="text-amber-600">配置已变化，请刷新预计后保存。</p><p v-if="preview && (preview.targetChanges.added.length || preview.targetChanges.removed.length)" class="break-words">成员变化：新增 {{ preview.targetChanges.added.map(item => item.accountName).join('、') || '无' }}；移出 {{ preview.targetChanges.removed.map(item => item.accountName).join('、') || '无' }}</p><p v-for="warning in preview?.warnings ?? []" :key="warning" class="break-words text-amber-600">{{ questionAnswerScheduleReason(warning) }}</p><button v-if="preview" type="button" class="text-primary underline" :aria-expanded="showTargets" @click="showTargets = !showTargets">查看账号</button><ul v-if="showTargets" class="max-h-44 space-y-2 overflow-y-auto"><li v-for="target in preview?.targetPreview ?? []" :key="target.targetId" class="break-words">{{ target.accountName }} · {{ target.matchedGroups.map(group => group.name).join('、') || '无当前分组' }}{{ target.missing ? ' · 已删除' : '' }}</li></ul></section>
    <label v-if="!initial" class="flex gap-2 text-xs"><input v-model="draft.enabled" type="checkbox">保存后启用计划（不会立即执行）</label>
    <p v-if="error || previewError || limitError" role="alert" class="break-words text-xs text-destructive">{{ limitError || readable(error || previewError) }}</p><div v-if="conflict" class="rounded-lg border border-amber-500/30 p-3 text-xs"><p>计划已变化，当前权威版本 {{ conflict.version }}；原输入仍保留。</p><button type="button" class="mt-2 text-primary underline" @click="reloadConflict">重新载入当前计划</button></div>
    <div class="flex justify-end gap-2 border-t border-border/50 pt-4"><button type="button" class="rounded-lg border border-border/60 px-3 py-2 text-xs" :disabled="saving || initializing" @click="emit('cancel')">取消</button><button type="submit" class="rounded-lg bg-primary px-4 py-2 text-xs font-medium text-primary-foreground disabled:opacity-50" :disabled="!canSave"><Loader2 v-if="saving" class="mr-1 inline h-3.5 w-3.5 animate-spin" />保存计划</button></div>
  </form>
</template>
