/* AI 音乐整理 - 前端逻辑 */
(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };

  var state = {
    status: null,
    lib: { items: [], total: 0, offset: 0, limit: 100 },
    config: null,
  view: "card",
  multi: false,      // 音乐库多选模式
  sel: {},           // 选中文件 path -> true（跨页保留）
  favMap: {},        // 「我的喜欢」集合：path / pending://源/id -> true
  pl: null,          // 歌单页数据缓存
};

  /* 内嵌 Material 风格图标（24x24 path，fill:currentColor），离线可用 */
  function ico(d) {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="' + d + '"/></svg>';
  }
  var ICONS = {
    play: ico("M8 5v14l11-7z"),
    ai: ico("M19 9l1.25-2.75L23 5l-2.75-1.25L19 1l-1.25 2.75L15 5l2.75 1.25L19 9zm-7.5.5L9 4 6.5 9.5 1 12l5.5 2.5L9 20l2.5-5.5L17 12l-5.5-2.5zM19 15l-1.25 2.75L15 19l2.75 1.25L19 23l1.25-2.75L23 19l-2.75-1.25L19 15z"),
    retidy: ico("M17.65 6.35A7.958 7.958 0 0 0 12 4c-4.42 0-7.99 3.58-7.99 8s3.57 8 7.99 8c3.73 0 6.84-2.55 7.73-6h-2.08A5.99 5.99 0 0 1 12 18c-3.31 0-6-2.69-6-6 0-1.66.69-3.15 1.79-4.24L4 12.01V4h8.01l-2.37 2.37z"),
    edit: ico("M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a.996.996 0 0 0 0-1.41l-2.34-2.34a.996.996 0 0 0-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z"),
    trash: ico("M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"),
  };

  /* 图标按钮进入/退出加载态：保留原图标，加旋转动画 */
  function btnLoading(btn, on) {
    if (on) { btn.disabled = true; btn.classList.add("icospin"); }
    else { btn.disabled = false; btn.classList.remove("icospin"); }
  }

  function toast(msg) {
    var t = $("toast");
    t.textContent = msg;
    t.classList.add("show");
    clearTimeout(t._h);
    t._h = setTimeout(function () { t.classList.remove("show"); }, 2400);
  }

  /* 网关前缀识别：fnOS 统一网关下页面挂在 /app/music-tidy/，
     浏览器地址栏与后端前缀一致，API 必须带上该前缀；本地独立运行时无前缀。 */
  function gwPrefix() {
    try {
      var m = (location.pathname || "").match(/^(\/app\/[^/]+)/);
      return m ? m[1] : "";
    } catch (e) { return ""; }
  }
  function api(path, opts) {
    opts = opts || {};
    var p = { method: opts.method || "GET", headers: { "Content-Type": "application/json" } };
    if (opts.body) p.body = JSON.stringify(opts.body);
    var url = gwPrefix() + "/" + String(path).replace(/^\/+/, "");
    return fetch(url, p).then(function (r) {
      return r.json().catch(function () { return { error: "bad response" }; });
    }).then(function (d) {
      if (d && d.error) { throw new Error(d.error); }
      return d;
    });
  }

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  /* ---------- 状态 / 概览 ---------- */
  function loadStatus() {
    return api("/api/status").then(function (d) {
      state.status = d;
      renderOverview();
      renderFolders();
      renderTask();
      if (d.admin === false) renderNonAdmin();
      return d;
    }).catch(function (e) { toast("状态加载失败: " + e.message); });
  }

  function renderNonAdmin() {
    // 普通用户只读：隐藏写操作按钮
    ["btn-scan", "btn-tidy", "btn-add-folder", "btn-save-llm", "btn-save-strategy", "btn-test-llm",
     "btn-pause", "btn-resume", "btn-stop", "btn-retidy", "btn-aibatch", "btn-dedupe-all", "btn-dedupe-ai",
     "btn-rule-analyze", "btn-libsel", "btn-organize", "btn-classify", "btn-touch", "btn-split-cue",
     "btn-dl-search", "btn-dl-sel", "btn-dl-playlist", "btn-save-ms", "btn-save-pl",
     "btn-pl-new", "btn-pl-create", "pld-share", "pld-archive", "pld-rename", "pld-del",
     "pld-savearch", "btn-isp-import", "btn-scope", "btn-scope2",
     "btn-scope-clear", "btn-scope-clear2"]
      .forEach(function (id) { var el = $(id); if (el) el.style.display = "none"; });
  }

  function renderOverview() {
    var s = state.status;
    var cards = [
      { label: "音乐文件", value: s.stats.files },
      { label: "缺歌词", value: s.stats.no_lyric },
      { label: "缺封面", value: s.stats.no_cover },
      { label: "重复组", value: s.stats.dup_groups },
    ];
    $("statcards").innerHTML = cards.map(function (c) {
      return '<div class="card"><div class="label">' + c.label + '</div><div class="value">' + c.value + '</div></div>';
    }).join("");
  }

  function renderTask() {
    var t = state.status.task;
    var tag = $("task-tag"), box = $("taskbox");
    var tstate = t.state || (t.running ? "running" : "idle");
    dlcSync(t);
    /* 仅全量扫描跑完：当场报“磁盘多少 / 已入库多少 / 差在哪”，并作废体检缓存 */
    if (t.type === "probe" && !t.running && t.finished_at && t.finished_at !== lastProbe) {
      lastProbe = t.finished_at;
      auditData = null;
      /* 重启后从快照恢复出来的“上次完成”，不是刚跑完的：别再弹一次结论提示，
         否则每次打开应用都被一句“仅全量扫描完成”糊脸，还以为是新结果。 */
      if (!t.restored) {
        loadDenied();   // “读不到的目录”快照刚刷新，提示条得跟着重画
        var pr = t.probe;
        if (pr) {
          /* 总数相同也可能是两边各错一个（一首已消失 + 一首未入库），所以另报差值明细 */
          var od = pr.only_db || 0, ok = pr.only_disk || 0;
          toast("仅全量扫描完成：" + probeSummary(pr)
            + ((od || ok) ? " · 其中 " + od + " 条记录本轮没扫到、" + ok + " 个文件未入库，逐条名单见「日志」页"
                          : ""));
        } else {
          toast("仅全量扫描结束，逐目录原因见「日志」页");
        }
      }
    }
    /* 正式扫描结束后体检缓存已旧（入库数变了），自动作废，不必再点一次“重新体检” */
    if (t.type === "scan" && !t.running && t.finished_at && t.finished_at !== lastScanFin) {
      lastScanFin = t.finished_at;
      auditData = null;
      if (!t.restored) loadDenied();
    }
    // 控制按钮显隐：运行中显示暂停/停止，暂停中显示继续/停止
    $("btn-pause").classList.toggle("hidden", !(t.running && tstate === "running"));
    $("btn-resume").classList.toggle("hidden", !(t.running && tstate === "paused"));
    $("btn-stop").classList.toggle("hidden", !t.running);
    // 运行中禁用「扫描/开始整理」，避免并发
    ["btn-scan", "btn-tidy", "btn-retidy", "btn-probe"].forEach(function (id) { $(id).disabled = !!t.running; });
    if (t.running && tstate === "paused") {
      tag.textContent = "已暂停"; tag.style.background = "rgba(255,159,10,.15)"; tag.style.color = "#ff9f0a";
      box.innerHTML =
        '<div>已暂停，进度：' + esc(t.current || "") + '</div>' + taskBar(t) +
        '<div style="margin-top:6px">' + taskCounts(t) + ' · 点「继续」接着跑，或「停止」保留断点</div>';
    } else if (t.running) {
      // 下载类任务后端会报 stage（检查本地/搜索音源/下载中/转码中/刮削入库），
      // 直接拿它当状态标签，避免“已在转码却仍显示下载中”。
      var tname = t.stage || (taskLabel(t.type) + "中");
      tag.textContent = tname;
      tag.style.background = "rgba(10,132,255,.18)"; tag.style.color = "#6cb2ff";
      box.innerHTML =
        '<div>正在' + taskLabel(t.type) + '：' + esc(t.current || "") + '</div>' + taskBar(t) +
        '<div style="margin-top:6px">' + taskCounts(t) + '</div>';
    } else {
      // 终态区分：跑完 / 被停止 / 异常，不再一律写“空闲”掩盖失败。
      var fin = tstate === "error" ? "异常中断" : tstate === "stopped" ? "已停止" : "已完成";
      tag.textContent = t.finished_at ? fin : "空闲";
      tag.style.background = tstate === "error" ? "rgba(255,69,58,.18)"
        : (tstate === "stopped" ? "rgba(255,159,10,.15)" : "rgba(255,255,255,.08)");
      tag.style.color = tstate === "error" ? "#ff6961"
        : (tstate === "stopped" ? "#ff9f0a" : "#98989d");
      box.innerHTML = t.finished_at ? taskSummary(t, fin) : "暂无任务。";
    }
    renderResume(t);
    renderWatchTag();
    /* 会花 token 的任务一跑完就重拉一次用量：否则用户从设置页看回到旧数字，
       会以为“AI 调了一堆”没记上。同一个 finished_at 只拉一次，不随轮询重复请求。 */
    if (AI_COSTY[t.type] && !t.running && t.finished_at && !t.restored
        && t.finished_at !== lastAiFin) {
      lastAiFin = t.finished_at;
      loadAiUsage();
    }
  }

  /* 进度条：total=0 是后端“分母未知”的约定（枚举前先数一遍总量，等于把盘
     再遍历一次，比扫本身还慢）。这种时候给不定长动画条 + 递增计数，比一个永远
     0% 的条诚实，也比干脆没有进度提示强。 */
  function taskBar(t) {
    if (!t.total) return '<div class="progress indet"><div></div></div>';
    return '<div class="progress"><div style="width:'
      + Math.round(t.done / t.total * 100) + '%"></div></div>';
  }

  function taskCounts(t) {
    var cnt = '（成功 ' + t.ok + ' · 失败 ' + t.fail
      + (t.skip ? ' · 跳过已存在 ' + t.skip : '') + '）';
    if (t.total) return t.done + ' / ' + t.total + cnt;
    // 分母未知：只报已处理量，不编百分比；没有任何成败时别插一串 0
    return '已处理 ' + t.done + ' 项，总量待枚举完成' + ((t.ok || t.fail || t.skip) ? cnt : "");
  }

  /* “上次仅全量扫描已完成于 …，共 2 项：成功 0 · 跳过 0 · 失败 0”是一句没用的话：
     仅全量扫描本来就不产生成功/失败。改用它自己的口径：授权目录 / 一级二级子目录 /
     音频首数 / 入库差值，一眼看出“扫了多少地方、多少歌、还差多少”。 */
  function probeSummary(pr) {
    if (!pr) return "";
    var l = [];
    l.push("授权目录 " + (pr.roots || 0) + " 个");
    l.push("子目录 " + (pr.sub_dirs != null ? pr.sub_dirs : (pr.dirs || 0)) + " 个（一级 "
      + (pr.d1 || 0) + " · 二级 " + (pr.d2 || 0)
      + (pr.deep ? " · 更深 " + pr.deep : "") + "）");
    l.push("音频 " + (pr.audio_total || 0) + " 首 · 全部文件 " + (pr.files_total || 0) + " 个");
    var gap = (pr.audio_total || 0) - (pr.in_db || 0);
    l.push(pr.scoped ? "范围内已入库 " + (pr.in_db || 0) + " 条（全库 " + (pr.db_total || 0) + " 条不计入）"
                     : "已入库 " + (pr.in_db || 0) + " 条");
    l.push(gap === 0 ? "磁盘与入库完全对得上"
      : (gap > 0 ? "还差 " + gap + " 首没入库，点「扫描音乐库」补上"
                 : "入库比磁盘多 " + (-gap) + " 条（文件已移动/删除，重扫会清掉）"));
    if (pr.complete === false) l.push("但本轮枚举不完整，上面的差值不能当结论");
    l.push(pr.issues ? "问题目录 " + pr.issues + " 个，原因逐条见「日志」页" : "未发现漏扫原因");
    if (pr.secs) l.push("用时 " + pr.secs + "s");
    return l.join(" · ");
  }

  function taskSummary(t, fin) {
    var head = "上次" + taskLabel(t.type) + fin + "于 " + (t.finished_at || "");
    if (t.restored) head += "（重启前保存的进度，不是本轮新跑的）";
    if (t.type === "probe" && t.probe) {
      return '<div>' + esc(head) + '</div>'
        + '<div style="margin-top:6px;line-height:1.8">' + esc(probeSummary(t.probe)) + '</div>'
        + (t.last_error ? '<div style="margin-top:6px;color:#ff6961">异常：' + esc(t.last_error) + '</div>' : "");
    }
    return esc(head + (t.total ? "，共 " + t.total + " 项：成功 " + (t.ok || 0)
      + " · 跳过已存在 " + (t.skip || 0) + " · 失败 " + (t.fail || 0) : "")
      + (t.last_error ? "；异常：" + t.last_error : ""));
  }

  /* 关应用 / 升级 / 崩一次就把任务弄丢：进度已落盘，这里把“断在哪一单”摆出来并给
     一个原范围继续的按钮。继续 = 重新提交同一个任务入口，不另起一套执行逻辑。 */
  function renderResume(t) {
    var rs = t.resume;
    if (!rs || t.running || (state.status && state.status.admin === false)) return;
    var box = $("taskbox");
    var tip = "用上次那 " + rs.candidate + " 个目录重新提交" + rs.label + "任务；"
      + (rs.type === "tidy" ? "已整理完的文件会自动跳过，不会重做"
                            : "已入库的文件也只补差异");
    box.insertAdjacentHTML("beforeend",
      '<div class="row" style="margin-top:12px;align-items:center;gap:8px;flex-wrap:wrap">'
      + '<span class="tag" style="background:rgba(255,159,10,.15);color:#ff9f0a">上次' + esc(rs.label)
      + ' 在 ' + esc(rs.at || "?") + ' 被中断（已完成 ' + rs.done + ' / ' + rs.total + '）</span>'
      + '<button class="btn btn-primary btn-sm" id="btn-resume-task" title="' + esc(tip) + '">'
      + '继续上次' + esc(rs.label) + '</button>'
      + (rs.dropped ? '<span style="font-size:12px;color:var(--sub)">原范围里有 ' + rs.dropped
          + ' 个目录已不在授权目录内，继续时会自动跳过</span>' : '')
      + '</div>');
    var rb = $("btn-resume-task");
    if (rb) rb.onclick = function () { resumeLastTask(rs); };
  }

  function resumeLastTask(rs) {
    var path = rs.type === "scan" ? "/api/scan"
      : rs.type === "probe" ? "/api/scan/probe" : "/api/tidy";
    api(path, { method: "POST", body: { folder_tokens: rs.tokens } }).then(function () {
      toast("已提交：继续上次" + rs.label);
      loadStatus(); pollTask();
    }).catch(function (e) { toast("启动失败: " + e.message); });
  }

  /* 增量监听“工作中”小标：每轮枚举要跑几十秒，不挂个动静看起来就像监听没生效 */
  function renderWatchTag() {
    var el = $("watch-tag");
    if (!el) return;
    var w = (state.status && state.status.watcher) || state.watch || {};
    el.className = "tag wt";
    if (!w.enable && !w.running) {
      el.textContent = "增量监听 未开启";
      el.title = "到设置页「整理策略 → 增量监听自动整理」开启";
      el.classList.add("wt-off");
      return;
    }
    if (w.busy) {
      el.textContent = "增量监听 工作中";
      el.title = w.stage || "正在检查目录变化";
      el.classList.add("wt-busy");
      return;
    }
    el.textContent = "增量监听 已开启";
    el.title = "每 " + (w.interval || 30) + "s 检查一次"
      + (w.last_check ? " · 上次检查 " + w.last_check : "")
      + (w.files ? " · 覆盖 " + w.files + " 首音频" : "")
      + (w.last_found ? " · 上轮发现 " + w.last_found + " 个变化" : " · 上轮无变化")
      + (w.checks ? " · 已检查 " + w.checks + " 轮" : "");
    el.classList.add("wt-idle");
  }

  /* 任务框重绘频率很高，靠 finished_at 去重，保证一次扫描只弹一次结论提示 */
  var lastProbe = "";
  var lastScanFin = "";
  var lastAiFin = "";
  var AI_COSTY = { ai: 1, cover: 1, tidy: 1 };

  function taskLabel(type) {
    return type === "scan" ? "扫描" : type === "probe" ? "仅全量扫描" : type === "ai" ? "AI 补全"
      : type === "cover" ? "AI 修复封面"
      : type === "organize" ? "格式移动" : type === "classify" ? "风格分类"
      : type === "touch" ? "触发重扫" : type === "split_cue" ? "CUE 拆分"
      : type === "download" ? "歌曲下载" : type === "dl_playlist" ? "歌单下载" : "整理";
  }

  /* ---------- 目录 ---------- */
  function browseBtn(f) {
    return '<button class="btn btn-ghost btn-sm btn-browse-folder" data-path="' + esc(f)
      + '" title="在这条授权目录里逐层勾选要扫的子目录">浏览/选为范围</button>';
  }

  function renderFolders() {
    var s = state.status;
    var sys = $("sysfolders"), man = $("manualfolders");
    if (!s.system_folders || !s.system_folders.length) {
      sys.innerHTML = '<div class="empty">暂无系统授权目录。请到飞牛「应用设置 → 授权目录」添加音乐目录。</div>';
    } else {
      sys.innerHTML = s.system_folders.map(function (f) {
        return '<div class="folder-item"><span class="mono">' + esc(f) + '</span>' + browseBtn(f) + '</div>';
      }).join("");
    }
    // 手动栏只列手动目录：以前拿的是合并后的全部目录，系统授权目录会重复出现，
    // 还带着一个点下去什么也不动的「移除」按钮。
    if (!s.manual_folders || !s.manual_folders.length) {
      man.innerHTML = '<div class="empty">暂无手动目录。</div>';
    } else {
      man.innerHTML = s.manual_folders.map(function (f) {
        return '<div class="folder-item"><span class="mono">' + esc(f) + '</span><span style="display:flex;gap:6px">' +
          browseBtn(f) +
          '<button class="btn btn-ghost btn-sm btn-del-folder" data-path="' + esc(f) + '">移除</button></span></div>';
      }).join("");
    }
    Array.prototype.forEach.call(document.querySelectorAll(".btn-del-folder"), function (b) {
      b.onclick = function () {
        api("/api/folders/del", { method: "POST", body: { path: b.getAttribute("data-path") } })
          .then(function () { toast("已移除目录"); return loadStatus(); });
      };
    });
    Array.prototype.forEach.call(document.querySelectorAll(".btn-browse-folder"), function (b) {
      b.onclick = function () { openScope(b.getAttribute("data-path")); };
    });
  }

  /* ---------- 指定目录扫描（扫描范围） ---------- */
  /* 范围只存当前浏览器：放进服务端的话，另一台设备留下的选择会悄悄把这一单的
     全量扫描变成局部扫描（「重新整理」尤其危险），宁可让用户在新设备上重选一次。 */
  var SCOPE_KEY = "scan_scope_v1";
  var scope = [];        // [{token, name, path}]：已生效的范围，空 = 全部授权目录
  var scDraft = {};      // 弹窗里的临时勾选（取消即丢弃）
  var scSeq = 0;         // 懒加载子树的 DOM 计数

  function loadScope() {
    try {
      var raw = JSON.parse(localStorage.getItem(SCOPE_KEY) || "[]");
      if (raw && raw.length) {
        scope = raw.filter(function (x) { return x && x.token; })
          .map(function (x) { return { token: x.token, name: x.name || "", path: x.path || "" }; });
      }
    } catch (e) { scope = []; }
  }

  function saveScope() {
    try { localStorage.setItem(SCOPE_KEY, JSON.stringify(scope)); } catch (e) {}
  }

  /* 任务请求体：没选范围就什么也不带（后端保持旧语义：全部授权目录） */
  function scopeBody(extra) {
    var o = extra || {};
    if (scope.length) {
      o.folder_tokens = scope.map(function (x) { return x.token; });
    }
    return o;
  }

  function scopeText() {
    if (!scope.length) return "全部授权目录";
    var names = scope.map(function (x) { return x.name || x.path; });
    return "指定 " + scope.length + " 个目录：" + names.slice(0, 3).join("、")
      + (names.length > 3 ? " 等" : "");
  }

  function renderScope() {
    var t = scopeText();
    var tip = scope.length
      ? "当前只作用在这些目录（含其子目录）：" + scope.map(function (x) { return x.path; }).join("、")
      : "未指定范围：扫描 / 整理作用于全部授权目录";
    ["scope-tag", "scope-tag2"].forEach(function (id) {
      var el = $(id);
      if (!el) return;
      el.textContent = t;
      el.title = tip;
    });
    ["btn-scope-clear", "btn-scope-clear2"].forEach(function (id) {
      var el = $(id);
      if (el) el.classList.toggle("hidden", !scope.length);
    });
    var sum = $("sc-sum");
    if (sum) sum.textContent = scSumText();
  }

  function scSumText() {
    var ks = Object.keys(scDraft || {});
    if (!ks.length) return "未选择：扫描全部授权目录";
    return "已选 " + ks.length + " 个目录：" + ks.map(function (k) { return scDraft[k].name; }).join("、");
  }

  /* 没数完的目录只能给下界：预算花光时已经数到的就是“至少这么多”，
     写成「≈0 首」会让人以为这个文件夹是空的，反而不如不显示。 */
  function scCount(nd) {
    if (!nd.counted) return "未数";
    if (nd.partial) return nd.audio ? "≥" + nd.audio + " 首（未数完）" : "未数完";
    return nd.audio + " 首";
  }

  function scHtml(list) {
    return (list || []).map(function (nd) {
      var on = !!scDraft[nd.token];
      return '<div class="sc-row" data-sc-tok="' + esc(nd.token) + '" data-sc-name="' + esc(nd.name)
        + '" data-sc-path="' + esc(nd.path) + '" data-sc-kids="' + (nd.has_kids ? 1 : 0) + '">'
        + '<span class="sc-caret' + (nd.has_kids ? '' : ' leaf') + '">' + (nd.has_kids ? "▸" : "·") + '</span>'
        + '<input type="checkbox"' + (on ? ' checked' : '') + '>'
        + '<span class="sc-name' + (on ? ' sel' : '') + '" title="' + esc(nd.path) + '">' + esc(nd.name) + '</span>'
        + '<span class="sc-n">' + esc(scCount(nd)) +
          (nd.nsub ? ' · 子目录 ' + nd.nsub + (nd.nsub_more ? '+' : '') : '') + '</span>'
        + '</div><div class="sc-kids hidden"></div>';
    }).join("");
  }

  function scBind(el) {
    Array.prototype.forEach.call(el.querySelectorAll(".sc-row"), function (row) {
      var cb = row.querySelector("input[type=checkbox]");
      var caret = row.querySelector(".sc-caret");
      cb.onchange = function () {
        scToggle(row.getAttribute("data-sc-tok"), cb.checked, row.getAttribute("data-sc-path"),
                 row.getAttribute("data-sc-name"));
        row.querySelector(".sc-name").classList.toggle("sel", cb.checked);
      };
      caret.onclick = function () { scExpand(row); };
    });
  }

  function scExpand(row) {
    if (row.getAttribute("data-sc-kids") !== "1") return;
    var kids = row.nextElementSibling;
    if (!kids) return;
    var caret = row.querySelector(".sc-caret");
    if (kids.getAttribute("data-loaded") === "1") {
      kids.classList.toggle("hidden");
      caret.textContent = kids.classList.contains("hidden") ? "▸" : "▾";
      return;
    }
    caret.textContent = "…";
    api("/api/dirs?token=" + encodeURIComponent(row.getAttribute("data-sc-tok")))
      .then(function (d) {
        kids.innerHTML = (d.items && d.items.length) ? scHtml(d.items)
          : '<div class="sc-tip">没有子目录了：这个目录里的音频就是它的全部</div>';
        kids.setAttribute("data-loaded", "1");
        kids.classList.remove("hidden");
        caret.textContent = "▾";
        scBind(kids);
      })
      .catch(function (e) { caret.textContent = "▸"; toast(e.message); });
  }

  function scToggle(tok, on, path, name) {
    if (!on) { delete scDraft[tok]; $("sc-sum").textContent = scSumText(); return; }
    scDraft[tok] = { token: tok, name: name || path, path: path || "" };
    // 勾了外层就去掉已勾的内层：后端只认最外层，留着只会让人以为多选了几个目录
    var me = String(path || "").replace(/[\\/]+$/, "");
    Object.keys(scDraft).forEach(function (k) {
      if (k === tok) return;
      var p = String(scDraft[k].path || "").replace(/[\\/]+$/, "");
      if (p === me || p.indexOf(me + "/") === 0 || p.indexOf(me + "\\") === 0) delete scDraft[k];
    });
    $("sc-sum").textContent = scSumText();
  }

  /* 「扫描全部授权目录」＝清空草稿，顺带把已渲染的勾去掉，不必重新拉一遍目录 */
  function scMarkAll() {
    Array.prototype.forEach.call($("scope-modal").querySelectorAll(".sc-row"), function (row) {
      var cb = row.querySelector("input[type=checkbox]");
      if (cb) cb.checked = false;
      var nm = row.querySelector(".sc-name");
      if (nm) nm.classList.remove("sel");
    });
  }

  function openScope(startPath) {
    scDraft = {};
    scope.forEach(function (x) { scDraft[x.token] = { token: x.token, name: x.name, path: x.path }; });
    $("scope-modal").classList.add("show");
    $("sc-sum").textContent = scSumText();
    $("sc-body").innerHTML = '<div class="hint" style="font-size:12px">正在读取授权目录…（只走目录树，不读文件内容）</div>';
    api("/api/dirs").then(function (d) {
      var items = d.items || [];
      $("sc-body").innerHTML = items.length ? scHtml(items)
        : '<div class="empty">还没有授权目录。请到飞牛「应用设置 → 授权目录」添加，或到「目录」页手动添加。</div>';
      scBind($("sc-body"));
      if (startPath) scJumpTo(startPath);
    }).catch(function (e) {
      $("sc-body").innerHTML = '<div class="hint" style="font-size:12px;color:var(--err)">'
        + esc(e.message) + '</div>';
    });
  }

  function scJumpTo(path) {
    var rows = $("sc-body").querySelectorAll(".sc-row");
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute("data-sc-path") === path) {
        scExpand(rows[i]);
        try { rows[i].scrollIntoView({ block: "center" }); } catch (e) {}
        return;
      }
    }
  }

  function scApply() {
    scope = Object.keys(scDraft).map(function (k) { return scDraft[k]; });
    saveScope();
    renderScope();
    $("scope-modal").classList.remove("show");
    auditData = null;    // 范围变了，上一份体检结论已经不代表当前范围
    toast(scope.length ? "已限定扫描范围：" + scopeText() : "已改回扫描全部授权目录");
  }

  function clearScope() {
    scope = [];
    saveScope();
    renderScope();
    auditData = null;
    toast("已改回扫描全部授权目录");
  }


  /* ---------- 音乐库 ---------- */
  function mediaUrl(path, kind) {
    return gwPrefix() + "/api/media?path=" + encodeURIComponent(path) + "&kind=" + kind;
  }

  function loadLibrary() {
    var q = new URLSearchParams({
      limit: state.lib.limit, offset: state.lib.offset,
      search: $("lib-search").value.trim(),
      tidied: $("lib-tidied").value,
      missing: ($("lib-missing") || {}).value || "",
      code: ($("lib-code") || {}).value || "",
    }).toString();
    return api("/api/library?" + q).then(function (d) {
      state.lib.items = d.items; state.lib.total = d.total;
      renderLibrary();
    });
  }

  /* 全维度代码筛选下拉：按风格/情绪/场景/版本分组 */
  function loadCodeStats() {
    return api("/api/code_stats").then(function (st) {
      var sel = $("lib-code");
      var cur = sel.value;
      sel.innerHTML = '<option value="">' + (st.total ? "全部分类（带代码 " + st.total + " 首）" : "全部分类") + '</option>';
      [["style", "戏腔风格"], ["emotion", "情绪"], ["scene1", "主场景"], ["version", "版本"]].forEach(function (grp) {
        var list = st[grp[0]] || [];
        if (!list.length) return;
        var og = document.createElement("optgroup");
        og.label = grp[1];
        list.forEach(function (x) {
          var op = document.createElement("option");
          op.value = x.code;
          op.textContent = x.label + "（" + x.count + "）";
          og.appendChild(op);
        });
        sel.appendChild(og);
      });
      sel.value = cur;
    }).catch(function () {});
  }

  function renderLibrary() {
    var admin = !!(state.status && state.status.admin);
    if (!admin) { state.multi = false; }
    if (state.view === "table") {
      $("lib-cards").classList.add("hidden");
      $("lib-tablewrap").classList.remove("hidden");
      renderLibraryTable();
    } else {
      $("lib-tablewrap").classList.add("hidden");
      $("lib-cards").classList.remove("hidden");
      renderLibraryCards();
    }
    renderLibThead();
    renderSelbar();
    var totalPages = Math.max(1, Math.ceil(state.lib.total / state.lib.limit));
    var page = Math.floor(state.lib.offset / state.lib.limit) + 1;
    $("lib-pageinfo").textContent = "第 " + page + " / " + totalPages + " 页 · 共 " + state.lib.total + " 条";
    $("lib-prev").disabled = state.lib.offset <= 0;
    $("lib-next").disabled = state.lib.offset + state.lib.limit >= state.lib.total;
  }

  /* ---------- 多选删除 ---------- */
  function selectedPaths() { return Object.keys(state.sel); }

  function renderLibThead() {
    var base = "<th>文件</th><th>歌手</th><th>歌名</th><th>歌词</th><th>封面</th><th>状态</th><th>操作</th>";
    $("lib-thead").innerHTML = state.multi
      ? '<tr><th style="width:34px"><input type="checkbox" id="lib-selall" title="全选本页"></th>' + base + "</tr>"
      : "<tr>" + base + "</tr>";
    var all = $("lib-selall");
    if (all) {
      all.checked = state.lib.items.length > 0 &&
        state.lib.items.every(function (it) { return state.sel[it.path]; });
      all.onchange = function () {
        state.lib.items.forEach(function (it) {
          if (all.checked) state.sel[it.path] = true; else delete state.sel[it.path];
        });
        renderLibrary();
      };
    }
  }

  function renderSelbar() {
    var bar = $("lib-selbar");
    if (!state.multi) { bar.classList.add("hidden"); bar.innerHTML = ""; return; }
    bar.classList.remove("hidden");
    var n = selectedPaths().length;
    var pageAll = state.lib.items.length > 0 &&
      state.lib.items.every(function (it) { return state.sel[it.path]; });
    bar.innerHTML =
      "<span>多选模式：已选中 <b>" + n + "</b> 个文件（删除将连同本地音频/歌词/封面文件一起移除）</span>" +
      '<span class="grow"></span>' +
      '<button class="btn btn-ghost btn-sm" id="sel-pageall">' + (pageAll ? "取消本页" : "全选本页") + "</button>" +
      '<button class="btn btn-ghost btn-sm" id="sel-clear">清空选择</button>' +
      '<button class="btn btn-danger btn-sm" id="sel-del"' + (n ? "" : " disabled") + ">" +
      ICONS.trash + "删除所选 " + (n ? "(" + n + ")" : "") + "</button>" +
      '<button class="btn btn-ghost btn-sm" id="sel-exit">退出多选</button>';
    $("sel-pageall").onclick = function () {
      state.lib.items.forEach(function (it) {
        if (pageAll) delete state.sel[it.path]; else state.sel[it.path] = true;
      });
      renderLibrary();
    };
    $("sel-clear").onclick = function () { state.sel = {}; renderLibrary(); };
    $("sel-exit").onclick = function () { toggleMulti(false); };
    $("sel-del").onclick = deleteSelected;
  }

  function toggleMulti(on) {
    state.multi = (on === undefined) ? !state.multi : !!on;
    if (!state.multi) state.sel = {};
    var btn = $("btn-libsel");
    btn.classList.toggle("btn-primary", state.multi);
    btn.classList.toggle("btn-ghost", !state.multi);
    btn.title = state.multi ? "退出多选模式" : "多选删除（连同本地文件）";
    renderLibrary();
  }

  function deleteSelected() {
    var paths = selectedPaths();
    if (!paths.length) { toast("请先勾选要删除的文件"); return; }
    if (!confirm("将删除选中的 " + paths.length + " 个文件：\n" +
      "· 本地音频文件与同名歌词(.lrc)、封面(.jpg/.jpeg/.png) 一并物理删除（不可恢复）\n" +
      "· 音乐库记录同步移除\n\n确定删除？")) return;
    var btn = $("sel-del");
    if (btn) { btn.disabled = true; btn.textContent = "删除中…"; }
    api("/api/library/delete", { method: "POST", body: { paths: paths } })
      .then(function (d) {
        var r = d.result || {};
        var msg = "已删除 " + (r.deleted || 0) + " 个文件（含本地文件）";
        if (r.failed && r.failed.length) msg += "，失败 " + r.failed.length + " 个";
        toast(msg);
        state.sel = {};
        (r.failed || []).forEach(function (f) { state.sel[f.path] = true; });  // 失败的保留勾选便于重试
        return Promise.all([loadLibrary(), loadStatus()]);
      })
      .catch(function (e) { toast("删除失败: " + e.message); })
      .finally(function () { if ($("sel-del")) { $("sel-del").disabled = false; renderSelbar(); } });
  }

  /* ---------- 一键按格式移动 ---------- */
  function organizeAll() {
    if (taskRunning()) { toast("已有任务在运行，请等待完成"); return; }
    var out = prompt("把全部「已整理」文件按 歌手/专辑 目录格式移动到哪个文件夹？\n" +
      "请填写服务器上的绝对路径，如 /vol1/1000/整理后的音乐：", "");
    if (out === null) return;
    out = out.trim();
    if (!out) { toast("请输入目标文件夹"); return; }
    if (!confirm("将按已整理格式移动到：\n" + out + "\n\n" +
      "· 目录结构：" + out + "/歌手/专辑/文件名\n" +
      "· 同名文件自动追加 _1/_2 序号；歌词/封面随迁\n" +
      "· 文件会从原位置移动（不保留副本）\n\n确定开始？")) return;
    api("/api/library/organize", { method: "POST", body: { out_dir: out } })
      .then(function () { toast("格式移动任务已开始"); loadStatus(); pollTask(); })
      .catch(function (e) { toast(e.message); });
  }

  /* ---------- 一键风格分类 ---------- */
  function classifyAll() {
    if (taskRunning()) { toast("已有任务在运行，请等待完成"); return; }
    if (!confirm("一键分类：把每首歌的风格写入音频内嵌标签（genre）。\n\n" +
      "· 风格来源：已识别/手工设置的风格，其次是文件名中的风格代码（如 [S03]）\n" +
      "· 会直接修改音乐库中的音频文件标签（不改动歌词/封面/其它信息）\n" +
      "· 解析不到风格的文件将跳过\n" +
      "· 完成后需在飞牛音乐中重新扫描音乐库，「风格」页才会刷新\n\n确定开始？")) return;
    api("/api/library/classify", { method: "POST", body: {} })
      .then(function () { toast("风格分类任务已开始"); loadStatus(); pollTask(); })
      .catch(function (e) { toast(e.message); });
  }

  /* ---------- 触发飞牛重扫（touch mtime） ---------- */
  function touchAll() {
    if (taskRunning()) { toast("已有任务在运行，请等待完成"); return; }
    if (!confirm("触发重扫：把音乐库所有音频文件（及歌词/封面附属文件）的修改时间刷新到当下。\n\n" +
      "· 只更新修改时间，不改动文件的任何内容\n" +
      "· 飞牛音乐按「路径+修改时间」判断文件是否变化，touch 后下次扫描会重新读取标签和歌词\n" +
      "· 刷完后请在飞牛音乐中执行一次扫描（或等待其自动扫描）\n\n确定开始？")) return;
    api("/api/library/touch", { method: "POST", body: {} })
      .then(function () { toast("触发重扫任务已开始"); loadStatus(); pollTask(); })
      .catch(function (e) { toast(e.message); });
  }

  /* ---------- CUE 整轨拆分 ---------- */
  function splitCue() {
    if (taskRunning()) { toast("已有任务在运行，请等待完成"); return; }
    if (!confirm("CUE 整轨拆分：扫描库内所有 .cue 文件，按时间戳拆分为逐曲 FLAC。\n\n" +
      "· 拆分后的文件会自动入库，可继续整理\n" +
      "· 已拆分过的 CUE（同目录已有对应数量的 FLAC）会跳过\n" +
      "· 需要飞牛已安装 ffmpeg（应用自带）\n" +
      "· 拆分过程可能需要几分钟，请耐心等待\n\n确定开始？")) return;
    api("/api/library/split-cue", { method: "POST", body: {} })
      .then(function () { toast("CUE 拆分任务已开始"); loadStatus(); pollTask(); })
      .catch(function (e) { toast(e.message); });
  }

  /* 全维度代码 -> 中文标签（与后端 STYLE_MAP 等一致） */
  var CODE_LABELS = {
    S01: "柔情抒情", S02: "江湖侠气", S03: "戏腔国风", S04: "DJ改编",
    S05: "禅意清雅", S06: "大气磅礴", S07: "国风说唱", S08: "民谣国风",
    E01: "温婉柔情", E02: "潇洒豪迈", E03: "伤感离愁", E04: "治愈舒缓",
    E05: "燃向力量", E06: "轻快灵动", E07: "情绪E07", E08: "恢弘大气",
    C01: "日常循环", C02: "车载出行", C03: "睡前静心", C04: "古风BGM",
    C05: "运动健身", C06: "办公学习", C07: "汉服活动",
    V00: "原唱", Z00: "翻唱",
  };
  function codeTags(code) {
    if (!code) return "";
    var parts = code.split("-"); // Y3-S03-E03-C04-C01-V00
    var tags = [];
    if (parts[1]) tags.push(CODE_LABELS[parts[1]] || parts[1]);
    if (parts[2]) tags.push(CODE_LABELS[parts[2]] || parts[2]);
    if (parts[5]) tags.push(CODE_LABELS[parts[5]] || parts[5]);
    return tags.map(function (t) {
      return '<span class="badge" style="background:rgba(148,120,255,.16);color:#b3a4ff">' + esc(t) + '</span>';
    }).join("");
  }

  function renderLibraryCards() {
    var wrap = $("lib-cards");
    if (!state.lib.items.length) {
      wrap.innerHTML = '<div class="empty" style="grid-column:1/-1">没有记录，请先扫描音乐库。</div>';
      return;
    }
    wrap.innerHTML = state.lib.items.map(function (it, idx) {
      var lyr = it.has_lyric ? '<span class="badge badge-ok">歌词</span>' : '<span class="badge badge-warn">缺歌词</span>';
      var cov = it.has_cover ? '<span class="badge badge-gray" style="background:rgba(10,132,255,.16);color:#2563eb">封面</span>' : '<span class="badge badge-gray">缺封面</span>';
      var miss = (!it.has_lyric || !it.has_cover) && state.status && state.status.admin;
      var admin = !!(state.status && state.status.admin);
      var coverImg = it.has_cover
        ? '<img loading="lazy" src="' + mediaUrl(it.path, "cover") + '" alt="" onerror="this.remove()">'
        : "♪";
      var title = it.title || it.name.replace(/\.[^.]+$/, "");
      var selBox = state.multi
        ? '<label class="msel" data-act="selstop"><input type="checkbox" data-sel="' + idx + '"' +
          (state.sel[it.path] ? " checked" : "") + '></label>'
        : "";
      return '<div class="mcard' + (state.sel[it.path] ? " selected" : "") + '" data-idx="' + idx + '">' +
        '<div class="mcover" data-act="play" data-idx="' + idx + '">' + selBox + coverImg +
        '<div class="playmask">▶</div></div>' +
        '<div class="mbody">' +
        '<div class="mtitle" title="' + esc(title) + '">' + esc(title) + '</div>' +
        '<div class="martist">' + esc(it.artist || "未知歌手") + '</div>' +
        '<div class="mpath" title="' + esc(it.path) + '">' + esc(it.path) + '</div>' +
        '<div class="mstat">' + lyr + cov + codeTags(it.code) + '</div>' +
        '<div class="mbtns">' +
        '<button class="btn btn-ghost btn-sm icobtn" data-act="play" data-idx="' + idx + '" title="播放">' + ICONS.play + '</button>' +
        '<button class="btn btn-ghost btn-sm icobtn' + (isFavKey(it.path) ? " btn-primary" : "") + '" data-act="fav" data-idx="' + idx + '" title="' + (isFavKey(it.path) ? "取消喜欢（我的喜欢）" : "标记到我的喜欢") + '">♥</button>' +
        '<button class="btn btn-ghost btn-sm icobtn" data-act="addpl" data-idx="' + idx + '" title="添加到指定歌单">＋</button>' +
        (miss ? '<button class="btn btn-primary btn-sm icobtn" data-act="aic" data-idx="' + idx + '" title="AI 补全">' + ICONS.ai + '</button>' : "") +
        (admin ? '<button class="btn btn-ghost btn-sm icobtn" data-act="retidy" data-idx="' + idx + '" title="重新整理">' + ICONS.retidy + '</button>' +
                '<button class="btn btn-ghost btn-sm icobtn" data-act="edit" data-idx="' + idx + '" title="编辑">' + ICONS.edit + '</button>' : "") +
        '</div></div></div>';
    }).join("");
    Array.prototype.forEach.call(wrap.querySelectorAll("[data-act]"), function (b) {
      var act = b.getAttribute("data-act");
      var it = state.lib.items[Number(b.getAttribute("data-idx"))];
      if (act === "play") b.onclick = function () { openPlayer(it); };
      else if (act === "fav") b.onclick = function () { libFav(it, b); };
      else if (act === "addpl") b.onclick = function () { openAddPl(libTrack(it)); };
      else if (act === "aic") b.onclick = function () { aiCompleteOne(it, b); };
      else if (act === "retidy") b.onclick = function () { reTidyOne(it, b); };
      else if (act === "edit") b.onclick = function () { openEdit(it); };
      else if (act === "selstop") b.onclick = function (e) { e.stopPropagation(); };
    });
    bindSelCheckboxes(wrap);
  }

  /* 音乐库单曲红心开关 */
  function libFav(it, btn) {
    favToggle({ path: it.path }, it.path, function (on) {
      it.fav = on;
      if (btn) {
        btn.classList.toggle("btn-primary", on);
        btn.title = on ? "取消喜欢（我的喜欢）" : "标记到我的喜欢";
      }
    });
  }

  /* 多选 checkbox：change 时更新 state.sel 与选中样式（不整表重绘，避免丢焦点） */
  function bindSelCheckboxes(root) {
    Array.prototype.forEach.call(root.querySelectorAll("[data-sel]"), function (cb) {
      var it = state.lib.items[Number(cb.getAttribute("data-sel"))];
      cb.onchange = function () {
        if (cb.checked) state.sel[it.path] = true; else delete state.sel[it.path];
        var card = cb.closest ? cb.closest(".mcard") : null;
        if (card) card.classList.toggle("selected", cb.checked);
        renderSelbar();
        var all = $("lib-selall");
        if (all) all.checked = state.lib.items.length > 0 &&
          state.lib.items.every(function (x) { return state.sel[x.path]; });
      };
    });
  }

  /* ---------- 单曲重新整理 / 编辑识别信息 ---------- */
  function taskRunning() {
    return !!(state.status && state.status.task && state.status.task.running);
  }

  function reTidyOne(it, btn) {
    if (taskRunning()) { toast("有任务正在运行，请等待完成后重试"); return; }
    if (!confirm("将按文件名（或已保存的手工信息）重新识别并搜刮这首歌曲的歌词/封面/标签，确定？")) return;
    btnLoading(btn, true);
    api("/api/re_tidy", { method: "POST", body: { path: it.path } })
      .then(function (d) {
        var r = d.result || {};
        toast("已重新整理：" + (r.artist ? r.artist + " - " : "") + (r.title || ""));
        return loadLibrary();
      })
      .catch(function (e) { toast("整理失败: " + e.message); btnLoading(btn, false); });
  }

  var editTarget = null;

  function loadEditLyric() {
    var box = $("ed-lyric");
    box.value = "";
    if (!editTarget) return;
    var target = editTarget;
    fetch(mediaUrl(target.path, "lyric"))
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (t) { if (editTarget === target) box.value = t || ""; })
      .catch(function () {});
  }

  /* 手写的分析提示词：填了就占最高优先级，留空走内置规则 */
  function edPrompt() {
    var el = $("ed-aprompt");
    return el ? String(el.value || "").trim() : "";
  }

  function edAnalyze() {
    if (!editTarget) return;
    if (taskRunning()) { toast("有任务正在运行，请等待完成后重试"); return; }
    var btn = $("ed-analyze");
    var msg = $("ed-msg");
    var target = editTarget;
    var up = edPrompt();
    btn.disabled = true;
    btn.textContent = "分析中…";
    msg.style.display = "none";
    msg.textContent = "AI 正在核验歌词并联网检索，请稍候…";
    api("/api/library/analyze", { method: "POST", body: { path: target.path, prompt: up } })
      .then(function (d) {
        if (editTarget !== target) return;
        try { localStorage.setItem("ed_aprompt_v1", up); } catch (e) {}
        var lines = [];
        if (d.prompt_used) lines.push("◆ 已按你填写的自定义提示词执行（最高优先级）");
        lines.push((d.matched ? "✔ 歌词与歌手、歌名匹配" : "✘ 歌词不匹配或缺失") +
          (d.confidence ? "（置信度 " + d.confidence + "）" : ""));
        if (d.reason) lines.push("依据: " + d.reason);
        if (d.actual) lines.push("歌词实际出自: " + d.actual);
        if (d.action) lines.push("处理: " + d.action + (d.source ? "（来源: " + d.source + "）" : ""));
        msg.textContent = lines.join("\n");
        msg.style.display = "block";
        loadEditLyric();
        return loadLibrary().catch(function () {});
      })
      .catch(function (e) {
        if (editTarget !== target) return;
        msg.textContent = "✘ 分析失败: " + e.message;
        msg.style.display = "block";
      })
      .then(function () { btn.disabled = false; btn.textContent = "AI 完整分析"; });
  }

  function openEdit(it) {
    editTarget = it;
    $("ed-artist").value = it.artist || "";
    $("ed-title").value = it.title || it.name.replace(/\.[^.]+$/, "");
    $("ed-album").value = it.album || "";
    $("ed-style").value = it.style || "";
    /* 提示词是全局偏好、不是单曲属性：回填上次填的那份，省得每首重打一遍。
       用户在本次会话里手动改过（touched）就不再覆盖，避免清空后又弹回来 */
    var ap = $("ed-aprompt");
    if (ap && !ap.getAttribute("data-touched")) {
      try { ap.value = localStorage.getItem("ed_aprompt_v1") || ""; } catch (e) {}
    }
    var abox = $("ed-aprompt-box");
    if (abox && ap && ap.value.trim()) abox.open = true;
    $("ed-path").textContent = it.name;
    $("ed-path").title = it.path;
    var msg = $("ed-msg");
    msg.style.display = "none";
    msg.textContent = "";
    $("edit-modal").classList.add("show");
    loadEditLyric();
    $("ed-title").focus();
  }

  function closeEdit() {
    $("edit-modal").classList.remove("show");
    editTarget = null;
  }

  function saveEdit() {
    if (!editTarget) return;
    if (taskRunning()) { toast("有任务正在运行，请等待完成后重试"); return; }
    var body = {
      path: editTarget.path,
      artist: $("ed-artist").value.trim(),
      title: $("ed-title").value.trim(),
      album: $("ed-album").value.trim(),
      style: $("ed-style").value.trim(),
    };
    if (!body.title) { toast("歌名不能为空"); return; }
    var btn = $("ed-save");
    btn.disabled = true;
    btn.textContent = "搜刮中…";
    api("/api/re_tidy", { method: "POST", body: body })
      .then(function (d) {
        toast("已按新信息重新搜刮");
        closeEdit();
        return loadLibrary();
      })
      .catch(function (e) { toast("保存失败: " + e.message); })
      .then(function () { btn.disabled = false; btn.textContent = "保存并重新搜刮"; });
  }

  /* ---------- 名称解析自定义规则 ---------- */
  var pendingRule = null;

  function renderRules() {
    var rules = (state.config && state.config.name_rules) || [];
    var el = $("rules-list");
    if (!el) return;
    if (!rules.length) {
      el.innerHTML = '<div class="empty">暂无自定义规则。</div>';
      return;
    }
    el.innerHTML = rules.map(function (r) {
      return '<div class="folder-item" style="align-items:flex-start">' +
        '<div style="flex:1;min-width:0">' +
        '<div class="mono" style="word-break:break-all">' + esc(r.sample || r.regex) + '</div>' +
        (r.desc ? '<div style="font-size:12px;color:var(--sub)">' + esc(r.desc) + '</div>' : "") +
        '<div style="font-size:11px;color:var(--sub);word-break:break-all;font-family:monospace">' + esc(r.regex) + '</div>' +
        '</div>' +
        '<button class="btn btn-ghost btn-sm btn-del-rule" data-id="' + esc(r.id) + '">删除</button></div>';
    }).join("");
    Array.prototype.forEach.call(document.querySelectorAll(".btn-del-rule"), function (b) {
      b.onclick = function () {
        api("/api/rules/del", { method: "POST", body: { id: b.getAttribute("data-id") } })
          .then(function () { toast("已删除规则"); return loadConfig(); })
          .catch(function (e) { toast("删除失败: " + e.message); });
      };
    });
  }

  /* 浏览器端预览：(?P<name>) 转 JS 语法后试解析样例 */
  function testRuleLocal(rx, sample) {
    try {
      var jsRx = String(rx || "").replace(/\(\?P<([A-Za-z_$][\w$]*)>/g, "(?<$1>");
      if (!jsRx) return null;
      var pat = new RegExp(jsRx);
      var base = String(sample || "").trim().replace(/^"|"$/g, "").split(/[\\/]/).pop();
      var stem = base.replace(/\.[^.]+$/, "");
      var m = pat.exec(base) || pat.exec(stem);
      if (!m) return null;
      var g = m.groups || {};
      var out = {};
      ["artist", "title", "album", "track"].forEach(function (k) {
        var v = String(g[k] == null ? "" : g[k]).replace(/^[\s.\-_—–]+|[\s.\-_—–]+$/g, "");
        if (v) out[k] = v;
      });
      if (!out.artist && !out.title) return null;
      return out;
    } catch (e) { return null; }
  }

  function renderRuleParsed() {
    var el = $("rlm-parsed");
    if (!pendingRule) return;
    var parsed = testRuleLocal($("rlm-regex").value, pendingRule.sample);
    if (!parsed) {
      el.innerHTML = '<span style="color:#ff6961">预览：当前正则无法从样例提取到歌手/歌名，请修正后再保存</span>';
      return;
    }
    el.textContent = "预览解析结果：歌手=" + (parsed.artist || "（无）") +
      " 歌名=" + (parsed.title || "（无）") +
      (parsed.album ? " 专辑=" + parsed.album : "") +
      (parsed.track ? " 序号=" + parsed.track : "");
  }

  function openRuleConfirm(d) {
    pendingRule = {
      sample: $("rule-sample").value.trim(),
      desc: $("rule-desc").value.trim(),
      note: d.note || "",
    };
    $("rlm-sample").textContent = pendingRule.sample;
    $("rlm-sample").title = pendingRule.sample;
    var warn = d.warn || "";
    $("rlm-warn").textContent = warn ? ("AI 生成的规则未通过校验：" + warn + "，请在下方修正正则") : "";
    $("rlm-warn").classList.toggle("hidden", !warn);
    $("rlm-note").textContent = pendingRule.note;
    $("rlm-regex").value = d.regex || "";
    renderRuleParsed();
    $("rule-modal").classList.add("show");
    $("rlm-regex").focus();
  }

  function closeRuleConfirm() {
    $("rule-modal").classList.remove("show");
    pendingRule = null;
  }

  function saveRule() {
    if (!pendingRule) return;
    var regex = $("rlm-regex").value.trim();
    if (!regex) { toast("正则不能为空"); return; }
    var btn = $("rlm-save");
    btn.disabled = true;
    btn.textContent = "保存中…";
    api("/api/rules/add", { method: "POST", body: {
      sample: pendingRule.sample, desc: pendingRule.desc,
      regex: regex, note: pendingRule.note,
    } })
      .then(function () {
        closeRuleConfirm();
        $("rule-sample").value = "";
        $("rule-desc").value = "";
        toast("规则已保存，重新整理时生效");
        return loadConfig();
      })
      .catch(function (e) { toast("保存失败: " + e.message); })
      .then(function () { btn.disabled = false; btn.textContent = "确认保存规则"; });
  }

  function aiCompleteOne(it, btn) {
    btnLoading(btn, true);
    api("/api/ai_complete", { method: "POST", body: { files: [it.path] } })
      .then(function (d) {
        var r = d.result || {};
        if (r.lyric_source === "llm") toast("歌词由 AI 生成（时间码为估算值）");
        else if (d.ok) toast("补全完成：歌词" + (r.lyric ? "✓" : "✗") + " 封面" + (r.cover ? "✓" : "✗"));
        else toast("补全失败：" + (r.error || d.error || "未知原因"));
        return loadLibrary();
      })
      .catch(function (e) { toast("补全失败: " + e.message); btnLoading(btn, false); });
  }

  function renderLibraryTable() {
    var body = $("lib-body");
    var admin = !!(state.status && state.status.admin);
    if (!state.lib.items.length) {
      body.innerHTML = '<tr><td colspan="' + (state.multi ? 8 : 7) + '"><div class="empty">没有记录，请先扫描音乐库。</div></td></tr>';
    } else {
      body.innerHTML = state.lib.items.map(function (it, idx) {
        var badge = it.tidied === 1 ? '<span class="badge badge-ok">已整理</span>'
          : it.tidied === 2 ? '<span class="badge badge-danger">失败</span>'
          : '<span class="badge badge-gray">未整理</span>';
        var lyr = it.has_lyric ? "✓" : "—";
        var cov = it.has_cover ? "✓" : "—";
        var ops = '<button class="btn btn-ghost btn-sm icobtn' + (isFavKey(it.path) ? " btn-primary" : "") + '" data-tact="fav" data-idx="' + idx + '" title="' + (isFavKey(it.path) ? "取消喜欢（我的喜欢）" : "加入我的喜欢") + '">♥</button>' +
          ' <button class="btn btn-ghost btn-sm icobtn" data-tact="addpl" data-idx="' + idx + '" title="添加到歌单">＋</button>';
        ops += admin
          ? ' <button class="btn btn-ghost btn-sm icobtn" data-tact="retidy" data-idx="' + idx + '" title="重新整理">' + ICONS.retidy + '</button>' +
            ' <button class="btn btn-ghost btn-sm icobtn" data-tact="edit" data-idx="' + idx + '" title="编辑">' + ICONS.edit + '</button>'
          : "";
        var selTd = state.multi
          ? '<td class="msel-td"><input type="checkbox" data-sel="' + idx + '"' +
            (state.sel[it.path] ? " checked" : "") + '></td>'
          : "";
        return '<tr>' + selTd +
          '<td class="mono" style="max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' + esc(it.path) + '">' + esc(it.name) + '</td>' +
          '<td>' + esc(it.artist || "—") + '</td>' +
          '<td>' + esc(it.title || (it.name || "").replace(/\.[^.]+$/, "")) + '</td>' +
          '<td>' + lyr + '</td><td>' + cov + '</td><td>' + badge + '</td><td>' + ops + '</td></tr>';
      }).join("");
      Array.prototype.forEach.call(body.querySelectorAll("[data-tact]"), function (b) {
        var act = b.getAttribute("data-tact");
        var it = state.lib.items[Number(b.getAttribute("data-idx"))];
        if (act === "retidy") b.onclick = function () { reTidyOne(it, b); };
        else if (act === "fav") b.onclick = function () { libFav(it, b); };
        else if (act === "addpl") b.onclick = function () { openAddPl(libTrack(it)); };
        else if (act === "edit") b.onclick = function () { openEdit(it); };
      });
      bindSelCheckboxes(body);
    }
  }

  /* ---------- 播放器弹窗（滚动歌词） ---------- */
  var player = { lines: [], idx: -1 };

  function parseLrc(text) {
    var out = [];
    var pat = /\[(\d{1,2}):(\d{2})(?:[.:](\d{1,3}))?\]/g;
    String(text || "").split(/\r?\n/).forEach(function (line) {
      var m, times = [], last = 0;
      pat.lastIndex = 0;
      while ((m = pat.exec(line))) {
        var frac = m[3] ? Number(("0." + m[3]).slice(0, 3)) : 0;
        times.push(Number(m[1]) * 60 + Number(m[2]) + frac);
        last = pat.lastIndex;
      }
      if (!times.length) return;
      var txt = line.slice(last).trim();
      if (!txt) return;  // 元信息行（纯时间码）跳过
      times.forEach(function (t) { out.push({ t: t, text: txt }); });
    });
    out.sort(function (a, b) { return a.t - b.t; });
    return out;
  }

  /* ---------- 统一播放器（底部迷你 + 放大全屏，共享同一 audio） ---------- */
  var P = { queue: [], i: -1, lines: [], idx: -1, expanded: false };

  function pAudio() { return $("mp-audio"); }

  /* 取文件扩展名大写，用作播放器顶部的音质徽标 */
  function fileExt(name) {
    var m = /\.([a-z0-9]+)$/i.exec(String(name || ""));
    return m ? m[1].toUpperCase() : "";
  }

  function libTrack(it) {
    return {
      src: "lib", ref: it,
      title: it.title || (it.name || "").replace(/\.[^.]+$/, ""),
      artist: it.artist || "未知歌手",
      album: it.album || "",
      qual: fileExt(it.name) || "本地",
      srcName: "本地音乐库",
      key: it.path || "",
      coverUrl: it.has_cover ? mediaUrl(it.path, "cover") : "",
      audioUrl: mediaUrl(it.path, "audio"),
      lyricUrl: it.has_lyric ? mediaUrl(it.path, "lyric") : "",
      lyricApi: ""
    };
  }

  function dlTrack(s) {
    var sj = encodeURIComponent(JSON.stringify(s));
    var q = [s.quality, s.bitrate ? s.bitrate + "kbps" : "", s.format].filter(Boolean).join(" · ");
    return {
      src: "dl", ref: s,
      title: s.name || "",
      artist: s.artist || "未知歌手",
      album: s.album || "",
      qual: q || "在线",
      srcName: s.source_name || s.source || "",
      key: "pending://" + (s.source || "") + "/" + (s.id || ""),
      coverUrl: dlCoverUrl(s),
      audioUrl: gwPrefix() + "/api/music/preview?song=" + sj,
      lyricUrl: "",
      lyricApi: (s.has_lyric === false) ? "" : gwPrefix() + "/api/music/lyric?song=" + sj
    };
  }

  /* ---------- 「我的喜欢」红心（本地文件与网络歌曲通用） ---------- */
  function loadFavMap() {
    return api("/api/favorites").then(function (d) {
      var m = {};
      (d.paths || []).forEach(function (p) { m[p] = true; });
      state.favMap = m;
      plSyncFavN((d.paths || []).length, true);
      if (state.lib.items && state.lib.items.length) renderLibrary();   // 服务端可能按歌手+歌名命中，重绘红心
      return d.paths || [];
    }).catch(function () { return []; });
  }

  function trackFavKey(tr) {
    if (!tr) return "";
    if (tr.key) return tr.key;
    var s = tr.ref || {};
    return tr.src === "lib" ? (s.path || "") : "pending://" + (s.source || "") + "/" + (s.id || "");
  }

  function isFavKey(k) { return !!(k && state.favMap[k]); }

  function pRenderFav() {
    var on = isFavKey(trackFavKey(P.queue[P.i]));
    var tip = on ? "取消喜欢（我的喜欢）" : "标记到我的喜欢";
    var b = $("pl-fav"); if (b) { b.classList.toggle("on", on); b.title = tip; }
    var t = $("pl-fav-t"); if (t) t.textContent = on ? "已喜欢" : "喜欢";
    /* 迷你条那颗心只变色不给提示，鼠标停上去看不出能不能点 */
    var m = $("mp-fav"); if (m) { m.classList.toggle("on", on); m.title = tip; }
  }

  /* 切换喜欢：payload = {path} 或 {song}；k 为本地状态键，after(on) 用于刷新列表红心 */
  function favToggle(payload, k, after) {
    api("/api/fav", { method: "POST", body: payload }).then(function (d) {
      var on = !!d.fav;
      if (k) { if (on) state.favMap[k] = true; else delete state.favMap[k]; }
      pRenderFav();
      plSyncFavN(Object.keys(state.favMap).length);   // 先乐观刷计数，随后的 loadFavMap 会再校准
      if (after) after(on);
      toast(on ? "已加入「我的喜欢」" : "已从「我的喜欢」移除");
      loadFavMap();
    }).catch(function (e) { toast("操作失败: " + e.message); });
  }

  /* 当前播放条目 → 喜欢开关 */
  function pToggleFav() {
    var tr = P.queue[P.i];
    if (!tr) { toast("请先播放一首歌"); return; }
    var body = (tr.src === "lib" || tr.ref && tr.ref.path && !tr.ref.source)
      ? { path: trackFavKey(tr) } : { song: tr.ref };
    if (!body.path && !body.song) { toast("歌曲信息不完整"); return; }
    favToggle(body, trackFavKey(tr), function () {
      (state.lib.items || []).forEach(function (it) {
        if (it.path === trackFavKey(tr)) it.fav = isFavKey(it.path);
      });
      renderLibrary();
    });
  }

  /* ---------- 音量（全屏播放器右下，与迷你条共享同一 audio） ---------- */
  function pApplyVol() {
    var a = pAudio(), sl = $("pl-vol"), v = Math.max(0, Math.min(100, Number(sl.value) || 0));
    a.volume = v / 100;
    if (v > 0) a.muted = false;
    var silent = a.muted || v === 0;
    $("pl-voln").textContent = silent ? "0" : String(v);
    $("pl-vol").style.setProperty("--p", v);   // 滑块填充比例（CSS 靠它画渐变）
    $("pl-mute").textContent = silent ? "🔇" : (v < 45 ? "🔉" : "🔊");
    $("pl-mute").title = silent ? "恢复音量" : "静音";
    try { localStorage.setItem("mp_vol", String(v)); } catch (e) {}
  }
  function pMute() {
    var a = pAudio(), sl = $("pl-vol");
    if (Number(sl.value) > 0) { a._lastVol = Number(sl.value); sl.value = 0; a.muted = true; }
    else { sl.value = a._lastVol || 80; a.muted = false; }
    pApplyVol();
  }
  function pInitVol() {
    var v = 80;
    try { var s = localStorage.getItem("mp_vol"); if (s !== null && s !== "") v = Number(s); } catch (e) {}
    if (!isFinite(v)) v = 80;
    $("pl-vol").value = Math.max(0, Math.min(100, v));
    pApplyVol();
  }

  /* ---------- 显示器全屏（Fullscreen API，Esc 退出） ---------- */
  function pFsEl() { return document.fullscreenElement || document.webkitFullscreenElement || null; }
  function pFull() {
    var d = document;
    if (pFsEl()) {
      var ex = d.exitFullscreen || d.webkitExitFullscreen;
      if (ex) ex.call(d);
      return;
    }
    var box = $("pl-box");
    var req = box.requestFullscreen || box.webkitRequestFullscreen;
    if (!req) { toast("当前浏览器不支持显示器全屏"); return; }
    try {
      var r = req.call(box);
      if (r && r.catch) r.catch(function () { toast("全屏被浏览器拒绝（通常需用户手势或 HTTPS）"); });
    } catch (e) { toast("全屏失败：" + e.message); }
  }
  function pSyncFullBtn() {
    var on = !!pFsEl();
    var b = $("pl-full");
    if (b) b.title = on ? "退出显示器全屏（Esc）" : "显示器全屏（Esc 退出）";
    $("pl-box").classList.toggle("realfull", on);
    /* 尺寸一变重算首尾留白，否则居中位置按旧高度算，当前行会偏离视区中间 */
    pLyricPad();
    if (P.lines.length) pSync(true);
  }

  function pPlayQueue(tracks, start) {
    if (!tracks || !tracks.length) return;
    P.queue = tracks;
    pLoad(start || 0, true);
  }

  function pLoad(i, autoplay) {
    if (i < 0 || i >= P.queue.length) return;
    P.i = i; P.lines = []; P.idx = -1;
    var tr = P.queue[i];
    var a = pAudio();
    $("pl-title").textContent = tr.title || "—";
    $("pl-artist").textContent = tr.artist || "";
    $("mp-title").textContent = tr.title || "—";
    var pc = $("pl-cover"), mc = $("mp-cover");
    if (tr.coverUrl) {
      pc.src = tr.coverUrl; pc.style.visibility = "";
      mc.src = tr.coverUrl; mc.style.visibility = "";
    } else {
      pc.removeAttribute("src"); pc.style.visibility = "hidden";
      mc.removeAttribute("src"); mc.style.visibility = "hidden";
    }
    $("pl-lyric").innerHTML = '<div class="empty">歌词加载中…</div>';
    /* 液态玻璃背景：用封面自身作模糊底层；顶部徽标显示音质与来源 */
    var g = $("pl-glass");
    if (g) g.style.backgroundImage = tr.coverUrl ? 'url("' + tr.coverUrl + '")' : "";
    $("pl-qual").textContent = tr.qual || "—";
    $("pl-src").textContent = tr.srcName || "";
    var ab = $("pl-album");
    ab.textContent = tr.album || "";
    ab.style.display = tr.album ? "" : "none";
    pSetMiniLyric("");
    a.src = tr.audioUrl;
    if (autoplay !== false) a.play().catch(function () { toast("无法播放：" + (tr.title || "")); });
    pLoadLyric(tr);
    $("miniplayer").classList.add("show");
    pRenderPlayBtn();
    pRenderFav();
  }

  function pLoadLyric(tr) {
    var prom;
    if (tr.lyricUrl) {
      prom = fetch(tr.lyricUrl).then(function (r) { if (!r.ok) throw new Error("x"); return r.text(); });
    } else if (tr.lyricApi) {
      prom = fetch(tr.lyricApi).then(function (r) { return r.json(); }).then(function (j) { return (j && j.lyric) || ""; });
    } else {
      prom = Promise.resolve("");
    }
    prom.then(function (text) {
      if (P.queue[P.i] !== tr) return;  // 已切歌，丢弃
      var lines = text ? parseLrc(text) : [];
      if (!lines.length) {
        var raw = text ? String(text).split(/\r?\n/).filter(Boolean) : [];
        $("pl-lyric").innerHTML = raw.length
          ? raw.map(function (l) { return '<div class="ln">' + esc(l) + '</div>'; }).join("")
          : '<div class="empty">暂无歌词。</div>';
        P.lines = [];
        pLyricPad();   // 无时间码 / 无歌词时要清掉上一首留下的上下留白
        $("pl-lyric").scrollTop = 0;
        return;
      }
      P.lines = lines;
      $("pl-lyric").innerHTML = lines.map(function (l, k) {
        return '<div class="ln" data-i="' + k + '">' + esc(l.text) + '</div>';
      }).join("");
      /* 新歌词一到手先回到开头：不给首尾留白时，第一行只能贴在顶部，
         读起来像“歌词没从头开始滚”；留白后第一行 / 最后一行都能落在视区中间 */
      P.idx = -1;
      pLyricPad();
      pSync(true);
    }).catch(function () {
      if (P.queue[P.i] === tr) $("pl-lyric").innerHTML = '<div class="empty">歌词加载失败。</div>';
    });
  }

  function pSetMiniLyric(text) {
    var span = $("mp-lyric-text"), wrap = $("mp-lyric");
    span.style.animation = "";
    span.textContent = text || "—";
    requestAnimationFrame(function () {
      var dist = span.scrollWidth - wrap.clientWidth;
      if (dist > 4) {
        span.style.setProperty("--mpdist", "-" + dist + "px");
        span.style.animation = "mpmarquee " + Math.max(6, Math.round(dist / 22)) + "s linear infinite";
      }
    });
  }

  function pToggle() {
    var a = pAudio();
    if (!a.getAttribute("src")) { if (P.queue.length) pLoad(P.i < 0 ? 0 : P.i, true); return; }
    if (a.paused) a.play().catch(function () {}); else a.pause();
  }
  function pNext(auto) {
    if (P.i + 1 < P.queue.length) pLoad(P.i + 1, true);
    else if (auto) pAudio().pause();
  }
  function pPrev() {
    var a = pAudio();
    if (a.currentTime > 3) { a.currentTime = 0; return; }
    if (P.i - 1 >= 0) pLoad(P.i - 1, true);
  }
  function pRenderPlayBtn() {
    var a = pAudio(), playing = !a.paused && !!a.getAttribute("src");
    var icon = playing ? "❚❚" : "▶";
    $("mp-play").textContent = icon;
    $("pl-play").textContent = icon;
    $("pl-box").classList.toggle("playing", playing);   // 黑胶旋转动画
  }
  function pExpand() { P.expanded = true; $("player-modal").classList.add("show"); pRenderFav(); pLyricPad(); pSync(true); }
  function pMinimize() {
    P.expanded = false;
    $("player-modal").classList.remove("show");
    var d = document;
    if (pFsEl() && (d.exitFullscreen || d.webkitExitFullscreen)) {
      (d.exitFullscreen || d.webkitExitFullscreen).call(d);
    }
  }
  /* 彻底关闭：先清队列再置空 src，否则 media error 会弹错提示 */
  function pClose() {
    var a = pAudio();
    P.queue = []; P.i = -1; P.lines = []; P.idx = -1;
    try { a.pause(); a.removeAttribute("src"); } catch (e) {}
    $("mp-title").textContent = "未在播放";
    $("mp-lyric-text").textContent = "—";
    pMinimize();
    $("miniplayer").classList.remove("show");
    pRenderPlayBtn();
    pRenderFav();
  }

  function pFmtTime(s) {
    if (!isFinite(s) || s < 0) s = 0;
    var m = Math.floor(s / 60), ss = Math.floor(s % 60);
    return m + ":" + (ss < 10 ? "0" : "") + ss;
  }

  /* 首尾留白：把行高以外的空间补给上下两端，让第一行也能居中 */
  function pLyricPad() {
    var box = $("pl-lyric");
    if (!box) return;
    var first = box.querySelector(".ln");
    if (!first) { box.style.paddingTop = ""; box.style.paddingBottom = ""; return; }
    var pad = Math.max(0, Math.round((box.clientHeight - first.clientHeight) / 2));
    box.style.paddingTop = pad + "px";
    box.style.paddingBottom = pad + "px";
  }

  /* 把当前行滚到视区中间。用 rect 差值而不是 c.offsetTop：旧写法靠外层
     .fbox 当偏移参照（100% 高、包含封面与标题栏），算出的值超过最大滚动距离，
     歌词会被一推到底，看起来就是“不从头开始滚动” */
  function pLyricCenter(c) {
    var box = $("pl-lyric");
    if (!box || !c) return;
    var top = c.getBoundingClientRect().top - box.getBoundingClientRect().top
      + box.scrollTop - (box.clientHeight - c.clientHeight) / 2;
    box.scrollTo({ top: Math.max(0, top), behavior: "smooth" });
  }

  function pSync(force) {
    var a = pAudio();
    var dur = a.duration || 0, cur = a.currentTime || 0;
    $("pl-cur").textContent = pFmtTime(cur);
    $("pl-dur").textContent = pFmtTime(dur);
    var seek = $("pl-seek");
    if (document.activeElement !== seek) seek.value = dur ? Math.round(cur / dur * 1000) : 0;
    /* 拖动时也以滑块自身为准，否则手指按住前沿、填充条会往后跳 */
    seek.style.setProperty("--p", (Number(seek.value) / 10).toFixed(2));
    if (!P.lines.length) return;
    var t = cur + 0.25;
    var i = P.lines.length - 1;
    for (var k = 0; k < P.lines.length; k++) { if (P.lines[k].t > t) { i = k - 1; break; } }
    if (i < 0) i = 0;
    if (force || i !== P.idx) {
      P.idx = i;
      var box = $("pl-lyric");
      Array.prototype.forEach.call(box.querySelectorAll(".ln.on,.ln.nx"), function (n) {
        n.classList.remove("on"); n.classList.remove("nx");
      });
      var c = box.querySelector('.ln[data-i="' + i + '"]');
      if (c) {
        c.classList.add("on");
        pLyricCenter(c);
      }
      var nx = box.querySelector('.ln[data-i="' + (i + 1) + '"]');
      if (nx) nx.classList.add("nx");
      pSetMiniLyric(P.lines[i] ? P.lines[i].text : "");
    }
  }

  /* 音乐库播放：以当前列表为歌单，顺序播放 */
  function openPlayer(it) {
    var items = (state.lib && state.lib.items) || [];
    var tracks = items.map(libTrack);
    var idx = 0;
    for (var k = 0; k < items.length; k++) { if (items[k].path === it.path) { idx = k; break; } }
    if (!tracks.length) { tracks = [libTrack(it)]; idx = 0; }
    pPlayQueue(tracks, idx);
  }

  /* 兼容旧引用：关闭 = 缩小回迷你播放器 */
  function closePlayer() { pMinimize(); }

  /* 指定曲目列表 → 顺序播放并展开全屏播放器（歌单页 / 筛选后的库） */
  function openTrackList(tracks, idx) {
    if (!tracks || !tracks.length) { toast("这个歌单里还没有可播放的本地歌曲"); return; }
    pPlayQueue(tracks, idx || 0);
    pExpand();
  }


  /* ---------- 歌单页：我的喜欢 / 我的歌单 / 归档 / 分享 / 导入 ---------- */
  var PL = { list: [], shares: [], favId: 0, pid: 0, items: [], base: "", cfg: {} };

  function plRow(pid) {
    for (var k = 0; k < PL.list.length; k++) { if (PL.list[k].id === pid) return PL.list[k]; }
    return null;
  }
  function plShares(pid) {
    return PL.shares.filter(function (s) { return s.pid === pid; });
  }
  function plIsFav() { var r = plRow(PL.pid); return !!(r && r.fav); }
  function shareUrl(token) { return (PL.base || "") + "/api/share/" + token; }

  function fmtBytes(n) {
    n = Number(n) || 0;
    if (n >= 1073741824) return (n / 1073741824).toFixed(2) + " GB";
    if (n >= 1048576) return (n / 1048576).toFixed(1) + " MB";
    if (n >= 1024) return Math.round(n / 1024) + " KB";
    return n + " B";
  }

  function loadPlaylists() {
    return api("/api/playlists").then(function (d) {
      PL.list = d.playlists || [];
      PL.shares = d.shares || [];
      PL.favId = d.fav_id || 0;
      PL.base = d.share_base_url || "";
      PL.cfg = { root: d.archive_root || "", mode: d.archive_mode || "copy", auto: d.auto_archive !== false };
      renderPlCards();
      if (PL.pid) {
        if (plRow(PL.pid)) fillPlDetailHead();
        else { PL.pid = 0; $("pl-detail").classList.add("hidden"); }
      }
    }).catch(function (e) {
      $("pl-sum").textContent = "加载失败";
      toast("歌单加载失败: " + e.message);
    });
  }

  /* 红心变动后同步「我的喜欢」卡片计数；正看它的明细时连列表体一起重拉 */
  function plSyncFavN(total, refetch) {
    if (typeof PL === "undefined" || !PL || !PL.list || !PL.list.length) return;
    var hit = false;
    PL.list.forEach(function (p) { if (p.fav) { p.n = total; hit = true; } });
    if (!hit) return;
    renderPlCards();
    if (refetch && PL.pid && PL.pid === PL.favId) openPlaylist(PL.pid);
  }

  /* 明细重拉到手后校正计数：乐观更新只改 n，「在库 / 待下」会和真实条目脱节 */
  function plSyncCounts(pid) {
    var r = plRow(pid);
    if (!r) return;
    var ln = 0;
    PL.items.forEach(function (x) { if (x.local) ln++; });
    var n = PL.items.length, pd = n - ln;
    if (r.n === n && r.local_n === ln && r.pending === pd) return;
    r.n = n; r.local_n = ln; r.pending = pd;
    renderPlCards();
    if (PL.pid === pid) fillPlDetailHead();
  }

  function renderPlCards() {
    var fav = null, mine = [];
    PL.list.forEach(function (p) { if (p.fav) { if (!fav) fav = p; } else mine.push(p); });
    $("plc-fav-n").textContent = (fav ? fav.n : 0) + " 首" + (fav && fav.pending ? " · 待下 " + fav.pending : "");
    $("plc-my-n").textContent = mine.length + " 个歌单";
    var total = PL.list.reduce(function (s, p) { return s + (p.n || 0); }, 0);
    $("pl-sum").textContent = total ? (PL.list.length + " 个歌单 · 共 " + total + " 首") : "还没有收藏或歌单";
    $("plc-fav").classList.toggle("active", !!PL.pid && PL.pid === PL.favId);
    $("plc-my").classList.toggle("active", !!PL.pid && PL.pid !== PL.favId);
    $("pl-list").classList.toggle("hidden", !mine.length);
    $("pl-list").innerHTML = mine.map(function (p) {
      var b = ['<span class="badge badge-gray">' + p.n + ' 首</span>'];
      if (p.pending) b.push('<span class="badge badge-warn">待下 ' + p.pending + '</span>');
      if (p.archivable) b.push('<span class="badge badge-ok">自动归档</span>');
      if (plShares(p.id).length) b.push('<span class="badge" style="background:rgba(94,92,230,.22);color:#9d9bff">分享中</span>');
      return '<div class="plitem' + (p.id === PL.pid ? " on" : "") + '" data-pid="' + p.id + '">' +
        '<div class="nm" title="' + esc(p.target_dir || "无归档目录") + '">' + esc(p.name) + '</div>' +
        '<div class="mt">' + b.join("") + '</div></div>';
    }).join("");
    Array.prototype.forEach.call($("pl-list").querySelectorAll(".plitem"), function (el) {
      el.onclick = function () { openPlaylist(Number(el.getAttribute("data-pid"))); };
    });
    $("pl-empty").textContent = mine.length ? "" : "还没有自建歌单：点下方「+ 新建歌单」，或在全屏播放器里点「+ 添加到歌单」。";
  }

  function plShowMine() { PL.pid = 0; $("pl-detail").classList.add("hidden"); renderPlCards(); }

  /* 歌单条目 → 播放条目：在库走本地文件，未下载优先走分享人的直链 */
  function plTrackOf(it) {
    if (it.local) {
      return libTrack({
        path: it.path, name: String(it.path || "").split(/[\\/]/).pop(),
        title: it.title, artist: it.artist, album: it.album,
        has_lyric: it.has_lyric, has_cover: it.has_cover
      });
    }
    var s = it.song || {};
    if (!s.name) {
      s = { name: it.title || "", artist: it.artist || "", album: it.album || "",
            duration: it.duration || 0, source: "share", id: String(it.id) };
    }
    var tr = dlTrack(s);
    if (s.remote_url) { tr.audioUrl = s.remote_url; tr.lyricApi = ""; tr.qual = "分享直链"; }
    return tr;
  }

  function plItemsHave(tr) {
    var k = trackFavKey(tr);
    return PL.items.some(function (x) {
      if (k && x.path === k) return true;
      return (x.title || "") === (tr.title || "") && (x.artist || "") === (tr.artist || "");
    });
  }

  function openPlaylist(pid) {
    var row = plRow(pid);
    if (!row) { toast("歌单不存在或已删除"); return; }
    PL.pid = pid;
    $("pl-detail").classList.remove("hidden");
    fillPlDetailHead();
    renderPlCards();
    $("pld-box").innerHTML = '<div class="hint" style="font-size:12px">加载中…</div>';
    api("/api/playlist/items?pid=" + pid).then(function (d) {
      if (PL.pid !== pid) return;
      PL.items = d.items || [];
      plSyncCounts(pid);
      renderPlItems();
    }).catch(function (e) {
      $("pld-box").innerHTML = '<div class="hint" style="font-size:12px;color:var(--danger)">' + esc(e.message) + '</div>';
    });
  }

  function fillPlDetailHead() {
    var r = plRow(PL.pid) || {};
    $("pld-title").textContent = r.fav ? "我的喜欢" : (r.name || "歌单");
    $("pld-count").textContent = (r.n || 0) + " 首" + (r.local_n != null ? " · 在库 " + r.local_n : "");
    var st = plShares(r.id);
    var tag = $("pld-share-tag");
    tag.style.display = st.length ? "" : "none";
    if (st.length) {
      var b = st.reduce(function (a, s) { return a + (s.bytes || 0); }, 0);
      tag.textContent = "分享中 · 已下发 " + fmtBytes(b);
      tag.title = "已被打开 " + st[0].hits + " 次；这些流量走的是本机的上行带宽";
    } else { tag.title = ""; }
    var sub = [r.fav ? "内置收藏歌单，不能删除，可以整体分享或归档。" : "自建歌单，可分享 / 归档 / 删除。"];
    sub.push(r.target_dir ? ("归档目标：" + r.target_dir + "（" + (r.archive_mode === "move" ? "移动文件" : "另存副本") + "）")
                          : "未设置归档目录（可在下方填写，或到设置 → 歌单分享统一指定）");
    if (!PL.cfg.auto) sub.push("注意：设置里的「新加入自动归档」已关闭，只会手动执行。");
    $("pld-sub").textContent = sub.join(" ");
    $("pld-archdir").value = r.archive_root || PL.cfg.root || "";
    $("pld-archmode").value = r.archive_mode || PL.cfg.mode || "copy";
    $("pld-autosync").checked = r.auto_sync !== false;
    $("pld-del").style.display = r.fav ? "none" : "";
    $("pld-rename").style.display = r.fav ? "none" : "";
    $("pld-fetch").style.display = r.pending ? "" : "none";
    $("pld-fetch").textContent = "补下缺失" + (r.pending ? "（" + r.pending + "）" : "");
  }

  function renderPlItems() {
    var box = $("pld-box");
    if (!PL.items.length) {
      box.innerHTML = '<div class="hint" style="font-size:12.5px">这个歌单还是空的。去音乐库点 ♥，' +
        '或在全屏播放器里点「喜欢 / 添加到歌单」。</div>';
      return;
    }
    box.innerHTML = PL.items.map(function (it, idx) {
      var badges = [];
      if (it.local) {
        badges.push('<span class="badge badge-ok">在库</span>');
        if (it.size) badges.push('<span style="color:var(--sub);font-size:11.5px">' + fmtSize(it.size) + '</span>');
      } else {
        badges.push('<span class="badge badge-warn">' + (it.fetchable ? "本地没有·可补下" : "本地没有") + '</span>');
      }
      if (it.duration) badges.push('<span style="color:var(--sub);font-size:11.5px">' + fmtDur(it.duration) + '</span>');
      var on = isFavKey(it.path);
      return '<div class="plrow" data-i="' + idx + '">' +
        '<span class="nm" title="' + esc(it.path || "") + '">' + esc(it.title || "（未知歌曲）") +
        ' <span style="color:var(--sub);font-weight:400">- ' + esc(it.artist || "") + '</span></span>' +
        badges.join("") +
        '<span class="op">' +
        (it.local ? '<button class="btn btn-ghost btn-sm" data-pact="play" title="从本首连续播放">▶</button>' : "") +
        '<button class="btn btn-ghost btn-sm icobtn' + (on ? " btn-primary" : "") + '" data-pact="fav" title="' +
          (on ? "取消喜欢（我的喜欢）" : "标记到我的喜欢") + '">♥</button>' +
        '<button class="btn btn-ghost btn-sm icobtn" data-pact="addpl" title="添加到其他歌单">+</button>' +
        '<button class="btn btn-ghost btn-sm icobtn" data-pact="del" title="从本歌单移除（不删文件）">✕</button>' +
        '</span></div>';
    }).join("");
    Array.prototype.forEach.call(box.querySelectorAll("[data-pact]"), function (b) {
      var row = b.closest ? b.closest(".plrow") : null;
      var it = PL.items[row ? Number(row.getAttribute("data-i")) : -1];
      if (!it) return;
      var act = b.getAttribute("data-pact");
      if (act === "play") b.onclick = function () { plPlayFrom(it); };
      else if (act === "fav") b.onclick = function () {
        favToggle(it.local ? { path: it.path } : { song: it.song || plTrackOf(it).ref },
                  it.path, function (on2) {
                    b.classList.toggle("btn-primary", on2);
                    b.title = on2 ? "取消喜欢（我的喜欢）" : "标记到我的喜欢";
                  });
      };
      else if (act === "addpl") b.onclick = function () { openAddPl(plTrackOf(it)); };
      else if (act === "del") b.onclick = function () {
        if (!confirm("从歌单移除「" + (it.title || "") + "」？（不会删除本地文件）")) return;
        api("/api/playlist/remove", { method: "POST", body: { item_id: it.id } })
          .then(function () { return loadPlaylists(); })
          .then(function () { openPlaylist(PL.pid); })
          .catch(function (e) { toast(e.message); });
      };
    });
  }

  function plPlayFrom(it) {
    var tracks = [], start = 0;
    PL.items.forEach(function (x) {
      if (!x.local) return;
      if (x.id === it.id) start = tracks.length;
      tracks.push(plTrackOf(x));
    });
    openTrackList(tracks, start);
  }
  function plPlayAll() {
    openTrackList(PL.items.filter(function (x) { return x.local; }).map(function (x) {
      return plTrackOf(x);
    }), 0);
  }

  /* ---- 归档：另存/移动到 归档目录/<歌单名>/ ---- */
  function plArchive() {
    var r = plRow(PL.pid) || {};
    var root = $("pld-archdir").value.trim() || PL.cfg.root || "";
    var mode = $("pld-archmode").value || "copy";
    if (!root) { toast("请先填写归档目录（需绝对路径）"); return; }
    if (root.charAt(0) !== "/") { toast("归档目录需为服务器上的绝对路径，如 /vol1/1000/Music"); return; }
    if (!confirm((mode === "move" ? "移动" : "另存") + "「" + (r.name || "") + "」的 " + (r.n || 0) + " 首歌到：\n" +
      root + "/" + (r.name || "") + "\n\n" +
      (mode === "move"
        ? "· 文件会从原位置移走（歌单自动跟随新路径）\n· 飞牛需重新扫描才能看到新位置\n"
        : "· 复制一份到目标目录，原文件不变\n") +
      "· 同名歌词/封面会一并处理；已归档过的同名同大小文件会跳过\n\n确定开始？")) return;
    api("/api/playlist/archive", { method: "POST", body: { id: PL.pid, root: root, mode: mode } })
      .then(function () { toast("归档任务已开始，进度见概览页"); loadStatus(); pollTask(); })
      .catch(function (e) { toast(e.message); });
  }
  function savePlArchive() {
    if (!PL.pid) { toast("请先打开一个歌单"); return; }
    api("/api/playlist/update", {
      method: "POST",
      body: {
        id: PL.pid,
        archive_dir: $("pld-archdir").value.trim(),
        archive_mode: $("pld-archmode").value,
        auto_sync: $("pld-autosync").checked,
      }
    }).then(function () {
      toast("归档设置已保存" + ($("pld-autosync").checked ? "（以后新加入的歌会自动执行）" : ""));
      return loadPlaylists();
    }).catch(function (e) { toast(e.message); });
  }

  function plCreate() {
    var nm = $("pl-newname").value.trim();
    if (!nm) { toast("请输入歌单名称"); return; }
    api("/api/playlist/create", { method: "POST", body: { name: nm } }).then(function (d) {
      $("pl-newname").value = "";
      $("pl-newwrap").classList.add("hidden");
      return loadPlaylists().then(function () { openPlaylist(d.id); });
    }).catch(function (e) { toast(e.message); });
  }
  function plRename() {
    var r = plRow(PL.pid) || {};
    var v = prompt("歌单名称（归档目录名也会跟着变）：", r.name || "");
    if (v === null) return;
    v = v.trim();
    if (!v || v === r.name) return;
    api("/api/playlist/update", { method: "POST", body: { id: PL.pid, name: v } })
      .then(function () { toast("已改名"); return loadPlaylists(); })
      .catch(function (e) { toast(e.message); });
  }
  function plDelete() {
    var r = plRow(PL.pid) || {};
    if (r.fav) { toast("「我的喜欢」不能删除"); return; }
    if (!confirm("删除歌单「" + r.name + "」？\n\n· 只删歌单记录和分享链接\n· 不会删除任何音乐文件\n\n确定？")) return;
    api("/api/playlist/delete", { method: "POST", body: { id: PL.pid } })
      .then(function () {
        toast("已删除歌单");
        PL.pid = 0;
        $("pl-detail").classList.add("hidden");
        return loadPlaylists();
      }).catch(function (e) { toast(e.message); });
  }
  function plFetch() {
    var r = plRow(PL.pid) || {};
    if (!confirm("把本地没有的 " + (r.pending || 0) + " 首歌从分享人的直链下载到本机？\n\n" +
      "· 下载走对方服务器，会占用对方的上行带宽（如果对方就是你，就是占自己）\n" +
      "· 保存到设置的下载目录，完成后自动入库整理\n\n确定？")) return;
    api("/api/playlist/fetch-pending", { method: "POST", body: { id: PL.pid } })
      .then(function () { toast("补下任务已开始，进度见概览页"); loadStatus(); pollTask(); })
      .catch(function (e) { toast(e.message); });
  }

  /* ---- 分享弹窗 ---- */
  function openShare() {
    var r = plRow(PL.pid) || {};
    if (!r.id) { toast("请先在上方打开一个歌单再分享"); return; }
    $("share-modal").classList.add("show");
    $("shm-name").textContent = (r.fav ? "我的喜欢" : r.name) + " · " + (r.n || 0) + " 首 · 在库 " + (r.local_n || 0) + " 首";
    renderShareList();
  }
  function closeShare() { $("share-modal").classList.remove("show"); }
  function renderShareList() {
    var st = plShares(PL.pid), box = $("shm-list");
    if (!st.length) {
      box.innerHTML = '<div class="hint" style="font-size:12px">暂无分享</div>';
      $("shm-linkwrap").classList.add("hidden");
      $("shm-stat").textContent = "尚未生成链接";
      return;
    }
    $("shm-linkwrap").classList.remove("hidden");
    $("shm-link").textContent = shareUrl(st[0].token);
    var totB = st.reduce(function (a, s) { return a + (s.bytes || 0); }, 0);
    $("shm-stat").textContent = "已被打开 " + (st[0].hits || 0) + " 次 · 已转发 " + fmtBytes(totB);
    box.innerHTML = st.map(function (s) {
      return '<div class="shr"><span class="mono" style="flex:1;min-width:150px;word-break:break-all">' +
        esc(shareUrl(s.token)) + '</span>' +
        '<span>' + esc(s.created || "") + '</span><span>打开 ' + (s.hits || 0) + ' 次</span>' +
        '<span>已下发 ' + fmtBytes(s.bytes) + '</span>' +
        '<button class="btn btn-ghost btn-sm shr-copy" data-t="' + esc(s.token) + '">复制</button>' +
        '<button class="btn btn-danger btn-sm shr-off" data-t="' + esc(s.token) + '">关闭</button></div>';
    }).join("");
    Array.prototype.forEach.call(box.querySelectorAll(".shr-copy"), function (b) {
      b.onclick = function () { copyText(shareUrl(b.getAttribute("data-t"))); };
    });
    Array.prototype.forEach.call(box.querySelectorAll(".shr-off"), function (b) {
      b.onclick = function () { revokeShare(b.getAttribute("data-t")); };
    });
  }
  function makeShare() {
    if (!PL.pid) { toast("请先打开一个歌单"); return; }
    var btn = $("shm-gen");
    btn.disabled = true;
    api("/api/share/create", { method: "POST", body: { pid: PL.pid } })
      .then(function () { toast("已生成分享链接（旧链接已失效）"); return loadPlaylists(); })
      .then(function () { renderShareList(); renderPlCards(); })
      .catch(function (e) { toast(e.message); })
      .finally(function () { btn.disabled = false; });
  }
  function revokeShare(token) {
    if (!confirm("关闭这个分享链接？\n\n· 之前拿到链接的人会立即打不开\n· 不影响本地歌单\n\n确定关闭？")) return;
    api("/api/share/revoke", { method: "POST", body: { token: token } })
      .then(function () { toast("分享已关闭"); return loadPlaylists(); })
      .then(function () { renderShareList(); renderPlCards(); fillPlDetailHead(); })
      .catch(function (e) { toast(e.message); });
  }
  function copyText(t, msg) {
    var ta = document.createElement("textarea");
    ta.value = t;
    ta.style.cssText = "position:fixed;left:-9999px;top:0";
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand("copy"); } catch (e) {}
    document.body.removeChild(ta);
    toast(ok ? (msg || "链接已复制，发给朋友即可（对方无需账号）") : "复制失败，请手动选中复制");
  }

  /* ---- 导入别人的分享歌单 ---- */
  function ispPreview() {
    var u = $("isp-url").value.trim();
    if (!u) { toast("请先粘贴分享链接"); return; }
    $("isp-box").innerHTML = '<div class="hint" style="font-size:12px">读取中…</div>';
    api("/api/share/preview?url=" + encodeURIComponent(u)).then(function (d) {
      renderIspPreview(d);
    }).catch(function (e) {
      $("isp-box").innerHTML = '<div class="warnbox">' + esc(e.message) + '</div>';
    });
  }
  function renderIspPreview(d, extra) {
    var songs = d.songs || [];
    /* s.file = 分享人本地有（可走直链补下）；s.local = 你本地已有（由服务端匹配得出）
       两者含义不同，得分开统计，否则预览与导入的结论会自相矛盾。 */
    var have = (typeof d.matched === "number") ? d.matched
      : songs.filter(function (s) { return s.local; }).length;
    var fetchable = (typeof d.fetchable === "number") ? d.fetchable
      : songs.filter(function (s) { return s.file && !s.local; }).length;
    $("isp-box").innerHTML =
      (extra || "") +
      '<div class="hint" style="font-size:12.5px;margin-bottom:6px">「' + esc(d.name || "") + '」共 ' +
      (d.count || songs.length) + ' 首 · 你本地已有 ' + have + ' 首 · 需从对方直链补下 ' + fetchable + ' 首</div>' +
      '<div style="max-height:240px;overflow-y:auto;background:rgba(0,0,0,.25);border:1px solid var(--border);border-radius:12px;padding:4px 12px">' +
      songs.map(function (s) {
        return '<div class="plrow"><span class="nm">' + esc(s.title || s.name || "") +
          ' <span style="color:var(--sub);font-weight:400">- ' + esc(s.artist || "") + '</span></span>' +
          (s.local ? '<span class="badge badge-ok">本地已有</span>'
                   : (s.file ? '<span class="badge badge-warn">可从对方下</span>'
                             : '<span class="badge badge-gray">对方也无此歌</span>')) +
          '</div>';
      }).join("") + '</div>';
  }
  function ispImport() {
    var u = $("isp-url").value.trim();
    if (!u) { toast("请先粘贴分享链接"); return; }
    var btn = $("btn-isp-import");
    btn.disabled = true; btn.textContent = "导入中…";
    api("/api/playlist/import-share", { method: "POST", body: { url: u, name: $("isp-name").value.trim() } })
      .then(function (d) {
        PL.pid = d.id;
        return loadPlaylists().then(function () {
          openPlaylist(d.id);
          $("isp-box").innerHTML = '<div class="warnbox" style="background:rgba(48,209,88,.1);' +
            'border-color:rgba(48,209,88,.32);color:#8ef0b0">已导入为「' + esc(d.name) + '」：共 ' + d.total +
            ' 首，本地已有 ' + d.matched + ' 首，待下载 ' + d.pending + ' 首' +
            (d.fetchable ? ('（其中 ' + d.fetchable + ' 首可从对方直链补下，点歌单里的「补下缺失」）')
                         : (d.pending ? '（对方本地也没有这些歌，无法补下）' : '')) +
            (d.skipped ? ('；有 ' + d.skipped + ' 首因信息不全未能导入') : '') + '。</div>';
          toast("导入完成：" + d.name);
        });
      })
      .catch(function (e) { $("isp-box").innerHTML = '<div class="warnbox">' + esc(e.message) + '</div>'; })
      .finally(function () { btn.disabled = false; btn.textContent = "导入到我的歌单"; });
  }

  /* ---- “添加到歌单”弹窗（可从任意歌曲打开） ---- */
  var APM = { track: null, fresh: {} };
  function openAddPl(tr) {
    if (!tr) { toast("请先选择一首歌"); return; }
    APM.track = tr;
    $("addpl-modal").classList.add("show");
    $("apm-song").textContent = (tr.artist ? tr.artist + " - " : "") + (tr.title || "");
    renderAddPlList();
    /* 歌单列表可能未加载（从音乐库/下载页直接打开），补一次再重绘 */
    loadPlaylists().then(renderAddPlList);
  }
  function closeAddPl() { $("addpl-modal").classList.remove("show"); }
  function renderAddPlList() {
    var box = $("apm-list"), tr = APM.track;
    if (!tr) return;
    var k = trackFavKey(tr);
    box.innerHTML = PL.list.map(function (p) {
      var has = p.fav ? isFavKey(k) : (APM.fresh[p.id] || (p.id === PL.pid && plItemsHave(tr)));
      return '<label class="srcbox" style="justify-content:flex-start;gap:8px" data-pid="' + p.id + '">' +
        '<input type="checkbox" class="apm-chk" value="' + p.id + '"' + (has ? " checked" : "") + '>' +
        '<span>' + esc(p.fav ? "我的喜欢" : p.name) + '</span>' +
        '<span style="color:var(--sub);font-size:11.5px">' + p.n + ' 首' + (p.archivable ? " · 自动归档" : "") + '</span></label>';
    }).join("") || '<div class="hint" style="font-size:12px">还没有歌单，可在下方新建。</div>';
  }
  function addPlCreate() {
    var nm = $("apm-new").value.trim();
    if (!nm) { toast("请输入新歌单名称"); return; }
    api("/api/playlist/create", { method: "POST", body: { name: nm } }).then(function (d) {
      $("apm-new").value = "";
      APM.fresh[d.id] = true;
      return loadPlaylists().then(renderAddPlList);
    }).catch(function (e) { toast(e.message); });
  }
  function addPlSubmit() {
    var tr = APM.track;
    if (!tr) { closeAddPl(); return; }
    var ids = [];
    Array.prototype.forEach.call($("apm-list").querySelectorAll(".apm-chk"), function (cb) {
      if (cb.checked) ids.push(Number(cb.value));
    });
    if (!ids.length) { toast("请勾选至少一个歌单"); return; }
    var body = (tr.src === "lib" && tr.ref && tr.ref.path)
      ? { path: tr.ref.path } : { song: tr.ref || { name: tr.title, artist: tr.artist } };
    var done = 0, msgs = [];
    ids.forEach(function (pid) {
      var req = { pid: pid };
      if (body.path) req.path = body.path; else req.song = body.song;
      api("/api/playlist/add", { method: "POST", body: req })
        .then(function (d) {
          var m = (d.playlist || "歌单") + (d.existed ? "（已在其中）" : "");
          if (d.archive && d.archive.state === "ok") m += " · 已归档";
          msgs.push(m);
        })
        .catch(function (e) { msgs.push("失败：" + e.message); })
        .finally(function () {
          done++;
          if (done < ids.length) return;
          closeAddPl();
          APM.fresh = {};
          toast("已加入：" + (msgs.join("，") || "完成"));
          loadFavMap().then(function () {
            renderLibrary();       // 刷音乐库红心
            renderDlResults();     // 刷下载搜索结果红心
          });
          if (!$("tab-playlist").classList.contains("hidden")) {
            loadPlaylists().then(function () { if (PL.pid) openPlaylist(PL.pid); });
          }
        });
    });
  }

  /* ---------- 去重 ---------- */
  /* 去重组的 delete 列表缓存（渲染后由事件读取） */
  var g_deletes = {};

  function loadDuplicatesCached() {
    var kind = ($("dup-kind") || {}).value || "both";
    return api("/api/duplicates?kind=" + kind).then(function (d) {
      g_deletes = {};
      d.groups.forEach(function (g, gi) {
        var keep = g.items[0].path;
        g_deletes[gi] = g.items.filter(function (it) { return it.path !== keep; }).map(function (it) { return it.path; });
      });
      var list = $("dup-list");
      var nc = d.groups.filter(function (g) { return g.type === "content"; }).length;
      var nn = d.groups.length - nc;
      $("dup-total").textContent = d.groups.length + " 组" + (kind === "both" && d.groups.length ? "（内容 " + nc + " · 名称 " + nn + "）" : "");
      if (!d.groups.length) {
        list.innerHTML = '<div class="empty">没有发现重复文件。</div>';
        return;
      }
      list.innerHTML = d.groups.map(function (g, gi) {
        var sizeMB = (g.total_size / 1024 / 1024).toFixed(1);
        var rows = g.items.map(function (it, i) {
          var info = (it.artist ? it.artist + " - " : "") + (it.title || it.name);
          return '<div class="dup-item">' +
            '<input type="radio" class="radio" name="dup-' + gi + '" value="' + esc(it.path) + '"' + (i === 0 ? " checked" : "") + '>' +
            '<div style="flex:1;min-width:0">' +
            '<div style="word-break:break-all">' + esc(it.path) + '</div>' +
            '<div style="font-size:12px;color:var(--sub)">' + esc(info) + ' · ' + (it.size / 1024 / 1024).toFixed(1) + ' MB</div>' +
            '</div></div>';
        }).join("");
        var head = g.type === "name"
          ? '<span class="badge" style="background:rgba(255,159,10,.16);color:#ff9f0a">名称重复</span> 同一首歌不同版本 · ' + g.count + ' 份 · 建议保留无损/高码率 · 选择保留项'
          : '<span class="badge" style="background:rgba(94,92,230,.2);color:#9d9bff">内容重复</span> SHA-1 相同 · ' + g.count + ' 份 · 共 ' + sizeMB + ' MB · 选择保留项';
        return '<div class="dup-group">' +
          '<div style="font-size:12px;color:var(--sub);margin-bottom:6px">' + head + '</div>' +
          rows +
          '<div class="row" style="margin-top:10px">' +
          '<button class="btn btn-primary btn-sm dup-resolve" data-group="' + gi + '" data-mode="trash">保留选中并移入回收站</button>' +
          '<button class="btn btn-danger btn-sm dup-resolve" data-group="' + gi + '" data-mode="delete">保留选中并删除其余</button>' +
          '</div></div>';
      }).join("");
      Array.prototype.forEach.call(document.querySelectorAll(".dup-resolve"), function (b) {
        b.onclick = function () {
          var gi = Number(b.getAttribute("data-group"));
          var checked = document.querySelector('input[name="dup-' + gi + '"]:checked');
          if (!checked) { toast("请选择要保留的文件"); return; }
          var keep = checked.value;
          var deletes = g_deletes[gi];
          if (!confirm("确定保留该文件，并处理其余 " + deletes.length + " 个重复文件吗？")) return;
          api("/api/duplicates/resolve", { method: "POST", body: { keep: keep, delete: deletes, mode: b.getAttribute("data-mode") } })
            .then(function (r) {
              var ok = r.results.filter(function (x) { return x.ok; }).length;
              toast("已处理 " + ok + " 个文件");
              loadDuplicatesCached(); loadStatus();
            });
        };
      });
    });
  }

  /* ---------- 重复封面检测 ---------- */
  function loadDupCovers() {
    var th = Number($("dcov-threshold").value || 3);
    var list = $("dcov-list");
    list.innerHTML = '<div class="empty">检测中…</div>';
    return api("/api/duplicate_covers?threshold=" + th).then(function (d) {
      $("dcov-total").textContent = d.groups.length + " 组可疑";
      if (!d.groups.length) {
        list.innerHTML = '<div class="empty">未发现跨目录重复的封面，一切正常。</div>';
        return;
      }
      list.innerHTML = d.groups.map(function (g) {
        var rows = g.sample.map(function (s) {
          return '<div style="font-size:12px;color:var(--sub);word-break:break-all;padding:2px 0">' +
            esc((s.artist ? s.artist + " - " : "") + (s.title || s.path)) + '</div>';
        }).join("");
        var more = g.count > g.sample.length ? '<div style="font-size:12px;color:#8e8e93">…等共 ' + g.count + ' 首</div>' : "";
        return '<div class="dup-group">' +
          '<div style="font-size:12px;color:var(--sub);margin-bottom:6px">同一张封面 · 跨 ' + g.folders + ' 个目录 · ' + g.count + ' 首 · ' +
          (g.size / 1024).toFixed(0) + ' KB</div>' +
          '<div style="display:flex;gap:10px;align-items:flex-start">' +
          '<img src="' + mediaUrl(g.songs[0], "cover") + '" style="width:64px;height:64px;border-radius:8px;object-fit:cover;background:rgba(255,255,255,.07)" alt="">' +
          '<div style="flex:1;min-width:0">' + rows + more + '</div></div></div>';
      }).join("");
    }).catch(function (e) {
      list.innerHTML = '<div class="empty">' + esc(e.message) + '</div>';
    });
  }
  function fixDupCovers() {
    var th = Number($("dcov-threshold").value || 3);
    if (!confirm("将逐首调用 AI 识别歌曲并从曲库重新下载封面替换（每首一次识别+一次下载，较慢）。确定开始？")) return;
    var btn = $("btn-dcov-fix");
    btn.disabled = true; btn.textContent = "任务已启动…";
    api("/api/duplicate_covers/fix", { method: "POST", body: { threshold: th } })
      .then(function () {
        toast("AI 修复封面任务已启动，进度见概览页");
        loadStatus();
      })
      .catch(function (e) { toast(e.message); })
      .finally(function () { btn.disabled = false; btn.textContent = "AI 重新获取并更换"; });
  }
  $("btn-dcov-scan").onclick = loadDupCovers;
  $("btn-dcov-fix").onclick = fixDupCovers;

  /* ---------- 设置 ---------- */
  /* 平台谐音别名：与后端 SOURCE_ALIAS_DEFAULT 一致；某项清则该处回退真实名称 */
  var SRC_ALIAS = { netease: "网忆云", qq: "秋秋音乐", migu: "米菇音乐", kugou: "库狗音乐", kuwo: "库我音乐" };
  var SRC_REAL = { netease: "网易云音乐", qq: "QQ 音乐", migu: "咪咕音乐", kugou: "酷狗音乐", kuwo: "酷我音乐" };
  function srcShow(k, alias) {
    var v = (alias && (k in alias)) ? String(alias[k] || "").trim() : (SRC_ALIAS[k] || "");
    return v || SRC_REAL[k];
  }
  function applySrcNames(alias) {
    alias = alias || {};
    Object.keys(SRC_ALIAS).forEach(function (k) {
      var el = $("src-nm-" + k);
      if (el) el.textContent = srcShow(k, alias);
      var inp = $("cfg-al-" + k);
      if (inp && document.activeElement !== inp) {
        inp.value = (k in alias) ? (alias[k] || "") : SRC_ALIAS[k];
        inp.placeholder = (k in alias && !alias[k]) ? SRC_REAL[k] : SRC_ALIAS[k];
      }
    });
    var tag = $("dl-pl-platforms");
    Array.prototype.forEach.call(document.querySelectorAll("[data-alias]"), function (el) {
      var k = el.getAttribute("data-alias");
      if (SRC_ALIAS[k]) el.textContent = srcShow(k, alias);
    });
    if (tag) {
      tag.textContent = ["netease", "qq", "kugou", "kuwo"].map(function (k) {
        return srcShow(k, alias).replace(/\s*音乐$/, "");
      }).join(" / ");
    }
  }

  function loadWatch() {
    return api("/api/watch").then(function (d) {
      state.watch = d.watcher;
      var w = $("cfg-watch");
      if (w) w.checked = !!d.watcher.enable;
      var iv = $("cfg-watch-interval");
      if (iv) iv.value = d.watcher.interval || 30;
      renderWatchTag();
    }).catch(function () {});
  }

  /* ---------- AI 用量统计（设置页 AI 子页）---------- */
  function fmtTok(n) {
    n = Number(n) || 0;
    /* token 动辄几十万，直接写一串数字看不出量级；按万/亿缩一下，保留一位小数 */
    if (n >= 100000000) return (n / 100000000).toFixed(1) + " 亿";
    if (n >= 10000) return (n / 10000).toFixed(1) + " 万";
    return String(n);
  }

  function loadAiUsage() {
    return api("/api/ai/usage?days=14").then(function (d) {
      state.aiUsage = d;
      renderAiUsage();
      return d;
    }).catch(function (e) {
      var box = $("ai-usage");
      if (box) box.innerHTML = '<span style="color:#ff6961">用量读取失败：' + esc(e.message) + '</span>';
    });
  }

  function usageCell(label, value, sub) {
    return '<div class="card"><div class="label">' + esc(label) + '</div>'
      + '<div class="value">' + esc(value) + '</div>'
      + (sub ? '<div class="label" style="margin-top:2px">' + esc(sub) + '</div>' : '') + '</div>';
  }

  function renderAiUsage() {
    var box = $("ai-usage");
    if (!box) return;
    var d = state.aiUsage;
    if (!d || !d.ok) {
      box.innerHTML = '<div class="hint" style="font-size:12px;color:var(--sub)">' 
        + esc((d && d.error) || "暂无用量记录 —— 用过一次 AI 功能就会自动开始统计") + '</div>';
      return;
    }
    var td = d.today || {}, tt = d.total || {};
    var html = '<div class="cards" style="margin:0 0 10px">'
      + usageCell("今日 token", fmtTok(td.ttok), "输入 " + fmtTok(td.ptok) + " · 输出 " + fmtTok(td.ctok))
      + usageCell("今日调用", (td.calls || 0) + " 次", (td.fails ? "失败 " + td.fails + " 次" : "无失败"))
      + usageCell("累计 token", fmtTok(tt.ttok), "输入 " + fmtTok(tt.ptok) + " · 输出 " + fmtTok(tt.ctok))
      + usageCell("累计调用", (tt.calls || 0) + " 次", d.first_day ? "自 " + d.first_day + " 起" : "还没有记录")
      + '</div>';
    if ((d.by_kind || []).length) {
      html += '<table><thead><tr><th>用途</th><th>次数</th><th>输入</th>'
        + '<th>输出</th><th>合计 token</th></tr></thead><tbody>'
        + d.by_kind.map(function (k) {
            return '<tr><td>' + esc(k.label || k.kind) + '</td><td>' + (k.calls || 0)
              + (k.fails ? '（失败 ' + k.fails + '）' : '') + '</td><td>' + fmtTok(k.ptok)
              + '</td><td>' + fmtTok(k.ctok) + '</td><td>' + fmtTok(k.ttok) + '</td></tr>';
          }).join("")
        + '</tbody></table>';
    }
    /* 部分兼容接口不返回 usage 字段：只记次数不记 token。不写明白，用户会以为“没花钱” */
    html += '<div class="hint" style="font-size:12px;color:var(--sub);margin-top:8px">'
      + "统计只算本应用发出的 AI 请求（识别文件名 / 生成歌词 / 分析决策 / 生封面 等），"
      + "不含平台自带额度；"
      + (tt.miss ? "其中 " + fmtTok(tt.miss) + " 次接口没回 token 明细（只计次数）· " : "")
      + "额度花费以服务商后台为准。" + '</div>';
    box.innerHTML = html;
  }
  function loadConfig() {
    return Promise.all([api("/api/config"), loadWatch()]).then(function (r) {
      var d = r[0];
      state.config = d.config;
      var llm = d.config.llm || {};
      $("cfg-llm-url").value = llm.base_url || "";
      /* 脱敏 key 不回填输入框（回填后原样提交会污染真实 key）；
         留空提交 = 后端保持已保存的 key 不变 */
      $("cfg-llm-key").value = "";
      $("cfg-llm-key").placeholder = llm.api_key
        ? "已保存（" + llm.api_key + "），留空保持不变；输入新值可替换"
        : "sk-...";
      $("cfg-llm-model").value = llm.model || "";
      $("cfg-llm-enabled").checked = !!d.config.enable_llm;
      var img = llm.image || {};
      $("cfg-img-url").value = img.base_url || "";
      $("cfg-img-key").value = "";
      $("cfg-img-key").placeholder = img.api_key
        ? "已保存（" + img.api_key + "），留空保持不变；输入新值可替换"
        : "留空复用 LLM Key";
      $("cfg-img-model").value = img.model || "";
      $("cfg-ai-cover").checked = !!d.config.enable_ai_cover;
      $("cfg-scrape").checked = !!d.config.enable_scrape;
      $("cfg-ow-lyric").checked = !!d.config.overwrite_lyric;
      $("cfg-ow-cover").checked = !!d.config.overwrite_cover;
      $("cfg-embed").checked = !!d.config.embed_tags;
      $("cfg-classify").checked = !!d.config.enable_classify;
      // 音源设置
      var ms = d.config.music_source || {};
      var enabled = ms.enabled_sources || ["netease", "qq", "migu", "kugou", "kuwo"];
      Array.prototype.forEach.call(document.querySelectorAll(".cfg-ms-src"), function (cb) {
        cb.checked = enabled.indexOf(cb.value) >= 0;
      });
      renderMsApis(ms.custom_apis || []);
      $("cfg-ms-dldir").value = ms.download_dir || "";
      var ck = ms.cookies || {};
      $("cfg-ms-ck-netease").value = ck.netease || "";
      $("cfg-ms-ck-qq").value = ck.qq || "";
      $("cfg-ms-ck-kugou").value = ck.kugou || "";
      $("cfg-ms-ck-kuwo").value = ck.kuwo || "";
      $("cfg-ms-ck-migu").value = ck.migu || "";
      $("cfg-ms-transcode").checked = ms.transcode !== false;
      applySrcNames(ms.alias || {});
      // 歌单与分享
      var plc = d.config.playlist || {};
      $("cfg-pl-root").value = plc.archive_root || "";
      $("cfg-pl-mode").value = plc.archive_mode === "move" ? "move" : "copy";
      $("cfg-pl-auto").checked = plc.auto_sync !== false;
      $("cfg-pl-base").value = plc.share_base || "";
      renderRules();
    });
  }

  function saveConfig(patch, msg) {
    return api("/api/config", { method: "POST", body: { config: patch } })
      .then(function () { toast(msg || "已保存"); return loadConfig(); })
      .catch(function (e) { toast("保存失败: " + e.message); });
  }

  /* ---------- 日志 ---------- */
  function loadLogs() {
    api("/api/log?limit=300").then(function (d) {
      var box = $("logbox");
      box.innerHTML = d.logs.slice().reverse().map(function (l) {
        return '<div class="' + esc(l.level) + '">[' + esc(l.ts) + '] [' + esc(l.level) + '] ' + esc(l.msg) + '</div>';
      }).join("") || '<div class="empty">暂无日志。</div>';
      box.scrollTop = box.scrollHeight;
    });
  }

  /* ---------- 歌单 ---------- */
  function renderPlaylistPreview(pl) {
    var el = $("pl-preview");
    var rows = (pl.songs || []).map(function (s) {
      return '<div style="display:flex;gap:8px;padding:4px 0;border-bottom:1px dashed var(--border);font-size:13px">' +
        '<span style="flex:1;min-width:0;word-break:break-all">' + esc(s.title) + '</span>' +
        '<span style="color:var(--sub);white-space:nowrap">' + esc(s.artist) + '</span></div>';
    }).join("");
    el.innerHTML =
      '<div style="margin-top:12px;background:rgba(255,255,255,.05);border:1px solid var(--border);border-radius:10px;padding:12px 14px">' +
      '<div style="font-weight:600;margin-bottom:4px">' + esc(pl.playlist_name) +
      ' <span class="badge badge-gray">' + esc(pl.platform) + '</span> 共 ' + pl.total + ' 首</div>' +
      (pl.limited ? '<div style="font-size:12px;color:var(--warn)">该平台歌单仅能获取页面可见的前 ' + pl.total + ' 首（平台限制）。</div>' : "") +
      '<div style="margin-top:6px;font-size:12px;color:var(--sub)">预览前 ' + rows.split("<div").length + ' 首：</div>' +
      rows + '</div>';
  }
  function renderPlaylistResult(r) {
    var el = $("pl-result");
    var badge = r.mode === "album"
      ? '已整理为专辑目录：<span class="mono">' + esc(r.album_dir || "") + '</span>（' + (r.files || []).length + ' 个文件）'
      : '已生成播放列表：<span class="mono">' + esc(r.playlist_file || "") + '</span>';
    var missRows = (r.miss_list || []).map(function (m) {
      return '<span style="display:inline-block;margin:2px 6px 2px 0;background:rgba(255,69,58,.14);color:var(--danger);border-radius:6px;padding:2px 8px;font-size:12px">' +
        esc(m.artist + " - " + m.title) + '</span>';
    }).join("");
    el.innerHTML =
      '<div style="margin-top:12px;background:rgba(48,209,88,.1);border:1px solid rgba(48,209,88,.32);border-radius:10px;padding:12px 14px">' +
      '<div style="font-weight:600;color:var(--ok)">导入完成：' + esc(r.playlist_name) + '</div>' +
      '<div style="margin-top:6px;font-size:13px">歌单共 ' + r.total + ' 首，本地匹配 ' + r.matched + ' 首，未匹配 ' + r.missed + ' 首。</div>' +
      '<div style="margin-top:4px;font-size:13px">' + badge + '</div>' +
      (missRows ? '<div style="margin-top:8px;font-size:12px;color:var(--sub)">未在本地找到的歌曲：</div><div>' + missRows + '</div>' : "") +
      '</div>';
  }
  /* ---------- 转换 ---------- */
  function renderConvertResult(results) {
    var rows = results.map(function (r) {
      if (!r.ok) {
        return '<div style="padding:8px 0;border-bottom:1px dashed var(--border);font-size:13px;color:var(--danger)">' +
          '✕ ' + esc(r.error) + '</div>';
      }
      var tidy = r.auto_tidy ? '<span class="badge badge-ok">已刮削</span>' : "";
      return '<div style="padding:8px 0;border-bottom:1px dashed var(--border);font-size:13px">' +
        '<span style="color:var(--ok)">✓</span> ' + esc(r.out_path) +
        ' <span class="badge badge-gray">' + esc(r.fmt) + '</span>' +
        (r.has_image ? ' <span class="badge badge-ok">封面</span>' : "") + ' ' + tidy + '</div>';
    }).join("");
    $("cv-result").innerHTML =
      '<div style="margin-top:12px;background:rgba(255,255,255,.05);border:1px solid var(--border);border-radius:10px;padding:12px 14px">' +
      '<div style="font-weight:600;margin-bottom:6px">转换结果</div>' + rows + '</div>';
  }
  function renderConvertSelftest(d) {
    var rows = (d.results || []).map(function (r) {
      if (r.skip) {
        return '<div style="padding:6px 0;border-bottom:1px dashed var(--border);font-size:13px;color:var(--sub)">' +
          '– ' + esc(r.name) + ' <span class="badge badge-gray">跳过</span></div>';
      }
      return '<div style="padding:6px 0;border-bottom:1px dashed var(--border);font-size:13px">' +
        (r.pass ? '<span style="color:var(--ok)">✓</span> ' : '<span style="color:var(--danger)">✕</span> ') +
        esc(r.name) + (r.detail ? ' <span style="color:var(--danger)">' + esc(r.detail) + '</span>' : '') + '</div>';
    }).join("");
    $("cv-selftest").innerHTML =
      '<div style="margin-top:12px;background:rgba(255,255,255,.05);border:1px solid var(--border);border-radius:10px;padding:12px 14px">' +
      '<div style="font-weight:600;margin-bottom:6px">解密自检 ' +
      (d.ok ? '<span class="badge badge-ok">全部通过</span>' : '<span class="badge badge-gray">存在失败项</span>') +
      '</div>' + rows + '</div>';
  }
  /* ---------- 音源自定义 API 列表（设置页） ---------- */
  function renderMsApis(apis) {
    var box = $("cfg-ms-apis");
    box.innerHTML = "";
    if (!apis.length) {
      box.innerHTML = '<div class="hint" style="font-size:12px;color:var(--sub)">暂未添加自定义音源</div>';
      return;
    }
    apis.forEach(function (a) { addMsApiRow(a); });
  }

  function addMsApiRow(a) {
    var box = $("cfg-ms-apis");
    var blank = box.querySelector(".hint");
    if (blank) box.innerHTML = "";
    var row = document.createElement("div");
    row.className = "ms-api-row";
    row.style.cssText = "display:flex;align-items:center;gap:8px;flex-wrap:wrap;background:rgba(255,255,255,.05);border:1px solid var(--border);border-radius:10px;padding:8px 10px";
    row.innerHTML =
      '<label style="display:flex;align-items:center;gap:4px;cursor:pointer" title="勾选参与混合搜索">' +
      '<input type="checkbox" class="msapi-en" ' + (a.enabled !== false ? "checked" : "") + '></label>' +
      '<input class="input msapi-name" placeholder="名称" style="flex:0 1 110px" value="' + esc(a.name || "") + '">' +
      '<input class="input msapi-url" placeholder="API 地址，如 http://192.168.1.100:3000/api" style="flex:2 1 220px" value="' + esc(a.url || "") + '">' +
      '<input class="input msapi-token" type="password" placeholder="' + (a.token ? "已保存，留空保持不变" : "Token（可选）") + '" style="flex:1 1 140px" value="' + esc(a.token || "") + '">' +
      '<button class="btn btn-ghost btn-sm msapi-del" title="删除">✕</button>';
    row.querySelector(".msapi-del").onclick = function () {
      row.remove();
      if (!$("cfg-ms-apis").children.length) {
        $("cfg-ms-apis").innerHTML = '<div class="hint" style="font-size:12px;color:var(--sub)">暂未添加自定义音源</div>';
      }
    };
    box.appendChild(row);
  }

  /* ---------- 下载 ---------- */
  var dlResults = [];
  var dlSelected = {};
  var dlDetailSong = null;

  function fmtSize(n) {
    if (!n) return "";
    if (n >= 1048576) return (n / 1048576).toFixed(1) + " MB";
    if (n >= 1024) return (n / 1024).toFixed(0) + " KB";
    return n + " B";
  }
  function fmtDur(sec) {
    if (!sec) return "";
    sec = Math.round(sec);
    return Math.floor(sec / 60) + ":" + ("0" + (sec % 60)).slice(-2);
  }

  function dlBarShow(show) {
    var el = $("dl-bar");
    if (el) el.classList[show ? "remove" : "add"]("hidden");
  }

  function searchMusic() {
    var kw = $("dl-keyword").value.trim();
    if (!kw) { toast("请输入搜索关键词"); return; }
    $("dl-results").innerHTML = '<div class="hint">多源搜索中，请稍候…</div>';
    api("/api/music/search", { method: "POST", body: { keyword: kw, limit: 60 } })
      .then(function (r) {
        if (!r.ok) { dlBarShow(false); $("dl-results").innerHTML = '<div class="hint" style="color:var(--err)">' + esc(r.error) + '</div>'; return; }
        dlResults = r.songs || [];
        dlSelected = {};
        if (!dlResults.length) {
          dlBarShow(false);
          $("dl-results").innerHTML = '<div class="hint">未找到相关歌曲</div>';
          var info0 = $("dl-filter-info");
          if (info0) info0.textContent = "";
          return;
        }
        dlResetFilter(true);   // 新一轮结果：清掉上轮筛选条件并重建分面
        dlBarShow(true);
        renderDlResults();
      })
      .catch(function (e) { $("dl-results").innerHTML = '<div class="hint" style="color:var(--err)">搜索失败: ' + esc(e.message) + '</div>'; });
  }

  /* 封面图加载失败时替换为「无封面」占位，避免裂图 */
  window.coverFail = function (img) {
    var sp = document.createElement("span");
    sp.style.cssText = "width:40px;height:40px;border-radius:6px;background:rgba(255,255,255,.08);" +
      "display:inline-flex;align-items:center;justify-content:center;color:#7c7c84;font-size:10px;flex:none";
    sp.textContent = "无封面";
    if (img.parentNode) img.parentNode.replaceChild(sp, img);
  };

  /* 结果封面地址：自带直链优先，为空时走服务端跨源补图（302 到真实图床）。
     部分源的图床接口已失效，补图放在浏览器做，不占搜索接口耗时。 */
  function dlCoverUrl(s) {
    if (!s) return "";
    if (s.cover) return s.cover;
    var name = (s.name || "").trim();
    if (!name) return "";
    return gwPrefix() + "/api/music/cover?song=" + encodeURIComponent(JSON.stringify({
      name: name, artist: (s.artist || "").trim(), album: (s.album || "").trim()}));
  }

  var dlPage = 1;
  var DL_PAGE_SIZE = 10;

  function dlIsBad(s) { return !!(s.locked || s.unplayable); }

  /* ---------- 搜索结果筛选 / 排序（纯前端，只作用于当前结果，不重新请求） ---------- */
  var DL_Q_RANK = { "Hi-Res": 6000, "无损": 900, "320k": 320, "192k": 192, "128k": 128 };
  var DL_Q_ORDER = ["Hi-Res", "无损", "320k", "192k", "128k"];
  var dlFilter = {
    sort: "relevance", q: "", artist: "",
    platform: {}, quality: {}, format: {},
    playable: false, lyric: false, cover: false,
    sizeMin: 0, sizeMax: 0, brMin: 0, durMin: 0,
  };

  function dlTxt(v) { return String(v == null ? "" : v); }
  function dlNum(v) {
    var n = parseFloat(v);
    return (isNaN(n) || n <= 0) ? 0 : n;
  }
  function dlAny(o) { for (var k in o) { if (o[k]) return true; } return false; }
  function dlCount(o) { var n = 0; for (var k in o) { if (o[k]) n++; } return n; }
  function dlSrcName(s) { return s.source_name || s.source || "其他"; }
  function dlQuality(s) { return s.quality || "未知"; }
  function dlFormat(s) { return dlTxt(s.format).toLowerCase() || "未知"; }
  function dlBitrate(s) { return s.bitrate || DL_Q_RANK[s.quality] || 0; }
  function dlArtists(s) {
    var out = [];
    dlTxt(s.artist).split("/").forEach(function (x) {
      x = x.trim();
      if (x && out.indexOf(x) < 0) out.push(x);
    });
    return out.length ? out : ["未知"];
  }
  function dlCmpTxt(a, b) {
    if (a.localeCompare) return a.localeCompare(b, "zh-Hans-CN", { numeric: true });
    return a < b ? -1 : a > b ? 1 : 0;
  }

  function dlMatch(s) {
    if (dlFilter.q) {
      var hay = (dlTxt(s.name) + " " + dlTxt(s.artist) + " " + dlTxt(s.album)).toLowerCase();
      if (hay.indexOf(dlFilter.q) < 0) return false;
    }
    if (dlFilter.artist && dlArtists(s).indexOf(dlFilter.artist) < 0) return false;
    if (dlAny(dlFilter.platform) && !dlFilter.platform[dlSrcName(s)]) return false;
    if (dlAny(dlFilter.quality) && !dlFilter.quality[dlQuality(s)]) return false;
    if (dlAny(dlFilter.format) && !dlFilter.format[dlFormat(s)]) return false;
    if (dlFilter.playable && dlIsBad(s)) return false;
    if (dlFilter.lyric && s.has_lyric === false) return false;
    if (dlFilter.cover && !s.cover) return false;
    var mb = s.size ? s.size / 1048576 : 0;
    if (dlFilter.sizeMin && mb < dlFilter.sizeMin) return false;
    if (dlFilter.sizeMax && (!mb || mb > dlFilter.sizeMax)) return false;
    if (dlFilter.brMin && dlBitrate(s) < dlFilter.brMin) return false;
    if (dlFilter.durMin && (s.duration || 0) < dlFilter.durMin) return false;
    return true;
  }

  /* 排序字段比较器；未命中条件的项自然排到末尾 */
  var DL_CMP = {
    "bitrate-desc": function (a, b) { return dlBitrate(b) - dlBitrate(a); },
    "bitrate-asc": function (a, b) {
      var x = dlBitrate(a), y = dlBitrate(b);
      if (!x && !y) return 0;
      if (!x) return 1;
      if (!y) return -1;
      return x - y;
    },
    "size-desc": function (a, b) { return (b.size || 0) - (a.size || 0); },
    "size-asc": function (a, b) {
      var x = a.size || 0, y = b.size || 0;
      if (!x && !y) return 0;
      if (!x) return 1;
      if (!y) return -1;
      return x - y;
    },
    "duration-desc": function (a, b) { return (b.duration || 0) - (a.duration || 0); },
    "duration-asc": function (a, b) { return (a.duration || 0) - (b.duration || 0); },
    "artist": function (a, b) { return dlCmpTxt(dlTxt(a.artist), dlTxt(b.artist)); },
    "name": function (a, b) { return dlCmpTxt(dlTxt(a.name), dlTxt(b.name)); },
    "source": function (a, b) { return dlCmpTxt(dlSrcName(a), dlSrcName(b)); },
  };

  /* 展示顺序：先取用户手改的相关度序（不可播沉底）→ 筛选 → 按需附加字段排序 */
  function dlFiltered() {
    var order = dlOrderedIdx().filter(function (i) { return dlMatch(dlResults[i]); });
    var cmp = DL_CMP[dlFilter.sort];
    if (cmp) {
      order.sort(function (a, b) {
        var d = (dlIsBad(dlResults[a]) ? 1 : 0) - (dlIsBad(dlResults[b]) ? 1 : 0);
        return d || cmp(dlResults[a], dlResults[b]) || (a - b);
      });
    }
    return order;
  }

  function dlKeys(map, order) {
    var keys = Object.keys(map);
    keys.sort(function (a, b) {
      if (order) {
        var ia = order.indexOf(a), ib = order.indexOf(b);
        if (ia >= 0 || ib >= 0) {
          if (ia < 0) return 1;
          if (ib < 0) return -1;
          return ia - ib;
        }
      }
      return (map[b] - map[a]) || dlCmpTxt(a, b);
    });
    return keys;
  }

  function dlChips(elId, keys, map, store) {
    var box = $(elId);
    if (!box) return;
    if (!keys.length) { box.innerHTML = '<span class="dlbar-lb">无</span>'; return; }
    var html = "";
    keys.forEach(function (k) {
      var label = /^[a-z0-9]+$/.test(k) ? k.toUpperCase() : k;
      html += '<button type="button" class="chip' + (store[k] ? " on" : "") + '" data-v="' + esc(k) + '"' +
        ' aria-pressed="' + (store[k] ? "true" : "false") + '" title="' + esc(k) + '">'
        + esc(label) + ' <b>' + map[k] + '</b></button>';
    });
    box.innerHTML = html;
  }

  function dlFacets() {
    var f = { platform: {}, quality: {}, format: {}, artist: {} };
    dlResults.forEach(function (s) {
      var p = dlSrcName(s), q = dlQuality(s), fm = dlFormat(s);
      f.platform[p] = (f.platform[p] || 0) + 1;
      f.quality[q] = (f.quality[q] || 0) + 1;
      f.format[fm] = (f.format[fm] || 0) + 1;
      dlArtists(s).forEach(function (a) { f.artist[a] = (f.artist[a] || 0) + 1; });
    });
    return f;
  }

  /* 新结果到达（或重置）后重建分面选项与计数 */
  function buildDlFacets() {
    var f = dlFacets();
    if (dlFilter.artist && !f.artist[dlFilter.artist]) dlFilter.artist = "";
    dlChips("dl-f-platform", dlKeys(f.platform), f.platform, dlFilter.platform);
    dlChips("dl-f-quality", dlKeys(f.quality, DL_Q_ORDER), f.quality, dlFilter.quality);
    dlChips("dl-f-format", dlKeys(f.format), f.format, dlFilter.format);
    var sel = $("dl-artist");
    if (sel) {
      var names = Object.keys(f.artist).sort(dlCmpTxt);
      var html = '<option value="">全部歌手（' + names.length + '）</option>';
      names.forEach(function (n) {
        html += '<option value="' + esc(n) + '"' + (n === dlFilter.artist ? " selected" : "") + '>' +
          esc(n) + '（' + f.artist[n] + '）</option>';
      });
      sel.innerHTML = html;
      sel.value = dlFilter.artist;
    }
  }

  function dlActiveCount() {
    var n = 0;
    if (dlFilter.q) n++;
    if (dlFilter.artist) n++;
    n += dlCount(dlFilter.platform) + dlCount(dlFilter.quality) + dlCount(dlFilter.format);
    if (dlFilter.playable) n++;
    if (dlFilter.lyric) n++;
    if (dlFilter.cover) n++;
    if (dlFilter.sizeMin || dlFilter.sizeMax) n++;
    if (dlFilter.brMin) n++;
    if (dlFilter.durMin) n++;
    return n;
  }

  function dlApply() { dlPage = 1; renderDlResults(); }

  /* silent=true 时只清条件不重绘（搜索回调自己会绘一次） */
  window.dlResetFilter = function (silent) {
    // 就地清空：chip 点击闭包持有的是这几个对象引用，重新赋值会导致筛选失效
    var clear = function (o) { for (var k in o) { delete o[k]; } };
    clear(dlFilter.platform); clear(dlFilter.quality); clear(dlFilter.format);
    dlFilter.q = ""; dlFilter.artist = "";
    dlFilter.playable = dlFilter.lyric = dlFilter.cover = false;
    dlFilter.sizeMin = dlFilter.sizeMax = dlFilter.brMin = dlFilter.durMin = 0;
    dlFilter.sort = "relevance";
    var set = function (id, v) { var el = $(id); if (el) el.value = v; };
    set("dl-q", ""); set("dl-sort", "relevance");
    ["dl-size-min", "dl-size-max", "dl-br-min", "dl-dur-min"].forEach(function (id) { set(id, ""); });
    ["dl-f-playable", "dl-f-lyric", "dl-f-cover"].forEach(function (id) {
      var el = $(id); if (el) el.checked = false;
    });
    buildDlFacets();
    if (!silent) dlApply(); else dlPage = 1;
  };

  function dlBindFilterBar() {
    function bindChips(elId, store) {
      var box = $(elId);
      if (!box || box._dlBound) return;
      box._dlBound = true;
      box.addEventListener("click", function (e) {
        var c = e.target && e.target.closest ? e.target.closest(".chip") : null;
        if (!c || !box.contains(c)) return;
        var v = c.getAttribute("data-v");
        if (v === null) return;
        if (store[v]) delete store[v]; else store[v] = true;
        c.classList.toggle("on", !!store[v]);
        c.setAttribute("aria-pressed", store[v] ? "true" : "false");
        dlApply();
      });
    }
    bindChips("dl-f-platform", dlFilter.platform);
    bindChips("dl-f-quality", dlFilter.quality);
    bindChips("dl-f-format", dlFilter.format);
    $("dl-sort").onchange = function () { dlFilter.sort = this.value; renderDlResults(); };
    $("dl-artist").onchange = function () { dlFilter.artist = this.value; dlApply(); };
    var t = null;
    $("dl-q").oninput = function () {
      var self = this;
      if (t) clearTimeout(t);
      t = setTimeout(function () {
        dlFilter.q = dlTxt(self.value).trim().toLowerCase();
        dlApply();
      }, 160);
    };
    [["dl-f-playable", "playable"], ["dl-f-lyric", "lyric"], ["dl-f-cover", "cover"]]
      .forEach(function (pair) {
        $(pair[0]).onchange = function () {
          dlFilter[pair[1]] = this.checked;
          dlApply();
        };
      });
    [["dl-size-min", "sizeMin"], ["dl-size-max", "sizeMax"], ["dl-br-min", "brMin"], ["dl-dur-min", "durMin"]]
      .forEach(function (pair) {
        $(pair[0]).oninput = function () {
          dlFilter[pair[1]] = dlNum(this.value);
          dlApply();
        };
      });
    $("dl-reset").onclick = function () { dlResetFilter(); };
  }

  /* 展示顺序：可播在前，不可播/锁定沉底（保持服务端相关度序） */
  function dlOrderedIdx() {
    var order = [];
    dlResults.forEach(function (s, i) { order.push(i); });
    order.sort(function (a, b) {
      var d = (dlIsBad(dlResults[a]) ? 1 : 0) - (dlIsBad(dlResults[b]) ? 1 : 0);
      return d || (a - b);
    });
    return order;
  }

  window.dlGoPage = function (p) { dlPage = p; renderDlResults(); };

  function renderDlResults() {
    var order = dlFiltered();
    var info = $("dl-filter-info");
    if (info) {
      var act = dlActiveCount();
      info.textContent = act
        ? "筛出 " + order.length + " / " + dlResults.length + " 条·" + act + " 项条件"
        : "共 " + dlResults.length + " 条";
    }
    if (!order.length) {
      $("dl-results").innerHTML = '<div class="dl-empty-f">当前筛选条件下无匹配结果 ' +
        '<button type="button" class="dl-plain" onclick="dlResetFilter()">重置筛选</button></div>';
      updateDlCount();
      return;
    }
    var pages = Math.max(1, Math.ceil(order.length / DL_PAGE_SIZE));
    if (dlPage < 1) dlPage = 1;
    if (dlPage > pages) dlPage = pages;
    var slice = order.slice((dlPage - 1) * DL_PAGE_SIZE, dlPage * DL_PAGE_SIZE);
    var html = '<div style="background:rgba(255,255,255,.05);border:1px solid var(--border);border-radius:10px;padding:8px 12px;max-height:min(68vh,760px);overflow-y:auto">';
    slice.forEach(function (i) {
      var s = dlResults[i];
      var chk = dlSelected[i] ? "checked" : "";
      var locked = dlIsBad(s);
      var srcBadge = s.source_name ? '<span style="background:rgba(10,132,255,.16);color:#6cb2ff;padding:1px 6px;border-radius:4px;font-size:11px;margin-left:6px;white-space:nowrap">' + esc(s.source_name) + '</span>' : '';
      var vipBadge = locked ? '<span style="background:rgba(255,69,58,.16);color:#ff6961;padding:1px 6px;border-radius:4px;font-size:11px;margin-left:6px;white-space:nowrap">' + (s.locked ? "VIP/需付费" : "无法试听") + '</span>' : '';
      var cover = s.cover
        ? '<img src="' + esc(s.cover) + '" loading="lazy" referrerpolicy="no-referrer" style="width:40px;height:40px;border-radius:6px;object-fit:cover;flex:none" onerror="coverFail(this)">'
        : (dlCoverUrl(s)
          ? '<img src="' + esc(dlCoverUrl(s)) + '" loading="lazy" referrerpolicy="no-referrer" style="width:40px;height:40px;border-radius:6px;object-fit:cover;flex:none" onerror="coverFail(this)">'
          : '<span style="width:40px;height:40px;border-radius:6px;background:rgba(255,255,255,.08);display:inline-flex;align-items:center;justify-content:center;color:#7c7c84;font-size:10px;flex:none">无封面</span>');
      var meta = [];
      if (s.quality) meta.push('<span style="background:rgba(255,214,10,.14);color:#ffd60a;padding:1px 6px;border-radius:4px;font-size:11px">' + esc(s.quality) + '</span>');
      if (s.format) meta.push('<span style="background:rgba(255,255,255,.07);color:#c7c7cc;padding:1px 6px;border-radius:4px;font-size:11px;text-transform:uppercase">' + esc(s.format) + '</span>');
      if (s.bitrate) meta.push('<span style="background:rgba(48,209,88,.13);color:#6ee7a8;padding:1px 6px;border-radius:4px;font-size:11px">' + esc(s.bitrate) + 'kbps</span>');
      if (s.size) meta.push('<span style="color:var(--sub);font-size:11px">' + fmtSize(s.size) + '</span>');
      if (s.duration) meta.push('<span style="color:var(--sub);font-size:11px">' + fmtDur(s.duration) + '</span>');
      var playBtn = locked
        ? '<button class="btn btn-ghost btn-sm" disabled title="' + (s.locked ? "VIP/需付费，无法试听（可在设置中配置该平台 Cookie）" : "试听链接获取失败，无法播放") + '" style="color:#5a5a62;border-color:#3a3a40;cursor:not-allowed">▶</button>'
        : '<button class="btn btn-ghost btn-sm" onclick="dlPreview(' + i + ')" title="试听">▶</button>';
      var fkey = "pending://" + (s.source || "") + "/" + (s.id || "");
      var noLyric = s.has_lyric === false;
      var lyricBtn = noLyric
        ? '<button class="btn btn-ghost btn-sm" disabled title="该音源无歌词" style="color:#5a5a62;border-color:#3a3a40;cursor:not-allowed">词</button>'
        : '<button class="btn btn-ghost btn-sm" onclick="dlShowLyric(' + i + ')" title="歌词">词</button>';
      html += '<div style="display:flex;align-items:center;gap:10px;padding:8px 0;border-bottom:1px dashed var(--border);' + (locked ? 'opacity:.72' : '') + '">' +
        '<input type="checkbox" ' + chk + ' onchange="toggleDlSel(' + i + ',this.checked)" style="flex:none">' +
        cover +
        '<div style="flex:1;min-width:0">' +
        '<div style="white-space:nowrap;overflow:hidden;text-overflow:ellipsis"><b>' + esc(s.name) + '</b>' + srcBadge + vipBadge +
        (s.artist ? ' <span style="color:var(--sub)">- ' + esc(s.artist) + '</span>' : '') + '</div>' +
        '<div style="display:flex;align-items:center;gap:6px;flex-wrap:wrap;margin-top:3px">' +
        (s.album ? '<span style="color:var(--sub);font-size:11px;max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' + esc(s.album) + '">' + esc(s.album) + '</span>' : '') +
        meta.join("") + '</div></div>' +
        '<div style="display:flex;gap:4px;flex:none">' +
        playBtn + lyricBtn +
        '<button class="btn btn-ghost btn-sm' + (isFavKey(fkey) ? " btn-primary" : "") + '" onclick="dlFav(' + i + ')" title="' +
          (isFavKey(fkey) ? "取消喜欢（我的喜欢）" : "标记到我的喜欢") + '，未下载的歌也会先记入歌单">♥</button>' +
        '<button class="btn btn-ghost btn-sm" onclick="dlAddPl(' + i + ')" title="添加到指定歌单">＋</button>' +
        '<button class="btn btn-ghost btn-sm" onclick="dlDetail(' + i + ')" title="详情">ℹ</button>' +
        '</div></div>';
    });
    html += '</div>';
    if (order.length > DL_PAGE_SIZE) {
      html += '<div style="display:flex;align-items:center;justify-content:center;gap:12px;margin-top:8px">' +
        '<button class="btn btn-ghost btn-sm" onclick="dlGoPage(' + (dlPage - 1) + ')"' + (dlPage <= 1 ? " disabled" : "") + '>上一页</button>' +
        '<span style="color:var(--sub);font-size:12px">第 ' + dlPage + ' / ' + pages + ' 页 · 共 ' + order.length + ' 条</span>' +
        '<button class="btn btn-ghost btn-sm" onclick="dlGoPage(' + (dlPage + 1) + ')"' + (dlPage >= pages ? " disabled" : "") + '>下一页</button>' +
        '</div>';
    }
    $("dl-results").innerHTML = html;
    updateDlCount();
  }

  window.toggleDlSel = function (idx, checked) {
    if (checked) dlSelected[idx] = true; else delete dlSelected[idx];
    updateDlCount();
  };

  /* 搜索结果里的红心 / 加歌单：未下载的歌先以 pending:// 记入歌单，下载后自动关联本地文件 */
  window.dlFav = function (idx) {
    var s = dlResults[idx];
    if (!s) return;
    favToggle({ song: s }, trackFavKey(dlTrack(s)), function () { renderDlResults(); });
  };
  window.dlAddPl = function (idx) {
    var s = dlResults[idx];
    if (s) openAddPl(dlTrack(s));
  };

  function updateDlCount() {
    var n = Object.keys(dlSelected).length;
    var el = $("dl-sel-count");
    if (el) el.textContent = n ? "已选 " + n + " 首" : "";
    var act = $("dl-actions");
    if (act) act.style.display = n ? "" : "none";
  }

  /* 试听：以当前筛选/排序后的结果为歌单，统一播放器顺序播放 */
  window.dlPreview = function (idx) {
    var s = dlResults[idx];
    if (!s) return;
    var queue = [], start = 0;
    dlFiltered().forEach(function (i) {
      var x = dlResults[i];
      if (x.locked || x.unplayable) return;
      if (i === idx) start = queue.length;
      queue.push(dlTrack(x));
    });
    if (!queue.length) { toast("试听失败：可能为 VIP 歌曲或源无试听地址"); return; }
    pPlayQueue(queue, start);
  };

  /* 歌词弹窗 */
  window.dlShowLyric = function (idx) {
    var s = dlResults[idx];
    if (!s) return;
    openDlDetail(s, true);
  };

  /* 详情弹窗 */
  window.dlDetail = function (idx) {
    var s = dlResults[idx];
    if (!s) return;
    openDlDetail(s, false);
  };

  function openDlDetail(s, autoLyric) {
    dlDetailSong = s;
    $("dl-detail-modal").classList.add("show");
    $("ddm-title").textContent = s.name || "—";
    $("ddm-artist").textContent = (s.artist || "未知歌手") + (s.source_name ? "  ·  来源：" + s.source_name : "");
    var img = $("ddm-cover");
    var cov = dlCoverUrl(s);
    if (cov) { img.src = cov; img.style.display = ""; }
    else { img.removeAttribute("src"); img.style.display = "none"; }
    var rows = [];
    rows.push("<b>专辑</b>：" + esc(s.album || "未知"));
    rows.push("<b>时长</b>：" + (s.duration ? fmtDur(s.duration) : "未知"));
    rows.push("<b>音质</b>：" + (s.quality ? esc(s.quality) : "未知"));
    rows.push("<b>格式</b>：" + (s.format ? esc(s.format).toUpperCase() : "未知"));
    rows.push("<b>码率</b>：" + (s.bitrate ? esc(s.bitrate) + " kbps（" + (s.quality ? esc(s.quality) : "未标音质") + "）" : "未知"));
    rows.push("<b>大小</b>：" + (s.size ? fmtSize(s.size) : "未知"));
    rows.push("<b>封面</b>：" + (cov ? '<span style="color:var(--ok)">有' + (s.cover ? "" : "（跨源补图）") + '</span>' : '<span style="color:var(--sub)">无</span>'));
    rows.push("<b>可播放</b>：" + (s.locked ? '<span style="color:var(--danger)">否（VIP/需付费，配置 Cookie 后可解锁）</span>' : '<span style="color:var(--ok)">是</span>'));
    rows.push("<b>歌词</b>：" + (s.has_lyric === false ? '<span style="color:var(--sub)">无</span>' : "点击查看"));
    $("ddm-info").innerHTML = rows.join("<div>") + "</div>";
    $("ddm-lyric").style.display = "none";
    $("ddm-lyric").innerHTML = "";
    if (autoLyric) loadDlLyric();
  }

  function loadDlLyric() {
    if (!dlDetailSong) return;
    var box = $("ddm-lyric");
    box.style.display = "";
    box.innerHTML = '<div class="empty">歌词加载中…</div>';
    api("/api/music/lyric?song=" + encodeURIComponent(JSON.stringify(dlDetailSong)))
      .then(function (d) {
        var text = (d.lyric || "").trim();
        if (!text) { box.innerHTML = '<div class="empty">该音源暂无歌词，下载后可用「AI 补全」生成。</div>'; return; }
        box.innerHTML = text.split(/\r?\n/).filter(Boolean).map(function (l) {
          return '<div class="ln">' + esc(l.replace(/\[[0-9:.]+\]/g, "").trim() || l) + '</div>';
        }).join("");
      })
      .catch(function (e) { box.innerHTML = '<div class="empty">歌词加载失败: ' + esc(e.message) + '</div>'; });
  }

  function closeDlDetail() {
    dlDetailSong = null;
    $("dl-detail-modal").classList.remove("show");
  }

  function downloadSelected() {
    var songs = [];
    Object.keys(dlSelected).forEach(function (idx) {
      songs.push(dlResults[parseInt(idx)]);
    });
    if (!songs.length) { toast("请先选择要下载的歌曲"); return; }
    api("/api/music/download", { method: "POST", body: { songs: songs } })
      .then(function (r) {
        if (r.error) { toast(r.error); return; }
        toast("下载任务已启动");
        dlcStart("歌曲下载（" + songs.length + " 首）", "download");
        dlSelected = {};
        renderDlResults();
        pollTask();
      })
      .catch(function (e) { toast(e.message); });
  }

  function downloadPlaylist() {
    var link = $("dl-pl-link").value.trim();
    if (!link) { toast("请输入歌单链接"); return; }
    if (!confirm("将自动解析歌单并逐首搜索下载，下载后自动整理刮削。确定开始？")) return;
    api("/api/music/download-playlist", { method: "POST", body: { link: link } })
      .then(function (r) {
        if (r.error) { toast(r.error); return; }
        toast("歌单下载任务已启动");
        dlcStart("歌单下载", "dl_playlist");
        pollTask();
      })
      .catch(function (e) { toast(e.message); });
  }

  /* ---------- 下载中心（右下角悬浮） ---------- */
  var dlcTasks = [];
  var dlcSeq = 1;
  var dlcOpen = false;

  function dlcLoad() {
    try {
      var raw = localStorage.getItem("dlc_tasks_v1");
      if (raw) dlcTasks = JSON.parse(raw) || [];
      dlcTasks.forEach(function (t) { if (t.id >= dlcSeq) dlcSeq = t.id + 1; });
    } catch (e) { dlcTasks = []; }
  }
  function dlcSave() {
    try { localStorage.setItem("dlc_tasks_v1", JSON.stringify(dlcTasks.slice(0, 30))); } catch (e) {}
  }
  function dlcActive() {
    return dlcTasks.filter(function (t) { return t.status === "running" || t.status === "paused"; });
  }
  function dlcStamp() {
    /* 与后端 task_log.ts 同格式（YYYY-MM-DD HH:MM:SS），方便按时刻过滤日志尾部 */
    var d = new Date(), p = function (n) { return (n < 10 ? "0" : "") + n; };
    return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) +
      " " + p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
  }
  function dlcStart(name, type) {
    var tk = { id: dlcSeq++, name: name, type: type, status: "running", stage: "",
      total: 0, done: 0, ok: 0, fail: 0, skip: 0, bytesDone: 0, bytesTotal: 0,
      speed: 0, current: "", startedAt: new Date().toLocaleTimeString(),
      seq: 0, tStart: "", t0: dlcStamp(), logs: [], logsTried: 0, note: "" };
    dlcTasks.unshift(tk);
    dlcSave(); renderDlCenter();
    return tk;
  }
  /* 后端任务 ↔ 面板条目对齐：先按「任务序号 + 启动时刻」精确匹配，再退回本地刚点、
     还没绑上序号的同类型条目。旧实现直接拿 act[0]，两单并存时后面那单永远停在「下载中」。 */
  function dlcSame(tk, t) {
    if (!tk.seq || !t.seq) return true;   // 旧数据本地刚点：认最近一单
    return tk.seq === t.seq && (!t.started_at || !tk.tStart || tk.tStart === t.started_at);
  }
  function dlcMatch(act, t) {
    var i;
    for (i = 0; i < act.length; i++) { if (t.seq && dlcSame(act[i], t) && act[i].type === t.type) return act[i]; }
    for (i = 0; i < act.length; i++) {
      if (act[i].type === t.type && !act[i].seq) return act[i];
    }
    return null;
  }
  /* 后端已经换到别的任务（或重启过）：这一单再等也等不到终态，
     按日志尾部最后一条动态结算，不能让它永远显示「下载中」。 */
  function dlcSettle(tk) {
    var last = (tk.logs && tk.logs.length) ? tk.logs[0] : null;
    var msg = last ? (last.msg || "") : "";
    tk.stage = "";
    if (/下载完成/.test(msg)) tk.status = /待处理 [1-9]/.test(msg) ? "stopped" : "done";
    /* 一行 error 级日志（如“下载目录未配置”）就是异常，不能只认“异常”两个字 */
    else if (/异常/.test(msg) || (last && last.level === "error")) tk.status = "error";
    else tk.status = "stopped";
    if (last) tk.note = "";
    else tk.note = "未取得本单最终进度（服务状态已刷新），结果以「日志」页为准。";
  }
  /* 有活动任务时把轮询提到 1.2s，否则进度的更新周期就是全局的 8s，
     面板看着像“停在旧状态不动”。 */
  var dlcFastTimer = 0;
  var dlcFastFail = 0;
  function dlcFastPoll(on) {
    if (on && !dlcFastTimer) {
      dlcFastFail = 0;
      dlcFastTimer = setInterval(function () {
        if (!dlcActive().length) { clearInterval(dlcFastTimer); dlcFastTimer = 0; return; }
        api("/api/status").then(function (d) {
          dlcFastFail = 0; state.status = d; renderTask();
        }).catch(function () {
          /* 服务不可达时不能每 1.2s 硬碰：退回后台轮询，并告诉用户面板已停更 */
          if (++dlcFastFail >= 5) {
            clearInterval(dlcFastTimer); dlcFastTimer = 0;
            dlcActive().forEach(function (x) { x.note = "与服务端的连接已中断，进度暂停更新（恢复后自动继续）。"; });
            dlcSave(); renderDlCenter();
          }
        });
      }, 1200);
    } else if (!on && dlcFastTimer) {
      clearInterval(dlcFastTimer); dlcFastTimer = 0;
    }
  }
  /* 把日志尾部的真实动态搬进面板：面板与「日志」页说的是同一句话 */
  var DLC_LOG_RE = /(已下载|跳过已存在|下载失败|未识别出歌手|断点保留|取链失败|下载完成|下载已停止|歌单下载|下载异常|转码|未配置)/;
  /* 面板要挂日志的条目：优先还在跑的；没有活动任务时，给“刚被结算、
     但一条日志都没拿到”的条目补拉一次，好把真实失败原因（如目录未配置）显示出来。 */
  function dlcPullTarget() {
    var act = dlcActive(), i;
    for (i = 0; i < act.length; i++) { if (act[i].status === "running") return act[i]; }
    if (act.length) return act[0];
    for (i = 0; i < dlcTasks.length; i++) {
      var x = dlcTasks[i];
      if (x.t0 && !x.logsTried && !(x.logs && x.logs.length)
        && (x.status === "error" || x.status === "stopped")) return x;
    }
    return null;
  }
  var dlcLogBusy = false;
  function dlcPullLogs() {
    var run = dlcPullTarget();
    if (!run || dlcLogBusy) return;
    var live = (run.status === "running" || run.status === "paused");
    if (!live) run.logsTried = 1;     // 已终态的只补拉一次，不能每轮都拉
    dlcLogBusy = true;
    api("/api/log?limit=60").then(function (d) {
      dlcLogBusy = false;
      var rows = (d.logs || []).filter(function (l) {
        return (l.level === "error" || DLC_LOG_RE.test(l.msg || ""))
          && (!run.t0 || !l.ts || l.ts >= run.t0);
      });
      /* /api/log 返回的就是“新→旧”，所以取前 3 条；旧写法 slice(-3) 拿到的是最早 3 条，
         面板上滚动的一直是刚开头那几句，看着就“没跟着日志动” */
      run.logs = rows.slice(0, 3);
      if (!live && run.logs.length) { dlcSettle(run); dlcSave(); }   // 拿到原因后重新定性与提示文案
      renderDlCenter();
    }).catch(function () { dlcLogBusy = false; });
  }
  /* 用后端 task 状态同步下载中心活动任务：状态对齐、陈旧条目结算、面板重绘三件都要做。
     旧版只改内存不调 renderDlCenter()，面板一直停在任务刚创建那一刻，看起来就是
     「日志都写完成了几首歌，下载中心还在 0% 下载中」。 */
  function dlcSync(t) {
    var isDl = t.type === "download" || t.type === "dl_playlist";
    var act = dlcActive();
    var changed = false, i, tk;
    if (t.running && isDl) {
      /* 后端同一时刻只跑一个任务：序号比当前这单更老、却还挂在 running 的同类型条目，
         不可能再被报状态（典型场景：另一个页面开了新下载），先按日志结算掉。 */
      for (i = 0; i < act.length; i++) {
        if (act[i].type === t.type && act[i].seq && t.seq && act[i].seq < t.seq) {
          dlcSettle(act[i]); changed = true;
        }
      }
      act = dlcActive();
      tk = dlcMatch(act, t);
      if (!tk) tk = dlcStart(t.type === "download" ? "歌曲下载" : "歌单下载", t.type);
      if (!tk.seq) { tk.seq = t.seq || 0; tk.tStart = t.started_at || ""; }
      tk.status = (t.state === "paused") ? "paused" : "running";
      tk.stage = t.stage || "";
      tk.total = t.total || 0; tk.done = t.done || 0;
      tk.ok = t.ok || 0; tk.fail = t.fail || 0; tk.skip = t.skip || 0;
      tk.current = t.current || "";
      tk.note = "";
      var now = Date.now();
      var bd = t.bytes_done || 0;
      if (tk._lastTs && now > tk._lastTs) {
        var dt = (now - tk._lastTs) / 1000;
        if (dt >= 0.5) {
          var db = bd - (tk._lastBytes || 0);
          if (db >= 0) tk.speed = Math.round(db / dt);
          tk._lastTs = now; tk._lastBytes = bd;
        }
      } else { tk._lastTs = now; tk._lastBytes = bd; }
      tk.bytesDone = bd; tk.bytesTotal = t.bytes_total || 0;
      changed = true;
    } else {
      for (i = 0; i < act.length; i++) {
        tk = act[i];
        if (t.running) {                // 后端在跑任务，但不是下载：这一单已经不会被报状态了
          dlcSettle(tk); changed = true; continue;
        }
        if (isDl && dlcSame(tk, t)) {
          // 后端已写终态（done / stopped / error）时直接采信；
          // 旧版只回 idle，才退化用 done<total 猜“被停了”。
          var s = t.state;
          if (s === "done" || s === "stopped" || s === "error") tk.status = s;
          else if (t.last_error) tk.status = "error";
          else tk.status = (t.total && (t.done || 0) < t.total) ? "stopped" : "done";
          tk.stage = ""; tk.note = "";
          tk.done = t.done || tk.done; tk.ok = t.ok || 0; tk.fail = t.fail || 0; tk.skip = t.skip || 0;
          tk.total = t.total || tk.total;
          tk.bytesDone = t.bytes_done || tk.bytesDone;
          tk.bytesTotal = t.bytes_total || tk.bytesTotal;
        } else {
          dlcSettle(tk);               // 序号已往前走（或服务重启）：按日志结算
        }
        changed = true;
      }
    }
    if (changed) { dlcSave(); renderDlCenter(); }
    dlcPullLogs();
    dlcFastPoll(!!dlcActive().length);
  }
  function fmtEta(sec) {
    if (sec <= 0) return "";
    if (sec >= 3600) return Math.floor(sec / 3600) + "h" + Math.floor(sec % 3600 / 60) + "m";
    if (sec >= 60) return Math.floor(sec / 60) + "分" + (sec % 60) + "秒";
    return sec + "秒";
  }
  /* 面板里直接挂本单最近几条真实日志，与「日志」页同源：
     以前只有日志页在动，下载中心静止，两边对不上就像“没同步”。 */
  function dlcLogsHtml(tk) {
    var h = "";
    var rows = (tk.logs || []).slice(0, 3);
    if (rows.length) {
      h += '<div class="dlc-logs">' + rows.map(function (l) {
        var warn = l.level === "warn" || l.level === "error";
        return '<div' + (warn ? ' class="w"' : "") + ">" + esc(String(l.ts || "").slice(11)) +
          " " + esc(l.msg || "") + "</div>";
      }).join("") + "</div>";
    }
    if (tk.note) h += '<div class="dlc-note">' + esc(tk.note) + "</div>";
    return h;
  }

  function renderDlCenter() {
    var act = dlcActive();
    var badge = $("dlc-badge");
    if (!badge) return;
    badge.classList.toggle("hidden", !act.length);
    badge.textContent = act.length;
    var list = $("dlc-list");
    if (!dlcTasks.length) { list.innerHTML = '<div class="dlc-empty">暂无下载任务</div>'; return; }
    var html = "";
    dlcTasks.forEach(function (tk) {
      // 整体进度：已完成首数 + 当前这首的字节占比。旧实现只看单文件字节，
      // 多首任务会出现 100%→0% 往返跳，看着像“卡住/重头开始”。
      var frac = (tk.bytesTotal > 0 && tk.bytesDone > 0)
        ? Math.min(1, tk.bytesDone / tk.bytesTotal) : 0;
      var pct;
      if (tk.total > 0) {
        var eff = tk.done + ((tk.status === "running" || tk.status === "paused") ? frac : 0);
        pct = Math.min(100, Math.round(eff / tk.total * 100));
      } else {
        pct = Math.min(100, Math.round(frac * 100));
      }
      if (tk.status === "done") pct = 100;
      var stMap = { running: ["下载中", "run"], paused: ["已暂停", "pause"],
        done: ["已完成", "done"], error: ["异常", "err"], stopped: ["已停止", "stop"] };
      var st = stMap[tk.status] || ["未知", "stop"];
      if (tk.status === "running" && tk.stage) st = [tk.stage, "run"];
      var line1, line2 = "";
      if (tk.status === "running" || tk.status === "paused") {
        var parts = [];
        if (tk.total) parts.push("第 " + Math.min(tk.total, tk.done + 1) + "/" + tk.total + " 首");
        parts.push(tk.bytesTotal ? (fmtSize(tk.bytesDone) + " / " + fmtSize(tk.bytesTotal)) : "准备中");
        var sum = (tk.ok || 0) + (tk.skip || 0) + (tk.fail || 0);
        if (sum) parts.push("成功 " + (tk.ok || 0) + " · 跳过 " + (tk.skip || 0) + " · 失败 " + tk.fail);
        line1 = parts.join(" · ");
        if (tk.speed > 0 && (tk.bytesTotal > tk.bytesDone)) {
          line2 = fmtSize(tk.speed) + "/s";
          var eta = fmtEta(Math.max(1, Math.round((tk.bytesTotal - tk.bytesDone) / tk.speed)));
          if (eta) line2 += " · 本曲剩余约 " + eta;
        }
      } else {
        line1 = "共 " + tk.total + " 首 · 成功 " + tk.ok + " · 跳过已存在 " + (tk.skip || 0) + " · 失败 " + tk.fail;
        if (tk.status === "stopped") line1 += "（已处理 " + tk.done + " 首，断点已保留，重新下载自动续传）";
      }
      html += '<div class="dlc-item">' +
        '<div class="dlc-name">' + esc(tk.name) + ' <span class="dlc-state ' + st[1] + '">' + st[0] + '</span></div>' +
        (tk.current && (tk.status === "running" || tk.status === "paused")
          ? '<div class="dlc-meta" style="white-space:nowrap;overflow:hidden;text-overflow:ellipsis;display:block">当前：' + esc(tk.current) + '</div>' : '') +
        '<div class="dlc-meta"><span>' + line1 + '</span>' + (line2 ? '<span>' + esc(line2) + '</span>' : '') + '</div>' +
        '<div class="dlc-bar"><i style="width:' + pct + '%"></i></div>' +
        dlcLogsHtml(tk) +
        '<div class="dlc-ctrl">' +
        (tk.status === "running" ? '<button data-act="pause" data-id="' + tk.id + '">暂停</button>' : '') +
        (tk.status === "paused" ? '<button data-act="resume" data-id="' + tk.id + '">继续</button>' : '') +
        ((tk.status === "running" || tk.status === "paused") ? '<button data-act="stop" data-id="' + tk.id + '">停止</button>' : '') +
        ((tk.status !== "running" && tk.status !== "paused") ? '<button data-act="del" data-id="' + tk.id + '">删除</button>' : '') +
        '</div></div>';
    });
    list.innerHTML = html;
    Array.prototype.forEach.call(list.querySelectorAll(".dlc-ctrl button"), function (b) {
      b.onclick = function () {
        var a = b.getAttribute("data-act");
        if (a === "del") {
          var id = parseInt(b.getAttribute("data-id"));
          dlcTasks = dlcTasks.filter(function (t) { return t.id !== id; });
          dlcSave(); renderDlCenter(); return;
        }
        if (a === "stop" && !confirm("停止后当前下载断点保留，重新下载同一首自动续传。确定停止？")) return;
        api("/api/task", { method: "POST", body: { action: a } })
          .then(function () {
            toast(a === "pause" ? "已暂停（当前歌曲下载完后生效）" : a === "resume" ? "已继续" : "已停止，断点保留");
            pollTask();
          })
          .catch(function (e) { toast(e.message); });
      };
    });
  }

  /* ---------- 扫描体检（漏扫对账） ---------- */
  var auditData = null;

  /* 与后端 WALK_KIND_LABEL 一致：逐类说清“为什么这一批没扫到” */
  var AUDIT_KIND = {
    denied: "读不到的目录", missing: "不存在/不可进入的根目录", depth: "超深度被截断的目录",
    link: "软链重复或成环被剪枝", excluded: "命中排除规则的目录", file: "音频扩展名却访问不到"
  };

  function openAudit() {
    $("audit-modal").classList.add("show");
    $("aud-run").title = scope.length
      ? "只重跑当前选中的扫描范围：" + scopeText() : "重新枚举全部授权目录";
    if (auditData) renderAudit(auditData); else runAudit();
  }

  function runAudit() {
    var btn = $("aud-run");
    btn.disabled = true;
    var url = "/api/scan/audit";
    if (scope.length) {
      url += "?tokens=" + scope.map(function (x) { return x.token; }).join(",");
    }
    $("aud-sum").textContent = "枚举中…";
    $("aud-body").innerHTML = '<div class="hint" style="font-size:12px">正在枚举目录…（只走目录树、不读文件内容，几千个目录通常几秒）</div>';
    api(url)
      .then(function (d) { auditData = d.audit; renderAudit(d.audit); })
      .catch(function (e) {
        $("aud-sum").textContent = "体检失败";
        $("aud-body").innerHTML = '<div class="hint" style="color:var(--err);font-size:12px">' + esc(e.message) + '</div>';
      })
      .then(function () { btn.disabled = false; });
  }

  function renderAudit(a) {
    a = a || {};
    var roots = a.roots || [];
    var iss = a.issues || [];
    var by = {};
    iss.forEach(function (x) { var k = x.kind || "other"; (by[k] = by[k] || []).push(x); });
    var diff = (a.audio_total || 0) - (a.in_db || 0);
    // 范围跟着后端的 scoped 说，不拿前端当前值猜：缓存的那一份可能是上一个范围跑出来的
    $("aud-sum").textContent = "磁盘音频 " + (a.audio_total || 0)
      + " · " + (a.scoped ? "范围内入库 " + (a.in_db || 0) + "（全库 " + (a.db_total || 0) + " 条未计入）"
                          : "已入库 " + (a.in_db || 0))
      + (diff > 0 ? "（差 " + diff + "）" : "") + " · 遍历文件 " + (a.files_total || 0)
      + " · 范围：" + (a.scoped ? "指定目录" : "全部授权目录");
    var ok = a.complete !== false;
    var h = [];
    h.push('<div style="padding:9px 11px;border-radius:10px;margin:2px 0 12px;font-size:12.5px;line-height:1.6;background:'
      + (ok ? "rgba(48,209,88,.12);color:#5bd87f" : "rgba(255,159,10,.14);color:#ffb043") + '">'
      + (ok ? "✓ 本次枚举完整：每个目录都读到了底。若入库数仍少于磁盘音频数，不是漏扫，而是单独入库失败（看下方“文件名编码异常”与日志）。"
            : "⚠ 本次枚举不完整：有目录读不到 / 超深度被截断 / 中途停止。不完整时系统不会清理已有入库记录（旧版会误删，表现为“几千首只扫出一半”），按下表处理后重新扫描。")
      + "</div>");
    h.push('<table style="width:100%"><thead><tr><th>目录</th><th>子目录</th><th>文件</th><th>音频</th>'
      + '<th>读不到子目录</th><th>超深度截断</th><th>软链剪枝</th><th>排除</th><th>状态</th></tr></thead><tbody>');
    roots.forEach(function (r) {
      var bad = !r.exists || r.errs || r.depth_cut;
      h.push("<tr>"
        + '<td class="mono" style="word-break:break-all">' + esc(r.root) + "</td>"
        + "<td>" + (r.dirs || 0) + "</td>"
        + "<td>" + (r.files || 0) + "</td><td>" + (r.audio || 0) + "</td>"
        + "<td>" + (r.errs ? '<b style="color:#ff6961">' + r.errs + "</b>" : "0") + "</td>"
        + "<td>" + (r.depth_cut ? '<b style="color:#ffb043">' + r.depth_cut + "</b>" : "0") + "</td>"
        + "<td>" + (r.link_cut || 0) + "</td>"
        + "<td>" + (r.excluded || 0) + "</td>"
        + "<td>" + (!r.exists ? '<span style="color:#ff6961">目录不存在/不可进入</span>'
                 : bad ? '<span style="color:#ffb043">需处理</span>' : '<span style="color:#5bd87f">正常</span>') + "</td>"
        + "</tr>");
    });
    h.push("</tbody></table>");
    if (!roots.length) h.push('<div class="hint" style="font-size:12px">还没有配置音乐目录，请先到「目录」页添加。</div>');
    if ((a.errors || []).length && !iss.length) {
      h.push('<div class="fld" style="margin-top:14px">读不到的子目录（旧快照，最多列 8 个）</div>');
      h.push('<div style="font-size:12px;line-height:1.7;color:#ff9f0a;word-break:break-all">'
        + a.errors.map(function (x) { return "<div>" + esc(x) + "</div>"; }).join("") + "</div>");
    }
    if (iss.length) {
      h.push('<div class="fld" style="margin-top:14px">没扫出来的目录与原因（' + iss.length + ' 条'
        + (a.issues_more ? '，另有 ' + a.issues_more + ' 条超出单次记录上限未列出' : '') + '）</div>');
      Object.keys(by).sort(function (x, y) { return by[y].length - by[x].length; }).forEach(function (k) {
        var rows = by[k];
        h.push('<div style="margin-top:9px;font-size:12px;color:#ffb043"><b>'
          + esc(AUDIT_KIND[k] || k) + ' × ' + rows.length + '</b>'
          + '<span style="color:var(--sub)"> · ' + esc((rows[0] && rows[0].hint) || "") + '</span></div>');
        h.push('<div style="font-size:11.5px;line-height:1.7;color:#c7c7cc;word-break:break-all">'
          + rows.slice(0, 60).map(function (x) {
            return '<div><span class="mono">' + esc(x.dir) + '</span>'
              + (x.code ? ' <span style="color:#ff9f0a">errno ' + x.code + '</span>' : '')
              + (k === "denied" && x.reason ? ' <span style="color:#98989d">' + esc(x.reason) + '</span>' : '')
              + '</div>';
          }).join("")
          + (rows.length > 60 ? '<div style="color:#98989d">…同类另有 ' + (rows.length - 60) + ' 条（日志页可见前 40 条）</div>' : '')
          + '</div>');
      });
      h.push('<div class="hint" style="font-size:12px;margin-top:8px">上面这些就是“歌明明在磁盘上、库里却没有”的全部原因：'
        + "读不到的要给应用用户加权限（具体缺哪一位、可复制的修复命令，概览页顶部「查看并处理」里逐条列好了），"
        + "超深度的去设置页调高「扫描深度上限」，排除规则命中到就去设置页去掉那条；处理完再点「扫描音乐库」正式入库。</div>");
    } else if (a.complete === false) {
      h.push('<div class="hint" style="font-size:12px;margin-top:10px;color:#ffb043">'
        + "本次被判定为枚举不完整却没逐项给出原因（旧快照），点「重新体检」重跑一次。</div>");
    }
    if (a.inaccessible) {
      h.push('<div class="fld" style="margin-top:14px">扩展名是音频但读不到：' + a.inaccessible + " 个</div>");
      h.push('<div style="font-size:12px;line-height:1.7;color:#ff9f0a;word-break:break-all">'
        + (a.inaccessible_samples || []).map(function (x) { return "<div>" + esc(x) + "</div>"; }).join("") + "</div>");
    }
    var exts = a.ext_skipped || {};
    var keys = Object.keys(exts);
    if (keys.length) {
      h.push('<div class="fld" style="margin-top:14px">未视为音频的扩展名 Top' + (a.no_ext ? "（另有 " + a.no_ext + " 个无扩展名文件）" : "") + '</div>');
      h.push('<div style="display:flex;flex-wrap:wrap;gap:6px">' + keys.map(function (k) {
        return '<span style="background:rgba(255,255,255,.07);border-radius:5px;padding:2px 7px;font-size:11.5px">'
          + esc(k) + " ×" + exts[k] + "</span>";
      }).join("") + "</div>");
      h.push('<div class="hint" style="font-size:12px;margin-top:6px">若你确实有上面某种扩展名的音乐没入库（如某些小众容器），反馈后可加入支持列表。</div>');
    }
    if (diff > 0 && ok) {
      h.push('<div class="hint" style="font-size:12px;margin-top:14px">枚举完整但入库少 ' + diff
        + " 个：先点一次「扫描」再看本页；仍不补齐就在「日志」页搜“无法入库 / 文件名编码异常”，这类文件通常是归档时留下的非 UTF-8 文件名，重命名后即可入库。</div>");
    }
    $("aud-body").innerHTML = h.join("");
  }

  /* ---------- 读不到的目录：概览页提示条 + 处理面板 ----------
     为什么不能只写在日志里：日志页要点进去、还会被后续任务刷掉，而“少了几首歌”
     这件事需要一直在眼前。快照落在后端数据目录，所以重启应用提示条还在。 */
  var deniedData = null;

  function renderDeniedBar(d) {
    d = d || {};
    var bar = $("denied-bar");
    if (!bar) return;
    var items = d.items || [];
    var blocked = items.filter(function (x) { return !x.fixed && !x.gone; });
    var fixedN = items.filter(function (x) { return x.fixed; }).length;
    var goneN = items.filter(function (x) { return x.gone; }).length;
    var n = blocked.length + (d.more || 0);
    if (!n) {
      /* 全修好了或全没了都不再推：这时候只留一句“重扫即可”比红字告警有用 */
      bar.classList.add("hidden");
      return;
    }
    bar.classList.remove("hidden");
    $("denied-txt").innerHTML = "有 <b>" + n + "</b> 个目录本应用读不到，里面所有歌都没被扫到"
      + "（外层授权不覆盖后来新建的子目录：给大目录开「继承父级权限」就能传下去）"
      + (fixedN ? " · 另有 " + fixedN + " 个现在已可读，重扫即可" : "")
      + (goneN ? " · " + goneN + " 个已不存在" : "")
      + '<span style="color:var(--sub)"> · 上次枚举 ' + esc(d.ts || "—") + "</span>";
  }

  function loadDenied() {
    return api("/api/scan/denied").then(function (d) {
      deniedData = d;
      renderDeniedBar(d);
      return d;
    }).catch(function () { /* 提示条是附加信息，拿不到不该打扰用户 */ });
  }

  function openDenied() {
    $("denied-modal").classList.add("show");
    $("dn-sum").textContent = "正在核对权限…";
    $("dn-body").innerHTML = '<div class="hint" style="font-size:12px">正在核对权限…（只看目录权限位，不读文件内容）</div>';
    api("/api/scan/denied").then(function (d) {
      deniedData = d;
      renderDeniedBar(d);
      var items = d.items || [];
      var blocked = items.filter(function (x) { return !x.fixed && !x.gone; }).length;
      $("dn-sum").textContent = "仍读不到 " + (blocked + (d.more || 0)) + " 个"
        + " · 已可读 " + items.filter(function (x) { return x.fixed; }).length + " 个"
        + " · 上次枚举 " + (d.ts || "—");
      renderDenied(d);
    }).catch(function (e) {
      $("dn-sum").textContent = "核对失败";
      $("dn-body").innerHTML = '<div class="hint" style="color:var(--err);font-size:12px">' + esc(e.message) + "</div>";
    });
  }

  function renderDenied(d) {
    d = d || {};
    var items = d.items || [];
    var h = [];
    h.push('<div style="padding:9px 11px;border-radius:10px;margin:2px 0 12px;font-size:12.5px;line-height:1.7;background:rgba(255,159,10,.12);color:#ffb043">'
      + "飞牛的「授权目录」是<b>按那一条路径生效</b>的：外层目录授权了，但授权之后由别的账号（SMB / 文件管理器 / 离线下载）新建的子目录，"
      + "权限是按创建者当时的设置落地的，外层那条授权不会补到它们身上。所以处理方向只有一个：让这些子目录把外层授权继承下来。<br>"
      + "本应用以账号 <b>" + esc(d.run_user || "music-tidy") + "</b> 运行，是非特权用户，"
      + "<b>没法自己给自己加权限</b>（改别人属主的目录只会得到「不允许的操作」），所以必须请你在飞牛一侧处理一次。");
    h.push("</div>");
    if (!items.length) {
      h.push('<div class="hint" style="font-size:12px">没有留下“读不到”的快照：要么一直能读，要么下次「仅全量扫描 / 扫描音乐库」会刷新这里。</div>');
      $("dn-body").innerHTML = h.join("");
      return;
    }
    h.push('<div class="fld" style="margin-top:4px">三条解法（任选其一，都不影响文件内容）</div>');
    /* 别再写「去授权目录里把子目录逐个加一遍」：飞牛上主目录授权后，选目录的控件
       不给再往里挑子目录，那条路根本不通。首选只能是右键开「继承父级权限」。 */
    h.push('<div style="font-size:12.5px;line-height:1.8;color:#c7c7cc">'
      + "① <b>首选（最省事）</b>：飞牛文件管理器里右键该目录 →「详细信息」→「权限」→「高级」→<b>启用继承父级权限</b>；"
      + "外层已经授权给本应用，继承下来就好。注意：别去「应用设置 → 授权目录」里挨个添加子目录，主目录授权后那里已选不动子目录<br>"
      + "② 同一个右键菜单的「权限」里，直接给账号 <b>" + esc(d.run_user || "music-tidy") + "</b> 加「读取 + 遍历」，并勾选应用到子项目<br>"
      + "③ SSH 执行 setfacl（只追加一条 ACL，不覆盖你原有的权限），每个目录后面都带了可直接复制的命令"
      + "</div>");
    h.push('<div class="fld" style="margin-top:14px">逐个目录的现场核对（共 ' + items.length + " 个"
      + (d.more ? "，另有 " + d.more + " 个未列在快照里" : "") + "）</div>");
    items.forEach(function (x) {
      var st = x.fixed ? '<span class="badge" style="background:rgba(48,209,88,.16);color:#5bd87f">已可读</span>'
        : x.gone ? '<span class="badge" style="background:rgba(255,255,255,.08);color:#98989d">目录已不存在</span>'
        : '<span class="badge" style="background:rgba(255,69,58,.16);color:#ff6961">读不到</span>';
      h.push('<div style="margin-top:10px;padding:10px 12px;border-radius:10px;background:rgba(255,255,255,.04);border:1px solid rgba(255,255,255,.08)">'
        + '<div class="mono" style="word-break:break-all;font-size:12.5px">' + esc(x.dir) + " " + st + "</div>"
        + '<div style="font-size:12px;color:#98989d;margin-top:5px;line-height:1.7">'
        + (x.mode ? "权限 <b>" + esc(x.mode) + "</b> · 属主 " + esc(x.owner || "?")
          + (x.uid !== null && x.uid !== undefined ? "(" + x.uid + ")" : "") + " · " : "")
        + (x.code ? "枚举时 errno " + x.code + " · " : "")
        + esc(x.brief || x.reason || "")
        + "</div>"
        + (x.fix ? '<div style="display:flex;align-items:center;gap:8px;margin-top:7px">'
          + '<code class="mono" style="flex:1;min-width:0;word-break:break-all;font-size:11.5px;background:rgba(0,0,0,.3);padding:5px 8px;border-radius:6px">'
          + esc(x.fix) + '</code>'
          + '<button class="btn btn-ghost btn-sm dn-copy" data-copy="' + esc(x.fix) + '" style="flex:0 0 auto">复制</button></div>' : "")
        + "</div>");
    });
    var allCmds = items.filter(function (x) { return x.fix; }).map(function (x) { return x.fix; }).join("\n");
    if (allCmds) {
      h.push('<div class="row" style="margin-top:12px;align-items:center;gap:8px;flex-wrap:wrap">'
        + '<button class="btn btn-ghost btn-sm dn-copy" data-copy="' + esc(allCmds) + '">复制全部命令</button>'
        + '<span class="hint" style="font-size:12px">执行完回到顶部点「重新扫描」；本应用不会尝试代你改权限。</span></div>');
    }
    h.push('<div class="hint" style="font-size:12px;margin-top:10px">处理完可直接点概览页的「扫描音乐库」；'
      + "不确定是不是权限问题就点「仅全量扫描」，逐目录原因会写进「日志」页。"
      + "本面板的结论是当下重新核对的：已标「已可读」的不用管，重扫就会补上。</div>");
    $("dn-body").innerHTML = h.join("");
    Array.prototype.forEach.call($("dn-body").querySelectorAll(".dn-copy"), function (b) {
      b.onclick = function () {
        copyText(b.getAttribute("data-copy") || "", "已复制，粘到 SSH 终端里执行即可");
      };
    });
  }

  /* ---------- Tab ---------- */
  function switchTab(name) {
    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (t) {
      t.classList.toggle("active", t.getAttribute("data-tab") === name);
    });
    // 窄屏下导航是横向滚动的：切页后把当前项滚进可视区，否则会“选中了但看不见”
    var cur = document.querySelector(".tab.active");
    if (cur && cur.scrollIntoView) {
      try { cur.scrollIntoView({ block: "nearest", inline: "nearest" }); } catch (e) { }
    }
    ["overview", "folders", "library", "playlist", "download", "convert", "dedupe", "settings", "log"].forEach(function (n) {
      $("tab-" + n).classList.toggle("hidden", n !== name);
    });
    if (name === "library") { loadLibrary(); loadCodeStats(); }
    if (name === "playlist") { loadPlaylists(); }
    if (name === "dedupe") { loadDuplicatesCached(); loadDupCovers(); }
    if (name === "settings") loadConfig();
    if (name === "log") loadLogs();
    if (name === "overview") { loadStatus(); loadDenied(); }
    /* 下载中心悬浮球只在「下载」页出现：别的页面挂一个全局浮窗只是挡内容。
       任务照常在后台跑，回到本页会立刻按 /api/status 补齐状态。 */
    var dlc = $("dlcenter");
    if (dlc) dlc.classList.toggle("hidden", name !== "download");
    if (name !== "download") {
      dlcOpen = false;
      var dpanel = $("dlc-panel");
      if (dpanel) dpanel.classList.remove("open");
    }
  }

  /* ---------- 设置页顶部分类标签 ---------- */
  var SET_SUBS = ["strategy", "ai", "source", "playlist", "rules"];
  function switchSubTab(name) {
    if (SET_SUBS.indexOf(name) < 0) name = SET_SUBS[0];
    Array.prototype.forEach.call($("set-subtabs").querySelectorAll(".subtab"), function (b) {
      b.classList.toggle("active", b.getAttribute("data-sub") === name);
    });
    SET_SUBS.forEach(function (n) {
      $("sp-" + n).classList.toggle("hidden", n !== name);
    });
    try { localStorage.setItem("set_sub_v1", name); } catch (e) {}
    /* 进 AI 子页就拉一次用量：这页的“今日/总计花了多少 token”必须是当下数值，
       停在设置页其它子页时不白拉（接口很轻，但没变化时也没必要轮） */
    if (name === "ai") loadAiUsage();
  }

  function initSubTabs() {
    Array.prototype.forEach.call($("set-subtabs").querySelectorAll(".subtab"), function (b) {
      b.onclick = function () { switchSubTab(b.getAttribute("data-sub")); };
    });
    var saved = "";
    try { saved = localStorage.getItem("set_sub_v1") || ""; } catch (e) {}
    switchSubTab(saved || "strategy");
  }

  /* ---------- 事件绑定 ---------- */
  function bind() {
    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (t) {
      t.onclick = function () { switchTab(t.getAttribute("data-tab")); };
    });
    initSubTabs();
    $("btn-scan").onclick = function () {
      api("/api/scan", { method: "POST", body: scopeBody() }).then(function () {
        toast(scope.length ? "扫描已开始（仅指定目录）" : "扫描已开始"); loadStatus();
        pollTask();
      }).catch(function (e) { toast(e.message); });
    };
    /* 仅全量扫描：只枚举计数不写库，专门用来看“哪个文件夹没扫到、为什么” */
    $("btn-probe").onclick = function () {
      api("/api/scan/probe", { method: "POST", body: scopeBody() }).then(function () {
        toast("仅全量扫描已启动（" + (scope.length ? "仅指定目录" : "全部授权目录")
          + "）：只数文件、不入库不改动任何文件，逐目录原因写入「日志」页");
        loadStatus(); pollTask();
      }).catch(function (e) { toast(e.message); });
    };
    $("btn-tidy").onclick = function () {
      api("/api/tidy", { method: "POST", body: scopeBody() }).then(function () {
        toast(scope.length ? "整理已开始（仅指定目录）" : "整理已开始"); loadStatus();
        pollTask();
      }).catch(function (e) { toast(e.message); });
    };
    $("btn-refresh").onclick = function () { loadStatus(); };
    function taskCtrl(action, msg) {
      api("/api/task", { method: "POST", body: { action: action } })
        .then(function () { toast(msg); loadStatus(); })
        .catch(function (e) { toast(e.message); });
    }
    $("btn-pause").onclick = function () { taskCtrl("pause", "已暂停（当前文件处理完后生效）"); };
    $("btn-resume").onclick = function () { taskCtrl("resume", "已继续"); };
    $("btn-stop").onclick = function () {
      if (!confirm("停止后已完成的文件保留进度，下次「开始整理」从断点继续。确定停止？")) return;
      taskCtrl("stop", "已停止，进度保留");
    };
    $("btn-retidy").onclick = function () {
      if (!confirm("重新整理会把" + (scope.length ? "所选目录里的" : "所有")
        + "文件重置为未整理并全部重新刮削（已有歌词/封面文件也会重新检查），确定？")) return;
      api("/api/task", { method: "POST", body: scopeBody({ action: "reset" }) })
        .then(function (d) {
          toast("已重置 " + d.reset + " 个文件，开始整理…");
          return api("/api/tidy", { method: "POST", body: scopeBody() });
        })
        .then(function () { loadStatus(); pollTask(); })
        .catch(function (e) { toast(e.message); });
    };
    /* 扫描范围：概览页与「目录」页共用一份，弹窗只改草稿，点确定才生效 */
    $("btn-scope").onclick = function () { openScope(""); };
    $("btn-scope2").onclick = function () { openScope(""); };
    $("btn-scope-clear").onclick = clearScope;
    $("btn-scope-clear2").onclick = clearScope;
    $("sc-ok").onclick = scApply;
    $("sc-cancel").onclick = function () { $("scope-modal").classList.remove("show"); };
    $("sc-close").onclick = function () { $("scope-modal").classList.remove("show"); };
    $("sc-all").onclick = function () { scDraft = {}; $("sc-sum").textContent = scSumText(); scMarkAll(); };
    $("btn-add-folder").onclick = function () {
      var v = $("folder-input").value.trim();
      if (!v) { toast("请输入目录路径"); return; }
      api("/api/folders", { method: "POST", body: { path: v } })
        .then(function () { $("folder-input").value = ""; toast("已添加目录"); return loadStatus(); });
    };
    $("btn-lib-query").onclick = function () { state.lib.offset = 0; loadLibrary(); };
    $("lib-search").addEventListener("keydown", function (e) { if (e.key === "Enter") { state.lib.offset = 0; loadLibrary(); } });
    $("lib-tidied").onchange = function () { state.lib.offset = 0; loadLibrary(); };
    $("lib-missing").onchange = function () { state.lib.offset = 0; loadLibrary(); };
    $("lib-code").onchange = function () { state.lib.offset = 0; loadLibrary(); };
    $("btn-codepl").onclick = function () {
      var out = prompt("分类歌单输出目录（音乐库外的目录），如 /vol1/1000/音乐歌单：", "");
      if (out === null) return;
      out = out.trim();
      if (!out) { toast("请输入输出目录"); return; }
      api("/api/export_code_playlists", { method: "POST", body: { out_dir: out } })
        .then(function (d) { toast("已生成 " + d.playlists.length + " 个分类歌单到 " + d.out_dir); })
        .catch(function (e) { toast(e.message); });
    };
    $("lib-prev").onclick = function () { state.lib.offset = Math.max(0, state.lib.offset - state.lib.limit); loadLibrary(); };
    $("lib-next").onclick = function () { state.lib.offset += state.lib.limit; loadLibrary(); };
    $("view-card").onclick = function () {
      state.view = "card"; $("view-card").classList.add("active"); $("view-table").classList.remove("active");
      renderLibrary();
    };
    $("view-table").onclick = function () {
      state.view = "table"; $("view-table").classList.add("active"); $("view-card").classList.remove("active");
      renderLibrary();
    };
    $("btn-aibatch").onclick = function () {
      if (!confirm("将调用 AI 为所有缺歌词/封面的文件补全（曲库优先，歌词缺失时由 AI 生成带时间码歌词）。确定开始？")) return;
      api("/api/ai_complete", { method: "POST", body: {} })
        .then(function () { toast("AI 补全任务已开始"); loadStatus(); pollTask(); })
        .catch(function (e) { toast(e.message); });
    };
    $("btn-libsel").onclick = function () { toggleMulti(); };
    $("btn-classify").onclick = classifyAll;
    $("btn-touch").onclick = touchAll;
    $("btn-split-cue").onclick = splitCue;
    $("btn-organize").onclick = organizeAll;
    $("btn-audit").onclick = openAudit;
    $("btn-denied").onclick = openDenied;
    $("dn-reprobe").onclick = openDenied;
    $("dn-close").onclick = function () { $("denied-modal").classList.remove("show"); };
    $("aud-run").onclick = runAudit;
    $("aud-close").onclick = function () { $("audit-modal").classList.remove("show"); };
    /* 下载 */
    $("btn-dl-search").onclick = searchMusic;
    $("dl-keyword").addEventListener("keydown", function (e) { if (e.key === "Enter") searchMusic(); });
    $("btn-dl-sel").onclick = downloadSelected;
    /* 工具条挂在列表上方，点了要能反悔 */
    $("btn-dl-uns").onclick = function () {
      dlSelected = {};
      renderDlResults();
    };
    $("ed-aprompt").addEventListener("input", function () { this.setAttribute("data-touched", "1"); });
    dlBindFilterBar();
    $("btn-dl-playlist").onclick = downloadPlaylist;
    /* 下载中心 */
    $("dlc-fab").onclick = function () {
      dlcOpen = !dlcOpen;
      $("dlc-panel").classList.toggle("open", dlcOpen);
      /* 展开时主动重绘一次并补拉日志：否则刷新后面板里还是 index.html 写死的“暂无下载任务” */
      if (dlcOpen) { renderDlCenter(); dlcPullLogs(); }
    };
    $("dlc-clear").onclick = function () {
      dlcTasks = dlcTasks.filter(function (t) { return t.status === "running" || t.status === "paused"; });
      dlcSave(); renderDlCenter();
    };
    /* 统一播放器（底部迷你条 + 放大全屏） */
    var pa = pAudio();
    pa.addEventListener("timeupdate", function () { pSync(false); });
    pa.addEventListener("loadedmetadata", function () { pSync(true); });
    pa.addEventListener("play", pRenderPlayBtn);
    pa.addEventListener("pause", pRenderPlayBtn);
    pa.addEventListener("ended", function () { pNext(true); });
    pa.addEventListener("error", function () {
      var tr = P.queue[P.i];
      pRenderPlayBtn();
      if (!tr) return;
      if (tr.src === "dl") {
        /* 标记为不可播：搜索结果中置灰并沉底 */
        if (tr.ref) tr.ref.unplayable = true;
        if (dlResults.length) renderDlResults();
        toast("试听失败：" + (tr.title || "") + "（VIP 歌曲或该平台 Cookie 无效）");
        pNext(true);  // 自动跳过，避免卡死在当前曲
      } else {
        toast("无法播放：" + (tr.title || "") + "（文件不可访问）");
      }
    });
    $("mp-play").onclick = pToggle;
    $("mp-prev").onclick = pPrev;
    $("mp-next").onclick = function () { pNext(false); };
    $("mp-expand").onclick = pExpand;
    $("pl-play").onclick = pToggle;
    $("pl-prev").onclick = pPrev;
    $("pl-next").onclick = function () { pNext(false); };
    $("pl-min").onclick = pMinimize;
    $("pl-close").onclick = pClose;
    $("pl-full").onclick = pFull;
    $("pl-fav").onclick = pToggleFav;
    $("mp-fav").onclick = pToggleFav;
    $("pl-addpl").onclick = function () { openAddPl(P.queue[P.i]); };
    $("pl-vol").addEventListener("input", pApplyVol);
    $("pl-mute").onclick = pMute;
    $("pl-seek").addEventListener("input", function () {
      var a2 = pAudio();
      if (a2.duration) a2.currentTime = this.value / 1000 * a2.duration;
      pSync(false);
    });
    /* 点歌词行直接跳播 */
    $("pl-lyric").addEventListener("click", function (e) {
      var ln = e.target && e.target.closest ? e.target.closest(".ln[data-i]") : null;
      if (!ln) return;
      var l = P.lines[Number(ln.getAttribute("data-i"))];
      var a3 = pAudio();
      if (!l || !isFinite(a3.duration)) return;
      a3.currentTime = l.t;
      a3.play().catch(function () {});
      pSync(true);
    });
    $("player-modal").onclick = function (e) { if (e.target === $("player-modal")) pMinimize(); };
    document.addEventListener("fullscreenchange", pSyncFullBtn);
    document.addEventListener("webkitfullscreenchange", pSyncFullBtn);
    window.addEventListener("resize", function () { pLyricPad(); if (P.lines.length) pSync(true); });
    /* 编辑识别信息弹窗 */
    $("ed-close").onclick = closeEdit;
    $("ed-cancel").onclick = closeEdit;
    $("ed-save").onclick = saveEdit;
    $("ed-analyze").onclick = edAnalyze;
    $("edit-modal").onclick = function (e) { if (e.target === $("edit-modal")) closeEdit(); };
    /* 确认解析规则弹窗 */
    $("rlm-close").onclick = closeRuleConfirm;
    $("rlm-cancel").onclick = closeRuleConfirm;
    $("rlm-save").onclick = saveRule;
    $("rlm-regex").addEventListener("input", renderRuleParsed);
    $("rule-modal").onclick = function (e) { if (e.target === $("rule-modal")) closeRuleConfirm(); };
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { closeEdit(); closeRuleConfirm(); closeShare(); closeAddPl(); }
    });
    /* 去重：一键 / AI */
    function dedupeAll(ai) {
      var mode = $("dup-mode").value;
      var kind = $("dup-kind").value;
      var outDir = $("dup-outdir").value.trim();
      if (mode === "move" && !outDir) { toast("请选择/填写移入的项目外目录"); return; }
      var tip = ai ? "AI 将逐组判断保留项（每组一次大模型调用，较慢）。" : "";
      var act = mode === "delete" ? "直接删除" : mode === "move" ? "移入 " + outDir : "移入回收站";
      var scope = kind === "content" ? "内容重复" : kind === "name" ? "名称重复（同曲不同版本）" : "全部重复（内容+名称）";
      if (!confirm(tip + "处理" + scope + "：每组保留一个最优文件，其余" + act + "。确定？")) return;
      var btn = ai ? $("btn-dedupe-ai") : $("btn-dedupe-all");
      var old = btn.textContent; btn.disabled = true; btn.textContent = "处理中…";
      api("/api/dedupe_all", { method: "POST", body: { mode: mode, out_dir: outDir, ai: ai, kind: kind } })
        .then(function (d) {
          var r = d.result || {};
          toast("已处理 " + r.groups + " 组，移除 " + r.removed + " 个文件" + (r.errors && r.errors.length ? "（失败 " + r.errors.length + "）" : ""));
          loadDuplicatesCached(); loadStatus();
        })
        .catch(function (e) { toast(e.message); })
        .finally(function () { btn.disabled = false; btn.textContent = old; });
    }
    $("btn-dedupe-all").onclick = function () { dedupeAll(false); };
    $("btn-dedupe-ai").onclick = function () { dedupeAll(true); };
    $("dup-kind").onchange = function () { loadDuplicatesCached(); };
    function llmConfigPatch() {
      return {
        enable_llm: $("cfg-llm-enabled").checked,
        enable_ai_cover: $("cfg-ai-cover").checked,
        llm: {
          base_url: $("cfg-llm-url").value.trim(),
          api_key: $("cfg-llm-key").value.trim(),
          model: $("cfg-llm-model").value.trim(),
          image: {
            base_url: $("cfg-img-url").value.trim(),
            api_key: $("cfg-img-key").value.trim(),
            model: $("cfg-img-model").value.trim(),
          },
        },
      };
    }
    $("btn-save-llm").onclick = function () {
      saveConfig(llmConfigPatch(), "LLM 配置已保存");
    };
    $("btn-save-ms").onclick = function () {
      var srcs = [];
      Array.prototype.forEach.call(document.querySelectorAll(".cfg-ms-src"), function (cb) {
        if (cb.checked) srcs.push(cb.value);
      });
      var apis = [];
      document.querySelectorAll(".ms-api-row").forEach(function (row) {
        var url = row.querySelector(".msapi-url").value.trim();
        if (!url) return;
        var tokenEl = row.querySelector(".msapi-token");
        apis.push({
          name: row.querySelector(".msapi-name").value.trim() || "自定义",
          url: url,
          token: tokenEl.value.trim(),
          enabled: row.querySelector(".msapi-en").checked,
        });
      });
      var alias = {};
      Object.keys(SRC_ALIAS).forEach(function (k) {
        var el = $("cfg-al-" + k);
        alias[k] = el ? el.value.trim().slice(0, 12) : "";
      });
      saveConfig({
        music_source: {
          enabled_sources: srcs,
          custom_apis: apis,
          alias: alias,
          download_dir: $("cfg-ms-dldir").value.trim(),
          cookies: {
            netease: $("cfg-ms-ck-netease").value.trim(),
            qq: $("cfg-ms-ck-qq").value.trim(),
            kugou: $("cfg-ms-ck-kugou").value.trim(),
            kuwo: $("cfg-ms-ck-kuwo").value.trim(),
            migu: $("cfg-ms-ck-migu").value.trim(),
          },
          transcode: $("cfg-ms-transcode").checked,
        }
      }, "音源设置已保存");
    };
    $("btn-ms-add-api").onclick = function () {
      addMsApiRow({ name: "", url: "", token: "", enabled: true });
    };
    $("ddm-close").onclick = closeDlDetail;
    $("dl-detail-modal").onclick = function (e) { if (e.target === $("dl-detail-modal")) closeDlDetail(); };
    $("ddm-preview").onclick = function () {
      if (!dlDetailSong) return;
      if (dlDetailSong.locked || dlDetailSong.unplayable) {
        toast(dlDetailSong.locked ? "VIP/需付费，无法试听" : "试听链接获取失败，无法播放"); return;
      }
      pPlayQueue([dlTrack(dlDetailSong)], 0);
      closeDlDetail();  // 关闭详情弹窗，露出底部迷你播放器
    };
    $("ddm-lyric-btn").onclick = loadDlLyric;
    $("btn-test-llm").onclick = function () {
      saveConfig(llmConfigPatch()).then(function () {
        toast("连接测试中……");
        $("btn-test-llm").disabled = true;
        return api("/api/llm/test", { method: "POST", body: {} })
          .then(function () { toast("LLM 连接成功 ✓"); })
          .catch(function (e) { toast("LLM 连接失败：" + e.message); })
          .finally(function () {
            $("btn-test-llm").disabled = false;
            /* 测试连接本身就是一次调用，当场刷一下，不让人以为“测试不算用量” */
            loadAiUsage();
          });
      });
    };
    $("btn-ai-usage").onclick = function () { loadAiUsage(); };
    $("btn-rule-analyze").onclick = function () {
      var sample = $("rule-sample").value.trim();
      if (!sample) { toast("请先填写文件名样例"); return; }
      var btn = this;
      btn.disabled = true;
      btn.textContent = "AI 分析中…";
      api("/api/rules/analyze", { method: "POST", body: { sample: sample, desc: $("rule-desc").value.trim() } })
        .then(function (d) { openRuleConfirm(d); })
        .catch(function (e) {
          toast("分析失败: " + e.message);
          // LLM 未配置/未启用时直接跳到 AI 标签页，省去手动找入口
          if (/LLM/.test(e.message)) switchSubTab("ai");
        })
        .finally(function () { btn.disabled = false; btn.textContent = "AI 分析规则"; });
    };
    $("btn-save-strategy").onclick = function () {
      saveConfig({
        enable_scrape: $("cfg-scrape").checked,
        overwrite_lyric: $("cfg-ow-lyric").checked,
        overwrite_cover: $("cfg-ow-cover").checked,
        embed_tags: $("cfg-embed").checked,
        enable_classify: $("cfg-classify").checked,
      }).then(function () {
        // 增量监听启停 + 间隔
        var w = $("cfg-watch").checked;
        var iv = Number($("cfg-watch-interval").value) || 30;
        var act = w ? "start" : "stop";
        return api("/api/watch", { method: "POST", body: { action: act } })
          .then(function () { return api("/api/watch", { method: "POST", body: { action: "set_interval", interval: iv } }); })
          .then(function () { toast("整理策略已保存（增量监听" + (w ? "已开启" : "已关闭") + "）"); })
          .catch(function () { toast("策略已保存，但监听设置失败"); });
      });
    };
    $("btn-refresh-log").onclick = function () { loadLogs(); };
    /* 歌单页：入口卡片 / 新建 / 明细操作 */
    $("plc-fav").onclick = function () {
      if (PL.favId) { openPlaylist(PL.favId); return; }
      loadPlaylists().then(function () { if (PL.favId) openPlaylist(PL.favId); });
    };
    $("plc-my").onclick = plShowMine;
    $("btn-pl-new").onclick = function () {
      $("pl-newwrap").classList.toggle("hidden");
      if (!$("pl-newwrap").classList.contains("hidden")) $("pl-newname").focus();
    };
    $("btn-pl-create").onclick = plCreate;
    $("btn-pl-create-cancel").onclick = function () { $("pl-newwrap").classList.add("hidden"); };
    $("pl-newname").addEventListener("keydown", function (e) { if (e.key === "Enter") plCreate(); });
    $("btn-pl-refresh").onclick = function () {
      loadPlaylists().then(function () { if (PL.pid) openPlaylist(PL.pid); });
    };
    $("pld-playall").onclick = plPlayAll;
    $("pld-share").onclick = openShare;
    $("pld-fetch").onclick = plFetch;
    $("pld-archive").onclick = plArchive;
    $("pld-savearch").onclick = savePlArchive;
    $("pld-rename").onclick = plRename;
    $("pld-del").onclick = plDelete;
    /* 分享弹窗 */
    $("shm-close").onclick = closeShare;
    $("share-modal").onclick = function (e) { if (e.target === $("share-modal")) closeShare(); };
    $("shm-gen").onclick = makeShare;
    $("shm-copy").onclick = function () { copyText($("shm-link").textContent); };
    $("shm-revoke").onclick = function () {
      var m = /\/api\/share\/([0-9a-zA-Z_-]+)/.exec(String($("shm-link").textContent || ""));
      if (!m) { toast("还没有可用的分享链接"); return; }
      revokeShare(m[1]);
    };
    /* 添加到歌单弹窗 */
    $("apm-close").onclick = closeAddPl;
    $("apm-cancel").onclick = closeAddPl;
    $("addpl-modal").onclick = function (e) { if (e.target === $("addpl-modal")) closeAddPl(); };
    $("apm-create").onclick = addPlCreate;
    $("apm-ok").onclick = addPlSubmit;
    /* 导入别人的分享歌单 */
    $("btn-isp-preview").onclick = ispPreview;
    $("btn-isp-import").onclick = ispImport;
    $("isp-url").addEventListener("keydown", function (e) { if (e.key === "Enter") ispPreview(); });
    /* 设置 → 歌单分享 */
    $("btn-save-pl").onclick = function () {
      saveConfig({
        playlist: {
          archive_root: $("cfg-pl-root").value.trim(),
          archive_mode: $("cfg-pl-mode").value,
          auto_sync: $("cfg-pl-auto").checked,
          share_base: $("cfg-pl-base").value.trim().replace(/\/+$/, ""),
        }
      }, "歌单设置已保存");
    };
    $("btn-pl-parse").onclick = function () {
      var text = $("pl-link").value.trim();
      if (!text) { toast("请先粘贴歌单链接"); return; }
      api("/api/playlist/parse?text=" + encodeURIComponent(text)).then(function (d) {
        renderPlaylistPreview(d);
      }).catch(function (e) { toast("解析失败: " + e.message); $("pl-preview").innerHTML = ""; });
    };
    $("btn-pl-import").onclick = function () {
      var text = $("pl-link").value.trim();
      var outDir = $("pl-outdir").value.trim();
      var mode = $("pl-mode").value;
      if (!text) { toast("请先粘贴歌单链接"); return; }
      if (!outDir) { toast("请填写输出目录"); return; }
      api("/api/playlist/import", { method: "POST", body: { text: text, out_dir: outDir, mode: mode } })
        .then(function (r) { renderPlaylistResult(r); })
        .catch(function (e) { toast("导入失败: " + e.message); $("pl-result").innerHTML = ""; });
    };
    $("btn-convert").onclick = function () {
      var files = $("cv-files").value.split("\n").map(function (s) { return s.trim(); }).filter(Boolean);
      var outDir = $("cv-outdir").value.trim();
      var auto = $("cv-autotidy").checked;
      if (!files.length) { toast("请填写待转换的文件路径"); return; }
      if (!outDir) { toast("请填写输出目录"); return; }
      $("btn-convert").disabled = true;
      $("btn-convert").textContent = "转换中…";
      api("/api/convert", { method: "POST", body: { files: files, out_dir: outDir, auto_tidy: auto } })
        .then(function (d) { renderConvertResult(d.results || []); loadStatus(); })
        .catch(function (e) { toast("转换失败: " + e.message); })
        .finally(function () {
          $("btn-convert").disabled = false;
          $("btn-convert").textContent = "开始转换";
        });
    };
    $("btn-convert-selftest").onclick = function () {
      var b = $("btn-convert-selftest");
      b.disabled = true;
      b.textContent = "自检中…";
      api("/api/convert_selftest", { method: "POST", body: {} })
        .then(function (d) { renderConvertSelftest(d); toast(d.ok ? "解密自检全部通过" : "解密自检存在失败项"); })
        .catch(function (e) { toast("自检失败: " + e.message); })
        .finally(function () { b.disabled = false; b.textContent = "解密自检"; });
    };

    /* ---------- 本地上传转码（多文件 / 拖拽） ---------- */
    var upFiles = [];
    var dropZone = $("cv-drop");
    var uploadInput = $("cv-upload");

    function renderUpList() {
      $("cv-ufiles").innerHTML = upFiles.map(function (f) {
        return '<span title="' + esc(f.name) + '">' + esc(f.name) +
          ' <b style="font-weight:400;color:var(--sub)">' + (f.size / 1024 / 1024).toFixed(1) + 'MB</b></span>';
      }).join("");
    }
    function addUpFiles(list) {
      Array.prototype.forEach.call(list, function (f) {
        var dup = upFiles.some(function (x) { return x.name === f.name && x.size === f.size; });
        if (!dup) upFiles.push(f);
      });
      renderUpList();
    }
    dropZone.onclick = function (e) {
      if (e.target !== uploadInput) uploadInput.click();
    };
    uploadInput.onchange = function () {
      addUpFiles(uploadInput.files);
      uploadInput.value = "";
    };
    ["dragenter", "dragover"].forEach(function (ev) {
      dropZone.addEventListener(ev, function (e) {
        e.preventDefault(); e.stopPropagation();
        dropZone.classList.add("over");
      });
    });
    ["dragleave", "drop"].forEach(function (ev) {
      dropZone.addEventListener(ev, function (e) {
        e.preventDefault(); e.stopPropagation();
        dropZone.classList.remove("over");
      });
    });
    dropZone.addEventListener("drop", function (e) {
      if (e.dataTransfer && e.dataTransfer.files) addUpFiles(e.dataTransfer.files);
    });

    $("btn-upload-convert").onclick = function () {
      if (!upFiles.length) { toast("请先选择要上传的文件"); return; }
      var form = new FormData();
      upFiles.forEach(function (f) { form.append("files", f, f.name); });
      var outDir = $("cv-outdir").value.trim();
      if (outDir) form.append("out_dir", outDir);
      form.append("auto_tidy", $("cv-autotidy").checked ? "1" : "0");
      var btn = $("btn-upload-convert");
      btn.disabled = true;
      btn.textContent = "上传转换中…";
      $("cv-uprog").textContent = "正在上传 " + upFiles.length + " 个文件…";
      fetch(gwPrefix() + "/api/convert_upload", { method: "POST", body: form })
        .then(function (r) {
          return r.json().catch(function () { return { error: "bad response" }; });
        })
        .then(function (d) {
          if (d && d.error) throw new Error(d.error);
          renderConvertResult(d.results || []);
          var ok = (d.results || []).filter(function (x) { return x.ok; }).length;
          toast("上传转码完成：成功 " + ok + " / " + upFiles.length);
          upFiles = [];
          renderUpList();
          loadStatus();
        })
        .catch(function (e) { toast("上传失败: " + e.message); })
        .finally(function () {
          btn.disabled = false;
          btn.textContent = "上传并转换";
          $("cv-uprog").textContent = "";
        });
    };
  }

  function pollTask() {
    var timer = setInterval(function () {
      api("/api/status").then(function (d) {
        state.status = d; renderTask();
        if (!d.task.running) { clearInterval(timer); loadStatus(); }
      }).catch(function () { clearInterval(timer); });
    }, 1500);
  }

  /* ---------- 启动 ---------- */
  /* 旧版定义了 dlcLoad() 却从没调用，刷新页面后 localStorage 里的下载记录全部“看不见”，
     只有新开一单才会重新出现——这也是“下载中心不同步”的一部分 */
  dlcLoad();
  loadScope();
  renderScope();
  bind();
  /* 刷新后面板不能停在 index.html 里写死的“暂无下载任务”：启动就按本地记录画一次 */
  renderDlCenter();
  pInitVol();
  loadFavMap();
  loadStatus();
  /* 提示条跨重启：快照在后端数据目录里，开页就得把它挂上来 */
  loadDenied();
  setInterval(function () { loadStatus(); }, 8000);
})();
