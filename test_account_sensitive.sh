#!/bin/bash
# Local regression only. Uses an isolated ClickHouse database and a temporary API server.
# CLICKHOUSE_URL=http://127.0.0.1:18123 bash test_account_sensitive.sh
set -euo pipefail
cd "$(dirname "$0")"
# make test discovers test*.sh. Creating a database requires explicit opt-in.
if [ -z "${CLICKHOUSE_URL:-}" ]; then
    echo '[SKIP] account-sensitive integration: set CLICKHOUSE_URL to a disposable local ClickHouse'
    exit 0
fi
PORT=${ACCOUNT_TEST_PORT:-$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')}
work=$(mktemp -d)
db="account_sensitive_test_$$_$(date +%s)"
server_pid=''
proxy_pid=''
ch() { curl -fsS "$CLICKHOUSE_URL" --data-binary "$1"; }
cleanup() {
    if [ "$?" -ne 0 ] && [ -f "$work/server.log" ]; then cat "$work/server.log"; fi
    if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
    if [ -n "$proxy_pid" ]; then kill "$proxy_pid" 2>/dev/null || true; wait "$proxy_pid" 2>/dev/null || true; fi
    ch "DROP DATABASE IF EXISTS $db" >/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT
ch "CREATE DATABASE $db"
ch "CREATE TABLE $db.audit (org String, dt Date, channel String, type String, content String, count UInt64, first_ts DateTime, last_ts DateTime, req_sens_uniq AggregateFunction(uniqMap, Map(String,String)), res_sens_uniq AggregateFunction(uniqMap, Map(String,String))) ENGINE=MergeTree PARTITION BY dt ORDER BY (org,dt,channel,type,content)"
ch "INSERT INTO $db.audit SELECT 'o',today()-days,'app','User',account,visits,toDateTime(today()-days),toDateTime(today()-days),initializeAggregation('uniqMapState',map('phone',value)),initializeAggregation('uniqMapState',map('email',value)) FROM values('account String, days UInt16, visits UInt64, value String', ('old',20,10,'old'),('mixed',20,100,'old'),('mixed',0,3,'recent'),('included',6,1,'in'),('excluded',7,1,'out'))"
mkdir "$work/model"
sed "s/default\.audit/$db.audit/g" model/AuditView.yaml > "$work/model/AuditView.yaml"
# Test-only projection checks exact distinct values without requiring privacy_dict.
cat >> "$work/model/AuditView.yaml" <<'YAML'
    sensitiveValues:
      sql: uniqMapMerge(req_sens_uniq)['phone']
      type: number
    responseValues:
      sql: uniqMapMerge(res_sens_uniq)['email']
      type: number
YAML
# Contract-only models exercise source_sql validation through the HTTP API.
cat > "$work/model/SourceContractView.yaml" <<YAML
cube:
  name: SourceContractView
  sql_table: $db.audit
  dimensions:
    content:
      sql: content
      type: string
  segments:
    valid:
      source_sql: (SELECT * FROM {source}) AS valid_source
    second:
      source_sql: (SELECT * FROM {source}) AS second_source
    missingPlaceholder:
      source_sql: (SELECT * FROM $db.audit) AS missing_source
YAML
cat > "$work/model/UnresolvedSourceView.yaml" <<'YAML'
cube:
  name: UnresolvedSourceView
  sql_table: "{vars.missing_source}"
  dimensions:
    content:
      sql: content
      type: string
  segments:
    wrapped:
      source_sql: (SELECT * FROM {source}) AS wrapped_source
YAML
# Capture the actual SQL sent by the API and explain it against the same fixture.
python3 - "$CLICKHOUSE_URL" "$work" <<'PYPROXY' &
import http.server, pathlib, sys, urllib.request, urllib.error
backend, work = sys.argv[1].rstrip('/'), pathlib.Path(sys.argv[2])
class Proxy(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_POST(self):
        body = self.rfile.read(int(self.headers['Content-Length']))
        try:
            if b'AS account_audit' in body and not (work/'query.sql').exists():
                plan = urllib.request.urlopen(urllib.request.Request(backend, data=b'EXPLAIN actions=1, indexes=1 '+body), timeout=30).read()
                (work/'explain.txt').write_bytes(plan)
                (work/'query.sql').write_bytes(body)
            data = urllib.request.urlopen(urllib.request.Request(backend+self.path, data=body), timeout=30).read()
            self.send_response(200); self.end_headers(); self.wfile.write(data)
        except urllib.error.HTTPError as e:
            self.send_response(e.code); self.end_headers(); self.wfile.write(e.read())
server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Proxy)
(work/'proxy.port').write_text(str(server.server_port))
server.serve_forever()
PYPROXY
proxy_pid=$!
for _ in {1..50}; do [ -s "$work/proxy.port" ] && break; sleep .1; done
[ -s "$work/proxy.port" ]
proxy_port=$(cat "$work/proxy.port")
cat > "$work/config.yaml" <<YAML
server:
  port: $PORT
  read_timeout: 30s
  write_timeout: 30s
clickhouse:
  hosts: ["http://127.0.0.1:$proxy_port"]
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
[ "$ready" = 1 ] || { echo "[FAIL] temporary API server did not become healthy"; cat "$work/server.log"; exit 1; }
load() { curl -fsS "http://127.0.0.1:$PORT/load" -H 'Content-Type: application/json' -H 'X-Sw-Org: o' --data-binary "$1"; }
expect_error() {
    expected=$1
    request=$2
    status=$(curl -sS -o "$work/error.json" -w '%{http_code}' "http://127.0.0.1:$PORT/load" -H 'Content-Type: application/json' --data-binary "$request")
    [ "$status" = 500 ]
    jq -e --arg expected "$expected" '.error | contains($expected)' "$work/error.json" >/dev/null
}
query='{"dimensions":["AuditView.content"],"measures":["AuditView.count","AuditView.firstTs","AuditView.lastTs","AuditView.accountStatus","AuditView.hasSensitive","AuditView.sensitiveValues","AuditView.responseValues"],"segments":["AuditView.org","AuditView.accountSensitiveToday"],"order":{"AuditView.content":"asc"},"limit":10}'
load "$query" > "$work/recent.json"
load "$(jq -c '.segments=["AuditView.org"]' <<< "$query")" > "$work/all.json"
load "$(jq -c '.filters=[{member:"AuditView.hasSensitive",operator:"equals",values:["1"]}]' <<< "$query")" > "$work/filtered.json"
load '{"measures":["AuditView.accountAssetCount","AuditView.sensitiveAccountAssetCount"],"segments":["AuditView.org","AuditView.accountSensitiveToday"]}' > "$work/counts.json"
load '{"dimensions":["SourceContractView.content"],"segments":["SourceContractView.valid"],"limit":1}' >/dev/null
expect_error 'source_sql must contain {source}' '{"dimensions":["SourceContractView.content"],"segments":["SourceContractView.missingPlaceholder"]}'
expect_error 'multiple source segments are not supported' '{"dimensions":["SourceContractView.content"],"segments":["SourceContractView.valid","SourceContractView.second"]}'
expect_error 'source has unresolved variables' '{"dimensions":["UnresolvedSourceView.content"],"segments":["UnresolvedSourceView.wrapped"]}'
python3 - "$work" <<'PY'
import json,sys,pathlib,re
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
assert int(a['mixed']['AuditView.responseValues'])==2
assert {k:int(v['AuditView.sensitiveValues']) for k,v in b.items()}=={'old':0,'mixed':1,'included':0,'excluded':0}
assert {k:int(v['AuditView.responseValues']) for k,v in b.items()}=={'old':0,'mixed':1,'included':0,'excluded':0}
assert {r['AuditView.content'] for r in rows('filtered')}=={'mixed'}
c=rows('counts')[0]
assert int(c['AuditView.accountAssetCount'])==4 and int(c['AuditView.sensitiveAccountAssetCount'])==1
print('[PASS] today request/response boundaries and exact values; base statistics and original audit unchanged')
print('[PASS] sensitive filter/count consistency')
query=(p/'query.sql').read_text()
assert 'default.audit' not in query, 'source_sql must honor the model source override'
branches=(p/'explain.txt').read_text().split('ReadFromMergeTree')[1:]
outputs=[re.search(r'Output: ([^\n]+)', branch).group(1) for branch in branches]
assert len(outputs)==2, outputs
assert 'req_sens_uniq' in outputs[0] and 'res_sens_uniq' in outputs[0], outputs
assert 'req_sens_uniq' not in outputs[1] and 'res_sens_uniq' not in outputs[1], outputs
assert 'count' in outputs[1] and 'first_ts' in outputs[1], outputs
print('[PASS] EXPLAIN: old branch excludes both sensitive columns; overridden source is used')
PY
echo '[PASS] source_sql contract: replacement and validation errors'
