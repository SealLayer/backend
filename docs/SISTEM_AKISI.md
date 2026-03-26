# SealLayer Backend – Sistem Akışı (Detaylı)

Bu doküman, `backend/` servisinin **uçtan uca nasıl çalıştığını**, RAM’de hangi verilerin tutulduğunu, güvenlik modelini ve imzalama akışını açıklar.

## 1) Bileşenler ve sorumluluklar

- **HTTP API (Gin)**: `internal/httpserver/server.go`
  - `POST /v1/seal`: İstemciden `content_hash` alır, kuyruğa iş ekler, sonuç hazırsa receipt döner; değilse `202` ile `batch_id` döner.
  - `GET /v1/status/:batch_id`: RAM’de tutulan batch durumunu döner.
  - `GET /healthz`: liveness.
- **Queue (RAM içi kanal)**: `internal/queue/queue.go`
  - `Queue.ch`: buffered `chan *SealJob` (kapasite: `QUEUE_CAPACITY`).
- **BatchStore (RAM içi durum)**: `internal/queue/queue.go`
  - `map[batch_id]BatchState`: batch’in `queued/batching/pushed/failed` durumunu tutar.
- **Worker (batching + ledger yazımı)**: `internal/worker/worker.go`
  - Her `BATCH_INTERVAL` tick’inde kuyruğu boşaltır (`Drain`) ve aynı `batch_id`’ye ait işleri tek batch olarak işler.
- **Ledger formatı (append-only JSONL)**: `internal/ledger/ledger.go`
  - Ledger dosyası satır satır JSON (`.jsonl`). Her satır bir `Row`.
- **GitHub/Git işlemleri**: `internal/gitops/signed_push.go`
  - Geçici dizine clone → dosyayı yaz → commit → push.
- **GPG işlemleri**: `internal/crypto/gpg.go`
  - Startup’ta private key import (`gpg --import`).
  - Batch root için detached signature üretir (`gpg --detach-sign --armor`) ve bunu base64 olarak döner.

## 2) HTTP akışı (POST /v1/seal)

### 2.1 İstek doğrulama

`POST /v1/seal` gövdesi:

```json
{ "content_hash": "<64 karakter küçük harf hex sha256>" }
```

Sunucu:
- JSON parse edemezse `400` → `error="invalid json"`.
- `content_hash` 64 değilse `400`.
- Hex dışında karakter varsa `400`.

### 2.2 batch_id üretimi (enqueue anında)

Sunucu her istek için:
- `now := time.Now().UTC()`
- `batchID := BatchIDForTime(now, BATCH_INTERVAL)`

> Önemli: `batch_id` **enqueue anında sabitlenir**. Worker tick’inde yeniden hesaplanmaz; böylece status anahtarları “drift” yapmaz.

### 2.3 RAM içi durum kaydı ve kuyruğa ekleme

Sunucu RAM’de:
- `BatchStore.Put({BatchID, Status: queued, UpdatedAt})`
- `SealJob{BatchID, ContentHash, RemoteIP, EnqueuedAt, ResultCh}` oluşturur ve `Queue.TryEnqueue(job)` ile kuyruğa koyar.

Kuyruk doluysa:
- `503` → `error="queue full"`

### 2.4 “inline bekleme” (200 vs 202)

Sunucu, `REQUEST_PENDING_MAX` kadar worker sonucunu bekler:
- Sonuç zamanında gelirse **200** ve `receipt` döner.
- Zaman aşımı olursa **202** döner:
  - `batch_id`
  - `status_url=/v1/status/<batch_id>`
  - `error="processing"` (bilgilendirme amaçlı)

## 3) Worker akışı (batching → ledger → imza → push)

Worker her `BATCH_INTERVAL`’de bir çalışır:

1. **Kuyruktan toplu çekme**
   - `jobs := q.Drain(QUEUE_CAPACITY)`
2. **Aynı batch_id’ye göre gruplama**
   - Bir tick’te birden fazla batch grubu olabilir.
