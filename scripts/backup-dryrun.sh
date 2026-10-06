#!/bin/bash
# ==============================================================================
#  曲率 —— 卸载/升级前数据备份（fpk-package/cmd/_backup）的本地干跑验证
#
#  为什么要有这个文件：这套逻辑的失败模式是「静默丢数据」，而它只在真机卸载/
#  升级的那一刻才跑到 —— NAS 上装不了包就等于验不了。所以把它拆成可重复的
#  本地断言：造一个假数据目录（含 0600 假凭据、含各种缓存），跑真脚本，断言
#  归档里**有**该有的、**没有**缓存、权限没变、命名与保留策略生效、失败路径
#  确实不阻塞退出。
#
#  跑法：bash scripts/backup-dryrun.sh          （全部用例）
#        bash scripts/backup-dryrun.sh -v       （带每一步命令行回显）
#
#  不碰宿主任何真实路径：全部活动都在 mktemp -d 出来的沙箱里。唯一例外是
#  「卷根推导」用例 —— 它用 PATH 桩替换 readlink（uid!=0 时建不出真的 /vol{n}
#  目录），桩只影响那几条用例。
#
#  ⚠️ 本脚本刻意**不用 $(...) 收集被测脚本的输出**，而是让它写进文件再读：
#     被测脚本里可能有后台进程继承 stdout，用命令替换会被那些 fd 拖住
#     （这个坑本地踩过 —— 表现是整个测试挂在用例 2 上不动）。写文件则任何 fd
#     都不影响测试继续跑。
#
#  这套用例已经抓到过三个真 bug，都在 _backup 的注释里留了说明：
#    ① 看门狗版限时在成功路径上留孤儿 sleep 攥着 stdout → 卸载要多等一整个限时
#    ② 超时只杀最外层 pid → tar/gzip 变孤儿继续跑，限时形同虚设
#    ③ 归档复核用了裸 `tar -tzf` → tar 卡死时验证步骤自己挂住
# ==============================================================================
set -u

VERBOSE=0
[ "${1:-}" = "-v" ] && VERBOSE=1

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CMD_DIR="$(cd "${HERE}/../fpk-package/cmd" && pwd)"
SANDBOX="$(mktemp -d)"
PASS=0
FAIL=0
RC=0
LAST_OUT=""
LAST_ERR=""

cleanup() { rm -rf "${SANDBOX}"; }
trap cleanup EXIT

