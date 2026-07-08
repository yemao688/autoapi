import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import i18n from './locales'
import './styles.css'

// Chart.js: register only what UsageTrendChart consumes (line/area, dual-axis,
// tooltips, legend). Tree-shaking is handled by importing from 'chart.js/auto'
// here; component-level imports are used inside UsageTrendChart.vue.
import {
  Chart as ChartJS,
  LineController,
  LineElement,
  PointElement,
  LinearScale,
  CategoryScale,
  Filler,
  Tooltip,
  Legend,
} from 'chart.js'

ChartJS.register(
  LineController,
  LineElement,
  PointElement,
  LinearScale,
  CategoryScale,
  Filler,
  Tooltip,
  Legend,
)

createApp(App).use(router).use(i18n).mount('#app')
