# TESTING — SoğukZincir nasıl doğrulanır

```bash
cp .env.example .env
docker compose up --build          # ilk açılış ~2,5 dk (derleme + seed: 2 000 sensör × 24 saat geçmiş)
make verify                        # her özellik + her zorlu görev, PASS/FAIL tablosu, hata varsa çıkış kodu ≠ 0
```

`make verify` yerine doğrudan: `docker compose --profile verify run --rm verify`. Süre ~30–60 sn. Sıfırdan tekrar etmek için `make reset-demo` (aynı `SEED` → aynı veri). Doğrulama sıfırlamadan tekrar çalıştırılabilir: senaryolar kendi verisini kendisi oluşturur.

## (a) Demo hesapları

Parola hepsinde **`Demo1234!`**. Arayüzde giriş ekranındaki butonlar e-postayı doldurur.

| Rol | E-posta | Görebildikleri |
|---|---|---|
| Depo operatörü | `depo@sogukzincir.test` | pano, parti kabulü, palet böl/birleştir/taşı, sensörler, HACCP kaydı |
| Şoför | `sofor@sogukzincir.test` | yalnızca kendi aracının sevkiyatları, çevrimdışı teslimat/sıcaklık |
| Şoför (2) | `sofor2@sogukzincir.test` | (başkasının sevkiyatına erişemediğini göstermek için) |
| Kalite sorumlusu | `kalite@sogukzincir.test` | alarmlar, uygunsuzluk, kalite kararı, sapma raporu, geri çağırma |
| Operasyon müdürü | `operasyon@sogukzincir.test` | sevkiyat oluşturma, FEFO planı, yükleme, izlenebilirlik |
| Müşteri (market) | `market@sogukzincir.test` | yalnızca kendi sevkiyatları + sıcaklık sertifikası |
| Müşteri (2) | `market2@sogukzincir.test` | (yukarıdakiyle izolasyon testi) |
| Denetçi | `denetci@sogukzincir.test` | her şeyi okur, hiçbir şeyi değiştiremez; denetim kaydı, zincir doğrulama |

Yetki matrisinin kaynağı: [`backend/internal/auth/rbac.go`](backend/internal/auth/rbac.go).

## (b) Doğrulama kimlikleri — nasıl denenir / beklenen sonuç

