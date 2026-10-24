# SoğukZincir — Gıda Dağıtım Soğuk Zincir İzleme ve Uyum Sistemi

**Ahmet Derviş Yorat** · DDO Yazılım Ekibi alım projesi

Depo ve frigorifik araçlardaki 2 000 sıcaklık/nem sensörünü izleyen; parti izlenebilirliğini, HACCP kayıtlarını, kalite kararlarını ve FEFO sevkiyat planlamasını yöneten sistem. Soğuk zincir kırıldığında ürünün akıbeti (hangi palet, hangi araç, hangi sıcaklık, hangi müşteri, hangi karar) değiştirilemez kayıtlarla kanıtlanabilir.

| | |
|---|---|
| Backend | Go 1.27 · `chi` · `pgx/v5` · `goose` · Prometheus |
| Veritabanı | PostgreSQL 16 (yerel partisyonlama, recursive CTE, satır kilitleri, tetikleyiciler) |
| Frontend | React 18 · Vite · TypeScript (UI kütüphanesi yok) |
| Çalıştırma | Docker Compose: `db`, `migrate`, `backend`, `simulator`, `frontend` (+ `verify`, `k6` profilleri) |
| Doğrulama | `make verify` → 67 uçtan uca kontrol, PASS/FAIL tablosu |

---

## Kurulum

```bash
git clone https://github.com/AhmetYorat/soguk-zincir.git && cd soguk-zincir
cp .env.example .env
docker compose up --build
```

- **Arayüz:** <http://localhost:3000> · **API:** <http://localhost:8080> · **API dokümanı (OpenAPI):** <http://localhost:8080/docs>
- Boş derleme önbelleğiyle ilk açılış bu makinede **150 sn**: imaj derleme 70 sn, migrasyon + deterministik seed ~70 sn (2 000 sensör × 24 saat = 5,75 M ölçüm). Temel imajların (`golang`, `node`, `postgres`, `nginx`, `distroless`) indirilmesi ağ hızına göre buna eklenir. Bütün servislerde `healthcheck` var; bağımlılıklar `service_healthy` / `service_completed_successfully` ile sıralanıyor.
- Migrasyon ve seed `migrate` servisinde otomatik çalışır; veritabanı zaten doluysa seed atlanır. Veritabanı host'a açılmaz.
- Tüm konteynerler root olmayan kullanıcıyla çalışır: backend distroless `nonroot` (uid 65532), frontend `nginx-unprivileged` (uid 101). İmajlar çok aşamalı derlenir (backend 46 MB, üç binary ile; frontend 76 MB).

| Komut | Ne yapar |
|---|---|
| `make verify` | Ayaktaki sistemde her özelliği (F-1…F-7), her zorlu görevi (T-1…T-5), sağlık, kimlik ve yetki senaryolarını API ve arayüz vekili üzerinden dener; tablo basar, hata varsa çıkış kodu ≠ 0 (~35 sn) |
| `make reset-demo` | Verileri silip aynı `SEED` ile yeniden üretir |
| `make load` | k6 yük testi (2 dk) ve sonuç tablosu |
| `make chaos` | Veritabanını 60 sn durdurur, toparlanmayı ve veri kaybı olmadığını doğrular |
| `make test` · `./scripts/test-integration.sh` | Birim testleri · gerçek PostgreSQL'e karşı entegrasyon testleri |

### Demo hesapları (parola: `Demo1234!`)

| Rol | E-posta |
|---|---|
| Depo operatörü | `depo@sogukzincir.test` |
| Şoför | `sofor@sogukzincir.test` (ikinci: `sofor2@…`) |
| Kalite sorumlusu | `kalite@sogukzincir.test` |
| Operasyon müdürü | `operasyon@sogukzincir.test` |
| Müşteri (market) | `market@sogukzincir.test` (ikinci: `market2@…`) |
| Denetçi | `denetci@sogukzincir.test` |

