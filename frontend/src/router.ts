import { createRouter, createWebHashHistory } from 'vue-router'
import DashboardView from './views/DashboardView.vue'
import ProvidersView from './views/ProvidersView.vue'
import ModelRulesView from './views/ModelRulesView.vue'
import ApiKeysView from './views/ApiKeysView.vue'
import UsageStatsView from './views/UsageStatsView.vue'
import SettingsView from './views/SettingsView.vue'
import PricingView from './views/PricingView.vue'

const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/dashboard', name: 'dashboard', component: DashboardView },
  { path: '/providers', name: 'providers', component: ProvidersView },
  { path: '/model-rules', name: 'model-rules', component: ModelRulesView },
  { path: '/api-keys', name: 'api-keys', component: ApiKeysView },
  { path: '/usage-stats', name: 'usage-stats', component: UsageStatsView },
  { path: '/settings', name: 'settings', component: SettingsView },
  { path: '/pricing', name: 'pricing', component: PricingView },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

export default router
