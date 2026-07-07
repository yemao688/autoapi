import { ref, type Ref } from 'vue'
import { api } from '../api/client'

export function useApi<T>(fetcher: () => Promise<T>) {
  const data: Ref<T | null> = ref(null)
  const loading = ref(false)
  const error: Ref<string | null> = ref(null)

  async function execute(): Promise<T | null> {
    loading.value = true
    error.value = null
    try {
      const result = await fetcher()
      data.value = result
      return result
    } catch (e: any) {
      const msg = e?.message || String(e)
      error.value = msg
      return null
    } finally {
      loading.value = false
    }
  }

  return { data, loading, error, execute }
}

export { api }
