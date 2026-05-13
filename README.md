# Demo Go API Framework

## Šta je trenutno urađeno

Ovo je generički JSON-driven Go framework za CRUD i izveštaje preko REST API-ja.

Aplikacija radi ovako:

- `config.json` definiše konekciju ka bazi i putanju do JSON modula.
- `app.go` učitava sve module iz `modules/*.json`, razrešava lookup i submodule veze i priprema regex validacije.
- `migration.go` kreira i sinhronizuje šemu baze, audit kolone, soft-delete, indekse, FK-ove i RBAC tabele.
- `dataset.go` radi generički data access, validaciju upita, CRUD, lookup/submodule ekspanziju i RBAC proveru.
- `api.go` izlaže REST API, login/session tok i nested submodule rute.

Trenutno implementirano:

- JSON-driven moduli (`module`, `report`, `group`, `root`, `system`)
- automatske migracije tabela
- `is_deleted` soft delete
- `created_at` i `updated_at` audit kolone
- parcijalni unique indeksi
- FK ograničenja i performansni indeksi
- login sa session cookie pristupom
- RBAC bitmask dozvole
- REST CRUD API
- `includeDeleted` filtriranje
- batch submodule expansion za list i detail API odgovore
- `resources` tabela kao izvor zahtevane dozvole po modulu/resursu
- efektivna dozvola se računa kao presek: podržano na resursu AND dodeljeno korisniku

## Značenje važnih flagova u kolonama

### `is_visible`

Kontroliše da li se kolona prikazuje u listi i u prikazu reda.

### `is_editable`

Kontroliše da li kolona ulazi u create/edit formu i da li framework pokušava da je upisuje u `INSERT` i `UPDATE`.

Ne odnosi se na list view.

### `is_read_only`

To je tvrđa zabrana upisa. Ako je `true`, kolona se ne prikazuje kao input i preskače se i pri create i pri update čak i ako bi bila `is_editable: true`.
Praktično: `is_editable` određuje da li korisnik sme da menja polje, a `is_read_only` je zaštita da ga framework nikad ne tretira kao upisivo polje.

Preporučena semantika:

- lista: `is_visible`
- forma: `is_editable`
- sistemska ili izvedena polja: `is_read_only`

## RBAC i READ enforcement

RBAC se radi po resursu, gde je resurs `module_id`.

Primer:

- `module_products`
- `module_orders`
- `module_sales_report`

READ permission enforcement ne znači opštu zabranu pristupa celoj aplikaciji.
Znači zabranu pristupa konkretnom resursu.

U praksi bi READ enforcement trebalo da blokira:

- `GET /api/modules`
- `GET /api/{moduleID}`
- `GET /api/{moduleID}/{recordID}`
- `GET /api/{moduleID}/{recordID}/{submoduleID}`
- i filtrira listu modula tako da vrati samo resurse nad kojima korisnik ima `READ`

To ne znači zabranu login-a ili zabranu drugih modula na koje korisnik ima pravo.

## Model dozvola po resursu (novo)

Resurs je modul koji izlaže funkcionalnost:

- `modul`
- `report`
- `system`
- `custom`

`group` i `root` nisu permission resursi.

Sistem sada koristi tri izvora:

- `resources.required_permissions`: šta resurs podržava (sinhronizuje se iz JSON definicije modula)
- `role_permissions.permissions`: šta je roli dodeljeno
- `user_roles`: koje role korisnik ima

Efektivna dozvola se računa ovako:

- `effective = granted_permissions & supported_permissions`

Gde je:

- `granted_permissions`: OR svih rola korisnika za taj resurs (admin ima full mask)
- `supported_permissions`: `resources.required_permissions` za taj resurs

Ako akcija nije podržana na resursu, biće odbijena čak i ako je dodeljena kroz rolu.

## Autentikacija

Login je trenutno session-cookie model.

Uvedene auth rute:

- `POST /api/login`
- `POST /api/logout`
- `GET /api/auth/session`
- `POST /api/auth/csrf/refresh`

