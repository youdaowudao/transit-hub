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
  getAdditionalCost,
  updateAdditionalCost,
  deleteAdditionalCost,
  getAccountAsset,
  getRechargeFeeRate,
  listRechargeFeeRateHistory,
  listAccountAssets,
  listAccountCostLedger,
  replaceAccountLink,
  refreshAccountStats,
  saveRechargeFeeRate,
  deleteRechargeFeeRate,
  listRevenueDiscountRates,
  saveRevenueDiscountRate,
  deleteRevenueDiscountRate,
  type AccountAsset,
  type AccountAssetDetail,
  type AccountBatchInput,
  type AccountEventInput,
  type AdditionalCostRecord,
  type AdditionalCostSummary,
  type RechargeFeeRate,
  type RevenueDiscountRate,
} from '../../api/dashboardAdmin'
import { listRealConnections } from '../../api/mySites'
import type { RealConnection } from '../../types/mySites'
import { formatAmount, formatRatePercent } from '../../utils/dashboard'
import { formatLastUsedAt } from '../../utils/userLastUsed'
import { prepareIdempotentSubmission, type IdempotentSubmission } from '../../utils/idempotency'

type WorkspaceTab = 'today' | 'assets' | 'ledger' | 'rules'

const router = useRouter()

const props = defineProps<{
  open: boolean
  businessDate: string
  directCost: number | null
  operatingCost: number | null
  adjustedNetProfit: number | null
  todayRevenue?: number | null
  profitMargin?: number | null
  summary?: AdditionalCostSummary | null
  initialTab?: WorkspaceTab
  workspaceId: string
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'updated'): void
  (event: 'open-upstream-details'): void
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
const statsNotice = ref('')
const costNotice = ref('')
const costReloadNeeded = ref(false)
const legacyCostNotice = ref('')
const todayLedgerError = ref('')
// Reads must distinguish closing and reopening the same workspace.
const costReadGeneration = ref(0)
const clearCostNotice = () => {
  costNotice.value = ''
  costReloadNeeded.value = false
}
const assets = ref<AccountAsset[]>([])
const ledger = ref<AdditionalCostRecord[]>([])
const todayLedger = ref<AdditionalCostRecord[]>([])
const todayLedgerLoadFailed = ref(false)
const connections = ref<RealConnection[]>([])
const feeRateHistory = ref<RechargeFeeRate[]>([])
const feeRateHistoryLoadFailed = ref(false)
const discountHistory = ref<RevenueDiscountRate[]>([])
const discountHistoryLoadFailed = ref(false)
const discountEditNotice = ref('')
const selectedDetail = ref<AccountAssetDetail | null>(null)
const showBatchForm = ref(false)
const recentBatch = ref<{ id: string; name: string; quantity: number } | null>(null)
const assetPage = ref(1)
const assetHasMore = ref(false)
const ledgerPage = ref(1)
const ledgerHasMore = ref(false)
const editingCost = ref<{ sourceId: string; type: 'promotion' | 'fixed' | 'adjustment' } | null>(null)
// 每次切换工作区都递增，所有异步读请求只允许把结果写回发起时的工作区。
const workspaceGeneration = ref(0)

const batchAttempt = ref<IdempotentSubmission | null>(null)
const eventAttempt = ref<IdempotentSubmission | null>(null)
const linkAttempt = ref<IdempotentSubmission | null>(null)
const statsModeAttempt = ref<IdempotentSubmission | null>(null)

const today = () => props.businessDate || new Date(Date.now() + 8 * 60 * 60 * 1000).toISOString().slice(0, 10)
// 营收折扣率最早明天生效：保存时按当时日期校验；零点前提交、零点后才落库的请求最多改动刚开始的这一天，已结算日期不变。
const tomorrow = () => {
  const next = new Date(`${today()}T00:00:00Z`)
  next.setUTCDate(next.getUTCDate() + 1)
  return next.toISOString().slice(0, 10)
}
const assetFilters = reactive({ platform: '', channel: '', accountType: '', status: '', search: '' })
const ledgerFilters = reactive({ from: today(), to: today(), type: '', platform: '', channel: '', batchId: '', accountAssetId: '' })
const costForm = reactive({ type: 'fixed' as 'promotion' | 'fixed' | 'adjustment', name: '', amount: '', businessDate: today(), usageRate: '80', days: '30', note: '' })
const feeForm = reactive({ rate: '1.6', effectiveDate: today() })
// 营收折扣率三项都按占主站消费的百分比填写。
const discountForm = reactive({ giftRate: '', newUserRate: '', referralRate: '', effectiveDate: tomorrow() })
const eventForm = reactive({ eventType: 'status', effectiveDate: today(), status: 'dead', statsMode: 'automatic', quotaUsed: '', revenue: '', upstreamCost: '', refund: '', refundClose: false, identifier: '', platform: '', channel: '', accountType: '', purchaseUrl: '', upstreamReferenceUrl: '', note: '' })
const linkForm = reactive({ connectionId: '', upstreamReferenceUrl: '', effectiveFrom: '', manualSameDaySplit: false, previousQuotaUsed: '', previousRevenue: '', replacementQuotaUsed: '', replacementRevenue: '', note: '' })

type AccountRow = { identifier: string; quota: string; connectionId: string; upstreamReferenceUrl: string }
const batchForm = reactive({
  batchName: '', platform: '', channel: '', accountType: '', purchaseDate: today(), purchaseUrl: '',
  defaultUpstreamReferenceUrl: '', quantity: '1', unitAmount: '', totalAmount: '', identifierPaste: '', accountingMode: 'replace_upstream',
  recognitionMode: 'immediate', recognitionStartDate: today(), recognitionDays: '30', statsMode: 'manual', note: '',
})
const accountRows = ref<AccountRow[]>([{ identifier: '', quota: '', connectionId: '', upstreamReferenceUrl: '' }])

type WorkspaceRequest = { workspaceId: string; generation: number }
const currentWorkspaceRequest = (): WorkspaceRequest => ({ workspaceId: props.workspaceId, generation: workspaceGeneration.value })
const isCurrentWorkspaceRequest = (request: WorkspaceRequest) => (
  request.workspaceId === props.workspaceId && request.generation === workspaceGeneration.value
)
const isCurrentCostRead = (request: WorkspaceRequest, generation: number) => (
  isCurrentWorkspaceRequest(request) && generation === costReadGeneration.value && props.open
)

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
    const group = groups.get(key) ?? { label: connection.siteName || connection.upstreamPlatform || connection.upstreamSiteId, items: [] }
    group.items.push(connection)
    groups.set(key, group)
  }
  return [...groups.values()]
})
const connectionLabel = (connection: RealConnection) => [
  connection.siteName || connection.upstreamPlatform || connection.upstreamSiteId,
  connection.connectionName || connection.adminAccountName,
  connection.keyName || connection.upstreamKeyId,
  connection.ownGroupName || connection.ownGroupNames?.[0],
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
  if (terminal) return `${cents >= 0 ? '最终赚' : '最终亏'} ${formatAmount(Math.abs(cents) / 100)}`
  return cents >= 0 ? `已超回本 ${formatAmount(cents / 100)}` : `距回本 ${formatAmount(Math.abs(cents) / 100)}`
}

const ledgerGroups = computed(() => {
  const groups = new Map<string, { key: string; name: string; type: string; amount: number; records: AdditionalCostRecord[] }>()
  for (const record of ledger.value) {
    const key = `${record.type}:${record.batchId || ''}:${record.accountAssetId || ''}:${record.sourceId || record.id}`
    const group = groups.get(key) ?? { key, name: record.name, type: record.type, amount: 0, records: [] }
    group.amount += record.amount
    group.records.push(record)
    groups.set(key, group)
  }
  return [...groups.values()]
})

