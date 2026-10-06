/* 注入脚本（`backend/pkg/intercept/assets/ui.js`）的纯函数测试。
 *
 * # 为什么测试文件在前端目录、被测文件在后端目录
 *
 * `npm test` 就是 `node --test`，它只扫**当前目录树**（`frontend/`）。注入脚本
 * 住在 Go 那边（`go:embed` 进去发给官方页面），所以这里按相对路径把真文件读进来 ——
 * 测的始终是**要发出去的那份**，不是副本。
 *
 * # 环境
 *
 * 仓库的前端测试**没有 DOM 环境**（没装 jsdom），这里也照旧：只搭一个「刚够
 * 脚本启动」的沙箱。脚本加载时会 boot()，而 boot 只碰 window.fetch、
 * window.XMLHttpRequest、document.body、window.MutationObserver —— 全不给，
 * 它就安静地什么都不做，正好留下那些纯函数给下面测。
 *
 * 这些用例的价值在于**钉住判据**：哪些曲目算在线、歌手字段怎么取、菜单是不是
 * 曲目菜单、偏好开关怎么读。判据漂了，界面上不会报错，只会「该出现的没出现」。
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import vm from 'node:vm';

const here = dirname(fileURLToPath(import.meta.url));
const SRC = join(here, '../../../backend/pkg/intercept/assets/ui.js');
const source = readFileSync(SRC, 'utf8');

// load 把脚本在一个隔离沙箱里跑一遍，返回它的测试钩子。
// 每次都是新的沙箱：脚本里有 `window.__QULV_PAGE_UI__` 幂等判据，
// 复用沙箱会直接被 return 掉，第二条用例就什么也拿不到。
//
// storage 传 'throw' 时，localStorage 的读写会抛 —— 官方页面在隐私模式下
// 就是这样，脚本必须退化成「当没存过」而不是整个挂掉。
function load(storage = {}) {
  const store = new Map(Object.entries(storage));
  const broken = storage === 'throw';
  const sandbox = {
    console,
    setTimeout,
    clearTimeout,
    // 脚本会挂 resize / click / keydown 监听（window 和 document 各一处）
    addEventListener() {},
    localStorage: {
      getItem: (k) => {
        if (broken) throw new Error('localStorage disabled');
        return store.has(k) ? store.get(k) : null;
      },
      setItem: (k, v) => {
        if (broken) throw new Error('localStorage disabled');
        store.set(k, String(v));
      }
    },
    document: {
      readyState: 'complete', // 不是 loading → 直接 boot，不走 DOMContentLoaded
      body: null,             // body 为空：ensureEntry / ensureHost 立刻返回
      documentElement: {},
      addEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
      createElement: () => ({
        style: {},
        classList: { add() {}, contains: () => false },
        setAttribute() {},
        appendChild() {}
      })
    }
  };
  sandbox.window = sandbox; // 脚本读 window.xxx，也直接读 xxx
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox, { filename: 'ui.js' });
  assert.ok(sandbox.__QULV_UI_TEST__, '脚本应挂出测试钩子');
  return sandbox.__QULV_UI_TEST__;
}

// 造一个像官方返回的曲目对象。
function track(over = {}) {
  return Object.assign(
    {
      guid: 'a'.repeat(32),
      title: '甲',
      artists: ['A', 'B'],
      source: 'wy',
      is_online: true
    },
    over
  );
}

// ── 在线判据 ────────────────────────────────────────────────────────────

test('isTruthy 认官方那三种写法，别的一律不算', () => {
  const ui = load();
  for (const v of [true, 'true', 1]) assert.equal(ui.isTruthy(v), true, `${v} 应算真`);
  // 官方接口有的地方给 0/1，有的地方给字符串 —— 但 false 和 'false' 绝不能算真，
  // 否则本地曲目会长出「下载」按钮。
  for (const v of [false, 'false', 0, undefined, null, '']) {
    assert.equal(ui.isTruthy(v), false, `${String(v)} 应算假`);
  }
});

test('guidOf 依次认 guid / trackGuid / id，并把数字转成字符串', () => {
  const ui = load();
  assert.equal(ui.guidOf({ guid: 'g1', trackGuid: 'g2', id: 'g3' }), 'g1');
  assert.equal(ui.guidOf({ trackGuid: 'g2', id: 'g3' }), 'g2');
  assert.equal(ui.guidOf({ id: 'g3' }), 'g3');
  assert.equal(ui.guidOf({ id: 12345 }), '12345');
  assert.equal(ui.guidOf(null), '');
  assert.equal(ui.guidOf({}), '');
});

test('artistsOf 吃掉官方的三种歌手写法', () => {
  const ui = load();
  // 对象数组（官方 track 的常见形状）
  assert.equal(ui.artistsOf({ artists: [{ name: 'A' }, { name: 'B' }] }), 'A / B');
  // 字符串数组
  assert.equal(ui.artistsOf({ artists: ['A', 'B'] }), 'A / B');
  // 逗号串（曲率合并搜索结果时用这个形状）
  assert.equal(ui.artistsOf({ singers: 'A, B' }), 'A, B');
  assert.equal(ui.artistsOf({ artist: 'A' }), 'A');
  assert.equal(ui.artistsOf({}), '');
  // 空数组不能拼出一个空的分隔符出来
  assert.equal(ui.artistsOf({ artists: [] }), '');
});

test('sourceLabel 把平台码翻成人话，认不出的不瞎猜', () => {
  const ui = load();
  assert.equal(ui.sourceLabel('wy'), '网易云音乐');
  assert.equal(ui.sourceLabel('tx'), 'QQ 音乐');
  assert.equal(ui.sourceLabel('kg'), '酷狗音乐');
  assert.equal(ui.sourceLabel('kw'), '酷我音乐');
  // 大小写不敏感
  assert.equal(ui.sourceLabel('WY'), '网易云音乐');
  // 新平台：原样大写返回，不编一个中文名出来
  assert.equal(ui.sourceLabel('mg'), 'MG');
  assert.equal(ui.sourceLabel(''), '');
});

// ── 旁听官方接口 ────────────────────────────────────────────────────────

test('harvest 会钻进官方信封里把曲目挖出来，并记住 is_online', () => {
  const ui = load();
  // 官方信封长这样：{code, msg, data:{list:[…]}}
  ui.harvest(
    {
      code: 0,
      msg: '',
      data: {
        total: 2,
        list: [
          track({ guid: 'o'.repeat(32), title: '在线歌', is_online: true, source: 'tx' }),
          track({ guid: 'l'.repeat(32), title: '本地歌', is_online: false, source: '' })
        ]
      }
    },
    0
  );

  const online = ui.indexOf('o'.repeat(32));
  assert.ok(online, '嵌套在 data.list 里的曲目也该被记下来');
  assert.equal(online.online, true);
  assert.equal(online.title, '在线歌');
  assert.equal(online.source, 'tx');

  const local = ui.indexOf('l'.repeat(32));
  assert.equal(local.online, false, '本地曲目要认出来 —— 它们不该有下载入口');
});

// 这条是 v2.1.81 真机上抓到的 bug 的回归测试。
//
// 真机现象：正在播放一首在线歌曲（面板标题、歌手、封面都对），但「下载到曲率」
// 是灰的。根因：`track/metadata` 的响应里同时有两份曲目 —— 顶层那份是曲率造的
// （带 `is_online: true`），嵌在 `track` 字段里的那份是**官方上游原样透传**的
// （键按字母序、**没有 `is_online`**）。原来的 harvest 是「最后写入者赢」，
// 于是已经认定在线的曲目被后一份覆盖成本地曲目 → 按钮置灰。
test('harvest 只升不降：后来的响应缺 is_online 不能把在线曲目降级', () => {
  const ui = load();
  const guid = 'o'.repeat(32);

  // 1) 曲率自己发的响应（带 is_online: true）
  ui.harvest({ data: { list: [track({ guid, title: '在线歌', is_online: true, source: 'wy' })] } }, 0);
  assert.equal(ui.indexOf(guid).online, true);

  // 2) 官方上游原样透传的那份：**没有** is_online 字段（字段缺失 ≠ 本地曲目）
  ui.harvest({ track: { guid, name: '在线歌', album: { name: '专辑' }, artists: [] } }, 0);
  assert.equal(ui.indexOf(guid).online, true, '缺字段不能把在线曲目降级成本地');
  // 顺带：新记录里为空 / 没有的字段不能把好数据盖掉
  assert.equal(ui.indexOf(guid).source, 'wy', '上游那份没有 source，不能盖掉已知平台');
  assert.equal(ui.indexOf(guid).artists, 'A / B', '上游那份 artists 为空，不能盖掉已知歌手');

  // 3) 显式为假才降级（那才是真的「这不是在线曲目」）
  ui.harvest({ data: { list: [track({ guid, title: '在线歌', is_online: false })] } }, 0);
  assert.equal(ui.indexOf(guid).online, false);
});

test('mergeRecord 是纯函数：缺字段保旧、显式假降级、显式真升格', () => {
  const ui = load();
  const base = { online: true, title: '甲', artists: 'A', source: 'wy' };
  // 缺字段：全都保旧
  // ⚠️ 逐字段比，别用 deepEqual —— mergeRecord 在 vm 沙箱那个 realm 里跑，
  // 返回的对象原型与测试 realm 的不是同一个，deepEqual 会因为原型不同而红。
  const kept = ui.mergeRecord(base, { guid: 'x', name: '甲' });
  assert.equal(kept.online, true);
  assert.equal(kept.title, '甲');
  assert.equal(kept.artists, 'A');
  assert.equal(kept.source, 'wy');
  // 显式 false：降级
  assert.equal(ui.mergeRecord(base, { guid: 'x', name: '甲', is_online: false }).online, false);
  // 显式 true：升格（本地曲目理论上不会走到这里，但语义要对称）
  assert.equal(ui.mergeRecord({ online: false }, { guid: 'x', name: '甲', is_online: true }).online, true);
  // 首次见到、且没字段 → 只能算本地（保守）
  assert.equal(ui.mergeRecord(undefined, { guid: 'x', name: '甲' }).online, false);
  // 新记录带值 → 用新值
  assert.equal(ui.mergeRecord(base, { guid: 'x', name: '乙', source: 'tx' }).title, '乙');
});

test('harvest 只收「有 id 又有标题」的节点', () => {
  const ui = load();
  ui.harvest({ data: { list: [{ guid: 'x'.repeat(32) }, { title: '没有 id' }] } }, 0);
  assert.equal(ui.indexOf('x'.repeat(32)), null, '只有 id、没有标题的不算曲目');
});

test('harvest 不会因为深挖而卡死（有深度上限）', () => {
  const ui = load();
  // 造一棵比上限深得多的树，把曲目埋在底部。
  let node = { guid: 'd'.repeat(32), title: '深埋的歌', is_online: true };
  for (let i = 0; i < 12; i++) node = { wrapper: node };
  ui.harvest(node, 0);
  assert.equal(ui.indexOf('d'.repeat(32)), null, '超过深度上限就不该继续挖');
});

// ── 菜单判据 ────────────────────────────────────────────────────────────

// fakeMenu 造一个假菜单：只需要 querySelectorAll('.semi-dropdown-item')。
function fakeMenu(labels) {
  return {
    querySelectorAll: (sel) =>
      sel === '.semi-dropdown-item'
        ? labels.map((t) => ({ textContent: t, children: [] }))
        : []
  };
}

test('looksLikeTrackMenu 只认曲目菜单，不认页面级「更多」菜单', () => {
  const ui = load();
  // 官方行菜单的真实项（顺序即真机所见）
  assert.equal(
    ui.looksLikeTrackMenu(
      fakeMenu(['播放', '下一首播放', '加入播放列表', '收藏', '添加到歌单', '查看专辑', '查看歌手', '歌曲信息'])
    ),
    true
  );
  // 有「歌曲信息」就够 —— 它是行菜单独有的
  assert.equal(ui.looksLikeTrackMenu(fakeMenu(['播放', '歌曲信息'])), true);
  // 页面级菜单：只有播放/收藏这类，没有「添加到歌单」，不该被认成曲目菜单
  assert.equal(ui.looksLikeTrackMenu(fakeMenu(['播放', '收藏', '分享'])), false);
  assert.equal(ui.looksLikeTrackMenu(fakeMenu([])), false);
  // 防御：没有 querySelectorAll 的节点不能把脚本炸掉
  assert.equal(ui.looksLikeTrackMenu(null), false);
});

test('itemText 把菜单项里的空白压掉（用来比对标签）', () => {
  const ui = load();
  assert.equal(ui.itemText({ textContent: ' 查看\n  专辑 ' }), '查看专辑');
  assert.equal(ui.itemText(null), '');
});

// ── 偏好 ────────────────────────────────────────────────────────────────

test('menuItemAllowed 默认开，只有显式存过 0 才关', () => {
  assert.equal(load().menuItemAllowed(), true, '没存过偏好 → 默认开');
  assert.equal(load({ 'qulv.ui.menu': '1' }).menuItemAllowed(), true);
  assert.equal(load({ 'qulv.ui.menu': '0' }).menuItemAllowed(), false);
  // 存了别的值（脏数据）不该把功能关掉
  assert.equal(load({ 'qulv.ui.menu': 'yes' }).menuItemAllowed(), true);
});

test('localStorage 抛异常时偏好退化成「开」，不把脚本带崩', () => {
  // 官方页面在隐私模式下 localStorage 可能直接抛。这时候必须当作「没存过」，
  // 而不是让 menuItemAllowed 抛出去 —— 它是在 decorate() 里被调的，
  // 抛了就等于整个行菜单注入静默失效。
  assert.equal(load('throw').menuItemAllowed(), true);
});

// ② 「如实标注实际来源」：下载会先去「下载源池」找同名的高音质版本，找到就换源。
// 只报曲目自己那个平台等于骗用户 —— 他以为下的是网易云那份，实际听的是咪咕那份。
test('describeSource 如实报实际来源与音质', () => {
  const t = load();
  assert.equal(t.describeSource({ data: { source: 'mg', quality: '无损' } }), '（咪咕 · 无损）');
  assert.equal(t.describeSource({ data: { source: 'wy', quality: '320k' } }), '（网易云 · 320k）');
  // 没有音质档位时只报来源
  assert.equal(t.describeSource({ data: { source: 'mg' } }), '（咪咕）');
  // 认不出的平台照原样显示 —— 宁可显示得丑，也别显示成错的
  assert.equal(t.describeSource({ data: { source: 'zzz' } }), '（zzz）');
  // 后端没给（老版本 / 异常响应）→ 不加括号，别在 toast 里挂一对空括号
  assert.equal(t.describeSource({}), '');
  assert.equal(t.describeSource({ data: null }), '');
  assert.equal(t.describeSource(null), '');
});
