<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { AlertTriangle, ArrowLeft, ArrowRight, ExternalLink, Loader2, Plus, RefreshCw, Save, Search, X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  createAccountBatch,
  createAccountEvent,
  createAdditionalCost,
  getAccountAsset,
  getRechargeFeeRate,
  listAccountAssets,
  listAccountCostLedger,
  replaceAccountLink,
  refreshAccountStats,
  saveRechargeFeeRate,
  type AccountAsset,
  type AccountAssetDetail,
  type AccountBatchInput,
  type AccountEventInput,
  type AdditionalCostRecord,
  type AdditionalCostSummary,
} from '../../api/dashboardAdmin'
import { listRealConnections } from '../../api/mySites'
import type { RealConnection } from '../../types/mySites'
import { formatCny } from '../../utils/dashboard'
import { prepareIdempotentSubmission, type IdempotentSubmission } from '../../utils/idempotency'

type WorkspaceTab = 'today' | 'assets' | 'ledger' | 'rules'

const router = useRouter()

const props = defineProps<{
  open: boolean
  businessDate: string
  directCost: number | null
  operatingCost: number | null
  adjustedNetProfit: number | null
	summary?: AdditionalCostSummary | null
	initialTab?: WorkspaceTab
	workspaceId: string
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'updated'): void
}>()

const tabs: Array<{ key: WorkspaceTab; label: string }> = [
  { key: 'today', label: '今日成本' },
  { key: 'assets', label: '买号资产' },
  { key: 'ledger', label: '成本账本' },
  { key: 'rules', label: '成本规则' },
]

const activeTab = ref<WorkspaceTab>('today')
const loading = ref(false)
const saving = ref(false)
const errorText = ref('')
const assets = ref<AccountAsset[]>([])
const ledger = ref<AdditionalCostRecord[]>([])
const connections = ref<RealConnection[]>([])
const selectedDetail = ref<AccountAssetDetail | null>(null)
const showBatchForm = ref(false)
const recentBatch = ref<{ id: string; name: string; quantity: number } | null>(null)
const assetPage = ref(1)
const assetHasMore = ref(false)
const ledgerPage = ref(1)
const ledgerHasMore = ref(false)

const batchAttempt = ref<IdempotentSubmission | null>(null)
const eventAttempt = ref<IdempotentSubmission | null>(null)
const linkAttempt = ref<IdempotentSubmission | null>(null)
const statsModeAttempt = ref<IdempotentSubmission | null>(null)

const today = () => props.businessDate || new Date(Date.now() + 8 * 60 * 60 * 1000).toISOString().slice(0, 10)
const assetFilters = reactive({ platform: '', channel: '', accountType: '', status: '', search: '' })
const ledgerFilters = reactive({ from: today(), to: today(), type: '', platform: '', channel: '', batchId: '', accountAssetId: '' })
const costForm = reactive({ type: 'fixed' as 'promotion' | 'fixed' | 'adjustment', name: '', amount: '', businessDate: today(), usageRate: '80', days: '30', note: '' })
const feeForm = reactive({ rate: '1.6', effectiveDate: today() })
const eventForm = reactive({ eventType: 'status', effectiveDate: today(), status: 'dead', statsMode: 'automatic', quotaUsed: '', revenue: '', upstreamCost: '', refund: '', refundClose: false, identifier: '', platform: '', channel: '', accountType: '', purchaseUrl: '', upstreamReferenceUrl: '', note: '' })
const linkForm = reactive({ connectionId: '', upstreamReferenceUrl: '', effectiveFrom: '', manualSameDaySplit: false, previousQuotaUsed: '', previousRevenue: '', replacementQuotaUsed: '', replacementRevenue: '', note: '' })

type AccountRow = { identifier: string; quota: string; connectionId: string; upstreamReferenceUrl: string }
const batchForm = reactive({
  batchName: '', platform: '', channel: '', accountType: '', purchaseDate: today(), purchaseUrl: '',
  defaultUpstreamReferenceUrl: '', quantity: '1', unitAmount: '', totalAmount: '', identifierPaste: '', accountingMode: 'replace_upstream',
  recognitionMode: 'immediate', recognitionStartDate: today(), recognitionDays: '30', statsMode: 'manual', note: '',
})
const accountRows = ref<AccountRow[]>([{ identifier: '', quota: '', connectionId: '', upstreamReferenceUrl: '' }])

watch(() => batchForm.quantity, (quantity) => {
  const safe = Math.max(1, Math.min(500, Number(quantity) || 1))
  while (accountRows.value.length < safe) accountRows.value.push({ identifier: '', quota: '', connectionId: '', upstreamReferenceUrl: '' })
  if (accountRows.value.length > safe) accountRows.value.splice(safe)
})

watch(() => batchForm.identifierPaste, (value) => {
  const identifiers = value.split(/\r?\n/).map(item => item.trim()).filter(Boolean).slice(0, 500)
  if (!identifiers.length) return
  batchForm.quantity = String(identifiers.length)
	while (accountRows.value.length < identifiers.length) accountRows.value.push({ identifier: '', quota: '', connectionId: '', upstreamReferenceUrl: '' })
  identifiers.forEach((identifier, index) => { if (accountRows.value[index]) accountRows.value[index].identifier = identifier })
})

const totalAmountCents = computed(() => {
  const quantity = Math.max(1, Number(batchForm.quantity) || 1)
  const total = batchForm.totalAmount === '' ? null : Math.round(Number(batchForm.totalAmount) * 100)
  const unit = batchForm.unitAmount === '' ? null : Math.round(Number(batchForm.unitAmount) * 100)
  if (total != null && unit != null && total !== unit * quantity) return null
  return total ?? (unit == null ? null : unit * quantity)
})

const allocationPreview = computed(() => {
  const total = totalAmountCents.value ?? 0
  const count = Math.max(1, Number(batchForm.quantity) || 1)
  const base = Math.trunc(total / count)
  return Array.from({ length: count }, (_, index) => base + (index === count - 1 ? total - base * count : 0))
})

const recognitionEndDate = computed(() => {
  const days = batchForm.recognitionMode === 'daily' ? Math.max(1, Number(batchForm.recognitionDays) || 1) : 1
  const parsed = new Date(`${batchForm.recognitionStartDate}T00:00:00Z`)
  if (Number.isNaN(parsed.getTime())) return '—'
  parsed.setUTCDate(parsed.getUTCDate() + days - 1)
  return parsed.toISOString().slice(0, 10)
})

const dailyRecognitionPreview = (accountIndex: number) => {
  if (batchForm.recognitionMode !== 'daily') return []
  const total = allocationPreview.value[accountIndex] ?? 0
  const days = Math.max(1, Number(batchForm.recognitionDays) || 1)
  const base = Math.trunc(total / days)
  const start = new Date(`${batchForm.recognitionStartDate}T00:00:00Z`)
  if (Number.isNaN(start.getTime())) return []
  return Array.from({ length: days }, (_, dayIndex) => {
    const date = new Date(start)
    date.setUTCDate(date.getUTCDate() + dayIndex)
    return {
      date: date.toISOString().slice(0, 10),
      cents: base + (dayIndex === days - 1 ? total - base * days : 0),
    }
  })
}