Napomena: zbog kompatibilnosti su i dalje podržani stari alias endpointi
`/login`, `/logout`, `/auth/session`, `/auth/csrf/refresh`, ali je preporuka da se koristi `/api/*` prefiks.

### `POST /api/login`

- Svrha:
  - Prijavljuje korisnika i postavlja `session` i `csrf_token` cookie.
- Parametri:
  - `username`
  - `password`
- Vraća:
  - JSON poruku, osnovne podatke o korisniku i `csrf_token`.

### `POST /api/logout`

- Svrha:
  - Briše aktivnu sesiju i cookie-je (`session`, `csrf_token`).
- Parametri:
  - Nema.
- Vraća:
  - JSON poruku o uspešnoj odjavi.

### `GET /api/auth/session`

- Svrha:
  - Vraća stanje aktivne sesije za već prijavljenog korisnika.
- Parametri:
  - Nema.
- Vraća:
  - `authenticated`, `user` i trenutni `csrf_token`.

### `POST /api/auth/csrf/refresh`

- Svrha:
  - Rotira CSRF token bez novog login-a.
- Parametri:
  - Nema body payload.
- Vraća:
  - JSON poruku i novi `csrf_token`, uz osvežen `csrf_token` cookie.

### CSRF pravilo za mutacije

Za sve mutacione rute (`POST`, `PUT`, `PATCH`, `DELETE`) koje se pozivaju sa `session` cookie-jem, obavezan je `X-CSRF-Token` (ili `X-XSRF-Token`) header sa važećim tokenom.

Praktično:

- `GET` rute ne traže CSRF header
- mutacione rute sa sesijom bez CSRF header-a vraćaju `403`
- posle `POST /api/auth/csrf/refresh` stari token je zamenjen novim

## Security config (auth)

`security` sekcija sada podržava i:

- `trust_proxy_headers` (bool)

Kada je `false` (default), `Secure` cookie se postavlja samo na realnom TLS zahtevu.
Kada je `true`, server sme da koristi i `X-Forwarded-Proto: https` za odluku o `Secure` cookie-ju (tipično iza trusted reverse proxy-ja).

## Health i Ready endpointi

- `GET /health`
  - proverava da je proces živ
  - vraća `200` sa status payload-om
- `GET /ready`
  - proverava da je servis spreman za saobraćaj
  - proverava dostupnost baze (`SELECT 1`) i stanje migracija
  - vraća `200` kada je spremno, `503` kada nije

## Migration safety

### Dry-run migracija

Za plan promena bez izvršenja SQL upisa koristi:

```bash
MIGRATIONS_DRY_RUN=true go run .
```

U dry-run modu:

- migracije se samo isplaniraju i loguju (`MIGRATION DRY-RUN`)
- SQL upisi se ne izvršavaju
- API server se ne pokreće nakon planiranja

### Kratka backup procedura pre deploy-a

Pre svakog produkcionog deploy-a sa migracijama:

1. Napravi backup baze:

```bash
PGPASSWORD="$DB_PASSWORD" pg_dump \
  -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
  -Fc -f "backup_$(date +%Y%m%d_%H%M%S).dump"
```

1. Pokreni migracije u dry-run modu i pregledaj plan:

```bash
MIGRATIONS_DRY_RUN=true go run .
```

1. Ako plan izgleda ispravno, pokreni normalan deploy.

## Faza 2 status

Faza 2 je završena kroz sledeće operativne stavke:

- Observability: request_id je uveden kroz ceo request lifecycle.
- Observability: access log je strukturisan sa poljima request_id, user_id, method, route, status, bytes, duration_ms.
- Observability: isti request_id se koristi u error response i audit upisima.
- Health i readiness: `GET /health` proverava liveness procesa.
- Health i readiness: `GET /ready` proverava dataset, stanje migracija i dostupnost baze (`SELECT 1`).
- Migration safety: podržan je dry-run režim migracija (`MIGRATIONS_DRY_RUN=true`) koji loguje plan bez izvršavanja SQL upisa.
- Migration safety: u dry-run režimu aplikacija završava nakon planiranja, bez pokretanja HTTP servera.
- Migration safety: u ovom README je dodata kratka backup procedura pre deploy-a.
- Verifikacija: `go test ./...` prolazi.
- Verifikacija: `go build ./...` prolazi.

