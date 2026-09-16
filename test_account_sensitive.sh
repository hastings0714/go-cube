#!/bin/bash
# Local regression only. Uses an isolated ClickHouse database and a temporary API server.
# CLICKHOUSE_URL=http://127.0.0.1:18123 bash test_account_sensitive.sh
set -euo pipefail
cd "$(dirname "$0")"
if [ -z "${CLICKHOUSE_URL:-}" ]; then
    echo '[SKIP] account-sensitive integration: set CLICKHOUSE_URL to a disposable local ClickHouse'
    exit 0
fi

PORT=${ACCOUNT_TEST_PORT:-$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')}
work=$(mktemp -d)
db="account_sensitive_test_$$_$(date +%s)"
server_pid=''
ch() { curl -fsS "$CLICKHOUSE_URL" --data-binary "$1"; }
cleanup() {
    if [ "$?" -ne 0 ] && [ -f "$work/server.log" ]; then cat "$work/server.log"; fi
    if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
    ch "DROP DATABASE IF EXISTS $db" >/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT

ch "CREATE DATABASE $db"
ch "CREATE TABLE $db.audit (org String, dt Date, channel String, type String, content String, count UInt64, first_ts DateTime, last_ts DateTime, req_sens_uniq AggregateFunction(uniqMap, Map(String,String)), res_sens_uniq AggregateFunction(uniqMap, Map(String,String))) ENGINE=MergeTree PARTITION BY dt ORDER BY (org,dt,channel,type,content)"
ch "INSERT INTO $db.audit SELECT 'o',today()-days,'app','User',account,visits,toDateTime(today()-days),toDateTime(today()-days),initializeAggregation('uniqMapState',map('phone',value)),initializeAggregation('uniqMapState',map('email',value)) FROM values('account String, days UInt16, visits UInt64, value String', ('old',20,10,'old'),('mixed',20,100,'old'),('mixed',0,3,'recent'),('included',6,1,'in'),('excluded',7,1,'out'))"

mkdir "$work/model"
sed "s/default\.audit/$db.audit/g" model/AuditView.yaml > "$work/model/AuditView.yaml"
cat >> "$work/model/AuditView.yaml" <<'YAML'
    sensitiveValues:
      sql: uniqMapMerge(req_sens_uniq)['phone']
      type: number
    sensitiveValuesToday:
      sql: uniqMapMergeIf(req_sens_uniq, dt = today())['phone']
      type: number
    responseValuesToday:
      sql: uniqMapMergeIf(res_sens_uniq, dt = today())['email']
      type: number
YAML

cat > "$work/config.yaml" <<YAML
server:
  port: $PORT
  read_timeout: 30s
  write_timeout: 30s
clickhouse:
  hosts: ["$CLICKHOUSE_URL"]
  database: $db
  username: default
models:
  path: $work/model
YAML

if [ -n "${ACCOUNT_TEST_BINARY:-}" ]; then
    binary=$ACCOUNT_TEST_BINARY
else
    go build -o "$work/go-cube" .
    binary="$work/go-cube"
fi
"$binary" "$work/config.yaml" > "$work/server.log" 2>&1 &
server_pid=$!
ready=0
for _ in {1..100}; do
    if curl --max-time 2 -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then ready=1; break; fi
    kill -0 "$server_pid" 2>/dev/null || break
    sleep .2
done
[ "$ready" = 1 ] || { echo '[FAIL] temporary API server did not become healthy'; cat "$work/server.log"; exit 1; }

load() { curl -fsS "http://127.0.0.1:$PORT/load" -H 'Content-Type: application/json' -H 'X-Sw-Org: o' --data-binary "$1"; }
query='{"dimensions":["AuditView.content"],"measures":["AuditView.count","AuditView.firstTs","AuditView.lastTs","AuditView.accountStatus","AuditView.hasSensitiveToday","AuditView.sensitiveValues","AuditView.sensitiveValuesToday","AuditView.responseValuesToday"],"segments":["AuditView.org"],"order":{"AuditView.content":"asc"},"limit":10}'
load "$query" > "$work/rows.json"
load "$(jq -c '.filters=[{member:"AuditView.hasSensitiveToday",operator:"equals",values:["1"]}]' <<< "$query")" > "$work/filtered.json"
load '{"measures":["AuditView.accountAssetCount","AuditView.sensitiveAccountAssetCountToday"],"segments":["AuditView.org"]}' > "$work/counts.json"

python3 - "$work" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
def rows(name): return json.loads((p / (name + '.json')).read_text())['results'][0]['data']
data = {r['AuditView.content']: r for r in rows('rows')}
assert set(data) == {'old', 'mixed', 'included', 'excluded'}
assert int(data['mixed']['AuditView.count']) == 103
assert data['mixed']['AuditView.firstTs'] != data['mixed']['AuditView.lastTs']
assert int(data['mixed']['AuditView.sensitiveValues']) == 2
assert {k: int(v['AuditView.sensitiveValuesToday']) for k, v in data.items()} == {'old': 0, 'mixed': 1, 'included': 0, 'excluded': 0}
assert {k: int(v['AuditView.responseValuesToday']) for k, v in data.items()} == {'old': 0, 'mixed': 1, 'included': 0, 'excluded': 0}
assert {r['AuditView.content'] for r in rows('filtered')} == {'mixed'}
counts = rows('counts')[0]
assert int(counts['AuditView.accountAssetCount']) == 4
assert int(counts['AuditView.sensitiveAccountAssetCountToday']) == 1
print('[PASS] today-sensitive measures preserve full-history account statistics')
print('[PASS] today-sensitive filter and exact count use only today data')
PY