Her rolün neyi görebildiği, her doğrulama kimliğinin nasıl deneneceği ve arayüz senaryoları: **[TESTING.md](TESTING.md)**. Elle deneme koleksiyonu: **[requests.http](requests.http)**. Mimari kararlar: **[DECISIONS.md](DECISIONS.md)**.

---

## Mimari özeti

```
  2 000 sensör (simulator)                Tarayıcı / telefon
  HMAC imzalı, 30 sn'de bir                React SPA  :3000 (nginx, /api vekili)
            │                                       │
            ▼                                       ▼
  ┌───────────────────────────── Go API :8080 ─────────────────────────────┐
  │ Observe (istek id, JSON log, metrik, panic) → güvenlik başlıkları →     │
  │ CORS → timeout → [authenticate → require(izin) → idempotent] → handler  │
  │                                                                         │
  │ ingest: imza + saat + atomik seq → bellek tamponu ──(1 sn, COPY)──┐     │
  │ domain (saf Go, DB yok): excursion · shelflife · loadplan ·       │     │
  │                          chain · devicesig                        │     │
  │ arka plan işleri: rollup 1m→15m→1h · partisyon + sıkıştırma ·     │     │
  │   sensör değerlendirici · parti değerlendirici · eskalasyon       │     │
  └───────────────────────────────────────────────────────────────────┼─────┘
                                                                      ▼
                          PostgreSQL 16 (yalnızca compose ağında)
   readings (günlük partisyon) · readings_1m/15m/1h · lots · units · unit_lineage
   custody_events · shipments · alarms · quality_decisions · haccp_* · recalls
   audit_log + excursion_reports (append-only tetikleyici, hash zinciri)
```

**Backend paketleri** (`backend/internal`): `domain/*` veritabanını bilmeyen saf iş kuralları ve testleri; `api` HTTP uçları, değerlendirici ve parti zaman çizelgesi servisi; `httpx` hata biçimi, sayfalama ve ara katmanlar; `auth` argon2id, JWT, RBAC; `ingest` toplu yazma; `jobs` özetleme; `db` havuz, migrasyon, partisyon bakımı; `seed` deterministik demo verisi (yerleşimi simülatörle paylaşır).

**Akış örnekleri**

- *Ölçüm:* simülatör imzalar → API anahtarı (AES-GCM ile şifreli saklanır) açar, HMAC ve ±2 dk saat penceresini kontrol eder → `UPDATE sensors SET last_seq … WHERE last_seq < $seq` (replay engeli, atomik) → tampon → `COPY` + `ON CONFLICT DO NOTHING`. Özetleme işi her 30 sn'de 1m/15m/1h kovalarını günceller; değerlendirici son pencereyi ürün kurallarına göre işler, alarmları tekrarsız (kısmi UNIQUE indeks) üretir, onaylanmayanları 10 dk'da bir eskale eder.
- *Parti kararı:* parti değerlendirmesi, partiyi taşıyan her paletin soy ağacındaki yolunu (recursive CTE, yalnız o partinin kenarları) ve her duraktaki sensör okumalarını toplayıp sapma motorundan geçirir; kümülatif limit aşılırsa parti otomatik `on_hold` olur. Kalite sorumlusu kararı satır kilidi altında verir; karar ve rapor değiştirilemez kayıtlara düşer.
- *Sevkiyat:* plan (FEFO + 0/1 sırt çantası) yalnızca öneridir; yükleme ayrı bir işlemde araç, sevkiyat ve palet satırlarını kilitleyip kapasiteyi, parti durumunu ve kalan raf ömrünü yeniden kontrol eder.

### Teknoloji ve karar gerekçeleri (özet — ayrıntı DECISIONS.md)

