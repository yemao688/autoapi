import * as wails from '../../wailsjs/go/api/App'
import type { model, api as apiModels } from '../../wailsjs/go/models'

function ensureWails(): void {
  if (typeof window === 'undefined' || !(window as any).go) {
    throw new Error(
      'Wails runtime not available — running outside of Wails webview. ' +
        'Use `wails dev` or build with `wails build`.'
    )
  }
}

export const api = {
  // Dashboard
  dashboard: (): Promise<model.DashboardData> => {
    ensureWails()
    return wails.GetDashboard() as Promise<model.DashboardData>
  },

  // Providers
  providers: (): Promise<model.Provider[]> => {
    ensureWails()
    return wails.ListProviders() as Promise<model.Provider[]>
  },
  getProvider: (id: string): Promise<model.Provider> => {
    ensureWails()
    return wails.GetProvider(id) as Promise<model.Provider>
  },
  createProvider: (input: model.ProviderInput): Promise<model.Provider> => {
    ensureWails()
    return wails.CreateProvider(input) as Promise<model.Provider>
  },
  updateProvider: (id: string, input: model.ProviderInput): Promise<model.Provider> => {
    ensureWails()
    return wails.UpdateProvider(id, input) as Promise<model.Provider>
  },
  deleteProvider: (id: string): Promise<void> => {
    ensureWails()
    return wails.DeleteProvider(id) as Promise<void>
  },
  testProvider: (id: string): Promise<model.ProviderTestResult> => {
    ensureWails()
    return wails.TestProvider(id) as Promise<model.ProviderTestResult>
  },
  testAllProviders: (): Promise<model.ProviderTestResult[]> => {
    ensureWails()
    return wails.TestAllProviders() as Promise<model.ProviderTestResult[]>
  },
  listModels: (providerId: string): Promise<model.Model[]> => {
    ensureWails()
    return wails.ListModels(providerId) as Promise<model.Model[]>
  },
  fetchUpstreamModels: (providerId: string): Promise<model.Model[]> => {
    ensureWails()
    return wails.FetchUpstreamModels(providerId) as Promise<model.Model[]>
  },
  addProviderModels: (providerId: string, names: string[]): Promise<void> => {
    ensureWails()
    return wails.AddProviderModels(providerId, names) as Promise<void>
  },
  deleteModel: (providerId: string, modelName: string): Promise<void> => {
    ensureWails()
    return wails.DeleteModel(providerId, modelName) as Promise<void>
  },
  clearProviderModels: (providerId: string): Promise<void> => {
    ensureWails()
    return wails.ClearProviderModels(providerId) as Promise<void>
  },
  updateModelName: (providerId: string, oldName: string, newName: string): Promise<void> => {
    ensureWails()
    return wails.UpdateModelName(providerId, oldName, newName) as Promise<void>
  },
  getProviderKey: (providerId: string): Promise<string> => {
    ensureWails()
    return wails.GetProviderKey(providerId) as Promise<string>
  },
  testModelLatency: (providerId: string, modelName: string): Promise<model.ModelTestResult> => {
    ensureWails()
    return wails.TestModelLatency(providerId, modelName) as Promise<model.ModelTestResult>
  },
  setModelsActive: (providerId: string, modelNames: string[], active: boolean): Promise<void> => {
    ensureWails()
    return wails.SetModelsActive(providerId, modelNames, active) as Promise<void>
  },
  setProviderEnabled: (id: string, enabled: boolean): Promise<void> => {
    ensureWails()
    return wails.SetProviderEnabled(id, enabled) as Promise<void>
  },
  testModelChat: (providerId: string, modelName: string): Promise<model.ModelChatTestResult> => {
    ensureWails()
    return wails.TestModelChat(providerId, modelName) as Promise<model.ModelChatTestResult>
  },

  // Model rules
  modelRules: (): Promise<model.ModelRule[]> => {
    ensureWails()
    return wails.ListModelRules() as Promise<model.ModelRule[]>
  },
  getModelRule: (id: string): Promise<model.ModelRule> => {
    ensureWails()
    return wails.GetModelRule(id) as Promise<model.ModelRule>
  },
  createModelRule: (input: model.ModelRuleInput): Promise<model.ModelRule> => {
    ensureWails()
    return wails.CreateModelRule(input) as Promise<model.ModelRule>
  },
  updateModelRule: (id: string, input: model.ModelRuleInput): Promise<model.ModelRule> => {
    ensureWails()
    return wails.UpdateModelRule(id, input) as Promise<model.ModelRule>
  },
  reorderRuleTargets: (ruleId: string, targetIds: string[]): Promise<void> => {
    ensureWails()
    return wails.ReorderModelRuleTargets(ruleId, targetIds) as Promise<void>
  },
  deleteModelRule: (id: string): Promise<void> => {
    ensureWails()
    return wails.DeleteModelRule(id) as Promise<void>
  },
  reorderModelRules: (ids: string[]): Promise<void> => {
    ensureWails()
    return wails.ReorderModelRules(ids) as Promise<void>
  },

  // API Keys
  apiKeys: (): Promise<model.ApiKey[]> => {
    ensureWails()
    return wails.ListAPIKeys() as Promise<model.ApiKey[]>
  },
  createApiKey: (input: model.ApiKeyInput): Promise<model.ApiKey> => {
    ensureWails()
    return wails.CreateAPIKey(input) as Promise<model.ApiKey>
  },
  updateApiKey: (id: string, input: model.ApiKeyInput): Promise<model.ApiKey> => {
    ensureWails()
    return wails.UpdateAPIKey(id, input) as Promise<model.ApiKey>
  },
  deleteApiKey: (id: string): Promise<void> => {
    ensureWails()
    return wails.DeleteAPIKey(id) as Promise<void>
  },

  // Usage & Logs
  usageStats: (query?: model.LogQuery): Promise<model.UsageStats> => {
    ensureWails()
    return wails.GetUsageStats(query || ({} as model.LogQuery)) as Promise<model.UsageStats>
  },
  queryLogs: (query: model.LogQuery): Promise<model.LogQueryResult> => {
    ensureWails()
    return wails.QueryLogs(query) as Promise<model.LogQueryResult>
  },
  usageTrends: (query: model.UsageTrendsQuery): Promise<model.UsageTrends> => {
    ensureWails()
    return wails.GetUsageTrends(query) as Promise<model.UsageTrends>
  },
  purgeLogs: (days: number): Promise<number> => {
    ensureWails()
    return wails.PurgeLogs(days) as Promise<number>
  },
  clearLogs: (): Promise<number> => {
    ensureWails()
    return wails.ClearLogs() as Promise<number>
  },
  pingLogEvent: (): Promise<void> => {
    ensureWails()
    return wails.PingLogEvent() as Promise<void>
  },

  // Settings
  getSettings: (): Promise<model.Settings> => {
    ensureWails()
    return wails.GetSettings() as Promise<model.Settings>
  },
  saveSettings: (settings: model.Settings): Promise<void> => {
    ensureWails()
    return wails.SaveSettings(settings) as Promise<void>
  },
  resetSettings: (): Promise<model.Settings> => {
    ensureWails()
    return wails.ResetSettings() as Promise<model.Settings>
  },
  runtimePaths: (): Promise<apiModels.RuntimePaths> => {
    ensureWails()
    return wails.GetRuntimePaths() as Promise<apiModels.RuntimePaths>
  },

  // System
  systemHealth: (): Promise<model.ServiceHealth> => {
    ensureWails()
    return wails.GetSystemHealth() as Promise<model.ServiceHealth>
  },
  proxyStatus: (): Promise<apiModels.ProxyStatus> => {
    ensureWails()
    return wails.GetProxyStatus() as Promise<apiModels.ProxyStatus>
  },
  startProxy: (): Promise<void> => {
    ensureWails()
    return wails.StartProxy() as Promise<void>
  },
  stopProxy: (): Promise<void> => {
    ensureWails()
    return wails.StopProxy() as Promise<void>
  },
  restartProxy: (): Promise<void> => {
    ensureWails()
    return wails.RestartProxy() as Promise<void>
  },
  listEndpoints: (): Promise<model.Endpoint[]> => {
    ensureWails()
    return wails.ListEndpoints() as Promise<model.Endpoint[]>
  },

  // Export
  exportData: (format: string): Promise<apiModels.ExportResult> => {
    ensureWails()
    return (wails.ExportData as (f: string) => Promise<apiModels.ExportResult>)(format)
  },
  openStorageFolder: (): Promise<void> => {
    ensureWails()
    return wails.OpenStorageFolder() as Promise<void>
  },
}
