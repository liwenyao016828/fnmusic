/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  theme: {
    extend: {
      /*
        设计令牌（v2.28.0 视觉重构，方向「fnOS / 飞牛管理台」）

        这一层是**全局视觉的地基**：改色阶 = 一次改掉全站几百处用色，
        所以每个值都必须有理由，不要为了「好看一点」随手调。

        ⚠️ 2026-10-29 用户口径：「参考飞牛截图的设计语言（色彩、留白、圆角、阴影）
        重构曲率的界面」。下面每个色值都是从截图**像素直方图**里量出来的，不是估的
        （4 张原图在 .dev/refs/fnos/，采样脚本见交接文档）。

        · gray —— 飞牛中性灰阶（**纯中性，不带蓝调**）。
                  F3F3F3 是框架层（侧栏/顶栏），FFFFFF 是内容画布，
                  FAFAFA 是列表行/次级填充，E1E1E1 是结构线，D8D8D8 是控件描边。
                  文字四档：0B0B0C(主) / 454546(次) / 858585(辅助) / C2C2C2(占位)。
        · emerald —— **保留这个类名，但值已改为飞牛钴蓝**。
                  为什么保留类名：全站有 540 处 `emerald-*` 工具类，改名的收益是
                  「名字更准」，成本是 540 处 diff + 极高的漏改风险；而视觉结果
                  完全一样。所以这里**只换值**，并在注释里写清楚它现在代表钴蓝。
                  以后新代码请优先用 --ys-accent 变量或 `brand` 语义类。
                  600 = 实心按钮（白字 5.3:1，AA）；700 = 悬停；
                  500/400 = 深色底上的图标；50/100/200 = 极淡底与描边。
        · rose/amber/blue —— 飞牛状态色（红/橙/蓝），状态色只在需要时出现。
      */
      colors: {
        gray: {
          50: '#FAFAFA',
          100: '#F3F3F3',
          200: '#E1E1E1',
          300: '#D8D8D8',
          400: '#C2C2C2',
          500: '#858585',
          600: '#454546',
          700: '#333334',
          800: '#1C1C1E',
          900: '#0B0B0C',
          950: '#000000',
        },
        /*
          · emerald —— **类名是历史包袱，值现在指向「主题强调色」**。

            2026-10-29 加了主题色功能（四套：蓝 / 青玉绿 / 樱粉 / 石墨灰），
            所以这里不能再写死色值 —— 写死了切主题就切不动。
            改成引用 CSS 变量：--ys-accent-*-rgb 是空格分隔的 RGB 三元组
            （Tailwind 的 `rgb(var(--x) / <alpha-value>)` 必须是这种形式，
            否则 bg-emerald-500/40、ring-emerald-500/20 这类透明度修饰会失效），
            由 style.css 的 :root + html[data-theme=...] 决定实际颜色。

            为什么还留着 emerald 这个名字：全站 540 处 emerald-* 工具类，
            改名 = 540 处 diff + 漏改风险，而视觉结果完全一样。
            新代码请直接用 var(--ys-accent) / var(--ys-accent-soft) 这些语义令牌。
        */
        emerald: {
          50: 'rgb(var(--ys-accent-50-rgb) / <alpha-value>)',
          100: 'rgb(var(--ys-accent-100-rgb) / <alpha-value>)',
          200: 'rgb(var(--ys-accent-200-rgb) / <alpha-value>)',
          300: 'rgb(var(--ys-accent-300-rgb) / <alpha-value>)',
          400: 'rgb(var(--ys-accent-400-rgb) / <alpha-value>)',
          500: 'rgb(var(--ys-accent-500-rgb) / <alpha-value>)',
          600: 'rgb(var(--ys-accent-600-rgb) / <alpha-value>)',
          700: 'rgb(var(--ys-accent-700-rgb) / <alpha-value>)',
          800: 'rgb(var(--ys-accent-800-rgb) / <alpha-value>)',
          900: 'rgb(var(--ys-accent-900-rgb) / <alpha-value>)',
        },
        rose: {
          50: '#FFF1F0',
          100: '#FFE1DC',
          200: '#FFC9C0',
          300: '#FF9E90',
          400: '#FF6B57',
          500: '#E5484D',   /* fnOS 危险红 */
          600: '#C93B40',
          700: '#A82F33',
          800: '#8A262A',
          900: '#6F1E21',
        },
        amber: {
          50: '#FFF8E8',
          100: '#FFEDC9',
          200: '#FFDD9B',
          300: '#FFC463',
          400: '#FF9F0A',   /* fnOS 警告橙 */
          500: '#E98A00',
          600: '#B96D00',
          700: '#95570A',
          800: '#7A470D',
          900: '#653B0F',
        },
        /*
          blue —— 同样是「强调色」的另一套旧叫法（原来和 emerald 同值），
          跟着主题走。sky 同理（11 处，原本是硬编码的蓝，跟主题撞色）。
        */
        blue: {
          50: 'rgb(var(--ys-accent-50-rgb) / <alpha-value>)',
          100: 'rgb(var(--ys-accent-100-rgb) / <alpha-value>)',
          200: 'rgb(var(--ys-accent-200-rgb) / <alpha-value>)',
          300: 'rgb(var(--ys-accent-300-rgb) / <alpha-value>)',
          400: 'rgb(var(--ys-accent-400-rgb) / <alpha-value>)',
          500: 'rgb(var(--ys-accent-500-rgb) / <alpha-value>)',
          600: 'rgb(var(--ys-accent-600-rgb) / <alpha-value>)',
          700: 'rgb(var(--ys-accent-700-rgb) / <alpha-value>)',
          800: 'rgb(var(--ys-accent-800-rgb) / <alpha-value>)',
          900: 'rgb(var(--ys-accent-900-rgb) / <alpha-value>)',
        },
        sky: {
          50: 'rgb(var(--ys-accent-50-rgb) / <alpha-value>)',
          100: 'rgb(var(--ys-accent-100-rgb) / <alpha-value>)',
          200: 'rgb(var(--ys-accent-200-rgb) / <alpha-value>)',
          300: 'rgb(var(--ys-accent-300-rgb) / <alpha-value>)',
          400: 'rgb(var(--ys-accent-400-rgb) / <alpha-value>)',
          500: 'rgb(var(--ys-accent-500-rgb) / <alpha-value>)',
          600: 'rgb(var(--ys-accent-600-rgb) / <alpha-value>)',
          700: 'rgb(var(--ys-accent-700-rgb) / <alpha-value>)',
          800: 'rgb(var(--ys-accent-800-rgb) / <alpha-value>)',
          900: 'rgb(var(--ys-accent-900-rgb) / <alpha-value>)',
        },
      },
      /*
        字体：飞牛 OS 是**纯系统栈**（不引外部字体）。
        用户明确要「参考飞牛的设计语言」，宿主一致优先 —— 故此处有意覆盖设计规范里
        「不用 system-ui 字体栈」的条款。中文回落苹方 / 冬青黑，Windows 回落微软雅黑。
        字距保持 0（飞牛不做负字距，负字距在中文小字号上会挤成一团）。
      */
      fontFamily: {
        sans: [
          '-apple-system', 'BlinkMacSystemFont', '"SF Pro Text"', '"Segoe UI"',
          '"PingFang SC"', '"Hiragino Sans GB"', '"Microsoft YaHei"',
          'system-ui', 'sans-serif',
        ],
        display: [
          '-apple-system', 'BlinkMacSystemFont', '"SF Pro Display"', '"Segoe UI"',
          '"PingFang SC"', 'system-ui', 'sans-serif',
        ],
        mono: ['"SF Mono"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
      },
      /*
        圆角：飞牛的尺度很克制 —— 控件 8 / 区块 12 / 浮层 16 / 开关胶囊 full。
        （原来 10/12/16/22 四档偏「Apple 柔」，飞牛更接近「工具」的硬朗。）
      */
      borderRadius: {
        /*
          ⚠️ 改这里等于改全站圆角：rounded-lg 221 处、rounded-xl 112 处、rounded-2xl 28 处。
          飞牛只有三档 —— 控件 8 / 区块 12 / 浮层 16，所以：
            md/sm  → 6px   小标签、角标、行内 chip
            lg     → 8px   按钮、输入框（最常用的一档）
            xl     → 10px  中间档（保留，用于「比按钮大、比卡片小」的块，如列表行）
            2xl    → 12px  区块/卡片（--ys-radius-block）
            3xl    → 16px  弹窗/抽屉（--ys-radius-overlay）
          这五档之外不要再出现 rounded-[18px]/[14px]/[22px] 这类任意值。
        */
        sm: '6px',
        DEFAULT: '8px',
        md: '6px',
        lg: '8px',
        xl: '10px',
        '2xl': '12px',
        '3xl': '16px',
      },
      /*
        阴影：飞牛**几乎不用阴影**。全站只留三档语义，杜绝「11 种灰色阴影」：
          sm  —— 侧栏选中项「浮起」（白底压在 F3F3F3 上，靠这层淡影立起来）
          DEFAULT —— 卡片贴边时的极淡投影（多数卡片其实是 1px 线，不用它）
          lg / xl / 2xl —— 弹窗、抽屉、浮层，统一 0 8px 24px / 0 16px 40px
      */
      boxShadow: {
        sm: '0 1px 3px rgba(0, 0, 0, 0.08)',
        DEFAULT: '0 1px 2px rgba(0, 0, 0, 0.05)',
        md: '0 2px 8px rgba(0, 0, 0, 0.07)',
        lg: '0 8px 24px rgba(0, 0, 0, 0.10)',
        xl: '0 16px 40px rgba(0, 0, 0, 0.12)',
        '2xl': '0 24px 56px rgba(0, 0, 0, 0.16)',
        inner: 'inset 0 1px 0 rgba(255, 255, 255, 0.6)',
      },
      transitionTimingFunction: {
        /* 飞牛的手感：标准 Material 缓动，比 Apple 的「快出慢收」更「工具」一些 */
        apple: 'cubic-bezier(0.4, 0, 0.2, 1)',
        spring: 'cubic-bezier(0.4, 0, 0.2, 1)',
      },
    },
  },
  plugins: [],
}