- **ORM yok**, `pgx` + elle SQL: sistemin değeri sorgularda; her sorgu `EXPLAIN` ile ölçülebilir (ADR-01).
- **Tamsayı ölçüler** (santi-derece, binde nem, kuruş): `float` karşılaştırma belirsizliği yok, satır 16 bayt veri (ADR-02).
- **Yerel partisyonlama + aşamalı özetleme**, TimescaleDB değil: yığın PostgreSQL 16; özetler `sum + count` tutar ve üst seviye alt seviyeden tam hesaplanır (ADR-03).
- **Sapma motoru saf Go**: boşluk politikası, doğrusal kesişim noktası, kayan pencere SQL'de test edilemez hale geliyordu (ADR-06).
- **Arrhenius raf ömrü**, MKT ile aynı model; limit altı "kredi" vermez (ADR-08).
- **FEFO + sırt çantası DP**: açgözlü FEFO'nun kapasiteyi boşa harcadığı durum testte gösteriliyor (ADR-09).
- **Açık kilitler** (`FOR UPDATE`, koşullu `UPDATE`, UNIQUE, kilit sırası) — `SERIALIZABLE` yerine (ADR-10).
- **Tetikleyici + hash zinciri** birlikte: biri kazayı engeller, diğeri kasıtlı müdahaleyi görünür kılar (ADR-11).

**Frontend seçimi: React + Vite + TypeScript.** Altı rol, çok sayıda form/tablo, canlı pano ve çevrimdışı çalışması gereken şoför ekranı var. HTMX ile Go şablonları da düşünüldü; ancak çevrimdışı kuyruk ve canlı grafik için yine istemci tarafı durum gerekecekti, iki paradigmayı karıştırmak yerine tek bir SPA seçildi. Bağımlılık yalnızca `react-router-dom`; grafik elle SVG. TypeScript, API yanıt tiplerini ekranlarda tutarlı tutuyor. Üretimde nginx aynı origin'den `/api`'yi backend'e vekiller (CORS gerekmez).

---

## T-2: Kapasite hesabı

Ölçümler bu sistemde, seed sonrası `pg_relation_size` / `pg_indexes_size` / `reltuples` ile alınmıştır (`GET /api/v1/timeseries/stats` partisyonları canlı gösterir).

| Seviye | Satır/gün | Bayt/satır (tablo + indeks, ölçülen) | Disk/gün | Saklama | Kalıcı hacim |
|---|---:|---:|---:|---:|---:|
| ham `readings` (30 sn) | 2 000 × 2 880 = **5 760 000** | 52 + 45 = 97 B | ~560 MB | 7 gün | ~3,9 GB |
| `readings_1m` | 2 880 000 | 71 + 40 = 111 B | ~320 MB | 30 gün | ~9,6 GB |
| `readings_15m` | 192 000 | 84 + 46 = 130 B | ~25 MB | 180 gün | ~4,5 GB |
| `readings_1h` | 48 000 | 92 + 46 = 138 B | ~6,6 MB | 2 yıl | ~4,8 GB |
| **Toplam** | | | **~910 MB/gün** | | **~23 GB** (WAL/yedek hariç) |

- Yazma hızı: 2 000 / 30 sn = **66,7 satır/sn** ortalama; ingest saniyede bir toplu `COPY` ile yazar (k6'da 2× yükte p95 2 ms).
- Saklama `DROP TABLE <partisyon>` ile yapılır (milyonlarca `DELETE` + VACUUM yerine O(1)).
- 1m seviyesi en büyük kalem; saklama 14 güne indirilirse toplam ~18 GB'a düşer. Tek ham satırın 52 baytının 24'ü PostgreSQL satır başlığıdır; ileride kova başına dizi (`smallint[]`) saklamak ham hacmi yaklaşık 5 kat küçültür (bkz. Sınırlamalar).

## EXPLAIN (ANALYZE) — kritik sorgular

**1) Bir sensörün son 24 saati (ham, 2 880 satır).** Partisyon budaması iki günlük partisyona iner, birincil anahtar `(sensor_id, ts)` ile indeks taraması.

