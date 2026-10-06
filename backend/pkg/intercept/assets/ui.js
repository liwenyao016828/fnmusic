/* 曲率注入脚本 —— 跑在**官方音乐页面**里（`/music/`），由拦截层在返回 HTML 时
 * 于 `</body>` 前插一行 `<script defer src="/music/_qulv/ui.js?v=…">` 带进来。
 *
 * # 它做什么
 *
 *   1. 顶栏一个「曲率」入口（图标按钮，插在官方「设置」左边）；
 *   2. 点开是一块面板：正在播放的歌 + 一键「下载到曲率」、跳转曲率、偏好开关、连接状态；
 *   3. 在线曲目的行菜单（「更多操作」）里补一条「下载到曲率」；
 *   4. 一个 toast 报下载结果。
 *
 * # 四条设计约束（都是想清楚才定的）
 *
 * 1. **不碰官方逻辑**。只新增 DOM（一个入口按钮、一份面板、一个菜单项），
 *    不改官方结构、不给官方元素挂事件、不覆盖官方函数。官方改版时最坏结果
 *    是「我们的东西不出现」，而不是「官方页面坏了」。
 *
 * 2. **不发明颜色**。官方页面自己带一整套设计变量（`--ds-*` / `--semi-*`，
 *    挂在 `:root` 上，见 HANDOVER「官方设计令牌」一节）。这里所有颜色、
 *    圆角、阴影、字体都写成 `var(--ds-xxx, 回退值)` —— 于是注入的界面和官方
 *    是同一套皮肤：官方换主题、换品牌色（`--ds-action-primary-bg`）、换字体，
 *    我们跟着变。回退值只是「变量读不到」时的兜底，不是第二套设计。
 *    自定义属性会穿过 shadow 边界继承，所以面板在影子树里照样读得到。
 *
 * 3. **零轮询**。全是事件驱动：一个 MutationObserver 等菜单/顶栏出现，其余靠
 *    `fetch`/XHR 旁听与点击捕获。没有 setInterval —— 定时器会在这个页面里常驻
 *    跑着，而用户开着音乐页面听歌可能是几小时。
 *
 * 4. **认不出就不插**。本地曲目不该出现「下载」——它们已经在 NAS 上了。
 *    判据来自官方接口自己返回的 `is_online`（见下面的 harvest）。
 *
 * # 为什么靠 coverId 认曲目
 *
 * 官方的行 DOM 上没有任何 id 属性（没有 data-guid），唯一能把「用户点的那一行」
 * 对回曲目的是封面图：`img[src="…/static/cover?coverId=<32hex>&size=…"]`。
 * 曲率在造在线曲目的响应时**让 guid 与 coverId 取同一个值**，所以从封面地址里
 * 抠出来的那串就是曲目 guid。这是曲率自己产、自己用的约定，改的时候要一起改。
 */
