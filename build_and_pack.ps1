# ==============================================================================
#  fnOS (飞牛 NAS) 极光音乐 / fnmusic —— Windows 一键构建打包脚本
#
#  全部路径均以本脚本所在目录为基准（相对路径），不再写死任何绝对路径。
#  打包器优先级：
#     1) scripts\fnpack.exe   （飞牛官方工具，若存在则优先使用）
#     2) scripts\build_fpk.py （仓库自带的零依赖 Python 打包器，回退方案）
#
#  用法：
#     powershell -ExecutionPolicy Bypass -File .\build_and_pack.ps1
#     powershell -ExecutionPolicy Bypass -File .\build_and_pack.ps1 -SkipFrontend
#     powershell -ExecutionPolicy Bypass -File .\build_and_pack.ps1 -Version 1.2.8
#     powershell -ExecutionPolicy Bypass -File .\build_and_pack.ps1 -Output .\dist\fn-lx-player.fpk
# ==============================================================================
[CmdletBinding()]
param(
    [string]$Version = "",
    [switch]$SkipFrontend,
    [string]$Output = "",
    [string]$Go = "",
    [switch]$LegacyModes
)

$ErrorActionPreference = "Stop"

function Write-Step($msg) { Write-Host "`n=== $msg ===" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "[信息] $msg" -ForegroundColor Green }
function Write-Warn2($msg){ Write-Host "[警告] $msg" -ForegroundColor Yellow }
function Fail($msg)       { Write-Host "[错误] $msg" -ForegroundColor Red; exit 1 }

# ---------------------------------------------------------------------------
# 0. 以脚本所在目录为基准解析所有路径
# ---------------------------------------------------------------------------
$RootDir = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($RootDir)) {
    $RootDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
}
$RootDir     = (Resolve-Path -LiteralPath $RootDir).Path
$ScriptsDir  = Join-Path $RootDir "scripts"
$FrontendDir = Join-Path $RootDir "frontend"
$BackendDir  = Join-Path $RootDir "backend"
$FpkPkgDir   = Join-Path $RootDir "fpk-package"
$AppDir      = Join-Path $FpkPkgDir "app"
$BinOut      = Join-Path $AppDir "fn-lx-player"
$ToolsDir    = Join-Path $RootDir ".tools"
$NpmCacheDir = Join-Path $RootDir ".npm-cache"

Write-Host "fnOS FPK 打包脚本" -ForegroundColor Cyan
Write-Ok "仓库根目录：$RootDir"

# ---------------------------------------------------------------------------
# 1. 前端构建
# ---------------------------------------------------------------------------
if (-not $SkipFrontend) {
    Write-Step "1. 构建前端 (npm run build)"
    $npm = Get-Command npm -ErrorAction SilentlyContinue
    if (-not $npm) { Fail "未找到 npm，请先安装 Node.js 18+，或加 -SkipFrontend 跳过前端构建。" }
    Push-Location $FrontendDir
    try {
        if (Test-Path -LiteralPath $NpmCacheDir) { $env:npm_config_cache = $NpmCacheDir }
        & npm run build
        if ($LASTEXITCODE -ne 0) { Fail "前端构建失败 (npm run build 退出码 $LASTEXITCODE)。" }
    } finally { Pop-Location }
    Write-Ok "前端构建完成。"
} else {
    Write-Step "1. 跳过前端构建 (-SkipFrontend)"
}

# ---------------------------------------------------------------------------
# 2. 打包器选择：优先 fnpack.exe，否则回退 build_fpk.py
# ---------------------------------------------------------------------------
$FnpackExe = Join-Path $ScriptsDir "fnpack.exe"
$BuildPy   = Join-Path $ScriptsDir "build_fpk.py"

