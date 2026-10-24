# Mimari karar kayıtları (ADR)

Her kayıt: **bağlam → karar → sonuç / bedeli**. Kodda ilgili yer belirtilmiştir.

---

## ADR-01 — ORM yok: `pgx` + elle yazılmış SQL, `chi` yönlendirici

**Bağlam.** Projenin değerli kısmı veritabanında: recursive CTE, partisyonlar, `FOR UPDATE`, koşullu `UPDATE`, `ON CONFLICT … WHERE`, `COPY`, kısmi indeksler. ORM'ler bunların çoğunu ya saklar ya da hiç desteklemez.

**Karar.** `pgx/v5` havuzu ile doğrudan SQL; sonuçlar `pgx.RowToStructByPos` ile struct'a. HTTP için `chi` (standart `net/http` uyumlu, rota deseni metriklerde etiket olarak kullanılıyor). Migrasyon: `goose`, SQL dosyaları binary'ye gömülü (`internal/db/migrations`).

**Sonuç.** Her sorgu okunabilir ve `EXPLAIN` ile birebir ölçülebilir. Bedeli: derleme zamanı tip denetimi yok (sqlc düşünüldü; 5 günlük süre ve dinamik filtreli listeler nedeniyle elendi). Bu açık, entegrasyon testleri ve `make verify` ile kapatılıyor.

---

## ADR-02 — Ölçüler tamsayı: santi-derece, binde nem, kuruş

**Bağlam.** "Para `float` olamaz" kuralı var. Sıcaklıkta da `float` karşılaştırması (`8.0 > 8.0000001`) limit kararlarını belirsizleştirir.

**Karar.** Sıcaklık `smallint` santi-derece (`temp_cc`, 4,25 °C = 425), nem `smallint` binde (`humidity_pm`), fiyat `bigint` kuruş. Hesaplama (MKT, Arrhenius) Go'da `float64` ile yapılır, saklanan değerler tamsayıdır.

**Sonuç.** Ham okuma satırı 16 bayt veri (`int4 + timestamptz + 2×int2`); günlük 5,76 M satırda bu fark büyüktür (bkz. README kapasite). Limit karşılaştırmaları tam.

---

## ADR-03 — Zaman serisi: yerel partisyonlama + aşamalı özetleme (TimescaleDB değil)

**Bağlam.** 2 000 sensör × 30 sn = 5,76 M satır/gün. Son 24 saat sorgusu p95 < 100 ms olmalı. Yığın "PostgreSQL 16" diyor; TimescaleDB resmi imajda yok ve değerlendirmede "neyi kendin yaptın" sorusunu bulanıklaştırır.

**Karar.**
- `readings` günlük `RANGE` partisyonlu, birincil anahtar `(sensor_id, ts)`; sorgu her zaman partisyon budaması + indeks taraması yapar.
- `readings_1m` (günlük), `readings_15m` ve `readings_1h` (aylık) partisyonlu özet tabloları. Her seviye **bir öncekinden** üretilir (1m → 15m → 1h) ve ortalama yerine `sum + count` tutar, böylece üst seviye alt seviyeden *tam* hesaplanır (entegrasyon testi `TestRollupLevelsAreConsistent` dört seviyenin toplam sayısının eşit olduğunu doğrular).
- Özetleme idempotent `UPSERT`'tür; her tur son kovayı yeniden hesaplar, geç gelen veri (cihaz tamponu) için ingest katmanı "kirli" en eski zamanı bildirir (`Pipeline.TakeDirty`).
- Saklama `DROP TABLE partition` ile (milyonlarca satırı `DELETE` etmek yerine O(1)): ham 7 gün, 1m 30 gün, 15m 180 gün, 1h 2 yıl.
- Arayüz/API aralığa göre çözünürlük seçer: ≤3 sa ham, ≤48 sa 1m, ≤30 gün 15m, üstü 1h.

**Sonuç.** Ölçülen son 24 saat p95: 1m çözünürlükte ~12 ms, ham 2 880 satırda ~11 ms (`make verify`), k6 altında 7,1 ms. Bedeli: partisyon bakım işi bizim sorumluluğumuzda (`db.MaintainPartitions`, saatlik; iki gün ilerisi hazır tutulur). Bilinen sınırlama için README §Doğrulama Soruları 2'ye bakınız (sayfa dağınıklığı).

