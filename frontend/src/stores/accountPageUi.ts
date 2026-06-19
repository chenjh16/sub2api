import { defineStore } from 'pinia'
import { ref } from 'vue'

const ACCOUNT_TOOLBAR_COLLAPSED_KEY = 'account-toolbar-collapsed'

const loadInitialToolbarCollapsed = (): boolean => {
  if (typeof window === 'undefined') return false
  try {
    return localStorage.getItem(ACCOUNT_TOOLBAR_COLLAPSED_KEY) === '1'
  } catch {
    return false
  }
}

export const useAccountPageUiStore = defineStore('accountPageUi', () => {
  const toolbarCollapsed = ref(loadInitialToolbarCollapsed())

  const setToolbarCollapsed = (collapsed: boolean) => {
    toolbarCollapsed.value = collapsed
    if (typeof window === 'undefined') return
    try {
      if (collapsed) {
        localStorage.setItem(ACCOUNT_TOOLBAR_COLLAPSED_KEY, '1')
      } else {
        localStorage.removeItem(ACCOUNT_TOOLBAR_COLLAPSED_KEY)
      }
    } catch (error) {
      console.error('Failed to save account toolbar collapsed state:', error)
    }
  }

  const toggleToolbarCollapsed = () => {
    setToolbarCollapsed(!toolbarCollapsed.value)
  }

  return {
    toolbarCollapsed,
    setToolbarCollapsed,
    toggleToolbarCollapsed
  }
})
