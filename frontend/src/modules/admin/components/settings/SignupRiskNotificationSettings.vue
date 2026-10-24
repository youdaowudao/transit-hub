<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { Button } from '@/components/ui/button'
import { getSignupRiskPreferences, saveSignupRiskPreferences } from '../../api/notifications'
import { safeScopedErrorText } from '../../utils/scopedErrorText'
import type { SignupRiskPreferences } from '../../types/notifications'

const props = defineProps<{ workspaceId: string; platform: string; switching?: boolean }>()
const emit = defineEmits<{ saved: [] }>()
const enabled = ref(false)
const confirmed = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const success = ref('')
let epoch = 0
let request: AbortController | null = null
const cause = (failure: unknown) => failure instanceof Error
  ? failure.message === 'auth.errors.unauthorized' ? '登录状态已失效，请重新登录后读取提醒设置' : safeScopedErrorText(failure.message)
  : '具体原因尚无法确认，请维护人员核查 Hub 后端通知设置接口'
const apply = (value: SignupRiskPreferences) => { enabled.value = value.enabled; confirmed.value = true }
const load = async () => {
  if (!props.workspaceId || props.platform !== 'sub2api' || saving.value || props.switching) return
  request?.abort()
  const abort = new AbortController(); request = abort
  const version = epoch; const workspace = props.workspaceId
  loading.value = true; confirmed.value = false; error.value = success.value = ''
  try {
    const result = await getSignupRiskPreferences(workspace, abort.signal)
    if (!abort.signal.aborted && version === epoch && workspace === props.workspaceId) apply(result)
  } catch (failure) {
    if (!abort.signal.aborted && version === epoch) error.value = `读取高风险注册提醒设置失败：${cause(failure)}。原设置未被当作关闭，请重试读取后再保存。`
  } finally {
    if (!abort.signal.aborted && version === epoch) loading.value = false
  }
}
const save = async () => {
  if (!confirmed.value || loading.value || saving.value || props.platform !== 'sub2api' || props.switching) return
  request?.abort()
  const abort = new AbortController(); request = abort
  const version = epoch; const workspace = props.workspaceId; const desired = enabled.value
  const current = () => !abort.signal.aborted && version === epoch && workspace === props.workspaceId
  saving.value = true; success.value = error.value = ''
  try {
    let result: SignupRiskPreferences
    try {
      result = await saveSignupRiskPreferences({ workspaceAdminAccountId: workspace, enabled: desired }, abort.signal)
    } catch (failure) {
      if (!current()) return
      // A failed response may follow a committed save; confirm through the same
      // read-only endpoint before showing success or changing the original state.
      try {
        result = await getSignupRiskPreferences(workspace, abort.signal)
        if (result.enabled !== desired || result.updatedAt === null) throw failure
      } catch { throw failure }
    }
    if (!current()) return
    apply(result)
    success.value = result.enabled
      ? '高风险注册提醒已开启；首次完整读取后开始提醒，不会修改主站。'
      : '高风险注册提醒已关闭；不再生成新消息，已有消息仍可查看。'
    emit('saved')
  } catch (failure) {
    if (current()) {
      confirmed.value = false
      error.value = `保存高风险注册提醒设置未能确认：${cause(failure)}。原设置未被当作关闭，请重试读取后再保存。`
    }
  } finally {
    if (current()) saving.value = false
  }
}
watch([() => props.workspaceId, () => props.platform, () => props.switching], () => {
  epoch++; request?.abort(); request = null
  enabled.value = confirmed.value = loading.value = saving.value = false
  error.value = success.value = ''
  void load()
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { epoch++; request?.abort() })
</script>

<template>
  <div class="space-y-3 border-t border-border/50 pt-4" aria-label="通知等级与开关">
    <h4 class="text-sm font-semibold">通知等级与开关</h4>
    <div class="flex items-start justify-between gap-4">
      <div><p class="text-sm font-medium">Sub2API 高风险注册提醒 <span class="text-xs text-muted-foreground">重要（弹窗提醒）</span></p><p class="mt-1 text-xs text-muted-foreground">每分钟只读检查当前 Sub2API 站点的注册风险备注。发现新标记用户后弹窗提醒，不会修改主站；请到 Sub2API 用户管理检查。</p></div>
      <label class="relative mt-1 inline-flex shrink-0 cursor-pointer items-center">
        <input v-model="enabled" type="checkbox" class="peer sr-only" aria-label="Sub2API 高风险注册提醒" :disabled="!confirmed || loading || saving || platform !== 'sub2api'">
        <span class="peer h-6 w-11 rounded-full bg-surface-elevated after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:border after:border-border after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-full peer-checked:after:border-white peer-focus-visible:ring-2 peer-focus-visible:ring-primary peer-disabled:opacity-50" />
      </label>
    </div>
    <p class="text-xs text-muted-foreground">首次开启或重新开启时，先完整记录已有标记用户，不弹窗；首次完整读取后开始提醒。关闭期间的注册不会补报，已有消息仍可查看。</p>
    <p class="text-xs text-muted-foreground">开启后，本页每15秒检查这类新提醒；原通知间隔保留，其他等级仍按原间隔提醒。</p>
    <p v-if="platform !== 'sub2api'" class="text-sm text-muted-foreground">这个工作区未连接 Sub2API，无法开启此提醒；请切换到 Sub2API 工作区。</p>
    <p v-if="loading" role="status" class="text-sm text-muted-foreground">正在读取高风险注册提醒设置；读取完成后再编辑，不会修改主站。</p>
    <p v-if="error" role="alert" class="break-words text-sm text-destructive">{{ error }}</p>
    <p v-if="success" role="status" class="text-sm text-green-500">{{ success }}</p>
    <div class="flex flex-wrap gap-3"><Button :disabled="!confirmed || loading || saving || platform !== 'sub2api'" @click="save">{{ saving ? '正在保存高风险注册提醒' : '保存高风险注册提醒' }}</Button><Button variant="secondary" :disabled="loading || saving || platform !== 'sub2api'" @click="load">重试读取注册提醒设置</Button></div>
  </div>
</template>
