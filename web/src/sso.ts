// SSO 独立入口
import { createApp } from 'vue'
import SSOView from './views/SSOView.vue'
import './styles/main.css'

const app = createApp(SSOView)
app.mount('#app')
