#!/bin/bash
# Local regression only. Uses an isolated ClickHouse database and a temporary API server.
# CLICKHOUSE_URL=http://127.0.0.1:18123 bash test_account_sensitive.sh
set -euo pipefail
cd "$(dirname "$0")"
CLICKHOUSE_URL=${CLICKHOUSE_URL:-http://127.0.0.1:18123}
PORT=${ACCOUNT_TEST_PORT:-14017}
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
ch "INSERT INTO $db.audit SELECT 'o',today()-days,'app','User',account,visits,toDateTime(today()-days),toDateTime(today()-days),initializeAggregation('uniqMapState',map('phone',value)),initializeAggregation('uniqMapState',CAST(map(),'Map(String,String)')) FROM values('account String, days UInt16, visits UInt64, value String', ('old',20,10,'old'),('mixed',20,100,'old'),('mixed',0,3,'recent'),('included',6,1,'in'),('excluded',7,1,'out'))"
mkdir "$work/model"
sed "s/default\.audit/$db.audit/g" model/AuditView.yaml > "$work/model/AuditView.yaml"
# Test-only projection checks exact distinct values without requiring privacy_dict.
cat >> "$work/model/AuditView.yaml" <<'YAML'
    sensitiveValues:
      sql: uniqMapMerge(req_sens_uniq)['phone']
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
for _ in {1..10}; do
    if curl --max-time 2 -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then ready=1; break; fi
    sleep .1
done
[ "$ready" = 1 ] || { curl -sS "http://127.0.0.1:$PORT/health"; cat "$work/server.log"; exit 1; }
load() { curl -fsS "http://127.0.0.1:$PORT/load" -H 'Content-Type: application/json' -H 'X-Sw-Org: o' --data-binary "$1"; }
query='{"dimensions":["AuditView.content"],"measures":["AuditView.count","AuditView.firstTs","AuditView.lastTs","AuditView.accountStatus","AuditView.hasSensitive","AuditView.sensitiveValues"],"segments":["AuditView.org","AuditView.accountSensitive7d"],"order":{"AuditView.content":"asc"},"limit":10}'
load "$query" > "$work/recent.json"
load "$(jq -c '.segments=["AuditView.org"]' <<< "$query")" > "$work/all.json"
load "$(jq -c '.filters=[{member:"AuditView.hasSensitive",operator:"equals",values:["1"]}]' <<< "$query")" > "$work/filtered.json"
load '{"measures":["AuditView.accountAssetCount","AuditView.sensitiveAccountAssetCount"],"segments":["AuditView.org","AuditView.accountSensitive7d"]}' > "$work/counts.json"
python3 - "$work" <<'PY'
import json,sys,pathlib
p=pathlib.Path(sys.argv[1])
def rows(name): return json.loads((p/(name+'.json')).read_text())['results'][0]['data']
a={r['AuditView.content']:r for r in rows('all')}
b={r['AuditView.content']:r for r in rows('recent')}
assert len(a)==len(b)==4
for key in a:
 for field in ['count','firstTs','lastTs','accountStatus']:
  assert a[key]['AuditView.'+field]==b[key]['AuditView.'+field],(key,field)
assert int(b['mixed']['AuditView.count'])==103
assert int(a['mixed']['AuditView.sensitiveValues'])==2
assert {k:int(v['AuditView.sensitiveValues']) for k,v in b.items()}=={'old':0,'mixed':1,'included':1,'excluded':0}
assert {r['AuditView.content'] for r in rows('filtered')}=={'mixed','included'}
c=rows('counts')[0]
assert int(c['AuditView.accountAssetCount'])==4 and int(c['AuditView.sensitiveAccountAssetCount'])==2
print('PASS: seven-day boundary, exact sensitive values, unchanged base statistics, filter/count consistency, original audit unaffected')
PY
