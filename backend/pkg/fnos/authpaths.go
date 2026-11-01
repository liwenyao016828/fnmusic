package fnos

// 授权路径 —— 本项目「哪些目录算数」的第三块拼图。
//
// 飞牛的文件访问模型是「应用默认看不到用户文件，必须由用户显式授权目录」
// （https://developer.fnnas.com/api/authorization/overview/）。授权分两类：
//
//	共享授权（管理员，trim.file.sharedAccess）
//	    「应用中心 → 本应用 → 设置 → 授权目录」里授权，对所有用户生效。
//	    官方能力：trim.file.getSharedAccessibleFolders / delSharedAccessibleFolder。
//	    前端：pickSharedFile / authorizeSharedFile。
//
//	用户授权（当前登录用户，trim.file.userAccess）
//	    用户在应用页面里自己选目录，只对自己生效。
//	    官方能力：trim.file.getUserAccessibleFolders（**必须带 uid**）。
//	    前端：pickUserFile({directory:true}) / authorizeUserFile。
//
// ⚠️ 2026-10-31：本应用只给管理员自己用（用户原话「一般都是管理员自己用」），
// 授权入口收敛为**唯一的共享授权**。个人授权相关的能力（getUserAccessibleFolders /
// delUserAccessibleFolder / checkUserACL / convertPath）连同它们的 scope 一起从本
// 项目移除了 —— 留着它们只会让人以为还有第二条授权路径，而实际后端不再查询个人授权。
// 因此 config/resource 只声明 trim.file.sharedAccess 一项 scope（getPlatformConfig
// 的调用方 SystemLanguage() 也一并删除，不再声明）。
//
// 两类授权系统都会把目标路径的 ACL 授给本应用的运行用户；应用侧要做的是
// 「承认这些路径」，也就是把它们并进 nas.AllowedRoots()。
//
// ⚠️ 本文件只负责「读系统注入的路径」，不做任何权限判断，也不缓存落盘。

import (
	"os"
	"strings"
)

// DataAccessiblePaths 解析系统注入的 TRIM_DATA_ACCESSIBLE_PATHS（冒号分隔）。
//
// 来源是「用户授权给本应用的可访问路径」，由系统在启动生命周期脚本时注入
// （https://developer.fnnas.com/docs/core-concepts/environment-variables/）。
// 它是**补充**来源，不是全部：用户授权可能发生在应用启动之后，所以运行时还要
// 再问一次开放 API（见 openapi.go 的 SharedFolders）。
//
// 官方原文：「使用这些路径读写文件前，应用仍应验证文件权限」—— 也就是说这条
// 环境变量只说明「用户授权了」，不保证当前进程一定能读写。
func DataAccessiblePaths() []string {
	return splitPathList(os.Getenv("TRIM_DATA_ACCESSIBLE_PATHS"))
}

// DataSharePaths 解析系统注入的 TRIM_DATA_SHARE_PATHS（config/resource 里
// data-share 声明的共享目录），同样是冒号分隔。
func DataSharePaths() []string {
	return splitPathList(os.Getenv("TRIM_DATA_SHARE_PATHS"))
}

// splitPathList 按冒号切分并用 os.PathListSeparator 兜底（两者都是 ':'，但语义
// 不同：TRIM_* 是官方定义的冒号分隔，不是平台相关分隔符，这里显式写死冒号）。
func splitPathList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ":")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// DataAccessiblePathsForTest 用显式字符串走一遍真实解析逻辑，供其它包的测试使用
// （避免测试直接改环境变量影响并行用例）。
func DataAccessiblePathsForTest(raw string) []string {
	return splitPathList(raw)
}
