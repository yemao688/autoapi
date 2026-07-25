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

export function useModelTestToast() {
  const toast = useToast()

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
        toast.finishModelTest(toastId, {
          status: 'success',
          type: 'success',
          detail: options.t('testModel.resultSuccess', {
            latency: result.latency_ms,
            firstByte: result.first_byte_latency_ms ?? result.latency_ms,
          }),
        })
        return
      }

      toast.finishModelTest(toastId, {
        status: 'error',
        type: 'error',
        detail: result.error || options.t('testModel.resultFailed'),
      })
    } catch (error: any) {
      toast.finishModelTest(toastId, {
        status: 'error',
        type: 'error',
        detail: error?.message || String(error),
      })
    }
  }

  return {
    runModelTest,
  }
}
