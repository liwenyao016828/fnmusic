package intercept

import (
	"context"
	"errors"
	"strings"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/search"
)

// 「洛雪(LX)音源脚本」进下载源池这一条路。
//
// # 为什么它是池里的一条**来源**，而不是另起一套挑选逻辑
//
// 池的语义是「同一首歌的多个来源之间挑最好的那份」（见 acquire.go 顶部）。
// 洛雪脚本能搜到歌（前提是脚本在 inited 里声明了 musicSearch），那它就是池里的
// 一个候选来源，没有理由另走一条路 —— 所以它的结果与平台候选一起
// **过同一套筛**（sameTitle / durationMatches），再一起进 sortByPreference。
// 这里只负责「把洛雪的搜索结果翻译成池认得的结构」，不新增任何匹配/挑选规则。
//
// # 缺字段的现状（实测，见 lxnode.Song 的注释）
//
// 真脚本 `qsvip` 的搜索结果只有 歌名 / 歌手 / 专辑 / 时长(秒) / 封面 / id，
// **没有音质档位、没有体积**。按池里**既有**的约定（这三条一条都不是这里另加的）：
//
//   - 时长缺失（0）→ durationMatches 不参与判定（那是「没给字段」，不是「对不上」）；
//   - 体积缺失（0）→ betterCandidate 里不主动占优（0 是「没有信息」，不是「更小」）；
//   - 音质缺失（空串）→ qualityRank 算最低档（0），于是它排在任何已知档位之后，
//     并且过不了 downloadCandidates 里「严格更好」那一道。
//
// ⚠️ 最后一条是**实测出来的实际后果**，不是这里可以随手放宽的东西：
// 给洛雪结果编一个档位就等于伪造决策依据（界面会显示「从某源下的无损」而实际不是），
// 而放宽「严格更好」会改掉池里既有的挑选语义。缺口要补在**字段来源**上
// （脚本/宿主多给一个 quality），不是补在判据上。

// lxSourcePrefix 是洛雪候选在池里的源标识前缀。
//
// 必须带前缀：洛雪脚本自己的源 key（wy / tx / kg / kw / mg）与曲率的平台代号
// **会撞名**，而那些代号在池里另有解析器。带上 `lx:` 之后「这条候选该找谁解析」
// 在池里是唯一的、不含糊的（把 lxnode 注册成 `lx:<src>` 的解析器即可）。
const lxSourcePrefix = "lx:"

// lxSearchLimit 是每次问洛雪要几条。
//
// 与平台源取的规模一致（rankedSources 里平台源是 size=5）：跨源匹配要的是
// 「有没有同名同版的那一条」，不是把某个源的结果全捞回来。
const lxSearchLimit = 5

// lxCandidates 问一次洛雪宿主，把结果翻译成池能用的统一歌曲结构。
//
// 任何失败（宿主没开 / 连不上 / 脚本报错）都只记一笔日志并返回 nil ——
// 池里少一条来源而已，绝不影响其它来源，也绝不影响调用方（对齐 lxnode 的硬约束）。
//
// ⚠️ **宿主没开**（lxnode.ErrNotRunning）**不记日志**：那是用户开关的正常状态
// （这个能力缺省关），不是故障 —— 否则每个下载请求都会刷一行噪音。
func (i *Interceptor) lxCandidates(ctx context.Context, keyword string) []search.UnifiedSong {
	if i.lxSearch == nil || strings.TrimSpace(keyword) == "" {
		return nil
	}
	songs, err := i.lxSearch(ctx, keyword, lxSearchLimit)
	if err != nil {
		if !errors.Is(err, lxnode.ErrNotRunning) {
			i.logf("[INTERCEPT] 洛雪源搜索失败（不影响其它来源）：%v", err)
		}
		return nil
	}

	out := make([]search.UnifiedSong, 0, len(songs))
	for _, s := range songs {
		// ⚠️ id 与歌名缺一不可 —— 与 trackFromSong 同一条口径：没有平台内 id 的候选
		// 根本没法解析，让它进池只会在后面变成一次注定失败的解析。
		id := strings.TrimSpace(s.ID)
		name := strings.TrimSpace(s.Name)
		src := strings.TrimSpace(s.Source)
		if id == "" || name == "" || src == "" {
			continue
		}
		out = append(out, search.UnifiedSong{
			ID:      id,
			Songmid: id,
			Name:    name,
			Singer:  strings.TrimSpace(s.Singer),
			Album:   strings.TrimSpace(s.Album),
			Cover:   strings.TrimSpace(s.Pic),
			Source:  lxSourcePrefix + src,
			// 时长两边都填：与 trackFromSong 同口径，谁先被读到都一样。
			Duration: s.Duration,
			Interval: s.Duration,
			// Quality / FileSize **缺就是缺**：不填默认值（理由见本文件顶部）。
			Quality:  s.Quality,
			FileSize: s.Size,
		})
	}
	return out
}
