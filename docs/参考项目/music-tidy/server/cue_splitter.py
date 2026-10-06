# -*- coding: utf-8 -*-
"""CUE 整轨拆分模块

解析 .cue 文件，用 ffmpeg 按时间戳拆分整轨音频为逐曲文件。
支持 UTF-8 / GBK / CP936 编码自动检测。
"""
import os
import re
import subprocess
from dataclasses import dataclass, field
from typing import List, Optional, Dict, Tuple


@dataclass
class CueTrack:
    """CUE 音轨信息"""
    number: int
    title: str = ""
    artist: str = ""
    start_seconds: float = 0.0  # 起始时间（秒）
    album: str = ""
    album_artist: str = ""
    date: str = ""
    genre: str = ""


@dataclass
class CueSheet:
    """CUE 文件解析结果"""
    file_path: str = ""  # 关联的音频文件路径
    album: str = ""
    album_artist: str = ""
    date: str = ""
    genre: str = ""
    tracks: List[CueTrack] = field(default_factory=list)


def _parse_time(time_str: str) -> float:
    """解析 CUE 时间格式 MM:SS:FF 为秒数

    Args:
        time_str: 时间字符串，如 "03:45:50"（分:秒:帧）

    Returns:
        秒数（浮点数）
    """
    parts = time_str.split(":")
    if len(parts) != 3:
        return 0.0
    try:
        minutes = int(parts[0])
        seconds = int(parts[1])
        frames = int(parts[2])
        # 75 帧/秒（CD 标准）
        return minutes * 60 + seconds + frames / 75.0
    except ValueError:
        return 0.0


def _try_read_cue(cue_path: str) -> Optional[str]:
    """尝试多种编码读取 CUE 文件

    Args:
        cue_path: CUE 文件路径

    Returns:
        文件内容字符串，读取失败返回 None
    """
    encodings = ["utf-8", "gbk", "cp936", "utf-16", "latin-1"]
    for enc in encodings:
        try:
            with open(cue_path, "r", encoding=enc) as f:
                return f.read()
        except (UnicodeDecodeError, UnicodeError):
            continue
    return None


def parse_cue(cue_path: str) -> Optional[CueSheet]:
    """解析 CUE 文件

    Args:
        cue_path: CUE 文件路径

    Returns:
        CueSheet 对象，解析失败返回 None
    """
    content = _try_read_cue(cue_path)
    if not content:
        return None

    cue_dir = os.path.dirname(os.path.abspath(cue_path))
    sheet = CueSheet()

    # 全局信息
    album_artist = ""
    album_title = ""
    date = ""
    genre = ""
    audio_file = ""

    # 音轨信息
    tracks: List[Dict] = []
    current_track: Optional[Dict] = None

    for line in content.splitlines():
        line = line.strip()
        if not line:
            continue

        # REM GENRE
        m = re.match(r'^REM\s+GENRE\s+"?([^"]*)"?$', line, re.IGNORECASE)
        if m:
            genre = m.group(1).strip()
            continue

        # REM DATE
        m = re.match(r'^REM\s+DATE\s+"?([^"]*)"?$', line, re.IGNORECASE)
        if m:
            date = m.group(1).strip()
            continue

        # PERFORMER（全局）
        m = re.match(r'^PERFORMER\s+"([^"]*)"', line, re.IGNORECASE)
        if m:
            if current_track is None:
                album_artist = m.group(1)
            else:
                current_track["artist"] = m.group(1)
            continue

        # TITLE（全局或音轨）
        m = re.match(r'^TITLE\s+"([^"]*)"', line, re.IGNORECASE)
        if m:
            if current_track is None:
                album_title = m.group(1)
            else:
                current_track["title"] = m.group(1)
            continue

        # FILE
        m = re.match(r'^FILE\s+"([^"]*)"', line, re.IGNORECASE)
        if m:
            audio_file = m.group(1)
            continue

        # TRACK
        m = re.match(r'^TRACK\s+(\d+)\s+AUDIO', line, re.IGNORECASE)
        if m:
            if current_track is not None:
                tracks.append(current_track)
            current_track = {
                "number": int(m.group(1)),
                "title": "",
                "artist": "",
                "start": 0.0,
            }
            continue

        # INDEX 01（音轨起始时间）
        if current_track is not None:
            m = re.match(r'^INDEX\s+01\s+(\d+:\d+:\d+)', line, re.IGNORECASE)
            if m:
                current_track["start"] = _parse_time(m.group(1))
                continue

    # 最后一个音轨
    if current_track is not None:
        tracks.append(current_track)

    if not tracks:
        return None

    # 查找音频文件
    if audio_file:
        # CUE 中的路径可能是相对路径
        audio_path = os.path.join(cue_dir, audio_file)
        if not os.path.exists(audio_path):
            # 尝试不同大小写
            for f in os.listdir(cue_dir):
                if f.lower() == audio_file.lower():
                    audio_path = os.path.join(cue_dir, f)
                    break
        sheet.file_path = audio_path
    else:
        # 没有 FILE 指令，尝试找同名音频文件
        cue_stem = os.path.splitext(os.path.basename(cue_path))[0]
        audio_exts = [".flac", ".ape", ".wav", ".wv", ".tta", ".mp3", ".m4a"]
        for ext in audio_exts:
            candidate = os.path.join(cue_dir, cue_stem + ext)
            if os.path.exists(candidate):
                sheet.file_path = candidate
                break

    # 填充 CueSheet
    sheet.album = album_title
    sheet.album_artist = album_artist
    sheet.date = date
    sheet.genre = genre

    for t in tracks:
        track = CueTrack(
            number=t["number"],
            title=t.get("title", ""),
            artist=t.get("artist", "") or album_artist,
            start_seconds=t.get("start", 0.0),
            album=album_title,
            album_artist=album_artist,
            date=date,
            genre=genre,
        )
        sheet.tracks.append(track)

    return sheet


