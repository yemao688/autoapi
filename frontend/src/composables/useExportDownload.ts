import { api, type ExportFormat } from '@/api/bridge'
import { useToast } from '@/composables/useToast'
import type { api as apiModels } from '../../wailsjs/go/models'

/**
 * Small helper to export data from the backend and download it as a Blob.
 */
export function useExportDownload() {
  const toast = useToast()

  async function download(format: ExportFormat) {
    let url = ''
    let anchor: HTMLAnchorElement | null = null
    try {
      const result: apiModels.ExportResult = await api.exportData(format)
      if (!result || !result.data) return
      const bytes = new Uint8Array(result.data)
      const blobType = format.endsWith('json') ? 'application/json' : 'text/csv'
      const blob = new Blob([bytes], { type: blobType })
      url = URL.createObjectURL(blob)
      anchor = document.createElement('a')
      anchor.href = url
      anchor.download = result.filename || `export.${format.includes('_') ? format.split('_').pop() : format}`
      document.body.appendChild(anchor)
      anchor.click()
    } catch (e: unknown) {
      const message = e instanceof Error ? e.message : String(e)
      toast.push(message, 'error')
    } finally {
      anchor?.remove()
      if (url) setTimeout(() => URL.revokeObjectURL(url), 1000)
    }
  }

  return { download }
}
