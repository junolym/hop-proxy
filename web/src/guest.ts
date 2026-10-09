// 访客独立入口
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import GuestView from './views/GuestView.vue'
import './styles/main.css'

const app = createApp(GuestView)
app.use(createPinia())
app.mount('#app')