| Kimlik | Nasıl denenir (API) | Beklenen sonuç | `make verify` kontrolü |
|---|---|---|---|
| `HEALTH` | `GET /health` | `200 {"status":"ok","database":"ok","migration_version":5}`; DB kapalıyken `503` | HEALTH |
| Kimlik doğrulama | `POST /api/v1/auth/login` yanlış parola; tokensız/bozuk tokenla `GET /api/v1/lots`; eski refresh token'ı tekrar kullan | `401 invalid_credentials`; `401`; tekrar kullanımda `401` ve yeni token da iptal | AUTH (5) |
| Yetki ihlali | şoför `GET /lots`, müşteri `GET /audit`, denetçi `POST /products`, depo `POST /lots/{id}/decisions`, … ; market başka marketin `GET /customer/shipments/{id}` | `403 forbidden` + `required_permission`; başkasının sevkiyatı `404` | AUTHZ (10) |
| `F-1` Ürün ve parti | `POST /products` (geçersiz → 422), `POST /lots` `Idempotency-Key` ile iki kez, farklı gövdeyle aynı anahtar, aynı kod tekrar, `GET /lots?limit=20&cursor=…` | 201; ikinci istek aynı yanıt + `Idempotent-Replayed: true`; 422 `idempotency_key_reused`; 409; sayfalar çakışmaz | F-1 (5) |
| `F-2` Sensör verisi | `GET /sensors`, `GET /sensors/1/readings` (son 24 sa), `GET /sensors/health`, imzalı `POST /ingest/readings` | sıcaklık+nem serisi (~1 440 nokta, 1m), pil/bağlantı raporu, `202` | F-2 (4) |
| `F-3` Sevkiyat ve yükleme | `POST /shipments` → `POST /shipments/{id}/load` → `POST /shipments/{id}/depart`; yoldaki sevkiyata yükleme; yüklenmiş paleti başka sevkiyata yükleme | 201 → 200 → `in_transit`; 409 `shipment_closed`; 409 | F-3 (2) |
| `F-4` Alarm ve uygunsuzluk | `GET /alarms?status=open`, `POST /alarms/{id}/ack` (iki kez), `POST /alarms/{id}/close`, `POST /nonconformities` | onay 200, ikinci onay 409; uygunsuzluk partiyi `on_hold` yapar; depo operatörü onaylayamaz (403) | F-4 (3) |
| `F-5` Kalite kararı | `POST /lots/{id}/decisions` `discount 25`, ardından `accept`; 10 paralel karar; `destroy` | `discounted`; `409 invalid_transition`; paralelde tek "accept"; imhada stok 0 | F-5 (5) |
| `F-6` HACCP | `GET /haccp/checklists`, `POST /haccp/records` başarısız madde + düzeltici faaliyet yok / var | 422 / 201 `all_ok:false` | F-6 (3) |
| `F-7` İzlenebilirlik ve geri çağırma | `GET /trace/lots/{LOT-TRACE-001}`, `POST /recalls` (LOT-TRACE-002), aynı parti tekrar | 4 müşteri, 4 araç; geri çağırma 1 müşteri; tekrar 409 | F-7 (2) |
| `T-1` Sapma motoru | `GET /lots/{id}/evaluation` (beklemedeki parti); imzalı 11 °C okuma gönder ve `GET /alarms?rule=limit_high`; `GET /sensors/{id}/evaluation?product_id=1&policy=uncertain` ve `=interpolate` | kümülatif + pencere ihlali, MKT, raf ömrü cezası; 60 sn içinde alarm; aynı koşula tek canlı alarm; eskale alarm (seviye ≥1); aynı kesinti "belirsiz"de sapma, "doldur"da değil | T-1 (6) |
| `T-2` Zaman serisi | `GET /timeseries/stats`; 100× `GET /sensors/{rastgele}/readings` (1m) ve `?resolution=raw` | günlük partisyonlar + 1m/15m/1h + watermark; p95 < 100 ms (ölçülen ~11–12 ms) | T-2 (3) |
| `T-3` İzlenebilirlik | `GET /trace/lots/{id}` (`query_ms`); `GET /trace/units/{M-TRACE-01}`; API ile böl + birleştir + iz; 8 paralel bölme | < 1 sn (ölçülen ~3 ms); karışık palette 2 parti, araç ve sıcaklık geçmişi; birleşik palet izde; stok korunur | T-3 (4) |
| `T-4` Raf ömrü ve FEFO | `POST /shipments/{planlı}/plan` `{"lines":[{"product_id":12,"quantity":2000}]}`; elenen paleti `load`; 40 paletlik parti tek araca; aynı palet iki sevkiyata paralel | kısa raf ömürlü elenir, seçim SKT sırasına göre; 409 `shelf_life_too_short`; 409 `capacity_exceeded`; 200 + 409 | T-4 (4) |
| `T-5` Kanıtlanabilir kayıt + cihaz | `POST /lots/{id}/excursion-reports` ×2, `GET /excursion-reports/verify`; imzasız / yanlış anahtar / değişmiş gövde / replay / eski zaman damgası; şoför `POST /driver/sync` iki kez + ikinci teslim + başka şoför; `GET /audit` | rapor 2 `prev_hash` = rapor 1 `hash`, zincir geçerli; 401/401/401/409/401; `applied → already_synced`, `duplicate_delivery`, `rejected`; denetim kaydında `lot.decision`, `lot.recall` eski/yeni değerle | T-5 (7) |
| Platform | `GET /metrics`, `GET /openapi.yaml`, arayüz `/` ve `/api` vekili | Prometheus metrikleri, OpenAPI, arayüz + giriş | OPS (2), UI (1) |

## (c) Arayüzde elle denenecek kısa senaryolar