---

## ADR-04 — Ölçüm alımı: bellek tamponu + `COPY` + `ON CONFLICT DO NOTHING`

**Bağlam.** Her cihaz mesajını kendi `INSERT`/commit'iyle yazmak saniyede ~67 commit demek; yük testinde 135 mesaj/sn denendi. Cihazlar kesinti sonrası aynı ölçümü tekrar gönderebilir.

**Karar.** İmza ve sıra doğrulandıktan sonra okumalar sınırlı bir bellek tamponuna alınır (500 000 satır), saniyede bir `COPY` ile geçici tabloya, oradan `INSERT … ON CONFLICT DO NOTHING` ile `readings`'e yazılır. Tampon doluysa `503 + Retry-After`; veritabanı kapalıyken cihaz ucu `503` döner ve cihaz kendi tamponunu korur.

**Sonuç.** k6: ingest p95 2 ms, hata %0. Kaos testi: 60 sn veritabanı kesintisinde ölçülen 4 000 okumanın 3 990'ı (geri kalanı simülatörün kasıtlı güç kesintisi) sonradan ulaştı. **Bedeli (bilinçli):** API süreci `202` döndükten sonra ama flush'tan önce (≤1 sn) çökerse o okumalar kaybolur; düzgün kapanışta (`SIGTERM`) tampon boşaltılır. Daha sıkı garanti için `202`'yi flush sonrasına almak veya bir kuyruk (NATS/Kafka) eklemek gerekir.

---

## ADR-05 — Cihaz kimliği: cihaz başına HMAC anahtarı, monoton sıra numarası, saat penceresi

**Bağlam.** Sensörler kullanıcı değildir; JWT anlamsız. Ölçüm sahteciliği ve tekrar (replay) saldırısı engellenmeli.

**Karar.**
- İmza: `HMAC-SHA256(cihaz_anahtarı, "v1\n" + id + "\n" + seq + "\n" + ts + "\n" + hex(sha256(gövde)))`. Gövdenin tamamı ve sıra numarası imzaya dahildir, yani sıra numarası değiştirilip tekrar denenemez.
- Tekrar engeli: `UPDATE sensors SET last_seq = $seq WHERE id = $id AND last_seq < $seq` — **atomik koşullu güncelleme**; aynı mesajın iki kopyası yarışsa bile yalnızca biri geçer (`TestReplayRaceSameSequence`: 6 paralel kopyadan 1×202, 5×409). Ayrıca zaman damgası ±2 dk.
- Anahtarlar veritabanında AES-256-GCM ile şifreli, cihaz kimliği AEAD ek verisi (bir satırın şifreli anahtarı başka satıra taşınamaz).
- Demo için anahtarlar `HMAC(DEVICE_PROVISION_SECRET, cihaz_kodu)` ile türetilir; seed ve simülatör anahtar alışverişi yapmadan anlaşır.

**Sonuç.** İmzasız 401, yanlış anahtar 401, değiştirilmiş gövde 401, tekrar 409, eski zaman damgası 401 — hepsi `make verify` T-5'te. **Bedeli:** türetme yöntemi demo kolaylığıdır; `DEVICE_PROVISION_SECRET` sızarsa tüm anahtarlar türetilebilir. Gerçek kurulumda anahtar üretimde rastgele üretilip cihaza gömülmeli.

---

## ADR-06 — Sapma motoru saf bir Go paketi; zaman "segment"lerle modellenir

**Bağlam.** Ürün limiti, kümülatif süre, kayan pencere, boşluk politikası ve MKT birbirine bağlı. SQL pencere fonksiyonlarıyla yazmak denendi: doğrusal doldurmada limit kesişim noktasını bulmak ve "belirsiz" süreyi ayrı raporlamak SQL'de okunaksızlaştı, test edilemiyordu.