const eligibleConnections = computed(() => {
  const candidates = connections.value.filter(connection => (
    connection.status === 'active' && connection.ownGroupIds.length === 1 && Boolean(connection.upstreamSiteId && connection.upstreamKeyId)
  ))
  const keyCounts = new Map<string, number>()
  const scopeCounts = new Map<string, number>()
  for (const connection of candidates) {
    const key = `${connection.upstreamSiteId}:${connection.upstreamKeyId}`
    const scope = `${connection.adminAccountId}:${connection.ownGroupIds[0]}`
    keyCounts.set(key, (keyCounts.get(key) ?? 0) + 1)
    scopeCounts.set(scope, (scopeCounts.get(scope) ?? 0) + 1)
  }
  return candidates.filter(connection => (
    keyCounts.get(`${connection.upstreamSiteId}:${connection.upstreamKeyId}`) === 1
    && scopeCounts.get(`${connection.adminAccountId}:${connection.ownGroupIds[0]}`) === 1
  ))
})
const connectionGroups = computed(() => {
  const groups = new Map<string, { label: string; items: RealConnection[] }>()
  for (const connection of eligibleConnections.value) {
    const key = connection.upstreamSiteId
    const group = groups.get(key) ?? { label: connection.upstreamPlatform || connection.upstreamSiteId, items: [] }
    group.items.push(connection)
    groups.set(key, group)
  }
  return [...groups.values()]
})
const connectionLabel = (connection: RealConnection) => [
  connection.upstreamPlatform || connection.upstreamSiteId,
  connection.upstreamGroupName,
  connection.adminAccountName,
  connection.ownGroupNames?.[0],
].filter(Boolean).join(' · ')

const quotaText = (asset: AccountAsset) => {
  if (asset.quotaUsedMicros == null && asset.quotaTotalMicros == null) return '暂无可靠数据'
  const used = asset.quotaUsedMicros == null ? '—' : (asset.quotaUsedMicros / 1_000_000).toFixed(2)
  const total = asset.quotaTotalMicros == null ? '—' : (asset.quotaTotalMicros / 1_000_000).toFixed(2)
  return `${used} / ${total}`
}

const outcomeText = (asset: AccountAsset) => {
  const performance = asset.performance
  if (!performance) return '缺少单号历史快照'
  const terminal = ['dead', 'exhausted', 'closed'].includes(asset.currentStatus)
  const cents = terminal ? performance.finalProfitCents : performance.breakevenDifferenceCents
  if (cents == null) return '暂无可靠数据'
  if (terminal) return `${cents >= 0 ? '最终赚' : '最终亏'} ${formatCny(Math.abs(cents) / 100)}`
  return cents >= 0 ? `已超回本 ${formatCny(cents / 100)}` : `距回本 ${formatCny(Math.abs(cents) / 100)}`
}

const ledgerGroups = computed(() => {
  const groups = new Map<string, { key: string; name: string; type: string; amount: number; records: AdditionalCostRecord[] }>()
  for (const record of ledger.value) {
    const key = `${record.type}:${record.batchId || ''}:${record.accountAssetId || ''}:${record.name}`
    const group = groups.get(key) ?? { key, name: record.name, type: record.type, amount: 0, records: [] }
    group.amount += record.amount
    group.records.push(record)
    groups.set(key, group)
  }
  return [...groups.values()]
})

const filterStorageKey = computed(() => `transithub.account-assets.filters.v1:${props.workspaceId || 'unknown'}`)
watch(() => props.workspaceId, () => {
  try {
    const saved = localStorage.getItem(filterStorageKey.value)
    if (saved) Object.assign(assetFilters, JSON.parse(saved))
  } catch { /* ignore invalid local preference */ }
}, { immediate: true })
watch(assetFilters, () => {
  try { localStorage.setItem(filterStorageKey.value, JSON.stringify(assetFilters)) } catch { /* storage unavailable */ }
}, { deep: true })

const costLines = computed(() => [
  { label: '上游直接成本', value: props.directCost },
  { label: '替代成本扣减', value: props.summary?.replacementDeduction == null ? null : -props.summary.replacementDeduction },
  { label: '买号确认成本', value: props.summary?.accountPurchase ?? 0 },
  { label: '退款冲减', value: props.summary?.accountRefund ?? 0 },
  { label: '充值手续费', value: props.summary?.rechargeFee ?? null },
  { label: '活动摊销', value: props.summary?.promotion ?? 0 },
  { label: '固定费用', value: props.summary?.fixed ?? 0 },
  { label: '手工调整', value: props.summary?.adjustment ?? 0 },
])

const loadAssets = async () => {
	const [result, availableConnections] = await Promise.all([
		listAccountAssets({ ...assetFilters, page: assetPage.value, pageSize: 50 }),
		listRealConnections(),
	])
  assets.value = result.items
	assetHasMore.value = result.hasMore
	connections.value = availableConnections
}

const loadLedger = async () => {
	  const result = await listAccountCostLedger({ ...ledgerFilters, page: ledgerPage.value, pageSize: 100 })
	  ledger.value = result.items
	  ledgerHasMore.value = result.hasMore
}

const searchAssets = () => { assetPage.value = 1; void loadAssets() }
const searchLedger = () => { ledgerPage.value = 1; void loadLedger() }
const changeAssetPage = (delta: number) => { assetPage.value = Math.max(1, assetPage.value + delta); void loadAssets() }
const changeLedgerPage = (delta: number) => { ledgerPage.value = Math.max(1, ledgerPage.value + delta); void loadLedger() }
const openUpstreamManagement = () => {
	emit('close')
	void router.push({ name: 'AdminUpstream' })
}

const loadRules = async () => {
  const result = await getRechargeFeeRate(today())
  feeForm.rate = String(result.rate * 100)
  feeForm.effectiveDate = result.effectiveDate || today()
}

const loadTab = async () => {
  loading.value = true
  errorText.value = ''
  try {
    if (activeTab.value === 'assets') await loadAssets()
    if (activeTab.value === 'ledger') await loadLedger()
    if (activeTab.value === 'rules') await loadRules()
  } catch (error) {
    errorText.value = error instanceof Error ? error.message : '加载失败'
  } finally {
    loading.value = false
  }
}

watch(() => props.open, (open) => {
  if (!open) return
	const nextTab = props.initialTab ?? 'today'
	const changed = activeTab.value !== nextTab
	activeTab.value = nextTab
  selectedDetail.value = null
  ledgerFilters.from = today()
  ledgerFilters.to = today()
	if (!changed) void loadTab()
})
watch(activeTab, () => void loadTab())

