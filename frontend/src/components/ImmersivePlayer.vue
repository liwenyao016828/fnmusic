<template>
  <!--
    沉浸式全屏播放页（2026-09-29 视觉层重做，v2.1.44）。

    结构只有一套（手机 / 桌面同一份 DOM），靠 CSS 变量与媒体查询调版式与尺度。
    ⚠️ 档位口径（v2.1.62 用户定）：**除了手机窗口，都是桌面档**。
       手机 = 触摸（粗指针）且窗口窄；鼠标设备无论窗口多窄都拿桌面档 —— 判定见文件尾的媒体查询注释。
    ⚠️ 版式（v2.1.64，用户 2026-10-01 发参考播放器截图：「把桌面端的播放页面布局改为这样的」）：
       手机档 = 纵向单栏：顶部极简导航 → 大封面 → 歌曲信息 → 沉浸式歌词 → 极细进度条 → 播放控制；
       桌面档 = **两栏**：左栏 封面 / 歌名行（含收藏 · 更多）/ 歌手 · 词曲行 / 进度条（两端时间 +
                FLAC 徽标压在轨道上方）/ 播放控制；右栏 **浮动歌词**（无卡片、无边框，直接浮在背景上）；
                右边缘圆形「上一首 / 下一首」在中上部；左上圆形收起、右上圆角方形全屏、右下浮动圆钮。
       （版式变更史：v2.1.42-43 40/60 分栏 → v2.1.45 全端纵向 → v2.1.46 宽屏分栏 → v2.1.47 全端纵向
         （spec 十六 当时明确「不要为了响应式在桌面强行加左右两栏」）→ v2.1.64 桌面两栏。
         口径以用户最新的截图为准，改回去之前先问。）

    背景链路（v2.1.63 起，对齐参考播放器实测样式）：
      当前封面 → 双主色（主色 + 与主色隔开的次色）→ 氛围渐变（左上主色 + 右上次色
      + 132° 暗色基底，saturate(1.24) / opacity .86）→ 桌面档 4 颗漂移光球
      （blur 46px，18~24s 无限漂移，鼠标视差 ±1.4%）→ 竖版三段压暗渐变。
      换歌靠「两层氛围交叉淡入」，不是跳变；背景只给颜色和氛围，看不清封面细节。
      （旧链路：blur(40px) 封面图 + 单色染色层 + 单色光晕，已在 v2.1.63 移除。）

    ⚠️ 功能面**一个字没动**：播放/暂停/上下首/进度/音量/播放模式/收藏/双语/字号/复制歌词/
      播放列表计数/点歌词跳转/歌词自动居中滚动 —— 全部原样，只换了呈现。
      （v2.1.63 换掉的是氛围背景、增加漂移光球与指针视差、加强歌词对比度、
        桌面档控制件与进度条尺寸 —— 功能面依旧零改动。）
    ⚠️ 这个文件曾被并行的另一个会话覆盖过（看板 c0978bd1「视觉质感优化」那版保留了左右分栏）。
      用户 2026-09-29 明确选边：**以本版（纵向单栏沉浸式）为准**。要改回去前先问。
  -->
  <div
    ref="rootEl"
    class="ys-imm"
    :class="[
      mobileTier ? 'is-tier-mobile' : 'is-tier-desktop',
      tierOverride ? 'is-tier-forced' : '',
      pageIdle ? 'is-page-idle' : '',
    ]"
    role="dialog"
    aria-modal="true"
    aria-label="沉浸式播放器"
    :style="rootStyle"
  >

    <!-- ① 氛围背景层（v2.1.63 靠齐参考播放器实测样式）
         封面双主色 → 两层交叉淡入淡出（换歌＝渐变不是跳变）→ 4 颗漂移光球（仅桌面档）→ 压暗渐变 -->
    <div class="ys-imm__bg" aria-hidden="true">
      <div
        class="ys-imm__ambient"
        :class="{ 'is-on': ambientFront === 'a' }"
        :style="{ background: ambientA }"
      ></div>
      <div
        class="ys-imm__ambient"
        :class="{ 'is-on': ambientFront === 'b' }"
        :style="{ background: ambientB }"
      ></div>

      <!-- 漂移光球 ×4：primary / secondary / tertiary / pulse
           （手机档不渲染：v2.1.62 口径下手机保持原样，也省掉四个 46px 模糊层的开销） -->
      <template v-if="desktopTier">
        <span class="ys-imm__orb ys-imm__orb--primary"></span>
        <span class="ys-imm__orb ys-imm__orb--secondary"></span>
        <span class="ys-imm__orb ys-imm__orb--tertiary"></span>
        <span class="ys-imm__orb ys-imm__orb--pulse"></span>
      </template>

      <!-- 压暗渐变：竖版三段，保证文字始终可读（比旧版更淡，让氛围透出来） -->
      <div class="ys-imm__scrim"></div>
    </div>

    <!-- ② 内容列：居中、限宽、纵向到底 -->
    <div class="ys-imm__shell">

      <!-- ③ 顶部角标：手机档 左收起（圆形）+ 右更多；桌面档 左收起（圆形）+ 右全屏（圆角方形）。
           桌面档的收藏 / 更多挪到了歌名行右侧（见 ④），顶部只剩这两颗，符合参考截图的「大留白」。 -->
      <header class="ys-imm__top">
        <button
          class="ys-imm__icon-btn ys-imm__icon-btn--back"
          @click="$emit('collapse')"
          title="收起播放页"
          aria-label="收起播放页"
        >
          <ChevronDown :stroke-width="1.5" class="ys-imm__icon" />
        </button>

        <!-- 手机档：更多（收藏 / 播放列表 / 复制歌词 / 双语 / 字号 都在这张浮层里） -->
        <button
          class="ys-imm__icon-btn ys-imm__mob-g"
          @click="showMore = !showMore"
          title="更多"
          aria-label="更多"
          :class="{ 'is-on': showMore }"
        >
          <MoreVertical :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
        </button>

        <!-- 桌面档：全屏（圆角方形，对齐参考截图中右上角那颗） -->
        <button
          class="ys-imm__icon-btn ys-imm__icon-btn--expand ys-imm__desk-g"
          @click="toggleFullscreen"
          :title="isFullscreen ? '退出全屏' : '全屏'"
          :aria-label="isFullscreen ? '退出全屏' : '全屏'"
          :class="{ 'is-on': isFullscreen }"
        >
          <Minimize2 v-if="isFullscreen" :stroke-width="1.5" class="ys-imm__icon" />
          <Maximize2 v-else :stroke-width="1.5" class="ys-imm__icon" />
        </button>
      </header>

      <!-- ③′ 「更多」浮层已挪进左栏信息块里（挂在歌名行那颗 ⋮ 下面）——见 __stage 内。 -->

      <!-- ④ 主体：桌面档两栏 —— 左栏（封面 · 信息，下方接 ⑤ 进度与控制）＋ 右栏（浮动歌词）。
           手机档：两个「栏」包装都是 display:contents，不生成盒子，DOM 顺序仍是
           封面 → 信息 → 歌词 → 进度/控制，和旧版逐像素一致。 -->
      <main class="ys-imm__body">

        <!-- 左栏 · 上半（桌面档 grid 第 1 行第 1 列；手机档透明） -->
        <div class="ys-imm__stage">

          <div class="ys-imm__cover-wrap">
            <img
              v-if="currentSong?.cover"
              :src="currentSong.cover"
              alt=""
              referrerpolicy="no-referrer"
              class="ys-imm__cover"
              @error="$event.target.style.display = 'none'"
            />
            <div v-else class="ys-imm__cover ys-imm__cover--empty">
              <Music :stroke-width="1" class="ys-imm__cover-icon" />
            </div>
          </div>

          <div class="ys-imm__meta">
            <div class="ys-imm__title-row">
              <h1 class="ys-imm__title">{{ currentSong?.name || '—' }}</h1>

              <!-- 桌面档：收藏 + 更多收在歌名行右侧（手机档这一组不渲染，标题仍是居中一行）。
                   「更多」这张浮层在桌面档就挂在这颗 ⋮ 下面（见下：浮层 DOM 在 __meta 之后）。 -->
              <div class="ys-imm__title-actions ys-imm__desk-f">
                <button
                  class="ys-imm__meta-btn"
                  :class="{ 'is-on': isFavorite }"
                  @click="toggleFavorite"
                  :title="isFavorite ? '取消收藏' : '收藏'"
                  :aria-label="isFavorite ? '取消收藏' : '收藏'"
                >
                  <Heart
                    :stroke-width="1.5"
                    :fill="isFavorite ? 'currentColor' : 'none'"
                    class="ys-imm__icon ys-imm__icon--sm"
                  />
                </button>
                <button
                  class="ys-imm__meta-btn"
                  :class="{ 'is-on': showMore }"
                  @click="showMore = !showMore"
                  title="更多"
                  aria-label="更多"
                >
                  <MoreVertical :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
                </button>
              </div>

              <!-- 「更多」浮层（v2.1.64）：挂在歌名行内部、从 ⋮ 的左上角展开。
                   手机档 .ys-imm__title-row 没有定位 → 参照物退回 .ys-imm__shell（仍旧贴屏幕右上角）；
                   桌面档 .ys-imm__title-row 转 position:relative → 由桌面块的 .ys-imm__sheet 规则定位。
                   收藏 / 播放列表不在浮层里重复：收藏就是歌名行右侧那颗爱心，播放列表是右下角那颗圆钮。 -->
              <Transition name="ys-imm-fade">
                <div v-if="showMore" class="ys-imm__sheet">
                  <button class="ys-imm__sheet-item" @click="showMore = false; copyLyrics()">复制歌词</button>
                  <button class="ys-imm__sheet-item" :class="{ 'is-on': showTranslation }" @click="showTranslation = !showTranslation">双语歌词</button>
                  <button class="ys-imm__sheet-item" @click="lyricFontSize = Math.max(13, lyricFontSize - 2)">歌词字号 −</button>
                  <button class="ys-imm__sheet-item" @click="lyricFontSize = Math.min(28, lyricFontSize + 2)">歌词字号 +</button>
                </div>
              </Transition>
            </div>

            <p class="ys-imm__sub">
              {{ currentSong?.singer || '—' }}<span v-if="currentSong?.album"> · {{ currentSong.album }}</span>
            </p>
            <!-- 词曲行：手机档留在信息区；桌面档挪到右栏歌词块顶部（见下） -->
            <p v-if="creditLine" class="ys-imm__credit ys-imm__mob-b">{{ creditLine }}</p>
          </div>
        </div>

        <!-- 右栏（桌面档 grid 第 2 列，跨两行，垂直居中；手机档透明）：
             浮动歌词 —— 无卡片、无边框、左对齐，直接浮在氛围背景上 -->
        <div class="ys-imm__lyrics-col">
          <!-- 词曲行：桌面档在几何上属于歌词块首行（对齐参考截图） -->
          <p v-if="creditLine" class="ys-imm__credit ys-imm__credit--lead ys-imm__desk-b">{{ creditLine }}</p>

          <!-- 沉浸式歌词：纵向、居中、两端渐变消失，非当前行轻微模糊 -->
          <div class="ys-imm__lyrics">
            <div v-if="!parsedLyrics.length" class="ys-imm__lyrics-empty">
              <Music :stroke-width="1" class="ys-imm__empty-icon" />
              <p class="ys-imm__empty-text">享受动人旋律…</p>
            </div>
            <div v-else ref="lyricContainer" class="ys-imm__lyrics-scroll no-scrollbar">
              <div
                v-for="(line, idx) in parsedLyrics"
                :key="idx"
                :ref="el => setLineRef(el, idx)"
                class="ys-imm__line"
                :class="{ 'is-active': currentLineIndex === idx }"
                :style="lineStyle(idx)"
                @click="$emit('seek', line.time)"
              >
                <p class="ys-imm__line-text">{{ line.text }}</p>
                <p
                  v-if="showTranslation && line.trans"
                  class="ys-imm__line-trans"
                  :style="{ '--line-trans-size': Math.round((currentLineIndex === idx ? lyricFontSize + 3 : lyricFontSize) * 0.72) + 'px' }"
                >{{ line.trans }}</p>
              </div>
            </div>

            <!-- 桌面：轻推歌词（保留原功能，做得很淡） -->
            <div v-if="parsedLyrics.length" class="ys-imm__nudge ys-imm__only-sm">
              <button @click="nudgeLyrics(-1)" title="上一段歌词" aria-label="上一段歌词" class="ys-imm__nudge-btn">
                <ChevronUp :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
              </button>
              <button @click="nudgeLyrics(1)" title="下一段歌词" aria-label="下一段歌词" class="ys-imm__nudge-btn">
                <ChevronDown :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
              </button>
            </div>
          </div>

          <!-- 歌词右上角那两颗（桌面档）：**歌词提前 / 歌词延后**，不是切换歌曲。
               平时完全隐藏，鼠标移到歌词区域（.ys-imm__lyrics-col）才淡入（见样式表）。 -->
          <div v-if="parsedLyrics.length" class="ys-imm__offset ys-imm__desk-f">
            <button
              class="ys-imm__rail-btn"
              @click="adjustLyricOffset(LYRIC_OFFSET_STEP)"
              title="歌词提前（每点一次 0.5 秒）"
              aria-label="歌词提前"
            >
              <ChevronsLeft :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
            </button>
            <button
              class="ys-imm__rail-btn"
              @click="adjustLyricOffset(-LYRIC_OFFSET_STEP)"
              title="歌词延后（每点一次 0.5 秒）"
              aria-label="歌词延后"
            >
              <ChevronsRight :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
            </button>
            <button
              v-if="lyricOffset"
              class="ys-imm__offset-val"
              @click="resetLyricOffset"
              title="点一下归零"
            >歌词{{ lyricOffset > 0 ? '提前' : '延后' }} {{ Math.abs(lyricOffset).toFixed(1) }}s</button>
          </div>
        </div>
      </main>

      <!-- ⑤ 底部：极细进度条 + 小时间 + 主次分明的控制键 -->
      <footer class="ys-imm__bottom">
        <div class="ys-imm__progress" @click="handleSeek" title="点击跳转">
          <div ref="progressBarRef" class="ys-imm__track">
            <div class="ys-imm__fill" :style="{ width: progressPercent + '%' }"></div>
            <span class="ys-imm__thumb" :style="{ left: progressPercent + '%' }"></span>
          </div>
        </div>

        <div class="ys-imm__times">
          <span class="ys-imm__time">{{ formatTime(currentTime) }}</span>
          <span class="ys-imm__quality">{{ qualityLine }}</span>
          <span class="ys-imm__time">{{ formatTime(duration) }}</span>
        </div>

        <div class="ys-imm__controls">
          <!-- 辅助 · 播放模式 -->
          <button
            class="ys-imm__ctrl-btn"
            @click="cyclePlayMode"
            :title="playModeTitle"
            :aria-label="playModeTitle"
            :class="{ 'is-on': playMode !== 'sequence' }"
          >
            <Repeat1 v-if="playMode === 'loop'" :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--aux" />
            <Shuffle v-else-if="playMode === 'random'" :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--aux" />
            <Repeat v-else :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--aux" />
          </button>

          <button class="ys-imm__ctrl-btn" @click="$emit('prev')" title="上一首" aria-label="上一首">
            <SkipBack :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--main" />
          </button>

          <button
            class="ys-imm__play"
            @click="$emit('toggle-play')"
            :title="isPlaying ? '暂停' : '播放'"
            :aria-label="isPlaying ? '暂停' : '播放'"
          >
            <Pause v-if="isPlaying" :size="30" fill="currentColor" stroke="none" />
            <Play v-else :size="30" fill="currentColor" stroke="none" class="ys-imm__play-glyph" />
          </button>

          <button class="ys-imm__ctrl-btn" @click="$emit('next')" title="下一首" aria-label="下一首">
            <SkipForward :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--main" />
          </button>

          <!-- 辅助 · 音量
               手机档：只有静音键（与本项目原实现一致）。
               桌面档（v2.1.64 起）：音量键＝**开关**竖向音量条（3px 竖轨、玫红从底向上
                       填充、下方「NN%」），默认收起；点音量键展开，点别处 / Esc 收起。
                       静音改到滑条里那颗「NN%」上。轨道尺寸与配色仍照参考稿 528c8995 实测。 -->
          <div class="ys-imm__vol" :class="{ 'ys-imm__vol--open': showVol }">
            <button
              class="ys-imm__ctrl-btn ys-imm__vol-icon"
              @click="onVolIconClick"
              :title="volIconTitle"
              :aria-label="volIconTitle"
              :aria-expanded="desktopTier ? String(showVol) : undefined"
            >
              <VolumeX v-if="isMuted" :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--aux" />
              <Volume1 v-else-if="volume < 0.5" :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--aux" />
              <Volume2 v-else :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--aux" />
            </button>

            <div class="ys-imm__vol-stick" :class="{ 'ys-imm__vol-stick--open': showVol }">
              <div
                ref="volRailRef"
                class="ys-imm__vol-rail"
                @pointerdown="startVolDrag"
                :title="'音量 ' + volumePercent + '%'"
              >
                <div class="ys-imm__vol-fill" :style="{ height: volumePercent + '%' }"></div>
              </div>
              <button
                class="ys-imm__vol-num"
                @click="toggleMute"
                :title="isMuted ? '取消静音' : '静音'"
                :aria-label="isMuted ? '取消静音' : '静音'"
              >{{ volumePercent }}%</button>
            </div>
          </div>
        </div>
      </footer>

      <!-- ⑥ 右下角浮动圆钮（仅桌面档）：打开**播放列表抽屉**（从右向左划出）。
           参考稿里那颗 ≡ 就是这个；「更多」已经挪到歌名行那颗 ⋮ 上了。 -->
      <button
        class="ys-imm__fab ys-imm__desk-g"
        :class="{ 'is-on': showQueue }"
        @click="showQueue = !showQueue"
        title="播放列表"
        aria-label="播放列表"
      >
        <ListMusic :stroke-width="1.5" class="ys-imm__icon" />
      </button>

      <!-- ⑦ 播放列表抽屉：从右向左划出。头部三颗 = 循环 / 清空 / 关闭；
           下面是播放队列，当前那首高亮、封面角上压一个 ▶。 -->
      <Transition name="ys-imm-fade">
        <div v-if="showQueue" class="ys-imm__queue-mask" @click="showQueue = false"></div>
      </Transition>
      <Transition name="ys-imm-slide">
        <aside v-if="showQueue" class="ys-imm__queue" aria-label="播放列表">
          <header class="ys-imm__queue-top">
            <span class="ys-imm__queue-title">
              播放列表<span v-if="queueSongs.length" class="ys-imm__queue-num">{{ queueSongs.length }}</span>
            </span>
            <div class="ys-imm__queue-acts">
              <button
                class="ys-imm__queue-btn"
                @click="cyclePlayMode"
                :title="playModeTitle"
                :aria-label="playModeTitle"
              >
                <Repeat1 v-if="playMode === 'loop'" :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
                <Repeat v-else-if="playMode === 'sequence'" :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
                <Shuffle v-else :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
              </button>
              <button
                class="ys-imm__queue-btn"
                @click="$emit('clear-playlist')"
                title="清空播放列表"
                aria-label="清空播放列表"
              >
                <Trash2 :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
              </button>
              <button class="ys-imm__queue-btn" @click="showQueue = false" title="关闭" aria-label="关闭">
                <X :stroke-width="1.5" class="ys-imm__icon ys-imm__icon--sm" />
              </button>
            </div>
          </header>

          <div v-if="!queueSongs.length" class="ys-imm__queue-empty">播放列表是空的</div>
          <div v-else class="ys-imm__queue-list no-scrollbar">
            <button
              v-for="(song, idx) in queueSongs"
              :key="`${song.id ?? song.path ?? idx}-${idx}`"
              class="ys-imm__queue-row"
              :class="{ 'is-active': idx === currentIndex }"
              @click="$emit('select-track', idx)"
            >
              <span class="ys-imm__queue-cover">
                <img v-if="trackCover(song)" :src="trackCover(song)" alt="" loading="lazy" />
                <Music v-else :stroke-width="1" class="ys-imm__queue-cover-i" />
                <span v-if="idx === currentIndex" class="ys-imm__queue-badge" aria-hidden="true">
                  <Play :stroke-width="1" fill="currentColor" class="ys-imm__queue-badge-i" />
                </span>
              </span>
              <span class="ys-imm__queue-meta">
                <span class="ys-imm__queue-name">{{ song.name || song.title || '—' }}</span>
                <span class="ys-imm__queue-sub">{{ song.singer || song.artist || '—' }}</span>
              </span>
              <span class="ys-imm__queue-time">{{ song.duration ? formatTime(song.duration) : '' }}</span>
            </button>
          </div>
        </aside>
      </Transition>
    </div>

    <Transition name="ys-imm-fade">
      <div v-if="toast" class="ys-imm__toast">{{ toast }}</div>
    </Transition>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import {
  ChevronDown, ChevronUp, Music,
  Play, Pause, SkipBack, SkipForward, Shuffle, Repeat, Repeat1,
  Volume2, Volume1, VolumeX, MoreVertical,
  Heart, Maximize2, Minimize2, ChevronsLeft, ChevronsRight,
  ListMusic, Trash2, X
} from 'lucide-vue-next'
import { loadPref, savePref } from '../services/prefs'
import { describeError } from '../services/userMsg'
import { logError } from '../services/appLog'