const isManualCost = (record: AdditionalCostRecord) => (
  ['promotion', 'fixed', 'adjustment'].includes(record.type) && !record.batchId && !record.accountAssetId
)
const knownOriginalAmount = (records: AdditionalCostRecord[]) => {
  const value = records[0]?.originalAmount
  if (value == null || !Number.isFinite(value) || (value === 0 && records.some(item => item.amount !== 0))) return null
  return value
}
const originalAmountText = (records: AdditionalCostRecord[]) => {
  const original = knownOriginalAmount(records)
  return original == null ? '原始总额未记录' : formatAmount(original)
}
const expectedAmount = (records: AdditionalCostRecord[]) => {
  const original = knownOriginalAmount(records)
  if (original == null) return null
  if (records[0]?.type !== 'promotion') return original
  const rate = records[0]?.usageRate
  if (rate == null && records.some(item => item.amount !== 0)) return null
  return Math.round(original * (rate ?? 0) * 100) / 100
}
const costErrorReason = (error: unknown) => {
  const message = error instanceof Error ? error.message : ''
  const known: Record<string, string> = {
    'dashboard.additionalCost.errors.invalidType': '费用类型不支持，请选择固定费用、活动赠送或手工调整',
    'dashboard.additionalCost.errors.invalidAmount': '金额无效，请填写有效金额；固定费用和赠送金额不能为负数',
    'dashboard.additionalCost.errors.invalidDate': '日期无效，请重新选择费用日期',
    'dashboard.additionalCost.errors.invalidDays': '固定费用金额不能为负数，分摊天数必须为大于零的整数，请核对后保存',
    'dashboard.additionalCost.errors.invalidRate': '使用率或费率必须在0至100%之间，活动分摊天数必须大于零，请核对后保存',
    'dashboard.additionalCost.errors.notFound': '这笔费用已不存在或不属于当前工作区，请重新加载费用确认',
    'dashboard.additionalCost.errors.protected': '这条记录由买号、退款或系统统计生成，请到对应功能处理',
    'dashboard.additionalCost.errors.unavailable': '费用存储暂时无法使用，请检查后端存储连接后重新加载费用',
    'dashboard.additionalCost.errors.promotionRetired': '活动赠送已停止新增：充值赠送请到‘成本规则’设置营收折扣率按比例扣除',
    'dashboard.additionalCost.errors.feeRateNotFound': '这条规则已不存在或不属于当前工作区，请点击‘重新加载费用’确认',
    'dashboard.revenueDiscount.errors.invalidRate': '每项折扣率需在0至100%之间，合计必须小于100%',
    'dashboard.revenueDiscount.errors.invalidDate': '生效日期无效，请重新选择',
    'dashboard.revenueDiscount.errors.pastDate': '生效日期最早为明天（今天和之前的日期不变），请改为明天或以后',
    'dashboard.revenueDiscount.errors.notFound': '这条规则已不存在或不属于当前工作区，请点击‘重新加载费用’确认',
    'dashboard.revenueDiscount.errors.locked': '这条折扣率已经生效，删除会改变今天或过去日期的营收，所以不能删除；要停用，请新增一条从明天起生效的折扣率（可都填 0）',
    'admin.dashboard.adminAuth.errors.network': '无法连接本站后端，请检查网络及后端服务',
    'auth.errors.unauthorized': '本站登录已失效，请重新登录后到成本账本核查结果',
    'admin.dashboard.adminAuth.errors.request': '后端没有返回具体原因，请查看后端日志核查本次费用操作',
  }
  if (known[message]) return known[message]
  if (!message || /^(?:admin|dashboard|auth)\.[\w.]+$/.test(message) || /(?:SQLSTATE|SELECT\s|INSERT\s|UPDATE\s|DELETE\s|postgres(?:ql)?:\/\/)/i.test(message)) return '后端未返回可确认的具体原因，请查看后端日志核查本次费用操作'
  return message
    .replace(/(?:Bearer\s+)[^\s,;]+/gi, 'Bearer [已隐藏]')
    .replace(/((?:password|passwd|secret|token|api[_-]?key|authorization|cookie)[\s"':=]+)[^\s,;]+/gi, '$1[已隐藏]')
    .replace(/(https?:\/\/)[^\s/@]+:[^\s/@]+@/gi, '$1[已隐藏]@')
    .replace(/[A-Za-z0-9_-]{40,}(?:\.[A-Za-z0-9_-]+)*/g, '[已隐藏]')
    .slice(0, 500)
}
const costFailureText = (error: unknown, step: '读取' | '保存' | '删除' | '保存费率') => {
  const reason = costErrorReason(error)
  if (step === '读取') return `费用读取失败：${reason}。请重新加载费用后再操作；不会修改主站。`
  if (error instanceof Error && /^dashboard\.additionalCost\.errors\.(?:invalidType|invalidAmount|invalidDate|invalidDays|invalidRate|notFound|protected|promotionRetired)$/.test(error.message)) {
    return `费用${step === '保存费率' ? '费率保存' : step}未执行：${reason}。原记录未改变，请按原因核对后重试；不会修改主站。`
  }
  return `费用${step === '保存费率' ? '费率保存' : step}未能确认：${reason}。暂时无法确认费用是否${step === '删除' ? '删除' : '保存'}，请先到成本账本核查，再决定是否重试；不会修改主站。`
}
// 规则（营收折扣率、手续费率）的保存与删除失败说明：业务校验失败说明原规则未变，其他失败说明结果未确认。
const ruleFailureText = (error: unknown, action: '折扣率保存' | '折扣率删除' | '费率删除') => {
  const reason = costErrorReason(error)
  if (error instanceof Error && /^dashboard\.(?:revenueDiscount\.errors\.(?:invalidRate|invalidDate|pastDate|notFound|locked)|additionalCost\.errors\.feeRateNotFound)$/.test(error.message)) {
    return `${action}未执行：${reason}。原规则未改变，请按原因核对后重试；不会修改主站。`
  }
  return `${action}未能确认：${reason}。暂时无法确认是否已${action.endsWith('删除') ? '删除' : '保存'}，请点击‘重新加载费用’查看规则历史，再决定是否重试；不会修改主站。`
}
const beginCostEdit = async (group: { records: AdditionalCostRecord[] }) => {
  const first = group.records[0]
  if (saving.value || !first || !isManualCost(first)) return
  const sourceId = first.sourceId || first.id
  const request = currentWorkspaceRequest()
  const generation = ++costReadGeneration.value
  loading.value = false
  saving.value = true
  errorText.value = ''
  costNotice.value = ''
  costReloadNeeded.value = false
  legacyCostNotice.value = ''
  try {
    const result = await getAdditionalCost(sourceId)
    if (!isCurrentCostRead(request, generation)) return
    const records = result.items.slice().sort((left, right) => left.businessDate.localeCompare(right.businessDate))
    const source = records[0]
    if (!source || records.some(item => !isManualCost(item))) throw new Error('这笔费用没有可编辑的完整记录，请重新加载费用确认')
    editingCost.value = { sourceId, type: source.type as 'promotion' | 'fixed' | 'adjustment' }
    costForm.type = editingCost.value.type
    costForm.name = source.name
    const original = knownOriginalAmount(records)
    costForm.amount = original == null ? '' : String(original)
    if (original == null) legacyCostNotice.value = '这笔旧费用未记录原始总额，请重新填写总额并核对日期、天数和使用率后保存；不会修改主站。'
    costForm.businessDate = source.businessDate
    costForm.usageRate = source.usageRate == null && records.some(item => item.amount !== 0) && source.type === 'promotion' ? '' : String((source.usageRate ?? 0) * 100)
    costForm.days = String(source.days && source.days > 0 ? source.days : records.length)
    costForm.note = source.note || ''
    activeTab.value = 'today'
  } catch (error) {
    if (isCurrentCostRead(request, generation)) errorText.value = costFailureText(error, '读取')
  } finally {
    if (isCurrentWorkspaceRequest(request)) saving.value = false
  }
}
const removeCost = async (group: { records: AdditionalCostRecord[] }) => {
  const first = group.records[0]
  if (saving.value || !first || !isManualCost(first)) return
  const sourceId = first.sourceId || first.id
  const request = currentWorkspaceRequest()
  const generation = ++costReadGeneration.value
  loading.value = false
  saving.value = true
  errorText.value = ''
  costNotice.value = ''
  costReloadNeeded.value = false
  let step: '读取' | '删除' = '读取'
  try {
    const result = await getAdditionalCost(sourceId)
    if (!isCurrentCostRead(request, generation)) return
    const records = result.items.slice().sort((left, right) => left.businessDate.localeCompare(right.businessDate))
    const source = records[0]
    if (!source || records.some(item => !isManualCost(item))) throw new Error('这笔费用没有可删除的完整记录，请重新加载费用确认')
    const total = records.reduce((sum, record) => sum + record.amountCents, 0) / 100
    const original = knownOriginalAmount(records)
    const originalText = original == null ? '未记录' : formatAmount(original)
    if (!window.confirm(`删除整笔费用「${source.name}」？输入金额 ${originalText}，计入总额 ${formatAmount(total)}，覆盖 ${source.businessDate} 至 ${records[records.length - 1]!.businessDate}，共 ${records.length} 条分摊。删除后将重算这些日期的成本和利润；不会修改主站。确定后删除，取消则保留原记录。`)) return
    step = '删除'
    await deleteAdditionalCost(sourceId)
    if (!isCurrentWorkspaceRequest(request)) return
    if (editingCost.value?.sourceId === sourceId) cancelCostEdit()
    emit('updated')
    await refreshAfterCostWrite('费用已删除，相关日期的成本和利润已重新计算；不会修改主站。', request)
  } catch (error) {
    if (isCurrentWorkspaceRequest(request) && (step === '删除' || isCurrentCostRead(request, generation))) errorText.value = costFailureText(error, step)
  } finally {
    if (isCurrentWorkspaceRequest(request)) saving.value = false
  }
}

const cancelCostEdit = () => {
  editingCost.value = null
  costForm.type = 'fixed'
  costForm.name = ''
  costForm.amount = ''
  costForm.businessDate = today()
  costForm.usageRate = '80'
  costForm.days = '30'
  costForm.note = ''
  legacyCostNotice.value = ''
}

const resetWorkspaceData = () => {
  saving.value = false
  loading.value = false
  selectedDetail.value = null
  editingCost.value = null
  batchAttempt.value = null
  eventAttempt.value = null
  linkAttempt.value = null
  statsModeAttempt.value = null
  showBatchForm.value = false
  recentBatch.value = null
  assets.value = []
  ledger.value = []
  todayLedger.value = []
  connections.value = []
  feeRateHistory.value = []
  discountHistory.value = []
  todayLedgerLoadFailed.value = false
  feeRateHistoryLoadFailed.value = false
  discountHistoryLoadFailed.value = false
  discountEditNotice.value = ''
  Object.assign(discountForm, { giftRate: '', newUserRate: '', referralRate: '', effectiveDate: tomorrow() })
  assetPage.value = 1
  assetHasMore.value = false
  ledgerPage.value = 1
  ledgerHasMore.value = false
  ledgerFilters.from = today()
  ledgerFilters.to = today()
  ledgerFilters.type = ''
  ledgerFilters.platform = ''
  ledgerFilters.channel = ''
  ledgerFilters.batchId = ''
  ledgerFilters.accountAssetId = ''
  cancelCostEdit()
}

const filterStorageKey = computed(() => `transithub.account-assets.filters.v1:${props.workspaceId || 'unknown'}`)
watch(() => props.workspaceId, () => {
	workspaceGeneration.value += 1
  costReadGeneration.value += 1
	Object.assign(assetFilters, { platform: '', channel: '', accountType: '', status: '', search: '' })
	resetWorkspaceData()
	errorText.value = ''
  statsNotice.value = ''
  costNotice.value = ''
  costReloadNeeded.value = false
  try {
    const saved = localStorage.getItem(filterStorageKey.value)
    if (saved) Object.assign(assetFilters, JSON.parse(saved))
  } catch { /* ignore invalid local preference */ }
  // 该 watcher 注册在加载函数之前；用微任务推迟首次加载，兼容组件以 open=true 初始挂载。
  if (props.open) void Promise.resolve().then(() => reloadActiveTab())
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

const loadAssets = async (request = currentWorkspaceRequest()) => {
	const [result, availableConnections] = await Promise.all([
		listAccountAssets({ ...assetFilters, page: assetPage.value, pageSize: 50 }),
		listRealConnections(),
	])
	if (!isCurrentWorkspaceRequest(request)) return
  assets.value = result.items
	assetHasMore.value = result.hasMore
	connections.value = availableConnections
}

const loadLedger = async (request = currentWorkspaceRequest(), generation = costReadGeneration.value) => {
  if (!isCurrentCostRead(request, generation)) return
  try {
    const result = await listAccountCostLedger({ ...ledgerFilters, page: ledgerPage.value, pageSize: 100 })
    if (!isCurrentCostRead(request, generation)) return
    ledger.value = result.items
    ledgerHasMore.value = result.hasMore
  } catch (error) {
    if (isCurrentCostRead(request, generation)) throw error
  }
}

const loadTodayLedger = async (request = currentWorkspaceRequest(), generation = costReadGeneration.value): Promise<unknown | null> => {
  if (!isCurrentCostRead(request, generation)) return null
  if (isCurrentCostRead(request, generation)) { todayLedgerLoadFailed.value = false; todayLedgerError.value = '' }
  try {
    const businessDate = today()
    const records: AdditionalCostRecord[] = []
    let page = 1
    for (;;) {
      const result = await listAccountCostLedger({ from: businessDate, to: businessDate, page, pageSize: 100 })
      if (!isCurrentCostRead(request, generation)) return null
      records.push(...result.items)
      if (!result.hasMore) break
      page += 1
    }
    if (isCurrentCostRead(request, generation)) todayLedger.value = records
    return null
  } catch (error) {
    if (isCurrentCostRead(request, generation)) {
      todayLedger.value = []
      todayLedgerLoadFailed.value = true
      todayLedgerError.value = costFailureText(error, '读取')
      return error
    }
    return null
  }
}
const refreshAfterCostWrite = async (success: string, request: WorkspaceRequest, rules = false, previousResult = '') => {
  if (!isCurrentWorkspaceRequest(request) || !props.open) return
  const generation = ++costReadGeneration.value
  loading.value = false
  costNotice.value = success || '正在重新加载费用，仅更新显示，不会重复保存或删除；不会修改主站。'
  costReloadNeeded.value = false
  const results = await Promise.allSettled(rules ? [loadRules(request, generation)] : [loadLedger(request, generation), loadTodayLedger(request, generation)])
  if (!isCurrentCostRead(request, generation)) return
  const reasons = results.flatMap(result => result.status === 'rejected' ? [result.reason] : result.value ? [result.value] : [])
  if (reasons.length) {
    costReloadNeeded.value = true
    const confirmed = previousResult || success.match(/^(费用已删除|费率已删除|折扣率已保存|折扣率已删除)/)?.[1] || (rules ? '费率已保存' : '费用已保存')
    costNotice.value = `${confirmed}，但${rules ? '规则及历史' : '费用列表'}更新失败。请点击‘重新加载费用’查看结果，不要重复录入；不会修改主站。具体原因：${reasons.map(costErrorReason).join('；')}`
  } else if (!success) costNotice.value = '费用列表已更新；不会修改主站。'
}
const retryCostRead = async () => {
  if (saving.value) return
  const request = currentWorkspaceRequest()
  saving.value = true
  errorText.value = ''
  try {
    const previousResult = costNotice.value.match(/^(费用已保存|费用已删除|费率已保存|费率已删除|折扣率已保存|折扣率已删除)/)?.[1] ?? '费用读取'
    await refreshAfterCostWrite('', request, activeTab.value === 'rules', previousResult)
    if (isCurrentWorkspaceRequest(request)) emit('updated')
  } finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}
const requestClose = () => { if (!saving.value) emit('close') }
const selectTab = (tab: WorkspaceTab) => { if (!saving.value) activeTab.value = tab }
const showUpstreamDetails = () => { if (!saving.value) emit('open-upstream-details') }

const searchAssets = () => { assetPage.value = 1; void loadAssets() }
const readLedgerPage = async (page: number) => {
  if (saving.value) return
  const request = currentWorkspaceRequest()
  const generation = ++costReadGeneration.value
  loading.value = false
  saving.value = true
  errorText.value = ''
  clearCostNotice()
  ledgerPage.value = page
  try { await loadLedger(request, generation) }
  catch (error) { if (isCurrentCostRead(request, generation)) errorText.value = costFailureText(error, '读取') }
  finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}
const searchLedger = () => readLedgerPage(1)
const changeAssetPage = (delta: number) => { assetPage.value = Math.max(1, assetPage.value + delta); void loadAssets() }
const changeLedgerPage = (delta: number) => readLedgerPage(Math.max(1, ledgerPage.value + delta))
const openUpstreamManagement = () => {
	if (saving.value) return
	emit('close')
	void router.push({ name: 'AdminUpstream' })
}

const loadFeeRateHistory = async (request = currentWorkspaceRequest(), generation = costReadGeneration.value) => {
  if (!isCurrentCostRead(request, generation)) return null
	if (isCurrentCostRead(request, generation)) feeRateHistoryLoadFailed.value = false
	try {
		const history = await listRechargeFeeRateHistory()
		if (isCurrentCostRead(request, generation)) feeRateHistory.value = history.items
		return null
	} catch (error) {
		if (isCurrentCostRead(request, generation)) {
      feeRateHistoryLoadFailed.value = true
      return error
    }
    return null
	}
}

const loadDiscountHistory = async (request = currentWorkspaceRequest(), generation = costReadGeneration.value) => {
  if (!isCurrentCostRead(request, generation)) return null
  discountHistoryLoadFailed.value = false
  try {
    const history = await listRevenueDiscountRates()
    if (!isCurrentCostRead(request, generation)) return null
    discountHistory.value = history.items
    // 表单默认填入今天生效的那一条，生效日期取今天。
    const current = history.items.find(item => item.effectiveDate <= today())
    if (current) fillDiscountForm(current)
    else Object.assign(discountForm, { giftRate: '', newUserRate: '', referralRate: '', effectiveDate: tomorrow() })
    return null
  } catch (error) {
    if (isCurrentCostRead(request, generation)) {
      discountHistoryLoadFailed.value = true
      return error
    }
    return null
  }
}

const loadRules = async (request = currentWorkspaceRequest(), generation = costReadGeneration.value) => {
  if (!isCurrentCostRead(request, generation)) return
  try {
    const [result, historyError, discountError] = await Promise.all([
      getRechargeFeeRate(today()),
      loadFeeRateHistory(request, generation),
      loadDiscountHistory(request, generation),
    ])
    if (!isCurrentCostRead(request, generation)) return
    feeForm.rate = String(result.rate * 100)
    feeForm.effectiveDate = result.effectiveDate || today()
    if (historyError) throw historyError
    if (discountError) throw discountError
  } catch (error) {
    if (isCurrentCostRead(request, generation)) throw error
  }
}

const loadTab = async () => {
  if (!props.open) return
  const request = currentWorkspaceRequest()
  const generation = costReadGeneration.value
  const tab = activeTab.value
  loading.value = true
  errorText.value = ''
	try {
    if (tab === 'today') await loadTodayLedger(request, generation)
    else if (tab === 'assets') await loadAssets(request)
    else if (tab === 'ledger') await loadLedger(request, generation)
    else if (tab === 'rules') await loadRules(request, generation)
  } catch (error) {
    if (isCurrentCostRead(request, generation)) errorText.value = costFailureText(error, '读取')
  } finally {
    if (isCurrentCostRead(request, generation)) loading.value = false
  }
}

const reloadActiveTab = async () => {
  if (!props.open) return
  const request = currentWorkspaceRequest()
  const generation = costReadGeneration.value
  const tab = activeTab.value
  try {
    if (tab === 'today') await loadTodayLedger(request, generation)
    else if (tab === 'assets') await loadAssets(request)
    else if (tab === 'ledger') await loadLedger(request, generation)
    else if (tab === 'rules') await loadRules(request, generation)
  } catch (error) {
    if (isCurrentCostRead(request, generation)) errorText.value = costFailureText(error, '读取')
  }
}

watch(() => props.open, (open) => {
	if (!open) {
    costReadGeneration.value += 1
    loading.value = false
    cancelCostEdit()
		selectedDetail.value = null
    costNotice.value = ''
    costReloadNeeded.value = false
		return
	}
	const nextTab = props.initialTab ?? 'today'
	const changed = activeTab.value !== nextTab
	activeTab.value = nextTab
  selectedDetail.value = null
  ledgerFilters.from = today()
  ledgerFilters.to = today()
	if (!changed) void loadTab()
})
watch(activeTab, () => {
  costReadGeneration.value += 1
  costNotice.value = ''
  costReloadNeeded.value = false
  void loadTab()
})

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
  const request = currentWorkspaceRequest()
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
	if (!isCurrentWorkspaceRequest(request)) return
    showBatchForm.value = false
    selectedDetail.value = null
    recentBatch.value = { id: result.batch.id, name: result.batch.batchName, quantity: result.assets.length }
    assetPage.value = 1
    await loadAssets(request)
    emit('updated')
		batchAttempt.value = null
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = error instanceof Error ? error.message : '保存失败'
  } finally {
    if (isCurrentWorkspaceRequest(request)) saving.value = false
  }
}

const openAsset = async (asset: AccountAsset) => {
  const request = currentWorkspaceRequest()
  loading.value = true
  errorText.value = ''
  try {
	const detail = await getAccountAsset(asset.id)
	if (!isCurrentWorkspaceRequest(request)) return
	selectedDetail.value = detail
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
  catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = costFailureText(error, '读取')
  }
  finally { if (isCurrentWorkspaceRequest(request)) loading.value = false }
}

const submitEvent = async () => {
  if (!selectedDetail.value) return
  const request = currentWorkspaceRequest()
  const assetId = selectedDetail.value.asset.id
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
			assetId,
			input,
		})
	    await createAccountEvent(assetId, input, eventAttempt.value.key)
	if (!isCurrentWorkspaceRequest(request)) return
    const refreshed = await getAccountAsset(assetId)
	if (!isCurrentWorkspaceRequest(request)) return
    selectedDetail.value = refreshed
    await loadAssets(request)
    emit('updated')
		eventAttempt.value = null
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = error instanceof Error ? error.message : '保存失败'
  }
  finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}