**Karar.** `internal/domain/excursion` veritabanından bağımsızdır. Her okuma bir sonrakine kadar "tutulur" (sample-and-hold); aradaki fark `max_gap_s`'den büyükse **boşluk**tur:
- `uncertain`: boşluk sapma sayılır (muhafazakâr), `uncertain_s` ayrıca raporlanır, MKT'ye girmez.
- `interpolate`: iki uç arasında doğrusal doldurma; limit çizgisinin kesildiği nokta analitik bulunur, yani dışarıda geçen süre tam hesaplanır.
- Kayan pencere: en kötü pencere bir sapma aralığının başında başlar ya da sonunda biter; yalnızca bu 2n aday prefix-sum + ikili arama ile denenir (O(n log n)).
- Alarm zamanı: kuralın *ilk aşıldığı an* (ör. kümülatif bütçenin bittiği saniye).

**Sonuç.** 15 birim testi, %92,5 kapsam; testler el hesabıyla doğrulanabilir senaryolar (ör. "20 dk'lık boşlukta 6→10 °C doğrusal çizgi 8 °C'yi tam ortada keser, 10 dk sapma"). Parti değerlendirmesi palet soy ağacı üzerinden yapılır (ADR-07).

---

## ADR-07 — Bir partinin sıcaklık geçmişi = soy ağacında *o partinin* yolu

**Bağlam.** Palet bölünür, birleşir, araca yüklenir. "Bu ürün hangi sıcaklıkta yolculuk etti?" sorusu partinin geçtiği her lokasyonun o zaman aralığındaki sensörlerini gerektirir. Karışık bir paletin birden çok ebeveyni vardır, ama her parti oraya tek bir yoldan gelmiştir.

**Karar.**
- `unit_lineage` (ebeveyn → çocuk, işlem, **lot_id**, adet, zaman) ve `custody_events` (palet, lokasyon, başlangıç, bitiş; palet başına tek açık aralık kısmi UNIQUE indeksle).
- Geriye iz: recursive CTE yalnızca ilgili partinin kenarlarını izler; her atanın gözetimi, çocuğun ondan ayrıldığı ana kadar kırpılır.
- Okumalar: lokasyondaki tüm sensörlerden 30 sn'lik kovada **en yüksek** değer (en kötü sensör kazanır). Ham saklama süresini aşan aralıklar için 1m özetin maksimumu kullanılır.
- Parti sonucu, partiyi taşıyan paletlerden **en kötüsüdür** (en az kalan raf ömrü).

**Sonuç.** İleri iz (geri çağırma) 401 bin kenarlı sentetik veride 0,2 ms (indekssiz 87,6 ms). **Bedeli:** en kötü sensör kuralı arızalı tek bir probun bütün odayı "sapmada" göstermesine yol açar (yanlış pozitif); soğuk zincirde yanlış negatiften iyidir, ama sensör sağlık raporu bu yüzden var.

---

## ADR-08 — Raf ömrü: üst limite göre Arrhenius, limit altında "kredi yok"

**Bağlam.** "Sapma süresine göre kalan raf ömrü yeniden hesaplanmalı." Q10 kuralı ve Arrhenius düşünüldü; MKT zaten Arrhenius'a (ΔH = 83,144 kJ/mol) dayandığı için tutarlılık adına Arrhenius seçildi.

**Karar.** Nominal raf ömrü üst depolama limitinde geçerli kabul edilir. `T > T_ref` olan her saniye `exp(Ea/R · (1/T_ref − 1/T))` saniye tüketir; altında 1 saniye. Ölçüm olmayan süre nominal hızda yaşlanır; "belirsiz" boşluklar komşu okumaların yükseğiyle hesaplanır.

**Sonuç.** 18 °C'de bir saat ≈ 3,3 saat raf ömrü (Q10 ≈ 3,3, testte doğrulanıyor). Limit altında kalmak raf ömrü kazandırmaz (iyimser hata yapılmaz); donma hasarı ayrı bir kalite kararıdır.

---

## ADR-09 — FEFO + kapasite: iki boyutlu 0/1 sırt çantası, sözlük sıralı amaç