if (Test-Path -LiteralPath $FnpackExe) {
    # ---------------- 2A. 官方 fnpack 路径 ----------------
    Write-Step "2. 探测 Go 工具链（fnpack 路径）"
    $goExe = $Go
    if ([string]::IsNullOrWhiteSpace($goExe)) {
        foreach ($c in @((Join-Path $ToolsDir "go\bin\go.exe"), (Join-Path $ToolsDir "go\bin\go"))) {
            if (Test-Path -LiteralPath $c) { $goExe = $c; break }
        }
    }
    if ([string]::IsNullOrWhiteSpace($goExe)) {
        $cmd = Get-Command go -ErrorAction SilentlyContinue
        if ($cmd) { $goExe = $cmd.Source }
    }
    if ([string]::IsNullOrWhiteSpace($goExe)) { Fail "未找到 Go 工具链。请用 -Go <path> 指定，或安装 Go 1.22+。" }
    Write-Ok "Go: $goExe"

    Write-Step "3. 交叉编译 Linux amd64 主程序"
    $env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
    if (-not $env:GOCACHE -and (Test-Path -LiteralPath (Join-Path $ToolsDir "gocache"))) {
        $env:GOCACHE = Join-Path $ToolsDir "gocache"
    }
    if (-not $env:GOPATH -and (Test-Path -LiteralPath (Join-Path $ToolsDir "gopath"))) {
        $env:GOPATH = Join-Path $ToolsDir "gopath"
    }
    Push-Location $BackendDir
    try {
        & $goExe build -ldflags="-s -w" -o $BinOut main.go
        if ($LASTEXITCODE -ne 0) { Fail "后端编译失败 (go build 退出码 $LASTEXITCODE)。" }
    } finally { Pop-Location }
    $binItem = Get-Item -LiteralPath $BinOut
    Write-Ok "编译产物：$($binItem.FullName)  $($binItem.Length) 字节"

    Write-Step "4. 调用 fnpack 打包"
    Push-Location $RootDir
    try {
        & $FnpackExe build -d $FpkPkgDir
        if ($LASTEXITCODE -ne 0) { Fail "fnpack build 失败 (退出码 $LASTEXITCODE)。" }
    } finally { Pop-Location }

    $resultFpk = Join-Path $RootDir "fn-lx-player.fpk"
    if (-not (Test-Path -LiteralPath $resultFpk)) { Fail "fnpack 未生成 $resultFpk" }

    Write-Step "5. 校验 app.tgz 内的可执行程序"
    $pyExe = ""
    foreach ($name in @("python", "python3", "py")) {
        $c = Get-Command $name -ErrorAction SilentlyContinue
        if ($c) { $pyExe = $c.Source; break }
    }
    if ($pyExe) {
        & $pyExe $BuildPy --inspect $resultFpk
    } else {
        Write-Warn2 "未找到 python，跳过深度校验。"
    }
    Write-Host "`n[完成] 打包成功：$resultFpk" -ForegroundColor Green
    exit 0
}

# ---------------- 2B. 回退：零依赖 Python 打包器 ----------------
Write-Warn2 "未找到 $FnpackExe，回退使用仓库自带的 Python 打包器 scripts\build_fpk.py"

if (-not (Test-Path -LiteralPath $BuildPy)) { Fail "打包器不存在：$BuildPy" }

$pyExe = ""
foreach ($name in @("python", "python3", "py")) {
    $c = Get-Command $name -ErrorAction SilentlyContinue
    if ($c) {
        & $c.Source -c "import sys; sys.exit(0 if sys.version_info >= (3,8) else 1)" 2>$null
        if ($LASTEXITCODE -eq 0) { $pyExe = $c.Source; break }
    }
}
if ([string]::IsNullOrWhiteSpace($pyExe)) { Fail "未找到 Python 3 (>=3.8)，无法运行 scripts\build_fpk.py。" }
Write-Ok "Python: $pyExe"

$pyArgs = @($BuildPy)
if ($Version)     { $pyArgs += @("--version", $Version) }
if ($SkipFrontend){ $pyArgs += "--skip-frontend" }
if ($Output)      { $pyArgs += @("--output", $Output) }
if ($Go)          { $pyArgs += @("--go", $Go) }
if ($LegacyModes) { $pyArgs += "--legacy-modes" }

Push-Location $RootDir
try {
    & $pyExe @pyArgs
    if ($LASTEXITCODE -ne 0) { Fail "打包失败 (build_fpk.py 退出码 $LASTEXITCODE)。" }
} finally { Pop-Location }

Write-Host "`n[完成] 打包成功。" -ForegroundColor Green
exit 0