1. **Canlı pano** — `operasyon@…` ile gir. Üstte "≈ 66 ölçüm/sn" yanıp sönen gösterge; araç/soğuk oda karoları 15 sn'de bir yenilenir. Bir karoya tıkla → sensörün 24 saatlik grafiği (bant: min–maks, kesikli çizgi: limitler); 1 sa / 7 gün düğmeleriyle çözünürlüğün (ham → 15m) değiştiğini gör.
2. **Soğuk zincir kırılması → kalite kararı** — `kalite@…` → Partiler → "Beklemede" filtresi → bir parti. Kümülatif sapma, en kötü pencere, MKT, kalan raf ömrü (ceza) ve ihlal zamanları görünür. Gerekçe yaz → "İndirim %20" → *Kalite kararı ver*. Sonra "Kabul" dene → hata mesajı (geri alınamaz).
3. **Sapma raporu + zincir** — aynı partide *Sapma raporu oluştur* (iki kez). Sapma raporları → *Zinciri doğrula* → "Zincir geçerli".
4. **FEFO planı** — `operasyon@…` → Sevkiyatlar → "Planlandı" → ilk sevkiyat → ürün "Hazır Yemek Etli Nohut", 2000 adet → *Plan öner*. LOT-FEFO-1 kırmızıyla "kalan raf ömrü yol + güvenlik payından kısa" gerekçesiyle elenir, diğerleri SKT sırasıyla seçilir → *Önerilen paletleri yükle* → *Yola çıkar*.
5. **İzlenebilirlik / geri çağırma** — İzlenebilirlik → `LOT-TRACE-001` → bölünme ağacı (derinlik girintili), 4 müşteri, 4 araç. Parti sayfasından *Geri çağır*.
6. **Şoför çevrimdışı** — telefonda veya tarayıcı mobil görünümünde `sofor@…` → "Çevrimdışı modu dene"yi işaretle → yoldaki sevkiyatta *Sıcaklık kaydet* ve *Teslim et* → kayıtlar "bekleyen" listesinde. İşareti kaldır → otomatik gönderilir, "✓ işlendi"; sevkiyat "Teslim edildi". (Gerçek ağ kesintisi için DevTools → Network → Offline aynı sonucu verir.)
7. **Müşteri** — `market@…` → Sevkiyatlarım → bir sevkiyat → sıcaklık sertifikası → *PDF olarak kaydet / yazdır*.
8. **Yetki** — `sofor@…` ile adres çubuğuna `/lots` yaz → kendi ekranına döner; API'de aynı istek 403.
9. **Denetçi** — `denetci@…` → Denetim kaydı: az önceki karar, kim / ne zaman / eski / yeni değerle.

## Kanıt komutları (zorlu görevler)

| Görev | Komut | Ne kanıtlar |
|---|---|---|
| T-1 | `cd backend && go test -v -cover ./internal/domain/excursion/` | 15 test: limit, kümülatif bütçenin bittiği an, kayan pencere (10+10 dk ayrı ayrı uygun, birlikte değil), iki boşluk politikası, MKT'nin bağımsız hesapla eşleşmesi; %92,5 kapsam |
| T-1 | `make verify` → T-1 satırları | uçtan uca: imzalı sıcak okuma → canlı alarm, tekrarsızlık, eskalasyon, politika karşılaştırması |
| T-1 / T-4 | `cd backend && go test -v ./internal/domain/shelflife/` | Arrhenius raf ömrü; 18 °C'de Q10 ≈ 3,3 |
| T-2 | `make verify` → T-2; `make load` | p95 ölçümleri; k6 tablosu (README) |
| T-2 | README §EXPLAIN | partisyon budaması + indeks taraması; indeks olmadan 15,6 → 218,9 ms |
| T-3 | `./scripts/test-integration.sh -run 'Recall\|Split'` | < 1 sn iz, eşzamanlı bölmede stok korunumu |
| T-4 | `cd backend && go test -v ./internal/domain/loadplan/` | 300 rastgele örnekte DP = kaba kuvvet optimumu; açgözlü FEFO'nun kaybettiği örnek |
| T-4 | `./scripts/test-integration.sh -run RaceSamePallet` | aynı palet 10 sevkiyata aynı anda → tam bir başarı |
| T-5 | `cd backend && go test -v ./internal/domain/chain/ ./internal/domain/devicesig/` | zincir müdahalesi (içerik, zaman, silme, yeniden hash'leme), imza/replay/saat kayması |
| T-5 | `./scripts/test-integration.sh -run 'AppendOnly\|Tampering\|Replay'` | gerçek DB'de UPDATE/DELETE/TRUNCATE reddi; tetikleyiciyi kapatan DBA'nın değişikliği tam seq'te yakalanır; replay yarışında tek kopya geçer |
| Eşzamanlılık | `./scripts/test-integration.sh -run Race` | yarış durumları (palet, bölme, kalite kararı) |
| Kaos | `make chaos` | DB 60 sn kapalı: `/health` 503, API kendiliğinden toparlanır, kesintide ölçülen okumalar sonradan ulaşır |
| Tümü | `make test` + `./scripts/test-integration.sh` | 69 Go birim + 12 entegrasyon + 6 frontend testi |

## Elle deneme koleksiyonu

[`requests.http`](requests.http) (VS Code REST Client): her rol için giriş isteği tokenı değişkene alır; tüm uçlar demo hesaplarıyla çalışır. OpenAPI: <http://localhost:8080/docs>.