const submitLink = async () => {
  if (!selectedDetail.value) return
	if (linkForm.manualSameDaySplit && [linkForm.previousQuotaUsed, linkForm.previousRevenue, linkForm.replacementQuotaUsed, linkForm.replacementRevenue].some(value => value === '' || Number(value) < 0)) {
		errorText.value = '当天换号必须填写旧号和新号的当日额度、营收'
		return
	}
  const request = currentWorkspaceRequest()
  const assetId = selectedDetail.value.asset.id
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
			assetId,
			input,
		})
		const refreshed = await replaceAccountLink(assetId, input, linkAttempt.value.key)
	if (!isCurrentWorkspaceRequest(request)) return
		selectedDetail.value = refreshed
	await loadAssets(request)
	emit('updated')
		linkAttempt.value = null
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = error instanceof Error ? error.message : '关联保存失败'
  }
  finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}

const submitStatsMode = async () => {
  if (!selectedDetail.value) return
	const request = currentWorkspaceRequest()
	const assetId = selectedDetail.value.asset.id
	  saving.value = true
	  errorText.value = ''
	  try {
		const input: AccountEventInput = {
			eventType: 'stats_mode_change', effectiveDate: today(), statsMode: eventForm.statsMode as 'automatic' | 'manual',
			note: '统计方式切换',
		}
		statsModeAttempt.value = prepareIdempotentSubmission(statsModeAttempt.value, 'account-stats-mode', {
			assetId,
			input,
		})
		await createAccountEvent(assetId, input, statsModeAttempt.value.key)
	if (!isCurrentWorkspaceRequest(request)) return
	const refreshed = await getAccountAsset(assetId)
	if (!isCurrentWorkspaceRequest(request)) return
	selectedDetail.value = refreshed
	await loadAssets(request)
		statsModeAttempt.value = null
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = error instanceof Error ? error.message : '统计方式保存失败'
  }
  finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}

