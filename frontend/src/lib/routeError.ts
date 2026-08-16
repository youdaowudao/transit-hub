import { ref } from 'vue'

// Route guards must not convert transport failures into a false "no workspace"
// state. Keeping the key outside a page lets the currently rendered route show the
// real failure while navigation is cancelled.
export const routeErrorKey = ref('')

export const clearRouteError = () => {
  routeErrorKey.value = ''
}

export const setRouteError = (error: unknown) => {
  const message = error instanceof Error ? error.message.trim() : ''
  routeErrorKey.value = message || 'admin.adminAccounts.errors.request'
}