ok()    { PASS=$((PASS + 1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()   { FAIL=$((FAIL + 1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
head2() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
show()  { [ "${VERBOSE}" = 1 ] && printf '  $ %s\n' "$*"; return 0; }
dump()  { printf '%s' "$1" | sed 's/^/    | /'; printf '\n'; }

assert_eq() {
    if [ "$2" = "$3" ]; then ok "$1"; else bad "$1（实际='$2' 期望='$3'）"; fi
}
assert_true() {
    local desc="$1"; shift
    if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}
assert_false() {
    local desc="$1"; shift
    if "$@" >/dev/null 2>&1; then bad "$desc"; else ok "$desc"; fi
}
# contains <描述> <文本> <正则>
contains()     { if printf '%s' "$2" | grep -qE "$3"; then ok "$1"; else bad "$1"; fi; }
not_contains() {
    if printf '%s' "$2" | grep -qE "$3"; then
        bad "$1（出现了：$(printf '%s' "$2" | grep -oE "$3" | head -1)）"
    else
        ok "$1"
    fi
}

archive_of()    { ls -1 "$1"/yinshu-ai-backup-*.tar.gz 2>/dev/null | head -1; }
count_archives() { ls -1 "$1"/yinshu-ai-backup-[0-9]*.tar.gz 2>/dev/null | wc -l; }
count_parts()   { ls -1 "$1"/*.part 2>/dev/null | wc -l; }

# ── 造一个「真机长什么样」的假数据目录 ───────────────────────────────────────
# 结构照着代码里真实会写进去的东西铺（逐项理由见 _backup 里那两份清单的注释）：
#   数据：config.json / ui_prefs.json / accounts.json(0600) / ai_config.json(0600)
#         name_rules.json / monitors.json / push_tasks.json / download_queue.json
#         lx_sources/（用户音源脚本）
#         online/{favorites,playlist_tracks,play_history,downloaded.json}
#   .env       数据目录根下、用户手写的开关
#   缓存：sidecar/（venv+work）/ ai_backup/（音频整份拷贝）/ library_index.json
#         scan_cache.json / fnos_rescan.json / online/covers/ / music/
make_fake_app() {
    local var_dir="$1"
    mkdir -p "${var_dir}/data/online/favorites" \
             "${var_dir}/data/online/playlist_tracks" \
             "${var_dir}/data/online/play_history" \
             "${var_dir}/data/online/covers" \
             "${var_dir}/data/lx_sources" \
             "${var_dir}/data/sidecar/venv/bin" \
             "${var_dir}/data/sidecar/work" \
             "${var_dir}/data/ai_backup/files" \
             "${var_dir}/data/music/某专辑"

    printf '{"port":8898}\n'                             > "${var_dir}/data/config.json"
    printf '{"theme":"fresh-mint"}\n'                    > "${var_dir}/data/ui_prefs.json"
    printf '{"netease":{"cookie":"FAKE-COOKIE-0600"}}\n' > "${var_dir}/data/accounts.json"
    printf '{"api_key":"FAKE-AI-KEY-0600"}\n'            > "${var_dir}/data/ai_config.json"
    printf '{"rules":[]}\n'                              > "${var_dir}/data/name_rules.json"
    printf '{"monitors":[]}\n'                           > "${var_dir}/data/monitors.json"
    printf '{"tasks":[]}\n'                              > "${var_dir}/data/push_tasks.json"
    printf '{"queue":[]}\n'                              > "${var_dir}/data/download_queue.json"
    printf '{"items":[]}\n'   > "${var_dir}/data/online/favorites/fp-485d391520ecf02c.json"
    printf '{"items":{}}\n'   > "${var_dir}/data/online/playlist_tracks/fp-485d391520ecf02c.json"
    printf '{"items":[]}\n'   > "${var_dir}/data/online/play_history/fp-485d391520ecf02c.json"
    printf '{"items":{}}\n'   > "${var_dir}/data/online/downloaded.json"
    printf '// 用户导入的音源脚本\n' > "${var_dir}/data/lx_sources/user_source.js"
    printf 'QULV_TAKEOVER=1\n'        > "${var_dir}/.env"

    # 缓存侧（一个都不该进归档）
    printf 'cache\n'            > "${var_dir}/data/online/covers/fp-485d391520ecf02c"
    printf '{}'                 > "${var_dir}/data/library_index.json"
    printf '{}'                 > "${var_dir}/data/scan_cache.json"
    printf '{}'                 > "${var_dir}/data/fnos_rescan.json"
    printf 'x\n'                > "${var_dir}/data/sidecar/.deps-ok"
    printf 'x\n'                > "${var_dir}/data/sidecar/work/tmp.json"
    printf '#!/bin/sh\n'        > "${var_dir}/data/sidecar/venv/bin/python3"
    printf '{"entries":[]}\n'   > "${var_dir}/data/ai_backup/manifest.json"
    printf 'FAKE-AUDIO-BYTES\n' > "${var_dir}/data/ai_backup/files/0001_song.mp3"
    printf 'FAKE-AUDIO-BYTES\n' > "${var_dir}/data/music/某专辑/歌.mp3"

    # 凭据/密钥必须 0600（真机上 account/store.go 与 features.go 是这么落的）
    chmod 600 "${var_dir}/data/accounts.json" "${var_dir}/data/ai_config.json"
    chmod 644 "${var_dir}/data/config.json"
}

# run_phase <阶段脚本> <var_dir> <backup_root> [额外 env 赋值...]
# 结果写进 RC / LAST_OUT / LAST_ERR
run_phase() {
    local script="$1" var_dir="$2" root="$3"
    shift 3
    show "TRIM_PKGVAR=${var_dir} QULV_BACKUP_DIR=${root} ${script} $*"
    env -i PATH="/usr/bin:/bin" HOME="${SANDBOX}" \
        TRIM_PKGVAR="${var_dir}" \
        QULV_BACKUP_DIR="${root}" \
        "$@" \
        bash "${CMD_DIR}/${script}" \
        > "${SANDBOX}/.stdout" 2> "${SANDBOX}/.stderr"
    RC=$?
    LAST_OUT="$(cat "${SANDBOX}/.stdout")"
    LAST_ERR="$(cat "${SANDBOX}/.stderr")"
}

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 1：卸载路径 —— 成功打包（有该有的 / 没缓存 / 权限没变 / 命名 / 输出契约）"

VAR1="${SANDBOX}/vol1/@appdata/yinshu-ai"
ROOT1="${SANDBOX}/vol1"
mkdir -p "${ROOT1}"
make_fake_app "${VAR1}"

T0="$(date +%s)"
run_phase uninstall_init "${VAR1}" "${ROOT1}"
T1="$(date +%s)"
echo "  --- 脚本 stderr（= 日志）---"; dump "${LAST_ERR}"
echo "  --- 脚本 stdout（应只有归档路径一行）---"; dump "${LAST_OUT}"

assert_eq "卸载脚本退出码为 0" "${RC}" "0"
# 回归钉子：第一版用「另起 sleep 看门狗」实现限时，成功路径上那只 sleep 会变成
# 孤儿攥着 stdout，让调用方一直等到限时结束（120s）。这条断言就是钉住它。
assert_true "备份很快返回（$((T1 - T0))s，远小于 120s 限时）" test "$((T1 - T0))" -lt 30

ARCH1="$(archive_of "${ROOT1}")"
assert_true "归档已生成 ${ARCH1}" test -f "${ARCH1}"
assert_eq "stdout 契约：只有归档路径一行" "${LAST_OUT}" "${ARCH1}"
assert_eq "没留下 .part 半成品" "$(count_parts "${ROOT1}")" "0"
NAME1="$(basename "${ARCH1}")"
assert_true "命名符合 yinshu-ai-backup-<14位时间戳>.tar.gz（${NAME1}）" \
    bash -c "printf '%s' '${NAME1}' | grep -qE '^yinshu-ai-backup-[0-9]{14}\.tar\.gz$'"
assert_false "归档落在数据目录之外（卸载删数据目录也带不走它）" \
    bash -c "printf '%s' '${ARCH1}' | grep -q '^${VAR1}/'"

LIST1="${SANDBOX}/list1.txt"
tar -tzf "${ARCH1}" > "${LIST1}" 2>&1
echo "  --- 归档成员 ---"; sed 's/^/    | /' "${LIST1}"

echo "  --- 该有的（数据）---"
for want in \
    '^\.env$' \
    '^data/config\.json$' \
    '^data/ui_prefs\.json$' \
    '^data/accounts\.json$' \
    '^data/ai_config\.json$' \
    '^data/name_rules\.json$' \
    '^data/monitors\.json$' \
    '^data/push_tasks\.json$' \
    '^data/download_queue\.json$' \
    '^data/lx_sources/user_source\.js$' \
    '^data/online/downloaded\.json$' \
    '^data/online/favorites/fp-485d391520ecf02c\.json$' \
    '^data/online/playlist_tracks/fp-485d391520ecf02c\.json$' \
    '^data/online/play_history/fp-485d391520ecf02c\.json$'
do
    assert_true "归档含 ${want}" grep -qE "${want}" "${LIST1}"
done

echo "  --- 不该有的（缓存）---"
for never in \
    'data/sidecar' \
    'data/ai_backup' \
    'data/library_index\.json' \
    'data/scan_cache\.json' \
    'data/fnos_rescan\.json' \
    'data/online/covers' \
    'data/music' \
    '\.mp3$'
do
    assert_false "归档不含 ${never}" grep -qE "${never}" "${LIST1}"
done

echo "  --- 权限 ---"
PERMS1="$(tar -tvzf "${ARCH1}" 2>&1)"
assert_true "归档里 accounts.json 记录为 0600" \
    bash -c "printf '%s' '${PERMS1}' | grep -qE '^-rw------- .*data/accounts\.json$'"
assert_true "归档里 ai_config.json 记录为 0600" \
    bash -c "printf '%s' '${PERMS1}' | grep -qE '^-rw------- .*data/ai_config\.json$'"
assert_true "归档里 config.json 记录为 0644" \
    bash -c "printf '%s' '${PERMS1}' | grep -qE '^-rw-r--r-- .*data/config\.json$'"

# 真解开一遍，确认还原后权限位没变（真机上是 root 身份解，本地是同 uid 解）
EXTRACT="${SANDBOX}/extract1"
mkdir -p "${EXTRACT}"
( umask 022; tar -xzf "${ARCH1}" -C "${EXTRACT}" ) 2>/dev/null
assert_eq "还原后 accounts.json 权限 = 600" "$(stat -c '%a' "${EXTRACT}/data/accounts.json")" "600"
assert_eq "还原后 config.json 权限 = 644"  "$(stat -c '%a' "${EXTRACT}/data/config.json")" "644"
assert_eq "还原后 accounts.json 内容一致" \
    "$(cat "${EXTRACT}/data/accounts.json")" "$(cat "${VAR1}/data/accounts.json")"
assert_eq "还原后 .env 落在数据目录根（tar -C 到 VAR_DIR 即原位）" \
    "$(cat "${EXTRACT}/.env")" "QULV_TAKEOVER=1"

contains "stderr 里给了还原命令" "${LAST_ERR}" '还原方式：先停掉曲率'
# 钉子：stderr 里不许有 shell 噪声。这类事故的形态是「sourced 文件里一行注释
# 丢了开头的 #」—— 它**不报语法错**（`sh -n` 也过），只在运行时打一行
# command not found，而本脚本的输出全在 stderr，混在日志里极容易被忽略。
# 这个坑本地真踩过（_backup 里 qulv_vol_root_of 上面那行注释）。
for needle in 'command not found' 'syntax error' 'unexpected token' 'not found'; do
    not_contains "stderr 里没有 shell 噪声：${needle}" "${LAST_ERR}" "${needle}"
done
assert_true "日志文件写在数据目录里" test -f "${VAR1}/qulv-backup.log"
echo "    --- 备份日志文件 ---"; sed 's/^/    | /' "${VAR1}/qulv-backup.log"

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 2：升级路径 —— 同一套逻辑，与卸载共用同一个轮转池"

sleep 1   # 让时间戳不同，便于观察份数
T0="$(date +%s)"
run_phase upgrade_init "${VAR1}" "${ROOT1}"
T1="$(date +%s)"
dump "${LAST_ERR}"
assert_eq "升级脚本退出码为 0" "${RC}" "0"
assert_true "升级路径同样很快返回（$((T1 - T0))s）" test "$((T1 - T0))" -lt 30
assert_eq "升级也产出了归档（卸载+升级共 2 份）" "$(count_archives "${ROOT1}")" "2"
LIST2="${SANDBOX}/list2.txt"
tar -tzf "$(ls -1t "${ROOT1}"/yinshu-ai-backup-*.tar.gz | head -1)" > "${LIST2}" 2>&1
assert_true "升级归档同样含 .env" grep -qE '^\.env$' "${LIST2}"
assert_true "升级归档同样含在线收藏" grep -qE '^data/online/favorites/' "${LIST2}"
assert_false "升级归档同样不含 venv" grep -qE 'data/sidecar' "${LIST2}"

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 3：保留策略 —— 只留最近 N 份（默认 5），且绝不误删别的文件"

# 用一个**干净**的落点，别复用 ROOT1 —— 那里有用例 1/2 留下的归档，份数算术会
# 跟着上面用例的增减漂移（这条本地错过一次，就是那么错的）。
ROOT3="${SANDBOX}/vol3-rotate"; mkdir -p "${ROOT3}"
# 8 份历史归档 + 一个「手工 cp 出来的副本」（名字不合规）+ 别的应用的备份 + 无关文件
for n in 01 02 03 04 05 06 07 08; do
    printf 'old\n' | gzip -c > "${ROOT3}/yinshu-ai-backup-202001010101${n}.tar.gz"
done
printf 'mine\n'  > "${ROOT3}/yinshu-ai-backup-20200101010108.tar.gz.bak"
printf 'other\n' > "${ROOT3}/fnmusic-ext-backup-20200101010101.tar.gz"
printf 'other\n' > "${ROOT3}/unrelated-file.txt"
SNAPSHOT_BEFORE="$(ls -1 "${ROOT3}" | grep -vE '^yinshu-ai-backup-[0-9]{14}\.tar\.gz$' | sort)"

run_phase uninstall_init "${VAR1}" "${ROOT3}"
assert_eq "退出码 0" "${RC}" "0"
assert_eq "8 份旧的 + 1 份新的 = 9，轮转后只剩 5 份" "$(count_archives "${ROOT3}")" "5"
NEWEST="$(basename "$(ls -1t "${ROOT3}"/yinshu-ai-backup-*.tar.gz | head -1)")"
assert_true "刚写的那份还在（${NEWEST}）" test -f "${ROOT3}/${NEWEST}"
echo "    --- 轮转后剩下的归档（应为刚写的 + …0108…0104）---"
ls -1 "${ROOT3}"/yinshu-ai-backup-[0-9]*.tar.gz | sed 's/^/    | /'
# 伪造的名字是 yinshu-ai-backup-202001010101<NN>.tar.gz（NN = 01..08）。
# 9 份归档（8 份伪造 + 1 份刚写的）按名字倒序留 5 个 → 刚写的 + 08/07/06/05，
# 删掉 04/03/02/01（共 4 份）。
for keep in 08 07 06 05; do
    assert_true "保留伪造的 …202001010101${keep}" \
        bash -c "ls -1 '${ROOT3}'/yinshu-ai-backup-[0-9]*.tar.gz | grep -q '202001010101${keep}\.tar\.gz'"
done
for gone in 04 03 02 01; do
    assert_false "删掉更旧的 …202001010101${gone}" \
        bash -c "ls -1 '${ROOT3}'/yinshu-ai-backup-[0-9]*.tar.gz | grep -q '202001010101${gone}\.tar\.gz'"
done
assert_true "不合规命名的副本没被碰（.tar.gz.bak）" \
    test -f "${ROOT3}/yinshu-ai-backup-20200101010108.tar.gz.bak"
assert_true "别的应用的备份没被碰（fnmusic-ext）" \
    test -f "${ROOT3}/fnmusic-ext-backup-20200101010101.tar.gz"
assert_true "无关文件没被碰（unrelated-file.txt）" test -f "${ROOT3}/unrelated-file.txt"
assert_eq "除归档以外，目录里其他文件一个没变" \
    "$(ls -1 "${ROOT3}" | grep -vE '^yinshu-ai-backup-[0-9]{14}\.tar\.gz$' | sort)" "${SNAPSHOT_BEFORE}"
contains "轮转动作记进了日志" "${LAST_ERR}" '轮转：删除更旧的备份'
assert_eq "轮转日志正好 4 条（删掉 4 份）" \
    "$(printf '%s' "${LAST_ERR}" | grep -c '轮转：删除更旧的备份')" "4"

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 4：失败路径 —— 打包失败 / 落点不可写 / 数据目录不存在，都不阻塞退出"

# 4a. tar 失败（PATH 桩让 tar 报错退出）
STUBDIR="${SANDBOX}/bin-failtar"
mkdir -p "${STUBDIR}"
for t in dirname gzip date readlink sort sed; do ln -s "/usr/bin/$t" "${STUBDIR}/$t"; done
printf '#!/bin/bash\necho "tar: 故意失败" >&2\nexit 2\n' > "${STUBDIR}/tar"
chmod +x "${STUBDIR}/tar"
ROOT4="${SANDBOX}/vol4"; mkdir -p "${ROOT4}"
show "PATH-桩 tar 恒失败 → uninstall_init"
env -i PATH="${STUBDIR}:/usr/bin:/bin" HOME="${SANDBOX}" TRIM_PKGVAR="${VAR1}" \
    QULV_BACKUP_DIR="${ROOT4}" bash "${CMD_DIR}/uninstall_init" \
    > "${SANDBOX}/.stdout" 2> "${SANDBOX}/.stderr"
RC=$?; LAST_OUT="$(cat "${SANDBOX}/.stdout")"; LAST_ERR="$(cat "${SANDBOX}/.stderr")"
dump "${LAST_ERR}"
assert_eq "tar 失败时卸载仍然 exit 0（不阻塞）" "${RC}" "0"
assert_eq "tar 失败时 stdout 干净（没有假装成功的路径）" "${LAST_OUT}" ""
contains "日志里说明了失败" "${LAST_ERR}" '备份失败'
assert_eq "失败后没留下归档" "$(count_archives "${ROOT4}")" "0"
assert_eq "失败后没留下 .part 半成品" "$(count_parts "${ROOT4}")" "0"

# 4b. 落点不可写（连兜底落点也推不出来可写的）
ROOT4B="${SANDBOX}/vol4b"
VAR4B="${ROOT4B}/@appdata/yinshu-ai"
mkdir -p "${VAR4B}/data"; printf '{"port":1}\n' > "${VAR4B}/data/config.json"
chmod 500 "${ROOT4B}/@appdata"          # 兜底落点 = @appdata，只读
show "落点只读 → uninstall_init"
env -i PATH="/usr/bin:/bin" HOME="${SANDBOX}" TRIM_PKGVAR="${VAR4B}" \
    bash "${CMD_DIR}/uninstall_init" > "${SANDBOX}/.stdout" 2> "${SANDBOX}/.stderr"
RC=$?; LAST_ERR="$(cat "${SANDBOX}/.stderr")"
dump "${LAST_ERR}"
assert_eq "落点不可写时 exit 0" "${RC}" "0"
contains "日志里说了落点不可写" "${LAST_ERR}" '不可写'
assert_eq "没有产出归档" "$(count_archives "${ROOT4B}")" "0"
chmod 700 "${ROOT4B}/@appdata"

# 4b2. QULV_BACKUP_DIR 指向不存在的地方 → 警告并改用自动推导的落点
show "QULV_BACKUP_DIR 不存在 → 退回推导落点"
run_phase uninstall_init "${VAR1}" "${SANDBOX}/nonexistent-dir"
assert_eq "退出码 0" "${RC}" "0"
contains "日志里警告了 QULV_BACKUP_DIR 不可用" "${LAST_ERR}" '不存在或不可写，改用自动推导的落点'
assert_eq "归档落到了推导出来的父目录" "$(count_archives "$(dirname "${VAR1}")")" "1"

# 4c. 数据目录存在但空（装完从没启动过）
VAR4C="${SANDBOX}/vol4c/@appdata/yinshu-ai"; mkdir -p "${VAR4C}"
ROOT4C="${SANDBOX}/vol4c"; mkdir -p "${ROOT4C}"
run_phase uninstall_init "${VAR4C}" "${ROOT4C}"
dump "${LAST_ERR}"
assert_eq "数据目录为空时 exit 0" "${RC}" "0"
contains "日志里说了没有可备份的数据" "${LAST_ERR}" '没有可备份的数据'
assert_eq "空数据目录不产出归档" "$(count_archives "${ROOT4C}")" "0"

# 4d. TRIM_PKGVAR 完全没设、也没有 /vol{n}（fNOS 传参顺序变了/手工跑）
rc4d() { env -i PATH="/usr/bin:/bin" HOME="${SANDBOX}" bash "${CMD_DIR}/upgrade_init"; }
assert_true "TRIM_PKGVAR 缺失时 exit 0" rc4d

# 4e. _backup 文件缺失（旧安装包装出来的 / 打包漏了这个文件）→ 仍然 exit 0
CMDNB="${SANDBOX}/cmd-nobackup"; mkdir -p "${CMDNB}"
cp "${CMD_DIR}/uninstall_init" "${CMDNB}/"
env -i PATH="/usr/bin:/bin" HOME="${SANDBOX}" TRIM_PKGVAR="${VAR1}" \
    bash "${CMDNB}/uninstall_init" > "${SANDBOX}/.stdout" 2> "${SANDBOX}/.stderr"
RC=$?; LAST_ERR="$(cat "${SANDBOX}/.stderr")"
dump "${LAST_ERR}"
assert_eq "_backup 缺失时 exit 0" "${RC}" "0"
contains "日志里说了 _backup 缺失" "${LAST_ERR}" '未找到 .*_backup'

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 5：限时 —— 卡死的 tar 被连子孙一起掐掉，归档不转正，退出码仍为 0"

STUBDIR5="${SANDBOX}/bin-slowtar"
mkdir -p "${STUBDIR5}"
for t in dirname gzip date readlink sort sed wc mv rm cat; do ln -s "/usr/bin/$t" "${STUBDIR5}/$t"; done
cat > "${STUBDIR5}/tar" <<'SLOW'
#!/bin/bash
# 冒充 tar：既不产出合法归档，又永远不结束（测超时）
# ⚠️ 必须用 exec：让 sleep **替换**这个 shell 进程。否则被杀的是外面这层 shell，
#    sleep 变成孤儿 —— 那正是要防的形态（真机上 tar 下面还挂着 gzip）。
echo "tar: 假装在打包" >&2
exec sleep 31415
SLOW
chmod +x "${STUBDIR5}/tar"
ROOT5="${SANDBOX}/vol5"; mkdir -p "${ROOT5}"
show "PATH-桩 tar 卡死 + QULV_BACKUP_TIMEOUT=1 QULV_BACKUP_VERIFY_TIMEOUT=1"
T0="$(date +%s)"
# 桩 tar 对任何调用都卡死（连 .part 的复核 tar -tzf 也一样），所以这一条同时验
# 两件事：打包被掐掉、**复核也被掐掉**（复核要是不限时，这里就会挂住 —— 本地抓到过）
env -i PATH="${STUBDIR5}:/usr/bin:/bin" HOME="${SANDBOX}" TRIM_PKGVAR="${VAR1}" \
    QULV_BACKUP_DIR="${ROOT5}" QULV_BACKUP_TIMEOUT=1 QULV_BACKUP_VERIFY_TIMEOUT=1 \
    bash "${CMD_DIR}/uninstall_init" > "${SANDBOX}/.stdout" 2> "${SANDBOX}/.stderr"
RC=$?
T1="$(date +%s)"
LAST_ERR="$(cat "${SANDBOX}/.stderr")"
dump "${LAST_ERR}"
assert_eq "卡死的 tar 下卸载仍然 exit 0" "${RC}" "0"
assert_true "打包+复核都被限时掐掉，总耗时 $((T1 - T0))s < 15s" test "$((T1 - T0))" -lt 15
contains "日志里说明了超时" "${LAST_ERR}" '超时'
assert_eq "超时后没留下归档" "$(count_archives "${ROOT5}")" "0"
assert_eq "超时后没留下 .part" "$(count_parts "${ROOT5}")" "0"
# 钉子：被限时命令的**子孙**也必须被收掉，不能留孤儿继续攥着 stdout/IO
sleep 0.5
assert_false "没有留下孤儿进程（sleep 31415）" pgrep -f "sleep 31415"

# 限时的另一半：预算给足时，正常打包照样成功（别把好命令也误杀）
ROOT5B="${SANDBOX}/vol5b"; mkdir -p "${ROOT5B}"
run_phase uninstall_init "${VAR1}" "${ROOT5B}" QULV_BACKUP_TIMEOUT=60
assert_eq "限时 60s 时正常打包成功" "$(count_archives "${ROOT5B}")" "1"

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 6：卷根推导（readlink 用桩 —— uid!=0 时建不出真的 /vol{n}）"

STUBDIR6="${SANDBOX}/bin-readlink"
mkdir -p "${STUBDIR6}"
probe_root() {
    printf '#!/bin/bash\nprintf "%%s\\n" "%s"\n' "$1" > "${STUBDIR6}/readlink"
    chmod +x "${STUBDIR6}/readlink"
    env -i PATH="${STUBDIR6}:/usr/bin:/bin" \
        bash -c "SELF_DIR='${CMD_DIR}'; . '${CMD_DIR}/_backup'; qulv_backup_root '${2}'" 2>/dev/null
}
assert_eq "readlink 给出 /vol5/@appdata/... → 卷根 /vol5" \
    "$(probe_root /vol5/@appdata/yinshu-ai "${SANDBOX}/anywhere")" "/vol5"
assert_eq "readlink 给出 /vol12/@appdata/... → 卷根 /vol12（多位数卷号）" \
    "$(probe_root /vol12/@appdata/yinshu-ai "${SANDBOX}/anywhere")" "/vol12"
assert_eq "readlink 给出正好 /vol1 → 卷根 /vol1（没有尾斜杠也不出错）" \
    "$(probe_root /vol1 "${SANDBOX}/anywhere")" "/vol1"
assert_eq "readlink 给不出 /vol{n} → 退回数据目录的父目录" \
    "$(env -i PATH="/usr/bin:/bin" bash -c "SELF_DIR='${CMD_DIR}'; . '${CMD_DIR}/_backup'; qulv_backup_root '${VAR1}'" 2>/dev/null)" \
    "$(dirname "${VAR1}")"
# 反向钉子：/tmp/vol9/... 这种「路径中间有 /volN」的绝不能被当成卷根
# （沙箱路径就在 /tmp 下，这条成立才轮得到上面几条断言）
mkdir -p "${SANDBOX}/x/y"
assert_eq "路径中间出现 /vol9 不被误判成卷根（应退到父目录）" \
    "$(probe_root "${SANDBOX}/vol9/@appdata/yinshu-ai" "${SANDBOX}/x/y")" "${SANDBOX}/x"

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 7：wizard_keep_data=false 跳过备份（兼容旧向导开关）"

ROOT7="${SANDBOX}/vol7"; mkdir -p "${ROOT7}"
show "wizard_keep_data=false → uninstall_init"
env -i PATH="/usr/bin:/bin" HOME="${SANDBOX}" TRIM_PKGVAR="${VAR1}" \
    QULV_BACKUP_DIR="${ROOT7}" wizard_keep_data=false \
    bash "${CMD_DIR}/uninstall_init" > "${SANDBOX}/.stdout" 2> "${SANDBOX}/.stderr"
RC=$?; LAST_ERR="$(cat "${SANDBOX}/.stderr")"
dump "${LAST_ERR}"
assert_eq "退出码 0" "${RC}" "0"
assert_eq "没有产出归档" "$(count_archives "${ROOT7}")" "0"

# ══════════════════════════════════════════════════════════════════════════════
head2 "用例 8：数据目录里有陈旧 unix socket / 瞬态文件时，打包照样成功"

# 真机上这是常见态：上一次被 kill -9（或崩溃）之后，takeover 注入用的
# `.qulv-stage-<pid>.sock` 会留在数据目录里（socket 文件不会随进程消失）。
# tar 碰到 socket 会「socket ignored」—— 要确认那是 warning 而不是失败。
VAR8="${SANDBOX}/vol8/@appdata/yinshu-ai"
ROOT8="${SANDBOX}/vol8"; mkdir -p "${ROOT8}"
make_fake_app "${VAR8}"
python3 -c "import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])" \
    "${VAR8}/data/.qulv-stage-12345.sock"
printf '' > "${VAR8}/data/ui.dev.js"
assert_true "夹具里确实有一个真 unix socket" test -S "${VAR8}/data/.qulv-stage-12345.sock"
run_phase uninstall_init "${VAR8}" "${ROOT8}"
dump "${LAST_ERR}"
assert_eq "有 socket 时退出码仍为 0" "${RC}" "0"
ARCH8="$(archive_of "${ROOT8}")"
assert_true "有 socket 时归档照样产出" test -f "${ARCH8}"
LIST8="${SANDBOX}/list8.txt"
tar -tzf "${ARCH8}" > "${LIST8}" 2>&1
assert_false "归档里没有 .sock 成员" grep -qE '\.sock$' "${LIST8}"
assert_true "归档里仍然有 config.json（socket 没把打包带崩）" grep -qE '^data/config\.json$' "${LIST8}"
assert_true "归档里带上了 ui.dev.js（默认进，不是缓存）" grep -qE '^data/ui\.dev\.js$' "${LIST8}"

# ══════════════════════════════════════════════════════════════════════════════
printf '\n\033[1m== 汇总 ==\033[0m\n'
printf '  通过 %d，失败 %d\n' "${PASS}" "${FAIL}"
[ "${FAIL}" -eq 0 ] || exit 1
exit 0