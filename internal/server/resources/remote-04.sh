set -u
id=%s
port=%s
meta="$HOME/.echomanager/instances/$id.meta"
%s
if [ ! -f "$meta" ]; then
  if adopted=$(adopt_port_listener); then
    old_pid=$(sed -n '1p' "$meta"); old_token=$(sed -n '2p' "$meta"); old_exe=$(sed -n '3p' "$meta"); old_started=$(sed -n '4p' "$meta")
  else
    if port_is_listening; then printf '__EM_STALE__\n'; exit 8; fi
    printf '__EM_STOPPED__\n'; exit 0
  fi
else
  old_pid=$(sed -n '1p' "$meta"); old_token=$(sed -n '2p' "$meta"); old_exe=$(sed -n '3p' "$meta"); old_started=$(sed -n '4p' "$meta")
fi
pid=$old_pid; token=$old_token; exe=$old_exe
if ! process_matches "$pid" "$exe" "$token"; then
  current_token=$(process_start_token "$pid")
  if kill -0 "$pid" 2>/dev/null && [ -n "$token" ] && [ "$current_token" = "$token" ]; then
    write_adopted_record "$pid" "$current_token" "$old_started" >/dev/null
    exe=__ECHOMANAGER_ADOPTED__
  elif adopted=$(adopt_port_listener); then
    pid=$(sed -n '1p' "$meta"); token=$(sed -n '2p' "$meta"); exe=$(sed -n '3p' "$meta")
  else
    if kill -0 "$pid" 2>/dev/null; then printf '__EM_STALE__\n'; exit 8; fi
    rm -f "$meta"; printf '__EM_STOPPED__\n'; exit 0
  fi
fi
kill -TERM "$pid" 2>/dev/null || true
i=0
while [ "$i" -lt 20 ] && process_matches "$pid" "$exe" "$token"; do sleep 0.25; i=$((i+1)); done
if process_matches "$pid" "$exe" "$token"; then kill -KILL "$pid" 2>/dev/null || true; fi
i=0
while [ "$i" -lt 8 ] && process_matches "$pid" "$exe" "$token"; do sleep 0.25; i=$((i+1)); done
if process_matches "$pid" "$exe" "$token"; then printf '__EM_ERROR__|进程未能退出'; exit 9; fi
rm -f "$meta"
	printf '__EM_STOPPED__\n'