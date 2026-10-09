import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getSetupStatus } from '../api/setup'

export const useSiteStore = defineStore('site', () => {
  const initialized = ref<boolean | null>(null)

  async function checkInitialized() {
    try {
      const res = await getSetupStatus()
      if (res.data) {
        initialized.value = res.data.initialized
      }
    } catch {
      initialized.value = null
    }
    return initialized.value
  }

  return { initialized, checkInitialized }
})
