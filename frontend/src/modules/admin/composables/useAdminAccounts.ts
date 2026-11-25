import { ref, computed } from 'vue'
import { getAccessToken } from '@/modules/auth/api/auth'
import { useRouter } from 'vue-router'
import type { AdminAccount } from '../types/adminAccounts'
import {
  listAdminAccounts,
  getCurrentAdminAccount,
  switchAdminAccount,
  updateAdminAccount,
  deleteAdminAccount,
} from '../api/adminAccounts'
import { markWorkspaceActive, resetWorkspaceCheck } from '@/lib/workspaceGuard'
import type { DeleteAdminAccountResponse, WorkspaceDeleteConfirmation } from '../types/adminAccounts'

const accounts = ref<AdminAccount[]>([])
const currentAccount = ref<AdminAccount | null>(null)
const currentAccountSession = ref<string | null>(null)
const readSession = (): string | null => { try { return getAccessToken() } catch { return null } }
const assignCurrentAccount = (account: AdminAccount | null, sourceSession: string | null) => {
  currentAccountSession.value = null
  currentAccount.value = account
  currentAccountSession.value = account ? sourceSession : null
}
const isCurrentAccountSession = (workspaceId: string): boolean => {
  const token = readSession()
  return Boolean(token && currentAccountSession.value === token && currentAccount.value?.id === workspaceId)
}
const isLoading = ref(false)
const isSwitching = ref(false)
const isDeleting = ref(false)
const errorKey = ref('')
const noticeKey = ref('')

const adminAccountsErrorPrefix = 'admin.adminAccounts.errors.'

const toAdminAccountsErrorKey = (err: unknown, fallback: string): string => {
  if (!(err instanceof Error)) return fallback
  if (err.message.startsWith(adminAccountsErrorPrefix) || err.message === 'auth.errors.unauthorized') {
    return err.message
  }
  return fallback
}

export function useAdminAccounts() {
  const router = useRouter()

  const hasAccounts = computed(() => accounts.value.length > 0)
  const hasCurrentAccount = computed(() => currentAccount.value !== null)

  const loadAccounts = async () => {
    isLoading.value = true
    errorKey.value = ''
    try {
      accounts.value = await listAdminAccounts()
    } catch (err) {
      errorKey.value = err instanceof Error ? err.message : 'admin.adminAccounts.errors.request'
    } finally {
      isLoading.value = false
    }
  }

  const loadCurrentAccount = async (): Promise<boolean> => {
    const sourceSession = readSession()
    try {
      assignCurrentAccount(await getCurrentAdminAccount(), sourceSession)
      return true
    } catch {
      assignCurrentAccount(null, null)
      return false
    }
  }

  const switchAccount = async (id: string) => {
    const sourceSession = readSession()
    isSwitching.value = true
    errorKey.value = ''
    try {
      assignCurrentAccount(await switchAdminAccount(id), sourceSession)
      accounts.value = accounts.value.map(a => ({
        ...a,
        current: a.id === id,
      }))
      markWorkspaceActive()
      await router.push('/admin')
    } catch (err) {
      // 切换失败时重置 workspace 缓存，下次导航会重新验证后端状态，
      // 防止前端缓存一个实际已失效的 workspace。
      resetWorkspaceCheck()
      errorKey.value = err instanceof Error ? err.message : 'admin.adminAccounts.errors.request'
    } finally {
      isSwitching.value = false
    }
  }

  const renameAccount = async (id: string, displayName: string) => {
    const sourceSession = readSession()
    errorKey.value = ''
    try {
      const updated = await updateAdminAccount(id, displayName)
      accounts.value = accounts.value.map(a => a.id === id ? updated : a)
      if (currentAccount.value?.id === id) {
        assignCurrentAccount(updated, sourceSession)
      }
    } catch (err) {
      errorKey.value = err instanceof Error ? err.message : 'admin.adminAccounts.errors.request'
    }
  }

  const deleteAccount = async (
    id: string,
    confirmation: WorkspaceDeleteConfirmation,
  ): Promise<DeleteAdminAccountResponse> => {
    const sourceSession = readSession()
    isDeleting.value = true
    errorKey.value = ''
    noticeKey.value = ''
    const deletedCurrent = currentAccount.value?.id === id || accounts.value.some(account => account.id === id && account.current)

    try {
      const response = await deleteAdminAccount(id, confirmation)
      const remainingAccounts = accounts.value.filter(account => account.id !== response.deletedId)

      if (response.hasCurrent && response.currentAdminAccountId) {
        accounts.value = remainingAccounts.map(account => ({
          ...account,
          current: account.id === response.currentAdminAccountId,
        }))
        assignCurrentAccount(accounts.value.find(account => account.id === response.currentAdminAccountId) ?? null, sourceSession)
        resetWorkspaceCheck()
        markWorkspaceActive()
        if (deletedCurrent) {
          await router.push('/admin')
        }
      } else {
        accounts.value = remainingAccounts.map(account => ({
          ...account,
          current: false,
        }))
        assignCurrentAccount(null, null)
        resetWorkspaceCheck()
        if (router.currentRoute.value.name !== 'AdminAccounts') {
          await router.push('/admin/accounts')
        }
      }

      if (response.cleanupPending) {
        noticeKey.value = 'admin.adminAccounts.delete.cleanupPending'
      }

      return response
    } catch (err) {
      const nextErrorKey = toAdminAccountsErrorKey(err, 'admin.adminAccounts.errors.deleteFailed')
      errorKey.value = nextErrorKey
      throw new Error(nextErrorKey)
    } finally {
      isDeleting.value = false
    }
  }

  return {
    accounts,
    currentAccount,
    isCurrentAccountSession,
    isLoading,
    isSwitching,
    isDeleting,
    errorKey,
    noticeKey,
    hasAccounts,
    hasCurrentAccount,
    loadAccounts,
    loadCurrentAccount,
    switchAccount,
    renameAccount,
    deleteAccount,
  }
}