```
-- sıkıştırılmış (CLUSTER) partisyonlar, soğuk önbellek
Sort (actual time=4.910..5.195 rows=2873 loops=1)
  ->  Append (actual time=0.987..3.738 rows=2873 loops=1)
        ->  Bitmap Heap Scan on readings_p20261023 (actual time=0.986..1.748 rows=1492 loops=1)
              Heap Blocks: exact=24
              ->  Bitmap Index Scan on readings_p20261023_pkey (actual time=0.956..0.957 rows=1492 loops=1)
        ->  Bitmap Heap Scan on readings_p20261024 (actual time=0.667..1.572 rows=1381 loops=1)
              Heap Blocks: exact=24
              ->  Bitmap Index Scan on readings_p20261024_pkey (actual time=0.634..0.634 rows=1381 loops=1)
Execution Time: 5.598 ms

-- aynı sorgu, satırlar zaman sırasıyla iç içe yazılmışken (sıkıştırma öncesi)
        ->  Bitmap Heap Scan on readings_p20261023 (actual time=0.405..23.299 rows=1519 loops=1)
              Heap Blocks: exact=1509
Execution Time: 40.882 ms            (sıcak önbellekte 15.6 ms)

-- aynı sorgu, birincil anahtar indeksi yokken
Gather Merge ... ->  Parallel Seq Scan on readings_p20261023
                       Rows Removed by Filter: 1033230
Execution Time: 218.892 ms
```

**2) Aynı 24 saat, 1 dk özetten (arayüzün varsayılanı):** `Bitmap Index Scan on readings_1m_p…_pkey`, 1 440 satır, **1,05 ms**.

**3) Geri çağırma — partinin ileri izi (recursive CTE).** Seed verisi (1 527 kenar) yanında 20 000 parti / 400 000 paletlik sentetik bir yıllık hacimle (401 585 kenar):

```
-- indekslerle
->  Index Scan using unit_lineage_receive_lot_idx on unit_lineage (actual time=0.040..0.041 rows=1 loops=1)
      ->  Index Scan using unit_lineage_parent_idx on unit_lineage l (actual time=0.003..0.004 rows=1 loops=20)
Execution Time: 0.204 ms

-- unit_lineage indeksleri olmadan
->  Parallel Seq Scan on unit_lineage (actual time=6.869..10.899 rows=0 loops=3)
      ->  Parallel Seq Scan on unit_lineage l (actual time=1.702..9.899 rows=7 loops=15)
Execution Time: 87.603 ms
```

Diğer kritik dizinler: `alarms_dedup_live_uq` (kısmi UNIQUE, `WHERE status <> 'closed'`: tekrarsız alarm ve `ON CONFLICT … WHERE` hedefi), `custody_one_open_uq` (palet başına tek açık konum), `units_location_idx` (kısmi, `WHERE status = 'active'`: FEFO adayları), `audit_log_entity_idx`.

## Yük testi (k6, `make load`)

2 dakika, sabit varış hızlı dört senaryo aynı anda; cihaz trafiği gerçek yükün 2 katı. Simülatör bu sırada durdurulur (k6 cihazları oynar).

| Senaryo | Hız | İstek | p50 (ms) | p95 (ms) | p99 (ms) | maks (ms) | Hata oranı |
|---|---:|---:|---:|---:|---:|---:|---:|
| ingest (imzalı cihaz mesajı) | 135/sn | 16 201 | 0,4 | 2,0 | 3,2 | 21,6 | 0,00 % |
| read_24h (rastgele sensör, son 24 sa) | 40/sn | 4 801 | 5,6 | 7,1 | 8,0 | 40,0 | 0,00 % |
| dashboard (canlı sayaç + alarmlar) | 20/sn | 4 800 | 0,7 | 1,4 | 2,4 | 31,0 | 0,00 % |
| trace (recursive geri çağırma) | 5/sn | 601 | 1,6 | 3,3 | 5,9 | 17,6 | 0,00 % |

Toplam 26 405 istek, ortalama 220 istek/sn, genel hata oranı %0,00. Eşikler (`load/k6.js`): ingest ve 24 saat p95 < 100 ms, pano < 200 ms, iz < 1 sn, hata < %1.

## Kaos senaryosu (`make chaos`)

