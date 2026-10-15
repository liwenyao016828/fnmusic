<template>
  <!--
    动作卡片：**图标 + 标题 + 一句简介**，点整张卡就等于点按钮。

    # 为什么要有它（用户 2026-10-07 指令）

    「精简项目里一些按钮的简介，不需要单独一行显示。可以用一行多个，每个按照卡片形式
    展示按钮和文字标题，简介就可以。」

    原来的排版是「一个按钮 + 下面跟一整行说明」——一屏塞不下几个动作，说明还得横着读很长。
    改成卡片之后：标题是主信息、简介缩成一行小字，靠响应式网格一行放 2~3 张，
    同屏能看的动作多了一倍，扫读成本反而更低。

    # 用法约定

    · 只用于**点击即动作**的东西。开关（switch）别用它 —— 那是 `SwitchCard` 的形态，
      硬塞进来会让「点整张卡」和「点开关」两个语义打架。
    · `hint` 写一句话，别写两句。写不下就是这句话该精简，而不是卡片该变宽。
    · 图标可选；给了就固定 16px，颜色由 `iconClass` 决定（默认品牌绿）。
  -->
  <button
    type="button"
    :disabled="disabled"
    class="group flex w-full items-start gap-2.5 rounded-xl border px-3 py-2.5 text-left transition-all"
    :class="disabled
      ? 'cursor-not-allowed border-gray-100 bg-gray-50 opacity-60'
      : 'border-gray-200 bg-white hover:border-emerald-300 hover:bg-emerald-50/40'"
  >
    <component
      :is="icon"
      v-if="icon"
      class="mt-0.5 h-4 w-4 shrink-0"
      :class="iconClass || (disabled ? 'text-gray-300' : 'text-emerald-500')"
    />
    <span class="min-w-0 flex-1">
      <b class="block text-[12.5px] font-bold" :class="disabled ? 'text-gray-400' : 'text-gray-800'">
        {{ title }}
      </b>
      <span v-if="hint" class="mt-0.5 block text-[11px] leading-snug text-gray-400">{{ hint }}</span>
    </span>
    <span v-if="badge" class="shrink-0 rounded-full bg-gray-100 px-1.5 py-0.5 text-[10px] font-bold text-gray-500">
      {{ badge }}
    </span>
  </button>
</template>

<script setup>
/**
 * `icon` 收的是组件（lucide 图标）或空 —— 传字符串会被 Vue 当成原生标签，所以这里
 * 不做兜底转换，传错在开发期就能看出来。
 */
defineProps({
  /** 卡片标题（主信息，一行） */
  title: { type: String, required: true },
  /** 一句简介（可选，缩成一行小字） */
  hint: { type: String, default: '' },
  /** lucide 图标组件，可选 */
  icon: { type: [Object, Function], default: null },
  /** 图标配色 class，默认品牌绿 */
  iconClass: { type: String, default: '' },
  /** 右侧小角标（如条数），可选 */
  badge: { type: [String, Number], default: '' },
  disabled: { type: Boolean, default: false },
})
</script>