const costAmountLabel = computed(() => costForm.type === 'promotion' ? '赠送总额' : costForm.type === 'adjustment' ? '调整金额' : '费用总额')
const addBusinessDays = (value: string, days: number) => {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return null
  const parsed = new Date(`${value}T00:00:00Z`)
  if (!Number.isFinite(parsed.getTime()) || parsed.toISOString().slice(0, 10) !== value) return null
  parsed.setUTCDate(parsed.getUTCDate() + days)
  return Number.isFinite(parsed.getTime()) ? parsed.toISOString().slice(0, 10) : null
}
const costPreview = computed(() => {
  if (String(costForm.amount).trim() === '' || !Number.isFinite(Number(costForm.amount))) return null
  const value = Number(costForm.amount)
  if (Math.abs(value) > 1e12 || (costForm.type !== 'adjustment' && value < 0)) return null
  const days = costForm.type === 'adjustment' ? 1 : Number(costForm.days)
  const rate = costForm.type === 'promotion' ? Number(costForm.usageRate) / 100 : 1
  if (!Number.isInteger(days) || days < 1 || (costForm.type === 'promotion' && (String(costForm.usageRate).trim() === '' || !Number.isFinite(rate) || rate < 0 || rate > 1))) return null
  const endDate = addBusinessDays(costForm.businessDate, days - 1)
  if (endDate == null) return null
  const raw = value * rate
  const totalCents = Math.sign(raw) * Math.round(Math.abs(raw) * 100)
  const dailyCents = Math.trunc(totalCents / days)
  return { total: totalCents / 100, daily: dailyCents / 100, last: (totalCents - dailyCents * (days - 1)) / 100, endDate }
})
const submitCost = async () => {
  if (saving.value) return
  costNotice.value = ''
  costReloadNeeded.value = false
  if (!costForm.name.trim() || !costPreview.value) {
    errorText.value = '费用尚未保存：请填写名称、有效金额和日期；分摊天数须为大于零的整数，活动使用率须在0至100%之间。补齐后可以保存，不会修改主站。'
    return
  }
  const request = currentWorkspaceRequest()
  costReadGeneration.value += 1
  loading.value = false
  saving.value = true
  errorText.value = ''
  try {
    const input = {
      type: costForm.type, name: costForm.name.trim(), businessDate: costForm.businessDate,
      amount: Number(costForm.amount), usageRate: costForm.type === 'promotion' ? Number(costForm.usageRate) / 100 : 0,
      days: costForm.type === 'adjustment' ? 0 : Number(costForm.days), note: costForm.note.trim(),
    }
    if (editingCost.value) await updateAdditionalCost(editingCost.value.sourceId, input)
    else await createAdditionalCost(input)
    if (!isCurrentWorkspaceRequest(request)) return
    cancelCostEdit()
    emit('updated')
    await refreshAfterCostWrite('费用已保存，本站统计将更新；不会修改主站。', request)
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = costFailureText(error, '保存')
  } finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}
const submitFee = async () => {
  if (saving.value) return
  costNotice.value = ''
  costReloadNeeded.value = false
  if (!String(feeForm.rate).trim() || !Number.isFinite(Number(feeForm.rate)) || Number(feeForm.rate) < 0 || Number(feeForm.rate) > 100 || !addBusinessDays(feeForm.effectiveDate, 0)) {
    errorText.value = '费率尚未保存：请填写0至100%的费率和有效生效日期，核对后可以保存；不会修改主站。'
    return
  }
  const request = currentWorkspaceRequest()
  costReadGeneration.value += 1
  loading.value = false
  saving.value = true
  errorText.value = ''
  try {
    const effectiveDate = feeForm.effectiveDate
    const ratePercent = Number(feeForm.rate)
    await saveRechargeFeeRate({ effectiveDate, rate: ratePercent / 100 })
    if (!isCurrentWorkspaceRequest(request)) return
    emit('updated')
    await refreshAfterCostWrite(`费率已保存，将从 ${effectiveDate} 起按 ${ratePercent}% 估算手续费；不会修改主站。`, request, true)
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = costFailureText(error, '保存费率')
  } finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}

