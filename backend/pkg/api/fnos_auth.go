package api

// 文件访问授权（飞牛官方权限模型）—— 状态查询与「授权后刷新」。
//
// 背景：飞牛的规则是「应用默认看不到用户文件，必须由用户显式授权目录」
// （https://developer.fnnas.com/api/authorization/overview/）。曲率以前只靠
// 「/vol1 起全都允许」这种粗口径，于是出现两类相反的故障：
//   · 用户在应用设置里授权了目录，曲率却仍进不去（后端没有承认这些路径）；
//   · 用户什么都没授权，曲率却能读到整个卷（不符合飞牛权限模型）。
//
// 本文件的接口只回答一件事：**这台机器上，曲率当前到底被授权了哪些目录**，
// 以及**如果没授权，该去哪儿点**。它不返回文件内容、不改任何授权。
//
// 授权入口（前端）：
//   · 管理员共享授权：JS SDK pickSharedFile / authorizeSharedFile，或「应用设置 → 授权目录」；
//   · 直接跳本应用的设置页：JS SDK openAppSetting()（官方 1.2.0401 / App 1.34.0 起）。
//
// ⚠️ 2026-10-31 用户明确「这个应用只给自己（管理员）用」，所以不再提供普通用户
// 的个人授权入口（pickUserFile / authorizeUserFile / trim.file.userAccess）：
// 个人授权绑定登录账号，换账号或 uid 变化就要重新授权，单管理员场景只会多一个
// 会失效的来源。授权入口统一为管理员共享授权。

import (
	"net/http"
	"strings"

	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/nas"
)

// authStatus 是「这台机器上曲率被授权了什么」的一次快照。
//
// ⚠️ **只对回环请求返回路径**。这个接口和 /api/fnos/status 一样会被同网段设备
// 扫到；路径列表等于这台 NAS 的目录结构，所以外部来源只给布尔与计数。
type authStatus struct {
	// Available 表示**当前是不是飞牛环境**（判据见 fnos.Detected()：api-scope socket
	// 或 /var/apps/<appname> 存在）。前端据此决定要不要显示授权提示条；
	// 非飞牛环境（本地开发）为 false，此时不该提示任何东西。
	//
	// ⚠️ 2026-10-31 这里的判据从 fnos.Available()（要求有 TRIM_API_TOKEN）换成了
	// fnos.Detected()。真机上系统可能没注入 token，Available() 会是 false，前端于是
	// 把「该提示用户去授权」的提示条整个隐藏了 —— 而这时用户恰恰最需要这条提示。
	Available bool `json:"available"`
	// APIAvailable 表示开放 API **现在能不能调**（需要 TRIM_API_TOKEN + socket）。
	// 它只影响「后端能不能自动读到授权目录」，不影响前端发起授权（授权走 JS SDK）。
	// 前端在 available=true 且 api_available=false 时可以多给一句「请重装/重启应用」。
	APIAvailable bool `json:"api_available"`
	// Authorized 表示「用户/管理员真的授权过至少一个业务目录」，前端据此决定要不要显示提示条。
	//
	// ⚠️ 2026-10-31 真机事故：这里原来用「共享授权 ∪ FnOSAuthorizedRoots()」判断，
	// 而后者包含 config/resource 里 data-share 声明的**应用自己的**数据目录
	// （TRIM_DATA_SHARE_PATHS → /vol1/@appshare/yinshu-ai/data）。于是新装机器在
	// 一个音乐目录都没授权的情况下就回 authorized=true，提示条永不出现，用户只能
	// 自己去撞 403 —— 这就是「点了没反应」的另一半原因。现在只看 FnOSGrantedRoots()。
	Authorized bool `json:"authorized"`
	// Roots 是当前实际生效的可访问根目录（飞牛环境下＝授权目录＋应用数据目录；非飞牛环境＝再加上探测卷），仅回环可见。
	Roots []string `json:"roots,omitempty"`
	// FnosRoots 是其中**来自飞牛授权**的那部分（含 data-share 自用目录），仅回环可见。
	FnosRoots []string `json:"fnos_roots,omitempty"`
	// GrantedRoots 是「用户授权」的那部分（管理员共享 ∪ TRIM_DATA_ACCESSIBLE_PATHS，
	// 已剔除 data-share 自用目录）—— 判断「用户到底有没有给过音乐目录」看它。
	GrantedRoots []string `json:"granted_roots,omitempty"`
	// SharedCount 是管理员共享授权的条目数（外部来源也能看，不含路径）。
	SharedCount int `json:"shared_count"`
	// CanGrant 表示当前会话能不能直接发起授权：只有管理员能共享授权，
	// 而我们无法在后端判断管理员身份（前端 SDK 会在弹窗里自己拒绝），
	// 所以这里恒为 true，前端仍要处理「仅管理员可进行此操作」的报错。
	CanGrant bool `json:"can_grant"`
	// Hint 是要显示给用户看的**一句话**，已经是成品文案（不含路径与内部标识）。
	Hint string `json:"hint,omitempty"`
	// HintAction 告诉前端该给哪个按钮：「grant」= 发起授权、「open_setting」= 跳应用设置。
	HintAction string `json:"hint_action,omitempty"`
}

