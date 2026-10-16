<template>
  <!--
    页面外壳：统一所有功能页的容器规范（布局参考 fnmusic-flow 的 .workspace）。

    结构：
      [居中容器 max-width 1500px]
        [操作栏：左侧 title-extra 徽标 + 右侧 actions（页面名在 App.vue 顶栏，见下）]
        [指标行（可选）]
        [内容]

    页面名不出现在这里（2026-10-18 用户口径：「把页面名移到 logo 和音源管理那行去」），
    介绍语也不常驻（同一天：「去掉简介」，一律改成悬停可见）。
    两者都只有**一个**数据源 —— services/navViews.js 里该视图 / 分段的 name 与 desc，
    由 App.vue 顶部那一条栏渲染。因此这里不再保留 title / desc 两个 prop：
    留着它们只会诱使以后又有人往页面里塞一份重复的标题或简介。

    尺度依据（对齐 flow 实测值，改之前先看 HANDOVER §3.3.1）：
      flow `.workspace { padding:28px 34px 100px; max-width:1500px }`
      flow `.section-head h2 { font-size:19px }`，hero h1 29px
      → 我们取 px-5/sm:px-8（≈32px）、py-6/sm:py-7（28px）、pb-28（112px，桌面再叠 sm:pb-24=96px 给浮动播放栏让位）、h1 26px

      用法：
      <PageShell>
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

      <!--
        操作栏（不再是「标题区」）。
        2026-10-18 用户口径：「页面最顶部，把发现音乐、本地曲库等页面名，移到 logo 和
        音源管理那行去」——所以页面名只出现在 App.vue 顶部那一条栏里，这里**不再渲染 h1**，
        各页也就不用再重复写一遍自己的名字。
        title / desc 两个 prop 仍保留（调用方语义与可访问性提示），只是不在这儿显示：
        · 页面名 → 顶栏的 <h1>（数据源仍是 navViews.js）
        · 介绍语 → 一律进 `title=` 悬停提示，不占版面（用户 2026-10-18：「去掉简介」）。
      -->
      <div v-if="$slots.actions || $slots['title-extra']" class="mb-3 flex flex-wrap items-center justify-between gap-2 sm:mb-6 sm:gap-3">
        <div class="min-w-0">
          <slot name="title-extra" />
        </div>
        <!--
          ⚠️ 这里**不能加 shrink-0**：窄屏下容器会按内容宽度撑开，
          内部的 flex-wrap 就失效了，按钮直接溢出屏幕。
          实测（2026-09-22，390px 视口）：「本地曲库」的操作区宽 460px、右边界到 480px，
          「批量内嵌歌词」被截在屏幕外；改成 min-w-0 后压到 340px 并正常换行成两行。
        -->
        <div class="ml-auto flex min-w-0 flex-wrap items-center gap-2">
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
// 无 props：页面名与介绍语都由 services/navViews.js + App.vue 顶栏负责（见上方注释）。
</script>