## Faza 3 status (release zatvaranje)

Faza 3 je završena kao final polish za v1.

- Dokumentacija: dodat je API contract deo sa stabilnim response oblikom.
- Dokumentacija: dodati su audit endpoint filter primeri.
- Dokumentacija: dodati su deploy koraci od nule do running instance.
- Test završnica: audit testovi pokrivaju filter po `module_id`.
- Test završnica: audit testovi pokrivaju filter po `action` i date range (`from`, `to`).
- Test završnica: pristup auditu za non-admin je zabranjen i testiran.
- Release readiness: scope je spreman za `v1.0.0` uz politiku bugfix-only.

## API Contract (v1)

Do v2 važi pravilo stabilnog API ugovora za ključne odgovore.

### Standardni error odgovor

Error shape je stabilan:

```json
{
  "error": true,
  "code": 403,
  "message": "forbidden",
  "details": null,
  "request_id": "req_..."
}
```

### Audit read odgovor

Audit read endpoint (`GET /api/audit`) vraća stabilan shape:

```json
{
  "data": [
    {
      "id": 1,
      "module_id": "module_orders",
      "record_id": "55",
      "action": "update",
      "actor_user_id": 99,
      "actor_username": "admin",
      "request_id": "req_1",
      "old_data": null,
      "new_data": null,
      "created_at": "2026-05-10T10:00:00Z"
    }
  ],
  "meta": {
    "limit": 100,
    "offset": 0,
    "count": 1
  }
}
```

## Audit endpoint filter primeri

Audit endpoint je admin-only i podržava filtere: `module_id`, `record_id`, `actor_user_id`, `actor_username`, `action`, `from`, `to`, `limit`, `offset`, `sort_by`, `sort_dir`.

### Filter po modulu

```bash
curl -b cookies.txt "http://localhost:8080/api/audit?module_id=module_orders"
```

### Filter po akciji i vremenskom opsegu

```bash
curl -b cookies.txt "http://localhost:8080/api/audit?action=update&from=2026-05-01&to=2026-05-10"
```

### Filter po korisniku i paginaciji

```bash
curl -b cookies.txt "http://localhost:8080/api/audit?actor_username=admin&limit=50&offset=0"
```

Napomena za vreme:

- `from` i `to` prihvataju RFC3339 (`2026-05-10T10:00:00Z`) ili `YYYY-MM-DD`.

## Deploy koraci (od nule do running instance)

1. Pripremi PostgreSQL bazu i kredencijale.
2. Podesi `config.json` (`host`, `port`, `user`, `password`, `db_name`, `ssl_mode`, `modules_path`).
3. Opcionalno proveri plan migracija bez izvršenja:

   ```bash
   MIGRATIONS_DRY_RUN=true go run .
   ```

4. Napravi backup pre produkcionih migracija (`pg_dump`).

5. Pokreni servis normalno:

   ```bash
   go run .
   ```

6. Proveri liveness:

   ```bash
   curl http://localhost:8080/healthy
   ```

7. Proveri readiness:

   ```bash
   curl http://localhost:8080/ready
   ```

8. Uradi login i sačuvaj cookie:

   ```bash
   curl -c cookies.txt -H "Content-Type: application/json" \
     -d '{"username":"admin","password":"admin"}' \
     -X POST http://localhost:8080/api/login
   ```

9. Proveri session stanje:

   ```bash
   curl -b cookies.txt http://localhost:8080/api/auth/session
   ```

10. Testiraj jedan modul endpoint i audit endpoint.

## Finalni release checklist (v1.0.0)

