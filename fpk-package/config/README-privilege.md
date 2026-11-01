为什么曲率仍以 root 运行（而不是官方建议的 run-as: package）
=============================================================

飞牛文档 /docs/core-concepts/privilege/ 的建议是：默认 `run-as: package`，
用专用 username/groupname，只在确有必要时 `join-groups`，并且避免长期 root。

曲率不适用这条建议，原因是**接管能力本身就要特权**：

  backend/pkg/takeover 与 pkg/intercept 要 rename / 重建
  /var/run/trim_music.socket（飞牛音乐应用的私有 socket），
  才能把 /music/api/v1/* 的请求拦下来合并进曲率自己的曲库与歌单。
  这个 socket 属于飞牛音乐、位于 /var/run 下，普通应用用户没有 rename 权限。
  一旦改成 run-as: package，「接管飞牛音乐页面 / 拦截官方接口」整套能力立即失效。

代价与补偿
----------
root 运行意味着**进程在操作系统层面能读一切**。飞牛的官方权限模型要求
「应用默认看不到用户文件，必须由用户显式授权目录」，这个约束在 root 下不会由
系统自动强加给我们，所以曲率**自己在路径守卫里实现它**：

  AllowedRoots() = 管理员共享授权 ∪ 当前用户个人授权 ∪ TRIM_DATA_ACCESSIBLE_PATHS
                   ∪ (FN_ALLOWED_ROOTS 或 卷探测)

见 backend/pkg/nas/pathguard.go 与 backend/pkg/fnos/authpaths.go。
也就是说：**能读 ≠ 会读** —— 只有当某个目录被用户/管理员在飞牛里授权给本应用，
它才会进入允许根目录；否则 /api/nas/* 一律 ErrPathForbidden。

不要做的事
----------
* 不要把 `config/privilege` 改成 `package` —— 会静默废掉接管功能（用户只会看到
  「曲率没有替换飞牛音乐界面」，不会看到任何报错）。
* 不要在生命周期脚本或向导里写 `FN_ALLOWED_ROOTS` —— 该变量一旦设置就**完全覆盖**
  内置根目录，等于绕过用户授权意图。要多认一个目录，请让用户去「授权目录」。
* 不要用 `chmod`/`chown` 去"解决"权限问题 —— 用户不需要改任何文件权限位。
