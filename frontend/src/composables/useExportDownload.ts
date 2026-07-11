import { api } from '@/api/bridge'
import { useToast } from '@/composables/useToast'
import type { api as apiModels } from '../../wailsjs/go/models'

/**
 * Small helper to export data from the backend and download it as a Blob.
 */
export function useExportDownload() {
  const toast = useToast()

  async function download(format: string) {
    try {
      const result: apiModels.ExportResult = await api.exportData(format)
      if (!result || !result.data) return
      const bytes = new Uint8Array(result.data)
      const blobType = format.endsWith('json') ? 'application/json' : 'text/csv'
      const blob = new Blob([bytes], { type: blobType })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = result.filename || `export.${format.includes('_') ? format.split('_').pop() : format}`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (e: any) {
      toast.push(e?.message || String(e), 'error')
    }
  }

  return { download }
}
