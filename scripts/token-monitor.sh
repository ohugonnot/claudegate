#!/usr/bin/env bash
# Monitors Claude OAuth token expiry and logs changes.
# Run as: sudo -u claudegate nohup /opt/claudegate/scripts/token-monitor.sh &

CREDS="/home/claudegate/.claude/.credentials.json"
LOG="/home/claudegate/token-monitor.log"
INTERVAL=1800  # 30 minutes

last_expires=""

while true; do
    if [ -f "$CREDS" ]; then
        expires=$(python3 -c "
import json, datetime
d = json.load(open('$CREDS'))
ms = d['claudeAiOauth']['expiresAt']
exp = datetime.datetime.fromtimestamp(ms/1000, tz=datetime.timezone.utc)
now = datetime.datetime.now(tz=datetime.timezone.utc)
remain = exp - now
h = int(remain.total_seconds() // 3600)
m = int((remain.total_seconds() % 3600) // 60)
print(f'{ms}|{exp.strftime(\"%Y-%m-%d %H:%M:%S UTC\")}|{h}h{m:02d}m')
" 2>/dev/null)

        ts=$(date '+%Y-%m-%d %H:%M:%S')
        expires_ms=$(echo "$expires" | cut -d'|' -f1)
        expires_date=$(echo "$expires" | cut -d'|' -f2)
        remaining=$(echo "$expires" | cut -d'|' -f3)

        if [ "$expires_ms" != "$last_expires" ]; then
            echo "[$ts] TOKEN REFRESHED — expires: $expires_date (remaining: $remaining)" >> "$LOG"
            last_expires="$expires_ms"
        else
            echo "[$ts] unchanged — expires: $expires_date (remaining: $remaining)" >> "$LOG"
        fi
    else
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] credentials file not found" >> "$LOG"
    fi
    sleep $INTERVAL
done
