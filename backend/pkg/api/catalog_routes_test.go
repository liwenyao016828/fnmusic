package api

import (
	"sort"
	"strings"
	"testing"
)

// 路由表 ↔ 接口目录的往返测试。
//
// 为什么必须双向且严格：这个 app 主要由外部 AI 通过 GET /api/catalog 驱动，
// 「有路由但没登记」= AI 永远发现不了这个能力；「登记了但没路由」= AI 照文档调用拿到 404。
// 两种都是静默故障 —— 调用方只会以为应用坏了，不会想到是文档错。只能靠测试拦。
//
// 为什么不能像 .dev/catalog-audit.mjs 那样用「前缀双向包含」判断（core.startsWith(ec) ||
// ec.startsWith(core)）：它把 /api/sources 当成覆盖了整个 /api/sources/* 家族、把
// /api/ai/usage 当成覆盖了 /api/ai/usage/reset，于是真漏登记的接口被它算成「已登记」，
// 输出「有路由但目录没登记：（无）」—— 正好是它本该抓的那类问题。这里只认精确匹配，
// 少数服务端写法与目录写法不同的接口必须显式进 routeCatalogAliases / undocumentedRoutes。

// catalogPaths 目录里出现过的路径 → 该方法下有哪些方法。
func catalogPaths() map[string][]string {
	out := map[string][]string{}
	for _, e := range APICatalog("http://example.com") {
		out[e.Path] = append(out[e.Path], e.Method)
	}
	for p := range out {
		sort.Strings(out[p])
	}
	return out
}

func routePathSet() map[string]bool {
	out := make(map[string]bool, len(routeTable))
	for _, rt := range routeTable {
		out[rt.Path] = true
	}
	return out
}

func TestEveryRouteIsInCatalogOrExplicitlyExcluded(t *testing.T) {
	cat := catalogPaths()
	for _, rt := range routeTable {
		if reason, excluded := undocumentedRoutes[rt.Path]; excluded {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("路由 %s 被列为「不登记」但没写理由；有理由也要写清楚，别让这张表变成掩盖漏登记的后门", rt.Path)
			}
			if _, inCat := cat[rt.Path]; inCat {
				t.Errorf("路由 %s 已在目录里，却又被列为「故意不登记」——两张表打架了", rt.Path)
			}
			continue
		}
		want := []string{rt.Path}
		if alias, ok := routeCatalogAliases[rt.Path]; ok {
			want = alias
		}
		for _, p := range want {
			if _, ok := cat[p]; !ok {
				t.Errorf("路由 %s 对应的目录条目 %s 不存在：请补进 catalog.go，或在 routes.go 的 routeCatalogAliases / undocumentedRoutes 里写明理由",
					rt.Path, p)
			}
		}
	}
}

func TestEveryCatalogPathHasARoute(t *testing.T) {
	cat := catalogPaths()
	routes := routePathSet()

	// 被某条路由认领的目录路径（含改名的别名目标）。
	claimed := map[string]bool{}
	for _, rt := range routeTable {
		if _, excluded := undocumentedRoutes[rt.Path]; excluded {
			continue
		}
		if alias, ok := routeCatalogAliases[rt.Path]; ok {
			for _, p := range alias {
				claimed[p] = true
			}
			continue
		}
		claimed[rt.Path] = true
	}

	paths := make([]string, 0, len(cat))
	for p := range cat {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if claimed[p] {
			continue
		}
		if _, ok := routes[p]; ok {
			// 路由本身就在，只是上一步走了别名分支；不是问题。
			continue
		}
		t.Errorf("目录里的 %s（方法 %s）没有任何路由服务它：文档在说谎，AI 照它调用只会得到 404",
			p, strings.Join(cat[p], "/"))
	}
}

func TestAliasAndExclusionTablesAreHonest(t *testing.T) {
	cat := catalogPaths()
	routes := routePathSet()

	for path, targets := range routeCatalogAliases {
		if !routes[path] {
			t.Errorf("routeCatalogAliases 的键 %s 不是真实路由（改名/删路由后忘了同步）", path)
		}
		if len(targets) == 0 {
			t.Errorf("routeCatalogAliases[%s] 是空的：要么写目标，要么改用 undocumentedRoutes 说明为什么不登记", path)
		}
		for _, target := range targets {
			if _, ok := cat[target]; !ok {
				t.Errorf("routeCatalogAliases[%s] 指向 %s，但目录里没有这条 —— 别名在说谎", path, target)
			}
		}
	}

	for path := range undocumentedRoutes {
		if !routes[path] {
			t.Errorf("undocumentedRoutes 的键 %s 不是真实路由（改名/删路由后忘了同步）", path)
		}
		if _, both := routeCatalogAliases[path]; both {
			t.Errorf("%s 同时出现在别名表和不登记表里，两张表打架了", path)
		}
	}
}

// 重复路径会让 http.ServeMux 在启动时 panic（而且信息不含路由名）。
// 这里给出能直接定位的报错。
func TestRouteTableHasNoDuplicatePaths(t *testing.T) {
	seen := map[string]int{}
	for _, rt := range routeTable {
		seen[rt.Path]++
	}
	dups := []string{}
	for p, n := range seen {
		if n > 1 {
			dups = append(dups, p)
		}
	}
	if len(dups) > 0 {
		sort.Strings(dups)
		t.Errorf("路由表里有重复路径（会导致 mux 注册 panic）：%s", strings.Join(dups, ", "))
	}
	if len(routeTable) == 0 {
		t.Fatal("路由表为空")
	}
}

// 注册一次真实路由表：重复注册会 panic，处理器构造函数写错（比如拼错方法名）会编译不过。
// 这里用零值 Server：所有处理器都是「取方法值」而不解引用，构造期不需要真实依赖。
func TestRouterCanRegisterRouteTable(t *testing.T) {
	s := &Server{}
	if h := s.Router(); h == nil {
		t.Fatal("Router() 返回了 nil")
	}
}