const submitBatch = async () => {
  const amountCents = totalAmountCents.value
  if (!batchForm.platform.trim() || !batchForm.channel.trim() || !batchForm.accountType.trim() || amountCents == null || amountCents < 0) {
		errorText.value = '请完整填写平台、渠道、账号类型和单价或总价；同时填写时两者必须相符'
    return
  }
	const missingConnection = batchForm.statsMode === 'automatic' ? accountRows.value.findIndex(row => !row.connectionId.trim()) : -1
	if (missingConnection >= 0) {
		errorText.value = `第 ${missingConnection + 1} 个账号未选择自动统计连接`
		return
	}
	const missingQuota = batchForm.recognitionMode === 'quota' ? accountRows.value.findIndex(row => !row.quota || Number(row.quota) <= 0) : -1
	if (missingQuota >= 0) {
		errorText.value = `第 ${missingQuota + 1} 个账号未填写有效总额度`
		return
	}
  saving.value = true
  errorText.value = ''
  try {
    const input: AccountBatchInput = {
      batchName: batchForm.batchName.trim(), platform: batchForm.platform.trim(), channel: batchForm.channel.trim(),
      accountType: batchForm.accountType.trim(), purchaseDate: batchForm.purchaseDate, purchaseUrl: batchForm.purchaseUrl.trim(),
      defaultUpstreamReferenceUrl: batchForm.defaultUpstreamReferenceUrl.trim(), quantity: Number(batchForm.quantity), totalAmountCents: amountCents,
      identifiers: accountRows.value.map(row => row.identifier.trim()),
      accounts: accountRows.value.map(row => ({
        identifier: row.identifier.trim(), quotaTotalMicros: row.quota ? Math.round(Number(row.quota) * 1_000_000) : null,
        connectionId: row.connectionId.trim(), upstreamReferenceUrl: row.upstreamReferenceUrl.trim(),
      })),
      accountingMode: batchForm.accountingMode as AccountBatchInput['accountingMode'],
      recognitionMode: batchForm.recognitionMode as AccountBatchInput['recognitionMode'],
      recognitionStartDate: batchForm.recognitionStartDate, recognitionDays: Number(batchForm.recognitionDays),
      statsMode: batchForm.statsMode as AccountBatchInput['statsMode'], note: batchForm.note.trim(),
    }
		batchAttempt.value = prepareIdempotentSubmission(batchAttempt.value, 'account-batch', input)
	    const result = await createAccountBatch(input, batchAttempt.value.key)
    showBatchForm.value = false
    selectedDetail.value = null
    recentBatch.value = { id: result.batch.id, name: result.batch.batchName, quantity: result.assets.length }
    assetPage.value = 1
    await loadAssets()
    emit('updated')
		batchAttempt.value = null
  } catch (error) {
    errorText.value = error instanceof Error ? error.message : '保存失败'
  } finally {
    saving.value = false
  }
}

const openAsset = async (asset: AccountAsset) => {
  loading.value = true
  errorText.value = ''
  try {
	selectedDetail.value = await getAccountAsset(asset.id)
	linkForm.upstreamReferenceUrl = selectedDetail.value.asset.upstreamReferenceUrl || ''
	linkForm.connectionId = ''
	linkForm.effectiveFrom = ''
	linkForm.manualSameDaySplit = false
	linkForm.previousQuotaUsed = ''
	linkForm.previousRevenue = ''
	linkForm.replacementQuotaUsed = ''
	linkForm.replacementRevenue = ''
	linkForm.note = ''
	eventForm.statsMode = selectedDetail.value.asset.statsMode
	eventForm.identifier = selectedDetail.value.asset.identifier
	eventForm.platform = selectedDetail.value.asset.platform
	eventForm.channel = selectedDetail.value.asset.channel
		eventForm.accountType = selectedDetail.value.asset.accountType
		eventForm.purchaseUrl = selectedDetail.value.batch.purchaseUrl || ''
		eventForm.upstreamReferenceUrl = selectedDetail.value.asset.upstreamReferenceUrl || ''
  }
  catch (error) { errorText.value = error instanceof Error ? error.message : '加载失败' }
  finally { loading.value = false }
}

const submitEvent = async () => {
  if (!selectedDetail.value) return
  const input: AccountEventInput = { eventType: eventForm.eventType as AccountEventInput['eventType'], effectiveDate: eventForm.effectiveDate, note: eventForm.note.trim() }
	if (input.eventType === 'status' || input.eventType === 'restore') input.status = (input.eventType === 'restore' ? 'active' : eventForm.status) as AccountEventInput['status']
	if (input.eventType === 'refund' && eventForm.refundClose) input.status = 'closed'
	if (input.eventType === 'stats_mode_change') input.statsMode = eventForm.statsMode as 'automatic' | 'manual'
	if (input.eventType === 'metadata_correction') {
		input.identifier = eventForm.identifier.trim()
		input.platform = eventForm.platform.trim()
		input.channel = eventForm.channel.trim()
		input.accountType = eventForm.accountType.trim()
		input.purchaseUrl = eventForm.purchaseUrl.trim()
		input.upstreamReferenceUrl = eventForm.upstreamReferenceUrl.trim()
	}
	if ((input.eventType === 'quota_observation' || input.eventType === 'manual_observation') && eventForm.quotaUsed !== '') input.quotaUsedMicros = Math.round(Number(eventForm.quotaUsed) * 1_000_000)
  if (input.eventType === 'manual_observation') {
    if (eventForm.revenue) input.revenueCents = Math.round(Number(eventForm.revenue) * 100)
    if (eventForm.upstreamCost) input.upstreamCostCents = Math.round(Number(eventForm.upstreamCost) * 100)
  }
  if (input.eventType === 'refund') input.refundCents = Math.round(Number(eventForm.refund) * 100)
  saving.value = true
  errorText.value = ''
  try {
		eventAttempt.value = prepareIdempotentSubmission(eventAttempt.value, 'account-event', {
			assetId: selectedDetail.value.asset.id,
			input,
		})
	    await createAccountEvent(selectedDetail.value.asset.id, input, eventAttempt.value.key)
    selectedDetail.value = await getAccountAsset(selectedDetail.value.asset.id)
    await loadAssets()
    emit('updated')
		eventAttempt.value = null
  } catch (error) { errorText.value = error instanceof Error ? error.message : '保存失败' }
  finally { saving.value = false }
}

const submitLink = async () => {
  if (!selectedDetail.value) return
	if (linkForm.manualSameDaySplit && [linkForm.previousQuotaUsed, linkForm.previousRevenue, linkForm.replacementQuotaUsed, linkForm.replacementRevenue].some(value => value === '' || Number(value) < 0)) {
		errorText.value = '当天换号必须填写旧号和新号的当日额度、营收'
		return
	}
  saving.value = true
	  errorText.value = ''
	  try {
		const input = {
			connectionId: linkForm.connectionId || undefined,
		upstreamReferenceUrl: linkForm.upstreamReferenceUrl.trim(),
		effectiveFrom: linkForm.effectiveFrom,
		manualSameDaySplit: linkForm.manualSameDaySplit,
		previousQuotaUsedMicros: linkForm.manualSameDaySplit ? Math.round(Number(linkForm.previousQuotaUsed) * 1_000_000) : undefined,
		previousRevenueCents: linkForm.manualSameDaySplit ? Math.round(Number(linkForm.previousRevenue) * 100) : undefined,
		replacementQuotaUsedMicros: linkForm.manualSameDaySplit ? Math.round(Number(linkForm.replacementQuotaUsed) * 1_000_000) : undefined,
			replacementRevenueCents: linkForm.manualSameDaySplit ? Math.round(Number(linkForm.replacementRevenue) * 100) : undefined,
			note: linkForm.note.trim(),
		}
		linkAttempt.value = prepareIdempotentSubmission(linkAttempt.value, 'account-link', {
			assetId: selectedDetail.value.asset.id,
			input,
		})
		selectedDetail.value = await replaceAccountLink(selectedDetail.value.asset.id, input, linkAttempt.value.key)
	await loadAssets()
	emit('updated')
		linkAttempt.value = null
  } catch (error) { errorText.value = error instanceof Error ? error.message : '关联保存失败' }
  finally { saving.value = false }
}

