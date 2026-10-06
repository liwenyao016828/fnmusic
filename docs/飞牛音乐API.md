# 飞牛音乐 (fnOS Music) API 完整技术文档

> **说明**：本文档整理自飞牛 NAS 原生“飞牛音乐”应用 (`/usr/local/apps/@appcenter/trim.music/trim-music`) 的二进制解析与本地实测 API，供 AI 或开发者直接阅读与实现对接。

---

## 1. 基础架构与连接方式

- **服务进程**: `trim-music` (`/usr/local/apps/@appcenter/trim.music/trim-music`)
- **通信接口**: 本地 Unix Domain Socket（非网络端口）
- **Socket 路径**: `/var/run/trim_music.socket`
- **HTTP 虚拟基地址**: `http://localhost/music/api/v1`
- **认证机制**: HTTP Header `Authorization: <token>`
- **Token 来源**: 从飞牛音乐本地 SQLite 数据库中只读获取
  - 数据库路径：**`/var/apps/trim.music/var/db/music.db`**
    - ⚠️ **勘误（2026-09-17）**：本文档此前写的是 `/var/lib/fnos-music-db/music.db`
      或 `/usr/local/apps/@appdata/trim.music/db/music.db`，**两个都是错的**。
      照着实现会导致真机上取不到令牌 → `POST /api/fnos/check` 回 503「飞牛音乐连接不上」。
    - 正确路径的来源：解包官方安装包 `trim.music-1.0.1.fpk`，在 `app/trim-music`
      二进制里 `music.db` **只出现一次**，即上述路径；与官方 `cmd/common` 的
      `check_dir()` 引用的 `${TRIM_PKGVAR}/db` 一致（`TRIM_PKGVAR=/var/apps/trim.music/var`）。
    - `trim.music` 进程以 `root` 运行（`config/privilege` 的 `run-as: root`），
      因此同权限的应用可以直接只读打开。
  - 提取语句：`SELECT token FROM user_token WHERE token IS NOT NULL AND token != '' ORDER BY id DESC LIMIT 1;`
- **统一响应格式**:
```json
{
  "code": 0,      // 0 表示成功，非 0 表示失败
  "msg": "",      // 错误提示信息
  "data": { ... } // 返回数据对象或数组
}
```

---

## 2. API 接口详解

### 2.1 认证与用户

#### GET `/user/me` - 获取当前登录用户信息
- **Headers**: `Authorization: <token>`
- **Response**:
```json
{
  "code": 0,
  "msg": "",
  "data": {
    "guid": "9db86e47dc0d4fa8a1bfaac1dabac9f5",
    "name": "liwenyao",
    "role": "admin",
    "createdAt": 1787923813,
    "updatedAt": 1789627505
  }
}
```

---

### 2.2 歌单管理

#### GET `/playlist/list` - 获取所有歌单
- **Response**:
```json
{
  "code": 0,
  "msg": "",
  "data": {
    "list": [
      {
        "guid": "bea03d61520c4780b700ea4976549330",
        "name": "歌单名字",
        "coverId": "playlist_c75a5d14d02945fd89369546407bd055",
        "createdAt": 1789567301,
        "updatedAt": 1789567311
      }
    ],
    "total": 1
  }
}
```

#### POST `/playlist/create` - 创建新歌单
- **Body** (JSON):
```json
{
  "name": "我的新歌单",
  "visibility": 1,
  "description": "歌单描述文本"
}
```
- **Response**: 返回含 `guid` 的对象。

#### POST `/playlist/edit` - 编辑歌单基本信息/关联封面
- **Body** (JSON):
```json
{
  "guid": "bea03d61520c4780b700ea4976549330",
  "name": "更新后的歌单名",
  "description": "更新后的描述",
  "coverId": "playlist_c75a5d14d02945fd89369546407bd055"
}
```

#### GET `/track/playlist-detail/list` - 分页获取歌单内部歌曲
- **Params**:
  - `playlistGUID`: 歌单 GUID (必填)
  - `page`: 页码，1 开始 (默认 1)
  - `pageSize`: 每页数量 (默认 50)
