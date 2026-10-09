<template>
  <div class="min-h-screen flex items-center justify-center bg-gray-50">
    <div class="w-full max-w-md bg-white rounded-lg shadow-sm border border-gray-200 p-8 mx-4 sm:mx-0 text-center">
      <div class="w-14 h-14 rounded-full bg-red-100 flex items-center justify-center mx-auto mb-5">
        <svg class="w-7 h-7 text-red-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
            d="M12 9v2m0 4h.01M4.93 19h14.14c1.54 0 2.5-1.67 1.73-3L13.73 4a2 2 0 00-3.46 0L3.2 16c-.77 1.33.19 3 1.73 3z" />
        </svg>
      </div>
      <h1 class="text-lg font-bold text-gray-900 mb-3">分享链接无效或已过期</h1>
      <p class="text-sm text-gray-500 leading-relaxed whitespace-nowrap">
        分享链接无效或已过期，请联系分享者获取新的链接。
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
// 静态错误页，无逻辑
</script>