(function () {
  'use strict';

  if (window.__QULV_PAGE_UI__) return; // 别插两次（接管层重启、脚本被缓存等）
  window.__QULV_PAGE_UI__ = true;

  // 同源通道的根。**必须是带 /music 前缀的绝对路径**：官方页面挂在
  // `location /music` 下，`/_qulv` 在生产环境根本到不了拦截层。
  var BASE = '/music/_qulv';
  var MARK = 'qulv-dl-item'; // 我们插进菜单的那一条自己的类名（避免观察器自激）
  var ENTRY_ATTR = 'data-qulv-entry';
  var LABEL = '下载到曲率';
  var PREF_MENU = 'qulv.ui.menu'; // 「行菜单里显示下载项」，默认开

  /* ── 0. 设计令牌 ──────────────────────────────────────────────────── */

  var T = {
    font: 'var(--ds-font-family-base, Montserrat, -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif)',
    text: 'var(--ds-text-primary, #fff)',
    text2: 'var(--ds-text-secondary, #fffc)',
    text3: 'var(--ds-text-tertiary, #fff9)',
    text4: 'var(--ds-text-quaternary, #fff6)',
    brand: 'var(--ds-action-primary-bg, #f62c55)',
    brandSoft: 'var(--ds-action-primary-soft, rgba(246, 44, 85, .16))',
    brandBorder: 'var(--ds-action-primary-border, rgba(246, 44, 85, .46))',
    popupBg: 'var(--ds-bg-dropdown, #000000e6)',
    border: 'var(--ds-border-dropdown, #fff3)',
    borderSoft: 'var(--ds-border-default, #ffffff1f)',
    rowBg: 'var(--ds-bg-list-item, #ffffff0f)',
    rowHover: 'var(--ds-bg-list-item-hover, #ffffff14)',
    ctrlBg: 'var(--ds-bg-button-primary, #ffffff14)',
    ctrlBgHover: 'var(--ds-bg-button-primary-hover, #ffffff1f)',
    shadow: 'var(--ds-shadow-dropdown, 0 8px 32px #00000080)',
    blur: 'var(--ds-player-glass-filter, blur(12px))',
    ok: 'var(--ds-special-success, #6bab45)',
    err: 'var(--ds-special-danger, #f62c55)',
    warn: 'var(--ds-special-warning, #f8bf28)',
    switchOn: 'var(--semi-color-switch-bg-on, #f93d63)',
    switchOff: 'var(--semi-color-switch-bg-off, #ffffff14)',
    knob: 'var(--ds-bg-toggle-knob, #fff)'
  };

  // guid -> {online:bool, title:string, artists:string, source:string}
  // 只记官方接口自己吐出来的曲目，不去猜。
  var index = Object.create(null);

  function isTruthy(v) { return v === true || v === 'true' || v === 1; }

  function guidOf(t) {
    if (!t) return '';
    var g = t.guid || t.trackGuid || t.id || '';
    return typeof g === 'string' ? g : String(g);
  }

  // 官方曲目对象里的歌手：可能是数组、也可能是逗号串。
  function artistsOf(t) {
    var a = t.artists || t.singers || t.artist;
    if (Array.isArray(a)) {
      var out = [];
      for (var i = 0; i < a.length; i++) {
        var one = a[i];
        if (one && typeof one === 'object') one = one.name || one.title || '';
        if (one) out.push(String(one));
      }
      return out.join(' / ');
    }
    return a ? String(a) : '';
  }

  var SOURCE_LABEL = { wy: '网易云音乐', tx: 'QQ 音乐', kg: '酷狗音乐', kw: '酷我音乐' };

  function sourceLabel(s) {
    s = String(s || '').toLowerCase();
    return SOURCE_LABEL[s] || s.toUpperCase();
  }

  /* ── 1. 旁听官方接口返回的曲目 ──────────────────────────────────────── */

  // mergeRecord 合并「同一个 guid 的另一份说法」—— 规则是**只升不降**。
  //
  // 为什么不能「最后写入者赢」（v2.1.81 真机上栽过）：同一个 guid 会在好几个响应里
  // 出现，而其中一份是**官方上游自己原样透传**的 track 对象 —— `track/metadata`
  // 的响应里嵌着一个 `track` 字段，就是上游那一份，它**没有** `is_online`。
  // 最后写入者赢的话，已经认定在线的曲目会被它降级成本地曲目；真机现象是
  // 「正在播放一首在线歌曲，面板里的下载按钮却是灰的」（`is_online` 字段在
  // 官方前端里一次都没被读过，所以只有我们这边会踩）。
  //
  // 判据：`is_online` 为真 = 权威的「在线」；**字段缺失**只是「这个响应没说」，
  // 不能拿来否定已知事实；只有**显式**为假才降级。
  // 标题 / 歌手 / 来源同理：新记录里为空就保留旧的，别用空串盖掉好数据。
  function mergeRecord(prev, node) {
    var has = ('is_online' in node) || ('isOnline' in node);
    var flag = isTruthy(node.is_online) || isTruthy(node.isOnline);
    var online = flag ? true : (has ? false : (prev ? prev.online : false));
    var title = String(node.title || node.name || '');
    var artists = artistsOf(node);
    var source = String(node.source || '');
    return {
      online: online,
      title: title || (prev ? prev.title : ''),
      artists: artists || (prev ? prev.artists : ''),
      source: source || (prev ? prev.source : '')
    };
  }

  function harvest(node, depth) {
    if (!node || typeof node !== 'object' || depth > 6) return;
    if (Array.isArray(node)) {
      for (var i = 0; i < node.length; i++) harvest(node[i], depth + 1);
      return;
    }
    var g = guidOf(node);
    if (g && (node.title || node.name)) {
      index[g] = mergeRecord(index[g], node);
    }
    for (var k in node) {
      if (Object.prototype.hasOwnProperty.call(node, k)) harvest(node[k], depth + 1);
    }
  }

  function harvestText(text) {
    if (!text || text.length > 4 * 1024 * 1024) return;
    var body;
    try { body = JSON.parse(text); } catch (e) { return; }
    harvest(body, 0);
  }

  function isAPIPath(url) {
    return typeof url === 'string' && url.indexOf('/api/v1/') >= 0;
  }

  // 包装 fetch：只旁听，不做任何改写（返回值原样给官方）。
  function wrapFetch() {
    if (typeof window.fetch !== 'function') return;
    var orig = window.fetch;
    window.fetch = function (input) {
      var url = typeof input === 'string' ? input : (input && input.url);
      var promise = orig.apply(this, arguments);
      if (isAPIPath(url) && promise && typeof promise.then === 'function') {
        promise.then(function (res) {
          try {
            if (res && typeof res.clone === 'function') {
              res.clone().text().then(harvestText, function () {});
            }
          } catch (e) { /* 旁听失败不影响官方 */ }
        }, function () {});
      }
      return promise;
    };
  }

  // 包装 XHR：axios 之类的库默认走 XHR，只包 fetch 会漏一半。
  function wrapXHR() {
    var XHR = window.XMLHttpRequest;
    if (!XHR || !XHR.prototype) return;
    var open = XHR.prototype.open;
    var send = XHR.prototype.send;
    XHR.prototype.open = function (method, url) {
      try { this.__qulvUrl = url; } catch (e) {}
      return open.apply(this, arguments);
    };
    XHR.prototype.send = function () {
      var self = this;
      try {
        if (isAPIPath(self.__qulvUrl)) {
          self.addEventListener('load', function () {
            try {
              if (self.responseType === '' || self.responseType === 'text') {
                harvestText(self.responseText);
              } else if (self.responseType === 'json') {
                harvest(self.response, 0);
              }
            } catch (e) { /* 同上 */ }
          });
        }
      } catch (e) {}
      return send.apply(this, arguments);
    };
  }

  /* ── 2. 认出「用户点的是哪一行」 ─────────────────────────────────────── */

  function guidFromRow(row) {
    if (!row || !row.querySelector) return '';
    var img = row.querySelector('img[src*="coverId="]');
    if (!img) return '';
    var src = img.getAttribute('src') || '';
    var m = /[?&]coverId=([^&]+)/.exec(src);
    return m ? decodeURIComponent(m[1]) : '';
  }

  var menuGUID = ''; // 「更多操作」刚点开的那一行

  document.addEventListener('click', function (e) {
    var t = e.target;
    if (!t || typeof t.closest !== 'function') return;
    var btn = t.closest('[aria-label="更多操作"]');
    if (!btn) return;
    var row = btn.closest('[data-index]') || btn.closest('tr');
    menuGUID = guidFromRow(row) || guidFromAncestors(btn);
    // 菜单可能已经被半设计挂好（节点复用，不会再触发 addedNodes），这里补一次。
    // 放到下一轮事件循环：React 此刻还没提交渲染，现在查还是上一行的菜单。
    setTimeout(decorateVisibleMenus, 0);
  }, true);

  // 兜底：行的 data-index 容器不在正上方时，向上再找几层。
  function guidFromAncestors(btn) {
    var el = btn;
    for (var i = 0; i < 6 && el; i++, el = el.parentElement) {
      var g = guidFromRow(el);
      if (g) return g;
    }
    return '';
  }

  // 正在播放的那一首：官方底部播放条是一块固定宽度的浮层，里面同样用封面图
  // 标识曲目。空着（没在放歌）时拿不到封面，返回空串 —— 面板据此把下载按钮置灰。
  function nowPlaying() {
    var prev = document.querySelector('button[aria-label="上一首"]');
    if (!prev) return null;
    var pill = prev;
    for (var i = 0; i < 6 && pill; i++) {
      var w = pill.getBoundingClientRect().width;
      if (w > 600 && w < 1200) break;
      pill = pill.parentElement;
    }
    if (!pill) return null;
    var guid = guidFromRow(pill);
    if (!guid) return null;
    var rec = index[guid] || {};
    var title = rec.title || '';
    if (!title) {
      // 旁听没拿到（例如刷新后还没发过接口请求）：从播放条的文字里取第一行。
      var lines = String(pill.innerText || '').split('\n');
      for (var j = 0; j < lines.length; j++) {
        if (lines[j].trim()) { title = lines[j].trim(); break; }
      }
    }
    return { guid: guid, rec: rec, title: title, online: rec.online !== false };
  }

  /* ── 3. 往行菜单里补一条 ────────────────────────────────────────────── */

  function itemText(el) {
    return (el && el.textContent ? el.textContent : '').replace(/\s+/g, '');
  }

  // 只认「曲目菜单」。页面级那个「更多」菜单里没有这几项，别往那儿插。
  function looksLikeTrackMenu(menu) {
    // 判据函数自己也要能接住 null：调用方里就有「向上找祖先、可能找不到」的
    // 那种写法，让这里抛出去就等于整个注入静默失效。
    if (!menu || !menu.querySelectorAll) return false;
    var items = menu.querySelectorAll('.semi-dropdown-item');
    var hasInfo = false, hasFav = false, hasAdd = false;
    for (var i = 0; i < items.length; i++) {
      var s = itemText(items[i]);
      if (s === '歌曲信息') hasInfo = true;
      if (s === '收藏') hasFav = true;
      if (s === '添加到歌单') hasAdd = true;
    }
    return hasInfo || (hasFav && hasAdd);
  }

  function setLabel(node, text) {
    // 半设计的类名是构建产物（`span.block.min-w-0.truncate.whitespace-nowrap`），
    // 换个版本就可能变 —— 所以退化成「找最后一个有文字、且没有子元素的节点」。
    var cands = node.querySelectorAll('span,div');
    for (var i = cands.length - 1; i >= 0; i--) {
      if (cands[i].children.length === 0 && cands[i].textContent.trim()) {
        cands[i].textContent = text;
        return;
      }
    }
    node.textContent = text;
  }

  function menuItemAllowed() {
    try { return localStorage.getItem(PREF_MENU) !== '0'; } catch (e) { return true; }
  }

  function decorate(menu) {
    if (!menu || !looksLikeTrackMenu(menu)) return;

    // 同一个菜单节点会被复用（换一行再点，还是这个节点），先清掉上一轮的。
    var mine = menu.querySelectorAll('.' + MARK);
    for (var i = 0; i < mine.length; i++) {
      if (mine[i].parentNode) mine[i].parentNode.removeChild(mine[i]);
    }

    if (!menuItemAllowed() || !menuGUID) return;
    var rec = index[menuGUID];
    // 明确知道是本地曲目 → 不插。认不出来（没旁听到接口）→ 插，让后端把关：
    // 后端只认自己发出去的 id，认不出会明确说「未登记」，不会误下别人的歌。
    if (rec && !rec.online) return;

    var items = menu.querySelectorAll('.semi-dropdown-item');
    if (!items.length) return;

    var proto = items[items.length - 1]; // 克隆现成的一条，样式/结构天然一致
    var node = proto.cloneNode(true);
    node.classList.add(MARK);
    node.removeAttribute('id');
    setLabel(node, LABEL);

    var anchor = null;
    for (var j = 0; j < items.length; j++) {
      if (itemText(items[j]) === '查看专辑') { anchor = items[j]; break; }
    }
    if (anchor && anchor.parentNode) anchor.parentNode.insertBefore(node, anchor);
    else menu.appendChild(node);

    // 点击时现读 menu.dataset.qulvGuid（而不是闭包捕获）：菜单节点复用换行时，
    // 闭包里的旧 guid 会把歌下错。
    node.addEventListener('click', function (e) {
      e.preventDefault();
      download(menu.dataset.qulvGuid || menuGUID);
    });

    menu.dataset.qulvGuid = menuGUID;
  }

  function decorateVisibleMenus() {
    var menus = document.querySelectorAll('.semi-dropdown-menu');
    for (var i = 0; i < menus.length; i++) decorate(menus[i]);
  }

  /* ── 4. 影子 DOM 宿主：面板 + toast ─────────────────────────────────
   *
   * 为什么整个界面住在一个 shadow root 里：官方页面是 Tailwind + Semi Design，
   * 全局 reset 与组件样式一大堆。隔离开之后我们这点东西不用去跟谁比
   * `!important` 谁更狠；而 `--ds-*` 这类自定义属性会**继承穿过**影子边界，
   * 所以「隔离样式」和「共用皮肤」两件事同时成立。
   */

  var host = null, root = null, panel = null, toastEl = null, toastTimer = 0;
  var info = null;          // /api/info 的结果（版本、曲率地址）
  var infoState = 'idle';   // idle | ok | fail
  var dlState = 'idle';     // idle | busy
  var open = false;

  function css() {
    return '' +
      '.wrap{font-family:' + T.font + ';color:' + T.text + ';font-size:13px;line-height:1.5;' +
        '-webkit-font-smoothing:antialiased}' +
      '.wrap *{box-sizing:border-box;margin:0;padding:0}' +

      /* 面板：用的是官方下拉/弹层的皮肤（同色、同边框、同阴影、同模糊） */
      '.panel{position:fixed;z-index:2147483646;width:320px;padding:14px 14px 12px;' +
        'background:' + T.popupBg + ';border:1px solid ' + T.border + ';border-radius:12px;' +
        'box-shadow:' + T.shadow + ';backdrop-filter:' + T.blur + ';-webkit-backdrop-filter:' + T.blur + ';' +
        'opacity:0;transform:translateY(-6px) scale(.98);pointer-events:none;' +
        'transition:opacity .16s ease,transform .16s cubic-bezier(.22,1,.36,1)}' +
      '.panel.show{opacity:1;transform:none;pointer-events:auto}' +

      '.hd{display:flex;align-items:center;gap:8px;margin-bottom:12px}' +
      '.mark{width:22px;height:22px;border-radius:7px;flex:none;display:flex;align-items:center;' +
        'justify-content:center;background:' + T.brand + ';color:var(--ds-text-on-accent,#fff)}' +
      '.mark svg{width:14px;height:14px}' +
      '.ttl{font-weight:700;font-size:14px;letter-spacing:.2px}' +
      '.ver{font-size:11px;color:' + T.text4 + ';margin-left:auto;font-variant-numeric:tabular-nums}' +
      '.x{width:22px;height:22px;border:0;border-radius:6px;background:transparent;color:' + T.text3 + ';' +
        'cursor:pointer;font-size:15px;line-height:1;display:flex;align-items:center;justify-content:center}' +
      '.x:hover{background:' + T.ctrlBg + ';color:' + T.text + '}' +

      '.sec{padding:10px 0;border-top:1px solid ' + T.borderSoft + '}' +
      '.sec:first-of-type{border-top:0;padding-top:2px}' +
      '.h4{font-size:11px;font-weight:600;color:' + T.text4 + ';letter-spacing:.6px;margin-bottom:8px}' +

      /* 正在播放：封面 + 两行文字，和官方列表项同一套排布 */
      '.now{display:flex;align-items:center;gap:10px;padding:8px;border-radius:8px;background:' + T.rowBg + ';margin-bottom:8px}' +
      '.cov{width:36px;height:36px;border-radius:6px;flex:none;object-fit:cover;background:' + T.ctrlBg + '}' +
      '.cov.ph{display:flex;align-items:center;justify-content:center;color:' + T.text4 + '}' +
      '.meta{min-width:0;flex:1}' +
      '.mt{font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}' +
      '.ms{font-size:11px;color:' + T.text4 + ';white-space:nowrap;overflow:hidden;text-overflow:ellipsis;margin-top:2px}' +

      '.btn{width:100%;border:1px solid ' + T.borderSoft + ';background:' + T.ctrlBg + ';color:' + T.text + ';' +
        'border-radius:8px;padding:8px 12px;font-family:inherit;font-size:13px;font-weight:600;cursor:pointer;' +
        'display:flex;align-items:center;justify-content:center;gap:6px;transition:background .15s,border-color .15s}' +
      '.btn:hover:not(:disabled){background:' + T.ctrlBgHover + ';border-color:' + T.border + '}' +
      '.btn:disabled{opacity:.45;cursor:default}' +
      '.btn.pri{background:' + T.brand + ';border-color:' + T.brand + ';color:var(--ds-text-on-accent,#fff)}' +
      '.btn.pri:hover:not(:disabled){filter:brightness(1.08);background:' + T.brand + '}' +
      '.btn.pri:disabled{background:' + T.brandSoft + ';border-color:' + T.brandBorder + ';color:' + T.text3 + '}' +
      '.btn svg{width:14px;height:14px;flex:none}' +

      '.hint{font-size:11px;color:' + T.text4 + ';margin-top:8px;line-height:1.5}' +

      /* 开关：配色跟官方（开启用品牌色）*/
      '.swrow{display:flex;align-items:center;justify-content:space-between;gap:12px;cursor:pointer}' +
      '.swrow span.lb{font-size:13px}' +
      '.sw{width:34px;height:20px;border-radius:999px;background:' + T.switchOff + ';flex:none;position:relative;' +
        'transition:background .16s}' +
      '.sw::after{content:"";position:absolute;top:2px;left:2px;width:16px;height:16px;border-radius:50%;' +
        'background:' + T.knob + ';transition:transform .16s cubic-bezier(.22,1,.36,1)}' +
      '.sw.on{background:' + T.switchOn + '}' +
      '.sw.on::after{transform:translateX(14px)}' +

      '.ft{display:flex;align-items:center;gap:6px;margin-top:12px;padding-top:10px;' +
        'border-top:1px solid ' + T.borderSoft + ';font-size:11px;color:' + T.text4 + '}' +
      '.dot{width:6px;height:6px;border-radius:50%;background:' + T.text4 + ';flex:none}' +
      '.dot.ok{background:' + T.ok + '}' +
      '.dot.err{background:' + T.err + '}' +

      /* toast：顶部居中（官方 Semi 的 Toast 也在那儿），压暗底 + 同款模糊 */
      '.toast{position:fixed;top:18px;left:50%;transform:translateX(-50%) translateY(-8px);z-index:2147483647;' +
        'max-width:min(420px,86vw);padding:9px 16px;border-radius:10px;font-size:13px;' +
        'background:' + T.popupBg + ';border:1px solid ' + T.border + ';box-shadow:' + T.shadow + ';' +
        'backdrop-filter:' + T.blur + ';-webkit-backdrop-filter:' + T.blur + ';' +
        'opacity:0;pointer-events:none;transition:opacity .18s ease,transform .18s cubic-bezier(.22,1,.36,1);' +
        'word-break:break-word}' +
      '.toast.show{opacity:1;transform:translateX(-50%)}' +
      '.toast.ok{border-color:' + T.ok + '}' +
      '.toast.err{border-color:' + T.err + '}';

    // ⚠️ 注意上面每条规则都带 `.wrap` / 具名类前缀：影子树里只有我们自己的
    // 元素，但保留前缀是为了「万一哪天改成挂在 light DOM 也能用」。
  }

  function ensureHost() {
    if (host) return true;
    if (!document.body) return false;
    host = document.createElement('div');
    host.setAttribute('data-qulv-ui', '');
    host.style.cssText = 'position:fixed;inset:0;z-index:2147483646;pointer-events:none';
    var r = host;
    if (host.attachShadow) {
      try { r = host.attachShadow({ mode: 'open' }); } catch (e) { r = host; }
    }
    if (r !== host) {
      var st = document.createElement('style');
      st.textContent = css();
      r.appendChild(st);
    }
    var wrap = document.createElement('div');
    wrap.className = 'wrap';
    // 没有 shadow DOM 的老内核：退回行内样式没有意义（整块面板都靠这段 CSS），
    // 所以这里只保证不抛错 —— 面板干脆不出现，页面照旧。
    if (r !== host) r.appendChild(wrap);
    panel = buildPanel();
    toastEl = document.createElement('div');
    toastEl.className = 'toast';
    wrap.appendChild(panel);
    wrap.appendChild(toastEl);
    document.body.appendChild(host);
    root = r;
    return true;
  }

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }

  var ICON_WAVE = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" ' +
    'stroke-linecap="round" stroke-linejoin="round"><path d="M1.5 8h2.2l1.5-4.2 2.1 8.4 1.7-5.4 1.4 3.2h4.1"/></svg>';

  function buildPanel() {
    var p = el('div', 'panel');

    var hd = el('div', 'hd');
    var mark = el('div', 'mark');
    mark.innerHTML = ICON_WAVE;
    hd.appendChild(mark);
    hd.appendChild(el('div', 'ttl', '曲率'));
    var ver = el('div', 'ver', '');
    hd.appendChild(ver);
    var x = el('button', 'x', '✕');
    x.setAttribute('aria-label', '关闭');
    x.addEventListener('click', function () { toggle(false); });
    hd.appendChild(x);
    p.appendChild(hd);

    // ── 正在播放 + 下载 ──
    var s1 = el('div', 'sec');
    s1.appendChild(el('div', 'h4', '正在播放'));
    var now = el('div', 'now');
    var cov = el('img', 'cov');
    cov.alt = '';
    cov.hidden = true;
    var ph = el('div', 'cov ph');
    ph.innerHTML = ICON_WAVE;
    var meta = el('div', 'meta');
    var mt = el('div', 'mt', '没有正在播放的歌曲');
    var ms = el('div', 'ms', '在飞牛音乐里播一首在线歌曲');
    meta.appendChild(mt);
    meta.appendChild(ms);
    now.appendChild(cov);
    now.appendChild(ph);
    now.appendChild(meta);
    s1.appendChild(now);
    var dl = el('button', 'btn pri');
    dl.innerHTML = ICON_WAVE + '<span>下载到曲率</span>';
    dl.disabled = true;
    dl.addEventListener('click', function () {
      var np = nowPlaying();
      if (np && np.guid) download(np.guid, refreshNow);
    });
    s1.appendChild(dl);
    p.appendChild(s1);

    // ── 跳转 ──
    var s2 = el('div', 'sec');
    s2.appendChild(el('div', 'h4', '跳转'));
    var go = el('button', 'btn');
    go.innerHTML = '<span>打开曲率</span><svg viewBox="0 0 16 16" fill="none" stroke="currentColor" ' +
      'stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3h7v7M13 3l-8 8M11 11v2H3V5h2"/></svg>';
    go.disabled = true;
    go.addEventListener('click', function () {
      if (info && info.qulv_url) window.open(info.qulv_url, '_blank', 'noopener');
    });
    s2.appendChild(go);
    s2.appendChild(el('p', 'hint', '曲率里还能批量下载、整理曲库、管理榜单。'));
    p.appendChild(s2);

    // ── 偏好 ──
    var s3 = el('div', 'sec');
    s3.appendChild(el('div', 'h4', '偏好'));
    var row = el('div', 'swrow');
    row.appendChild(el('span', 'lb', '行菜单显示「下载到曲率」'));
    var sw = el('span', 'sw');
    sw.setAttribute('role', 'switch');
    row.appendChild(sw);
    row.addEventListener('click', function () {
      var on = sw.classList.toggle('on');
      sw.setAttribute('aria-checked', on ? 'true' : 'false');
      try { localStorage.setItem(PREF_MENU, on ? '1' : '0'); } catch (e) {}
      if (!on) {
        // 关掉之后，已经展开的菜单里那一条要立刻消失（否则「关了还在」）
        var mine = document.querySelectorAll('.' + MARK);
        for (var i = 0; i < mine.length; i++) {
          if (mine[i].parentNode) mine[i].parentNode.removeChild(mine[i]);
        }
      } else {
        decorateVisibleMenus();
      }
    });
    s3.appendChild(row);
    p.appendChild(s3);

    // ── 状态 ──
    var ft = el('div', 'ft');
    var dot = el('span', 'dot');
    var state = el('span', 'st', '正在连接曲率…');
    ft.appendChild(dot);
    ft.appendChild(state);
    p.appendChild(ft);

    p.__q = { ver: ver, cov: cov, ph: ph, mt: mt, ms: ms, dl: dl, go: go, sw: sw, dot: dot, state: state };
    return p;
  }

  /* ── 5. 顶栏入口 ─────────────────────────────────────────────────────
   *
   * 克隆官方「设置」那颗按钮再改内容 —— 于是高度、圆角、hover、焦点圈、
   * 与相邻元素的间距全都是官方的，不是我照着抄的。
   *
   * 代价：React 不认这颗节点，重渲染时可能把它摘掉。所以它是**幂等**的
   * （先查存在性再插），并由下面那个 MutationObserver 顺手补回来。
   */

  function entryNode() {
    return document.querySelector('[' + ENTRY_ATTR + ']');
  }

  function ensureEntry() {
    if (!document.body) return;
    if (entryNode()) return;
    var gear = document.querySelector('button[aria-label="设置"]');
    if (!gear || !gear.parentNode) return;
    var btn = gear.cloneNode(true);
    btn.setAttribute(ENTRY_ATTR, '');
    btn.removeAttribute('id');
    btn.setAttribute('aria-label', '曲率');
    btn.setAttribute('title', '曲率 —— 打开面板');
    // 换掉里面的齿轮图标，换成曲率自己的波形标
    var svgs = btn.querySelectorAll('svg');
    for (var i = 0; i < svgs.length; i++) {
      var box = svgs[i].parentNode;
      if (box) box.removeChild(svgs[i]);
    }
    var holder = document.createElement('span');
    holder.style.cssText = 'display:flex;align-items:center;justify-content:center;width:16px;height:16px';
    holder.innerHTML = ICON_WAVE;
    btn.appendChild(holder);
    // 品牌色小圆点：让它在官方那一排里「是自己的」，但不刺眼
    var dot = document.createElement('span');
    dot.style.cssText = 'position:absolute;right:2px;bottom:2px;width:5px;height:5px;border-radius:50%;' +
      'background:' + T.brand;
    if (getComputedStyle(btn).position === 'static') btn.style.position = 'relative';
    btn.appendChild(dot);
    btn.addEventListener('click', function (e) {
      e.preventDefault();
      e.stopPropagation();
      toggle();
    });
    gear.parentNode.insertBefore(btn, gear);
  }

  /* ── 6. 面板开合与刷新 ──────────────────────────────────────────────── */

  function place() {
    if (!panel) return;
    var anchor = entryNode();
    var r = anchor ? anchor.getBoundingClientRect() : null;
    var top = r ? Math.round(r.bottom + 8) : 56;
    var right = r ? Math.round(window.innerWidth - r.right) : 24;
    if (right < 12) right = 12;
    if (right + 320 > window.innerWidth) right = 12;
    panel.style.top = top + 'px';
    panel.style.right = right + 'px';
  }

  function refreshNow() {
    if (!panel) return;
    var q = panel.__q;
    var np = nowPlaying();
    var can = !!(np && np.guid && np.online);
    if (np && np.guid) {
      q.mt.textContent = np.title || '这首歌';
      var bits = [];
      if (np.rec.artists) bits.push(np.rec.artists);
      if (np.rec.source) bits.push(sourceLabel(np.rec.source));
      q.ms.textContent = bits.join(' · ') || '在线曲目';
      q.cov.src = BASE.replace(/\/_qulv$/, '/api/v1/static/cover') +
        '?coverId=' + encodeURIComponent(np.guid) + '&size=160';
      q.cov.hidden = false;
      q.ph.hidden = true;
    } else {
      q.mt.textContent = '没有正在播放的歌曲';
      q.ms.textContent = '在飞牛音乐里播一首在线歌曲';
      q.cov.hidden = true;
      q.ph.hidden = false;
    }
    if (dlState === 'busy') {
      q.dl.disabled = true;
      q.dl.querySelector('span').textContent = '正在下载…';
    } else {
      q.dl.disabled = !can;
      q.dl.querySelector('span').textContent = '下载到曲率';
      q.dl.title = can ? '' : '本地曲目不用下载（已经在 NAS 上了）';
    }
  }

  function refreshInfo() {
    if (!panel) return;
    var q = panel.__q;
    if (infoState === 'ok' && info) {
      q.ver.textContent = info.version ? 'v' + info.version : '';
      q.go.disabled = !info.qulv_url;
      q.dot.className = 'dot ok';
      q.state.textContent = '已连接曲率' + (info.version ? ' · v' + info.version : '');
    } else if (infoState === 'fail') {
      q.dot.className = 'dot err';
      q.state.textContent = '曲率没响应 —— 检查应用是否在运行';
    } else {
      q.dot.className = 'dot';
      q.state.textContent = '正在连接曲率…';
    }
  }

  function refreshPref() {
    if (!panel) return;
    var q = panel.__q;
    var on = menuItemAllowed();
    q.sw.className = on ? 'sw on' : 'sw';
    q.sw.setAttribute('aria-checked', on ? 'true' : 'false');
  }

  function toggle(force) {
    var next = force === undefined ? !open : !!force;
    if (!next) {
      open = false;
      if (panel) panel.classList.remove('show');
      return;
    }
    if (!ensureHost()) return;
    open = true;
    place();
    refreshPref();
    refreshNow();
    refreshInfo();
    panel.classList.add('show');
    loadInfo();
  }

  function loadInfo() {
    if (infoState === 'ok') return;
    fetch(BASE + '/api/info', { credentials: 'same-origin' })
      .then(function (res) { return res.json(); })
      .then(function (body) {
        var d = (body && (body.data || body)) || {};
        info = { version: d.version || '', qulv_url: d.qulv_url || '' };
        infoState = 'ok';
        refreshInfo();
      })
      .catch(function () {
        infoState = 'fail';
        refreshInfo();
      });
  }

  document.addEventListener('click', function (e) {
    if (!open) return;
    var t = e.target;
    if (t && typeof t.closest === 'function' && (t.closest('[data-qulv-ui]') || t.closest('[' + ENTRY_ATTR + ']'))) return;
    toggle(false);
  }, true);

  document.addEventListener('keydown', function (e) {
    if (open && e.key === 'Escape') toggle(false);
  }, true);

  window.addEventListener('resize', function () { if (open) place(); });

  /* ── 7. toast ─────────────────────────────────────────────────────── */

  function toast(text, kind) {
    if (!ensureHost()) return;
    toastEl.textContent = text;
    toastEl.className = 'toast show' + (kind ? ' ' + kind : '');
    if (toastTimer) clearTimeout(toastTimer);
    // 失败信息留久一点：用户要读完才知道下一步该干什么。
    toastTimer = setTimeout(function () {
      toastEl.className = 'toast' + (kind ? ' ' + kind : '');
    }, kind === 'err' ? 9000 : 4000);
  }

  /* ── 8. 下载 ───────────────────────────────────────────────────────── */

  // describeSource 把「实际从哪儿、什么音质下来的」拼成一句人话。
  //
  // ⚠️ 必须报**实际**来源：下载会先去「下载源池」里找同名的高音质版本，找到就换源
  // （见 pkg/intercept/acquire.go）。只报曲目自己那个平台等于骗用户 ——
  // 他以为下的是网易云那份，实际听的是咪咕那份。
  var PLATFORM_LABEL = {
    wy: '网易云', tx: 'QQ音乐', kg: '酷狗', kw: '酷我',
    mg: '咪咕', bq: '千千', bi: 'B站'
  };
  function describeSource(body) {
    var d = (body && body.data) || {};
    var src = d.source || '';
    var q = d.quality || '';
    if (!src && !q) return '';
    var label = PLATFORM_LABEL[src] || src || '未知来源';
    return '（' + label + (q ? ' · ' + q : '') + '）';
  }

  function download(guid, done) {
    if (!guid) { toast('没认出这首歌，请重新打开一次行菜单', 'err'); return; }
    var rec = index[guid] || {};
    var name = rec.title ? '《' + rec.title + '》' : '这首歌';
    dlState = 'busy';
    refreshNow();
    toast('正在下载 ' + name + '，文件大时可能要等一会儿…', '');

    // 下载是同步长请求（解析 + 抓取 + 写标签 + 落盘），后端给了 5 分钟预算。
    fetch(BASE + '/api/download/online', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ guid: guid })
    }).then(function (res) {
      return res.json().catch(function () { return null; }).then(function (body) {
        if (!res.ok) {
          throw new Error((body && (body.message || body.msg)) || ('接口返回 HTTP ' + res.status));
        }
        if (body && body.code !== undefined && body.code !== 0 && body.code !== 200) {
          throw new Error(body.message || body.msg || ('code ' + body.code));
        }
        return body || {};
      });
    }).then(function (body) {
      toast('已下载到曲率曲库' + describeSource(body), 'ok');
    }).catch(function (err) {
      toast('下载失败：' + ((err && err.message) || err), 'err');
    }).then(function () {
      dlState = 'idle';
      if (done) done(); else refreshNow();
    });
  }

  /* ── 9. 启动 ──────────────────────────────────────────────────────── */

  var pendingMenu = null, scheduled = false;

  function schedule(menu) {
    pendingMenu = menu;
    if (scheduled) return;
    scheduled = true;
    // 合并同一批 DOM 变更：一次菜单展开会带来好几条记录。
    setTimeout(function () {
      scheduled = false;
      var m = pendingMenu;
      pendingMenu = null;
      decorate(m);
    }, 0);
  }

  function watch() {
    if (!window.MutationObserver) { ensureEntry(); return; }
    new MutationObserver(function (records) {
      for (var i = 0; i < records.length; i++) {
        var rec = records[i];
        var added = rec.addedNodes;
        for (var j = 0; j < added.length; j++) {
          var n = added[j];
          if (!n || n.nodeType !== 1) continue;
          // 自己插的那一条也会触发观察器 —— 不排除它就会和上面「先删后插」互相激。
          if (n.classList && n.classList.contains(MARK)) continue;
          if (n.closest && n.closest('.' + MARK)) continue;
          if (n.classList && n.classList.contains('semi-dropdown-menu')) { schedule(n); continue; }
          if (n.closest && n.closest('.semi-dropdown-menu')) { schedule(n.closest('.semi-dropdown-menu')); continue; }
          if (n.querySelector) {
            var inner = n.querySelector('.semi-dropdown-menu');
            if (inner) schedule(inner);
          }
        }
      }
      // 顶栏入口按钮是幂等的：存在就什么都不做。放在每批变更末尾查一次，
      // 一并覆盖两种「它不在了」——官方重渲染把它摘掉、以及首屏时官方还没挂载完。
      if (!entryNode()) ensureEntry();
    }).observe(document.documentElement, { childList: true, subtree: true });
  }

  function boot() {
    try { wrapFetch(); } catch (e) {}
    try { wrapXHR(); } catch (e) {}
    try { ensureEntry(); } catch (e) {}
    try { watch(); } catch (e) {}
    // 官方 SPA 挂载之后顶栏才存在：首屏补一次，之后交给观察器。
    setTimeout(function () { try { ensureEntry(); } catch (e) {} }, 1200);
  }

  /* ── 10. 测试钩子 ───────────────────────────────────────────────────
   *
   * 只暴露**纯函数**（不碰 DOM、不改状态的那几个），供
   * `frontend/src/engine/pageUi.test.mjs` 在 node 里直接调 —— 这个脚本是个
   * 浏览器 IIFE、没有模块边界，不开口子就没法自动化验它（而 `npm test`
   * 只扫 `frontend/`，所以测试文件在那边）。
   *
   * 不导出任何会改状态的东西：导出了就等于多一个能被外部改坏的面。
   */
  if (typeof window !== 'undefined') {
    window.__QULV_UI_TEST__ = {
      isTruthy: isTruthy,
      guidOf: guidOf,
      artistsOf: artistsOf,
      sourceLabel: sourceLabel,
      itemText: itemText,
      looksLikeTrackMenu: looksLikeTrackMenu,
      menuItemAllowed: menuItemAllowed,
      harvest: harvest,
      mergeRecord: mergeRecord,
      describeSource: describeSource,
      indexOf: function (g) { return index[g] || null; }
    };
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
