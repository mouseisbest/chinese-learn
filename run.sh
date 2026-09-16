#!/usr/bin/env bash
#
# 认字练习 —— 启动脚本
#
#   ./run.sh start     后台启动（默认）
#   ./run.sh stop      停止
#   ./run.sh restart   重启
#   ./run.sh status    查看运行状态和访问地址
#   ./run.sh log       跟踪日志
#   ./run.sh fg        前台运行（Ctrl+C 退出，调试用）
#   ./run.sh dev       开发模式前台运行（改模板刷新即见）
#   ./run.sh backup    备份数据库
#
# 环境变量（都有默认值，一般不用改）：
#   ADDR=0.0.0.0:8090   监听地址。只想本机访问就改成 127.0.0.1:8090
#   DB=data/chinese.db  数据库文件路径

set -euo pipefail

cd "$(dirname "$0")"

ADDR="${ADDR:-0.0.0.0:8090}"
DB="${DB:-data/chinese.db}"
BIN="chinese-learn"
PIDFILE=".run.pid"
LOGFILE="run.log"

# 颜色（管道输出时自动禁用）
if [ -t 1 ]; then
  C_OK=$'\033[32m'; C_ERR=$'\033[31m'; C_DIM=$'\033[2m'; C_B=$'\033[1m'; C_0=$'\033[0m'
else
  C_OK=''; C_ERR=''; C_DIM=''; C_B=''; C_0=''
fi

info()  { printf '%s\n' "$*"; }
ok()    { printf '%s✓%s %s\n' "$C_OK" "$C_0" "$*"; }
err()   { printf '%s✗%s %s\n' "$C_ERR" "$C_0" "$*" >&2; }

# 读取 PID 文件里记录的进程号，顺带校验它确实还活着。
# 直接 kill -0 会因为 PID 被系统回收而误判，所以额外核对进程名。
running_pid() {
  [ -f "$PIDFILE" ] || return 1
  local pid
  pid="$(cat "$PIDFILE" 2>/dev/null || true)"
  [ -n "$pid" ] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  # 确认这个 PID 确实是我们的程序，而不是被回收后分给了别的进程
  if [ -r "/proc/$pid/comm" ]; then
    grep -q "^${BIN}$" "/proc/$pid/comm" 2>/dev/null || return 1
  fi
  printf '%s' "$pid"
}

build() {
  # 静态资源和模板是用 embed 打进二进制的，改了它们同样需要重新编译。
  # 只检查 *.go 会漏掉「改了 CSS/JS 但没重编译」这种情况。
  if [ ! -f "$BIN" ] || [ -n "$(find . \( -name '*.go' -o -name '*.html' -o -name '*.css' -o -name '*.js' -o -name '*.svg' \) -newer "$BIN" -print -quit 2>/dev/null)" ]; then
    info "${C_DIM}编译中…${C_0}"
    if ! go build -o "$BIN" . ; then
      err "编译失败"
      exit 1
    fi
  fi
}

# 从 8090 开始找一个没被占用的端口，用于在端口冲突时给出可用建议。
free_port() {
  local p
  for p in $(seq 8090 8110); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
      printf '%s' "$p"
      return 0
    fi
    exec 3>&- 2>/dev/null || true
  done
  return 1
}

# 列出本机局域网地址，方便在平板上打开
lan_urls() {
  local port="${ADDR##*:}"
  if command -v ip >/dev/null 2>&1; then
    ip -4 addr show scope global 2>/dev/null \
      | grep -oP '(?<=inet\s)\d+(\.\d+){3}' \
      | while read -r ip; do printf '  http://%s:%s\n' "$ip" "$port"; done
  fi
}

show_urls() {
  local port="${ADDR##*:}"
  local host="${ADDR%:*}"
  info ""
  info "${C_B}访问地址${C_0}"
  if [ "$host" = "127.0.0.1" ]; then
    info "  http://localhost:${port}"
  else
    info "  本机      http://localhost:${port}"
    local urls
    urls="$(lan_urls)"
    if [ -n "$urls" ]; then
      info "  平板/手机（需连同一个 WiFi）"
      printf '%s\n' "$urls"
    else
      info "  ${C_DIM}（没找到局域网地址，检查一下网卡）${C_0}"
    fi
  fi
  info ""
}