**Bağlam.** Saf FEFO açgözlüdür ve kapasiteyi kötü kullanabilir (60+50+50 adetlik paletler, 100 adetlik talep: FEFO 60 alır, 50 sığmaz → 60; optimum 50+50 = 100).

**Karar.** Önce kalan raf ömrü `yol süresi + güvenlik payı`ndan kısa paletler gerekçesiyle elenir. Sonra (adet, palet yeri) üzerinde DP: değer = `adet·B + (n − FEFO_sırası)`. `B` FEFO bonuslarının toplamından büyük seçildiği için önce yüklenen adet maksimize edilir, eşitlikte erken son kullanma tarihli paletler kazanır. Tek ürün satırında ağırlık = adet × birim ağırlık olduğundan ağırlık kısıtı adet kısıtına dönüşür. Durum sayısı 4 000'i aşarsa adetler yukarı yuvarlanarak ölçeklenir (kapasite asla aşılmaz, sonuç en fazla birkaç birim eksik olabilir).

**Sonuç.** 300 rastgele örnekte kaba kuvvet çözümüyle aynı optimum (`TestMatchesBruteForceOnRandomInstances`). Plan yalnızca öneridir; **yükleme** ayrı, kilitli bir işlemde kapasiteyi, parti durumunu ve raf ömrünü yeniden kontrol eder (istemci planı atlasa bile).

---

## ADR-10 — Eşzamanlılık: satır kilidi + koşullu UPDATE + UNIQUE, kilit sırası

**Bağlam.** Paylaşılan durumlar: palet içeriği (stok), paletin hangi sevkiyatta olduğu, araç kapasitesi, parti durumu, rapor zinciri başı.

**Karar.**
- Stok: `UPDATE unit_contents SET quantity = quantity - $n WHERE … AND quantity > $n` (koşullu; asla negatif olmaz).
- Yükleme: sevkiyat satırı + araç satırı + her palet `FOR UPDATE`; ayrıca `shipment_items.unit_id UNIQUE` son savunma hattı.
- Birleştirme: paletler **id sırasıyla** kilitlenir; çakışan iki birleştirme kilitlenme (deadlock) yapamaz.
- Kalite kararı: parti satırı `FOR UPDATE`, durum makinesi kilit altında okunur.
- Rapor zinciri: tek satırlık `report_chain_head` `FOR UPDATE` → ardışık seq.
- `SERIALIZABLE` + yeniden deneme yardımcı fonksiyonu var (`db.Serializable`), ama bu iş yükünde açık kilitler daha öngörülebilir olduğu için kullanılmadı.

**Sonuç.** Entegrasyon testleri: aynı palet 10 sevkiyata aynı anda → tam 1 başarı; 12 eşzamanlı bölme → tam 6 başarı ve toplam adet korunur; 10 eşzamanlı "kabul" → tam 1.

---

## ADR-11 — Değiştirilemez kayıt: tetikleyici + hash zinciri (ikisi birlikte)

**Bağlam.** Tetikleyici kazara değişikliği engeller ama süper kullanıcı tetikleyiciyi kapatabilir. Hash zinciri değişikliği engellemez ama *görünür* kılar.

**Karar.** `audit_log` ve `excursion_reports` için `BEFORE UPDATE OR DELETE` (satır) ve `BEFORE TRUNCATE` (deyim) tetikleyicileri. Raporlar `hash_n = SHA-256(hash_{n−1} ‖ seq ‖ created_at_µs ‖ kanonik_JSON)`. Kanonik JSON: anahtarlar sıralı, sayılar yazıldığı gibi (`UseNumber`), çünkü `jsonb` anahtar sırasını değiştirir. `GET /excursion-reports/verify` zinciri baştan hesaplar ve zincir başı satırıyla karşılaştırır (sondan silmeyi de yakalar).

**Sonuç.** `TestHashChainDetectsTamperingByDBA`: tetikleyiciyi kapatıp 2. raporu değiştiren "DBA" → doğrulama tam o seq'i işaret eder. **Bedeli:** demo sıfırlama `TRUNCATE` yapamaz, şemayı düşürüp yeniden kurar (`api reset`). Daha güçlü bir sürüm zincir başını periyodik olarak harici bir yere (ör. imzalı e-posta, RFC 3161 zaman damgası) yazmalı; şu an zincirin tamamını yeniden yazan bir DBA'yı tek başına yakalayamaz.