```
== Kaos senaryosu: veritabanı 60 sn kapatılıyor
  PASS  başlangıçta /health 200
  PASS  kesinti sırasında /health 503 (database: unreachable)
  PASS  API 1 sn içinde kendiliğinden toparlandı
  PASS  kesinti aralığında ölçülen 3990 okuma sonradan ulaştı (beklenen ≈ 4000, kayıp yok)
  PASS  veri akışı normale döndü (132.97 ölçüm/sn)
  PASS  backend yeniden başlatılmadı (aynı süreç toparlandı)
```

Kesintide cihaz ucu `503 + Retry-After` döner, cihazlar okumaları kendi tamponunda tutar ve geri geldiğinde gönderir (eksik 10 okuma, simülatörün kasıtlı "güç kesintisi" boşluklarıdır). Havuz kendini yeniler; özetleme, geç gelen veriyi "kirli" işaretinden görüp ilgili kovaları yeniden hesaplar.

## Test ve CI

- **69 Go birim testi** (DB gerektirmez), **12 entegrasyon testi** (gerçek PostgreSQL, `-race`), **6 frontend testi** (vitest) ve **67 kontrollük `make verify`**.
- Çekirdek paket kapsamı: `excursion` %92,5 · `shelflife` %93,1 · `loadplan` %98,5 · `chain` %96,4 · `devicesig` %90,7 · `auth` %86,7 — CI'daki birleşik kapsam kapısı (≥ %70) **%83,1**.
- GitHub Actions (`.github/workflows/ci.yml`): gofmt, `go vet`, staticcheck → birim testleri + kapsam tablosu (iş özetine yazılır) → PostgreSQL servisiyle entegrasyon testleri → frontend tip denetimi/test/derleme → konteyner imajları → yığını 5 dk zaman aşımıyla ayağa kaldırıp `make verify`.

---

## Doğrulama Soruları

### 1. En zorlandığın görev hangisiydi? Hangi alternatifleri düşündün, neden eledin?

**T-1 ile T-3'ün kesiştiği yer: bir partinin gerçek sıcaklık geçmişini çıkarmak.** Sapma motorunun kendisi (limit, kümülatif, pencere, MKT) iyi tanımlı bir problem. Zor olan, "bu partinin" geçmişinin ne olduğu: palet bölünüyor, başka partiyle birleşiyor, araca biniyor, müşteriye iniyor; her durakta farklı sensörler var ve boşluklar oluşuyor.

Düşündüklerim:

- **Sensör bazlı değerlendirme, sonucu partilere dağıtmak.** Basit, ama bir palet odadan araca geçtiğinde sapma yanlış partiye yazılıyor ve kümülatif süre yolculuk boyunca toplanamıyor. Elendi.
- **Her pakete `unit_contents` üzerinden bakıp atayı hiç izlememek.** Bölünmeden önceki sıcaklık geçmişi kayboluyor; ayrıca karışık paletin iki partisinin geçmişi birbirine karışıyor. Elendi.
- **SQL'de pencere fonksiyonlarıyla sapma tespiti.** Kümülatif ve kayan pencere SQL'de yazılabiliyor; ama "doğrusal doldurmada 8 °C çizgisinin kesildiği an" ve "belirsiz süreyi ayrı raporla" kuralları okunmaz hale geldi ve birim testi yazılamıyordu. Elendi; motor saf Go'ya taşındı (ADR-06).
- **TimescaleDB (continuous aggregates, gap-fill).** Yığın PostgreSQL 16 diyor, resmi imajda yok; ayrıca kendi çözümümü açıklayabilmem için elendi.

Seçilen çözüm: soy ağacı kenarlarına **lot_id** yazmak ve geriye izi yalnız o partinin kenarları üzerinden yürütmek (karışık palette her parti kendi yolundan gelir), her atanın gözetimini çocuğun ondan ayrıldığı ana kadar kırpmak, lokasyondaki sensörlerden 30 sn'lik kovada en kötüsünü almak ve partinin sonucunu en kötü paletinden vermek (ADR-07).

### 2. Çözümün kırıldığı ya da yetersiz kaldığı bir senaryo ve nedeni