const props = defineProps({
  currentSong: Object,
  isPlaying: Boolean,
  currentTime: Number,
  duration: Number,
  lyricData: String,
  transData: String,
  playlistCount: { type: Number, default: 0 },
  playMode: { type: String, default: 'sequence' },
  /* v2.1.64：桌面档右下角那颗圆钮打开的是**播放列表抽屉**，所以这里要拿到队列本身。
     ⚠️ 只读引用：本组件不复制队列，点行只 $emit('select-track', idx) 交给 App.vue，
        免得出现「抽屉里的顺序和真正播放的顺序」两份真相（App.vue 的 prev/next 走的就是它）。 */
  playlist: { type: Array, default: () => [] },
  currentIndex: { type: Number, default: 0 },
})

const emit = defineEmits([
  'collapse', 'toggle-play', 'prev', 'next', 'seek', 'volume-change', 'update:playMode',
  'select-track', 'clear-playlist'
])

const lyricFontSize = ref(18)
const showTranslation = ref(true)
const volume = ref(0.8)
const isMuted = ref(false)
const prevVolume = ref(0.8)

/* ── 竖向音量条（v2.1.64 桌面档，对齐参考稿 528c8995 裁切）──────────────────
   3px 竖轨 + 玫红从**底向上**填充 + 下方「NN%」。
   拖动按 clientY 反算（进度条是 clientX 正算，方向反过来：越靠上越响）。 */
const volumePercent = computed(() => Math.round(volume.value * 100))
const volRailRef = ref(null)

function applyVolumeFromEvent(e) {
  const el = volRailRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  if (!rect.height) return
  // 越靠底越响；volume 的 watch 负责 emit('volume-change') 并解除静音
  volume.value = Math.min(1, Math.max(0, 1 - (e.clientY - rect.top) / rect.height))
}
function stopVolDrag() {
  window.removeEventListener('pointermove', applyVolumeFromEvent)
}
function startVolDrag(e) {
  e.preventDefault()
  applyVolumeFromEvent(e)
  window.addEventListener('pointermove', applyVolumeFromEvent)
  window.addEventListener('pointerup', stopVolDrag, { once: true })
  window.addEventListener('pointercancel', stopVolDrag, { once: true })
}
const playModes = ['sequence', 'random', 'loop']
const lyricContainer = ref(null)
const progressBarRef = ref(null)
const lineRefs = ref([])
const showQueue = ref(false)

/* 歌词偏移（秒）：>0 = 歌词提前（比音频早一点亮），<0 = 歌词延后。
   对齐参考截图里歌词区右上角那两颗（平时隐藏、hover 歌词区才出现）——不是切歌。
   存在 localStorage（和字号、收藏同一个 prefs 服务），换歌不重置。 */
const LYRIC_OFFSET_STEP = 0.5
const LYRIC_OFFSET_MAX = 10
const lyricOffset = ref(Number(loadPref('lyric_offset', 0)) || 0)

/** 队列（只读）：抽屉里那一列播放列表。 */
const queueSongs = computed(() => props.playlist || [])

/** 队列行封面：搜索/在线曲目带 cover，NAS 扫描出来的带 cover_url。 */
function trackCover(song) {
  return song?.cover || song?.cover_url || ''
}

function setLineRef(el, idx) { if (el) lineRefs.value[idx] = el }

/* ══════════════════════════════════════════════════════════════════════════
   动态环境色（v2.1.63 重做：对齐参考播放器的「氛围背景 + 漂移光球」实测样式）。
   封面 → 双主色（主色 + 与主色隔开的次色）→ 两层交叉淡入的氛围渐变 + 4 颗漂移光球。
   纯视觉层：采样失败（跨域 / 图挂了）就退回参考站那套中性暗紫，不报错、不打扰播放。
   ══════════════════════════════════════════════════════════════════════════ */
const NEUTRAL_TINT = 'rgb(58, 38, 56)'
// 兜底氛围：参考站蓝本（左上粉紫 + 右上次紫 + 132° 暗紫基底），取不到封面主色时用
const NEUTRAL_AMBIENT =
  'radial-gradient(120% 90% at 12% 6%, rgba(219, 82, 161, 0.5) 0%, rgba(219, 82, 161, 0.17) 38%, rgba(0, 0, 0, 0) 66%),'
  + 'radial-gradient(120% 90% at 88% 10%, rgba(153, 107, 186, 0.44) 0%, rgba(153, 107, 186, 0.15) 40%, rgba(0, 0, 0, 0) 68%),'
  + 'linear-gradient(132deg, rgb(74, 31, 59) 0%, rgb(43, 31, 56) 48%, rgb(59, 41, 74) 100%)'
// 光球基色兜底（'r, g, b' 三元组，配合 CSS 的 rgba(var(--orb-x), α) 用）
const NEUTRAL_ORBS = { a: '226, 88, 166', b: '153, 107, 186', c: '120, 78, 168', p: '226, 88, 166' }

const rootEl = ref(null)
// v2.1.66 性能：页面不可见 / 窗口失焦时，把无限动画（光球、品牌呼吸）全部停掉。
// 场景来源：应用在飞牛宿主里被当成 iframe 嵌着，用户拖动分栏时这些动画照跑，父页面每帧都要跟它合成。
const pageIdle = ref(false)
function setPageIdle(v) {
  if (pageIdle.value === v) return
  pageIdle.value = v
  // 品牌呼吸在 style.css 里（不受本组件 scoped 样式管辖），用 <html> 上的 class 联动
  if (typeof document !== 'undefined' && document.documentElement) {
    document.documentElement.classList.toggle('ys-app-idle', v)
  }
}
function onDocVisibilityChange() { setPageIdle(document.hidden === true) }
function onWindowBlur() { setPageIdle(true) }
function onWindowFocus() { setPageIdle(false) }
const ambientA = ref(NEUTRAL_AMBIENT)   // 首次进入就先铺一层中性氛围，采到色再交叉淡入换成封面色
const ambientB = ref('')
const ambientFront = ref('a')
const rootStyle = ref({
  '--imm-halo': NEUTRAL_TINT,
  '--orb-a': NEUTRAL_ORBS.a,
  '--orb-b': NEUTRAL_ORBS.b,
  '--orb-c': NEUTRAL_ORBS.c,
  '--orb-p': NEUTRAL_ORBS.p,
})
let sampleToken = 0

/**
 * 档位（v2.1.64）：从「只靠媒体查询」改成「JS 决定根节点上的类」。
 * 根节点挂 .is-tier-desktop / .is-tier-mobile，两套版式的差异全看这个类；
 * 好处是电脑浏览器也能直接看手机档 —— 加 ?tier=mobile 锁档（记进 localStorage）、?tier=auto 解锁。
 * ⚠️ 锁档只用于「看版式」：真机判档仍然靠 (max-width:767px) and (hover:none) and (pointer:coarse)。
 */
const TIER_KEY = 'imm_tier'
function readTierOverride() {
  try {
    const q = new URLSearchParams(window.location.search).get('tier')
    if (q === 'mobile' || q === 'desktop') { localStorage.setItem(TIER_KEY, q); return q }
    if (q === 'auto') { localStorage.removeItem(TIER_KEY); return '' }
    const saved = localStorage.getItem(TIER_KEY)
    return saved === 'mobile' || saved === 'desktop' ? saved : ''
  } catch (e) { return '' }
}
const tierOverride = ref(typeof window === 'undefined' ? '' : readTierOverride())
const phoneMq = (typeof window !== 'undefined' && typeof window.matchMedia === 'function')
  ? window.matchMedia('(max-width: 767px) and (hover: none) and (pointer: coarse)')
  : null