- **Response**:
```json
{
  "code": 0,
  "msg": "",
  "data": {
    "list": [
      {
        "guid": "161c6ba15a26492c82bb9b7507c399ad",
        "title": "歌曲标题",
        "coverId": "album_1cd0ae031e544297b8fb168b6ca1e66d",
        "duration": 320405,
        "createdAt": 1789230000
      }
    ],
    "total": 100
  }
}
```

#### POST `/playlist/add-track` - 批量添加歌曲到歌单
- **Body** (JSON):
```json
{
  "guid": "bea03d61520c4780b700ea4976549330",
  "trackGUIDs": [
    "854bb6e7aa2f4910bcb3eab551687205",
    "161c6ba15a26492c82bb9b7507c399ad"
  ]
}
```
*注：建议单次提交不超过 50 首 GUID。*

#### POST `/playlist/remove-track` - 从歌单移除歌曲
- **Body** (JSON):
```json
{
  "guid": "bea03d61520c4780b700ea4976549330",
  "trackGUIDs": ["854bb6e7aa2f4910bcb3eab551687205"]
}
```

---

### 2.3 检索与匹配

#### GET `/search/track` - 搜索曲库内的歌曲
- **Params**: `q`: 搜索关键词（如 `周杰伦` 或 `周杰伦 七里香`）
- **Response**:
```json
{
  "code": 0,
  "msg": "",
  "data": {
    "list": [
      {
        "guid": "854bb6e7aa2f4910bcb3eab551687205",
        "title": "周杰伦 - 七里香",
        "coverId": null,
        "duration": 141971
      }
    ]
  }
}
```

#### 其他搜索端点：
- GET `/search/album?q=...` : 搜索专辑
- GET `/search/artist?q=...` : 搜索歌手
- GET `/search/playlist?q=...` : 搜索公共歌单

---

### 2.4 媒体库管理与刷新

#### POST `/shared-library/scan-all` - 全量扫描并刷新媒体库
- **Response**:
```json
{
  "code": 0,
  "msg": "",
  "data": null
}
```

---

### 2.5 封面与资源处理

#### POST `/static/cover/playlist` - 上传歌单封面文件
- **Content-Type**: `multipart/form-data`
- **Form Data**: `file` (图片文件二进制，支持 png / jpg / webp)
- **Response**:
```json
{
  "code": 0,
  "msg": "",
  "data": {
    "coverId": "playlist_c75a5d14d02945fd89369546407bd055"
  }
}
```
*说明：获得 `coverId` 后，需调用 `/playlist/edit` 将其绑定到对应歌单。*

---

## 3. Python 核心对接参考实现 (基于 httpx)

