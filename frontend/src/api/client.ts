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

  // Routes
  routes: (): Promise<model.Route[]> => {
    ensureWails()
    return wails.ListRoutes() as Promise<model.Route[]>
  },
  getRoute: (id: string): Promise<model.Route> => {
    ensureWails()
    return wails.GetRoute(id) as Promise<model.Route>
  },
  createRoute: (input: model.RouteInput): Promise<model.Route> => {
    ensureWails()
    return wails.CreateRoute(input) as Promise<model.Route>
  },
  updateRoute: (id: string, input: model.RouteInput): Promise<model.Route> => {
    ensureWails()
    return wails.UpdateRoute(id, input) as Promise<model.Route>
  },
  deleteRoute: (id: string): Promise<void> => {
    ensureWails()
    return wails.DeleteRoute(id) as Promise<void>
  },
  reorderRoutes: (ids: string[]): Promise<void> => {
    ensureWails()
    return wails.ReorderRoutes(ids) as Promise<void>
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
  usageStats: (): Promise<model.UsageStats> => {
    ensureWails()
    return wails.GetUsageStats() as Promise<model.UsageStats>
  },
  queryLogs: (query: model.LogQuery): Promise<model.RequestLog[]> => {
    ensureWails()
    return wails.QueryLogs(query) as Promise<model.RequestLog[]>
  },
  purgeLogs: (days: number): Promise<number> => {
    ensureWails()
    return wails.PurgeLogs(days) as Promise<number>
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

  // System
  systemHealth: (): Promise<model.ServiceHealth> => {
    ensureWails()
    return wails.GetSystemHealth() as Promise<model.ServiceHealth>
  },
  proxyStatus: (): Promise<apiModels.ProxyStatus> => {
    ensureWails()
    return wails.GetProxyStatus() as Promise<apiModels.ProxyStatus>
  },
  listEndpoints: (): Promise<model.Endpoint[]> => {
    ensureWails()
    return wails.ListEndpoints() as Promise<model.Endpoint[]>
  },

  // Master password
  hasMasterPassword: (): Promise<boolean> => {
    ensureWails()
    return wails.HasMasterPassword() as Promise<boolean>
  },
  isUnlocked: (): Promise<boolean> => {
    ensureWails()
    return wails.IsUnlocked() as Promise<boolean>
  },
  setMasterPassword: (password: string): Promise<void> => {
    ensureWails()
    return wails.SetMasterPassword(password) as Promise<void>
  },
  unlock: (password: string): Promise<void> => {
    ensureWails()
    return wails.Unlock(password) as Promise<void>
  },
  changeMasterPassword: (old: string, new_: string): Promise<void> => {
    ensureWails()
    return wails.ChangeMasterPassword(old, new_) as Promise<void>
  },

  // Export
  exportData: (format: string): Promise<apiModels.ExportResult> => {
    ensureWails()
    return (wails.ExportData as (f: string) => Promise<apiModels.ExportResult>)(format)
  },
}