---

## ADR-12 — Idempotency: kullanıcı başına anahtar tablosu, istek özeti, yalnız 2xx saklanır

**Karar.** `Idempotency-Key` ara katmanı: `INSERT … ON CONFLICT DO NOTHING` ile anahtar rezerve edilir. Aynı anahtar + aynı istek (yöntem + yol + gövde SHA-256) → saklanan yanıt aynen (`Idempotent-Replayed: true`); farklı gövde → 422; ilk istek hâlâ çalışıyorsa → 409. Yalnızca 2xx saklanır; hata durumunda rezervasyon silinir ki istemci sebebi düzeltip aynı anahtarla yeniden deneyebilsin. Anahtarlar 24 saat sonra temizlenir. Şoför senkronunda olay başına `client_event_id UNIQUE` aynı işi yapar.

**Sonuç.** 8 paralel aynı anahtarlı istekten tam 1 parti oluşur (`TestIdempotencyKeyUnderConcurrency`).

---

## ADR-13 — Kimlik: kısa ömürlü JWT + dönen, opak refresh token (aile iptali)

**Karar.** Parola argon2id (OWASP temel parametreleri, parametreler hash'in içinde). Access JWT HS256, 15 dk, `alg` sabit (alg=none reddedilir). Refresh token 32 bayt rastgele, veritabanında SHA-256 olarak; her kullanımda döner. Daha önce kullanılmış bir refresh token gelirse çalınmış sayılır ve **tüm aile** iptal edilir. Olmayan kullanıcıya da sahte parola kontrolü yapılır (zamanlama ile hesap tespiti engeli). RBAC: rol → izin tablosu kodda; her rota `require(izin)` ile tanımlanır ve `TestEveryAPIRouteRequiresAuthentication` tüm `/api/v1` rotalarının tokensız 401 döndüğünü `chi.Walk` ile doğrular. Müşteri başka müşterinin kaydına 403 değil 404 alır (varlık sızdırılmaz).

---

## ADR-14 — Arayüz: React + Vite + TypeScript, UI kütüphanesi yok

**Bağlam.** Altı rol, çok sayıda tablo/form, bir grafik, çevrimdışı şoför ekranı. HTMX düşünüldü: sunucu tarafı şablonlar Go'da hızlı olurdu ama çevrimdışı kuyruk ve canlı pano için yine istemci JS gerekecekti.

**Karar.** React 18 + Vite + TS; bağımlılık yalnızca `react-router-dom`. Grafik elle SVG (bant: min–maks, çizgi: ortalama, kesikli: ürün limitleri). Her ekran `AsyncView` ile yükleniyor / boş / hata durumlarını zorunlu ele alır. Şoför ekranı olayları `localStorage` kuyruğuna yazar, `online` olayında ve 20 sn'de bir gönderir; "çevrimdışı modu dene" anahtarı demo içindir. Üretim imajı nginx-unprivileged (uid 101), `/api` aynı origin'den backend'e vekillenir (CORS gerekmez).

Görsel dil: soğuk lacivert yüzeyler, buz mavisi marka rengi ve tek bir imza öğesi — soğuktan sıcağa termal ölçek (üst çubuktaki şerit, canlı panodaki dikey göstergeler, grafikte limit dışı bölgeler). Yazı: IBM Plex Sans (arayüz) + JetBrains Mono (sıcaklık, kod, sayı; hizalı rakamlar). Durum hiçbir yerde yalnızca renkle verilmez (nokta + metin, "limit dışı" etiketi); açık/koyu/sistem tema seçici, iskelet yükleme, `prefers-reduced-motion` desteği, emoji yerine tek bir SVG ikon ailesi.

**Sonuç.** İmaj küçük, derleme ~10 sn. Bedeli: tam bir PWA (service worker, arka plan senkronu) yapılmadı; sayfa çevrimdışıyken *yeniden yüklenirse* uygulama kabuğu gelmez (açık sekmede çalışmaya devam eder).