const submitStatsMode = async () => {
  if (!selectedDetail.value) return
	  saving.value = true
	  errorText.value = ''
	  try {
		const input: AccountEventInput = {
			eventType: 'stats_mode_change', effectiveDate: today(), statsMode: eventForm.statsMode as 'automatic' | 'manual',
			note: '统计方式切换',
		}
		statsModeAttempt.value = prepareIdempotentSubmission(statsModeAttempt.value, 'account-stats-mode', {
			assetId: selectedDetail.value.asset.id,
			input,
		})
		await createAccountEvent(selectedDetail.value.asset.id, input, statsModeAttempt.value.key)
	selectedDetail.value = await getAccountAsset(selectedDetail.value.asset.id)
	await loadAssets()
		statsModeAttempt.value = null
  } catch (error) { errorText.value = error instanceof Error ? error.message : '统计方式保存失败' }
  finally { saving.value = false }
}

const submitCost = async () => {
  saving.value = true
  errorText.value = ''
  try {
    await createAdditionalCost({
      type: costForm.type, name: costForm.name.trim(), businessDate: costForm.businessDate,
      amount: Number(costForm.amount), usageRate: costForm.type === 'promotion' ? Number(costForm.usageRate) / 100 : 0,
      days: costForm.type === 'adjustment' ? 0 : Number(costForm.days), note: costForm.note.trim(),
    })
    costForm.name = ''; costForm.amount = ''; costForm.note = ''
    emit('updated')
  } catch (error) { errorText.value = error instanceof Error ? error.message : '保存失败' }
  finally { saving.value = false }
}

const submitFee = async () => {
  saving.value = true
  errorText.value = ''
  try {
    await saveRechargeFeeRate({ effectiveDate: feeForm.effectiveDate, rate: Number(feeForm.rate) / 100 })
    emit('updated')
  } catch (error) { errorText.value = error instanceof Error ? error.message : '保存失败' }
  finally { saving.value = false }
}

const refreshStats = async () => {
	saving.value = true
	errorText.value = ''
	try {
		await refreshAccountStats(today())
		emit('updated')
	} catch (error) { errorText.value = error instanceof Error ? error.message : '刷新失败' }
	finally { saving.value = false }
}

const detailTotals = computed(() => {
  const detail = selectedDetail.value
  if (!detail) return { quota: null, revenue: null, upstream: null, refunds: 0 }
	if (detail.dailyStats.some(stat => stat.quality !== 'complete')) {
		const refunds = detail.events.reduce((sum, event) => sum + (event.eventType === 'refund' ? event.refundCents ?? 0 : 0), 0)
		return { quota: null, revenue: null, upstream: null, refunds }
	}
  let quota = 0; let revenue = 0; let upstream = 0
  let hasQuota = false; let hasRevenue = false; let hasUpstream = false
  for (const stat of detail.dailyStats) {
	if (stat.rawQuotaUsedMicros != null) { quota += stat.rawQuotaUsedMicros; hasQuota = true }
	if (stat.revenueCents != null) { revenue += stat.revenueCents; hasRevenue = true }
	if (stat.upstreamCostCents != null) { upstream += stat.upstreamCostCents; hasUpstream = true }
  }
  const refunds = detail.events.reduce((sum, event) => sum + (event.eventType === 'refund' ? event.refundCents ?? 0 : 0), 0)
  return { quota: hasQuota ? quota : null, revenue: hasRevenue ? revenue : null, upstream: hasUpstream ? upstream : null, refunds }
})

