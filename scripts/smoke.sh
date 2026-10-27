#!/usr/bin/env bash
# 端到端冒烟测试：起本地假书站 + 微服务，跑 REST 与 MCP 全流程，验证 TTL 回收。
#
#   ./scripts/smoke.sh
#
# 依赖：go、curl、python3（仅用于取 JSON 字段）。
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)
TMP=$(mktemp -d)
API=http://127.0.0.1:18080
SITE=http://127.0.0.1:18081

cleanup() {
  [[ -n "${SRV_PID:-}" ]] && kill "$SRV_PID" 2>/dev/null || true
  [[ -n "${FIX_PID:-}" ]] && kill "$FIX_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

say() { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
json() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
post() { local body=${2:-'{}'}; curl -sS -X POST "$API$1" -H 'Content-Type: application/json' -d "$body"; }
_q() { python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$1"; }

say "构建二进制"
go build -o "$TMP/fixtureserver" ./tools/fixtureserver
go build -o "$TMP/server" ./cmd/server

say "启动假书站 $SITE 与微服务 $API"
"$TMP/fixtureserver" -addr 127.0.0.1:18081 >"$TMP/fixture.log" 2>&1 & FIX_PID=$!
DEFAULT_TTL=5s SWEEP_INTERVAL=500ms "$TMP/server" -addr 127.0.0.1:18080 -quiet >"$TMP/server.log" 2>&1 & SRV_PID=$!
for _ in $(seq 1 50); do
  curl -sf "$API/healthz" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -sf "$API/healthz" >/dev/null || { echo "服务未启动"; cat "$TMP/server.log"; exit 1; }

say "1) 健康检查与元信息"
curl -sS "$API/healthz" | json 'd["status"], d["stats"]'
curl -sS "$API/api/v1/meta" | json 'd["name"], d["endpoints"][0]'

say "2) 校验书源配置"
CFG=$(cat examples/book-source.json)
VALID=$(post /api/v1/sources/validate "{\"config\":$CFG}")
echo "$VALID" | json 'd'

say "3) 创建实例"
CREATED=$(post /api/v1/instances "{\"config\":$CFG,\"ttl\":\"10m\",\"name\":\"冒烟源\"}")
echo "$CREATED" | json 'd["instance"]'
ID=$(echo "$CREATED" | json 'd["instance"]["id"]')

say "4) 搜索 → 详情 → 目录 → 正文"
SEARCH=$(post "/api/v1/instances/$ID/search" '{"key":"测试","page":1}')
echo "$SEARCH" | json 'd["count"], [b["name"] for b in d["books"]]'
BOOK=$(echo "$SEARCH" | json 'json.dumps(d["books"][0])')
BOOK_URL=$(echo "$SEARCH" | json 'd["books"][0]["bookUrl"]')

INFO=$(post "/api/v1/instances/$ID/bookinfo" "{\"book\":$BOOK}")
echo "$INFO" | json 'd["book"]["name"], d["book"]["author"], d["book"]["intro"]'
INFO_URL=$(echo "$INFO" | json 'd["book"]["bookUrl"]')
TOC_URL=$(echo "$INFO" | json 'd["book"]["tocUrl"]')

CH=$(post "/api/v1/instances/$ID/chapters" "{\"book\":{\"bookUrl\":$(_q "$INFO_URL"),\"tocUrl\":$(_q "$TOC_URL")}}")
echo "$CH" | json 'd["count"], [c["title"] for c in d["chapters"]]'
CHAPTER=$(echo "$CH" | json 'json.dumps(d["chapters"][0])')

CONTENT=$(post "/api/v1/instances/$ID/content" "{\"book\":{\"bookUrl\":$(_q "$INFO_URL")},\"chapter\":$CHAPTER}")
echo "$CONTENT" | json 'd["content"]["title"], repr(d["content"]["content"])'

say "5) 发现页 / JS 求值"
post "/api/v1/instances/$ID/explore" '{"page":1}' | json 'd["count"]'
post "/api/v1/instances/$ID/eval" '{"script":"key + \"@\" + page","key":"测试","page":2}' | json 'd["result"]'

say "6) 实例列表与续期"
curl -sS "$API/api/v1/instances" | json 'len(d["instances"]), d["stats"]'
post "/api/v1/instances/$ID/touch" '{"ttl":"30s"}' | json 'd["instance"]["ttlSeconds"]'

say "7) MCP：tools/list + 全流程"
MCP=$(curl -sS -X POST "$API/mcp" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')
echo "$MCP" | json 'len(d["result"]["tools"]), [t["name"] for t in d["result"]["tools"]][:4]'

MCP_CREATE=$(curl -sS -X POST "$API/mcp" -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"source_create\",\"arguments\":{\"config\":$CFG,\"ttlSeconds\":60}}}")
MID=$(echo "$MCP_CREATE" | json 'd["result"]["structuredContent"]["instance"]["id"]')
echo "mcp instance: $MID"

curl -sS -X POST "$API/mcp" -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"source_search\",\"arguments\":{\"instanceId\":\"$MID\",\"key\":\"MCP\"}}}" \
  | json 'd["result"]["structuredContent"]["count"], d["result"]["structuredContent"]["books"][0]["name"]'

say "8) TTL 回收：创建 1s 实例，休眠后应 410"
SHORT=$(post /api/v1/instances "{\"config\":$CFG,\"ttl\":\"1s\"}")
SID=$(echo "$SHORT" | json 'd["instance"]["id"]')
sleep 2.5
CODE=$(curl -sS -o "$TMP/expired.json" -w '%{http_code}' "$API/api/v1/instances/$SID")
echo "HTTP $CODE $(cat "$TMP/expired.json")"
[[ "$CODE" == "410" ]] || { echo "期望 410，实际 $CODE"; exit 1; }

say "9) 删除实例"
curl -sS -X DELETE -o /dev/null -w 'DELETE -> HTTP %{http_code}\n' "$API/api/v1/instances/$ID"
curl -sS -o /dev/null -w 'GET 删除后 -> HTTP %{http_code}\n' "$API/api/v1/instances/$ID"

say "冒烟测试全部通过 ✅"
