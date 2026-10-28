# سجلات القرارات المعمارية (ADRs)

كل قرار معماري يُكتب هنا قبل تنفيذه أو معه، ولا يُعدَّل بعد اعتماده. والقرار الذي يتغير يُكتب له سجل جديد يحل محل القديم.

السجلات ADR-001 إلى ADR-003 وADR-005 وADR-006 كُتبت في مرحلة التخطيط، وليست جزءًا من هذا المستودع. وADR-004 حجزته الخطة لعقد الرمز، فكُتب هنا مع المرحلة 2.

| السجل | الموضوع | الحالة |
|---|---|---|
| [ADR-004](ADR-004-token-contract.md) | عقد رمز المستخدم ومُصدِره | معتمد (المرحلة 2) |
| [ADR-007](ADR-007-projects-and-keys.md) | المشاريع والمفاتيح والمعرّفات | مجمّد. و`databaseURL` في ADR-018 |
| [ADR-008](ADR-008-single-entry.md) | المدخل الواحد | معتمد |
| [ADR-009](ADR-009-data-tooling.md) | أدوات البيانات | معتمد |
| [ADR-010](ADR-010-secure-defaults.md) | الافتراضيات الآمنة | معتمد. وعلى سطح قاعدة المستندات: جزء من S-3، وS-11، في ADR-029 (مسودة)، وجزء من S-8 (CORS على مسارات البيانات، بشرط) في ADR-027 (مسودة) |
| [ADR-011](ADR-011-calibration.md) | معايرة التقدير (v0) | معتمد |
| [ADR-012](ADR-012-phase-durations.md) | مدد المراحل: المقيس والمقدَّر | معتمد |
| [ADR-013](ADR-013-one-line.md) | تعريف «السطر الواحد» | معتمد (المرحلة 2) |
| [ADR-014](ADR-014-auth-endpoints.md) | استضافة نقاط Auth وCORS | معتمد (المرحلة 2) |
| [ADR-015](ADR-015-services-layout.md) | تخطيط الخدمات وحدودها، وتجميد الـ migrations | معتمد (المرحلة 2) |
| [ADR-016](ADR-016-verification-libraries.md) | لغات مكتبة التحقق: Node وGo | معتمد |
| [ADR-017](ADR-017-postgres-rls.md) | العزل داخل Postgres (RLS): مؤجّل | معتمد (تأجيل) |
| [ADR-018](ADR-018-rtdb-database-url.md) | رابط Realtime Database ونطاقه وشهادته | معتمد (RTDB، R0) |
| [ADR-019](ADR-019-rtdb-storage-and-ordering.md) | تخزين Realtime Database وترتيب الكتابة وحدودها | معتمد (RTDB، R0) |
| [ADR-020](ADR-020-rtdb-v02-scope.md) | نطاق RTDB في 0.2: الاستعلامات والقواعد وREST والاستطلاع الطويل | معتمد (RTDB، R0) |
| [ADR-021](ADR-021-rules-language.md) | محرك قواعد Firestore وStorage: لغة واحدة مكتوبة يدويًا في `platform/rules` | معتمد (v0.3، S0) |
| [ADR-022](ADR-022-storage-architecture.md) | Cloud Storage for Firebase: المعمارية | معتمد (v0.3، S0) |
| [ADR-023](ADR-023-v03-scope.md) | نطاق 0.3: لغة القواعد وCloud Storage | معتمد (v0.3، S0). وصفّان من §2 في ADR-028 (مسودة) |
| [ADR-024](ADR-024-documents-storage-ordering-locks.md) | قاعدة المستندات: التخزين، وترتيب الكتابات بصف متسلسل، ومدير الأقفال بـ wound-wait | مسودة (v0.4، F0) |
| [ADR-025](ADR-025-documents-queries-indexes-transactions.md) | قاعدة المستندات: الاستعلامات والفهارس وفرضها، والمعاملات بحسب الطالب | مسودة (v0.4، F0) |
| [ADR-026](ADR-026-documents-listen.md) | قاعدة المستندات: البث وتدفق الكتابة، والحدود والإلغاء والنسخة الواحدة | مسودة (v0.4، F0) |
| [ADR-027](ADR-027-documents-transport-and-protos.md) | قاعدة المستندات: النقل (connect على المنفذ الواحد) والـ protos، وسطح المتصفح، ويحل محل جزء من ADR-010 S-8 بشرط | مسودة (v0.4، F0) |
| [ADR-028](ADR-028-documents-v04-scope-and-verification.md) | قاعدة المستندات: الاسم ونطاق 0.4 وجهاز التحقق | مسودة (v0.4، F0) |
| [ADR-029](ADR-029-documents-admin-auth.md) | قاعدة المستندات: مصادقة Admin بـ JWT حساب الخدمة، ويحل محل جزء من ADR-010 S-3 وS-11 على سطحها | مسودة (v0.4، F0) |
| [ADR-030](ADR-030-documents-console.md) | قاعدة المستندات: واجهة اللوحة (console API) | مسودة (v0.4، F7) |

## الصيغة

- الحالة، والتاريخ، والمرحلة، والمراجع
- السياق: لماذا نحتاج قرارًا
- القرار
- البدائل المرفوضة، ولماذا رُفضت
- العواقب