3. **BatchState’i “batching” yapma**
   - `BatchStore.Put({Status: batching})`
4. **Ledger hedef dosyasını seçme**
   - Path: `ledger/YYYY/MM/DD.jsonl` (UTC’ye göre)
5. **GitHub’dan mevcut ledger içeriğini çekme**
   - `gh.GetFile(owner, repo, path, branch)` ile içerik alınır.
6. **Hash zinciri üretimi (satırların hazırlanması)**
   - `prevHash` çözümü şu sırayla yapılır:
     1) Bugünkü `ledger/YYYY/MM/DD.jsonl` içindeki son `final_hash`,
     2) Yoksa geçmiş gün dosyaları geriye taranarak bulunan en yakın son `final_hash`,
     3) Hiç yoksa (ilk kurulum) deterministic genesis anchor.
   - Her iş için bir `Row` üretilir:
     - `content_hash`: istemcinin verdiği hash
     - `prev_hash`: bir önceki final hash
     - `ts`: tek bir `ts_utc` (Unix epoch) batch için ortak
     - `final_hash`: `FinalHash(content_hash, prev_hash, ts)`
   - `prev_hash` her satırda bir önceki satırın `final_hash`’ine güncellenir (zincir).
7. **Ledger’a append**
   - `AppendRows(lastContent, rows)` ile JSONL güncellenir.
8. **Batch root hesabı**
   - Her satırın `final_hash` değerlerinden `BatchRoot(finals)` üretilir.
9. **Detached signature (GPG)**
   - `gpg --detach-sign --armor` ile **ASCII-armored** imza üretilir.
   - Bu armored imza **base64**’e çevrilip receipt’e konur (`sig`).
10. **GitHub’a yazma (clone → commit → push)**
    - `SignedCloneCommitPush(...)` çağrılır:
      - Temp klasör oluşturur (`os.MkdirTemp`) ve buraya repo clone eder.
      - Ledger dosyasını yazar.
      - Commit mesajı: `seal batch <batch_id>`.
      - `GIT_SIGN_COMMITS=true` ise commit’i ayrıca GPG ile imzalar.
      - Push yapar ve `commit_sha` döndürür.
11. **BatchState’i “pushed” yapma ve iş sonuçlarını dağıtma**
    - `BatchStore.Put({Status: pushed, LedgerPath, CommitSHA})`
    - Her job için `ResultCh` üzerinden `SealJobResult` yollanır (receipt dahil).

Hata olursa:
- `BatchStore.Put({Status: failed, Error: err.Error()})`
- Her job’a `Err` gönderilir.

## 4) RAM’de hangi veriler tutuluyor?

### 4.1 Kuyruk (Queue)

RAM’de buffered kanal:
- `Queue.ch chan *SealJob`
- İçerik: `BatchID`, `ContentHash`, `RemoteIP`, `EnqueuedAt`, `ResultCh`.

**Kapanma / restart durumunda** kanal boşalır; bekleyen işler kaybolur.

### 4.2 BatchStore (durum cache’i)

RAM’de map:
- `map[batch_id]BatchState`
- Alanlar: `status`, `ledger_path`, `commit_sha`, `error`, `updated_at`.

**Persist edilmez**. Servis restart olursa:
- `GET /v1/status/:batch_id` muhtemelen `404 not_found` döner (çünkü RAM state yok).

### 4.3 İstek başına kısa ömürlü veriler

- `ResultCh`: `POST /v1/seal` çağrısı için oluşturulan tek-use kanal (buffer=1).
- Worker içinde:
  - GitHub’dan okunan `lastContent` (string)
  - Üretilen `rows`, `finals`, `batch_root`
  - Temp clone dizini yolu

### 4.4 Diskte tutulanlar (konteyner içinde)

Bu proje “DB yok” yaklaşımıyla gitse de iki disk izine dikkat:
- **GNUPGHOME** (örn. `/app/.gnupg`): startup’ta import edilen GPG keyring burada oluşur.
- **Git clone temp dir**: `SignedCloneCommitPush` her batch’te geçici bir dizin oluşturur; işlem bitince silinir (`defer os.RemoveAll`).

