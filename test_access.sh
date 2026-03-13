#!/bin/bash
# Test AccessView queries against local go-cube server
# Mirrors production curl requests from demo.servicewall.cn

BASE="http://localhost:4000"
pass=0
fail=0

check() {
    local desc="$1"
    local result="$2"
    if echo "$result" | jq -e '.results[0].data' > /dev/null 2>&1; then
        count=$(echo "$result" | jq '.results[0].data | length')
        echo "[PASS] $desc — $count rows"
        ((pass++))
    else
        echo "[FAIL] $desc"
        echo "$result" | jq . 2>/dev/null || echo "$result"
        ((fail++))
    fi
}

echo "Starting go-cube server in background..."
./go-cube &
SERVER_PID=$!
sleep 2

echo ""
echo "Testing health endpoint..."
curl -s "$BASE/health" | jq .

echo ""
echo "========================================"
echo "=== AccessView aggregate queries ==="
echo "========================================"

echo ""
echo "=== 1. count by channel with time granularity (minute) ==="
# measures: [AccessView.count], timeDimensions: [{AccessView.ts, from 15 min ago, granularity: minute}]
# order: {AccessView.count: desc}, dimensions: [AccessView.channel], segments: org+black
result=$(curl -s "$BASE/load?queryType=multi&query=%7B%22measures%22%3A%5B%22AccessView.count%22%5D%2C%22timeDimensions%22%3A%5B%7B%22dimension%22%3A%22AccessView.ts%22%2C%22dateRange%22%3A%22from+15+minutes+ago+to+15+minutes+from+now%22%2C%22granularity%22%3A%22minute%22%7D%5D%2C%22order%22%3A%7B%22AccessView.count%22%3A%22desc%22%7D%2C%22filters%22%3A%5B%5D%2C%22dimensions%22%3A%5B%22AccessView.channel%22%5D%2C%22segments%22%3A%5B%22AccessView.org%22%2C%22AccessView.black%22%5D%2C%22timezone%22%3A%22Asia%2FShanghai%22%7D")
check "count by channel (granularity=minute)" "$result"

echo ""
echo "=== 2. count by ipGeoProvince filtered by country (中国/局域网/内网) ==="
# measures: [AccessView.count], filter: ipGeoCountry equals [中国,局域网,内网]
# dimensions: [AccessView.ipGeoProvince], segments: org+black
result=$(curl -s "$BASE/load?queryType=multi&query=%7B%22measures%22%3A%5B%22AccessView.count%22%5D%2C%22timeDimensions%22%3A%5B%7B%22dimension%22%3A%22AccessView.ts%22%2C%22dateRange%22%3A%22from+15+minutes+ago+to+15+minutes+from+now%22%7D%5D%2C%22filters%22%3A%5B%7B%22member%22%3A%22AccessView.ipGeoCountry%22%2C%22operator%22%3A%22equals%22%2C%22values%22%3A%5B%22%E4%B8%AD%E5%9B%BD%22%2C%22%E5%B1%80%E5%9F%9F%E7%BD%91%22%2C%22%E5%86%85%E7%BD%91%22%5D%7D%5D%2C%22dimensions%22%3A%5B%22AccessView.ipGeoProvince%22%5D%2C%22segments%22%3A%5B%22AccessView.org%22%2C%22AccessView.black%22%5D%2C%22timezone%22%3A%22Asia%2FShanghai%22%7D")
check "count by ipGeoProvince where country in [中国,局域网,内网]" "$result"

echo ""
echo "=== 3. count by status (notEquals '') limit 10 ==="
# measures: [AccessView.count], filter: status notEquals ['']
# dimensions: [AccessView.status], limit: 10, segments: org+black
result=$(curl -s "$BASE/load?queryType=multi&query=%7B%22measures%22%3A%5B%22AccessView.count%22%5D%2C%22timeDimensions%22%3A%5B%7B%22dimension%22%3A%22AccessView.ts%22%2C%22dateRange%22%3A%22from+15+minutes+ago+to+15+minutes+from+now%22%7D%5D%2C%22filters%22%3A%5B%7B%22dimension%22%3A%22AccessView.status%22%2C%22operator%22%3A%22notEquals%22%2C%22values%22%3A%5B%22%22%5D%7D%5D%2C%22dimensions%22%3A%5B%22AccessView.status%22%5D%2C%22limit%22%3A10%2C%22segments%22%3A%5B%22AccessView.org%22%2C%22AccessView.black%22%5D%2C%22timezone%22%3A%22Asia%2FShanghai%22%7D")
check "count by status where status != '' limit 10" "$result"

echo ""
echo "=== 4. count by uaOs limit 10 ==="
# measures: [AccessView.count], dimensions: [AccessView.uaOs], limit: 10, segments: org+black
result=$(curl -s "$BASE/load?queryType=multi&query=%7B%22measures%22%3A%5B%22AccessView.count%22%5D%2C%22timeDimensions%22%3A%5B%7B%22dimension%22%3A%22AccessView.ts%22%2C%22dateRange%22%3A%22from+15+minutes+ago+to+15+minutes+from+now%22%7D%5D%2C%22filters%22%3A%5B%5D%2C%22dimensions%22%3A%5B%22AccessView.uaOs%22%5D%2C%22limit%22%3A10%2C%22segments%22%3A%5B%22AccessView.org%22%2C%22AccessView.black%22%5D%2C%22timezone%22%3A%22Asia%2FShanghai%22%7D")
check "count by uaOs limit 10" "$result"

echo ""
echo "=== 5. count by urlRoute+channel+host+method limit 1000 ==="
# measures: [AccessView.count], dimensions: [urlRoute, channel, host, method]
# limit: 1000, segments: org+black
result=$(curl -s "$BASE/load?queryType=multi&query=%7B%22measures%22%3A%5B%22AccessView.count%22%5D%2C%22timeDimensions%22%3A%5B%7B%22dimension%22%3A%22AccessView.ts%22%2C%22dateRange%22%3A%22from+15+minutes+ago+to+15+minutes+from+now%22%7D%5D%2C%22filters%22%3A%5B%5D%2C%22dimensions%22%3A%5B%22AccessView.urlRoute%22%2C%22AccessView.channel%22%2C%22AccessView.host%22%2C%22AccessView.method%22%5D%2C%22limit%22%3A1000%2C%22segments%22%3A%5B%22AccessView.org%22%2C%22AccessView.black%22%5D%2C%22timezone%22%3A%22Asia%2FShanghai%22%7D")
check "count by urlRoute+channel+host+method limit 1000" "$result"

echo ""
echo "=== 6. count by ip limit 1000 ==="
# measures: [AccessView.count], dimensions: [AccessView.ip]
# limit: 1000, segments: org+black
result=$(curl -s "$BASE/load?queryType=multi&query=%7B%22measures%22%3A%5B%22AccessView.count%22%5D%2C%22timeDimensions%22%3A%5B%7B%22dimension%22%3A%22AccessView.ts%22%2C%22dateRange%22%3A%22from+15+minutes+ago+to+15+minutes+from+now%22%7D%5D%2C%22filters%22%3A%5B%5D%2C%22dimensions%22%3A%5B%22AccessView.ip%22%5D%2C%22limit%22%3A1000%2C%22segments%22%3A%5B%22AccessView.org%22%2C%22AccessView.black%22%5D%2C%22timezone%22%3A%22Asia%2FShanghai%22%7D")
check "count by ip limit 1000" "$result"

echo ""
echo "--- $pass passed, $fail failed ---"

echo ""
echo "Stopping server..."
kill $SERVER_PID
wait $SERVER_PID 2>/dev/null
echo "All tests completed."