cmd_start() {
  local pid
  if pid="$(running_pid)"; then
    info "已经在运行了（PID ${pid}）"
    show_urls
    return 0
  fi

  build
  mkdir -p "$(dirname "$DB")"

  nohup "./$BIN" -addr "$ADDR" -db "$DB" >> "$LOGFILE" 2>&1 &
  local newpid=$!
  printf '%s' "$newpid" > "$PIDFILE"

  # 等一下确认它没立刻挂掉（端口被占用是最常见的原因）
  sleep 1.5
  if ! kill -0 "$newpid" 2>/dev/null; then
    rm -f "$PIDFILE"
    err "启动失败。日志末尾："
    tail -n 15 "$LOGFILE" >&2 || true
    info ""
    info "常见原因：端口 ${ADDR##*:} 被别的程序占用了。"
    local alt
    alt="$(free_port 2>/dev/null || echo 8091)"
    info "换个端口重试：  ${C_DIM}ADDR=0.0.0.0:${alt} ./run.sh start${C_0}"
    exit 1
  fi

  ok "已启动（PID ${newpid}）"
  show_urls
  info "${C_DIM}日志：./run.sh log${C_0}"
}

cmd_stop() {
  local pid
  if ! pid="$(running_pid)"; then
    rm -f "$PIDFILE"
    info "没有在运行"
    return 0
  fi

  kill "$pid" 2>/dev/null || true

  # 等它自己收尾（程序做了优雅关闭，会给在途请求留时间）
  local i=0
  while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do
    sleep 0.25
    i=$((i + 1))
  done

  if kill -0 "$pid" 2>/dev/null; then
    info "${C_DIM}优雅关闭超时，强制结束${C_0}"
    kill -9 "$pid" 2>/dev/null || true
  fi

  rm -f "$PIDFILE"
  ok "已停止"
}

cmd_restart() {
  cmd_stop
  cmd_start
}

cmd_status() {
  local pid
  if pid="$(running_pid)"; then
    ok "运行中（PID ${pid}）"
    local uptime
    uptime="$(ps -o etime= -p "$pid" 2>/dev/null | tr -d ' ' || true)"
    [ -n "$uptime" ] && info "  已运行 ${uptime}"
    info "  数据库 $(du -h "$DB" 2>/dev/null | cut -f1 || echo '—')  ${C_DIM}${DB}${C_0}"
    if [ -f "$DB" ]; then
      local n
      n="$(sqlite3 "$DB" 'SELECT COUNT(*) FROM hanzi;' 2>/dev/null || echo '?')"
      info "  字表 ${n} 个字"
    fi
    show_urls
  else
    info "未运行"
    info "${C_DIM}启动：./run.sh start${C_0}"
    return 1
  fi
}

cmd_log() {
  if [ -f "$LOGFILE" ]; then
    tail -f "$LOGFILE"
  else
    info "还没有日志文件"
  fi
}

cmd_fg() {
  build
  mkdir -p "$(dirname "$DB")"
  info "${C_DIM}前台运行，Ctrl+C 退出${C_0}"
  show_urls
  exec "./$BIN" -addr "$ADDR" -db "$DB"
}

cmd_dev() {
  mkdir -p "$(dirname "$DB")"
  info "${C_DIM}开发模式：模板和样式从磁盘读取，改完刷新浏览器即见${C_0}"
  show_urls
  exec go run . -dev -addr "$ADDR" -db "$DB"
}

cmd_backup() {
  if [ ! -f "$DB" ]; then
    err "数据库不存在：$DB"
    exit 1
  fi
  local out="backup-$(date +%Y%m%d-%H%M%S).db"
  # 必须用 VACUUM INTO：程序开了 WAL，直接 cp 可能拿到不完整的快照
  if sqlite3 "$DB" "VACUUM INTO '$out';"; then
    ok "已备份到 $out（$(du -h "$out" | cut -f1)）"
  else
    err "备份失败"
    exit 1
  fi
}

case "${1:-start}" in
  start)   cmd_start ;;
  stop)    cmd_stop ;;
  restart) cmd_restart ;;
  status)  cmd_status ;;
  log|logs) cmd_log ;;
  fg)      cmd_fg ;;
  dev)     cmd_dev ;;
  backup)  cmd_backup ;;
  *)
    info "认字练习 —— 启动脚本"
    info ""
    info "用法：./run.sh <命令>"
    info ""
    info "  ${C_B}start${C_0}     后台启动（默认）"
    info "  ${C_B}stop${C_0}      停止"
    info "  ${C_B}restart${C_0}   重启"
    info "  ${C_B}status${C_0}    查看状态和访问地址"
    info "  ${C_B}log${C_0}       跟踪日志"
    info "  ${C_B}fg${C_0}        前台运行（调试用）"
    info "  ${C_B}dev${C_0}       开发模式（改模板即时生效）"
    info "  ${C_B}backup${C_0}    备份数据库"
    info ""
    info "环境变量："
    info "  ADDR=${ADDR}"
    info "  DB=${DB}"
    exit 1
    ;;
esac