const phoneDetected = ref(phoneMq ? phoneMq.matches : false)
/** 手机档判定：v2.1.62 口径「除手机窗口都是桌面档」（锁档时听锁档的）。 */
const mobileTier = computed(() => (tierOverride.value ? tierOverride.value === 'mobile' : phoneDetected.value))
/** 桌面档：光球、视差、两栏栅格这类重活都挂在这一档上。 */
const desktopTier = computed(() => !mobileTier.value)
function syncTier() { if (phoneMq) phoneDetected.value = phoneMq.matches }
function onTierMqChange() { if (!tierOverride.value) syncTier() }

/**
 * 把新的氛围写进「背面」那一层再翻上来：两层 opacity 交叉淡入淡出 → 换歌是渐变不是跳变。
 * 光球颜色跟着换（不插值，跳变被氛围层的渐变盖住，观感可接受；换来的是最大的浏览器兼容性）。
 */
function pushAmbient(css, orbs, halo) {
  if (ambientFront.value === 'a') { ambientB.value = css; ambientFront.value = 'b' }
  else { ambientA.value = css; ambientFront.value = 'a' }
  rootStyle.value = {
    '--imm-halo': halo,
    '--orb-a': orbs.a,
    '--orb-b': orbs.b,
    '--orb-c': orbs.c,
    '--orb-p': orbs.p,
  }
}

function rgbToHsl(r, g, b) {
  r /= 255; g /= 255; b /= 255
  const max = Math.max(r, g, b)
  const min = Math.min(r, g, b)
  const l = (max + min) / 2
  let h = 0
  let s = 0
  if (max !== min) {
    const d = max - min
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
    if (max === r) h = ((g - b) / d + (g < b ? 6 : 0))
    else if (max === g) h = (b - r) / d + 2
    else h = (r - g) / d + 4
    h /= 6
  }
  return [h, s, l]
}

function hslToRgb(h, s, l) {
  if (s === 0) {
    const v = Math.round(l * 255)
    return [v, v, v]
  }
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s
  const p = 2 * l - q
  const hue2rgb = (t) => {
    if (t < 0) t += 1
    if (t > 1) t -= 1
    if (t < 1 / 6) return p + (q - p) * 6 * t
    if (t < 1 / 2) return q
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6
    return p
  }
  return [
    Math.round(hue2rgb(h + 1 / 3) * 255),
    Math.round(hue2rgb(h) * 255),
    Math.round(hue2rgb(h - 1 / 3) * 255),
  ]
}

/**
 * 取调色板：12 个色相桶（每桶 30°）加权累计 —— 权重偏饱和、偏亮，避免灰边把颜色拉成死灰。
 * 主桶 = 主色；与主桶隔开 ≥2 桶的最大桶 = 次色（隔开是为了不让相邻色相的噪点当次色）。
 */
function analyzePalette(data) {
  const bins = new Array(12).fill(0)
  let r = 0, g = 0, b = 0, w = 0
  for (let i = 0; i < data.length; i += 4) {
    if (data[i + 3] < 128) continue
    const rr = data[i], gg = data[i + 1], bb = data[i + 2]
    const max = Math.max(rr, gg, bb)
    const min = Math.min(rr, gg, bb)
    const sat = max === 0 ? 0 : (max - min) / max
    const weight = 0.25 + sat * (max / 255) * 1.6
    r += rr * weight; g += gg * weight; b += bb * weight; w += weight
    const [h] = rgbToHsl(rr, gg, bb)
    bins[Math.min(11, Math.floor(h * 12))] += weight
  }
  if (!w) return null
  let bi1 = 0
  for (let i = 1; i < 12; i++) if (bins[i] > bins[bi1]) bi1 = i
  let bi2 = -1
  let best = 0
  for (let i = 0; i < 12; i++) {
    if (i === bi1) continue
    const dist = Math.min(Math.abs(i - bi1), 12 - Math.abs(i - bi1))
    if (dist < 2) continue
    if (bins[i] > best) { best = bins[i]; bi2 = i }
  }
  // 次色太弱（几乎是单色封面）→ 取主色对侧，保证氛围层有层次而不是一坨同色
  if (bi2 < 0 || best < w * 0.05) bi2 = (bi1 + 4) % 12
  const [h1, s1] = rgbToHsl(r / w, g / w, b / w)
  return {
    h1,
    s1: Math.min(0.62, Math.max(0.3, s1 * 1.25)),   // 灰封面也得有一点点颜色，不然氛围层是死的
    h2: (bi2 + 0.5) / 12,
  }
}

const cssRgb = (c, a) => `rgba(${c[0]}, ${c[1]}, ${c[2]}, ${a})`
const cssTriplet = (c) => `${c[0]}, ${c[1]}, ${c[2]}`

/**
 * 由双主色生成整套氛围（层叠顺序与数值对齐参考播放器实测）：
 *   氛围渐变 = 左上主色 radial(0.7 → 38% 0.24 → 66% 透明)
 *            + 右上次色 radial(0.6 → 40% 0.2 → 68% 透明)
 *            + 132° 暗色基底（双主色各自压暗，保证画面暗、文字始终可读）
 *   光球色   = 主色 / 次色 / 主色+0.11 色相（那颗偏紫的第三球）/ 主色（pulse）
 */
function buildAmbient(p) {
  const c1 = hslToRgb(p.h1, p.s1, 0.58)
  const c2 = hslToRgb(p.h2, Math.min(0.58, Math.max(0.28, p.s1 * 0.85)), 0.56)
  const c3 = hslToRgb((p.h1 + 0.11) % 1, Math.min(0.6, Math.max(0.3, p.s1 * 0.9)), 0.55)
  const halo = hslToRgb(p.h1, Math.min(0.85, p.s1 * 1.4), 0.44)
  const b1 = hslToRgb(p.h1, p.s1 * 0.55, 0.16)
  const b2 = hslToRgb(p.h1, p.s1 * 0.45, 0.11)
  const b3 = hslToRgb(p.h2, p.s1 * 0.5, 0.14)
  return {
    halo: `rgb(${halo.join(', ')})`,
    ambient:
      `radial-gradient(120% 90% at 12% 6%, ${cssRgb(c1, 0.7)} 0%, ${cssRgb(c1, 0.24)} 38%, rgba(0, 0, 0, 0) 66%),`
      + `radial-gradient(120% 90% at 88% 10%, ${cssRgb(c2, 0.6)} 0%, ${cssRgb(c2, 0.2)} 40%, rgba(0, 0, 0, 0) 68%),`
      + `linear-gradient(132deg, rgb(${b1.join(', ')}) 0%, rgb(${b2.join(', ')}) 48%, rgb(${b3.join(', ')}) 100%)`,
    orbs: {
      a: cssTriplet(c1),
      b: cssTriplet(c2),
      c: cssTriplet(c3),
      p: cssTriplet(c1),
    },
  }
}

/** 取色的图必须能被 canvas 读：跨域图只有在 CDN 回了 ACAO 时才行 */
function loadForSample(src, useCors) {
  return new Promise((resolve) => {
    const img = new Image()
    if (useCors) img.crossOrigin = 'anonymous'
    img.referrerPolicy = 'no-referrer'
    img.onload = () => resolve(img)
    img.onerror = () => resolve(null)
    img.src = src
  })
}

/** 把图缩到 24×24 再取调色板；画布被污染（跨域无 ACAO）时返回 null */
function pickPixels(img) {
  try {
    const size = 24
    const cv = document.createElement('canvas')
    cv.width = size
    cv.height = size
    const ctx = cv.getContext('2d', { willReadFrequently: true })
    ctx.drawImage(img, 0, 0, size, size)
    return analyzePalette(ctx.getImageData(0, 0, size, size).data)
  } catch (e) {
    return null
  }
}

function isSameOrigin(url) {
  try { return new URL(url, location.href).origin === location.origin }
  catch (e) { return true }
}

/**
 * 采样链路（三级兜底，全程静默、绝不影响页面上那张封面图）：
 *   ① 直连 + crossOrigin：CDN 给 ACAO 就零额外开销（酷狗这类）
 *   ② 同源代理 `/api/player/stream?url=…`：QQ 音乐 / 网易云这类 CDN **会拒绝带 Origin 的请求**
 *      （实测 y.gtimg.cn：带 crossOrigin 直接 load error，不带则 canvas 被污染）——
 *      走本机后端把同一张图取回来就变成同源，canvas 可读（实测可取到主色）。
 *   ③ 都失败 → 中性深色底，界面照常。
 */
async function sampleAmbient(url) {
  const neutral = () => pushAmbient(NEUTRAL_AMBIENT, NEUTRAL_ORBS, NEUTRAL_TINT)
  if (!url) { neutral(); return }
  const token = ++sampleToken
  const commit = (palette) => {
    if (token !== sampleToken || !palette) return false
    const built = buildAmbient(palette)
    pushAmbient(built.ambient, built.orbs, built.halo)
    return true
  }

  const direct = await loadForSample(url, true)
  if (token !== sampleToken) return
  if (direct && commit(pickPixels(direct))) return

  if (!isSameOrigin(url)) {
    const viaProxy = await loadForSample('/api/player/stream?url=' + encodeURIComponent(url), false)
    if (token !== sampleToken) return
    if (viaProxy && commit(pickPixels(viaProxy))) return
  }

  if (token === sampleToken) neutral()
}

watch(() => props.currentSong?.cover, (url) => sampleAmbient(url), { immediate: true })

/* ── 歌词解析（逻辑与原实现完全一致） ── */
const parsedLyrics = computed(() => {
  if (!props.lyricData) return []
  let offsetSec = 0
  const offsetMatch = props.lyricData.match(/\[offset:\s*(-?\d+)\]/i)
  if (offsetMatch) {
    offsetSec = parseInt(offsetMatch[1], 10) / 1000
  }

  const transMap = new Map()
  if (props.transData) {
    for (const l of props.transData.split('\n')) {
      const tagRegex = /\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]/g
      let match
      const text = l.replace(/\[\d{1,2}:\d{2}(?:\.\d{1,3})?\]/g, '').trim()
      while ((match = tagRegex.exec(l)) !== null) {
        const min = parseInt(match[1], 10)
        const sec = parseInt(match[2], 10)
        const msStr = (match[3] || '0').padEnd(3, '0').slice(0, 3)
        const totalSec = min * 60 + sec + parseInt(msStr, 10) / 1000 - offsetSec
        transMap.set(totalSec.toFixed(1), text)
      }
    }
  }

  const result = []
  for (const l of props.lyricData.split('\n')) {
    if (/^\[offset:/i.test(l) || /^\[(ti|ar|al|by|hash):/i.test(l)) continue
    const tagRegex = /\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]/g
    let match
    const text = l.replace(/\[\d{1,2}:\d{2}(?:\.\d{1,3})?\]/g, '').trim()
    if (!text) continue
    while ((match = tagRegex.exec(l)) !== null) {
      const min = parseInt(match[1], 10)
      const sec = parseInt(match[2], 10)
      const msStr = (match[3] || '0').padEnd(3, '0').slice(0, 3)
      const totalSec = Math.max(0, min * 60 + sec + parseInt(msStr, 10) / 1000 - offsetSec)
      result.push({
        time: totalSec,
        text,
        trans: transMap.get(totalSec.toFixed(1)) || ''
      })
    }
  }
  return result.sort((a, b) => a.time - b.time)
})

const currentLineIndex = computed(() => {
  if (!parsedLyrics.value.length) return -1
  // 歌词偏移：+lyricOffset = 提前（同一时刻的音频点亮更靠后的那一行）
  const t = props.currentTime + lyricOffset.value
  for (let i = parsedLyrics.value.length - 1; i >= 0; i--) {
    if (t >= parsedLyrics.value[i].time) return i
  }
  return 0
})

function prefersReducedMotion() {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
}

watch(currentLineIndex, (idx) => {
  const el = lineRefs.value[idx]
  if (!el) return
  // spec 要求：当前行平滑滚到中间
  el.scrollIntoView({ behavior: prefersReducedMotion() ? 'auto' : 'smooth', block: 'center' })
})

/**
 * 视口尺寸变了（手机横竖屏、桌面窗口拉伸）之后，歌词框高度跟着变，
 * 但滚动位置是浏览器按旧高度留着的 —— 当前行会跑到可视区外，要等到下一句才回来。
 * 所以尺寸稳定后**重新居中一次**（instant，不带动画）。
 */
let recenterRaf = 0
function recenterActiveLine() {
  if (typeof cancelAnimationFrame === 'function') cancelAnimationFrame(recenterRaf)
  recenterRaf = requestAnimationFrame(() => {
    const el = lineRefs.value[currentLineIndex.value]
    if (el) el.scrollIntoView({ block: 'center', behavior: 'auto' })
  })
}

/** 窗口尺寸变了：先同步档位（媒体查询 change 偶尔不触发），再重新居中当前歌词行。 */
function onWindowResize() {
  syncTier()
  recenterActiveLine()
}

let parallaxOn = false
let parallaxRaf = 0

/**
 * 指针视差（v2.1.63 新增）：鼠标在窗口里移动时，背景层跟着轻微位移（±1.4%），
 * 让「氛围」有一点深度。rAF 节流，JS 只写 CSS 变量、不碰布局。
 * 手机档不挂监听、CSS 里也不消费这两个变量 → 这个效果对手机端完全不存在。
 */
function onOrbParallax(e) {
  if (parallaxRaf) return
  parallaxRaf = requestAnimationFrame(() => {
    parallaxRaf = 0
    const el = rootEl.value
    if (!el) return
    const w = window.innerWidth || 1
    const h = window.innerHeight || 1
    el.style.setProperty('--par-x', ((e.clientX / w - 0.5) * 2).toFixed(3))
    el.style.setProperty('--par-y', ((e.clientY / h - 0.5) * 2).toFixed(3))
  })
}