- [x] API contract dokumentovan i stabilizovan.
- [x] Audit filter primeri dokumentovani.
- [x] Deploy koraci dokumentovani od nule do running instance.
- [x] Audit integration testovi prošireni (`module_id`, `action + date range`, non-admin forbidden).
- [x] `go test ./...` prolazi.
- [x] `go build ./...` prolazi.
- [x] Scope zatvoren na bugfix-only za v1 tag.

## Generičke API rute

> [!Note]
> **Napomena**:  
> Ispod su generičke rute.  
> `moduleID` se menja po konkretnom modulu.

### `GET /api/modules`

- Svrha:
  - Vraća hijerarhiju modula za navigaciju.
- Parametri:
  - Nema.
- Vraća:
  - JSON tree strukturu aplikacije, grupa i modula.

### `GET /api/{moduleID}`

- Svrha:
  - Vraća listu zapisa ili izveštaj za jedan modul.
- Parametri query string:
  - `_limit`: maksimalan broj redova
  - `_offset`: pomeraj
  - `_sort`: sortiranje, npr. `name,-price`
  - `_search`: tekstualna pretraga po string kolonama
  - `_include_deleted` ili `includeDeleted` ili `include_deleted`: uključuje soft-deleted redove
  - filteri po kolonama, npr. `name=Test`, `price__gt=10`, `category_id__in=1,2,3`
- Vraća:
  - JSON niz zapisa.

### `GET /api/{moduleID}/{recordID}`

- Svrha:
  - Vraća jedan zapis po ID-u.
- Parametri:
  - `recordID`: vrednost primarnog ključa
- Vraća:
  - JSON objekat jednog zapisa, uključujući ekspandovane `lookup` i `sub_modules` podatke ako su definisani.

### `GET /api/{moduleID}/{recordID}/{submoduleID}`

- Svrha:
  - Vraća child zapise za konkretan parent zapis i definisani submodule odnos.
- Parametri:
  - `recordID`: parent zapis
  - `submoduleID`: `submodule.id` ili `target_module_id`
  - query string podržava iste filtere kao i standardna list ruta; parent FK filter se uvek nameće iz rute
- Vraća:
  - JSON objekat sa parent kontekstom, metadata opisom submodule veze i nizom child zapisa.

### `GET /api/{moduleID}/{recordID}/{submoduleID}/{childRecordID}`

- Svrha:
  - Vraća jedan child zapis u kontekstu parent submodule relacije.
- Parametri:
  - `recordID`: parent zapis
  - `submoduleID`: `submodule.id` ili `target_module_id`
  - `childRecordID`: ID child zapisa
- Vraća:
  - JSON objekat sa parent kontekstom, metadata opisom submodule veze i jednim child zapisom.

### `POST /api/{moduleID}/{recordID}/{submoduleID}`

- Svrha:
  - Kreira child zapis vezan za parent zapis.
- Parametri:
  - JSON payload sa kolonama child modula
  - `child_foreign_key_field` se automatski popunjava iz parent zapisa i ne treba ga slati ručno
- Vraća:
  - JSON poruku i ID novog child zapisa.

### `PUT /api/{moduleID}/{recordID}/{submoduleID}/{childRecordID}`

- Svrha:
  - Ažurira child zapis koji pripada parent zapisu.
- Parametri:
  - `recordID`: parent zapis
  - `submoduleID`: `submodule.id` ili `target_module_id`
  - `childRecordID`: ID child zapisa
  - JSON payload sa kolonama child modula
- Vraća:
  - JSON poruku o uspešnoj izmeni child zapisa.

### `DELETE /api/{moduleID}/{recordID}/{submoduleID}/{childRecordID}`

- Svrha:
  - Briše child zapis koji pripada parent zapisu.
- Parametri:
  - `recordID`: parent zapis
  - `submoduleID`: `submodule.id` ili `target_module_id`
  - `childRecordID`: ID child zapisa
- Vraća:
  - JSON poruku o uspešnom brisanju child zapisa.

