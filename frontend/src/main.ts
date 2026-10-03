import { createApp } from 'vue'
import App from './App.vue'
import './styles/globals.css'
import { router } from './router'

import { messageLocale } from './locales'

// HTML 标识实际文案语言，格式化语言通过 locales 统一配置。
document.documentElement.lang = messageLocale

const app = createApp(App)

const themeColor = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
const syncThemeColor = () => {
  themeColor?.setAttribute('content', document.documentElement.classList.contains('dark') ? '#121212' : '#fafafa')
}
const themeObserver = new MutationObserver(syncThemeColor)
themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
syncThemeColor()

app.use(router)
app.mount('#app')