```python
import asyncio
import sqlite3
from typing import Any, List, Optional
import httpx

class FnosMusicClient:
    """飞牛音乐 Unix Socket API 客户端"""

    def __init__(
        self,
        # 真实路径，见本文档 §1 的勘误说明
        db_path: str = "/var/apps/trim.music/var/db/music.db",
        socket_path: str = "/var/run/trim_music.socket",
    ):
        self.db_path = db_path
        self.socket_path = socket_path
        self.base_url = "http://localhost/music/api/v1"
        self.token = self._get_token_from_db()

    def _get_token_from_db(self) -> str:
        """从 SQLite 数据库提取最新有效 Token"""
        try:
            uri = f"file:{self.db_path}?mode=ro"
            with sqlite3.connect(uri, uri=True) as conn:
                cursor = conn.cursor()
                cursor.execute(
                    "SELECT token FROM user_token WHERE token IS NOT NULL AND token != '' ORDER BY id DESC LIMIT 1"
                )
                row = cursor.fetchone()
                if row:
                    return str(row[0])
        except Exception as e:
            print(f"[Warning] Failed to fetch token from DB: {e}")
        return ""

    async def _request(
        self, method: str, path: str, json_data: Optional[dict] = None, params: Optional[dict] = None
    ) -> Any:
        if not self.token:
            self.token = self._get_token_from_db()

        transport = httpx.AsyncHTTPTransport(uds=self.socket_path)
        headers = {"Authorization": self.token}

        async with httpx.AsyncClient(transport=transport, timeout=30.0) as client:
            url = f"{self.base_url}/{path.lstrip('/')}"
            resp = await client.request(method, url, headers=headers, json=json_data, params=params)
            
            # Token 失效处理
            if resp.status_code in (401, 403):
                self.token = self._get_token_from_db()
                headers["Authorization"] = self.token
                resp = await client.request(method, url, headers=headers, json=json_data, params=params)

            res = resp.json()
            if res.get("code") != 0:
                raise RuntimeError(f"fnOS Music API Error: {res.get('msg')}")
            return res.get("data")

    # ---- 常用功能封装 ----

    async def search_track(self, keyword: str) -> List[dict]:
        """搜索歌曲"""
        data = await self._request("GET", "/search/track", params={"q": keyword})
        return data.get("list", []) if isinstance(data, dict) else []

    async def list_playlists(self) -> List[dict]:
        """获取所有歌单"""
        data = await self._request("GET", "/playlist/list")
        return data.get("list", []) if isinstance(data, dict) else []

    async def create_playlist(self, name: str, description: str = "") -> dict:
        """创建歌单"""
        return await self._request("POST", "/playlist/create", json_data={"name": name, "visibility": 1, "description": description})

    async def add_tracks_to_playlist(self, playlist_guid: str, track_guids: List[str]) -> None:
        """批量添加歌曲到歌单"""
        # 分批处理，每批最多 50 首
        for i in range(0, len(track_guids), 50):
            batch = track_guids[i : i + 50]
            await self._request("POST", "/playlist/add-track", json_data={"guid": playlist_guid, "trackGUIDs": batch})

    async def trigger_library_scan(self) -> None:
        """触发曲库全量扫描"""
        await self._request("POST", "/shared-library/scan-all")


# ---- 使用示例 ----
async def main():
    client = FnosMusicClient()
    
    # 1. 搜索本地歌曲
    tracks = await client.search_track("晴天")
    print(f"找到 {len(tracks)} 首匹配歌曲")
    
    if tracks:
        track_guid = tracks[0]["guid"]
        
        # 2. 创建新歌单
        playlist = await client.create_playlist(name="AI 下载推荐", description="自动推送到飞牛音乐")
        playlist_guid = playlist.get("guid")
        
        # 3. 将歌曲加入歌单
        await client.add_tracks_to_playlist(playlist_guid, [track_guid])
        print(f"成功将歌曲添加至歌单: {playlist_guid}")

if __name__ == "__main__":
    asyncio.run(main())
```

---

## 4. 对接最佳实践指南

1. **自动 Token 获取**: 建议无需在 UI 要求用户手动输入 Token，应用在 NAS 本地以 root 或同权限用户运行时，直接只读读取 `/var/apps/trim.music/var/db/music.db` 的 `user_token` 表，即可实现**零配置极速免登录对接**。（路径务必用这个，别用旧文档里的 `/var/lib/fnos-music-db/...`，见 §1 勘误。）
2. **下载落盘与自动入库**：下载外部音乐文件到 NAS 绑定的音乐共享文件夹后，调用 `POST /shared-library/scan-all` 触发飞牛音乐刷新。
3. **第三方歌单同步流程**：
   - 解析第三方歌单（网易云/QQ音乐）列表；
   - 逐首调用 `GET /search/track?q=<歌手> <歌名>` 匹配本地曲库，拿到的 `guid` 存在本地；
   - 调用 `POST /playlist/create` 创建同名歌单，拿到 `playlistGUID`；
   - 批量调用 `POST /playlist/add-track` 将匹配到的 `guid` 追加至飞牛歌单；
   - 如有封面，用 `POST /static/cover/playlist` 上传，拿 `coverId` 后调用 `POST /playlist/edit` 更新歌单封面。