### `POST /api/{moduleID}`

- Svrha:
  - Kreira novi zapis za `moduleID` modul.
- Parametri:
  - JSON payload sa upisivim kolonama modula.
- Vraća:
  - JSON sa ID-em ili porukom o uspehu, zavisno od handler toka.

### `PUT /api/{moduleID}/{recordID}`

- Svrha:
  - Ažurira postojeći zapis.
- Parametri:
  - `recordID`
  - JSON payload sa poljima za izmenu
- Vraća:
  - JSON poruku o uspehu.

### `DELETE /api/{moduleID}/{recordID}`

- Svrha:
  - Soft-delete zapisa (`is_deleted = true`).
- Parametri:
  - `recordID`
- Vraća:
  - JSON poruku o uspehu.

## Spisak modula i poziva po modulu

Za sve `module` module važi isti obrazac poziva:

- API lista: `/api/{moduleID}`
- API jedan zapis: `/api/{moduleID}/{recordID}`
- API submodule lista: `/api/{moduleID}/{recordID}/{submoduleID}`
- API submodule create: `POST /api/{moduleID}/{recordID}/{submoduleID}`
- API create: `POST /api/{moduleID}`
- API update: `PUT /api/{moduleID}/{recordID}`
- API delete: `DELETE /api/{moduleID}/{recordID}`

Za `report` module važi read-only obrazac:

- API lista: `/api/{moduleID}`

## Konkretna implementacija

### `module_products` — Proizvodi

- Svrha: osnovni katalog proizvoda sa nazivom, cenom i opisom.
- Parametri: standardni filter parametri za listu; create/update payload koristi `name`, `price`, `description`.
- Vraća: listu proizvoda ili jedan proizvod, zavisno od rute.

### `module_categories` — Kategorije

- Svrha: šifarnik kategorija proizvoda.
- Parametri: standardni filter parametri; create/update payload koristi kategorijske kolone modula.
- Vraća: listu kategorija ili jedan zapis kategorije.

### `module_users` — Korisnici

- Svrha: upravljanje korisnicima aplikacije i admin flagom.
- Parametri: create/update payload tipično koristi `username`, `email`, `is_admin`; `password_hash` se ne izlaže kroz API odgovor.
- Vraća: listu korisnika ili jedan korisnički zapis.

### `module_orders` — Narudžbine

- Svrha: zaglavlja narudžbina sa vezom prema kupcu.
- Parametri: filteri po kolonama i create/update payload za broj narudžbine i lookup polja.
- Vraća: listu narudžbina ili jedan zapis narudžbine.

### `module_order_items` — Stavke Narudžbine

- Svrha: detaljne stavke narudžbine sa proizvodom i količinom.
- Parametri: create/update payload koristi FK polja i količinu.
- Vraća: listu stavki ili jedan zapis stavke.

### `module_comments` — Komentari

- Svrha: komentari vezani za roditeljski zapis, npr. proizvod ili narudžbinu.
- Parametri: create/update payload koristi `parent_id` i `comment_text`.
- Vraća: listu komentara ili jedan komentar.

### `module_product_categories` — Veze Proizvod-Kategorija

- Svrha: veza više-prema-više između proizvoda i kategorija.
- Parametri: create/update payload koristi lookup/FK kolone za proizvod i kategoriju.
- Vraća: listu veza ili jedan zapis veze.

### `module_product_with_categories_report` — Proizvodi po kategorijama

- Svrha: read-only izveštaj koji spaja proizvode i kategorije.
- Parametri: list query parametri za filtriranje/sortiranje gde su podržani.
- Vraća: JSON ili HTML listu rezultata iz `select_query`.

### `module_sales_report` — Izveštaj o prodaji

- Svrha: agregirani izveštaj prodaje po proizvodu.
- Parametri: read-only list query parametri.
- Vraća: zbirne redove iz `select_query`.

### `module_app_settings` — Podešavanja

