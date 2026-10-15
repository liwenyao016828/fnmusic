package intercept

import (
	"path/filepath"
	"testing"
)

// cfgOnly 造一台「只有配置」的拦截层：cache_dir 这条路径只读 i.cfg，
// 走 New() 反而要凑齐 Upstream / 存储目录（缺一个就直接返回 nil）。
func cfgOnly(c Config) *Interceptor { return &Interceptor{cfg: c} }

// cache_dir（设置页的「缓存目录」）的语义：配了就**两个**暂存目录都挪进去，
// 没配就各自用原来的默认位置。
//
// 为什么两个默认位置不一样还要一起管：见 tee.go 的 cacheRoot —— 曲库暂存要跟
// 成品同卷（「提升」是一次 rename，跨卷会退化成复制），滚动试听缓存跟成品无关。
// 配上 cache_dir 的典型用法是「缓存单独放一块盘」，所以两个都跟着走。
func TestCacheDirOverridesBothStagingDirs(t *testing.T) {
	data := t.TempDir()
	dl := t.TempDir()
	cache := t.TempDir()

	// 没配 cache_dir：各用各的默认位置（与改造前完全一致）
	i := cfgOnly(Config{DataDir: data, DownloadDir: func() string { return dl }})
	if got, want := i.teeDir(), filepath.Join(dl, teeSubdir); got != want {
		t.Errorf("默认曲库暂存目录 = %q，期望 %q", got, want)
	}
	if got, want := i.teeRollingDir(), filepath.Join(data, teeRollingSubdir); got != want {
		t.Errorf("默认滚动缓存目录 = %q，期望 %q", got, want)
	}

	// 配了 cache_dir：两个都挪过去
	i2 := cfgOnly(Config{
		DataDir:     data,
		DownloadDir: func() string { return dl },
		CacheDir:    func() string { return cache },
	})
	if got, want := i2.teeDir(), filepath.Join(cache, teeSubdir); got != want {
		t.Errorf("配了 cache_dir 后曲库暂存目录 = %q，期望 %q", got, want)
	}
	if got, want := i2.teeRollingDir(), filepath.Join(cache, teeRollingSubdir); got != want {
		t.Errorf("配了 cache_dir 后滚动缓存目录 = %q，期望 %q", got, want)
	}

	// 空白 = 「没配」。界面上点「恢复默认」写的就是空串（三态），
	// 这里绝不能拼出「根目录下的 /.qulv-tee」这种东西。
	i3 := cfgOnly(Config{
		DataDir:     data,
		DownloadDir: func() string { return dl },
		CacheDir:    func() string { return "   " },
	})
	if got, want := i3.teeDir(), filepath.Join(dl, teeSubdir); got != want {
		t.Errorf("空白 cache_dir 应回落默认，得到 %q，期望 %q", got, want)
	}
	if got, want := i3.teeRollingDir(), filepath.Join(data, teeRollingSubdir); got != want {
		t.Errorf("空白 cache_dir 应回落默认，得到 %q，期望 %q", got, want)
	}

	// CacheDir 没接线（老构造 / 最小构造）＝ 没配，不能崩也不能拼出怪路径。
	i4 := cfgOnly(Config{DataDir: data, DownloadDir: func() string { return dl }})
	if got, want := i4.teeRollingDir(), filepath.Join(data, teeRollingSubdir); got != want {
		t.Errorf("CacheDir 为 nil 时应回落默认，得到 %q，期望 %q", got, want)
	}
}
