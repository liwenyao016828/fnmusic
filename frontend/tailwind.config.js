/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  theme: {
    extend: {
      /*
        设计令牌（v2.11.0 视觉重构，方向「Quiet Glass / 安静玻璃」）

        这一层是**全局视觉的地基**：改色阶 = 一次改掉全站几百处用色，
        所以每个值都必须有理由，不要为了「好看一点」随手调。

        · gray   —— Apple 系统灰阶。画布 #F5F5F7、正文 #1D1D1F、次要 #6E6E73、
                    发丝线 #E8E8ED。原来 Tailwind 的灰偏蓝、偏冷，叠在白卡上显脏。
        · emerald —— 品牌青玉。原来 #10b981 是霓虹绿，且白字压在上面只有 2.5:1；
                    现在 500 承担实心（白字 4.7:1），600 承担亮底上的文字（5.3:1），
                    400 是深色玻璃里的图标色。50/100/200 只当极淡的底与描边。
        · rose/amber/blue —— 对齐 Apple 系统色（红/橙/蓝），状态色只在需要时出现。
      */
      colors: {
        gray: {
          50: '#FAFAFC',
          100: '#F5F5F7',
          200: '#E8E8ED',
          300: '#D2D2D7',
          400: '#AEAEB2',
          500: '#86868B',
          600: '#6E6E73',
          700: '#4A4A4F',
          800: '#2C2C2E',
          900: '#1D1D1F',
          950: '#0B0B0C',
        },
        emerald: {
          50: '#F0F8F4',
          100: '#DCF0E7',
          200: '#BCE5D4',
          300: '#7FD0B5',
          400: '#149C77',
          500: '#0B8A66',
          600: '#0A7A5B',
          700: '#086450',
          800: '#074F40',
          900: '#063F34',
        },
        rose: {
          50: '#FFF1F0',
          100: '#FFE0DD',
          200: '#FFC6C1',
          300: '#FF9E97',
          400: '#FF6B61',
          500: '#E03E33',
          600: '#C22E24',
          700: '#A3251D',
          800: '#86211B',
          900: '#701D18',
        },
        amber: {
          50: '#FFF6E9',
          100: '#FFEACC',
          200: '#FFD79E',
          300: '#FFBE64',
          400: '#FF9F2E',
          500: '#E8811A',
          600: '#B65C00',
          700: '#94500A',
          800: '#7A420D',
          900: '#67390F',
        },
        blue: {
          50: '#EFF6FF',
          100: '#DCEBFF',
          200: '#BDD9FF',
          300: '#8FBEFF',
          400: '#5A9CFF',
          500: '#0A7AFF',
          600: '#0064D2',
          700: '#0055B3',
          800: '#004894',
          900: '#003D7A',
        },
        sky: {
          50: '#EFF8FF',
          100: '#DBEEFF',
          200: '#B9DDFF',
          300: '#8BC7FF',
          400: '#56ACFF',
          500: '#0A88F0',
          600: '#006FC9',
          700: '#005CA8',
          800: '#004E8C',
          900: '#004273',
        },
        // 旧深色/极光令牌：沉浸式播放器等历史页面还在引用，保留不动
        dark: { 950: '#07080d', 900: '#0c0e17', 850: '#111422', 800: '#171b2d', 700: '#232842' },
        aurora: { cyan: '#00f2fe', magenta: '#f72585', violet: '#7209b7', purple: '#4cc9f0', emerald: '#00f5d4', neon: '#39ff14' },
      },
      /*
        字体：用户明确要「苹果那种简洁风格」——
        故此处有意覆盖设计规范里「不用 system-ui 字体栈」的条款，采用 SF Pro 字栈。
        中文回落到苹方 / 冬青黑，Windows 回落到微软雅黑。
      */
      fontFamily: {
        sans: [
          '-apple-system', 'BlinkMacSystemFont', '"SF Pro Text"', '"SF Pro Display"',
          '"Helvetica Neue"', '"PingFang SC"', '"Hiragino Sans GB"',
          '"Microsoft YaHei"', 'system-ui', 'sans-serif',
        ],
        display: [
          '-apple-system', 'BlinkMacSystemFont', '"SF Pro Display"', '"SF Pro Text"',
          '"Helvetica Neue"', '"PingFang SC"', 'system-ui', 'sans-serif',
        ],
        mono: ['"SF Mono"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
      },
      /*
        圆角：把全站的 rounded-lg / xl / 2xl 一次性收进 Apple 的尺度
        （控件 10 / 内层卡 12 / 卡片 16 / 大容器 22），不再出现 10/12/18 三种混用。
      */
      borderRadius: {
        lg: '10px',
        xl: '12px',
        '2xl': '16px',
        '3xl': '22px',
      },
      /*
        阴影：Apple 的阴影是「大扩散 + 极低不透明度」，不是「小黑边」。
        下面几档统一按这个思路给，全站的 shadow-sm/lg 因此一起升级。
      */
      boxShadow: {
        sm: '0 1px 2px rgba(0, 0, 0, 0.04), 0 1px 1px rgba(0, 0, 0, 0.03)',
        DEFAULT: '0 1px 3px rgba(0, 0, 0, 0.05), 0 1px 2px rgba(0, 0, 0, 0.04)',
        md: '0 4px 16px rgba(0, 0, 0, 0.06)',
        lg: '0 8px 30px rgba(0, 0, 0, 0.08)',
        xl: '0 18px 48px rgba(0, 0, 0, 0.12)',
        '2xl': '0 28px 72px rgba(0, 0, 0, 0.16)',
        inner: 'inset 0 1px 0 rgba(255, 255, 255, 0.6)',
      },
      transitionTimingFunction: {
        // Apple 的「快出慢收」曲线：动画末尾几乎无减速感
        apple: 'cubic-bezier(0.32, 0.72, 0, 1)',
        spring: 'cubic-bezier(0.34, 1.3, 0.64, 1)',
      },
      keyframes: {
        'ys-rise': {
          from: { opacity: '0', transform: 'translate3d(0, 10px, 0)' },
          // 终态用 none：fill-mode both 会保留终态，非 none 的 transform 会成为 fixed 子元素的包含块
          to: { opacity: '1', transform: 'none' },
        },
        'ys-fade': {
          from: { opacity: '0' },
          to: { opacity: '1' },
        },
      },
      animation: {
        'spin-slow': 'spin 18s linear infinite',
        'ys-rise': 'ys-rise 0.5s cubic-bezier(0.32, 0.72, 0, 1) both',
        'ys-fade': 'ys-fade 0.6s cubic-bezier(0.32, 0.72, 0, 1) both',
      },
    },
  },
  plugins: [],
}
