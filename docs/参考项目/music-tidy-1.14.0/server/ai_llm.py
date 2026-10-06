#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
ai_llm.py — LLM 语义识别模块（可选增强）

对文件名极乱、正则解析失败的音乐文件，调用 OpenAI 兼容的大模型
（如豆包 / OpenAI / DeepSeek 等）做语义识别，推断 歌手 / 歌名 / 风格。

- 使用 Python 标准库 urllib 调用 OpenAI 兼容 /chat/completions 接口。
- 未配置 API Key 时静默降级：返回空结果，不影响主流程。
- 强制模型输出严格 JSON，并做防御性解析。
- 支持 /v1/chat/completions 或 /chat/completions 两种 base 形式。
"""

import json
import os
import re
import sys
import urllib.error
import urllib.request

DEFAULT_MODEL = "doubao-seed-1-6-250615"
DEFAULT_TIMEOUT = 20


class LLMClient:
    def __init__(self, config: dict, enabled=None, on_usage=None):
        # config: {base_url, api_key, model, timeout, image: {base_url, api_key, model}}
        # enabled: 顶层开关 enable_llm；为 None 时回退读 config.enabled（兼容旧配置）
        # on_usage(kind, pt, ct, tt, ok, miss): 每次调用后的用量回调，供设置页统计今日/总计
        self._on_usage = on_usage
        self.config = config or {}
        if enabled is None:
            enabled = self.config.get("enabled")
        self.enabled = bool(enabled and self.config.get("api_key"))
        self.base_url = (self.config.get("base_url") or "").rstrip("/")
        self.api_key = self.config.get("api_key", "")
        self.model = self.config.get("model") or DEFAULT_MODEL
        self.timeout = int(self.config.get("timeout") or DEFAULT_TIMEOUT)
        # 文生图（AI 封面）：独立可选配置，api_key/base_url 缺省复用主 LLM
        img = self.config.get("image") or {}
        self.image_base = (img.get("base_url") or "").strip().rstrip("/")
        self.image_key = (img.get("api_key") or "").strip() or self.api_key
        self.image_model = (img.get("model") or "").strip()
        self.image_timeout = int(img.get("timeout") or 60)
        # 最近一次 generate_image 失败原因（供日志透出）
        self.last_error = ""

    @property
    def available(self) -> bool:
        return self.enabled and bool(self.base_url) and bool(self.api_key)

    @property
    def image_available(self) -> bool:
        return bool(self.image_model and self.image_key and
                    (self.image_base or self.base_url))

    def _endpoint(self) -> str:
        base = self.base_url.strip().rstrip("/")
        # 兼容用户填：完整 /chat/completions、带版本段 /v1 /v3（如方舟
        # https://ark.cn-beijing.volces.com/api/plan/v3）、或裸域名
        if base.endswith("/chat/completions"):
            return base
        if re.search(r"/v\d+$", base):
            return base + "/chat/completions"
        return base + "/v1/chat/completions"

    def test_connection(self) -> dict:
        """真实发起一次最小对话请求，返回 {ok, error}，供 UI「测试连接」使用。"""
        if not self.base_url:
            return {"ok": False, "error": "未填写 API 地址"}
        if not self.api_key:
            return {"ok": False, "error": "未填写 API Key"}
        body = {
            "model": self.model,
            "messages": [{"role": "user", "content": "ping"}],
            "max_tokens": 1,
        }
        req = urllib.request.Request(
            self._endpoint(),
            data=json.dumps(body).encode("utf-8"),
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {self.api_key}",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=min(self.timeout, 15)) as resp:
                resp.read(64)
            self._report("test", miss=1, ok=True)   # 测试连接也发了一次请求，计进去
            return {"ok": True, "error": ""}
        except urllib.error.HTTPError as e:
            detail = ""
            try:
                detail = e.read().decode("utf-8", errors="replace")[:200]
            except Exception:
                pass
            hint = {401: "API Key 无效", 403: "无权限/模型未开通", 404: "模型名或地址错误"}.get(e.code, "")
            self._report("test", ok=False)
            return {"ok": False, "error": f"HTTP {e.code} {hint} {detail}".strip()}
        except Exception as e:
            self._report("test", ok=False)
            return {"ok": False, "error": f"无法连接：{e}"}

    def recognize(self, filename: str) -> dict:
        """从文件名识别 {artist, title, style}，失败返回空 dict。"""
        if not self.available:
            return {}
        prompt = (
            "你是一个音乐文件元数据整理助手。请根据下面的音乐文件名，推断它的"
            "歌手、歌名和音乐风格。文件可能命名混乱、缺少信息或全是乱码。\n"
            "规则：\n"
            "1. 只输出一个 JSON 对象，不要任何额外文字。\n"
            "2. 字段固定为 artist(歌手)、title(歌名)、style(风格，如流行/摇滚/民谣等，未知填空串)。\n"
            "3. 无法判断的字段填空字符串。\n"
            "4. 歌手与歌名都只能取自文件名本身的文字，不要凭自己的知识换成另一首歌。\n"
            "5. 文件名里「好听的歌曲推荐/热门/经典老歌/抖音热歌/车载音乐/合集/歌单/榜单」"
            "这类歌单名、推荐语既不是歌手也不是歌名，要剔掉。\n"
            f'文件名："{filename}"\n'
            '输出示例：{"artist":"周杰伦","title":"晴天","style":"流行"}'
        )
        content = self._chat(prompt, max_tokens=200, kind="recognize")
        return self._parse_json(content) if content else {}

    def judge_name(self, filename: str, candidates=None) -> dict:
        """判定文件名里到底谁是歌手、谁是歌名，返回 {artist, title, style}。

        candidates 是规则层按分隔符切出的候选顺序 [(artist, title), ...]，可能把
        歌单前缀误当成歌手。模型的作用是「挑对/纠正」而不是重新创作，所以要求
        输出必须来自文件名。失败返回空 dict。

        用量归到 recognize 同一类（设置页「文件名识别」），不另开账。
        """
        if not self.available:
            return {}
        lines = []
        for i, pair in enumerate(candidates or []):
            try:
                a, t = str(pair[0] or "").strip(), str(pair[1] or "").strip()
            except Exception:
                continue
            if a or t:
                lines.append(f"候选{i + 1}：歌手=「{a}」 歌名=「{t}」")
        hint = ""
        if lines:
            hint = ("\n下面是程序按分隔符切出来的候选，可能切错了位置：\n"
                    + "\n".join(lines) + "\n")
        prompt = (
            "你是音乐文件命名判定助手。一个音频文件名里混着歌单名、推荐语、音质标记、"
            "歌手名、歌名，请判断真正的歌手与歌名。\n"
            "规则：\n"
            "1. 只输出一个 JSON 对象，不要任何额外文字。\n"
            "2. 字段固定为 artist(歌手)、title(歌名)、style(风格，未知填空串)。\n"
            "3. 歌手与歌名都只能是文件名里出现过的文字（可以再剔掉其中的噪声词），"
            "绝对不许换成另一首歌或另一个歌手；判不出来就把对应字段填空串。\n"
            "4. 「好听的歌曲推荐/热门/经典老歌/抖音热歌/车载音乐/合集/歌单/榜单/无损」"
            "这类歌单名、推荐语、音质标记既不是歌手也不是歌名。\n"
            "5. 歌单名前缀通常是紧贴在歌名上的（如「好听的歌曲推荐-下山」里的下山是歌名）；"
            "候选都不对时，从文件名里重新给出正确的歌手与歌名。\n"
            + hint +
            f'文件名："{filename}"\n'
            '输出示例：{"artist":"周杰伦","title":"晴天","style":"流行"}'
        )
        content = self._chat(prompt, max_tokens=200, kind="recognize")
        return self._parse_json(content) if content else {}

    def analyze_name_rule(self, sample: str, desc: str) -> dict:
        """把一个「文件名样例 + 命名解释」分析成一条命名解析正则规则。

        返回 {regex, fields, note}；失败返回 {}。
        regex 使用 Python 命名分组：artist(歌手)/title(歌名)/album(专辑)/track(序号)。
        """
        if not self.available or not sample:
            return {}
        prompt = (
            "你是音乐文件命名规则分析助手。用户给出一个文件名样例和对该命名方式的一句解释，"
            "请把它转换成一条用于解析文件名的 Python re 正则表达式。\n"
            "要求：\n"
            "1. 使用 Python 命名分组语法 (?P<名字>...)，组名只能取 artist(歌手)、"
            "title(歌名)、album(专辑)、track(序号)，至少包含 artist 或 title 之一；"
            "不需要提取的部分用非捕获分组或通配。\n"
            "2. 正则要能匹配样例文件名本身（用 re.search 语义），对扩展名要兼容"
            "（例如结尾用 (?:\\.[^.]+)?$ 或不锚定结尾）。\n"
            "3. 样例中出现的 . [ ] ( ) 等正则特殊字符若按字面匹配必须转义。\n"
            "4. 只输出一个 JSON 对象，格式："
            '{"regex":"...","fields":["artist","title"],"note":"一句话中文说明"}\n'
            f"文件名样例：\"{sample}\"\n"
            f"用户解释：\"{desc or '（无，请按样例与音乐命名习惯推断）'}\"\n"
        )
        content = self._chat(prompt, max_tokens=400, kind="name_rule")
        if not content:
            return {}
        obj = self._parse_obj(content)
        rx = str(obj.get("regex") or "").strip()
        if not rx:
            return {}
        return {
            "regex": rx,
            "fields": [str(f) for f in (obj.get("fields") or []) if isinstance(f, str)],
            "note": str(obj.get("note") or "").strip()[:200],
        }

    def dedupe_decide(self, group: list, gtype: str = "content") -> dict:
        """让 AI 判断一组重复文件保留哪个。

        group: [{"index": 0, "path": "...", "size": 123, "bitrate_hint": "...", "tidied": 1}, ...]
        gtype: content=内容完全相同(SHA-1一致) | name=同曲不同版本(名称相同内容不同)
        返回 {"keep_index": 0, "reason": "..."}；失败返回 {}。
        """
        if not self.available or len(group) < 2:
            return {}
        lines = []
        for it in group:
            lines.append(
                f'- 编号{it["index"]}: {it["path"]} | 大小 {round((it.get("size") or 0) / 1048576, 1)}MB'
                f' | 已整理:{"是" if it.get("tidied") == 1 else "否"}'
                f' | 时长 {it.get("duration") or "未知"}'
                f' | 码率 {it.get("bitrate") or "未知"}kbps'
                f' | 格式 {it.get("ext") or ""}')
        if gtype == "name":
            head = ("下面是同一首歌（歌手与歌名相同）但文件内容不同的多个版本，需要决定保留哪一个、移除其余。\n"
                    "判断依据优先级：1) 音质更好（无损格式 flac wav ape 优先，其次码率高、时长完整）；"
                    "2) 路径更规范、命名更完整（含歌手-歌名）；3) 已整理过的优先。\n")
        else:
            head = ("下面是内容完全相同（SHA-1 一致）的多个音乐文件，需要决定保留哪一个、删除其余。\n"
                    "判断依据优先级：1) 音质更好（时长更长/码率更高/无损格式如 flac wav ape）；"
                    "2) 路径更规范、命名更完整（含歌手-歌名）；3) 已整理过的优先。\n")
        prompt = (head +
                  "请只输出一个 JSON 对象，字段：keep_index(整数，要保留的文件编号)、"
                  'reason(不超过40字的中文理由)。不要输出其它文字。\n\n文件列表：\n' + "\n".join(lines)
        )
        content = self._chat(prompt, max_tokens=200, kind="dedupe")
        if not content:
            return {}
        obj = self._parse_obj(content)
        ki = obj.get("keep_index")
        if isinstance(ki, str):
            try:
                ki = int(re.search(r"\d+", ki).group(0))
            except Exception:
                ki = None
        if not isinstance(ki, int) or ki < 0 or ki >= len(group):
            return {}
        return {"keep_index": ki, "reason": str(obj.get("reason") or "").strip()[:80]}

    @staticmethod
    def _with_user(base: str, user: str) -> str:
        """把用户手写的提示词接到内置提示词前面，并拿到最高优先级。

        只给“最高优先级”而不保留输出契约的话，模型会按用户话式自由输出，
        下游 JSON/LRC 解析直接全部失败；所以只提升优先级、不删内置格式要求。
        """
        user = (user or "").strip()
        if not user:
            return base
        return ("【用户附加要求 —— 最高优先级，与下方任何说明冲突时一律以它为准；"
                "但输出格式必须仍遵守下方的格式要求】\n" + user[:2000] + "\n\n" + base)

    def generate_lyric(self, artist: str, title: str, style: str = "", user_prompt: str = "") -> str:
        """让 AI 生成带时间码的标准 LRC 歌词。失败返回空串。

        注意：AI 生成的时间码是估算值，仅作为兜底；返回前会做 LRC 格式校验。
        user_prompt 为用户手写提示词，非空时优先级高于内置要求（但仍保留 LRC 格式校验）。
        """
        if not self.available or not title:
            return ""
        tpl = (
            "请为歌曲《{t}》（歌手：{a}{s}）生成一份标准 LRC 歌词文件内容。\n"
            "严格要求：\n"
            "1. 每行格式为 [mm:ss.xx]歌词文本，时间码从 [00:00.00] 开始并随歌曲推进递增。\n"
            "2. 前 3 行放 [00:00.00] 标题、歌手、专辑信息（可留空文本）。\n"
            "3. 歌词内容要贴合这首歌的真实歌词；若不确定完整歌词，请根据歌名与风格创作合理、连贯、"
            "有画面感的中文歌词，共 12-24 行，主歌副歌结构。\n"
            "4. 只输出 LRC 正文，不要 markdown 代码块、不要任何解释文字。\n"
            '5. 每句歌词单独一行，行内不要包含方括号时间码以外的 "[" 字符。'
        ).format(t=title, a=artist or "未知", s=("，风格：" + style) if style else "")
        content = self._chat(self._with_user(tpl, user_prompt), max_tokens=1200, temperature=0.7,
                             kind="lyric")
        return self._sanitize_lrc(content)

    @staticmethod
    def _sanitize_lrc(text: str) -> str:
        """清洗并校验 LRC：去掉代码块包裹，只保留合法时间码行。"""
        if not text:
            return ""
        text = text.strip()
        m = re.search(r"```(?:lrc|text)?\s*(.*?)```", text, re.S)
        if m:
            text = m.group(1).strip()
        out = []
        pat = re.compile(r"^\[\d{1,2}:\d{2}(?:[.:]\d{1,3})?\].+\S")
        for line in text.splitlines():
            line = line.strip()
            if not line:
                continue
            if pat.match(line):
                out.append(line)
        # 至少要 4 行有效歌词行才认为可用
        if len(out) < 4:
            return ""
        return "\n".join(out) + "\n"

    def analyze_song(self, artist: str, title: str, lyric: str, user_prompt: str = "") -> dict:
        """判断给定歌词是否与「歌手 + 歌名」匹配（只判定，不建议修改歌手/歌名）。

        返回 {match, confidence, reason, actual}；失败返回 {}。
        user_prompt 非空时作为最高优先级附加要求，但输出仍需满足下面的 JSON 契约。
        """
        if not self.available or not (lyric or "").strip():
            return {}
        body = re.sub(r"\[\d{1,2}:\d{2}(?:[.:]\d{1,3})?\]", "", lyric)
        body = re.sub(r"\n{2,}", "\n", body).strip()[:1500]
        prompt = (
            "你是歌词核验助手。请充分运用你自身的知识以及联网检索能力，"
            "核实下面的歌词是否就是「歌手 + 歌名」这首歌的歌词"
            "（允许歌词来自该歌曲的官方版本/现场/翻唱文本，内容主体一致即算匹配）。\n"
            "规则：\n"
            "1. 只判断歌词与歌曲是否匹配，不要提出修改歌手或歌名。\n"
            "2. 只输出一个 JSON 对象，不要任何额外文字。\n"
            "3. 字段固定为："
            'match(布尔，歌词是否属于这首歌)、confidence(0~1 小数，判断置信度)、'
            'reason(一句话中文说明依据)、'
            'actual(若判定不匹配，写出歌词实际出自的「歌手 - 歌名」，无法确定或判定匹配时填空串)。\n'
            f"歌手：{artist or '未知'}\n"
            f"歌名：{title or '未知'}\n"
            f"歌词（已去除时间码，可能截断）：\n{body}\n"
            '输出示例：{"match":true,"confidence":0.9,"reason":"歌词与《xxx》副歌一致","actual":""}'
        )
        content = self._chat(self._with_user(prompt, user_prompt), max_tokens=300, kind="analyze")
        if not content:
            return {}
        obj = self._parse_obj(content)
        if "match" not in obj:
            return {}
        try:
            conf = float(obj.get("confidence", 0))
        except (TypeError, ValueError):
            conf = 0.0
        return {
            "match": bool(obj.get("match")),
            "confidence": round(max(0.0, min(1.0, conf)), 2),
            "reason": str(obj.get("reason") or "").strip()[:200],
            "actual": str(obj.get("actual") or "").strip()[:120],
        }

    def cover_prompt(self, artist: str, title: str, lyric: str = "", style: str = "") -> str:
        """让 LLM 根据歌词大意与风格生成中文文生图画面描述（用于 AI 封面）。"""
        base = (
            "请为歌曲《{t}》（歌手：{a}{s}）设计一张音乐专辑封面的画面描述。"
        ).format(t=title, a=artist or "未知", s=("，风格：" + style) if style else "")
        if lyric:
            base += "以下是歌词节选，画面必须贴合歌词意境：\n" + "\n".join(lyric.splitlines()[:12]) + "\n"
        base += (
            "要求：正方形专辑封面构图，国风/与歌曲情绪相符的艺术风格，画面中必须包含歌名「{t}」四个"
            "汉字作为标题文字排版（醒目、居中或竖排均可），可有歌手名「{a}」小字。"
            "只输出一段中文画面描述（120字以内），不要解释、不要引号、不要 markdown。"
        ).format(t=title, a=artist or "")
        content = self._chat(base, max_tokens=220, temperature=0.8, kind="cover_prompt")
        text = (content or "").strip().strip('"').strip()
        return text[:300]

    def generate_image(self, prompt: str) -> bytes:
        """生图 + 记一次用量（成功失败都算一次：请求发出去就可能计费）。

        用包接口而不是在原来每个 return 点插计数：本函数有八个出口，
        散着插总会漏一个，漏的那次就隐形了。
        """
        img = self._generate_image_impl(prompt)
        if prompt and self.image_available:
            self._report("image", miss=1, ok=bool(img))
        return img

    def _generate_image_impl(self, prompt: str) -> bytes:
        """调用 OpenAI 兼容 /images/generations 接口生成图片，返回 bytes；失败返回 b''。

        响应解析兼容：data[0].url（下载）与 data[0].b64_json（直接解码）两种形式。
        """
        self.last_error = ""
        if not self.image_available or not prompt:
            return b""
        base = self.image_base or self.base_url
        ds_root = self._dashscope_root(base)
        if ds_root:
            # 阿里云百炼文生图不支持 OpenAI 兼容接口，需走原生协议
            return self._generate_image_dashscope(ds_root, prompt)
        if base.endswith("/images/generations"):
            url = base
        elif re.search(r"/v\d+$", base):
            url = base + "/images/generations"
        else:
            url = base + "/v1/images/generations"
        body = {"model": self.image_model, "prompt": prompt,
                "size": "1024x1024", "n": 1, "response_format": "b64_json"}
        req = urllib.request.Request(
            url, data=json.dumps(body).encode("utf-8"),
            headers={"Content-Type": "application/json",
                     "Authorization": f"Bearer {self.image_key}"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.image_timeout) as resp:
                data = json.loads(resp.read().decode("utf-8", errors="replace"))
        except urllib.error.HTTPError as e:
            detail = ""
            try:
                detail = e.read().decode("utf-8", errors="replace")[:160]
            except Exception:
                pass
            self.last_error = f"生图 HTTP {e.code} {detail}"
            print(f"[warn] {self.last_error}", file=sys.stderr)
            return b""
        except Exception as e:
            self.last_error = f"生图失败: {e}"
            print(f"[warn] {self.last_error}", file=sys.stderr)
            return b""
        import base64
        try:
            item = (data.get("data") or [{}])[0]
        except Exception:
            self.last_error = "生图响应格式异常"
            return b""
        if item.get("b64_json"):
            try:
                return base64.b64decode(item["b64_json"])
            except Exception:
                self.last_error = "生图 b64 解码失败"
                return b""
        if item.get("url"):
            try:
                with urllib.request.urlopen(item["url"], timeout=self.image_timeout) as r:
                    return r.read()
            except Exception as e:
                self.last_error = f"生图图片下载失败: {e}"
                return b""
        self.last_error = "生图响应缺少图片数据"
        return b""

    # ---- 阿里云百炼（DashScope）原生生图 ----

    def _dashscope_root(self, base: str) -> str:
        """判断生图地址是否属于 DashScope/百炼站点，是则返回站点根地址。

        百炼的 qwen-image / 通义万相均为原生协议（compatible-mode 不提供
        /images/generations），填了百炼地址时自动改走原生接口。
        """
        m = re.match(r"(https?://[^/]+)", (base or "").strip())
        if not m:
            return ""
        host = m.group(1)
        if "dashscope" in host or ".maas.aliyuncs.com" in host:
            return host
        return ""

    def _ds_request(self, url: str, body: dict, extra=None,
                    timeout: int = 30) -> dict:
        headers = {"Content-Type": "application/json",
                   "Authorization": "Bearer " + self.image_key}
        if extra:
            headers.update(extra)
        req = urllib.request.Request(url, data=json.dumps(body).encode("utf-8"),
                                     headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode("utf-8", errors="replace"))

    @staticmethod
    def _ds_read_err(e) -> str:
        try:
            return e.read().decode("utf-8", errors="replace")[:120]
        except Exception:
            return ""

    @staticmethod
    def _ds_resp_error(data: dict) -> str:
        return "{} {}".format(data.get("code") or "",
                              data.get("message") or "").strip()

    def _ds_download(self, img_url: str, errors: list, tag: str) -> bytes:
        try:
            with urllib.request.urlopen(
                    img_url, timeout=min(30, max(10, self.image_timeout))) as r:
                return r.read()
        except Exception as e:
            errors.append("{}图片下载失败: {}".format(tag, e))
            return b""

    def _ds_sync(self, root: str, model: str, prompt: str,
                 errors: list) -> bytes:
        """同步协议（wan2.6 / qwen-image）：一次请求直接返回图片 URL。"""
        body = {"model": model,
                "input": {"messages": [{"role": "user",
                                        "content": [{"text": prompt}]}]},
                "parameters": {"n": 1, "size": "1280*1280", "watermark": False}}
        try:
            data = self._ds_request(
                root + "/api/v1/services/aigc/multimodal-generation/generation",
                body, timeout=self.image_timeout)
        except urllib.error.HTTPError as e:
            errors.append("同步 HTTP {} {}".format(e.code, self._ds_read_err(e)))
            return b""
        except Exception as e:
            errors.append("同步请求失败: {}".format(e))
            return b""
        img_url = ""
        for ch in ((data.get("output") or {}).get("choices") or []):
            for c in ((ch.get("message") or {}).get("content") or []):
                if c.get("image"):
                    img_url = c["image"]
        if not img_url:
            errors.append("同步: " + (self._ds_resp_error(data) or "响应缺少图片"))
            return b""
        return self._ds_download(img_url, errors, "同步")

    def _ds_async(self, root: str, model: str, prompt: str,
                  errors: list) -> bytes:
        """异步任务协议（wanx2.x / wan2.2 / wan2.5）：创建任务后轮询结果。"""
        body = {"model": model, "input": {"prompt": prompt},
                "parameters": {"n": 1, "size": "1280*1280"}}
        try:
            data = self._ds_request(
                root + "/api/v1/services/aigc/text2image/image-synthesis",
                body, extra={"X-DashScope-Async": "enable"},
                timeout=min(20, max(5, self.image_timeout)))
        except urllib.error.HTTPError as e:
            errors.append("异步 HTTP {} {}".format(e.code, self._ds_read_err(e)))
            return b""
        except Exception as e:
            errors.append("异步创建任务失败: {}".format(e))
            return b""
        out = data.get("output") or {}
        task_id = out.get("task_id") or ""
        if not task_id:
            errors.append("异步: " + (self._ds_resp_error(data) or "响应缺少 task_id"))
            return b""
        import time
        deadline = time.time() + max(30, self.image_timeout)
        while time.time() < deadline:
            time.sleep(4)
            try:
                req = urllib.request.Request(
                    root + "/api/v1/tasks/" + task_id,
                    headers={"Authorization": "Bearer " + self.image_key})
                with urllib.request.urlopen(req, timeout=15) as resp:
                    st = json.loads(resp.read().decode("utf-8", errors="replace"))
            except Exception as e:
                errors.append("异步轮询失败: {}".format(e))
                return b""
            out = st.get("output") or {}
            status = out.get("task_status") or ""
            if status == "SUCCEEDED":
                results = out.get("results") or []
                img_url = results[0].get("url", "") if results else ""
                if not img_url:
                    errors.append("异步: 任务成功但无图片 URL")
                    return b""
                return self._ds_download(img_url, errors, "异步")
            if status in ("FAILED", "CANCELED", "UNKNOWN"):
                errors.append("异步: 任务 {} {} {}".format(
                    status, out.get("code") or "", out.get("message") or "").strip())
                return b""
        errors.append("异步: 任务轮询超时")
        return b""

    def _generate_image_dashscope(self, root: str, prompt: str) -> bytes:
        model = self.image_model or ""
        errors: list = []
        sync_first = model.startswith("wan2.6") or "qwen-image" in model
        calls = ([self._ds_sync, self._ds_async] if sync_first
                 else [self._ds_async, self._ds_sync])
        for fn in calls:
            img = fn(root, model, prompt, errors)
            if img:
                return img
        self.last_error = ("生图失败: " + "；".join(errors)) if errors else "生图失败"
        print(f"[warn] {self.last_error}", file=sys.stderr)
        return b""

    def _chat(self, prompt: str, max_tokens: int = 200, temperature: float = 0.1,
              kind: str = "chat") -> str:
        """发送一次对话请求，返回模型文本内容；失败返回空串。

        kind 是“这次调用用在哪”（识别文件名 / 生歌词 / 去重决策 …），只用于用量统计；
        不传也不报错，退化成 chat，免得新增调用点漏传时统计里出现空类目。
        """
        body = {
            "model": self.model,
            "messages": [
                {"role": "system", "content": "你是音乐元数据整理助手，只输出要求的内容。"},
                {"role": "user", "content": prompt},
            ],
            "temperature": temperature,
            "max_tokens": max_tokens,
        }
        req = urllib.request.Request(
            self._endpoint(),
            data=json.dumps(body).encode("utf-8"),
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {self.api_key}",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read().decode("utf-8", errors="replace")
            data = json.loads(raw)
            content = data["choices"][0]["message"]["content"]
            u = data.get("usage") if isinstance(data, dict) else None
            pt = int((u or {}).get("prompt_tokens") or 0)
            ct = int((u or {}).get("completion_tokens") or 0)
            tt = int((u or {}).get("total_tokens") or 0) or (pt + ct)
            # 兼容接口常不返回 usage：miss 单独记一笔，不能把“没回明细”当成“没花钱”
            self._report(kind, pt, ct, tt, ok=True, miss=0 if u else 1)
            return content
        except Exception:
            self._report(kind, ok=False)
            return ""

    def _report(self, kind, pt=0, ct=0, tt=0, ok=True, miss=0):
        """把本次调用用量交给引擎记账；回调方抛任何异常都不能影响 AI 本身。"""
        cb = getattr(self, "_on_usage", None)
        if cb is None:
            return
        try:
            cb(str(kind or "other"), int(pt or 0), int(ct or 0), int(tt or 0), bool(ok), int(miss or 0))
        except Exception:
            pass

    @staticmethod
    def _parse_obj(content: str) -> dict:
        """通用 JSON 提取（保留模型返回的全部字段），供去重决策等使用。"""
        if not content:
            return {}
        text = content.strip()
        m = re.search(r"```(?:json)?\s*(.*?)```", text, re.S)
        if m:
            text = m.group(1).strip()
        try:
            obj = json.loads(text)
        except Exception:
            m2 = re.search(r"\{.*\}", text, re.S)
            if not m2:
                return {}
            try:
                obj = json.loads(m2.group(0))
            except Exception:
                return {}
        return obj if isinstance(obj, dict) else {}

    @staticmethod
    def _parse_json(content: str) -> dict:
        """从模型输出中提取 JSON（可能被 markdown 代码块包裹）。"""
        if not content:
            return {}
        content = content.strip()
        # 去掉 ```json ... ``` 包裹
        m = re.search(r"```(?:json)?\s*(.*?)```", content, re.S)
        if m:
            content = m.group(1).strip()
        # 提取第一个 { ... } 块
        try:
            obj = json.loads(content)
        except Exception:
            m2 = re.search(r"\{.*\}", content, re.S)
            if not m2:
                return {}
            try:
                obj = json.loads(m2.group(0))
            except Exception:
                return {}
        if not isinstance(obj, dict):
            return {}
        return {
            "artist": str(obj.get("artist") or "").strip(),
            "title": str(obj.get("title") or "").strip(),
            "style": str(obj.get("style") or "").strip(),
        }
