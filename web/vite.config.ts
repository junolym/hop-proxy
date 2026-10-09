import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue()],
  build: {
    rollupOptions: {
      input: {
        main: resolve(__dirname, 'index.html'),
        sso: resolve(__dirname, 'sso.html'),
        login: resolve(__dirname, 'login.html'),
        guest: resolve(__dirname, 'guest.html'),
        'share-error': resolve(__dirname, 'share-error.html'),
        'qr-authorize': resolve(__dirname, 'qr-authorize.html'),
      },
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': {
        target: 'ws://localhost:8080',
        ws: true
      }
    }
  }
})
