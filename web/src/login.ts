// 登录独立入口
import { createApp } from 'vue'
import LoginView from './views/LoginView.vue'
import './styles/main.css'

const app = createApp(LoginView)
app.mount('#app')
