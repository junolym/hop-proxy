<template>
  <div ref="containerRef" class="relative">
    <button
      type="button"
      :disabled="disabled"
      @click="toggle"
      @keydown="onTriggerKeydown"
      :class="btnClass"
      class="flex items-center justify-between text-left bg-white"
    >
      <span :class="hasValue ? 'text-gray-900' : 'text-gray-400'" class="truncate">
        {{ currentLabel || placeholder }}
      </span>
      <svg
        class="flex-shrink-0 ml-2 w-4 h-4 text-gray-400 transition-transform"
        :class="{ 'rotate-180': open }"
        fill="none" viewBox="0 0 24 24" stroke="currentColor"
      >
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
      </svg>
    </button>

    <ul
      v-if="open"
      class="absolute z-50 mt-1 w-full bg-white border border-gray-200 rounded-md shadow-lg max-h-60 overflow-auto py-1"
      @keydown="onListKeydown"
      tabindex="-1"
      ref="listRef"
    >
      <li
        v-for="(opt, idx) in options"
        :key="String(opt.value) + '|' + opt.label"
        @click="select(opt)"
        @mouseenter="highlightIndex = idx"
        class="px-3 py-2 text-sm cursor-pointer"
        :class="opt.value === modelValue
          ? 'bg-blue-50 text-blue-700 font-medium'
          : highlightIndex === idx
            ? 'bg-gray-100 text-gray-900'
            : 'text-gray-700'"
      >
        {{ opt.label }}
        <span v-if="opt.description" class="block text-xs font-normal text-gray-400">{{ opt.description }}</span>
      </li>
      <li v-if="options.length === 0" class="px-3 py-2 text-sm text-gray-400">无选项</li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'

interface Option {
  value: string | number | null
  label: string
  // 可选：选项描述，展示在标题下方的小字第二行
  description?: string
}

const props = withDefaults(defineProps<{
  modelValue: string | number | null
  options: Option[]
  placeholder?: string
  disabled?: boolean
  btnClass?: string
}>(), {
  placeholder: '',
  disabled: false,
  btnClass: 'w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500',
})

const emit = defineEmits<{
  'update:modelValue': [value: string | number | null]
  'open': []
}>()

const open = ref(false)
const highlightIndex = ref(-1)
const containerRef = ref<HTMLElement | null>(null)
const listRef = ref<HTMLElement | null>(null)

const hasValue = computed(() => props.modelValue !== null && props.modelValue !== '' && props.modelValue !== undefined)

const currentLabel = computed(() => {
  const opt = props.options.find(o => o.value === props.modelValue)
  return opt?.label ?? ''
})

function toggle() {
  if (props.disabled) return
  open.value = !open.value
  if (open.value) {
    emit('open')
    // 默认高亮当前选中项
    const idx = props.options.findIndex(o => o.value === props.modelValue)
    highlightIndex.value = idx >= 0 ? idx : 0
  }
}

function select(opt: Option) {
  emit('update:modelValue', opt.value)
  open.value = false
}

function onTriggerKeydown(e: KeyboardEvent) {
  if (props.disabled) return
  if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    if (!open.value) {
      open.value = true
      emit('open')
      const idx = props.options.findIndex(o => o.value === props.modelValue)
      highlightIndex.value = idx >= 0 ? idx : 0
    } else {
      moveHighlight(1)
    }
  } else if (e.key === 'Escape') {
    open.value = false
  }
}

function onListKeydown(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    moveHighlight(1)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    moveHighlight(-1)
  } else if (e.key === 'Enter') {
    e.preventDefault()
    if (highlightIndex.value >= 0 && highlightIndex.value < props.options.length) {
      select(props.options[highlightIndex.value])
    }
  } else if (e.key === 'Escape') {
    open.value = false
  }
}

function moveHighlight(delta: number) {
  const len = props.options.length
  if (len === 0) return
  let idx = highlightIndex.value
  idx = ((idx + delta) % len + len) % len
  highlightIndex.value = idx
  // 滚动可见
  nextTick(() => {
    const el = listRef.value?.querySelectorAll('li')[idx] as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  })
}

function onClickOutside(e: MouseEvent) {
  if (containerRef.value && !containerRef.value.contains(e.target as Node)) {
    open.value = false
  }
}

onMounted(() => document.addEventListener('click', onClickOutside))
onUnmounted(() => document.removeEventListener('click', onClickOutside))

// 关闭时重置高亮
watch(open, (v) => { if (!v) highlightIndex.value = -1 })
</script>
