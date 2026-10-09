// 扫码授权独立入口（iPhone 扫码后打开）
import { createApp } from 'vue'
import QrAuthorizeView from './views/QrAuthorizeView.vue'
import './styles/main.css'

const app = createApp(QrAuthorizeView)
app.mount('#app')
