<template>
  <!--
    页面外壳：统一所有功能页的容器规范（布局参考 fnmusic-flow 的 .workspace）。

    结构：
      [居中容器 max-width 1500px]
        [标题区：h1 26px + 描述 13px + 右侧操作]
        [指标行（可选）]
        [内容]

    尺度依据（对齐 flow 实测值，改之前先看 HANDOVER §3.3.1）：
      flow `.workspace { padding:28px 34px 100px; max-width:1500px }`
      flow `.section-head h2 { font-size:19px }`，hero h1 29px
      → 我们取 px-5/sm:px-8（≈32px）、py-6/sm:py-7（28px）、pb-28（112px，桌面再叠 sm:pb-24=96px 给浮动播放栏让位）、h1 26px

    用法：
      <PageShell title="下载队列" desc="...">
        <template #actions><button>…</button></template>
        <template #metrics><div>…</div></template>
        ...内容...
      </PageShell>
  -->
  <div class="h-full overflow-y-auto bg-[#f5f7f9]">
    <!--
      手机端把左右内边距从 20px 收到 12px、上下从 24px 收到 12px：390px 宽的屏上
      40px 的横向留白等于白白吃掉一成宽度，而这一页要塞的是两列的卡片。
      （用户 2026-10-08：「适配手机端，一行可以用两个元素」）
    -->
    <div class="mx-auto w-full max-w-[1500px] px-3 py-3 pb-28 pb-dock sm:px-8 sm:py-7 sm:pb-24">

      <!-- 标题区 -->
      <div v-if="title || $slots.actions" class="mb-3 flex flex-wrap items-end justify-between gap-2 sm:mb-6 sm:gap-3">
        <div class="min-w-0">
          <!--
            desc 收进标题 tooltip（2026-10-14 用户：「全局页面里面的提示太多了。看起来很挤」）。
            手机上原来就不显示；桌面从「常驻一行」改成「悬停标题才出现」—— 信息不丢，
            版面每页净赚一行。纯装饰性介绍该精简的精简，操作要点应该写在动作旁边而不是这里。
          -->
          <h1 v-if="title" :title="desc || undefined" class="flex flex-wrap items-center gap-2 text-[19px] font-bold tracking-tight text-gray-800 sm:text-[26px]">
            {{ title }}
            <slot name="title-extra" />
          </h1>
        </div>
        <!--
          ⚠️ 这里**不能加 shrink-0**：窄屏下容器会按内容宽度撑开，
          内部的 flex-wrap 就失效了，按钮直接溢出屏幕。
          实测（2026-09-22，390px 视口）：「本地曲库」的操作区宽 460px、右边界到 480px，
          「批量内嵌歌词」被截在屏幕外；改成 min-w-0 后压到 340px 并正常换行成两行。
        -->
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <slot name="actions" />
        </div>
      </div>

      <!-- 指标行 -->
      <div v-if="$slots.metrics" class="mb-5">
        <slot name="metrics" />
      </div>

      <!-- 内容 -->
      <slot />
    </div>
  </div>
</template>

<script setup>
defineProps({
  title: { type: String, default: '' },
  desc: { type: String, default: '' },
})
</script>
