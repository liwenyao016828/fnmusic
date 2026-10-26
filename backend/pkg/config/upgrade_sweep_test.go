package config

import (
	"testing"
	"time"
)

// 后台补强的设置口径（v2.22）：nil = 跟收藏自动下载同步、显式值覆盖它、
// 数值夹区间、改动真的落到 cm.Get()。
func TestUpgradeSweepConfig(t *testing.T) {
	cm, err := NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatal(err)
	}
	// 老配置：FavAutoDownload=false → 补强也关（升级前的行为）
	if cm.Get().UpgradeSweepOn() {
		t.Fatal("老配置（没这个键）且收藏自动下载关着时，补强该是关的")
	}
	if err := cm.Update(AppConfig{FavAutoDownload: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if !cm.Get().UpgradeSweepOn() {
		t.Fatal("老配置且收藏自动下载开着时，补强该跟着开（升级不能把它悄悄停掉）")
	}
	// 显式关掉：哪怕收藏自动下载是开的也不补
	if err := cm.Update(AppConfig{UpgradeSweepEnabled: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if cm.Get().UpgradeSweepOn() {
		t.Fatal("显式关掉之后就该关")
	}
	// 数值夹区间
	if err := cm.Update(AppConfig{UpgradeSweepIntervalMinutes: 1, UpgradeSweepBatch: 999}); err != nil {
		t.Fatal(err)
	}
	c := cm.Get()
	if c.UpgradeSweepInterval() != time.Duration(UpgradeSweepMinIntervalMinutes)*time.Minute {
		t.Fatalf("间隔该被夹到下限，实际 %v", c.UpgradeSweepInterval())
	}
	if c.UpgradeSweepPerRound() != UpgradeSweepMaxBatch {
		t.Fatalf("批量该被夹到上限，实际 %d", c.UpgradeSweepPerRound())
	}
	// 0 = 回到默认
	// 0 = 没改（局部 patch 里没这个键）：已存的 15 分钟不能被顺手改掉。
	if err := cm.Update(AppConfig{UpgradeSweepIntervalMinutes: 0, UpgradeSweepBatch: 0}); err != nil {
		t.Fatal(err)
	}
	if cm.Get().UpgradeSweepInterval() != time.Duration(UpgradeSweepMinIntervalMinutes)*time.Minute {
		t.Fatalf("0 表示「没改这个键」，已存的间隔不该被动：%v", cm.Get().UpgradeSweepInterval())
	}
	// 从来没设过时读出来是默认值。
	for _, a := range []AppConfig{{}, {UpgradeSweepIntervalMinutes: 0, UpgradeSweepBatch: 0}} {
		if a.UpgradeSweepInterval() != time.Duration(UpgradeSweepDefaultIntervalMinutes)*time.Minute {
			t.Fatalf("没设过时该读成默认 %d 分钟", UpgradeSweepDefaultIntervalMinutes)
		}
		if a.UpgradeSweepPerRound() != UpgradeSweepDefaultBatch {
			t.Fatalf("没设过时批量该读成默认 %d", UpgradeSweepDefaultBatch)
		}
	}
}