const percentText = (rate: number) => String(Number((rate * 100).toFixed(4)))
const fillDiscountForm = (rate: RevenueDiscountRate) => {
  discountForm.giftRate = percentText(rate.giftRate)
  discountForm.newUserRate = percentText(rate.newUserRate)
  discountForm.referralRate = percentText(rate.referralRate)
  discountForm.effectiveDate = rate.effectiveDate > today() ? rate.effectiveDate : tomorrow()
}
const discountPartPercents = computed(() => [discountForm.giftRate, discountForm.newUserRate, discountForm.referralRate].map(value => (
  String(value).trim() === '' ? Number.NaN : Number(value)
)))
const discountTotalPercent = computed(() => {
  const parts = discountPartPercents.value
  return parts.every(value => Number.isFinite(value)) ? Number(parts.reduce((sum, value) => sum + value, 0).toFixed(4)) : null
})
const editDiscount = (rate: RevenueDiscountRate) => {
  if (saving.value) return
  clearCostNotice()
  errorText.value = ''
  fillDiscountForm(rate)
  discountEditNotice.value = '已把这组比率填入上方表单；生效日期最早为明天，保存后从该日期起生效。'
}
const submitDiscount = async () => {
  if (saving.value) return
  clearCostNotice()
  const parts = discountPartPercents.value
  const total = discountTotalPercent.value
  if (parts.some(value => !Number.isFinite(value) || value < 0 || value > 100) || total == null || total >= 100) {
    errorText.value = '折扣率尚未保存：每项需在 0 至 100% 之间，合计必须小于 100%，请核对后保存；不会修改主站。'
    return
  }
  if (!addBusinessDays(discountForm.effectiveDate, 0) || discountForm.effectiveDate <= today()) {
    errorText.value = '折扣率尚未保存：生效日期最早为明天（今天和之前的日期不变），请改为明天或以后；不会修改主站。'
    return
  }
  const request = currentWorkspaceRequest()
  costReadGeneration.value += 1
  loading.value = false
  saving.value = true
  errorText.value = ''
  try {
    const effectiveDate = discountForm.effectiveDate
    const [giftRate, newUserRate, referralRate] = parts.map(value => Number((value / 100).toFixed(6))) as [number, number, number]
    await saveRevenueDiscountRate({ effectiveDate, giftRate, newUserRate, referralRate })
    if (!isCurrentWorkspaceRequest(request)) return
    discountEditNotice.value = ''
    emit('updated')
    await refreshAfterCostWrite(`折扣率已保存，从 ${effectiveDate} 起按 ${total}% 扣除营收折扣；之前的日期不受影响，不会修改主站。`, request, true)
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = ruleFailureText(error, '折扣率保存')
  } finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}
const removeDiscount = async (rate: RevenueDiscountRate) => {
  if (saving.value) return
  clearCostNotice()
  errorText.value = ''
  if (!window.confirm(`删除 ${rate.effectiveDate} 起的营收折扣率（合计 ${formatRatePercent(rate.totalRate)}）？删除后从该日期起改用更早的一条，没有则不扣折扣；已经结算的日期不会重算，不会修改主站。`)) return
  const request = currentWorkspaceRequest()
  costReadGeneration.value += 1
  loading.value = false
  saving.value = true
  try {
    await deleteRevenueDiscountRate(rate.id)
    if (!isCurrentWorkspaceRequest(request)) return
    discountEditNotice.value = ''
    emit('updated')
    await refreshAfterCostWrite('折扣率已删除；已结算的日期不重算，不会修改主站。', request, true)
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = ruleFailureText(error, '折扣率删除')
  } finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}
const editFee = (rate: RechargeFeeRate) => {
  if (saving.value) return
  clearCostNotice()
  errorText.value = ''
  feeForm.rate = percentText(rate.rate)
  feeForm.effectiveDate = rate.effectiveDate
}
const removeFee = async (rate: RechargeFeeRate) => {
  if (saving.value) return
  clearCostNotice()
  errorText.value = ''
  if (!window.confirm(`删除 ${rate.effectiveDate} 起的手续费率 ${formatRatePercent(rate.rate)}？删除后从该日期起改用更早的一条，没有则按默认 1.6%；已经结算的日期不会重算，不会修改主站。`)) return
  const request = currentWorkspaceRequest()
  costReadGeneration.value += 1
  loading.value = false
  saving.value = true
  try {
    await deleteRechargeFeeRate(rate.id)
    if (!isCurrentWorkspaceRequest(request)) return
    emit('updated')
    await refreshAfterCostWrite('费率已删除；已结算的日期不重算，不会修改主站。', request, true)
  } catch (error) {
    if (isCurrentWorkspaceRequest(request)) errorText.value = ruleFailureText(error, '费率删除')
  } finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
}

// 今日营收说明：营收 = 主站消费 - 营收折扣。
const revenueDiscountLine = computed(() => {
  const summary = props.summary
  const net = summary?.netRevenue ?? props.todayRevenue
  if (summary?.revenueDiscount == null || net == null) return ''
  const gross = net + summary.revenueDiscount
  const rule = summary.revenueDiscountRule
  const parts = rule
    ? `：充值赠送 ${formatRatePercent(rule.giftRate)}、新用户福利 ${formatRatePercent(rule.newUserRate)}、邀请返利 ${formatRatePercent(rule.referralRate)}`
    : ''
  return `营收 ${formatAmount(net)} = 主站消费 ${formatAmount(gross)} − 营收折扣 ${formatAmount(summary.revenueDiscount)}（合计 ${formatRatePercent(summary.revenueDiscountRate)}${parts}）。营收折扣不计入成本。`
})