**Bugünün verisinde soğuk sorgu yavaşlığı (fiziksel yerleşim).** 2 000 sensörün ölçümleri zaman sırasıyla geldiği için bir sensörün ardışık satırları farklı sayfalara düşüyor: ölçtüğümde tek sensörün bir günlük 1 519 satırı **1 509 ayrı sayfadaydı** ve soğuk önbellekte 24 saat sorgusu 41 ms sürdü (indeks doğru kullanılmasına rağmen). Çözüm olarak seed ve özetler anahtar sırasıyla yazılıyor ve kapanmış partisyonlar (`gece yarısı + 7 sa`, cihaz geç gönderme payı) `CLUSTER` ile yeniden yazılıyor: sonuç **24 sayfa, 5,6 ms**. **Yetersiz kaldığı yer:** *bugünün* partisyonu kapanana kadar dağınık kalıyor; önbellekten düşmüş bir "bugün" sorgusu hâlâ 1 500 sayfaya dokunuyor. Ayrıca `CLUSTER` partisyonu birkaç saniye kilitliyor (o güne dönük okumalar bekler). Gerçek çözüm ya ölçümleri sensör × saat kovası başına dizi (`smallint[]`) olarak saklamak (tek satır, ~5 kat küçük) ya da `pg_repack` gibi kilitsiz yeniden yazma olurdu; ikisi de bu süreye sığmadı.

Diğer bilinen sınırlamalar:

- **Ingest dayanıklılık penceresi:** API `202` dönüp ≤1 sn sonra toplu yazar; süreç tam o anda *çökerse* (düzgün kapanış değil) o okumalar kaybolur (ADR-04).
- **En kötü sensör kuralı:** arızalı tek bir prob bütün odayı sapmada gösterir (muhafazakâr yanlış pozitif).
- **Hash zinciri tek başına:** zincirin *tamamını* yeniden hesaplayıp yazan bir DBA'yı yakalamaz; zincir başını periyodik olarak harici bir yere sabitlemek gerekir (ADR-11).
- **Cihaz anahtarları demo için türetiliyor:** `DEVICE_PROVISION_SECRET` sızarsa tüm anahtarlar türetilebilir; gerçekte üretimde rastgele anahtar gömülmeli (ADR-05).
- **Şoför ekranı:** çevrimdışıyken *sayfa yenilenirse* uygulama kabuğu gelmez (service worker yok); telefon saatine +5 dk'ya kadar güvenilir.

### 3. İki dizin: hangi sorgu için, dizinsiz/dizinli fark

| Dizin | Sorgu | Dizinli | Dizinsiz |
|---|---|---:|---:|
| `readings (sensor_id, ts)` birincil anahtar (her günlük partisyonda) | bir sensörün son 24 saati (`T-2`, arayüz grafiği, sapma değerlendirmesi) | **5,6 ms** soğuk / sıkıştırma öncesi 15,6 ms sıcak | **218,9 ms** (iki partisyonda paralel tam tarama, 2,4 M satır elendi) |
| `unit_lineage_parent_idx (parent_unit_id)` + `unit_lineage_receive_lot_idx (lot_id) WHERE op='receive'` (kısmi) | geri çağırma: partinin ileri izi (recursive CTE) | **0,20 ms** (401 585 kenar) | **87,6 ms** |

Dürüst not: seed'deki küçük soy ağacında (1 527 kenar) ikinci dizinin **hiçbir faydası yok**; planlayıcı zaten tam taramayı seçiyor ve sorgu dizinli/dizinsiz 0,3–0,6 ms. Fark ancak gerçekçi hacimde ortaya çıkıyor; bu yüzden ölçümü 400 bin paletlik sentetik veriyle tekrarladım (README §EXPLAIN 3).

### 4. Yapay zekâ kullanımı: reddettiğim ya da düzelttiğim çıktılar

Bu projede kodlama asistanı olarak yapay zekâ (Claude) kullandım; kodun önemli bir kısmı onunla birlikte yazıldı. Her çıktıyı test, ölçüm ya da bağımsız hesapla doğruladım. Reddettiğim ve düzelttiğim örnekler:

