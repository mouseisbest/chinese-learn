#!/usr/bin/env bash
#
# 认字练习 —— 停止脚本
#
#   ./stop.sh        停止程序
#   ./stop.sh -f     强制停止（不等优雅关闭，直接 kill -9）
#   ./stop.sh -a     停止所有 chinese-learn 进程（含孤儿进程）
#
# 与 ./run.sh stop 的区别：这个脚本在 PID 文件丢失、或程序是被手工启动
# 的情况下也能找到进程并停掉。run.sh stop 只认 PID 文件。

set -uo pipefail

cd "$(dirname "$0")"

BIN="chinese-learn"
PIDFILE=".run.pid"

FORCE=0
ALL=0
while [ $# -gt 0 ]; do
  case "$1" in
    -f|--force) FORCE=1 ;;
    -a|--all)   ALL=1 ;;
    -h|--help)
      # 打印文件开头的注释块（到第一个非注释行为止），避免行号写死后错位
      awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next }
           NR>1 { exit }' "$0"
      exit 0
      ;;
    *) printf '未知参数：%s（用 -h 看帮助）\n' "$1" >&2; exit 1 ;;
  esac
  shift
done

# 强制停止时一并处理孤儿进程：用户要的是「停掉」，不该还得再加一个参数。
if [ "$FORCE" = 1 ]; then
  ALL=1
fi

if [ -t 1 ]; then
  C_OK=$'\033[32m'; C_ERR=$'\033[31m'; C_DIM=$'\033[2m'; C_0=$'\033[0m'
else
  C_OK=''; C_ERR=''; C_DIM=''; C_0=''
fi

info() { printf '%s\n' "$*"; }
ok()   { printf '%s✓%s %s\n' "$C_OK" "$C_0" "$*"; }
warn() { printf '%s!%s %s\n' "$C_ERR" "$C_0" "$*"; }

# 找出所有 chinese-learn 进程的 PID。
#
# 注意排除自己：脚本名里没有 chinese-learn，但调用方的命令行可能包含，
# 所以用 pgrep 精确匹配可执行文件名，再排除自身 PID。
find_pids() {
  local self=$$
  if command -v pgrep >/dev/null 2>&1; then
    pgrep -x "$BIN" 2>/dev/null | grep -v "^${self}$" || true
  else
    ps -eo pid=,comm= 2>/dev/null \
      | awk -v b="$BIN" -v s="$self" '$2 == b && $1 != s { print $1 }'
  fi
}

# 等待进程退出，返回 0 表示已退出。
wait_gone() {
  local pid=$1 limit=$2 i=0
  while kill -0 "$pid" 2>/dev/null && [ "$i" -lt "$limit" ]; do
    sleep 0.25
    i=$((i + 1))
  done
  ! kill -0 "$pid" 2>/dev/null
}

stopped=0

# ---- 第一步：按 PID 文件停（正常情况）----
if [ -f "$PIDFILE" ]; then
  pid="$(cat "$PIDFILE" 2>/dev/null || true)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    if [ "$FORCE" = 1 ]; then
      kill -9 "$pid" 2>/dev/null || true
      ok "已强制停止（PID ${pid}）"
    else
      kill "$pid" 2>/dev/null || true
      if wait_gone "$pid" 5; then
        ok "已停止（PID ${pid}）"
      else
        warn "优雅关闭超时，强制结束（PID ${pid}）"
        kill -9 "$pid" 2>/dev/null || true
      fi
    fi
    stopped=1
  fi
  rm -f "$PIDFILE"
fi

# ---- 第二步：处理 PID 文件没覆盖到的进程 ----
# 孤儿进程的来源：手工 ./chinese-learn 启动、换了 DB 路径启动、
# 或者 PID 文件被误删。这些进程仍占着端口，必须能停掉。
orphans="$(find_pids)"

if [ -n "$orphans" ]; then
  # 用数组承接，避免管道里的 while 在子 shell 里跑导致 stopped 传不回来
  pids=()
  while IFS= read -r p; do
    [ -n "$p" ] && pids+=("$p")
  done <<< "$orphans"

  if [ "$ALL" != 1 ]; then
    info "${C_DIM}发现 ${#pids[@]} 个没有记录在 PID 文件里的进程：${C_0}"
    for p in "${pids[@]}"; do
      start="$(ps -o lstart= -p "$p" 2>/dev/null || true)"
      printf '  PID %s  %s\n' "$p" "$start"
    done
    info ""
    info "要停掉它们，加 -a：  ${C_DIM}./stop.sh -a${C_0}"
    if [ "$stopped" = 1 ]; then
      info "${C_DIM}（PID 文件记录的那个已经停了）${C_0}"
    fi
    exit 0
  fi

  for p in "${pids[@]}"; do
    if [ "$FORCE" = 1 ]; then
      kill -9 "$p" 2>/dev/null || true
      ok "已强制停止（PID ${p}）"
    else
      kill "$p" 2>/dev/null || true
      if wait_gone "$p" 5; then
        ok "已停止（PID ${p}）"
      else
        warn "优雅关闭超时，强制结束（PID ${p}）"
        kill -9 "$p" 2>/dev/null || true
      fi
    fi
    stopped=1
  done
fi

if [ "$stopped" = 0 ]; then
  info "没有在运行"
  rm -f "$PIDFILE"
fi