const refreshStats = async () => {
	const request = currentWorkspaceRequest()
	saving.value = true
	errorText.value = ''
	statsNotice.value = ''
	try {
		const result = await refreshAccountStats(today())
		if (!isCurrentWorkspaceRequest(request)) return
		if (result.status === 'in_progress') {
			statsNotice.value = '统计仍在进行，请稍后查看'
			return
		}
		emit('updated')
	} catch (error) {
		if (isCurrentWorkspaceRequest(request)) errorText.value = error instanceof Error ? error.message : '刷新失败'
	}
	finally { if (isCurrentWorkspaceRequest(request)) saving.value = false }
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
const centsText = (value?: number | null) => value == null ? '暂无可靠数据' : formatAmount(value / 100)
const ratioText = (value?: number | null) => value == null ? '暂无可靠数据' : `${value.toFixed(2)}x`
const missingFieldText = (field: string) => ({ dailyStats: '完整单号日快照', revenue: '累计营收', quotaUsed: '累计额度', upstreamCost: '叠加上游成本' }[field] ?? field)
const eventTypeText = (eventType: string) => ({ status: '状态变化', restore: '恢复使用', refund: '退款', quota_observation: '额度观察', manual_observation: '手工经营数据', link_change: '关联变化', stats_mode_change: '统计方式变化', metadata_correction: '资料更正' }[eventType] ?? eventType)
const costTypeText = (type: string) => ({ recharge_fee: '手续费', promotion: '活动', fixed: '固定', adjustment: '调整', account_purchase: '买号确认', account_refund: '退款冲减' }[type] ?? type)
const percentageText = (value?: number | null) => value == null ? '暂无可靠数据' : `${value.toFixed(1)}%`
const historyCreatedAtText = (value: string) => (formatLastUsedAt(value) || value).slice(0, 16).replace('T', ' ')
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
      <div class="absolute inset-0 bg-background/60 backdrop-blur-sm" @click="requestClose" />
      <section role="dialog" aria-modal="true" aria-label="成本与买号" class="absolute inset-y-0 right-0 flex w-full max-w-6xl flex-col border-l border-border/60 bg-card shadow-2xl">
        <header class="flex shrink-0 items-center justify-between border-b border-border/60 px-4 py-3 sm:px-6">
          <div><h2 class="text-base font-semibold text-foreground">成本与买号</h2><p class="mt-0.5 text-xs text-muted-foreground">{{ businessDate }} · 经营成本、账号资产与历史账本</p></div>
          <button type="button" class="rounded-md p-2 text-muted-foreground hover:bg-surface-elevated hover:text-foreground" title="关闭" :disabled="saving" @click="requestClose"><X class="h-5 w-5" /></button>
        </header>

        <nav class="flex shrink-0 gap-1 overflow-x-auto border-b border-border/60 px-4 py-2 sm:px-6">
          <button v-for="tab in tabs" :key="tab.key" type="button" class="h-9 shrink-0 rounded-md px-3 text-sm font-medium transition-colors" :class="activeTab === tab.key ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-surface-elevated hover:text-foreground'"  :disabled="saving" @click="selectTab(tab.key)">{{ tab.label }}</button>
        </nav>

        <main class="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
          <p v-if="errorText" class="mb-4 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">{{ errorText }}</p>
          <div v-if="costNotice" role="status" class="mb-4 space-y-2 rounded-md border border-border/60 bg-surface-elevated px-3 py-2 text-sm text-muted-foreground">
            <p>{{ costNotice }}</p><Button v-if="costReloadNeeded" type="button" size="sm" variant="secondary" :disabled="saving" @click="retryCostRead">重新加载费用</Button>
          </div>
          <p v-if="statsNotice" role="status" class="mb-4 rounded-md border border-warning/30 bg-warning/5 px-3 py-2 text-sm text-muted-foreground">{{ statsNotice }}</p>
          <div v-if="loading" class="flex justify-center py-16"><Loader2 class="h-6 w-6 animate-spin text-muted-foreground" /></div>

          <section v-else-if="activeTab === 'today'" class="space-y-6">
 <p class="text-xs text-muted-foreground">费用按新加坡日期统计，使用最近一次同步结果；暂缺的费用不会计作零。</p>
            <p class="text-xs text-muted-foreground">这里记录和管理本站的额外费用，会影响成本和利润统计，不会修改主站。</p>
            <div class="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"><p>编辑、删除会处理整笔费用；历史费用请到成本账本按日期查询。</p><Button type="button" size="sm" variant="secondary" :disabled="saving" @click="selectTab('ledger')">查看历史费用</Button></div>
 <p v-if="summary?.accountQuality && summary.accountQuality !== 'complete'" class="text-xs text-warning">这份账号费用尚未按新加坡日期重新核对，请点击‘刷新账号统计’。旧历史记录保留；此操作不会修改主站。</p>
 <p v-if="summary?.unavailableReason" class="text-xs text-warning">{{ summary.unavailableReason }}</p>
            <div class="flex flex-wrap justify-end gap-2"><Button variant="secondary" :disabled="saving" @click="showUpstreamDetails"><ExternalLink class="mr-2 h-4 w-4" />查看上游成本明细</Button><Button variant="secondary" :disabled="saving" @click="refreshStats"><RefreshCw class="mr-2 h-4 w-4" :class="saving ? 'animate-spin' : ''" />刷新账号统计</Button></div>
            <div class="grid gap-3 sm:grid-cols-3">
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">今日营收</p>{{ ' ' }}<p class="mt-1 text-xl font-bold tabular-nums">{{ formatAmount(todayRevenue ?? null) }}</p></div>
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">今日总成本</p>{{ ' ' }}<p class="mt-1 text-xl font-bold tabular-nums">{{ formatAmount(operatingCost) }}</p></div>
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">今日净利润</p>{{ ' ' }}<p class="mt-1 text-xl font-bold tabular-nums">{{ formatAmount(adjustedNetProfit) }}</p></div>
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">今日利润率</p>{{ ' ' }}<p class="mt-1 text-xl font-bold tabular-nums">{{ percentageText(profitMargin) }}</p></div>
              <div class="rounded-md border border-border/60 p-4"><p class="text-xs text-muted-foreground">账号统计质量</p><p class="mt-1 text-sm font-semibold">{{ summary?.accountQuality === 'complete' ? '完整' : summary?.accountQuality ? '部分数据' : '暂无可靠数据' }}</p></div>
            </div>
            <p v-if="revenueDiscountLine" class="text-sm text-muted-foreground">{{ revenueDiscountLine }}</p>
            <p v-else-if="summary?.available" class="text-sm text-muted-foreground">今天没有生效的营收折扣率，营收按主站消费原值计算；可在‘成本规则’设置，最早明天起生效。</p>
            <div class="space-y-1 text-sm text-muted-foreground"><p>营收 = 主站消费 − 营收折扣</p><p>净利润 = 营收 - 总成本</p><p>利润率 = 净利润 ÷ 营收</p></div>
            <div class="grid gap-x-8 sm:grid-cols-2">
              <div v-for="line in costLines" :key="line.label" class="flex items-center justify-between border-b border-border/50 py-2.5 text-sm"><span class="text-muted-foreground">{{ line.label }}</span><span class="font-medium tabular-nums">{{ formatAmount(line.value) }}</span></div>
            </div>
            <div class="border-t border-border/60 pt-5">
              <h3 class="text-sm font-semibold">当日成本变动</h3>
              <div v-if="todayLedgerLoadFailed" class="space-y-2 py-5 text-sm text-destructive"><p>当日成本变动加载失败</p><p>{{ todayLedgerError }}</p><Button type="button" variant="secondary" size="sm" :disabled="saving" @click="retryCostRead">重新加载费用</Button></div>
              <p v-else-if="!todayLedger.length" class="py-5 text-sm text-muted-foreground">当日暂无成本变动</p>
              <div v-else class="mt-2 divide-y divide-border/50">
                <div v-for="record in todayLedger" :key="record.id" class="grid gap-2 py-2.5 text-sm sm:grid-cols-[5rem_1fr_auto]">
                  <span class="text-muted-foreground">{{ costTypeText(record.type) }}</span>
                  <div class="min-w-0 break-all">{{ record.name }}<p v-if="record.note" class="text-xs text-warning">{{ record.note }}</p><p class="text-xs text-muted-foreground">来源 {{ record.sourceId || record.batchId || record.accountAssetId || record.id }}</p>
                    <div v-if="isManualCost(record)" class="mt-2 flex flex-wrap gap-2"><Button type="button" size="sm" variant="secondary" :disabled="saving" @click="beginCostEdit({ records: [record] })">编辑 {{ record.name }}</Button><Button type="button" size="sm" variant="secondary" :disabled="saving" @click="removeCost({ records: [record] })">删除 {{ record.name }}</Button></div>
                  </div><span class="font-medium tabular-nums">{{ formatAmount(record.amount) }}</span>
                </div>
              </div>
            </div>
            <form class="space-y-3 border-t border-border/60 pt-5" @input="clearCostNotice" @change="clearCostNotice" @submit.prevent="submitCost">
              <div class="flex flex-wrap items-center justify-between gap-2"><div><h3 class="text-sm font-semibold">{{ editingCost ? '编辑手工成本' : '记一笔成本' }}</h3><p v-if="editingCost" class="mt-1 text-xs text-muted-foreground">正在修改整笔费用；保存后会重新计算原日期和新日期的成本与利润，不会修改主站。</p></div><div class="flex gap-2"><Button v-if="editingCost" type="button" variant="ghost" :disabled="saving" @click="cancelCostEdit">取消编辑</Button><Button v-else type="button" variant="secondary" :disabled="saving" @click="selectTab('assets'); showBatchForm = true"><Plus class="mr-2 h-4 w-4" />录入买号</Button></div></div>
              <p v-if="legacyCostNotice" class="text-xs text-warning">{{ legacyCostNotice }}</p>
              <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                <label class="space-y-1 text-xs text-muted-foreground"><span>费用类型</span><select v-model="costForm.type" :disabled="saving" class="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"><option v-if="editingCost?.type === 'promotion'" value="promotion">活动赠送</option><option value="fixed">固定费用</option><option value="adjustment">手工调整</option></select></label>
                <label class="space-y-1 text-xs text-muted-foreground"><span>名称</span><Input v-model="costForm.name" :disabled="saving" placeholder="名称" required /></label>
                <label class="space-y-1 text-xs text-muted-foreground"><span>{{ costAmountLabel }}</span><Input v-model="costForm.amount" :disabled="saving" type="number" step="0.01" placeholder="金额" required /></label>
                <label class="space-y-1 text-xs text-muted-foreground"><span>{{ costForm.type === 'adjustment' ? '计入日期' : '开始日期' }}</span><Input v-model="costForm.businessDate" :disabled="saving" type="date" required /></label>
                <label v-if="costForm.type === 'promotion'" class="space-y-1 text-xs text-muted-foreground"><span>预计使用率 %</span><Input v-model="costForm.usageRate" :disabled="saving" type="number" min="0" max="100" step="0.01" placeholder="预计使用率 %" required /></label>
                <label v-if="costForm.type !== 'adjustment'" class="space-y-1 text-xs text-muted-foreground"><span>分摊天数</span><Input v-model="costForm.days" :disabled="saving" type="number" min="1" step="1" placeholder="分摊天数" required /></label>
                <label class="space-y-1 text-xs text-muted-foreground sm:col-span-2"><span>说明（可选）</span><Input v-model="costForm.note" :disabled="saving" placeholder="说明（可选）" /></label>
              </div>
              <p v-if="costForm.type === 'fixed'" class="text-xs text-muted-foreground">填写这段期间的费用总额，系统按天分摊；不会自动每月续记。只计一天时填 1 天。</p>
              <template v-else-if="costForm.type === 'promotion'"><p class="text-xs text-warning">活动赠送已停止新增；充值赠送请在‘成本规则’按营收折扣率扣除。这里只能修改或删除旧记录，如与折扣率重复请删除。</p><p class="text-xs text-muted-foreground">这是赠送金额按预计使用率计算的额外成本估算，不跟随实际用量自动修正；如果相关支出已计入上游成本，请先核查，避免重复计入。</p></template>
              <p v-else class="text-xs text-muted-foreground">只调整所选日期：正数增加成本，负数减少成本；不会修改主站。</p>
              <div v-if="costPreview" class="flex flex-wrap gap-x-6 gap-y-1 rounded-md bg-surface-elevated px-3 py-2 text-xs text-muted-foreground">
                <template v-if="costForm.type === 'adjustment'"><span>计入日期 {{ costForm.businessDate }}</span><span>{{ costPreview.total < 0 ? '本次减少成本' : '本次增加成本' }} {{ formatAmount(Math.abs(costPreview.total)) }}</span></template>
                <template v-else><span>预计计入总额 {{ formatAmount(costPreview.total) }}</span><span>分摊期间 {{ costForm.businessDate }} 至 {{ costPreview.endDate }}</span><span>每天计入 {{ formatAmount(costPreview.daily) }}</span><span>最后一天计入 {{ formatAmount(costPreview.last) }}</span></template>
              </div>
              <p v-else class="text-xs text-muted-foreground">填写有效金额、日期及分摊参数后，可在这里查看计入金额；核对后再保存，不会修改主站。</p>
              <Button :disabled="saving" type="submit"><Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" /><Save v-else class="mr-2 h-4 w-4" />保存记录</Button>
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
                    <Input v-model="linkForm.previousRevenue" type="number" min="0" step="0.01" placeholder="旧号营收" required />
                    <Input v-model="linkForm.replacementQuotaUsed" type="number" min="0" step="0.000001" placeholder="新号额度" required />
                    <Input v-model="linkForm.replacementRevenue" type="number" min="0" step="0.01" placeholder="新号营收" required />
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
                    <Input v-if="eventForm.eventType === 'manual_observation'" v-model="eventForm.revenue" type="number" min="0" step="0.01" placeholder="累计营收" />
                    <Input v-if="eventForm.eventType === 'manual_observation'" v-model="eventForm.upstreamCost" type="number" min="0" step="0.01" placeholder="累计上游成本" />
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
                  <p v-if="stat.source === 'automatic' && stat.businessDate === today() && stat.quality !== 'complete'" class="col-span-full text-xs text-warning">这份账号费用尚未按新加坡日期重新核对，请点击‘刷新账号统计’。旧历史记录保留；此操作不会修改主站。</p><span>{{ stat.businessDate }}</span><span>{{ stat.quality === 'complete' ? '完整' : '部分数据' }} · {{ stat.source === 'automatic' ? '自动' : '手工' }}</span>
                  <span>额度 {{ stat.rawQuotaUsedMicros == null ? '—' : (stat.rawQuotaUsedMicros / 1_000_000).toFixed(2) }}</span><span>营收 {{ centsText(stat.revenueCents) }}</span>
                  <span>上游 {{ centsText(stat.upstreamCostCents) }}</span><span>确认成本 {{ centsText(stat.recognizedCostCents) }}</span><span>替代扣减 {{ centsText(stat.replacementDeductionCents) }}</span>
                </div>
              </div>
              <div>
                <h4 class="mb-2 text-sm font-semibold">完整事件时间线</h4>
                <div v-if="!selectedDetail.events.length" class="text-sm text-muted-foreground">暂无事件</div>
				<div v-for="event in selectedDetail.events" :key="event.id" class="flex items-start justify-between gap-3 border-b border-border/50 py-2 text-sm"><span class="min-w-0 break-all">{{ event.effectiveDate }} · {{ eventSummary(event) }}</span><span class="shrink-0 text-xs text-muted-foreground">{{ historyCreatedAtText(event.createdAt) }}</span></div>
              </div>
            </template>
            <template v-else>
              <div class="flex flex-wrap items-center justify-between gap-3"><div class="flex flex-1 flex-wrap gap-2"><Input v-model="assetFilters.search" class="max-w-52" placeholder="搜索账号标识" /><Input v-model="assetFilters.platform" class="max-w-40" placeholder="平台" /><Input v-model="assetFilters.channel" class="max-w-40" placeholder="渠道" /><Input v-model="assetFilters.accountType" class="max-w-40" placeholder="账号类型" /><select v-model="assetFilters.status" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="">全部状态</option><option value="unactivated">未激活</option><option value="active">使用中</option><option value="exhausted">已耗尽</option><option value="dead">死号</option><option value="closed">已关闭</option></select><Button variant="secondary" title="查询" @click="searchAssets"><Search class="h-4 w-4" /></Button></div><Button @click="showBatchForm = !showBatchForm"><Plus class="mr-2 h-4 w-4" />录入买号</Button></div>
              <form v-if="showBatchForm" class="space-y-4 border-y border-border/60 py-4" @submit.prevent="submitBatch">
                <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  <Input v-model="batchForm.batchName" placeholder="批次名称（可选）" /><Input v-model="batchForm.platform" list="account-platforms" placeholder="平台" required /><Input v-model="batchForm.channel" list="account-channels" placeholder="购买渠道" required /><Input v-model="batchForm.accountType" list="account-types" placeholder="账号类型" required />
				  <label class="space-y-1 text-xs text-muted-foreground"><span>购买日期</span><Input v-model="batchForm.purchaseDate" type="date" aria-label="购买日期" /></label><Input v-model="batchForm.quantity" type="number" min="1" max="500" placeholder="数量" /><Input v-model="batchForm.unitAmount" type="number" min="0" step="0.01" placeholder="单价（二选一）" /><Input v-model="batchForm.totalAmount" type="number" min="0" step="0.01" placeholder="总金额（二选一）" />
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
              <p v-if="assets.some(asset => asset.statsQuality === 'partial')" class="text-xs text-warning">累计只包含已确认记录，今天尚未确认，请刷新账号统计</p><div class="hidden overflow-x-auto md:block"><table class="w-full min-w-[980px] text-left text-sm"><thead class="border-b border-border/60 text-xs text-muted-foreground"><tr><th class="py-2">账号 / 平台</th><th>渠道</th><th>状态</th><th>额度进度</th><th>实际平均倍率</th><th>回本 / 最终盈亏</th><th>上游</th><th></th></tr></thead><tbody><tr v-for="asset in assets" :key="asset.id" class="border-b border-border/40" :class="asset.batchId === recentBatch?.id ? 'bg-primary/5' : ''"><td class="py-3"><p class="font-medium">{{ asset.identifier }}</p><p class="text-xs text-muted-foreground">{{ asset.platform }} · {{ asset.accountType }}</p></td><td>{{ asset.channel }}</td><td>{{ statusText(asset.currentStatus) }}</td><td>{{ quotaText(asset) }}</td><td>{{ ratioText(asset.performance?.averageSaleMultiplier) }}</td><td>{{ outcomeText(asset) }}</td><td><a v-if="asset.upstreamReferenceUrl" :href="asset.upstreamReferenceUrl" target="_blank" rel="noopener noreferrer" title="打开上游参考链接" class="inline-flex rounded-md p-2 text-primary hover:bg-surface-elevated"><ExternalLink class="h-4 w-4" /></a><span v-else class="text-xs text-muted-foreground">{{ asset.hasActiveLink ? '已关联' : '未关联' }}</span></td><td class="text-right"><Button variant="secondary" @click="openAsset(asset)">查看 / 操作</Button></td></tr></tbody></table></div><div class="divide-y divide-border/50 md:hidden"><button v-for="asset in assets" :key="asset.id" type="button" class="block w-full py-3 text-left" :class="asset.batchId === recentBatch?.id ? 'bg-primary/5' : ''" @click="openAsset(asset)"><div class="flex items-start justify-between gap-3"><div class="min-w-0"><p class="truncate text-sm font-medium">{{ asset.identifier }}</p><p class="truncate text-xs text-muted-foreground">{{ asset.platform }} · {{ asset.channel }} · {{ asset.accountType }}</p></div><span class="shrink-0 text-xs font-medium">{{ statusText(asset.currentStatus) }}</span></div><div class="mt-2 grid grid-cols-2 gap-2 text-xs"><span>额度 {{ quotaText(asset) }}</span><span class="text-right">{{ outcomeText(asset) }}</span></div></button></div><p v-if="!assets.length" class="py-12 text-center text-sm text-muted-foreground">暂无买号资产</p><div v-if="assets.length || assetPage > 1" class="flex items-center justify-end gap-2 pt-3"><Button variant="secondary" size="sm" title="上一页" :disabled="assetPage === 1" @click="changeAssetPage(-1)"><ArrowLeft class="h-4 w-4" /></Button><span class="text-xs text-muted-foreground">第 {{ assetPage }} 页</span><Button variant="secondary" size="sm" title="下一页" :disabled="!assetHasMore" @click="changeAssetPage(1)"><ArrowRight class="h-4 w-4" /></Button></div>
            </template>
          </section>

          <section v-else-if="activeTab === 'ledger'" class="space-y-4">
            <p class="text-xs text-muted-foreground">下面只显示查询日期内的费用。整笔金额与这段日期计入金额可能不同；修改、删除会影响整笔费用，不会修改主站。</p>
            <p class="text-xs text-muted-foreground">当前查询日期：{{ ledgerFilters.from || '不限开始日期' }} 至 {{ ledgerFilters.to || '不限结束日期' }}。查看历史费用时修改日期后点击“查询账本”。</p>
            <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-4"><Input v-model="ledgerFilters.from" :disabled="saving" type="date" aria-label="查询开始日期" /><Input v-model="ledgerFilters.to" :disabled="saving" type="date" aria-label="查询结束日期" /><select v-model="ledgerFilters.type" :disabled="saving" class="h-9 rounded-md border border-input bg-background px-3 text-sm"><option value="">全部类型</option><option value="account_purchase">买号确认</option><option value="account_refund">退款冲减</option><option value="recharge_fee">手续费</option><option value="promotion">活动</option><option value="fixed">固定</option><option value="adjustment">调整</option></select><Input v-model="ledgerFilters.platform" :disabled="saving" placeholder="平台" /><Input v-model="ledgerFilters.channel" :disabled="saving" placeholder="渠道" /><Input v-model="ledgerFilters.batchId" :disabled="saving" placeholder="批次 ID" /><Input v-model="ledgerFilters.accountAssetId" :disabled="saving" placeholder="单号 ID" /><Button variant="secondary" :disabled="saving" @click="searchLedger"><Search class="mr-2 h-4 w-4" />查询账本</Button></div>
            <div class="divide-y divide-border/50">
              <div v-for="group in ledgerGroups" :key="group.key" class="py-3">
                <div v-if="isManualCost(group.records[0]!)" class="mb-2 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground"><span>原始输入金额 {{ originalAmountText(group.records) }}</span><span>预计计入总额 {{ expectedAmount(group.records) == null ? '未记录' : formatAmount(expectedAmount(group.records)) }}</span><Button size="sm" variant="secondary" type="button" :disabled="saving" @click="beginCostEdit(group)">编辑 {{ group.name }}</Button><Button size="sm" variant="secondary" type="button" :disabled="saving" @click="removeCost(group)">删除 {{ group.name }}</Button></div>
                <details class="group"><summary class="grid cursor-pointer list-none grid-cols-[1fr_auto] items-center gap-3 text-sm sm:grid-cols-[1fr_9rem_10rem]"><div class="min-w-0"><p class="truncate font-medium">{{ group.name }}</p><p class="text-xs text-muted-foreground">{{ costTypeText(group.type) }} · {{ group.records.length }} 条确认记录</p></div><span class="hidden text-xs text-muted-foreground sm:block">{{ group.records[0]?.businessDate }}</span><span class="text-right font-semibold tabular-nums" :class="group.amount < 0 ? 'text-emerald-600' : ''"><span class="block text-xs font-normal text-muted-foreground">所选日期计入</span>{{ formatAmount(group.amount) }}</span></summary>
                  <div class="mt-3 overflow-x-auto border-t border-border/40"><table class="w-full min-w-[680px] text-left text-xs"><thead class="text-muted-foreground"><tr><th class="py-2">业务日</th><th>金额</th><th>批次 / 单号</th><th>质量</th><th>录入时间</th></tr></thead><tbody><tr v-for="record in group.records" :key="record.id" class="border-t border-border/30"><td class="py-2">{{ record.businessDate }}</td><td>{{ formatAmount(record.amount) }}</td><td>{{ record.batchId || '—' }} / {{ record.accountAssetId || '—' }}</td><td>{{ record.note?.includes('尚未按新加坡日期重新确认') ? '待重新确认' : record.estimated ? '估算' : '已确认' }}<p v-if="record.note" class="text-warning">{{ record.note }}</p></td><td>{{ historyCreatedAtText(record.createdAt) }}</td></tr></tbody></table></div>
                </details>
              </div>
              <p v-if="!ledgerGroups.length" class="py-12 text-center text-sm text-muted-foreground">所选范围暂无记录</p>
            </div>
            <div v-if="ledger.length || ledgerPage > 1" class="flex items-center justify-end gap-2"><Button variant="secondary" size="sm" title="上一页" :disabled="saving || ledgerPage === 1" @click="changeLedgerPage(-1)"><ArrowLeft class="h-4 w-4" /></Button><span class="text-xs text-muted-foreground">第 {{ ledgerPage }} 页</span><Button variant="secondary" size="sm" title="下一页" :disabled="saving || !ledgerHasMore" @click="changeLedgerPage(1)"><ArrowRight class="h-4 w-4" /></Button></div>
          </section>

          <section v-else class="max-w-3xl space-y-6"><div><h3 class="text-sm font-semibold">营收折扣率（从营收中扣除）</h3><p class="mt-2 text-xs text-muted-foreground">用户花掉的充值赠送、新用户福利、邀请返利不是实际收到的钱。这里按比例从主站消费中扣除，得到实收营收；营收折扣不计入成本，不会修改主站。</p><div class="mt-3 grid gap-3 sm:grid-cols-4" @input="clearCostNotice" @change="clearCostNotice"><label class="space-y-1 text-xs text-muted-foreground"><span>充值赠送 %</span><Input v-model="discountForm.giftRate" :disabled="saving" aria-label="充值赠送 %" type="number" min="0" max="100" step="0.01" placeholder="充值赠送 %" /></label><label class="space-y-1 text-xs text-muted-foreground"><span>新用户福利 %</span><Input v-model="discountForm.newUserRate" :disabled="saving" aria-label="新用户福利 %" type="number" min="0" max="100" step="0.01" placeholder="新用户福利 %" /></label><label class="space-y-1 text-xs text-muted-foreground"><span>邀请返利 %</span><Input v-model="discountForm.referralRate" :disabled="saving" aria-label="邀请返利 %" type="number" min="0" max="100" step="0.01" placeholder="邀请返利 %" /></label><label class="space-y-1 text-xs text-muted-foreground"><span>生效日期</span><Input v-model="discountForm.effectiveDate" :disabled="saving" aria-label="折扣率生效日期" type="date" :min="tomorrow()" /></label></div><div class="mt-3 flex flex-wrap items-center gap-3"><p class="text-sm">合计折扣率 <span class="text-lg font-bold tabular-nums">{{ discountTotalPercent == null ? '—' : `${discountTotalPercent}%` }}</span></p><Button :disabled="saving" @click="submitDiscount"><Save class="mr-2 h-4 w-4" />保存折扣率</Button></div><p class="mt-2 text-xs text-muted-foreground">每项都填占主站消费的比例，不是活动面额：例如邀请返利是 10% × 被邀请用户充值占主站消费的比例。生效日期最早为明天：新规则从所选日期起生效，之前的日期不受影响；只有还没生效的折扣率可以删除；已经结算的日期不会重算。</p><p v-if="discountEditNotice" role="status" class="mt-2 text-xs text-warning">{{ discountEditNotice }}</p></div><div class="border-t border-border/60 pt-5"><h3 class="text-sm font-semibold">折扣率生效历史</h3><p v-if="discountHistoryLoadFailed" class="py-5 text-sm text-destructive">折扣率历史加载失败</p><p v-else-if="!discountHistory.length" class="py-5 text-sm text-muted-foreground">还没有设置折扣率，营收按主站消费原值计算。</p><div v-else class="mt-2 divide-y divide-border/50"><div v-for="rate in discountHistory" :key="rate.id" class="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm"><span>{{ rate.effectiveDate }} 起 · 合计 <strong class="tabular-nums">{{ formatRatePercent(rate.totalRate) }}</strong> · 充值赠送 {{ formatRatePercent(rate.giftRate) }} · 新用户福利 {{ formatRatePercent(rate.newUserRate) }} · 邀请返利 {{ formatRatePercent(rate.referralRate) }}</span><span class="flex gap-2"><Button type="button" size="sm" variant="secondary" :disabled="saving" :aria-label="`编辑 ${rate.effectiveDate} 起的折扣率`" @click="editDiscount(rate)">编辑</Button><Button v-if="rate.effectiveDate > today()" type="button" size="sm" variant="secondary" :disabled="saving" :aria-label="`删除 ${rate.effectiveDate} 起的折扣率`" @click="removeDiscount(rate)">删除</Button></span><span v-if="rate.effectiveDate <= today()" class="w-full text-xs text-muted-foreground">已经生效，不能删除；要停用，请新增一条从明天起生效的折扣率（可都填 0）。</span></div></div></div><div class="border-t border-border/60 pt-5"><h3 class="text-sm font-semibold">充值手续费（按营收估算）</h3><p class="mt-2 text-xs text-muted-foreground">按当天实收营收（主站消费扣除营收折扣后）× 费率估算手续费，默认 1.6%；不是支付平台实际账单。新费率从所选日期生效，不自动重算已保存的历史手续费，不会修改主站。</p><div class="mt-3 grid gap-3 sm:grid-cols-[1fr_1fr_auto]" @input="clearCostNotice" @change="clearCostNotice"><Input v-model="feeForm.rate" :disabled="saving" aria-label="费率 %" type="number" min="0" max="100" step="0.01" placeholder="费率 %" /><Input v-model="feeForm.effectiveDate" :disabled="saving" aria-label="费率生效日期" type="date" /><Button :disabled="saving" @click="submitFee"><Save class="mr-2 h-4 w-4" />保存费率</Button></div><p class="mt-2 text-xs text-muted-foreground">编辑会把该条填入上面的表单，保存后覆盖同一生效日期；要改生效日期，请删除旧的再新增。</p></div><div class="border-t border-border/60 pt-5"><h3 class="text-sm font-semibold">费率生效历史</h3><p v-if="feeRateHistoryLoadFailed" class="py-5 text-sm text-destructive">费率历史加载失败</p><p v-else-if="!feeRateHistory.length" class="py-5 text-sm text-muted-foreground">暂无费率历史</p><div v-else class="mt-2 divide-y divide-border/50"><div v-for="rate in feeRateHistory" :key="rate.id" class="grid items-center gap-1 py-2.5 text-sm sm:grid-cols-[7rem_1fr_auto_auto]"><span>{{ rate.effectiveDate }}</span><span>{{ (rate.rate * 100).toFixed(2) }}% · {{ rate.id }}</span><span class="text-xs text-muted-foreground">{{ historyCreatedAtText(rate.createdAt) }}</span><span class="flex gap-2"><Button type="button" size="sm" variant="secondary" :disabled="saving" :aria-label="`编辑 ${rate.effectiveDate} 起的手续费率`" @click="editFee(rate)">编辑</Button><Button type="button" size="sm" variant="secondary" :disabled="saving" :aria-label="`删除 ${rate.effectiveDate} 起的手续费率`" @click="removeFee(rate)">删除</Button></span></div></div></div><div class="border-t border-border/60 pt-5"><h3 class="text-sm font-semibold">核算说明</h3><dl class="mt-3 space-y-3 text-sm"><div><dt class="font-medium">替代上游成本</dt><dd class="mt-1 text-muted-foreground">购买价包含额度时，从上游直接成本扣除该账号已核对的 Key 成本，再计入买号确认成本。</dd></div><div><dt class="font-medium">叠加上游成本</dt><dd class="mt-1 text-muted-foreground">购买价是订阅或接入费时，买号确认成本与关联上游按量成本同时计入。</dd></div><div><dt class="font-medium">历史成本更正</dt><dd class="mt-1 text-muted-foreground">手工成本可以编辑或删除整笔费用，操作后会重新计算受影响日期的成本和利润，不会修改主站；买号购买、退款和状态请到买号资产处理。</dd></div></dl></div></section>
        </main>
      </section>
    </div>
  </Teleport>
</template>
