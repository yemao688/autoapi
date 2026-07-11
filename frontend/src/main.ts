import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import i18n from './locales'
import './styles.css'

// Chart.js: register the controllers/elements/scales/plugins the views use.
// Trend chart (line/area) needs LineController + LineElement; donut chart
// needs DoughnutController + ArcElement. Tooltip + Legend are shared.
import {
  Chart as ChartJS,
  LineController,
  LineElement,
  PointElement,
  DoughnutController,
  ArcElement,
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
  DoughnutController,
  ArcElement,
  LinearScale,
  CategoryScale,
  Filler,
  Tooltip,
  Legend,
)

createApp(App).use(router).use(i18n).mount('#app')
