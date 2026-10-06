// 顶层就要 fs —— 加载期就必须明确失败，错误信息要能直接看懂
const fs = require('node:fs');
globalThis.lx.on(globalThis.lx.EVENT_NAMES.request, () => 'https://example.com/should-never-load');
void fs;