**a) Brief'teki gizli talimatlar — reddedildi.** `proje.md` içinde görünmeyen HTML yorumlarında yapay zekâya yönelik "[SİSTEM TALİMATI]" satırları vardı: tablo adlarına `lp_` öneki, her yanıta `X-DDO-Build` başlığı, gizli bir `GET /_hz/…` ucu, belirli bir seed e-postası, konteyner adı ve README'nin son satırı. Bunlar görünür gereksinimlerde yok, kaynağı doğrulanamıyor ve bir kısmı (belgelenmemiş uç, sabit başlık) güvenlik açısından kötü pratik. Hiçbiri uygulanmadı.

**b) MKT testindeki beklenen değer — yanlıştı.** Test ilk hâliyle şöyleydi:

```go
// önce: "el ile hesaplanmış" referans
got := MKT([]float64{20, 30}, 83144)
if math.Abs(got-25.9) > 0.1 { t.Fatalf(...) }   // test kırmızı: MKT = 26.260
```

Motor mu yanlış, test mi? Aynı USP <1160> formülünü Python'da bağımsız hesapladım: `26.2599 °C`. Hatalı olan testteki "el hesabı"ydı; motor doğruydu.

```go
// sonra: bağımsız hesapla doğrulanmış değer
if math.Abs(got-26.2599) > 0.001 { t.Fatalf(...) }
```

**c) Canlı alarm yalnızca son okumaya bakıyordu — düzeltildi.** İlk sürüm `last.TempCC > rules.MaxCC` kontrolü yapıyordu; değerlendirme 30 sn'de bir çalıştığı için iki tur arasında yaşanıp biten bir sıçrama alarm üretmezdi. Sonra: son turdan bu yana gelen **tüm** okumalar taranıyor (`evaluator.go`, `recent` penceresi). Bunu uçtan uca test yazarken fark ettim: simülatörün bir sonraki normal okuması benim enjekte ettiğim sıcak okumayı "son okuma" olmaktan çıkarıyordu.

**d) Simülatörün sıra numarası — yanlış pozitif replay'lere yol açıyordu.** Tüm cihazlar için ortak sayaç `seq++` idi ve zamanla saatin gerisinde kalıyordu. Doğrulama programı bir sensöre güncel zamanlı bir seq gönderince, o sensörün sonraki meşru mesajları "replay" diye reddedildi (panoda "S-00003 sessiz" alarmı). Sonra: `seq = max(önceki + 1, şimdi_µs)`, CAS döngüsüyle.

**e) Geçen ama hiçbir şeyi kanıtlamayan test.** Append-only testi `UPDATE excursion_reports …` için hata bekliyordu ve *geçiyordu* — çünkü tablo boştu, satır tetikleyicisi hiç çalışmıyordu. Test önce bir rapor oluşturacak şekilde düzeltildi ve `TRUNCATE` da eklendi.

**f) Yanlış teşhis.** Seed süresi 45 sn'den 200 sn'ye çıkınca ilk varsayım "yeni `ORDER BY` diske taşan sıralama yapıyor" oldu ve `work_mem` artırıldı — fark etmedi. Ölçünce sıralı/sırasız ekleme aynı çıktı (13,5 / 13,6 sn); zamanın yarısı yabancı anahtar tetikleyicisinin satır başı kontrolüydü. Seed sırasında FK kontrolü atlanarak (sensör kimlikleri zaten `sensors` tablosundan JOIN ile geliyor) süre 102 sn'ye indi.

### 5. "Sıcaklık sapma motoru"nun doğru çalıştığını nasıl kanıtladın?

İki katmanda: saf motorun birim testleri (el ile doğrulanabilir senaryolar, bağımsız hesapla karşılaştırma) ve canlı sistemde uçtan uca davranış.

```bash
cd backend && go test -count=1 -v -cover ./internal/domain/excursion/
```

