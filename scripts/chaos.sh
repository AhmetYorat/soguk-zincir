#!/usr/bin/env bash
# Chaos scenario: stop PostgreSQL for OUTAGE seconds while the simulator keeps
# sending, then start it again and check that
#   1. /health reports the outage (503) instead of hanging or lying,
#   2. the API recovers by itself (connection pool reconnects),
#   3. readings measured during the outage arrive afterwards (device backlog),
#   4. no process had to be restarted by hand.
set -euo pipefail
cd "$(dirname "$0")/.."

API=${API:-http://localhost:8080}
OUTAGE=${OUTAGE:-60}
set -a; source .env; set +a

json() { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }
health() { curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API/health" || echo 000; }
pass=0; fail=0
ok()  { echo "  PASS  $1"; pass=$((pass+1)); }
bad() { echo "  FAIL  $1"; fail=$((fail+1)); }

echo "== Kaos senaryosu: veritabanı ${OUTAGE} sn kapatılıyor"
[[ $(health) == 200 ]] && ok "başlangıçta /health 200" || { bad "sistem ayakta değil"; exit 1; }
TOKEN=$(curl -s "$API/api/v1/auth/login" -d '{"email":"kalite@sogukzincir.test","password":"Demo1234!"}' | json 'd["access_token"]')
api_restarts_before=$(docker inspect -f '{{.RestartCount}}' "$(docker compose ps -q backend)")

down_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker compose stop db >/dev/null
echo "   $(date +%T) db durduruldu"

saw503=0
for _ in $(seq 1 $((OUTAGE / 3))); do
  code=$(health)
  [[ $code == 503 ]] && saw503=1
  sleep 3
done
[[ $saw503 == 1 ]] && ok "kesinti sırasında /health 503 (database: unreachable)" || bad "kesinti /health'te görünmedi"

docker compose start db >/dev/null
up_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "   $(date +%T) db yeniden başlatıldı"

start=$(date +%s)
until [[ $(health) == 200 ]]; do
  if (( $(date +%s) - start > 90 )); then bad "API 90 sn içinde toparlanmadı"; break; fi
  sleep 1
done
[[ $(health) == 200 ]] && ok "API $(( $(date +%s) - start )) sn içinde kendiliğinden toparlandı"

# devices retry every 30 s; give them two rounds to flush their backlog
echo "   cihaz tamponlarının boşalması bekleniyor (75 sn)…"
sleep 75
# every probe measures every 30 s, also while the database is down; count
# what was stored for the outage window (seed outages make a few gaps)
n=$(docker compose exec -T db psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
  "SELECT count(*) FROM readings WHERE ts >= '$down_at' AND ts < '$up_at'")
probes=$(docker compose exec -T db psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "SELECT count(*) FROM sensors")
expected=$(( probes * OUTAGE / 30 ))
if (( n * 100 >= expected * 95 )); then
  ok "kesinti aralığında ölçülen $n okuma sonradan ulaştı (beklenen ≈ $expected, kayıp yok)"
else
  bad "kesinti aralığında $n okuma var (beklenen ≈ $expected)"
fi

live=$(curl -s -H "Authorization: Bearer $TOKEN" "$API/api/v1/live" | json 'd["ingest"]["readings_per_second"]')
python3 -c "import sys; sys.exit(0 if float('$live') > 10 else 1)" && ok "veri akışı normale döndü ($live ölçüm/sn)" || bad "veri akışı düşük ($live ölçüm/sn)"

api_restarts_after=$(docker inspect -f '{{.RestartCount}}' "$(docker compose ps -q backend)")
[[ $api_restarts_before == "$api_restarts_after" ]] && ok "backend yeniden başlatılmadı (aynı süreç toparlandı)" || bad "backend yeniden başladı"

echo "== Sonuç: $pass başarılı, $fail başarısız"
[[ $fail == 0 ]]
