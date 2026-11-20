set -u
umask 077
id=%s
exe=%s
port=%s
root="$HOME/.echomanager"
meta="$root/instances/$id.meta"
log="$root/logs/$id.log"
mkdir -p "$root/instances" "$root/logs"
%s
if [ ! -x "$exe" ]; then printf '__EM_ERROR__|程序不存在或不可执行'; exit 4; fi
if [ -f "$meta" ]; then
  old_pid=$(sed -n '1p' "$meta")
  old_token=$(sed -n '2p' "$meta")
  old_exe=$(sed -n '3p' "$meta")
  old_started=$(sed -n '4p' "$meta")
  if [ -n "$old_pid" ] && process_matches "$old_pid" "$old_exe" "$old_token"; then
    printf '__EM_ERROR__|此实例已在目标设备运行（PID %%s）' "$old_pid"; exit 5
  fi
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
    current_token=$(process_start_token "$old_pid")
    if [ -n "$old_token" ] && [ "$current_token" = "$old_token" ]; then
      write_adopted_record "$old_pid" "$current_token" "$old_started"
      exit 0
    fi
    if adopted=$(adopt_port_listener); then printf '%%s\n' "$adopted"; exit 0; fi
    printf '__EM_ERROR__|远端进程身份已变化，且无法唯一确认配置端口的监听进程；为避免启动重复实例，未执行接管'; exit 8
  fi
  if adopted=$(adopt_port_listener); then printf '%%s\n' "$adopted"; exit 0; fi
fi
if [ ! -f "$meta" ]; then
  if adopted=$(adopt_port_listener); then printf '%%s\n' "$adopted"; exit 0; fi
  if port_is_listening; then printf '__EM_ERROR__|配置端口已有监听进程，但无法唯一确认其身份；为避免启动重复实例，未执行启动'; exit 8; fi
fi
nohup env%s "$exe"%s >>"$log" 2>&1 </dev/null &
pid=$!
sleep 0.15
if ! kill -0 "$pid" 2>/dev/null; then printf '__EM_ERROR__|进程启动后立即退出，请检查远程日志'; exit 6; fi
token=$(process_start_token "$pid")
started=$(date +%%s)
if [ -z "$token" ]; then kill -TERM "$pid" 2>/dev/null || true; printf '__EM_ERROR__|无法核验远程进程身份'; exit 7; fi
tmp="$meta.tmp.$$"
printf '%%s\n%%s\n%%s\n%%s\n' "$pid" "$token" "$exe" "$started" >"$tmp"
mv "$tmp" "$meta"
printf '__EM_STARTED__|%%s|%%s\n' "$pid" "$started"