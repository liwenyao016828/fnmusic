# 参考项目：music-tidy（源码已移出仓库）

> 这一目录原本收录了第三方项目 `music-tidy` 的**完整源码副本**（v1.12.0 与 v1.14.0，
> 共 88 个文件：Python 后端 + 原生 JS 前端 + 打包清单），用于研究其整理引擎与 AI 模块。
>
> **2026-10-22 已从仓库移除**（原因见下），只保留我们自己写的分析与精读报告。

## 为什么移除

1. **许可证风险**：`music-tidy` 是第三方维护者（manifest 里 `maintainer = 拾壹`，
   `maintainer_url = https://11.qlj.ink`）的飞牛应用，原包里**没有附带 LICENSE**。
   把别人未声明授权条件的源码整份复制进我们的公开仓库，等于在替对方做授权决定 ——
   这是合规问题，不是洁癖。
2. **体积**：2.6 MB / 95 个文件，绝大部分与我们的代码无关，且每次读都要 grep 噪声。
3. **可替代**：真正有用的信息已经被**提炼成我们自己的文档**（见下）。

## 保留下来的（都是我们自己写的）

| 文件 | 内容 |
|---|---|
| [`music-tidy/music-tidy-tidy_engine-研究报告.md`](music-tidy/music-tidy-tidy_engine-研究报告.md) | `tidy_engine.py`（4118 行）精读报告：整理引擎的算法、匹配策略、边界条件，引用格式 `文件:行号` |
| [`music-tidy-1.14.0/README.md`](music-tidy-1.14.0/README.md) | v1.14.0 的定位说明与「看 AI 部分从哪入手」的导航 |
| [`../music-meta-web-数据源插件分析.md`](../music-meta-web-数据源插件分析.md) | 另一个参考项目的数据源插件分析 |
| [`../leelaa.playlist-调研.md`](../leelaa.playlist-调研.md) | 歌单相关调研 |

## 要再读源码怎么办

不要重新往仓库里拷。在**本地**解包一份就够：

```bash
# 从 fpk（就是 gzip tar）解到仓库外
mkdir -p ~/refs/music-tidy && tar -xzf music-tidy-1.14.0.fpk -C ~/refs/music-tidy
```

需要引用某段实现时，把**结论**写进上面那几份报告（带 `文件:行号`），
而不是把代码搬进仓库。

## 对本项目的影响（历史）

- `backend/pkg/ai` 的 LLM 调用是**移植**自它的 `ai_llm.py` —— 这部分代码现在是我们自己的实现，
  与上游只存在设计思路上的对应关系。
- 「整理去重 / 补全歌词封面」的功能设计大量参考了它的 `tidy_engine.py`。
