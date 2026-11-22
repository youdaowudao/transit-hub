<script setup lang="ts">
// 弹窗、抽屉共用的进出场动画。样式在 styles/globals.css 的 overlay-* 段落。
// fade：整体淡入淡出；modal：整体淡入 + 面板轻微放大；drawer：整体淡入 + 面板从右侧滑入。
// modal、drawer 需要在面板元素上加 data-overlay-panel，面板动画跟随外层同一次进出场。
withDefaults(defineProps<{ variant?: 'fade' | 'modal' | 'drawer' }>(), { variant: 'fade' })
</script>

<template>
  <Transition :name="`overlay-${variant}`">
    <slot />
  </Transition>
</template>