// HandleFnosAuthStatus 返回文件访问授权状态
//
// GET /api/fnos/auth-status
//
// 前端用途（App.vue 顶部提示条）：
//   - authorized=false 时显示提示条，按钮调 pickSharedFile / pickUserFile 或 openAppSetting()；
//   - 授权成功后调 POST /api/fnos/auth-refresh 让后端立刻重新取授权目录。
func (s *Server) HandleFnosAuthStatus(w http.ResponseWriter, r *http.Request) {
	detailed := isLocalRequest(r)

	status := authStatus{
		// Available = 是不是飞牛环境（判据与 token 无关，见 fnos.Detected()）。
		Available: fnos.Detected(),
		// APIAvailable = 开放 API 现在能不能调（要 token + socket）。
		APIAvailable: fnos.Available(),
		CanGrant:     true,
	}

	// 授权条目数：只统计管理员共享授权（本应用只给管理员自己用，见文件头注释）。
	if status.APIAvailable {
		if folders, err := fnos.SharedFolders(); err == nil {
			status.SharedCount = len(folders)
		}
	}

	all := nas.AllowedRoots()
	// FnosRoots = 路径守卫认可的飞牛侧全部根（含 data-share 自用目录），用于排障展示。
	status.FnosRoots = nas.FnOSAuthorizedRoots()
	// GrantedRoots = 用户/管理员真正给过的那部分（不含 data-share 自用目录）。
	// 「要不要提示用户去授权」只看这一份：自己的数据目录不算授权，否则新装机
	// 永远显示「已授权」，提示条不出现（2026-10-31 真机故障）。
	status.GrantedRoots = nas.FnOSGrantedRoots()
	status.Authorized = status.SharedCount > 0 || len(status.GrantedRoots) > 0
	if detailed {
		status.Roots = all
	}

	switch {
	case !status.Available:
		// 非飞牛环境（本地开发）：不提示，前端看 available=false 自行隐藏。
		status.Hint = ""
		status.HintAction = ""
	case status.Authorized:
		status.Hint = ""
		status.HintAction = ""
	default:
		// ⚠️ 这段会原样显示在界面上，只写「发生了什么 + 你该点什么」，
		// 不写 /vol1、不写接口名、不写 Markdown 标记。
		status.Hint = "曲率还没有被授权访问任何音乐目录。点「授权目录」选择你的音乐文件夹，" +
			"或到「应用中心 → 曲率 → 设置 → 授权目录」里添加。"
		status.HintAction = "grant"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": status,
	})
}

// HandleFnosAuthRefresh 让后端立刻重新读取飞牛授权目录
//
// POST /api/fnos/auth-refresh
//
// 用户在页面上点了「授权目录」并选完目录之后，前端调这个接口。没有它的话，
// 后端最长要等 30 秒缓存过期才认新授权 —— 而用户会把这段等待理解成「授权没生效」。
//
// 查询参数 `path`（可选）：宿主选择器刚返回的那个目录。带上它就顺手回答一句
// 「这个路径现在读得到吗」—— 前端据此决定「选完直接用」还是给一句可操作的提示
// （见 pickResult 里的注释：飞牛宿主侧授权可能是异步落库的）。
func (s *Server) HandleFnosAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "请使用 POST")
		return
	}
	nas.InvalidateFnOSRootsCache()
	fnosRoots := nas.FnOSAuthorizedRoots()
	granted := nas.FnOSGrantedRoots()

	data := map[string]interface{}{
		// authorized 只看「用户授权的业务目录」（FnOSGrantedRoots，不含 data-share
		// 自用目录）：非飞牛环境下 AllowedRoots 会退回探测卷，用 len(roots)>0
		// 判断会让「开发机」也报 authorized=true；而把 data-share 算进来会让
		// 新装机永远「已授权」（2026-10-31 真机故障）。
		"authorized":    len(granted) > 0,
		"roots_count":   len(nas.AllowedRoots()),
		"fnos_count":    len(fnosRoots),
		"granted_count": len(granted),
	}

	// 刚选中的路径是否已经落在可访问范围内。
	// 这里**只读**——不复用 ResolveSafePath 的错误细节，因为前端要的是
	// 「能用 / 还不能用 + 该怎么办」，而不是路径守卫的异常串。
	if p := strings.TrimSpace(r.URL.Query().Get("path")); p != "" {
		real, err := nas.ResolveSafePath(p)
		readable := err == nil
		msg := ""
		if !readable {
			// 文案要能照着做：说清「还没生效」和「下一步点哪儿」，
			// 不写 /api 路径、不写异常类名（界面上只显示这一句）。
			msg = "这个目录还没出现在授权列表里，稍等几秒点「重试」，" +
				"或到「应用中心 → 曲率 → 设置 → 授权目录」确认它已保存"
		}
		data["picked_path"] = p
		data["picked_readable"] = readable
		data["picked_real_path"] = real
		data["picked_hint"] = msg
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": data,
	})
}