const statusText = (status: string) => ({ unactivated: '未激活', active: '使用中', exhausted: '已耗尽', dead: '死号', closed: '已关闭' }[status] ?? status)
const centsText = (value?: number | null) => value == null ? '暂无可靠数据' : formatCny(value / 100)
const ratioText = (value?: number | null) => value == null ? '暂无可靠数据' : `${value.toFixed(2)}x`
const missingFieldText = (field: string) => ({ dailyStats: '完整单号日快照', revenue: '累计营收', quotaUsed: '累计额度', upstreamCost: '叠加上游成本' }[field] ?? field)
const eventTypeText = (eventType: string) => ({ status: '状态变化', restore: '恢复使用', refund: '退款', quota_observation: '额度观察', manual_observation: '手工经营数据', link_change: '关联变化', stats_mode_change: '统计方式变化', metadata_correction: '资料更正' }[eventType] ?? eventType)
const eventSummary = (event: AccountAssetDetail['events'][number]) => {
	const values: string[] = [eventTypeText(event.eventType)]
	if (event.status) values.push(statusText(event.status))
	if (event.statsMode) values.push(event.statsMode === 'automatic' ? '自动统计' : '手工统计')
	for (const value of [event.identifier, event.platform, event.channel, event.accountType]) if (value) values.push(value)
	if (event.purchaseUrl !== undefined) values.push(event.purchaseUrl ? `购买链接 ${event.purchaseUrl}` : '购买链接已清空')
	if (event.quotaUsedMicros != null) values.push(`累计额度 ${(event.quotaUsedMicros / 1_000_000).toFixed(2)}`)
	if (event.revenueCents != null) values.push(`累计营收 ${centsText(event.revenueCents)}`)
	if (event.upstreamCostCents != null) values.push(`累计上游 ${centsText(event.upstreamCostCents)}`)
	if (event.refundCents != null) values.push(`退款 ${centsText(event.refundCents)}`)
	if (event.upstreamReferenceUrl !== undefined) values.push(event.upstreamReferenceUrl ? `上游参考链接 ${event.upstreamReferenceUrl}` : '上游参考链接已清空')
	if (event.note) values.push(event.note)
	return values.join(' · ')
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-[160]">
      <div class="absolute inset-0 bg-background/60 backdrop-blur-sm" @click="emit('close')" />
      <section role="dialog" aria-modal="true" aria-label="成本与买号" class="absolute inset-y-0 right-0 flex w-full max-w-6xl flex-col border-l border-border/60 bg-card shadow-2xl">
        <header class="flex shrink-0 items-center justify-between border-b border-border/60 px-4 py-3 sm:px-6">
          <div><h2 class="text-base font-semibold text-foreground">成本与买号</h2><p class="mt-0.5 text-xs text-muted-foreground">{{ businessDate }} · 经营成本、账号资产与历史账本</p></div>
          <button type="button" class="rounded-md p-2 text-muted-foreground hover:bg-surface-elevated hover:text-foreground" title="关闭" @click="emit('close')"><X class="h-5 w-5" /></button>
        </header>

        <nav class="flex shrink-0 gap-1 overflow-x-auto border-b border-border/60 px-4 py-2 sm:px-6">
          <button v-for="tab in tabs" :key="tab.key" type="button" class="h-9 shrink-0 rounded-md px-3 text-sm font-medium transition-colors" :class="activeTab === tab.key ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-surface-elevated hover:text-foreground'" @click="activeTab = tab.key">{{ tab.label }}</button>
        </nav>

        <main class="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
          <p v-if="errorText" class="mb-4 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">{{ errorText }}</p>
          <div v-if="loading" class="flex justify-center py-16"><Loader2 class="h-6 w-6 animate-spin text-muted-foreground" /></div>

          <section v-else-if="activeTab === 'today'" class="space-y-6">
            <div class="flex justify-end"><Button variant="secondary" :disabled="saving" @click="refreshStats"><RefreshCw class="mr-2 h-4 w-4" :class="saving ? 'animate-spin' : ''" />刷新账号统计</Button></div>
            <div class="grid gap-3 sm:grid-cols-3">
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">今日总成本</p><p class="mt-1 text-xl font-bold tabular-nums">{{ formatCny(operatingCost) }}</p></div>
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">调整后净利润</p><p class="mt-1 text-xl font-bold tabular-nums">{{ formatCny(adjustedNetProfit) }}</p></div>
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">账号统计质量</p><p class="mt-1 text-sm font-semibold">{{ summary?.accountQuality === 'complete' ? '完整' : summary?.accountQuality ? '部分数据' : '暂无可靠数据' }}</p></div>
            </div>
            <div class="grid gap-x-8 sm:grid-cols-2">
              <div v-for="line in costLines" :key="line.label" class="flex items-center justify-between border-b border-border/50 py-2.5 text-sm"><span class="text-muted-foreground">{{ line.label }}</span><span class="font-medium tabular-nums">{{ formatCny(line.value) }}</span></div>
            </div>
            <form class="space-y-3 border-t border-border/60 pt-5" @submit.prevent="submitCost">
              <div class="flex items-center justify-between"><h3 class="text-sm font-semibold">记一笔成本</h3><Button type="button" variant="secondary" @click="activeTab = 'assets'; showBatchForm = true"><Plus class="mr-2 h-4 w-4" />录入买号</Button></div>
              <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                <select v-model="costForm.type" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="promotion">活动赠送</option><option value="fixed">固定费用</option><option value="adjustment">手工调整</option></select>
                <Input v-model="costForm.name" placeholder="名称" required />
                <Input v-model="costForm.amount" type="number" step="0.01" placeholder="金额（元）" required />
                <Input v-model="costForm.businessDate" type="date" required />
                <Input v-if="costForm.type === 'promotion'" v-model="costForm.usageRate" type="number" min="0" max="100" placeholder="预计使用率 %" />
                <Input v-if="costForm.type !== 'adjustment'" v-model="costForm.days" type="number" min="1" placeholder="分摊天数" />
                <Input v-model="costForm.note" class="sm:col-span-2" placeholder="说明（可选）" />
              </div>
              <Button :disabled="saving"><Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" /><Save v-else class="mr-2 h-4 w-4" />保存记录</Button>
            </form>
          </section>

          <section v-else-if="activeTab === 'assets'" class="space-y-4">
            <template v-if="selectedDetail">
              <form class="flex flex-wrap items-center justify-end gap-2" @submit.prevent="submitStatsMode"><span class="text-xs text-muted-foreground">统计方式</span><select v-model="eventForm.statsMode" class="h-8 rounded-md border border-input bg-background px-2 text-sm"><option value="manual">手工</option><option value="automatic">自动</option></select><Button size="sm" variant="secondary" :disabled="saving || eventForm.statsMode === selectedDetail.asset.statsMode">切换</Button></form>
              <button type="button" class="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground" @click="selectedDetail = null"><ArrowLeft class="h-4 w-4" />返回资产列表</button>
              <div class="flex flex-wrap items-start justify-between gap-3"><div><h3 class="text-lg font-semibold">{{ selectedDetail.asset.identifier }}</h3><p class="text-sm text-muted-foreground">{{ selectedDetail.asset.platform }} · {{ selectedDetail.asset.channel }} · {{ selectedDetail.asset.accountType }}</p></div><span class="rounded-md bg-surface-elevated px-2 py-1 text-xs font-medium">{{ statusText(selectedDetail.asset.currentStatus) }}</span></div>
              <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">买号成本</p><p class="mt-1 font-semibold">{{ centsText(selectedDetail.asset.purchaseCostCents) }}</p></div>
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">额度已用</p><p class="mt-1 font-semibold">{{ detailTotals.quota == null ? '暂无可靠数据' : `${(detailTotals.quota / 1_000_000).toFixed(2)} / ${selectedDetail.asset.quotaTotalMicros == null ? '—' : (selectedDetail.asset.quotaTotalMicros / 1_000_000).toFixed(2)}` }}</p></div>
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">累计营收</p><p class="mt-1 font-semibold">{{ centsText(detailTotals.revenue) }}</p></div>
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">实际平均售出倍率</p><p class="mt-1 font-semibold">{{ ratioText(selectedDetail.performance.averageSaleMultiplier) }}</p></div>
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">总成本回收倍数</p><p class="mt-1 font-semibold">{{ ratioText(selectedDetail.performance.costRecoveryMultiple) }}</p></div>
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">{{ selectedDetail.performance.finalProfitCents == null ? '当前回本差额' : '最终盈亏' }}</p><p class="mt-1 font-semibold">{{ selectedDetail.performance.finalProfitCents == null && selectedDetail.performance.breakevenDifferenceCents == null ? '暂无可靠数据' : centsText(selectedDetail.performance.finalProfitCents ?? selectedDetail.performance.breakevenDifferenceCents ?? 0) }}</p></div>
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">退款累计</p><p class="mt-1 font-semibold">{{ centsText(detailTotals.refunds) }}</p></div>
                <div class="rounded-md border border-border/60 p-3"><p class="text-xs text-muted-foreground">ROI</p><p class="mt-1 font-semibold">{{ selectedDetail.performance.roi == null ? '暂无可靠数据' : `${(selectedDetail.performance.roi * 100).toFixed(1)}%` }}</p></div>
              </div>
              <div class="grid gap-4 lg:grid-cols-2">
                <div class="space-y-2 rounded-md border border-border/60 p-4"><h4 class="text-sm font-semibold">来源与关联</h4><div class="flex flex-wrap gap-3"><a v-if="selectedDetail.batch.purchaseUrl" :href="selectedDetail.batch.purchaseUrl" target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 text-sm text-primary hover:underline">购买链接<ExternalLink class="h-3.5 w-3.5" /></a><a v-if="selectedDetail.asset.upstreamReferenceUrl" :href="selectedDetail.asset.upstreamReferenceUrl" target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 text-sm text-primary hover:underline">上游参考<ExternalLink class="h-3.5 w-3.5" /></a></div><p v-if="!selectedDetail.links.length" class="text-sm text-muted-foreground">暂未关联可信连接</p><div v-for="link in selectedDetail.links" :key="link.id" class="border-t border-border/40 pt-2 text-sm"><p>{{ link.siteName }} / {{ link.keyName }} / {{ link.ownGroupName }}</p><p class="mt-0.5 text-xs text-muted-foreground">{{ link.effectiveFrom }} 至 {{ link.effectiveTo || '当前' }}</p></div></div>
                <form class="space-y-3 rounded-md border border-border/60 p-4" @submit.prevent="submitLink">
                  <h4 class="text-sm font-semibold">补充或更换上游关联</h4>
                  <select v-model="linkForm.connectionId" class="h-9 w-full rounded-md border border-input bg-background px-3 text-sm">
                    <option value="">仅更新参考链接</option>
                    <optgroup v-for="group in connectionGroups" :key="group.label" :label="group.label">
                      <option v-for="connection in group.items" :key="connection.id" :value="connection.id">{{ connectionLabel(connection) }}</option>
                    </optgroup>
                  </select>
                  <Input v-model="linkForm.upstreamReferenceUrl" type="url" placeholder="上游参考链接（可选）" />
				  <label class="space-y-1 text-xs text-muted-foreground"><span>关联生效日</span><Input v-model="linkForm.effectiveFrom" type="date" aria-label="关联生效日" /></label>
                  <label class="flex items-start gap-2 text-sm"><input v-model="linkForm.manualSameDaySplit" type="checkbox" class="mt-0.5" /><span>当天换号并拆分当日数据</span></label>
                  <div v-if="linkForm.manualSameDaySplit" class="grid grid-cols-2 gap-2">
                    <Input v-model="linkForm.previousQuotaUsed" type="number" min="0" step="0.000001" placeholder="旧号额度" required />
                    <Input v-model="linkForm.previousRevenue" type="number" min="0" step="0.01" placeholder="旧号营收（元）" required />
                    <Input v-model="linkForm.replacementQuotaUsed" type="number" min="0" step="0.000001" placeholder="新号额度" required />
                    <Input v-model="linkForm.replacementRevenue" type="number" min="0" step="0.01" placeholder="新号营收（元）" required />
                  </div>
                  <Input v-model="linkForm.note" placeholder="更换原因（可选）" />
                  <Button type="submit" :disabled="saving"><Save class="mr-2 h-4 w-4" />保存关联</Button>
                </form>
                <form class="space-y-3 rounded-md border border-border/60 p-4 lg:col-span-2" @submit.prevent="submitEvent">
                  <h4 class="text-sm font-semibold">补充生命周期记录</h4>
				  <div v-if="eventForm.eventType === 'status' && ['dead', 'exhausted', 'closed'].includes(eventForm.status) && selectedDetail.asset.hasActiveLink" class="flex flex-wrap items-center gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-sm"><AlertTriangle class="h-4 w-4 shrink-0 text-warning" /><span class="min-w-0 flex-1">该账号仍关联运行中的上游连接。保存状态不会停用真实连接，请同时安排替换或处理关联。</span><Button type="button" size="sm" variant="secondary" @click="openUpstreamManagement">前往上游处理关联</Button></div>
                  <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                    <select v-model="eventForm.eventType" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="status">状态变化</option><option value="restore">恢复使用</option><option value="refund">退款</option><option value="quota_observation">额度观察</option><option value="manual_observation">手工经营数据</option><option value="metadata_correction">资料更正</option></select>
					<label class="space-y-1 text-xs text-muted-foreground"><span>事件生效日</span><Input v-model="eventForm.effectiveDate" type="date" aria-label="事件生效日" /></label>
                    <select v-if="eventForm.eventType === 'status'" v-model="eventForm.status" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="active">使用中</option><option value="exhausted">已耗尽</option><option value="dead">死号</option><option value="closed">已关闭</option></select>
                    <Input v-if="eventForm.eventType === 'quota_observation' || eventForm.eventType === 'manual_observation'" v-model="eventForm.quotaUsed" type="number" min="0" step="0.000001" placeholder="累计原始额度" />
                    <Input v-if="eventForm.eventType === 'manual_observation'" v-model="eventForm.revenue" type="number" min="0" step="0.01" placeholder="累计营收（元）" />
                    <Input v-if="eventForm.eventType === 'manual_observation'" v-model="eventForm.upstreamCost" type="number" min="0" step="0.01" placeholder="累计上游成本（元）" />
                    <Input v-if="eventForm.eventType === 'refund'" v-model="eventForm.refund" type="number" min="0.01" step="0.01" placeholder="退款金额（正数）" />
                    <label v-if="eventForm.eventType === 'refund'" class="flex items-center gap-2 text-sm"><input v-model="eventForm.refundClose" type="checkbox" />退款并关闭账号</label>
                    <template v-if="eventForm.eventType === 'metadata_correction'">
                      <Input v-model="eventForm.identifier" placeholder="账号标识" required />
                      <Input v-model="eventForm.platform" list="account-platforms" placeholder="平台" required />
                      <Input v-model="eventForm.channel" list="account-channels" placeholder="购买渠道" required />
					  <Input v-model="eventForm.accountType" list="account-types" placeholder="账号类型" required />
					  <Input v-model="eventForm.purchaseUrl" type="url" placeholder="购买链接（可清空）" />
					  <Input v-model="eventForm.upstreamReferenceUrl" type="url" placeholder="上游参考链接（可选）" />
                    </template>
                    <Input v-model="eventForm.note" placeholder="说明（可选）" />
                  </div>
                  <Button type="submit" :disabled="saving"><Save class="mr-2 h-4 w-4" />保存事件</Button>
                </form>
              </div>
              <p v-if="selectedDetail.performance.missingFields?.length" class="text-sm text-warning">暂不计算盈亏：缺少 {{ selectedDetail.performance.missingFields.map(missingFieldText).join('、') }}</p>
              <div>
                <h4 class="mb-2 text-sm font-semibold">每日经营快照</h4>
                <div v-if="!selectedDetail.dailyStats.length" class="text-sm text-muted-foreground">缺少单号历史快照，可通过手工经营数据补录，不会从站点总额猜测。</div>
                <div v-for="stat in selectedDetail.dailyStats" :key="stat.businessDate" class="grid grid-cols-2 gap-2 border-b border-border/50 py-2 text-sm sm:grid-cols-4 lg:grid-cols-7">
                  <span>{{ stat.businessDate }}</span><span>{{ stat.quality === 'complete' ? '完整' : '部分数据' }} · {{ stat.source === 'automatic' ? '自动' : '手工' }}</span>
                  <span>额度 {{ stat.rawQuotaUsedMicros == null ? '—' : (stat.rawQuotaUsedMicros / 1_000_000).toFixed(2) }}</span><span>营收 {{ centsText(stat.revenueCents) }}</span>
                  <span>上游 {{ centsText(stat.upstreamCostCents) }}</span><span>确认成本 {{ centsText(stat.recognizedCostCents) }}</span><span>替代扣减 {{ centsText(stat.replacementDeductionCents) }}</span>
                </div>
              </div>
              <div>
                <h4 class="mb-2 text-sm font-semibold">完整事件时间线</h4>
                <div v-if="!selectedDetail.events.length" class="text-sm text-muted-foreground">暂无事件</div>
				<div v-for="event in selectedDetail.events" :key="event.id" class="flex items-start justify-between gap-3 border-b border-border/50 py-2 text-sm"><span class="min-w-0 break-all">{{ event.effectiveDate }} · {{ eventSummary(event) }}</span><span class="shrink-0 text-xs text-muted-foreground">{{ event.createdAt.slice(0, 16).replace('T', ' ') }}</span></div>
              </div>
            </template>
            <template v-else>
              <div class="flex flex-wrap items-center justify-between gap-3"><div class="flex flex-1 flex-wrap gap-2"><Input v-model="assetFilters.search" class="max-w-52" placeholder="搜索账号标识" /><Input v-model="assetFilters.platform" class="max-w-40" placeholder="平台" /><Input v-model="assetFilters.channel" class="max-w-40" placeholder="渠道" /><Input v-model="assetFilters.accountType" class="max-w-40" placeholder="账号类型" /><select v-model="assetFilters.status" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="">全部状态</option><option value="active">使用中</option><option value="dead">死号</option><option value="exhausted">已耗尽</option><option value="closed">已关闭</option></select><Button variant="secondary" title="查询" @click="searchAssets"><Search class="h-4 w-4" /></Button></div><Button @click="showBatchForm = !showBatchForm"><Plus class="mr-2 h-4 w-4" />录入买号</Button></div>
              <form v-if="showBatchForm" class="space-y-4 border-y border-border/60 py-4" @submit.prevent="submitBatch">
                <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  <Input v-model="batchForm.batchName" placeholder="批次名称（可选）" /><Input v-model="batchForm.platform" list="account-platforms" placeholder="平台" required /><Input v-model="batchForm.channel" list="account-channels" placeholder="购买渠道" required /><Input v-model="batchForm.accountType" list="account-types" placeholder="账号类型" required />
				  <label class="space-y-1 text-xs text-muted-foreground"><span>购买日期</span><Input v-model="batchForm.purchaseDate" type="date" aria-label="购买日期" /></label><Input v-model="batchForm.quantity" type="number" min="1" max="500" placeholder="数量" /><Input v-model="batchForm.unitAmount" type="number" min="0" step="0.01" placeholder="单价（元，二选一）" /><Input v-model="batchForm.totalAmount" type="number" min="0" step="0.01" placeholder="总金额（元，二选一）" />
                  <Input v-model="batchForm.purchaseUrl" type="url" placeholder="购买订单链接" /><Input v-model="batchForm.defaultUpstreamReferenceUrl" type="url" placeholder="默认上游参考链接" />
                  <select v-model="batchForm.accountingMode" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="replace_upstream">替代上游成本</option><option value="additive_upstream">叠加上游成本</option></select>
                  <select v-model="batchForm.recognitionMode" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="immediate">一次确认</option><option value="daily">按天分摊</option><option value="quota">按额度使用确认</option></select>
				  <label class="space-y-1 text-xs text-muted-foreground"><span>确认开始日</span><Input v-model="batchForm.recognitionStartDate" type="date" aria-label="确认开始日" /></label><Input v-if="batchForm.recognitionMode === 'daily'" v-model="batchForm.recognitionDays" type="number" min="1" placeholder="分摊天数" />
                  <select v-model="batchForm.statsMode" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="manual">手工统计</option><option value="automatic">自动统计</option></select>
                  <Input v-model="batchForm.note" placeholder="批次说明" /><textarea v-model="batchForm.identifierPaste" rows="3" class="rounded-md border border-input bg-background px-3 py-2 text-sm sm:col-span-2" placeholder="批量粘贴账号标识，每行一个" />
                </div>
                <div class="flex flex-wrap gap-x-6 gap-y-1 rounded-md bg-surface-elevated px-3 py-2 text-xs text-muted-foreground"><span>批次总额 {{ centsText(totalAmountCents) }}</span><span>确认结束 {{ recognitionEndDate }}</span><span>尾差归最后一个账号、最后一天</span><span>{{ batchForm.accountingMode === 'replace_upstream' ? '买号成本替代关联 Key 上游成本' : '买号成本与上游按量成本叠加' }}</span></div>
                <div>
                  <h4 class="mb-2 text-sm font-semibold">逐号分摊预览</h4>
                  <div class="max-h-72 overflow-auto border-y border-border/60">
                    <div v-for="(row, index) in accountRows" :key="index" class="grid gap-2 border-b border-border/40 py-2 sm:grid-cols-[3rem_1fr_8rem_1fr_1fr]">
                      <span class="pt-2 text-xs text-muted-foreground">#{{ index + 1 }}</span><Input v-model="row.identifier" :placeholder="`账号标识 ${index + 1}`" /><Input v-model="row.quota" type="number" min="0" step="0.000001" :required="batchForm.recognitionMode === 'quota'" :placeholder="batchForm.recognitionMode === 'quota' ? '总额度（必填）' : '总额度'" />
                      <select v-model="row.connectionId" class="h-9 min-w-0 rounded-md border border-input bg-background px-2 text-sm">
                        <option value="">{{ batchForm.statsMode === 'automatic' ? '请选择连接（必填）' : '暂不关联' }}</option>
                        <optgroup v-for="group in connectionGroups" :key="group.label" :label="group.label"><option v-for="connection in group.items" :key="connection.id" :value="connection.id">{{ connectionLabel(connection) }}</option></optgroup>
                      </select>
                      <Input v-model="row.upstreamReferenceUrl" type="url" placeholder="该号上游链接（可选）" /><span class="text-xs text-muted-foreground sm:col-start-2">账号成本 {{ centsText(allocationPreview[index]) }} · {{ batchForm.recognitionMode === 'daily' ? `${batchForm.recognitionDays} 天确认` : batchForm.recognitionMode === 'quota' ? '按额度累计确认' : '一次确认' }}</span>
                      <details v-if="batchForm.recognitionMode === 'daily'" class="text-xs sm:col-span-4 sm:col-start-2">
                        <summary class="cursor-pointer text-primary">查看每天确认金额</summary>
                        <div class="mt-2 grid max-h-32 grid-cols-2 gap-x-4 gap-y-1 overflow-auto border-l border-border/60 pl-3 sm:grid-cols-3 lg:grid-cols-4">
                          <span v-for="day in dailyRecognitionPreview(index)" :key="day.date" class="flex justify-between gap-2 tabular-nums"><span class="text-muted-foreground">{{ day.date }}</span><span>{{ centsText(day.cents) }}</span></span>
                        </div>
                      </details>
                    </div>
                  </div>
                </div>
                 <Button type="submit" :disabled="saving"><Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" /><Save v-else class="mr-2 h-4 w-4" />保存批次</Button>
              </form>
              <div v-if="recentBatch" class="flex flex-wrap items-center justify-between gap-2 border-y border-primary/30 bg-primary/5 px-3 py-2 text-sm">
                <span>已保存批次 <strong>{{ recentBatch.name || recentBatch.id }}</strong>，共 {{ recentBatch.quantity }} 个账号</span>
                <Button variant="ghost" size="sm" title="关闭提示" @click="recentBatch = null"><X class="h-4 w-4" /></Button>
              </div>
              <datalist id="account-platforms"><option v-for="value in [...new Set(assets.map(item => item.platform))]" :key="value" :value="value" /></datalist><datalist id="account-channels"><option v-for="value in [...new Set(assets.map(item => item.channel))]" :key="value" :value="value" /></datalist><datalist id="account-types"><option v-for="value in [...new Set(assets.map(item => item.accountType))]" :key="value" :value="value" /></datalist>
              <div class="hidden overflow-x-auto md:block"><table class="w-full min-w-[980px] text-left text-sm"><thead class="border-b border-border/60 text-xs text-muted-foreground"><tr><th class="py-2">账号 / 平台</th><th>渠道</th><th>状态</th><th>额度进度</th><th>实际平均倍率</th><th>回本 / 最终盈亏</th><th>上游</th><th></th></tr></thead><tbody><tr v-for="asset in assets" :key="asset.id" class="border-b border-border/40" :class="asset.batchId === recentBatch?.id ? 'bg-primary/5' : ''"><td class="py-3"><p class="font-medium">{{ asset.identifier }}</p><p class="text-xs text-muted-foreground">{{ asset.platform }} · {{ asset.accountType }}</p></td><td>{{ asset.channel }}</td><td>{{ statusText(asset.currentStatus) }}</td><td>{{ quotaText(asset) }}</td><td>{{ ratioText(asset.performance?.averageSaleMultiplier) }}</td><td>{{ outcomeText(asset) }}</td><td><a v-if="asset.upstreamReferenceUrl" :href="asset.upstreamReferenceUrl" target="_blank" rel="noopener noreferrer" title="打开上游参考链接" class="inline-flex rounded-md p-2 text-primary hover:bg-surface-elevated"><ExternalLink class="h-4 w-4" /></a><span v-else class="text-xs text-muted-foreground">{{ asset.hasActiveLink ? '已关联' : '未关联' }}</span></td><td class="text-right"><Button variant="secondary" @click="openAsset(asset)">查看 / 操作</Button></td></tr></tbody></table></div><div class="divide-y divide-border/50 md:hidden"><button v-for="asset in assets" :key="asset.id" type="button" class="block w-full py-3 text-left" :class="asset.batchId === recentBatch?.id ? 'bg-primary/5' : ''" @click="openAsset(asset)"><div class="flex items-start justify-between gap-3"><div class="min-w-0"><p class="truncate text-sm font-medium">{{ asset.identifier }}</p><p class="truncate text-xs text-muted-foreground">{{ asset.platform }} · {{ asset.channel }} · {{ asset.accountType }}</p></div><span class="shrink-0 text-xs font-medium">{{ statusText(asset.currentStatus) }}</span></div><div class="mt-2 grid grid-cols-2 gap-2 text-xs"><span>额度 {{ quotaText(asset) }}</span><span class="text-right">{{ outcomeText(asset) }}</span></div></button></div><p v-if="!assets.length" class="py-12 text-center text-sm text-muted-foreground">暂无买号资产</p><div v-if="assets.length || assetPage > 1" class="flex items-center justify-end gap-2 pt-3"><Button variant="secondary" size="sm" title="上一页" :disabled="assetPage === 1" @click="changeAssetPage(-1)"><ArrowLeft class="h-4 w-4" /></Button><span class="text-xs text-muted-foreground">第 {{ assetPage }} 页</span><Button variant="secondary" size="sm" title="下一页" :disabled="!assetHasMore" @click="changeAssetPage(1)"><ArrowRight class="h-4 w-4" /></Button></div>
            </template>
          </section>

          <section v-else-if="activeTab === 'ledger'" class="space-y-4"><div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-4"><Input v-model="ledgerFilters.from" type="date" /><Input v-model="ledgerFilters.to" type="date" /><select v-model="ledgerFilters.type" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="">全部类型</option><option value="account_purchase">买号确认</option><option value="account_refund">退款冲减</option><option value="recharge_fee">手续费</option><option value="promotion">活动</option><option value="fixed">固定</option><option value="adjustment">调整</option></select><Input v-model="ledgerFilters.platform" placeholder="平台" /><Input v-model="ledgerFilters.channel" placeholder="渠道" /><Input v-model="ledgerFilters.batchId" placeholder="批次 ID" /><Input v-model="ledgerFilters.accountAssetId" placeholder="单号 ID" /><Button variant="secondary" @click="searchLedger"><Search class="mr-2 h-4 w-4" />查询账本</Button></div><div class="divide-y divide-border/50"><details v-for="group in ledgerGroups" :key="group.key" class="group py-3"><summary class="grid cursor-pointer list-none grid-cols-[1fr_auto] items-center gap-3 text-sm sm:grid-cols-[1fr_9rem_8rem]"><div class="min-w-0"><p class="truncate font-medium">{{ group.name }}</p><p class="text-xs text-muted-foreground">{{ group.type }} · {{ group.records.length }} 条确认记录</p></div><span class="hidden text-xs text-muted-foreground sm:block">{{ group.records[0]?.businessDate }}</span><span class="text-right font-semibold tabular-nums" :class="group.amount < 0 ? 'text-emerald-600' : ''">{{ formatCny(group.amount) }}</span></summary><div class="mt-3 overflow-x-auto border-t border-border/40"><table class="w-full min-w-[680px] text-left text-xs"><thead class="text-muted-foreground"><tr><th class="py-2">业务日</th><th>金额</th><th>批次 / 单号</th><th>质量</th><th>录入时间</th></tr></thead><tbody><tr v-for="record in group.records" :key="record.id" class="border-t border-border/30"><td class="py-2">{{ record.businessDate }}</td><td>{{ formatCny(record.amount) }}</td><td>{{ record.batchId || '—' }} / {{ record.accountAssetId || '—' }}</td><td>{{ record.estimated ? '估算' : '已确认' }}</td><td>{{ record.createdAt.slice(0, 16).replace('T', ' ') }}</td></tr></tbody></table></div></details><p v-if="!ledgerGroups.length" class="py-12 text-center text-sm text-muted-foreground">所选范围暂无记录</p></div><div v-if="ledger.length || ledgerPage > 1" class="flex items-center justify-end gap-2"><Button variant="secondary" size="sm" title="上一页" :disabled="ledgerPage === 1" @click="changeLedgerPage(-1)"><ArrowLeft class="h-4 w-4" /></Button><span class="text-xs text-muted-foreground">第 {{ ledgerPage }} 页</span><Button variant="secondary" size="sm" title="下一页" :disabled="!ledgerHasMore" @click="changeLedgerPage(1)"><ArrowRight class="h-4 w-4" /></Button></div></section>

          <section v-else class="max-w-3xl space-y-6"><div><h3 class="text-sm font-semibold">充值手续费</h3><div class="mt-3 grid gap-3 sm:grid-cols-[1fr_1fr_auto]"><Input v-model="feeForm.rate" type="number" min="0" max="100" step="0.01" placeholder="费率 %" /><Input v-model="feeForm.effectiveDate" type="date" /><Button :disabled="saving" @click="submitFee"><Save class="mr-2 h-4 w-4" />保存费率</Button></div></div><div class="border-t border-border/60 pt-5"><h3 class="text-sm font-semibold">核算说明</h3><dl class="mt-3 space-y-3 text-sm"><div><dt class="font-medium">替代上游成本</dt><dd class="mt-1 text-muted-foreground">购买价包含额度时，从上游直接成本扣除该账号已对账 Key 成本，再计入买号确认成本。</dd></div><div><dt class="font-medium">叠加上游成本</dt><dd class="mt-1 text-muted-foreground">购买价是订阅或接入费时，买号确认成本与关联上游按量成本同时计入。</dd></div><div><dt class="font-medium">历史不可改写</dt><dd class="mt-1 text-muted-foreground">成本、退款和状态均追加记录；更正通过冲销或新事件完成。</dd></div></dl></div></section>
        </main>
      </section>
    </div>
  </Teleport>
</template>