onMounted(() => {
  window.addEventListener('resize', onWindowResize)
  window.addEventListener('orientationchange', recenterActiveLine)
  // 档位：设备指针能力变化（转屏、插上鼠标）时同步一次
  if (phoneMq) {
    if (phoneMq.addEventListener) phoneMq.addEventListener('change', onTierMqChange)
    else if (phoneMq.addListener) phoneMq.addListener(onTierMqChange)
  }
  // 全屏状态回写（Esc / F11 退出也要把按钮图标换回来）
  document.addEventListener('fullscreenchange', onFullscreenChange)
  document.addEventListener('webkitfullscreenchange', onFullscreenChange)
  // 浮层收起：点空白处（capture 先看） / Esc
  document.addEventListener('pointerdown', onDocPointerDown, true)
  document.addEventListener('keydown', onDocKeydown)
  // v2.1.66：不可见 / 失焦 → 停掉无限动画（iframe 被盖住或用户去拖父页面时省合成器）
  document.addEventListener('visibilitychange', onDocVisibilityChange)
  window.addEventListener('blur', onWindowBlur)
  window.addEventListener('focus', onWindowFocus)
  setPageIdle(document.hidden === true)
  // 桌面档才挂视差：悬停设备 + 没开「减少动效」（手机档不挂 → 布局与性能零影响）
  if (rootEl.value && typeof matchMedia === 'function'
    && matchMedia('(hover: hover) and (pointer: fine)').matches
    && !prefersReducedMotion()) {
    rootEl.value.addEventListener('pointermove', onOrbParallax)
    parallaxOn = true
  }
  // 锁档时提示一下：不然「版式怎么不一样」会被当成改坏了
  if (tierOverride.value) {
    showToast(`档位已锁定 · ${mobileTier.value ? '手机档' : '桌面档'}`)
  }
})
onUnmounted(() => {
  window.removeEventListener('resize', onWindowResize)
  window.removeEventListener('orientationchange', recenterActiveLine)
  if (phoneMq) {
    if (phoneMq.removeEventListener) phoneMq.removeEventListener('change', onTierMqChange)
    else if (phoneMq.removeListener) phoneMq.removeListener(onTierMqChange)
  }
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  document.removeEventListener('webkitfullscreenchange', onFullscreenChange)
  document.removeEventListener('pointerdown', onDocPointerDown, true)
  document.removeEventListener('keydown', onDocKeydown)
  document.removeEventListener('visibilitychange', onDocVisibilityChange)
  window.removeEventListener('blur', onWindowBlur)
  window.removeEventListener('focus', onWindowFocus)
  setPageIdle(false)   // 卸载时清掉 <html> 上的 class，别把品牌呼吸留在暂停态
  stopVolDrag()
  if (parallaxOn && rootEl.value) rootEl.value.removeEventListener('pointermove', onOrbParallax)
  if (typeof cancelAnimationFrame === 'function') {
    cancelAnimationFrame(recenterRaf)
    if (parallaxRaf) cancelAnimationFrame(parallaxRaf)
  }
})

/**
 * 歌词强调（v2.1.63 靠齐参考播放器的「卡拉OK」对比度）：当前行 1.0 纯白 + 字号 +5；
 * 上下相邻 0.4 / 0.27，更远 0.18 / 0.1 —— 强调靠「放大 + 透明度对比」，不做逐字染色。
 * 非当前行叠一点点模糊，让歌词融进背景而不是变成一块黑色歌词窗。
 * ⚠️ 字号必须在这里一起给 —— A− / A+ 靠它生效（漏过一次，按钮直接失效）。
 */
function lineStyle(idx) {
  const cur = currentLineIndex.value
  // 这里只给「基准字号」，最终 px 交给 CSS 算：桌面档乘 --imm-lyric-scale 放大
  // （手机档系数为 1 → 渲染结果与改动前逐像素一致）。A− / A+ 调的就是这个基准值。
  const size = (idx === cur ? lyricFontSize.value + 5 : lyricFontSize.value) + 'px'
  const font = { '--line-size': size }
  if (cur < 0) return { ...font, opacity: 0.35 }
  const d = Math.abs(idx - cur)
  if (d === 0) return { ...font, opacity: 1, filter: 'none' }
  if (d === 1) return { ...font, opacity: 0.4, filter: 'blur(0.3px)' }
  if (d === 2) return { ...font, opacity: 0.27, filter: 'blur(0.6px)' }
  if (d === 3) return { ...font, opacity: 0.18, filter: 'blur(0.9px)' }
  return { ...font, opacity: 0.1 }
}

const progressPercent = computed(() => !props.duration ? 0 : Math.min(100, (props.currentTime / props.duration) * 100))

/**
 * 当前这一行歌词自己的进度（0~1）：LRC 只有行级时间，没有逐字时间，
 * 所以用「本行到下一行」的区间来推。
 */
function lineProgress(idx) {
  const lines = parsedLyrics.value
  if (!lines.length || idx !== currentLineIndex.value) return 0
  const start = lines[idx].time
  const end = lines[idx + 1] ? lines[idx + 1].time : start + 6
  const span = Math.max(0.6, end - start)
  return Math.max(0, Math.min(1, (props.currentTime - start) / span))
}

function formatTime(sec) {
  if (!sec || isNaN(sec)) return '00:00'
  return Math.floor(sec / 60).toString().padStart(2, '0') + ':' + Math.floor(sec % 60).toString().padStart(2, '0')
}

function handleSeek(e) {
  if (!progressBarRef.value || !props.duration) return
  const rect = progressBarRef.value.getBoundingClientRect()
  emit('seek', Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width)) * props.duration)
}

/** 歌名下面那行小字：从歌词里抽「词：xxx / 曲：xxx」（LRC 头几行常带） */
const creditLine = computed(() => {
  const out = []
  for (const line of parsedLyrics.value) {
    const m = /^(作词|作曲|词|曲)\s*[:：]\s*(.+)$/.exec(line.text || '')
    if (!m) continue
    const key = m[1] === '作词' || m[1] === '词' ? '词' : '曲'
    const val = m[2].trim()
    if (val && !out.some((x) => x.startsWith(key + '：'))) out.push(key + '：' + val)
  }
  return out.join(' / ')
})

/** 音质行：原文件 · MP3 · 320K（字段缺了就自动少一段，不留空点） */
const qualityLine = computed(() => ['原文件', props.currentSong?.format, props.currentSong?.quality].filter(Boolean).join(' · '))

// 收藏：目前后端没有单曲收藏接口，这里做**本地收藏**（存 prefs，切换时给个提示），不假装同步到服务器。
const favorites = ref(loadPref('favorite_songs', []) || [])
const favoriteKey = computed(() => {
  const s = props.currentSong
  return s ? [s.name, s.singer].filter(Boolean).join(' - ') : ''
})
const isFavorite = computed(() => !!favoriteKey.value && favorites.value.includes(favoriteKey.value))
function toggleFavorite() {
  if (!favoriteKey.value) return
  favorites.value = isFavorite.value
    ? favorites.value.filter((f) => f !== favoriteKey.value)
    : [...favorites.value, favoriteKey.value]
  savePref('favorite_songs', favorites.value)
  showToast(isFavorite.value ? '已收藏（本机）' : '已取消收藏')
}

/** 手机端的「更多」展开面板 */
const showMore = ref(false)

/** 桌面档的竖向音量条：默认收起，点音量键才展开（v2.1.64 起；在此之前是一直挂着的） */
const showVol = ref(false)

/** 桌面上下箭头：轻推歌词滚动一段（约一屏的 1/3） */
function nudgeLyrics(dir) {
  const c = lyricContainer.value
  if (!c) return
  c.scrollBy({ top: dir * Math.max(80, Math.round(c.clientHeight / 3)), behavior: 'smooth' })
}

/**
 * 歌词提前 / 延后：delta 秒，正数 = 提前（歌词比音频早亮），负数 = 延后。
 * 参考截图里歌词区右上角那两颗就是干这个的（hover 才现身）——**不是切歌**，
 * 也会写进 prefs，下一首/下次进来还是同一个偏移。
 */
function adjustLyricOffset(delta) {
  const next = Math.max(-LYRIC_OFFSET_MAX, Math.min(LYRIC_OFFSET_MAX, +(lyricOffset.value + delta).toFixed(1)))
  if (next === lyricOffset.value) {
    showToast(delta > 0 ? `歌词已提前到上限 ${LYRIC_OFFSET_MAX}s` : `歌词已延后到上限 ${LYRIC_OFFSET_MAX}s`)
    return
  }
  lyricOffset.value = next
  savePref('lyric_offset', next)
  showToast(next === 0
    ? '歌词偏移已归零'
    : `歌词${next > 0 ? '提前' : '延后'} ${Math.abs(next).toFixed(1)}s`)
}

/** 歌词偏移归零（点一下徽标） */
function resetLyricOffset() {
  if (!lyricOffset.value) return
  lyricOffset.value = 0
  savePref('lyric_offset', 0)
  showToast('歌词偏移已归零')
}

/** 打开播放列表：桌面档是右滑抽屉；手机档还没有抽屉，退回一句提示（不假装有）。 */
function openQueue() {
  if (desktopTier.value) { showQueue.value = true; return }
  showToast(`播放列表共 ${props.playlistCount} 首`)
}

/* 浮层的「点其它地方就收起」（v2.1.64）：以前只能再点一次那颗 ⋮ 才关得掉，
   浮层挂在屏幕右侧时很容易忘了它在开着。capture 阶段先看一眼：
   点在浮层里、或点在两颗「更多」按钮上 → 放行给各自的 click 处理，其余一律收起。 */
function onDocPointerDown(e) {
  const t = e.target
  const inVol = !!(t && typeof t.closest === 'function' && t.closest('.ys-imm__vol'))
  if (showVol.value && !inVol) showVol.value = false      // 音量条：点在它之外就收起（点音量键本身不算）
  if (!showMore.value) return
  if (t && typeof t.closest === 'function'
    && (t.closest('.ys-imm__sheet') || t.closest('.ys-imm__meta-btn') || t.closest('.ys-imm__icon-btn'))) return
  showMore.value = false
}

/** Esc 逐层收起：先收「更多」浮层，再收音量条，最后收播放列表抽屉（不影响 App 里的其他 Esc 行为）。 */
function onDocKeydown(e) {
  if (e.key !== 'Escape') return
  if (showMore.value) { showMore.value = false; return }
  if (showVol.value) { showVol.value = false; return }
  if (showQueue.value) showQueue.value = false
}

// ⚠️ playMode 是 prop，脚本里必须走 props.xxx（模板里才能直接写 playMode）
const playModeTitle = computed(() => ({ sequence: '顺序播放', random: '随机播放', loop: '单曲循环' }[props.playMode] || '播放模式'))

function cyclePlayMode() {
  const nextIdx = (playModes.indexOf(props.playMode) + 1) % playModes.length
  emit('update:playMode', playModes[nextIdx])
}

/** 音量键：桌面档＝**开关竖向音量条**；手机档＝静音切换（手机档没有竖条，与原来一致）。 */
function onVolIconClick() {
  if (desktopTier.value) { showVol.value = !showVol.value; return }
  toggleMute()
}

const volIconTitle = computed(() => {
  if (desktopTier.value) return showVol.value ? '收起音量条' : '展开音量条'
  return isMuted.value ? '取消静音' : '静音'
})

function toggleMute() {
  if (isMuted.value) { isMuted.value = false; volume.value = prevVolume.value || 0.8 }
  else { prevVolume.value = volume.value; volume.value = 0; isMuted.value = true }
}

watch(volume, (val) => { emit('volume-change', parseFloat(val)); if (val > 0) isMuted.value = false })

const toast = ref('')
let toastTimer = null
function showToast(msg) {
  toast.value = msg
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toast.value = '' }, 2200)
}

/* ── 全屏（v2.1.64 桌面档右上角那颗圆角方形按钮；手机档不渲染）──────────────
   用浏览器原生 Fullscreen API，不引第三方依赖。三个坑都堵上：
   ① iOS Safari 不支持对普通元素 requestFullscreen（只有 video 能全屏）→ 探测不到就提示一次，
      播放照旧，绝不因为「按钮不好使」抛异常；
   ② 缺用户手势 / 上层容器没给 allowfullscreen 时 promise 会 reject → 捕获并提示；
   ③ 用户自己按 Esc / F11 退出时不会有我们的 click → 必须监听 fullscreenchange 回写图标状态，
      否则按钮上的图标会一直停在「退出全屏」。 */
const isFullscreen = ref(false)
function onFullscreenChange() { isFullscreen.value = !!document.fullscreenElement }
function toggleFullscreen() {
  if (document.fullscreenElement) {
    const exit = document.exitFullscreen || document.webkitExitFullscreen
    if (exit) {
      const p = exit.call(document)
      if (p && p.catch) p.catch(() => {})
    }
    return
  }
  const el = rootEl.value || document.documentElement
  const req = el.requestFullscreen || el.webkitRequestFullscreen
  if (!req) { showToast('这台设备不支持网页全屏'); return }
  const p = req.call(el)
  if (p && p.catch) {
    p.catch((e) => {
      logError('播放页全屏', '请求全屏失败', describeError(e))
      showToast('全屏被浏览器拦下了')
    })
  }
}

function copyLyrics() {
  if (!props.lyricData) { showToast('这首歌还没有歌词'); return }
  const text = parsedLyrics.value.map(l => l.text).join('\n')
  const done = () => showToast('歌词已复制')
  const fail = (e) => {
    logError('复制歌词', '写入剪贴板失败', describeError(e))
    showToast('复制失败 —— 浏览器可能没给剪贴板权限')
  }

  // ⚠️ 本应用是 http + 局域网 IP 部署 → **不是安全上下文**，navigator.clipboard 直接是 undefined。
  // 原实现无条件调 .writeText，会同步抛 TypeError（按钮点了没反应、控制台一个红字），
  // 所以这里必须先判存在，再退回老办法 execCommand。
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done).catch(fail)
    return
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    ta.setSelectionRange(0, ta.value.length)
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    if (ok) done()
    else fail(new Error('execCommand("copy") 返回 false'))
  } catch (e) {
    fail(e)
  }
}
</script>

<style scoped>
/* ══════════════════════════════════════════════════════════════════════════
   视觉层（scoped）。所有尺度集中在 .ys-imm 的变量上，断点只改变量，不改结构。
   ══════════════════════════════════════════════════════════════════════════ */