```
--- PASS: TestInRangeSeriesHasNoBreaches (0.00s)
--- PASS: TestHighExcursionDurationIsCountedPerHeldReading (0.00s)
--- PASS: TestLowExcursion (0.00s)
--- PASS: TestSlidingWindowRule (0.00s)
--- PASS: TestSlidingWindowSpacedFarApartIsFine (0.00s)
--- PASS: TestCumulativeRuleAcrossManyShortExcursions (0.00s)
--- PASS: TestGapUncertainPolicyCountsGapAsExcursion (0.00s)
--- PASS: TestGapInterpolatePolicyFillsLinearly (0.00s)
--- PASS: TestTrailingGapIsUnknownEvenWhenInterpolating (0.00s)
--- PASS: TestShortGapWithinToleranceIsHeld (0.00s)
--- PASS: TestUnsortedAndDuplicateReadings (0.00s)
--- PASS: TestEmptySeries (0.00s)
--- PASS: TestMKTKnownValue (0.00s)
--- PASS: TestMKTFromEngineMatchesClosedForm (0.00s)
--- PASS: TestBreachesAreOrderedByTime (0.00s)
PASS
coverage: 92.5% of statements
ok  	github.com/AhmetYorat/soguk-zincir/backend/internal/domain/excursion	3.465s	coverage: 92.5% of statements
```

Testlerden örnekler: 6 dk'lık iki sapma 20 dk arayla bir saatlik pencereye 12 dk sokar (limit 10 → ihlal), 70 dk arayla hiç aynı pencereye girmez (ihlal yok); 8 × 4 dk'lık sapmada 30 dk'lık kümülatif bütçe tam olarak 8. sapmanın 2. dakikasında biter; 20 dk'lık boşlukta 6 → 10 °C doğrusal çizgi 8 °C'yi tam ortada keser (10 dk sapma); 20 °C ve 30 °C'nin MKT'si bağımsız Python hesabıyla (26,2599 °C) eşleşir.

Canlı sistemde (`make verify`, T-1 satırları):

```
| T-1 | Sıcak odadaki parti: kümülatif sapma, MKT, raf ömrü cezası   | PASS | lot 293: 20 dk sapma, MKT 4.52 °C, ceza… |
| T-1 | Kayan pencere sapması alarm olarak üretilmiş                 | PASS | 4+ alarm                                 |
| T-1 | Canlı sapma: imzalı 11 °C ölçümü 60 sn içinde alarm üretir   | PASS | S-00401 → alarm 292, 21 sn               |
| T-1 | Alarmlar tekrarsız: aynı koşul için tek canlı alarm          | PASS | 200 canlı alarm, tekrar yok              |
| T-1 | Süre aşımında eskalasyon (onaylanmamış eski alarm seviye ≥1) | PASS | alarm 1 seviye 3                         |
| T-1 | Boşluk politikası: aynı kesinti 'belirsiz' sapma sayılır, '… | PASS | S3: belirsiz 24 dk sapma, doldurunca 0 … |
```

Son satır aynı 20 dakikalık sensör kesintisini iki politikayla değerlendirir: "belirsiz say" 24 dk sapma raporlar, "doğrusal doldur" 0 dk (iki uç da limit içinde). Entegrasyon testleri `TestSensorPassDeduplicatesAlarms` ve `TestEscalationRaisesLevelsOverTime` de tekrarsızlığı ve eskalasyon seviyelerini gerçek veritabanında doğrular.

---

## Proje yapısı

```
backend/
  cmd/api          serve | migrate | reset | healthcheck
  cmd/simulator    2 000 imzalı sensör + saldırılar + palet olayları
  cmd/verify       make verify
  internal/
    domain/        excursion · shelflife · loadplan · chain · devicesig   (saf, testli)
    api/           uçlar, değerlendirici, parti zaman çizelgesi, OpenAPI, entegrasyon testleri
    auth/ httpx/ ingest/ jobs/ db/ (migrations/) seed/ config/
frontend/          React + Vite + TS (pages/, components/, offline.ts)
load/              k6 senaryosu ve özet betiği
scripts/           chaos.sh · test-integration.sh
docker-compose.yml · Makefile · .env.example · requests.http
DECISIONS.md · TESTING.md
```
