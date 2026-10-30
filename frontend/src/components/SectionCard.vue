<template>
  <!--
    分区卡片：**图标方块 + 标题 + 一句副标题 +（可选）右侧动作**，下面是内容。

    # 为什么要有它（2026-10-18 用户指令）

    「这个页面有点不够优雅和简介。根据全局 ui 和布局排版。重构界面。我喜欢简洁，
    现在界面内的文字和功能没有设计感，模仿主页那种简介明了。其他几个导航页面也可以优化一下。」

    改之前每个页面各写各的卡片：圆角有 `rounded-xl` / `rounded-2xl`，标题有 13px 有 15px，
    头部有的是纯文字有的是「图标 + 标题 + 灰字说明」，同一个应用里像三个应用的拼盘。
    主页（发现音乐）用的是 18px 圆角 + 克制的头部，看起来最「有设计感」—— 所以
    这里把主页那套抽成一个组件，其余页面统一改用它。

    # 用法约定

    · 标题一行、副标题一行。副标题写不下就说明它该精简，而不是这张卡该变宽。
    · 需要悬浮解释的长文案挂 `subTitle`（或直接别写），别把三段话塞进头部。
    · 卡内**不要再嵌白底卡片** —— 内层用 `bg-[var(--ys-surface-sunken)]` 这类浅底分区，
      白底嵌白底会让人分不清哪一层是边界（补全整理页以前就踩过这个）。
    · `tone="brand"` 给「当前主任务」用（实心蓝图标），其余一律 `tone="soft"`（浅灰图标）。
      满屏实心蓝等于没有重点。

    # v2.28.0 飞牛化改动（2026-10-29）

    用户口径：「参考飞牛截图的设计语言（色彩、留白、圆角、阴影）重构界面」。
    飞牛的卡片**没有阴影**，靠「1px 不透明结构线 + 纯白底」跟 F3F3F3 框架层拉开层次；
    头部是「图标 + 标题 + 一行说明」的紧凑排布，留白靠 padding 而不是靠 line-height。
    因此这里三处改动：
      · 去掉（全站 44 张卡片叠加起来就是「灰雾」）
      · 圆角走 --ys-radius-card（12px），内边距 p-4/p-5 → p-4/p-[18px]（飞牛更紧）
      · 图标方块 10px → 8px 圆角、36px → 32px，跟飞牛的应用图标块同尺寸
  -->
  <section
    class="rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-white p-4 sm:p-[18px]"
    :class="flush ? '!p-0' : ''"
  >
    <div v-if="title || $slots.actions" class="flex items-center gap-2.5">
      <span
        v-if="icon"
        class="grid h-8 w-8 shrink-0 place-items-center rounded-[var(--ys-radius-control)]"
        :class="tone === 'brand'
          ? 'bg-[var(--ys-accent)] text-white'
          : 'bg-[var(--ys-surface-control)] text-[var(--ys-text-secondary)]'"
      >
        <component :is="icon" class="h-[15px] w-[15px]" />
      </span>
      <div class="min-w-0 flex-1">
        <h2 class="truncate text-[var(--ys-fs-title)] font-semibold tracking-[-0.01em] text-[var(--ys-text)]" :title="titleTitle || title">{{ title }}</h2>
        <!--
          窄屏副标题：原来是 `line-clamp` 之外一律 `truncate`（单行省略），
          像「歌名修复」段那句 60+ 字的说明在手机上只剩「没有歌名 / 歌名就是…」，
          用户根本不知道这段是干什么的。改成窄屏最多两行、sm 以上仍单行省略。
        -->
        <p v-if="sub" class="mt-1 text-[var(--ys-fs-caption)] leading-snug text-[var(--ys-text-aux)] line-clamp-2 sm:line-clamp-1" :title="subTitle || sub">{{ sub }}</p>
      </div>
      <!--
        右侧：计数徽标、次要按钮等（`shrink-0` 不能加在外层，窄屏会溢出）。
        `[&>*]:shrink-0` 给每个按钮单独兜底 —— 窄屏下 flex-wrap 换行后，
        按钮仍会被 flex 压成「…」或半个字，之前只能靠每个调用点自己记得加。
      -->
      <div v-if="$slots.actions" class="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-2 [&>*]:shrink-0">
        <slot name="actions" />
      </div>
    </div>

    <div :class="title || $slots.actions ? (compact ? 'mt-3' : 'mt-4') : ''">
      <slot />
    </div>
  </section>
</template>

<script setup>
/**
 * `icon` 收的是 lucide 组件（不是字符串）—— 传字符串会被 Vue 当成原生标签。
 * 传错在开发期就能看出来（控制台会报未知元素）。
 */
defineProps({
  /** 分区标题（一行） */
  title: { type: String, default: '' },
  /** 副标题（一行灰字，可选） */
  sub: { type: String, default: '' },
  /** lucide 图标组件，可选 */
  icon: { type: [Object, Function], default: null },
  /** `brand` = 实心绿图标（当前主任务）；默认 `soft` = 浅灰图标 */
  tone: { type: String, default: 'soft' },
  /** 标题的悬浮解释（长说明走这里，不要常驻占版面） */
  titleTitle: { type: String, default: '' },
  /** 副标题的悬浮解释 */
  subTitle: { type: String, default: '' },
  /** 头部与内容之间用更小的间距（内容自带分隔线时用） */
  compact: { type: Boolean, default: false },
  /** 内容区不加内边距（表格 / 列表要自己贴边时用） */
  flush: { type: Boolean, default: false },
})
</script>