.ys-imm {
  --imm-glass-bg: rgba(255, 255, 255, 0.05);
  --imm-glass-bg-strong: rgba(255, 255, 255, 0.1);
  --imm-glass-line: rgba(255, 255, 255, 0.08);
  --imm-blur: blur(20px) saturate(140%);
  --imm-radius: 20px;
  --imm-col: 100%;                /* 手机：撑满容器，左右留白交给 --imm-gutter */
  --imm-gutter: 22px;
  --imm-cover: min(78vw, 380px, 42dvh);   /* 同时受屏宽与屏高约束，矮屏自动缩小 */
  --imm-title: 22px;
  --imm-sub: 13.5px;
  --imm-credit: 11.5px;
  --imm-lyric: 17px;
  --imm-lyric-scale: 1;           /* 桌面档放大歌词（spec：桌面不是手机的等比放大），手机为 1 */
  /* 光球基色（'r, g, b' 三元组，配合 rgba(var(--orb-x), α)）：JS 按封面双主色覆写，
     这里是采不到色时的兜底（参考站那套粉紫）。 */
  --orb-a: 226, 88, 166;
  --orb-b: 153, 107, 186;
  --orb-c: 120, 78, 168;
  --orb-p: 226, 88, 166;

  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  overflow: clip;                 /* ⚠️ clip 不是滚动容器：歌词 scrollIntoView 不会把整页顶上去（hidden 会） */
  color: #fff;
  user-select: none;
  -webkit-tap-highlight-color: transparent;
  background-color: #0a0a0c;
}

/* ── 背景（v2.1.63：氛围渐变 + 漂移光球，对齐参考播放器实测样式） ─────────
   底色垫底 → 两层 .ys-imm__ambient 交叉淡入换色 → 4 颗光球（仅桌面档渲染）
   → 竖版压暗渐变。背景只给颜色和氛围，看不清封面细节。 */
.ys-imm__bg {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
  background-color: #0f0f0f;
}

/* 氛围渐变：左上主色 + 右上次色 + 132° 暗色基底（JS 按封面双主色生成，这里只兜初始值） */
.ys-imm__ambient {
  position: absolute;
  inset: 0;
  opacity: 0;
  filter: saturate(1.24);
  transition: opacity 800ms ease;   /* spec：换歌 500~1000ms 平滑过渡 */
}
.ys-imm__ambient.is-on { opacity: 0.86; }

/* 漂移光球（对齐参考站 .drift-orb）：圆形 radial（中心 α0.72 → 38% α0.24 → 66% 透明），
   saturate(1.45)（v2.1.66 起不再叠 blur —— 46px 模糊让这层每帧按 ~512px 重栅格化，是「沉浸页
   8.8fps」的大头）；颜色走 --orb-* 三元组变量，跟着封面双主色换。
   ⚠️ 这些元素只在桌面档渲染（模板 v-if="desktopTier"）→ 手机档零 DOM、零开销。 */
.ys-imm__orb {
  position: absolute;
  width: clamp(280px, 40vw, 620px);
  height: clamp(280px, 40vw, 620px);
  border-radius: 9999px;
  opacity: 0.47;
  /* v2.1.66 性能：radial 渐变在 66% 处已经淡到全透明，46px 模糊只是把外缘再软化一点，
     肉眼几乎分不出；但带 filter 的层做变形动画要每帧重新栅格化。实测（软件渲染、
     最坏情况）：8.8fps → 58fps，帧间隔中位数 113.6ms → 16.7ms。 */
  filter: saturate(1.45);
  /* v2.1.66 性能：去掉 will-change（有动画时仍会提升为合成层，但不再让整页常驻持有）。 */
}

.ys-imm__orb--primary {
  top: -14%;
  left: -10%;
  background: radial-gradient(circle, rgba(var(--orb-a), 0.72) 0%, rgba(var(--orb-a), 0.24) 38%, rgba(0, 0, 0, 0) 66%);
  animation: ys-imm-orb-primary 18s ease-in-out infinite alternate;
}

.ys-imm__orb--secondary {
  top: 4%;
  right: -14%;
  background: radial-gradient(circle, rgba(var(--orb-b), 0.72) 0%, rgba(var(--orb-b), 0.24) 38%, rgba(0, 0, 0, 0) 66%);
  animation: ys-imm-orb-secondary 21s ease-in-out infinite alternate;
}

.ys-imm__orb--tertiary {
  bottom: -18%;
  left: 12%;
  background: radial-gradient(circle, rgba(var(--orb-c), 0.7) 0%, rgba(var(--orb-c), 0.22) 38%, rgba(0, 0, 0, 0) 66%);
  animation: ys-imm-orb-tertiary 24s ease-in-out infinite alternate;
}

.ys-imm__orb--pulse {
  top: 32%;
  right: 14%;
  width: clamp(220px, 30vw, 480px);
  height: clamp(220px, 30vw, 480px);
  opacity: 0.42;
  background: radial-gradient(circle, rgba(var(--orb-p), 0.66) 0%, rgba(var(--orb-p), 0.2) 38%, rgba(0, 0, 0, 0) 66%);
  animation: ys-imm-orb-pulse 16s ease-in-out infinite;
}

/* ⚠️ v2.1.75 性能：光球「漂移」本身几乎不要钱（纯 transform，走合成器），
   真正贵的是它们头上压着 5~7 层 backdrop-filter: blur(20px) 的玻璃 ——
   **背景一动，每一层玻璃都要每帧重新算一次 20px 模糊**。
   同一机理本仓库里早被记过一次：SwipePager.vue 写着「背景内容在动的时候
   backdrop-filter 每帧都要重算，手机上最贵」，并因此在滑动期间强制
   backdrop-filter: none。这里做同一件事的温和版：默认让光球**静止**
   （配色 / 位置 / 柔光 / saturate 全保留，只是不再漂），玻璃就退化成
   一次性光栅化。
   想恢复漂移：给 <html> 加 ys-orb-drift 类 —— 不碰歌词滚动，也不碰呼吸环。 */
html:not(.ys-orb-drift) .ys-imm__orb {
  animation: none !important;
}
@media (prefers-reduced-motion: reduce) {
  .ys-imm__orb {
    animation: none !important;
  }
}

/* v2.1.66 性能：四组 keyframes 原来都带 scale()（0.82~1.15 之间来回），对带 filter 的层来说
   缩放＝每帧重新栅格化；现在只留纯位移。translate3d 是合成器直接能做的变换，不触发重栅格。
   视觉差别：光球不再「呼吸变大」，只漂移。 */
@keyframes ys-imm-orb-primary {
  from { transform: translate3d(0, 0, 0); }
  to { transform: translate3d(-20vw, -10vh, 0); }
}
@keyframes ys-imm-orb-secondary {
  from { transform: translate3d(0, 0, 0); }
  to { transform: translate3d(24vw, -12vh, 0); }
}
@keyframes ys-imm-orb-tertiary {
  from { transform: translate3d(-12vw, 24vh, 0); }
  to { transform: translate3d(0, 0, 0); }
}
@keyframes ys-imm-orb-pulse {
  0% { transform: translate3d(0, 0, 0); }
  100% { transform: translate3d(0, -4vh, 0); }
}

/* v2.1.66：页面不可见 / 窗口失焦（iframe 被别的窗格盖住、用户正在拖父页面分栏）→ 光球停转。
   根节点上的 is-page-idle 由脚本用 visibilitychange + window blur/focus 维护，
   这样父页面在拖动时不必再每帧与这几颗大层合成。 */
.ys-imm.is-page-idle .ys-imm__orb { animation-play-state: paused; }

/* 压暗渐变（v2.1.63 调淡：让氛围透出来，同时保住文字对比度） */
.ys-imm__scrim {
  position: absolute;
  inset: 0;
  background: linear-gradient(
    to bottom,
    rgba(0, 0, 0, 0.1) 0%,
    rgba(0, 0, 0, 0.34) 52%,
    rgba(0, 0, 0, 0.66) 100%
  );
}

/* ── 内容列 ───────────────────────────────────────────────────────────── */
.ys-imm__shell {
  position: relative;
  display: flex;
  flex-direction: column;
  width: 100%;
  max-width: var(--imm-col);
  height: 100%;
  margin: 0 auto;
  padding: var(--safe-top) var(--imm-gutter) calc(14px + var(--safe-bottom));
}

/* ── 顶部 ─────────────────────────────────────────────────────────────── */
.ys-imm__top {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  min-height: 44px;
  padding-top: 10px;
}

.ys-imm__icon-btn {
  appearance: none;
  -webkit-appearance: none;
  position: relative;
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  padding: 0;
  border-radius: 50%;
  border: 1px solid var(--imm-glass-line);
  background: var(--imm-glass-bg);
  backdrop-filter: var(--imm-blur);
  -webkit-backdrop-filter: var(--imm-blur);
  color: rgba(255, 255, 255, 0.65);
  font-size: 11px;
  font-weight: 500;
  letter-spacing: 0.02em;
  cursor: pointer;
  transition: color 0.25s ease, background-color 0.25s ease, transform 0.18s ease;
}
.ys-imm__icon-btn:hover { color: rgba(255, 255, 255, 1); background: var(--imm-glass-bg-strong); }
.ys-imm__icon-btn:active { transform: scale(0.94); }
.ys-imm__icon-btn.is-on { color: rgba(255, 255, 255, 0.95); background: var(--imm-glass-bg-strong); }
.ys-imm__icon-btn--back { width: 42px; height: 42px; }

.ys-imm__icon { width: 20px; height: 20px; }
.ys-imm__icon--sm { width: 18px; height: 18px; }
.ys-imm__icon--aux { width: 20px; height: 20px; }
.ys-imm__icon--main { width: 24px; height: 24px; }

/* ── 主体 ─────────────────────────────────────────────────────────────── */
.ys-imm__body {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  padding-top: clamp(10px, 4vh, 40px);   /* 封面不贴顶 */
}

.ys-imm__cover-wrap {
  flex: 0 0 auto;
  display: grid;
  place-items: center;
  width: 100%;
}

.ys-imm__cover {
  width: var(--imm-cover);
  max-width: 100%;
  aspect-ratio: 1 / 1;
  border-radius: 22px;               /* spec 封面 18~24px */
  object-fit: cover;
  background: rgba(255, 255, 255, 0.05);
  /* 只给非常柔和的环境阴影，靠明度与背景分层，不靠粗边框 */
  box-shadow: 0 30px 70px -34px rgba(0, 0, 0, 0.8), 0 6px 22px -12px rgba(0, 0, 0, 0.5);
}

.ys-imm__cover--empty {
  display: grid;
  place-items: center;
  border: 1px solid var(--imm-glass-line);
  background: var(--imm-glass-bg);
  backdrop-filter: var(--imm-blur);
  -webkit-backdrop-filter: var(--imm-blur);
}
.ys-imm__cover-icon { width: 68px; height: 68px; color: rgba(255, 255, 255, 0.2); }