- Svrha: sistemska podešavanja aplikacije.
- Parametri: zavise od definicije modula; trenutno je to sistemski modul i nije centralan u CRUD toku kao table moduli.
- Vraća: sistemske vrednosti prema definiciji modula.

## Dozvole iz JSON modula

Mogući flagovi na nivou modula sada se učitavaju iz JSON-a:

- `can_read`
- `can_create`
- `can_update`
- `can_delete`
- `can_execute`
- `can_export`
- `can_import`
- `can_approve`

Ti flagovi određuju dve stvari:

- koje dozvole resurs uopšte podržava
- koje default dozvole dobija osnovna `User` rola kroz migracije
- vrednost `resources.required_permissions` za taj resurs

Trenutna politika za default `User` rolu:

- dobija `READ` ako modul ima `can_read: true`
- dobija `CREATE` samo ako modul ima `can_create: true`
- ostale dozvole ostaju isključene dok ih eksplicitno ne dodeliš
- `system` moduli su po default-u admin-only (default `User` ne dobija dozvole za njih)

## Kako se koristi u praksi

Preporučeni admin tok:

1. Definiši modul/resurs u `modules/*.json` sa `can_*` flagovima.
2. Pokreni aplikaciju ili migracije da se `resources` automatski sinhronizuje.
3. Admin kreira role i dodeli role korisnicima.
4. Admin postavlja `role_permissions` po resursu.
5. Runtime provera radi automatski kroz `HasPermission`.

Za proveru stanja u bazi:

```sql
SELECT resource, resource_type, required_permissions
FROM resources
ORDER BY resource;
```

```sql
SELECT r.name AS role_name, rp.resource, rp.permissions
FROM role_permissions rp
JOIN roles r ON r.id = rp.role_id
ORDER BY r.name, rp.resource;
```

## Submodule definicija u JSON-u

`sub_modules` u parent modulu definiše child relaciju za FK sync i nested API rute.

Primer:

```json
"sub_modules": [
  {
    "id": "order_items",
    "display_name": "Stavke narudžbine",
    "parent_key_field": "id",
    "target_module_id": "module_order_items",
    "child_foreign_key_field": "order_id",
    "display_order": 1
  }
]
```

Semantika polja:

- `id`: opciona stabilna oznaka relacije za API rutu; ako nije setovan, može da se koristi `target_module_id`
- `display_name`: opis relacije za API metadata odgovor i eventualnu administraciju
- `parent_key_field`: parent kolona koja se koristi za join; može biti i polje koje nije vidljivo u standardnom API odgovoru, jer ga backend čita direktno iz baze
- `target_module_id`: child modul
- `child_foreign_key_field`: FK kolona u child modulu
- `display_order`: redosled kojim želiš da relacije budu prikazane ili dokumentovane

Trenutni nested API tok je:

- `GET /api/module_orders/1/module_order_items`
- `GET /api/module_orders/1/module_order_items/10`
- `POST /api/module_orders/1/module_order_items`
- `PUT /api/module_orders/1/module_order_items/10`
- `DELETE /api/module_orders/1/module_order_items/10`

Kod `POST` child FK se uvek uzima iz parent relacije i prepisuje preko eventualne vrednosti iz payload-a.

## Korisni primeri poziva

### Login

```bash
curl -c cookies.txt -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin"}' \
  -X POST http://localhost:8080/api/login
```

Primer odgovora (skraćeno):

```json
{
  "message": "login successful",
  "csrf_token": "...",
  "user": {
    "id": 1,
    "username": "admin",
    "is_admin": true
  }
}
```

### Session bootstrap

```bash
curl -b cookies.txt http://localhost:8080/api/auth/session
```

### CSRF refresh

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -H "X-CSRF-Token: ${CSRF_TOKEN}" -X POST \
  http://localhost:8080/api/auth/csrf/refresh