def split_cue(cue_path: str, output_dir: Optional[str] = None,
              progress_callback=None) -> List[str]:
    """拆分 CUE 整轨音频为逐曲文件

    Args:
        cue_path: CUE 文件路径
        output_dir: 输出目录，默认与 CUE 同目录
        progress_callback: 进度回调 callback(current, total, message)

    Returns:
        拆分后的文件路径列表

    Raises:
        ValueError: CUE 解析失败或音频文件不存在
        RuntimeError: ffmpeg 拆分失败
    """
    sheet = parse_cue(cue_path)
    if not sheet:
        raise ValueError(f"无法解析 CUE 文件: {cue_path}")

    if not sheet.file_path or not os.path.exists(sheet.file_path):
        raise ValueError(f"找不到关联的音频文件: {sheet.file_path}")

    if not sheet.tracks:
        raise ValueError("CUE 文件中没有音轨信息")

    # 输出目录
    if output_dir is None:
        output_dir = os.path.dirname(os.path.abspath(cue_path))
    os.makedirs(output_dir, exist_ok=True)

    # 检查 ffmpeg
    try:
        subprocess.run(["ffmpeg", "-version"], capture_output=True, check=True)
    except (subprocess.CalledProcessError, FileNotFoundError):
        raise RuntimeError("找不到 ffmpeg，请先安装")

    audio_path = sheet.file_path
    total = len(sheet.tracks)
    output_files = []

    for i, track in enumerate(sheet.tracks):
        if progress_callback:
            progress_callback(i, total, f"拆分第 {i + 1}/{total} 首: {track.title or f'Track {track.number}'}")

        # 计算结束时间
        if i + 1 < len(sheet.tracks):
            end_time = sheet.tracks[i + 1].start_seconds
        else:
            end_time = None  # 最后一首到结尾

        # 生成输出文件名
        title = track.title or f"Track {track.number:02d}"
        # 清理文件名中的非法字符
        safe_title = re.sub(r'[<>:"/\\|?*]', "_", title)
        output_name = f"{track.number:02d} - {safe_title}.flac"
        output_path = os.path.join(output_dir, output_name)

        # 构建 ffmpeg 命令
        cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y"]

        # 起始时间
        cmd.extend(["-ss", str(track.start_seconds)])

        # 结束时间
        if end_time is not None:
            cmd.extend(["-to", str(end_time)])

        # 输入文件
        cmd.extend(["-i", audio_path])

        # 输出参数（FLAC 无损压缩）
        cmd.extend(["-c:a", "flac", "-compression_level", "5"])

        # 元数据标签
        if track.title:
            cmd.extend(["-metadata", f"title={track.title}"])
        if track.artist:
            cmd.extend(["-metadata", f"artist={track.artist}"])
        if track.album:
            cmd.extend(["-metadata", f"album={track.album}"])
        if track.album_artist:
            cmd.extend(["-metadata", f"album_artist={track.album_artist}"])
        if track.date:
            cmd.extend(["-metadata", f"date={track.date}"])
        if track.genre:
            cmd.extend(["-metadata", f"genre={track.genre}"])
        cmd.extend(["-metadata", f"track={track.number}"])

        # 输出文件
        cmd.append(output_path)

        # 执行拆分
        result = subprocess.run(cmd, capture_output=True, text=True)
        if result.returncode != 0:
            raise RuntimeError(f"ffmpeg 拆分失败: {result.stderr}")

        output_files.append(output_path)

    if progress_callback:
        progress_callback(total, total, "拆分完成")

    return output_files


def find_cue_files(folders: List[str]) -> List[str]:
    """在指定目录中查找 CUE 文件

    Args:
        folders: 目录列表

    Returns:
        CUE 文件路径列表
    """
    cue_files = []
    for folder in folders:
        if not os.path.isdir(folder):
            continue
        for root, dirs, files in os.walk(folder):
            for f in files:
                if f.lower().endswith(".cue"):
                    cue_files.append(os.path.join(root, f))
    return cue_files