.ys-imm__meta {
  flex: 0 0 auto;
  width: 100%;
  padding: 22px 4px 0;
  text-align: center;
}
.ys-imm__title {
  font-size: var(--imm-title);
  font-weight: 600;
  line-height: 1.3;
  letter-spacing: 0.01em;
  color: rgba(255, 255, 255, 0.95);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ys-imm__sub {
  margin-top: 7px;
  font-size: var(--imm-sub);
  color: rgba(255, 255, 255, 0.55);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ys-imm__credit {
  margin-top: 5px;
  font-size: var(--imm-credit);
  color: rgba(255, 255, 255, 0.35);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 「更多」浮层：绝对定位锚在右上角，不参与布局流（不会把封面推下去） */
.ys-imm__sheet {
  position: absolute;
  top: calc(var(--safe-top) + 60px);
  right: var(--imm-gutter);
  z-index: 6;
  display: flex;
  flex-direction: column;
  min-width: 174px;
  padding: 6px;
  border-radius: 18px;
  border: 1px solid var(--imm-glass-line);
  background: var(--imm-glass-bg);
  backdrop-filter: var(--imm-blur);
  -webkit-backdrop-filter: var(--imm-blur);
  box-shadow: 0 26px 64px -30px rgba(0, 0, 0, 0.8);
}
.ys-imm__sheet-item {
  appearance: none;
  border: 0;
  background: transparent;
  padding: 10px 12px;
  border-radius: 12px;
  font-size: 13px;
  text-align: left;
  color: rgba(255, 255, 255, 0.62);
  cursor: pointer;
  transition: color 0.2s ease, background-color 0.2s ease;
}
.ys-imm__sheet-item:hover { color: #fff; background: rgba(255, 255, 255, 0.06); }
.ys-imm__sheet-item.is-on { color: rgba(255, 255, 255, 0.95); }
.ys-imm__sheet-num { color: rgba(255, 255, 255, 0.4); font-variant-numeric: tabular-nums; }

/* ── 沉浸式歌词 ───────────────────────────────────────────────────────── */
.ys-imm__lyrics {
  position: relative;
  flex: 1 1 auto;
  min-height: 90px;                 /* 兜底：再挤也不让歌词窗塌掉 */
  width: 100%;
  margin-top: 16px;
  /* 两端渐变消失：歌词融进背景，而不是一块黑色歌词窗 */
  -webkit-mask-image: linear-gradient(to bottom, rgba(0, 0, 0, 0) 0%, #000 20%, #000 76%, rgba(0, 0, 0, 0) 100%);
  mask-image: linear-gradient(to bottom, rgba(0, 0, 0, 0) 0%, #000 20%, #000 76%, rgba(0, 0, 0, 0) 100%);
}

.ys-imm__lyrics-scroll {
  height: 100%;
  overflow-y: auto;
  overflow-x: hidden;
  display: flex;
  flex-direction: column;
  text-align: center;
  scrollbar-width: none;
  -ms-overflow-style: none;
}
.ys-imm__lyrics-scroll::-webkit-scrollbar { width: 0; height: 0; display: none; }
/* 上下留白（用 flex 撑，不用百分比 padding —— 那会按宽度算）：首尾行也能滚到正中 */
.ys-imm__lyrics-scroll::before,
.ys-imm__lyrics-scroll::after {
  content: '';
  flex: 0 0 auto;
  height: 42%;
  min-height: 8vh;   /* 百分比高度万一不解析时的兜底 */
}

.ys-imm__line {
  flex: 0 0 auto;
  padding: 7px 8px;
  font-size: calc(var(--line-size, var(--imm-lyric)) * var(--imm-lyric-scale));
  line-height: 1.5;
  color: rgba(255, 255, 255, 0.65);
  cursor: pointer;
  transition: opacity 520ms cubic-bezier(0.4, 0, 0.2, 1), filter 320ms ease, color 320ms ease, font-size 320ms ease;
}
.ys-imm__line.is-active { color: #fff; font-weight: 600; }
.ys-imm__line-text { margin: 0; }
.ys-imm__line-trans {
  margin: 4px 0 0;
  font-size: calc(var(--line-trans-size, 12px) * var(--imm-lyric-scale));
  font-weight: 400;
  line-height: 1.4;
  color: rgba(255, 255, 255, 0.5);
}

.ys-imm__lyrics-empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
}
.ys-imm__empty-icon { width: 40px; height: 40px; color: rgba(255, 255, 255, 0.18); }
.ys-imm__empty-text { font-size: 13px; color: rgba(255, 255, 255, 0.28); }

.ys-imm__nudge {
  position: absolute;
  right: -6px;
  top: 50%;
  transform: translateY(-50%);
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.ys-imm__nudge-btn {
  appearance: none;
  border: 0;
  background: transparent;
  padding: 0;
  width: 28px;
  height: 28px;
  display: grid;
  place-items: center;
  color: rgba(255, 255, 255, 0.2);
  cursor: pointer;
  transition: color 0.25s ease, transform 0.18s ease;
}
.ys-imm__nudge-btn:hover { color: rgba(255, 255, 255, 0.6); }
.ys-imm__nudge-btn:active { transform: scale(0.94); }

/* ── 底部：进度 + 控制 ────────────────────────────────────────────────── */
.ys-imm__bottom {
  flex: 0 0 auto;
  width: 100%;
  padding-top: 6px;
}

.ys-imm__progress {
  display: flex;
  align-items: center;
  height: 22px;                     /* 命中区，视觉仍只有 3px */
  cursor: pointer;
}
.ys-imm__track {
  position: relative;
  width: 100%;
  height: 3px;
  border-radius: 9999px;
  background: rgba(255, 255, 255, 0.2);
}
.ys-imm__fill {
  position: absolute;
  left: 0;
  top: 0;
  height: 100%;
  border-radius: 9999px;
  background: rgba(255, 255, 255, 0.9);
}
.ys-imm__thumb {
  position: absolute;
  top: 50%;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #fff;
  transform: translate(-50%, -50%);
  box-shadow: 0 1px 5px rgba(0, 0, 0, 0.45);
}

.ys-imm__times {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-top: 2px;
  font-size: 11px;
  color: rgba(255, 255, 255, 0.35);
}
.ys-imm__time { flex: 0 0 auto; font-variant-numeric: tabular-nums; }
.ys-imm__quality {
  flex: 1 1 auto;
  min-width: 0;
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ys-imm__controls {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  padding: 18px 2px 4px;
}

.ys-imm__ctrl-btn {
  appearance: none;
  -webkit-appearance: none;
  position: relative;
  display: grid;
  place-items: center;
  width: 30px;                      /* 手机 30px（spec 十二：28~32）；桌面档再收回 28 */
  height: 30px;
  padding: 0;
  border: 0;
  background: transparent;
  color: rgba(255, 255, 255, 0.65);
  cursor: pointer;
  transition: color 0.25s ease, transform 0.18s ease;
}
/* 命中区撑到 44px，视觉尺寸不变 */
.ys-imm__ctrl-btn::after { content: ''; position: absolute; inset: -9px; }
.ys-imm__ctrl-btn:hover { color: rgba(255, 255, 255, 1); }
.ys-imm__ctrl-btn:active { transform: scale(0.94); }
.ys-imm__ctrl-btn.is-on { color: rgba(255, 255, 255, 0.95); }

.ys-imm__play {
  appearance: none;
  -webkit-appearance: none;
  display: grid;
  place-items: center;
  width: 56px;                      /* 手机 56px（spec 十二：52~60）；桌面档 52 */
  height: 56px;
  padding: 0;
  border-radius: 50%;
  border: 1px solid var(--imm-glass-line);
  background: rgba(255, 255, 255, 0.08);
  backdrop-filter: var(--imm-blur);
  -webkit-backdrop-filter: var(--imm-blur);
  color: #fff;
  cursor: pointer;
  transition: background-color 0.25s ease, transform 0.18s ease;
}
.ys-imm__play:hover { background: rgba(255, 255, 255, 0.14); }
.ys-imm__play:active { transform: scale(0.94); }
.ys-imm__play-glyph { transform: translateX(2px); }

.ys-imm__vol { display: flex; align-items: center; gap: 6px; }

/* ── 竖向音量条（桌面档专用；手机档整组 display:none，只留那颗静音键）──────
   尺寸/配色逐像素抄自参考稿 528c8995 裁切：轨道 3px 宽 × 74px 高、白 18% 底、
   填充玫红 rgb(240,86,114) 从**底向上**（该图 50% 音量时填充正好 37px）。 */
.ys-imm__vol-stick { display: none; }
.ys-imm__vol-rail {
  position: relative;
  width: 3px;
  height: 74px;
  border-radius: 9999px;
  /* 参考稿实测：未填充段比背景亮 ~18 个亮度值 ≈ 白 8~10% */
  background: rgba(255, 255, 255, 0.1);
  cursor: pointer;
  touch-action: none;               /* 拖音量时别把页面带着滚 */
}
/* 命中区撑到 ~21×90，视觉仍只有 3px */
.ys-imm__vol-rail::after { content: ''; position: absolute; inset: -8px -9px; }
.ys-imm__vol-fill {
  position: absolute;
  left: 0;
  bottom: 0;
  width: 100%;
  border-radius: 9999px;
  background: #f05672;
}
.ys-imm__vol-num {
  appearance: none;
  -webkit-appearance: none;
  margin: 0;
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  font-size: 13px;                  /* 参考稿标签实测约 13px（文字带 26×13px、比图标行略低一点点） */
  line-height: 1;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.82);
  cursor: pointer;
  transition: color 0.25s ease;
}
.ys-imm__vol-num:hover { color: rgba(255, 255, 255, 0.95); }

/* ── 提示 / 过渡 ──────────────────────────────────────────────────────── */
.ys-imm__toast {
  position: absolute;
  left: 50%;
  bottom: calc(120px + var(--safe-bottom));
  transform: translateX(-50%);
  max-width: 82vw;
  padding: 9px 16px;
  border-radius: 9999px;
  border: 1px solid var(--imm-glass-line);
  background: rgba(255, 255, 255, 0.08);
  backdrop-filter: var(--imm-blur);
  -webkit-backdrop-filter: var(--imm-blur);
  font-size: 12px;
  color: rgba(255, 255, 255, 0.9);
  pointer-events: none;
}

.ys-imm-fade-enter-active,
.ys-imm-fade-leave-active { transition: opacity 0.28s ease; }
.ys-imm-fade-enter-from,
.ys-imm-fade-leave-to { opacity: 0; }

/* ══════════════════════════════════════════════════════════════════════════
   响应式：两档（v2.1.62 口径）—— 手机 / 桌面（原「平板 768~1023」档已并入桌面）。
   手机档：纵向居中 —— 顶部导航 → 大封面 → 歌曲信息 → 歌词 → 细进度条 → 播放控制。
   桌面档：**两栏**（v2.1.64，按用户 2026-10-01 发来的参考播放器截图重排）——
           左栏 封面 / 歌名行 / 歌手 · 词曲 / 进度条 / 控制键；右栏 浮动歌词；
           右缘圆形上下首、右上角全屏、右下角浮动列表钮。

   ⚠️ spec 十六 当年明确「不要为了响应式强行在桌面端增加左右两栏」，第七节也要求歌词是
      「轻量级浮动歌词层」而不是右侧黑色歌词栏 —— 这两条**现在仍然成立**：
      右栏是**无卡片、无边框、直接浮在氛围背景上的歌词**，不是黑底歌词栏；
      而桌面两栏是用户看着参考截图点名要的，不是我们为了响应式自己加的。
      （口径变更史：v2.1.42-43 40/60 分栏 → v2.1.45 全端纵向 → v2.1.46 宽屏分栏 →
        v2.1.47 按用户 spec 回到全端纵向 → v2.1.64 按用户截图回到桌面两栏。
        截图口径优先；要改回全端纵向之前先问用户。）
   ══════════════════════════════════════════════════════════════════════════ */

/* 桌面专属件（歌词轻推箭头）在手机档隐藏 */
.ys-imm__only-sm { display: none; }
.ys-imm__only-xs { display: flex; }

/* ── 档位显隐工具（v2.1.64）────────────────────────────────────────────────
   --desk-* 只在桌面档渲染，--mob-* 只在手机档渲染。
   ⚠️ 必须写在被它们覆盖的组件样式**之后**（同为单类选择器，靠源序取胜）：
      `.ys-imm__icon-btn` 自带 display:grid，只有靠这里更晚的 display:none 才能真藏住它。
      桌面档的 display 覆写统一放在文件尾那张桌面媒体查询里。 */
.ys-imm__desk-g,
.ys-imm__desk-f,
.ys-imm__desk-b { display: none; }
.ys-imm__mob-g { display: grid; }
.ys-imm__mob-b { display: block; }

/* 播放列表抽屉（v2.1.64）：只有桌面档有 —— 手机档的播放列表还是走「更多」里那条计数提示。
   默认 display:none，桌面档媒体查询里再打开（源序在后，覆盖得到）。 */
.ys-imm__queue-mask,
.ys-imm__queue { display: none; }

/* ── 两栏包装（v2.1.64）────────────────────────────────────────────────────
   手机档 display:contents —— 不生成盒子，子元素照旧排进 .ys-imm__body 的那条纵向流，
   所以手机档的 DOM 顺序（封面 → 信息 → 歌词 → 进度/控制）与旧版逐像素一致。 */
.ys-imm__stage,
.ys-imm__lyrics-col { display: contents; }

/* 歌名行：桌面档在歌名右侧挂「收藏 / 更多」（手机档那一组不渲染 → 标题仍是居中一行） */
.ys-imm__title-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.ys-imm__title-row .ys-imm__title { flex: 1 1 auto; min-width: 0; }
.ys-imm__title-actions { flex: 0 0 auto; align-items: center; gap: 2px; }
.ys-imm__meta-btn {
  appearance: none;
  -webkit-appearance: none;
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  padding: 0;
  border: 0;
  border-radius: 50%;
  background: transparent;
  color: rgba(255, 255, 255, 0.42);
  cursor: pointer;
  transition: color 0.22s ease, background-color 0.22s ease;
}
.ys-imm__meta-btn:hover { color: #fff; background: rgba(255, 255, 255, 0.08); }
.ys-imm__meta-btn.is-on { color: rgba(255, 255, 255, 0.95); }

/* 词曲行 · 桌面档版（挂在右栏歌词块顶部，见桌面媒体查询） */
.ys-imm__credit--lead { margin-top: 0; }

/* 右缘圆形上下首 + 右下浮动列表钮：共用一套玻璃圆钮皮肤（都只桌面档渲染） */
.ys-imm__rail { flex-direction: column; gap: 12px; }
.ys-imm__rail-btn,
.ys-imm__fab {
  appearance: none;
  -webkit-appearance: none;
  place-items: center;
  padding: 0;
  border-radius: 50%;
  border: 1px solid var(--imm-glass-line);
  background: rgba(0, 0, 0, 0.26);
  backdrop-filter: var(--imm-blur);
  -webkit-backdrop-filter: var(--imm-blur);
  color: rgba(255, 255, 255, 0.6);
  cursor: pointer;
  transition: color 0.22s ease, background-color 0.22s ease, transform 0.18s ease;
}
.ys-imm__rail-btn { display: grid; width: 44px; height: 44px; }
.ys-imm__fab { width: 46px; height: 46px; }
.ys-imm__rail-btn:hover,
.ys-imm__fab:hover { color: #fff; background: rgba(255, 255, 255, 0.14); }
.ys-imm__rail-btn:active,
.ys-imm__fab:active { transform: scale(0.94); }
.ys-imm__fab.is-on { color: rgba(255, 255, 255, 0.95); background: rgba(255, 255, 255, 0.14); }

/* 歌词提前 / 延后（桌面档，歌词块右上角那两颗）：共用上面那套玻璃圆钮皮肤，尺寸小一号 */
.ys-imm__offset { align-items: center; gap: 6px; }
.ys-imm__offset .ys-imm__rail-btn { width: 32px; height: 32px; }
.ys-imm__offset-val {
  appearance: none;
  -webkit-appearance: none;
  display: grid;
  place-items: center;
  padding: 4px 10px;
  border-radius: 999px;
  border: 1px solid var(--imm-glass-line);
  background: rgba(0, 0, 0, 0.32);
  backdrop-filter: var(--imm-blur);
  -webkit-backdrop-filter: var(--imm-blur);
  color: rgba(255, 255, 255, 0.66);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  cursor: pointer;
  transition: color 0.2s ease;
}
.ys-imm__offset-val:hover { color: #fff; }

/* 原「平板 768~1023px」那一档已并入下面的桌面档（v2.1.62）：用户口径是「除了手机窗口，都是桌面」，
   留一档四不像的中间态只会让人分不清自己看到的是哪套。 */

/* ── 桌面档：**除了手机，全是桌面**（v2.1.62 用户口径）──
   判定不是「窗口有多宽」，而是「**是不是手机**」——只有「窄 + 触摸」才算手机：
       手机 = (max-width: 767px) **且** hover:none **且** pointer:coarse → 走上面的手机档；
       其余**一律桌面档**，包括鼠标设备上的窄窗口（半屏、挂了 devtools、小笔记本、WebView 面板都算）。
   ⚠️ 这里必须写成**否定式**，不要写成肯定式的 `(hover: hover) and (pointer: fine)`：
      实测过，无指针设备（headless / 某些 WebView / 无鼠标的 kiosk）报告的是 pointer:none ——
      hover 与 pointer 两个特性**都不匹配**，肯定式会静默失效、退回手机档，正是用户抱怨的那种情况。
      否定式在「特性不支持」时整条查询为假 → `not` 为真 → 兜到桌面档，这是安全的默认。
   ⚠️ 早先这里是 `min-width: 1024px`，结果是「电脑上把窗口拉窄就退回手机版式」——
      用户 2026-09-30 原话：「你限定太多了，改为除了手机窗口，都是桌面，我现在电脑浏览器打开还是手机端布局」。
   桌面档内含（spec 十四）：内容列居中限宽 560，但顶栏与「更多」浮层**脱离内容列、钉在窗口两侧** ——
   手机上这三样本来就在列内、而手机列宽 = 100%，所以位移只在桌面可见。
   垂直居中用一对 auto 外边距（body 的 margin-top:auto + bottom 的 margin-bottom:auto）。
   ⚠️ 高度预算已被吃满（1440×900 上这一组刚好填满整屏），这里的改动刻意做到零净高度变化，
      任何加高都会挤压歌词窗（它 flex: 0 1 auto，会被压到 clamp 下限）。 */
/* ═══ 桌面档样式（v2.1.64 起改为「根类开关」，不再用 @media 否定式查询）═══
   为什么：媒体查询只能听设备的指针/宽度，YG 在电脑上永远看不到手机档、也没法对着改。
   现在档位由 JS 决定根节点上的 .is-tier-desktop / .is-tier-mobile，可被 ?tier=mobile|desktop 覆盖，
   于是电脑上也能直接看手机版式。原先这里是 `@media not all and (max-width:767px) and (hover:none) and (pointer:coarse)`。
   ⚠️ 拆掉媒体查询后本块选择器都升了一级权重 → 文件尾部靠「同权重+源序」压制的几个块已同步补权重。 */
.ys-imm.is-tier-desktop {
  --imm-col: 560px;
  --imm-gutter: 32px;
  --imm-edge: clamp(28px, 4vw, 56px);     /* 桌面：顶栏/浮层离窗口两侧的距离 */
  --imm-lyric-scale: 1.16;                /* 桌面歌词放大系数（手机档固定 1，17px 不动） */
  /* spec 给的宽度是 min(65vw, 420px)；再叠一道**高度预算**：
     顶栏 58 + 外壳留白 40 + 信息区 ~107（含词曲行）+ 歌词间距 28 + 歌词窗 16vh + 传输条 125 ≈ 458 + 16vh。
     取 500 而不是 470：必须把「有词曲行」的歌算进去，否则那首歌的控制键会被挤出屏幕。
     代价与收益：900px 高的窗口封面仍是 400px（与旧版一致，不回归）；
     1080p 及以上拿满 420px，比旧的 clamp(280px,30vw,420px) 更大；1024×768 这类窄高屏也更大。 */
  --imm-cover: min(65vw, 420px, calc(100vh - 500px));
  --imm-title: 26px;
  --imm-sub: 15px;
  --imm-credit: 12px;
}
.ys-imm.is-tier-desktop .ys-imm__only-sm { display: flex; }
.ys-imm.is-tier-desktop .ys-imm__only-xs { display: none; }
.ys-imm.is-tier-desktop .ys-imm__shell {
  padding-top: calc(var(--safe-top) + 58px);   /* 顶栏改绝对定位后，外壳替它占位（与旧布局等高） */
  padding-bottom: 40px;
  /* ⚠️ 关键：外壳不再是定位参照（它只是内容列，宽 560）。
     保持 relative 会把绝对定位的顶栏与「更多」浮层困在 560px 列内 —— 实测过，按钮只挪到列内缩进 56px。
     设为 static 后两者改以 .ys-imm（fixed，等于窗口）为参照，才真正贴到窗口两侧。
     外壳内其余绝对定位件不受影响：__toast 用 left:50% 居中、__nudge 以 __lyrics/__body 为参照。 */
  position: static;
}
.ys-imm.is-tier-desktop .ys-imm__top {
  position: absolute;
  top: var(--safe-top);
  left: var(--imm-edge);
  right: var(--imm-edge);
  min-height: 44px;
  padding-top: 14px;
}
/* spec 五：顶部按钮 40~44px，桌面取上限 */
.ys-imm.is-tier-desktop .ys-imm__icon-btn { width: 44px; height: 44px; }
.ys-imm.is-tier-desktop .ys-imm__icon-btn--back { width: 44px; height: 44px; }
.ys-imm.is-tier-desktop .ys-imm__icon { width: 22px; height: 22px; }
.ys-imm.is-tier-desktop .ys-imm__icon--sm { width: 20px; height: 20px; }
/* 「更多」浮层跟着右边那颗按钮走（原来固定离边 32px，会和按钮错开） */
.ys-imm.is-tier-desktop .ys-imm__sheet { top: calc(var(--safe-top) + 72px); right: var(--imm-edge); }

/* 封面环境光晕（spec 四）：跟着封面主色，只铺在封面周围，糊不到歌词 */
.ys-imm.is-tier-desktop .ys-imm__cover-wrap { position: relative; }
.ys-imm.is-tier-desktop .ys-imm__cover-wrap::before {
  content: '';
  position: absolute;
  inset: -10% -8%;
  z-index: 0;
  border-radius: 50%;
  background: radial-gradient(58% 58% at 50% 48%, var(--imm-halo, transparent) 0%, transparent 72%);
  filter: blur(42px);
  opacity: 0.55;
  pointer-events: none;
}
.ys-imm.is-tier-desktop .ys-imm__cover { position: relative; z-index: 1; }

.ys-imm.is-tier-desktop .ys-imm__body {
  flex: 0 0 auto;              /* 不再抢满：交给下面两个 auto 外边距居中 */
  margin-top: auto;
  padding-top: 16px;
}
.ys-imm.is-tier-desktop .ys-imm__meta { padding-top: 30px; }
/* 歌词是「轻量级浮动层」：高度有上限 —— 大屏多出来的是留白，不是更高的歌词窗 */
.ys-imm.is-tier-desktop .ys-imm__lyrics {
  flex: 0 1 auto;
  height: clamp(96px, 16vh, 200px);
  min-height: 0;
  margin-top: 28px;
}
/* 大屏上 17px 歌词看着像小字：桌面乘系数放大，距离衰减层级不变 */
.ys-imm.is-tier-desktop .ys-imm__line { font-size: calc(var(--line-size, var(--imm-lyric)) * var(--imm-lyric-scale)); }
/* 歌词轻推箭头挪到内容列外的空白处，不再贴着歌词边缘 */
.ys-imm.is-tier-desktop .ys-imm__nudge { right: -44px; }

.ys-imm.is-tier-desktop .ys-imm__bottom { margin-bottom: auto; }
/* v2.1.63 对齐参考播放器实测尺寸：桌面控制件取 56 / 30。
   ⚠️ padding-top 由 24 收到 20：高度预算已被吃满，加高必须等量抵消，否则挤压歌词窗。 */
.ys-imm.is-tier-desktop .ys-imm__controls { padding: 20px 4px 6px; }
.ys-imm.is-tier-desktop .ys-imm__play { width: 56px; height: 56px; }
.ys-imm.is-tier-desktop .ys-imm__ctrl-btn { width: 30px; height: 30px; }

/* ── 竖向音量条（桌面档：**默认收起**，点音量键才浮出来；尺寸/配色照参考稿 528c8995）──
   展开后轨道底端 → 「NN%」顶端 = 24px（参考稿实测），整组悬在音量键正上方居中。
   ⚠️ 整组是**绝对定位的浮层**（bottom: 100% + 8px）：一旦参与行高，这一行会被撑高
      ~111px（813px 高的窗口封面立刻被挤掉）。展开时轨道顶端会飘到左栏下沿之上，
      与参考稿里「这根线越过进度行那一带」的姿态一致。 */
.ys-imm.is-tier-desktop .ys-imm__vol { position: relative; align-items: center; margin-right: 36px; }   /* 让开右侧时间：轨道飘上去时不切「4:16」这行读数（参考稿里音量列也是内缩的） */
.ys-imm.is-tier-desktop .ys-imm__vol-icon { display: grid; }      /* 桌面档恢复音量键：点它展开 / 收起竖条（静音在竖条里的「NN%」上） */
.ys-imm.is-tier-desktop .ys-imm__vol-stick {
  position: absolute;
  display: none;                  /* 默认收起 */
  left: 50%;
  bottom: calc(100% + 8px);       /* 悬在音量键正上方 8px，水平居中 → 轨道 x 与参考稿实测值一致 */
  transform: translateX(-50%);
  flex-direction: column;
  align-items: center;
}
.ys-imm.is-tier-desktop .ys-imm__vol-stick--open { display: flex; }
/* 竖起后轨道与标签的间距：参考稿实测 24px（标签 13px 高 → 组高 74 + 24 + 13 = 111px） */
.ys-imm.is-tier-desktop .ys-imm__vol-rail { margin-bottom: 24px; }
/* 轨道向上飘会经过右时间那一带：让时间文字压在轨道上面，读数不被细线切 */
.ys-imm.is-tier-desktop .ys-imm__time { position: relative; z-index: 1; }

/* 进度条对齐参考播放器：6px 圆角轨 + 未播轨道压淡（未播 0.07 / hover 0.16） */
.ys-imm.is-tier-desktop .ys-imm__track { height: 6px; background: rgba(255, 255, 255, 0.07); transition: background-color 0.18s ease; }
.ys-imm.is-tier-desktop .ys-imm__thumb {
  width: 11px;
  height: 11px;
  transition: transform 0.16s ease;
}
.ys-imm.is-tier-desktop .ys-imm__progress:hover .ys-imm__track { background: rgba(255, 255, 255, 0.16); }
.ys-imm.is-tier-desktop .ys-imm__progress:hover .ys-imm__thumb { transform: translate(-50%, -50%) scale(1.2); }

/* 音质做成一枚小药丸徽标（对应参考站那枚 FLAC 徽标）；手机档不受影响，仍是纯文字 */
.ys-imm.is-tier-desktop .ys-imm__quality {
  flex: 0 0 auto;
  margin: 0 auto;
  padding: 2px 10px;
  border: 1px solid rgba(255, 255, 255, 0.14);
  border-radius: 9999px;
  background: rgba(255, 255, 255, 0.04);
  font-size: 10px;
  letter-spacing: 0.06em;
  color: rgba(255, 255, 255, 0.45);
}

/* 指针视差：鼠标移动时背景整体轻微位移（±1.4%），氛围有点深度。只有桌面档有这段。 */
.ys-imm.is-tier-desktop .ys-imm__bg {
  transform: translate3d(calc(var(--par-x, 0) * 1.4%), calc(var(--par-y, 0) * 1.4%), 0) scale(1.04);
  transition: transform 400ms ease-out;
}

/* ══ 桌面两栏版式（v2.1.64：按用户 2026-10-01 发来的参考播放器截图重排）════════════
   左栏 = 封面 → 歌名行（含收藏）→ 歌手 → 进度条（两端时间、FLAC 徽标压在轨道上方）→ 控制键；
   右栏 = 浮动歌词（无卡片、无边框、左对齐，直接浮在氛围背景上）；
   右缘圆形上下首（中上部）、左上圆形收起、右上圆角方形全屏、右下角浮动圆钮。

   实现要点：`.ys-imm__body` 在桌面档改 display:contents —— 它那两个「栏」包装
   （.ys-imm__stage / .ys-imm__lyrics-col，手机档是 display:contents）随之升级成外壳的栅格项，
   而底部的 __bottom 仍是外壳的直接子元素。于是外壳的栅格是：
      第 1 行 1 列 = __stage（封面 + 信息）      第 2 列跨两行 = __lyrics-col
      第 2 行 1 列 = __bottom（进度 + 控制）
   DOM 顺序与手机档完全一致，手机档一行都不用动。 */
.ys-imm.is-tier-desktop {
  --imm-col: min(1120px, 92vw);                   /* 两栏合计的内容列宽 */
  --imm-cover: min(100%, calc(100dvh - 330px));   /* 封面吃满左栏宽度，只受窗口高度约束 */
  --imm-lyric-scale: 1.3;
  --imm-edge: clamp(20px, 2.6vw, 40px);           /* 角标贴得比旧版更近（对齐截图） */
}
.ys-imm.is-tier-desktop .ys-imm__desk-g { display: grid; }
.ys-imm.is-tier-desktop .ys-imm__desk-f { display: flex; }
.ys-imm.is-tier-desktop .ys-imm__desk-b { display: block; }
.ys-imm.is-tier-desktop .ys-imm__mob-g,
.ys-imm.is-tier-desktop .ys-imm__mob-b { display: none; }
.ys-imm.is-tier-desktop .ys-imm__stage,
.ys-imm.is-tier-desktop .ys-imm__lyrics-col { display: flex; }

.ys-imm.is-tier-desktop .ys-imm__shell {
  display: grid;
  grid-template-columns: minmax(0, 46fr) minmax(0, 54fr);
  grid-template-rows: auto auto;
  column-gap: clamp(32px, 5vw, 76px);
  row-gap: clamp(10px, 1.6vh, 18px);
  align-items: center;
  align-content: center;
}
.ys-imm.is-tier-desktop .ys-imm__body { display: contents; }
.ys-imm.is-tier-desktop .ys-imm__stage {
  grid-area: 1 / 1 / 2 / 2;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  min-width: 0;
}

/* ── 左栏：封面 → 歌名行 → 歌手 → 进度/控制，一律左对齐（不再居中） ── */
.ys-imm.is-tier-desktop .ys-imm__cover-wrap { justify-items: start; }
.ys-imm.is-tier-desktop .ys-imm__cover { border-radius: 20px; }
.ys-imm.is-tier-desktop .ys-imm__meta { padding: 18px 0 0; text-align: left; }
.ys-imm.is-tier-desktop .ys-imm__title-row { width: 100%; }
.ys-imm.is-tier-desktop .ys-imm__sub,
.ys-imm.is-tier-desktop .ys-imm__credit { text-align: left; }

/* 进度区重排：`时间 | 轨道 | 时间` 三列一行，FLAC 徽标压在轨道上方居中，
   控制键占满左栏整行 —— 正好是参考截图里那三层的排法。 */
.ys-imm.is-tier-desktop .ys-imm__bottom {
  grid-area: 2 / 1 / 3 / 2;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  grid-template-rows: auto auto auto;
  column-gap: 14px;
  align-items: center;
  width: 100%;
  margin-bottom: 0;
}
.ys-imm.is-tier-desktop .ys-imm__times { display: contents; }
.ys-imm.is-tier-desktop .ys-imm__progress { grid-area: 2 / 2 / 3 / 3; }
.ys-imm.is-tier-desktop .ys-imm__time:first-of-type { grid-area: 2 / 1 / 3 / 2; }
.ys-imm.is-tier-desktop .ys-imm__time:last-of-type { grid-area: 2 / 3 / 3 / 4; }
.ys-imm.is-tier-desktop .ys-imm__quality { grid-area: 1 / 2 / 2 / 3; justify-self: center; margin: 10px 0 8px; }
.ys-imm.is-tier-desktop .ys-imm__controls { grid-area: 3 / 1 / 4 / 4; padding: 12px 0 0; }

/* ── 右栏：浮动歌词 ── */
.ys-imm.is-tier-desktop .ys-imm__lyrics-col {
  grid-area: 1 / 2 / 3 / 3;
  display: flex;
  flex-direction: column;
  justify-content: flex-start;
  align-self: stretch;        /* 跨第 1、2 行 → 高度就是左栏（封面+信息+进度/控制）的整体高度 */
  min-width: 0;
  height: auto;
  /* ⚠️ min-height:0 是这套高度的关键：跨两行的栅格项若保留自动最小尺寸，
     它会拿歌词内容的自然高度去撑 auto 轨道，两行一起被顶爆（实测列高 1743px、整块飞出屏幕）。
     归零后轨道只由左栏内容决定，这一列被拉伸到同样的高度，歌词窗自己在内部滚动。 */
  min-height: 0;
}
/* 词曲行在桌面档是歌词块的首行，对齐歌词行的 8px 内边距。
   flex:0 0 auto 不能少：右栏是 flex 纵向，歌词窗的 flex-basis 是它的内容高度（上千 px），
   默认收缩会把这一行按比例压扁（实测被压到 7px，文字直接切掉）。 */
.ys-imm.is-tier-desktop .ys-imm__credit--lead { flex: 0 0 auto; margin: 0 0 14px; padding-left: 8px; text-align: left; }
/* 歌词窗撑满右栏剩下的全部高度（v2.1.64 用户口径：高度按左栏整体高度来，
   不做「飘在中间的一小块」），当前行由 recenterActiveLine 钉在视觉中心。
   flex-basis 取 0：拿「剩余空间」而不是「内容高度」当基准，避免反过来压扁词曲行。 */
.ys-imm.is-tier-desktop .ys-imm__lyrics {
  flex: 1 1 0;
  height: auto;
  min-height: 0;
  margin-top: 0;
}
.ys-imm.is-tier-desktop .ys-imm__lyrics-scroll,
.ys-imm.is-tier-desktop .ys-imm__line { text-align: left; }
.ys-imm.is-tier-desktop .ys-imm__nudge { right: -40px; }

/* ── 歌词提前 / 延后：贴在歌词块右上角，**平时隐身**，鼠标进歌词区才淡入 ──
      （用户口径：右上角那两颗不是切歌，是调歌词偏移；平时不显示，鼠标放到歌词区域才出来。）
      用 .ys-imm__lyrics-col 的盒子做定位参照：它的顶边就是歌词块的首行（词曲行）。 */
.ys-imm.is-tier-desktop .ys-imm__lyrics-col { position: relative; }
.ys-imm.is-tier-desktop .ys-imm__offset {
  position: absolute;
  top: 0;
  right: 0;
  z-index: 3;
  opacity: 0;
  transform: translateY(-4px);
  pointer-events: none;
  transition: opacity 0.22s ease, transform 0.22s ease;
}
.ys-imm.is-tier-desktop .ys-imm__lyrics-col:hover .ys-imm__offset,
.ys-imm.is-tier-desktop .ys-imm__offset:focus-within {
  opacity: 1;
  transform: none;
  pointer-events: auto;
}

/* ── 右下角浮动圆钮：打开播放列表抽屉 ── */
.ys-imm.is-tier-desktop .ys-imm__fab {
  position: absolute;
  right: clamp(18px, 2vw, 32px);
  bottom: clamp(18px, 2vw, 32px);
  z-index: 4;
}

/* ── 播放列表抽屉：从右向左划出 ──
   浮在右侧一竖条上（不整屏铺满，对齐参考截图的悬浮面板）：
   遮罩 + 面板，面板里 头部（标题 + 循环/清空/关闭）→ 队列列表（封面角标 ▶ / 歌名 / 歌手 / 时长）。 */
.ys-imm.is-tier-desktop .ys-imm__queue-mask {
  display: block;
  position: absolute;
  inset: 0;
  z-index: 6;
  background: rgba(0, 0, 0, 0.46);
  backdrop-filter: blur(2px);
  -webkit-backdrop-filter: blur(2px);
}
.ys-imm.is-tier-desktop .ys-imm__queue {
  display: flex;
  position: absolute;
  top: clamp(12px, 1.6vh, 20px);
  right: clamp(12px, 1.6vw, 20px);
  bottom: clamp(12px, 1.6vh, 20px);
  z-index: 7;
  width: min(400px, 86vw);
  flex-direction: column;
  padding: 20px 14px 14px;
  border-radius: 22px;
  border: 1px solid var(--imm-glass-line);
  background: rgba(20, 18, 22, 0.9);
  backdrop-filter: blur(28px) saturate(1.2);
  -webkit-backdrop-filter: blur(28px) saturate(1.2);
  box-shadow: 0 40px 90px -40px rgba(0, 0, 0, 0.9);
}
.ys-imm.is-tier-desktop .ys-imm__queue-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 0 6px 14px;
}
.ys-imm.is-tier-desktop .ys-imm__queue-title {
  display: inline-flex;
  align-items: flex-start;
  gap: 3px;
  font-size: 15px;
  font-weight: 600;
  color: rgba(255, 255, 255, 0.94);
}
.ys-imm.is-tier-desktop .ys-imm__queue-num {
  font-size: 10px;
  font-weight: 500;
  line-height: 1.5;
  color: rgba(255, 255, 255, 0.45);
  font-variant-numeric: tabular-nums;
}
.ys-imm.is-tier-desktop .ys-imm__queue-acts { display: flex; align-items: center; gap: 4px; }
.ys-imm.is-tier-desktop .ys-imm__queue-btn {
  appearance: none;
  -webkit-appearance: none;
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  padding: 0;
  border: 0;
  border-radius: 50%;
  background: transparent;
  color: rgba(255, 255, 255, 0.5);
  cursor: pointer;
  transition: color 0.2s ease, background-color 0.2s ease;
}
.ys-imm.is-tier-desktop .ys-imm__queue-btn:hover { color: #fff; background: rgba(255, 255, 255, 0.1); }
.ys-imm.is-tier-desktop .ys-imm__queue-empty {
  flex: 1 1 auto;
  display: grid;
  place-items: center;
  color: rgba(255, 255, 255, 0.35);
  font-size: 13px;
}
.ys-imm.is-tier-desktop .ys-imm__queue-list {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.ys-imm.is-tier-desktop .ys-imm__queue-row {
  appearance: none;
  -webkit-appearance: none;
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 7px 8px;
  border: 0;
  border-radius: 14px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  transition: background-color 0.2s ease;
}
.ys-imm.is-tier-desktop .ys-imm__queue-row:hover { background: rgba(255, 255, 255, 0.06); }
.ys-imm.is-tier-desktop .ys-imm__queue-row.is-active { background: rgba(255, 255, 255, 0.1); }
.ys-imm.is-tier-desktop .ys-imm__queue-cover {
  position: relative;
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  border-radius: 10px;
  overflow: hidden;
  background: rgba(255, 255, 255, 0.06);
}
.ys-imm.is-tier-desktop .ys-imm__queue-cover img { width: 100%; height: 100%; object-fit: cover; }
.ys-imm.is-tier-desktop .ys-imm__queue-cover-i { width: 18px; height: 18px; color: rgba(255, 255, 255, 0.3); }
/* 当前曲目：封面上压一枚 ▶（对齐参考截图那张列表） */
.ys-imm.is-tier-desktop .ys-imm__queue-badge {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  background: rgba(0, 0, 0, 0.42);
}
.ys-imm.is-tier-desktop .ys-imm__queue-badge-i { width: 16px; height: 16px; color: #fff; }
.ys-imm.is-tier-desktop .ys-imm__queue-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.ys-imm.is-tier-desktop .ys-imm__queue-name {
  font-size: 13.5px;
  font-weight: 600;
  color: rgba(255, 255, 255, 0.92);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ys-imm.is-tier-desktop .ys-imm__queue-sub {
  font-size: 11.5px;
  color: rgba(255, 255, 255, 0.45);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ys-imm.is-tier-desktop .ys-imm__queue-time {
  font-size: 11.5px;
  color: rgba(255, 255, 255, 0.4);
  font-variant-numeric: tabular-nums;
}

/* 右上角全屏钮做成圆角方形（参考截图里那颗不是圆的） */
.ys-imm.is-tier-desktop .ys-imm__icon-btn--expand { border-radius: 13px; }

/* 「更多」浮层在桌面档从歌名行那颗 ⋮ 的左上角展开：
   right:30px 正好让开按钮本身（按钮宽 30px，是行内最后一颗），
   bottom:calc(100% + 10px) 把浮层抬到歌名行上方 10px。
   ⚠️ 别再改回向下展开：.ys-imm 是 overflow:clip，浮层底边会落到屏幕外被裁掉。 */
.ys-imm.is-tier-desktop .ys-imm__title-row { position: relative; }
.ys-imm.is-tier-desktop .ys-imm__sheet {
  top: auto;
  bottom: calc(100% + 10px);
  right: 30px;
  left: auto;
}

/* ── 抽屉的划出动画（向右滑出 / 收回）── */
.ys-imm-slide-enter-active,
.ys-imm-slide-leave-active {
  transition: transform 0.34s cubic-bezier(0.22, 0.82, 0.3, 1), opacity 0.34s ease;
}
.ys-imm-slide-enter-from,
.ys-imm-slide-leave-to { transform: translateX(104%); opacity: 0.2; }

/* ── 没有 hover 的设备（触摸大屏 / 无指针）：歌词偏移那两颗没有「悬停」可言，直接常显 ──
   放在桌面档媒体查询之后：同权重下源序取胜，覆盖掉那边的 opacity:0。 */
@media (hover: none) {
  /* ⚠️ 选择器和桌面块同权重：拆掉媒体查询后那边升到 (0,3,0)，这里必须跟上才能靠源序压过它。 */
  .ys-imm.is-tier-desktop .ys-imm__offset { opacity: 1; transform: none; pointer-events: auto; }
}

/* ── 矮视口（任意宽度）：封面先收，把空间让给歌词与控制 ──
   ⚠️ 这里**不要**加 max-width 限定：桌面纵向版同样会被矮窗口压到，必须一起兜住。 */
@media (max-height: 700px) {
  /* ⚠️ 拆掉桌面档媒体查询后桌面块权重升了一级，这里必须同权重才能靠「源序在后」继续压过它
     （矮窗口的桌面纵向版同样要收，所以两档都写）。 */
  .ys-imm.is-tier-desktop, .ys-imm.is-tier-mobile { --imm-cover: min(60vw, 300px, 30vh); }
  .ys-imm.is-tier-desktop .ys-imm__body, .ys-imm.is-tier-mobile .ys-imm__body { padding-top: 8px; }
  .ys-imm.is-tier-desktop .ys-imm__meta, .ys-imm.is-tier-mobile .ys-imm__meta { padding-top: 14px; }
  .ys-imm.is-tier-desktop .ys-imm__lyrics, .ys-imm.is-tier-mobile .ys-imm__lyrics { margin-top: 10px; height: clamp(80px, 14vh, 160px); }
  .ys-imm.is-tier-desktop .ys-imm__controls, .ys-imm.is-tier-mobile .ys-imm__controls { padding-top: 10px; }
}

/* ── 极矮视口（横屏手机 / 很扁的窗口）：再收一档，歌词窗只留剩余空间 ── */
@media (max-height: 560px) {
  /* ⚠️ 拆掉桌面档媒体查询后桌面块权重升了一级，这里必须同权重才能靠「源序在后」继续压过它
     （矮窗口的桌面纵向版同样要收，所以两档都写）。 */
  .ys-imm.is-tier-desktop, .ys-imm.is-tier-mobile { --imm-cover: min(52vw, 240px, 26vh); }
  .ys-imm.is-tier-desktop .ys-imm__top, .ys-imm.is-tier-mobile .ys-imm__top { min-height: 40px; padding-top: 6px; }
  .ys-imm.is-tier-desktop .ys-imm__meta, .ys-imm.is-tier-mobile .ys-imm__meta { padding-top: 6px; }
  .ys-imm.is-tier-desktop .ys-imm__lyrics, .ys-imm.is-tier-mobile .ys-imm__lyrics { min-height: 0; margin-top: 6px; height: clamp(0px, 12vh, 110px); }
  .ys-imm.is-tier-desktop .ys-imm__body, .ys-imm.is-tier-mobile .ys-imm__body { overflow: hidden; overflow: clip; }
  .ys-imm.is-tier-desktop .ys-imm__controls, .ys-imm.is-tier-mobile .ys-imm__controls { padding-top: 6px; }
}

@media (prefers-reduced-motion: reduce) {
  .ys-imm__ambient,
  .ys-imm__line,
  .ys-imm__icon-btn,
  .ys-imm__ctrl-btn,
  .ys-imm__play { transition: none; }
  /* 光球不再漂移，保留静态氛围色（与参考站 reduce-motion 行为一致） */
  .ys-imm__orb { animation: none; }
  .ys-imm__bg { transform: none; }
}

/* ── 锁档看版式时的「手机预览框」（v2.1.64）──────────────────────────────
   电脑浏览器上 ?tier=mobile 时，宽窗口里收成一个手机宽度的壳，方便对着改手机版式。
   ⚠️ 只有「锁档 + 窗口够宽」才生效；真机手机档（.is-tier-mobile 不带 .is-tier-forced）零影响。 */
@media (min-width: 640px) {
  .ys-imm.is-tier-mobile.is-tier-forced {
    left: 50%;
    right: auto;
    width: min(430px, 92vw);
    transform: translateX(-50%);
    border-radius: 26px;
    box-shadow: 0 0 0 1px rgba(255, 255, 255, 0.08), 0 32px 80px rgba(0, 0, 0, 0.62);
    /* 帧里 78vw 是按整个窗口（1280）算的 → 封面会撑到 42dvh 上限、把歌词窗挤短。
       换成按容器百分比：78% × 386 ≈ 301，与真机 390 宽时的 78vw = 304 同比例。
       ⚠️ 只管锁定的预览框；真机手机档（不带 .is-tier-forced）仍然走 78vw。 */
    --imm-cover: min(78%, 380px, 42dvh);
  }
}
</style>
