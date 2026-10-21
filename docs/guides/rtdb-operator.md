# Serving Realtime Database

[العربية](#تشغيل-قاعدة-البيانات-اللحظية)

Every SDK finds a database by the first label of its host, as Firebase's own
`<ns>.firebaseio.com` does (ADR-018). In production each project's database therefore has a host
of its own, `https://<project_id>-default-rtdb.<rtdb.domain>`, and that host needs a DNS name, a
certificate and a proxy that passes it through unchanged. This guide sets them up.

## 1. `rtdb.domain`

```yaml
public_url: https://aruma.example.com
rtdb:
  enabled: true
  domain: rtdb.example.com      # ARUMA_RTDB_DOMAIN
metrics_listen: 127.0.0.1:9464  # private; the TLS ask below is served here
```

- Config files and the dashboard then carry `databaseURL: https://<project_id>-default-rtdb.rtdb.example.com`.
- A dedicated subdomain (`rtdb.example.com`, four-label hosts) is the suggested form. A domain of its
  own (`example-db.com`) works too, and suits a proxy whose free certificate covers one level only.
- `public_url` and `project_domain` must lie outside `rtdb.domain`: every host under it belongs to
  the database.
- Without `rtdb.domain`, databases are reached as `<public_url>/?ns=<project_id>-default-rtdb`. That
  form breaks the iOS, Admin Go and Admin Python SDKs, so it only suits local development on
  `http://127.0.0.1`; the server warns at start when `public_url` is https and `rtdb.domain` is unset.

## 2. The wildcard DNS record

One record covers every project, present and future:

```
*.rtdb.example.com.  300  IN  A     203.0.113.10
*.rtdb.example.com.  300  IN  AAAA  2001:db8::10
```

The address is the proxy in front of Aruma. Nothing else is needed per project.

## 3. Certificates: Caddy with `on_demand_tls`

A wildcard certificate needs a DNS-01 challenge and an API key at your DNS provider. Caddy can
instead obtain one certificate per database host the first time a client connects, if Aruma vouches
for the name. Aruma answers `GET /rtdb/tls-ask?domain=<name>` on `metrics_listen` only: 200 when the
name is `<ns>.<rtdb.domain>` and `ns` the database of an existing, active project, 404 for anything
else, so no one can make Caddy request certificates for names that serve nothing.

```caddyfile
{
	on_demand_tls {
		ask http://127.0.0.1:9464/rtdb/tls-ask
	}
}

aruma.example.com {
	reverse_proxy 127.0.0.1:8080
}

*.rtdb.example.com {
	tls {
		on_demand
	}
	reverse_proxy 127.0.0.1:8080
}
```

- `ask` must point at the private address. If Caddy runs on another machine or in a container,
  bind `metrics_listen` to an interface only Caddy can reach (a private network), never a public
  one: the answer tells which projects exist.
- The first connection to a new project's host waits a few seconds for its certificate; the SDKs
  wait 30 seconds before they give up and retry.
- Let's Encrypt allows 50 new certificates per registered domain per week. Beyond that rate of new
  projects, or to avoid the first-connection wait, use a wildcard certificate obtained with DNS-01
  (a Caddy build with your DNS provider's module, lego, certbot or acme.sh). Aruma does not care how
  the certificate was issued.
- Behind Cloudflare's proxy the free certificate covers one level below the zone: either leave the
  `*.rtdb` record DNS only, or use a two-label `rtdb.domain` of its own.

## 4. Proxies: keep the host

The database is named by the host the client connected to, and the server tells clients that host
in the handshake, where they reconnect. A proxy must therefore either:

- **keep the `Host` header** as the client sent it (Caddy's `reverse_proxy` does by default;
  nginx needs `proxy_set_header Host $host;`), or
- **send `X-Forwarded-Host`** with the client's host, from an address listed in `trusted_proxies`
  (`ARUMA_TRUSTED_PROXIES`). Aruma believes `X-Forwarded-Host` and `X-Forwarded-For` only from
  those addresses; from anyone else they are ignored.

A proxy that rewrites the host to `127.0.0.1:8080` without either makes every database request fail
with "Specify the database".

## 5. Timeouts: at least 60 seconds idle

Clients hold WebSockets open for hours and long polls for up to 20 seconds per request. The SDKs
send a keep-alive after 45 seconds of silence and Aruma closes a connection after 120 silent
seconds, so every hop between them must allow **at least 60 seconds** of idle time on a connection,
and must pass WebSocket upgrades (`Upgrade` and `Connection` headers).

- **Caddy:** nothing to set; `reverse_proxy` streams WebSockets with no idle limit.
- **nginx:**
  ```nginx
  location / {
      proxy_pass http://127.0.0.1:8080;
      proxy_http_version 1.1;
      proxy_set_header Host $host;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection $connection_upgrade;  # map $http_upgrade $connection_upgrade
      proxy_read_timeout 120s;
      proxy_send_timeout 120s;
  }
  ```
- **Load balancers** (AWS ALB, Google Cloud, HAProxy `timeout tunnel`): raise the idle timeout to
  120 seconds or more.
- Aruma's own 60-second request timeout and 1 MB body cap do not apply to database connections.

## 6. Check it

```sh
curl -s -o /dev/null -w '%{http_code}\n' 'http://127.0.0.1:9464/rtdb/tls-ask?domain=chat-app-default-rtdb.rtdb.example.com'  # 200
curl -s 'https://chat-app-default-rtdb.rtdb.example.com/.settings/rules.json'  # 401: the host reaches the database
```

---

# تشغيل قاعدة البيانات اللحظية

تجد كل مكتبة قاعدة البيانات من أول جزء في اسم المضيف، كما في Firebase نفسه `<ns>.firebaseio.com`
(ADR-018). لذلك يكون لقاعدة كل مشروع في الإنتاج مضيف خاص بها،
`https://<project_id>-default-rtdb.<rtdb.domain>`، يحتاج اسم DNS وشهادة ووكيلًا يمرّر المضيف كما هو.

## 1. الإعداد `rtdb.domain`

```yaml
public_url: https://aruma.example.com
rtdb:
  enabled: true
  domain: rtdb.example.com      # ARUMA_RTDB_DOMAIN
metrics_listen: 127.0.0.1:9464  # عنوان خاص، وعليه نقطة ask أدناه
```

- تحمل ملفات الإعداد واللوحة حينها `databaseURL: https://<project_id>-default-rtdb.rtdb.example.com`.
- المقترح نطاق فرعي مخصص مثل `rtdb.example.com` (مضيف من أربعة أجزاء). ونطاق مستقل مثل `example-db.com`
  يصلح أيضًا، ويناسب وكيلًا تغطي شهادته المجانية مستوى واحدًا.
- يجب أن يكون `public_url` و`project_domain` خارج `rtdb.domain`، لأن كل مضيف تحته لقاعدة البيانات.
- بلا `rtdb.domain` تُخدم القواعد على `<public_url>/?ns=<project_id>-default-rtdb`. هذه الصيغة تنكسر في
  مكتبات iOS وAdmin Go وAdmin Python، فهي للتطوير المحلي على `http://127.0.0.1` فقط، ويحذّر الخادم عند
  الإقلاع إن كان `public_url` على https بلا `rtdb.domain`.

## 2. سجل DNS العام

سجل واحد يغطي كل المشاريع، الحالية والقادمة:

```
*.rtdb.example.com.  300  IN  A     203.0.113.10
*.rtdb.example.com.  300  IN  AAAA  2001:db8::10
```

العنوان هو الوكيل الذي أمام Aruma. ولا يلزم شيء آخر لكل مشروع.

## 3. الشهادات: Caddy مع `on_demand_tls`

شهادة wildcard تحتاج تحقق DNS-01 ومفتاح API عند مزوّد الـ DNS. والبديل أن يطلب Caddy شهادة لكل مضيف
قاعدة عند أول اتصال به، إن أكّد Aruma الاسم. يجيب Aruma على `GET /rtdb/tls-ask?domain=<name>` على
`metrics_listen` وحده: 200 إن كان الاسم `<ns>.<rtdb.domain>` و`ns` قاعدة مشروع موجود ونشط، و404 لأي اسم
آخر، فلا يستطيع أحد أن يدفع Caddy إلى طلب شهادات لأسماء لا تخدم شيئًا.

```caddyfile
{
	on_demand_tls {
		ask http://127.0.0.1:9464/rtdb/tls-ask
	}
}

aruma.example.com {
	reverse_proxy 127.0.0.1:8080
}

*.rtdb.example.com {
	tls {
		on_demand
	}
	reverse_proxy 127.0.0.1:8080
}
```

- يشير `ask` إلى العنوان الخاص. إن كان Caddy على جهاز آخر أو في حاوية، فاربط `metrics_listen` بواجهة لا
  يصلها إلا Caddy (شبكة خاصة)، لا بواجهة عامة: الجواب يكشف المشاريع الموجودة.
- أول اتصال بمضيف مشروع جديد ينتظر ثواني حتى تصدر شهادته، والمكتبات تنتظر 30 ثانية قبل أن تعيد المحاولة.
- يسمح Let's Encrypt بخمسين شهادة جديدة لكل نطاق مسجّل في الأسبوع. فوق هذا المعدل من المشاريع الجديدة، أو
  لتجنّب انتظار أول اتصال، استخدم شهادة wildcard بتحقق DNS-01 (نسخة Caddy فيها وحدة مزوّدك، أو lego أو
  certbot أو acme.sh). ولا يفرق عند Aruma كيف صدرت الشهادة.
- خلف وكيل Cloudflare تغطي الشهادة المجانية مستوى واحدًا تحت النطاق: اجعل سجل `*.rtdb` بلا وكيل (DNS only)،
  أو اختر `rtdb.domain` مستقلًا من جزأين.

## 4. الوكيل: احفظ اسم المضيف

قاعدة البيانات تُعرف بالمضيف الذي اتصل به العميل، والخادم يرسله للعميل في المصافحة ليعيد الاتصال به. لذلك
يجب على الوكيل أحد أمرين:

- **أن يحفظ ترويسة `Host`** كما أرسلها العميل (يفعل ذلك `reverse_proxy` في Caddy افتراضيًا، ويحتاج nginx
  `proxy_set_header Host $host;`)، أو
- **أن يرسل `X-Forwarded-Host`** بمضيف العميل، من عنوان مدرج في `trusted_proxies`
  (`ARUMA_TRUSTED_PROXIES`). لا يصدّق Aruma `X-Forwarded-Host` و`X-Forwarded-For` إلا من هذه العناوين.

الوكيل الذي يغيّر المضيف إلى `127.0.0.1:8080` بلا أحدهما يُفشل كل طلب بالخطأ "Specify the database".

## 5. المهلات: 60 ثانية خمول على الأقل

تبقي العملاء اتصالات WebSocket مفتوحة ساعات، وطلبات long polling حتى 20 ثانية لكل طلب. ترسل المكتبات
إشارة بقاء بعد 45 ثانية صمت، ويغلق Aruma الاتصال بعد 120 ثانية صمت. لذلك يجب أن تسمح كل طبقة بينهما
بخمول **60 ثانية على الأقل**، وأن تمرّر ترقية WebSocket (ترويستا `Upgrade` و`Connection`).

- **Caddy:** لا شيء؛ `reverse_proxy` يمرّر WebSocket بلا حد خمول.
- **nginx:** `proxy_http_version 1.1` مع ترويستي `Upgrade` و`Connection`، و`proxy_read_timeout 120s`
  و`proxy_send_timeout 120s` (المثال في القسم الإنجليزي أعلاه).
- **موازنات الأحمال** (AWS ALB وGoogle Cloud و`timeout tunnel` في HAProxy): ارفع مهلة الخمول إلى 120 ثانية
  أو أكثر.
- مهلة الطلب في Aruma (60 ثانية) وحد الجسم (1 MB) لا ينطبقان على اتصالات قاعدة البيانات.

## 6. التحقق

```sh
curl -s -o /dev/null -w '%{http_code}\n' 'http://127.0.0.1:9464/rtdb/tls-ask?domain=chat-app-default-rtdb.rtdb.example.com'  # 200
curl -s 'https://chat-app-default-rtdb.rtdb.example.com/.settings/rules.json'  # 401: المضيف يصل إلى القاعدة
```
