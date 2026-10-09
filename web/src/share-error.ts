// 分享链接错误页独立入口
import { createApp } from 'vue'
import ShareErrorView from './views/ShareErrorView.vue'
import './styles/main.css'

const app = createApp(ShareErrorView)
app.mount('#app')