> Bu dizinler genelde container filesystem’inde kalır; host/volume yapılandırmasına göre kalıcılık değişebilir.

## 5) Güvenlik modeli (mevcut durum)

### 5.1 Ağ / erişim

- API uçları şu an **kimlik doğrulama olmadan** çalışır (API key, JWT vb. yok).
- Pratikte üretimde korunması gerekenler:
  - Cloudflare / WAF kuralları
  - Rate limiting (kodda config alanı var; runtime’a tam bağlı değil)
  - IP allowlist (sadece iç sistemler çağıracaksa)

### 5.2 Sırlar (secrets)

Servisin kritik secret’ları environment üzerinden gelir:
- `GITHUB_TOKEN` (repo clone/push)
- `GPG_PRIVATE_KEY_*` (private key material)
- `GPG_PASSPHRASE`

Öneri:
- Multiline secret’lar PaaS’ta bozulabileceği için `GPG_PRIVATE_KEY_ARMORED_B64` veya secret file daha güvenilir.
- Bu secret’ların **loglara** düşmemesi gerekir (proje loglarda anahtar içeriği yazmaz).

### 5.3 CORS

`CORS_ALLOWED_ORIGINS` boşsa CORS kapalıdır (tarayıcıdan cross-origin `fetch` engellenir).

- **Server-to-server** istemciler (curl, n8n, backend servisleri, mobil uygulama) CORS’a tabi değildir.
- Tarayıcı tabanlı bir frontend ayrı origin’de olacaksa CORS gerekir.

### 5.4 Cloudflare “Under Attack Mode”

Bu mod JS challenge uygular:
- Tarayıcı açabilir,
- `curl`, `n8n`, Python gibi otomasyonlar genelde **403/forbidden** alır.

API istemcileri kullanılacaksa Under Attack Mode kapalı tutulmalı veya API path’i için istisna kuralı yazılmalıdır.

## 6) İmzalama nasıl yapılıyor?

Bu sistemde **iki ayrı imzalama** kavramı var:

### 6.1 Receipt için detached signature (asıl “kanıt”)

Worker her batch için:
1. Satırların `final_hash`’lerinden `batch_root` üretir.
2. `gpg --detach-sign --armor` ile `batch_root` üzerinde detached signature üretir.
3. Bu armored signature’ı base64 olarak receipt’e koyar:
   - `signature.alg = "openpgp-detached"`
   - `signature.sig = <base64(armored)>`

Bu imza:
- Ledger’a yazılan hash zinciriyle ilişkilidir.
- İstemci tarafında public key fingerprint ile doğrulanabilir.

### 6.2 Git commit imzası (opsiyonel)

`GIT_SIGN_COMMITS=true` ise:
- Git commit’i `-S<keyid>` ile ayrıca imzalanır.
- Bu, GitHub tarafında “verified commit” gibi özellikler için faydalıdır.

Not:
- Commit imzası devre dışı bırakılsa bile (**`GIT_SIGN_COMMITS=false`**), receipt için detached signature yine üretilir.

## 7) Uçtan uca örnek (istek → sonuç)

1. İstemci `content_hash` hesaplar.
2. `POST /v1/seal`:
   - hızlıysa `200` + `receipt`
   - yavaşsa `202` + `batch_id`
3. `GET /v1/status/<batch_id>` ile `pushed` olana kadar takip edilir.
4. `pushed` olduğunda:
   - `ledger_path` ve `commit_sha` verilir.
   - Receipt içindeki detached signature ve public key fingerprint ile doğrulama yapılabilir.

## 8) Bilinen sınırlamalar

- **RAM state kaybı**: restart sonrası `batch_id` durumları kaybolur.
- **DB yok**: “status” uzun süreli takip için ledger/GitHub’dan türetilecek ayrı bir indeksleme ihtiyacı doğabilir.
- **Auth yok**: Üretimde abuse riski var; WAF/rate-limit önerilir.

