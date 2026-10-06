<template>
  <!--
    NAS 下载目录设置弹窗。

    ⚠️ **必须独立成组件、在 App.vue 里全局挂一次**，不能只挂在下载抽屉内部：
    「发现」页的「设置目录」按钮（`SearchView.vue`）只做一件事 —— 把
    `downloadManager.showDirModal` 置 true。而弹窗真正渲染的地方原先在
    `DownloadQueuePanel.vue`，那个面板又藏在 `DownloadDrawer` 的
    `v-if="downloadManager.isOpen"` 里：**抽屉没开时没有任何组件渲染这个弹窗**，
    点击看起来就是「按钮点不动」。

    用户 2026-09-22 反馈：「发现音乐订阅后，设置目录无法点击」——
    订阅后的自动补下载走 `addSong(autoOpen=false)`，不会打开抽屉，正好命中。

    注：`DownloadQueuePanel` 同时挂在抽屉、完整队列页、曲库管家里，
    弹窗留在那边还会**重复渲染多份**；提出来顺带解决这个问题。
  -->
  <Teleport to="body">
    <div
      v-if="downloadManager.showDirModal.value"
      class="fixed inset-0 z-60 flex items-center justify-center p-4 bg-black/50 backdrop-blur-xs"
    >
      <div class="bg-white rounded-2xl max-w-md w-full p-5 shadow-2xl border border-gray-100 space-y-4">
        <div class="flex items-center justify-between">
          <h3 class="text-sm font-bold text-gray-800 flex items-center gap-1.5">
            <Folder class="w-4 h-4 text-amber-500" />
            <span>设置歌曲保存到 NAS 目录</span>
          </h3>
          <button
            @click="downloadManager.showDirModal.value = false"
            class="text-gray-400 hover:text-gray-600"
          >
            <X class="w-4 h-4" />
          </button>
        </div>

        <div>
          <label class="block text-xs font-medium text-gray-600 mb-1">存储路径：</label>
          <input
            v-model="downloadManager.editDownloadDir.value"
            type="text"
            placeholder="/vol1/Music"
            class="w-full px-3 py-2 rounded-xl bg-gray-50 border border-gray-200 text-xs font-mono text-gray-800 focus:outline-none focus:border-emerald-400"
          />
        </div>

        <!-- 目录浏览器 -->
        <div>
          <div class="flex items-center justify-between text-xs text-gray-400 mb-1.5">
            <span>NAS 存储卷目录浏览：</span>
            <button
              v-if="downloadManager.browseParentPath.value"
              @click="downloadManager.browseDir(downloadManager.browseParentPath.value)"
              class="text-emerald-600 hover:underline flex items-center gap-0.5"
            >
              <span>↑ 上一级</span>
            </button>
          </div>
          <div class="max-h-48 overflow-y-auto border border-gray-100 rounded-xl bg-gray-50 p-2 space-y-1 divide-y divide-gray-100">
            <div
              v-for="folder in downloadManager.browseFolders.value" :key="folder.path"
              @click="downloadManager.browseDir(folder.path)"
              class="py-1.5 px-2 rounded-lg hover:bg-white cursor-pointer flex items-center justify-between text-xs text-gray-700 transition-colors"
            >
              <div class="flex items-center gap-2 truncate">
                <Folder class="w-3.5 h-3.5 text-amber-400 shrink-0" />
                <span class="truncate">{{ folder.name }}</span>
              </div>
              <span class="text-[10px] text-gray-400 shrink-0 font-mono">{{ folder.audio_count }} 首</span>
            </div>
            <div v-if="!downloadManager.browseFolders.value.length" class="py-4 text-center text-xs text-gray-400">
              当前目录下无子文件夹
            </div>
          </div>
        </div>

        <!-- 保存目录失败：一句原因 + 「查看日志」入口（替代原来 service 层的 alert） -->
        <InlineNotice :text="downloadManager.dirError.value" class="mb-2" />

        <div class="flex justify-end gap-2 pt-2">
          <button
            @click="downloadManager.showDirModal.value = false"
            class="px-4 py-1.5 rounded-xl text-xs text-gray-500 hover:bg-gray-100"
          >
            取消
          </button>
          <button
            @click="downloadManager.saveDownloadDir()"
            :disabled="downloadManager.isSavingDir.value"
            class="px-5 py-1.5 rounded-xl bg-emerald-500 hover:bg-emerald-600 text-white font-semibold text-xs shadow-sm disabled:opacity-50"
          >
            {{ downloadManager.isSavingDir.value ? '保存中...' : '确定设为下载目录' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { downloadManager } from '../services/downloadManager'
import InlineNotice from './InlineNotice.vue'
import { X, Folder } from 'lucide-vue-next'
</script>