```

### Lista proizvoda

```bash
curl -b cookies.txt http://localhost:8080/api/module_products
```

### Lista proizvoda sa obrisanim zapisima

```bash
curl -b cookies.txt http://localhost:8080/api/module_products?includeDeleted=true
```

### Kreiranje proizvoda

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -H "Content-Type: application/json" -H "X-CSRF-Token: ${CSRF_TOKEN}" -X POST \
  -d '{"name":"Test","price":99.5,"description":"Opis"}' \
  http://localhost:8080/api/module_products
```

### Lista child zapisa za parent

```bash
curl -b cookies.txt \
  http://localhost:8080/api/module_orders/1/module_order_items
```

### Kreiranje child zapisa kroz parent relaciju

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -H "Content-Type: application/json" -H "X-CSRF-Token: ${CSRF_TOKEN}" -X POST \
  -d '{"product_id":2,"quantity":3}' \
  http://localhost:8080/api/module_orders/1/module_order_items
```

### Jedan child zapis kroz parent relaciju

```bash
curl -b cookies.txt \
  http://localhost:8080/api/module_orders/1/module_order_items/10
```

### Ažuriranje child zapisa kroz parent relaciju

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -H "Content-Type: application/json" -H "X-CSRF-Token: ${CSRF_TOKEN}" -X PUT \
  -d '{"quantity":5}' \
  http://localhost:8080/api/module_orders/1/module_order_items/10
```

### Brisanje child zapisa kroz parent relaciju

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -H "X-CSRF-Token: ${CSRF_TOKEN}" -X DELETE \
  http://localhost:8080/api/module_orders/1/module_order_items/10
```

### Ažuriranje proizvoda

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -X PUT -H "Content-Type: application/json" -H "X-CSRF-Token: ${CSRF_TOKEN}" \
  -d '{"name":"Novo ime","price":120}' \
  http://localhost:8080/api/module_products/1
```

### Soft-delete proizvoda

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -H "X-CSRF-Token: ${CSRF_TOKEN}" -X DELETE \
  http://localhost:8080/api/module_products/1
```

### Logout

```bash
CSRF_TOKEN="<token-iz-login-odgovora-ili-auth-session>"
curl -b cookies.txt -H "X-CSRF-Token: ${CSRF_TOKEN}" -X POST http://localhost:8080/api/logout
```

## Trenutna ograničenja

- sesije su i dalje in-memory; restart servera briše aktivne prijave
- default admin lozinka je inicijalno `admin`; promena lozinke i password reset workflow još ne postoje
- nested submodule CRUD je dostupan kroz parent kontekst, ali i dalje koristi standardnu child permission logiku
- duboka rekurzivna submodule stabla i dalje mogu da traže dodatno batchovanje

## Status

Ovaj dokument je ažuriran sa svim promenama iz trenutnog ciklusa rada (RBAC resursi, auto-sync migracije, efektivne dozvole i refaktor permission toka).

## Feature list (v1)

- JSON-driven moduli (`root`, `group`, `module`, `report`, `system`) učitani iz `modules/*.json`
- Automatske migracije šeme sa audit kolonama, soft-delete, indeksima i FK vezama
- RBAC po resursu (`resources`, `roles`, `role_permissions`, `user_roles`) sa efektivnim maskama dozvola
- Session-cookie autentikacija sa CSRF zaštitom i CSRF token refresh tokom sesije
- Login rate limit, session TTL i CORS whitelist kroz `security` konfiguraciju
- REST CRUD za module i nested submodule rute kroz parent kontekst
- Dinamički filter/sort/pagination (`_limit`, `_offset`, `_sort`, `_search`, `includeDeleted`)
- Lookup i submodule ekspanzija u API odgovorima
- Audit trail upisi (`create`, `update`, `delete`) sa admin read endpoint-om i filterima
- Observability minimum: request_id propagation i strukturisani access log
- Operativni endpointi: `GET /health` (liveness) i `GET /ready` (readiness)
- Migration safety: dry-run mod (`MIGRATIONS_DRY_RUN=true`) i pre-deploy backup procedura
- Test pokrivenost za auth, module, submodule i audit ključne tokove
