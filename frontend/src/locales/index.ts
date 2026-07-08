import { createI18n } from 'vue-i18n'
import zhCN from './zh-CN.json'
import enUS from './en-US.json'

export type AppLocale = 'zh-CN' | 'en-US'

const LOCALE_STORAGE_KEY = 'autoapi-language'
const SUPPORTED_LOCALES: AppLocale[] = ['zh-CN', 'en-US']
const DEFAULT_LOCALE: AppLocale = 'zh-CN'

function resolveInitialLocale(): AppLocale {
  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(LOCALE_STORAGE_KEY)
    if (stored && (SUPPORTED_LOCALES as string[]).includes(stored)) {
      return stored as AppLocale
    }
  }
  if (typeof navigator !== 'undefined' && navigator.language) {
    const lang = navigator.language
    if (lang.toLowerCase().startsWith('en')) return 'en-US'
    if (lang.toLowerCase().startsWith('zh')) return 'zh-CN'
  }
  return DEFAULT_LOCALE
}

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: resolveInitialLocale(),
  fallbackLocale: 'zh-CN',
  messages: {
    'zh-CN': zhCN,
    'en-US': enUS,
  },
})

export { LOCALE_STORAGE_KEY, SUPPORTED_LOCALES, DEFAULT_LOCALE }
export default i18n
