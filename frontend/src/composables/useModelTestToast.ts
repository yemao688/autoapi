import { api } from '@/api/bridge'
import { useToast } from '@/composables/useToast'

export type ModelApiType = 'openai_chat' | 'openai_responses' | 'anthropic_messages' | 'gemini'

type Translator = (key: string, params?: Record<string, unknown>) => string

function toChatProtocol(apiType: ModelApiType): 'chat' | 'responses' | 'messages' | 'gemini' {
  if (apiType === 'openai_responses') return 'responses'
  if (apiType === 'anthropic_messages') return 'messages'
  if (apiType === 'gemini') return 'gemini'
  return 'chat'
}

function formatDurationMs(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 ms'
  if (value < 1000) return `${Math.round(value)} ms`

  const seconds = value / 1000
  if (seconds < 10) return `${seconds.toFixed(1)} s`
  if (seconds < 60) return `${Math.round(seconds)} s`

  const minutes = Math.floor(seconds / 60)
  const restSeconds = Math.round(seconds % 60)
  return `${minutes}m ${String(restSeconds).padStart(2, '0')}s`
}

export function useModelTestToast() {
  const toast = useToast()

  function normalizedDetail(value: string | undefined | null): string {
    return (value || '').trim()
  }

  function buildSuccessSummary(result: {
    latency_ms: number
    first_byte_latency_ms?: number
    finish_reason?: string
  }, t: Translator): string {
    const parts = [
      t('testModel.resultSuccessMeta', {
        firstByte: formatDurationMs(result.first_byte_latency_ms ?? result.latency_ms),
        latency: formatDurationMs(result.latency_ms),
      }),
    ]
    if (result.finish_reason) {
      parts.push(t('testModel.finishReasonValue', { reason: result.finish_reason }))
    }
    return parts.join(' · ')
  }

  function buildErrorSummary(result: {
    http_status: number
  }, t: Translator): string {
    if (result.http_status > 0) {
      return t('testModel.resultFailedStatus', { status: result.http_status })
    }
    return t('testModel.resultFailed')
  }

  function buildErrorDetail(result: {
    http_status: number
    error?: string
    response?: string
  }, t: Translator): string {
    const parts: string[] = []
    if (result.http_status > 0) {
      parts.push(t('testModel.httpStatusLine', { status: result.http_status }))
    }
    const errorText = normalizedDetail(result.error)
    if (errorText) {
      parts.push(errorText)
    }
    const responseText = normalizedDetail(result.response)
    if (responseText) {
      parts.push(responseText)
    }
    return parts.join('\n\n') || t('testModel.resultFailed')
  }

  async function runModelTest(options: {
    providerId: string
    modelName: string
    apiType: ModelApiType
    t: Translator
  }) {
    const testId = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `test-${Date.now()}`
    const toastId = toast.startModelTest({
      key: `${options.providerId}:${options.modelName}`,
      title: options.modelName,
      summary: options.t('testModel.runningSeconds', { seconds: 0 }),
      detail: options.t('testModel.runningSeconds', { seconds: 0 }),
      onRunningClose: () => {
        void api.cancelModelTest(testId).catch(() => {})
      },
    })

    try {
      const result = await api.testModelChat(
        options.providerId,
        options.modelName,
        toChatProtocol(options.apiType),
        true,
        testId,
      )

      if (result.ok) {
        const responseText = normalizedDetail(result.response) || options.t('testModel.emptyResponse')
        toast.finishModelTest(toastId, {
          status: 'success',
          type: 'success',
          summary: buildSuccessSummary(result, options.t),
          detail: responseText,
        })
        return
      }

      toast.finishModelTest(toastId, {
        status: 'error',
        type: 'error',
        summary: buildErrorSummary(result, options.t),
        detail: buildErrorDetail(result, options.t),
      })
    } catch (error: any) {
      toast.finishModelTest(toastId, {
        status: 'error',
        type: 'error',
        summary: options.t('testModel.resultFailed'),
        detail: error?.message || String(error),
      })
    }
  }

  return {
    runModelTest,
  }
}
