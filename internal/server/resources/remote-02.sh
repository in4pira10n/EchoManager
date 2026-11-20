set -eu
log="$HOME/.echomanager/logs/%s.log"
if [ ! -f "$log" ]; then printf '尚无远程运行日志。\n'; exit 0; fi
tail -c 262144 "$log"