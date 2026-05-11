# Orders + Order Items: korak po korak

Ovaj mini vodic pokazuje kako da:

1. upises podatke u module_orders (parent)
2. upises podatke u module_order_items (child)
3. vratis podatke iz oba modula

Napomena:

- Ispravan naziv child modula je module_order_items (ne module_orders_items).
- Primeri koriste cookie sesiju iz login koraka.

## 0) Pokreni API

Ako vec nije pokrenut:

```bash
go run .
```

API je na:

- <http://localhost:8080>

## 1) Login (dobij cookie)

```bash
curl -c cookies.txt -H "Content-Type: application/json" \
  -X POST http://localhost:8080/api/login \
  -d '{"username":"admin","password":"admin123"}'
```

Ako je login uspesan, cookie se cuva u cookies.txt i koristis ga u narednim pozivima sa -b cookies.txt.

## 2) Upis parent zapisa u module_orders

Polja koja realno saljes:

- order_number (required)
- customer_id (lookup na module_users)

Primer create:

```bash
curl -b cookies.txt -H "Content-Type: application/json" -X POST \
  <http://localhost:8080/api/modules/module_orders> \
  -d '{
    "order_number": "ORD-1001",
    "customer_id": 1
  }'
```

Ocekivan odgovor (primer):

```json
{
  "id": 1
}
```

## 3) Citanje podataka iz module_orders

Lista svih narudzbina:

```bash
curl -b cookies.txt http://localhost:8080/api/modules/module_orders
```

Jedna narudzbina po ID:

```bash
curl -b cookies.txt http://localhost:8080/api/modules/module_orders/1
```

## 4) Upis child zapisa u module_order_items (preko parent rute)

Najbolja praksa je nested ruta jer order_id dolazi iz parent ID-a u URL-u.

Ruta:

- POST /api/modules/module_orders/{orderID}/submodules/module_order_items

Primer create stavke za order 1:

```bash
curl -b cookies.txt -H "Content-Type: application/json" -X POST \
  <http://localhost:8080/api/modules/module_orders/1/submodules/module_order_items> \
  -d '{
    "product_id": 2,
    "quantity": 3
  }'
```

Ocekivan odgovor (primer):

```json
{
  "id": 10
}
```

Napomena:

- U payload obicno ne moras da saljes order_id kada koristis nested rutu.
- Backend povezuje child sa parent narudzbinom preko order_id.

## 5) Citanje child podataka preko parent rute

Sve stavke za jednu narudzbinu:

```bash
curl -b cookies.txt \
  http://localhost:8080/api/modules/module_orders/1/submodules/module_order_items
```

Jedna stavka po child ID-u (uz proveru da pripada parent-u):

```bash
curl -b cookies.txt \
  http://localhost:8080/api/modules/module_orders/1/submodules/module_order_items/10
```

## 6) Citanje module_order_items direktno (bez parent rute)

Mozes i direktno da citas modul:

```bash
curl -b cookies.txt http://localhost:8080/api/modules/module_order_items
```

I jedan zapis:

```bash
curl -b cookies.txt http://localhost:8080/api/modules/module_order_items/10
```

## 7) Najkraci realan flow

1. Login
2. POST module_orders -> dobijes order ID
3. POST nested module_order_items za taj order ID
4. GET nested list da vratis sve stavke te narudzbine
5. GET module_orders da vratis parent listu

Time imas i parent i child podatke, povezane kroz order_id.

## 8) Izmena i brisanje parent zapisa (module_orders)

Izmena narudzbine (npr. promena broja narudzbine ili kupca):

```bash
curl -b cookies.txt -H "Content-Type: application/json" -X PUT \
  http://localhost:8080/api/modules/module_orders/1 \
  -d '{
    "order_number": "ORD-1001-REV1",
    "customer_id": 1
  }'
```

Brisanje narudzbine (soft delete):

```bash
curl -b cookies.txt -X DELETE \
  http://localhost:8080/api/modules/module_orders/1
```

## 9) Izmena i brisanje child zapisa (module_order_items)

Preporuka: koristi nested rutu da backend proveri da child zapis pripada tom parent-u.

Izmena stavke (za order 1, child 10):

```bash
curl -b cookies.txt -H "Content-Type: application/json" -X PUT \
  http://localhost:8080/api/modules/module_orders/1/submodules/module_order_items/10 \
  -d '{
    "product_id": 2,
    "quantity": 5
  }'
```

Brisanje stavke (za order 1, child 10):

```bash
curl -b cookies.txt -X DELETE \
  http://localhost:8080/api/modules/module_orders/1/submodules/module_order_items/10
```

## 10) Kompletan CRUD tok (parent + child)

1. Login i sacuvaj cookie.
2. Kreiraj parent (`module_orders`) i sacuvaj vraceni `id`.
3. Kreiraj child (`module_order_items`) preko nested rute.
4. Citaj parent listu i parent by ID.
5. Citaj child listu i child by ID preko nested rute.
6. Izmeni child (PUT nested).
7. Izmeni parent (PUT module_orders/{id}).
8. Obrisi child (DELETE nested).
9. Obrisi parent (DELETE module_orders/{id}).

Ovim redosledom mozes lako da testiras i referencijalnu povezanost (order_id), i sve osnovne API operacije nad oba modula.

## 11) Isti flow automatski (bez rucnog kucanja ID-eva, uz jq)

Provera da li imas jq:

```bash
jq --version
```

Ako jq nije instaliran (Ubuntu/Debian):

```bash
sudo apt-get update && sudo apt-get install -y jq
```

Kopiraj i pokreni ceo blok:

```bash
set -e

BASE_URL="http://localhost:8080"
COOKIE_FILE="cookies.txt"

echo "1) Login"
curl -s -c "$COOKIE_FILE" -H "Content-Type: application/json" \
  -X POST "$BASE_URL/api/login" \
  -d '{"username":"admin","password":"admin123"}' | jq .

echo "2) Create order"
ORDER_ID=$(curl -s -b "$COOKIE_FILE" -H "Content-Type: application/json" -X POST \
  "$BASE_URL/api/modules/module_orders" \
  -d '{"order_number":"ORD-AUTO-1001","customer_id":1}' | jq -r '.id')
echo "ORDER_ID=$ORDER_ID"

echo "3) Create order item (nested)"
ITEM_ID=$(curl -s -b "$COOKIE_FILE" -H "Content-Type: application/json" -X POST \
  "$BASE_URL/api/modules/module_orders/$ORDER_ID/submodules/module_order_items" \
  -d '{"product_id":2,"quantity":3}' | jq -r '.id')
echo "ITEM_ID=$ITEM_ID"

echo "4) Read order by ID"
curl -s -b "$COOKIE_FILE" "$BASE_URL/api/modules/module_orders/$ORDER_ID" | jq .

echo "5) Read nested items"
curl -s -b "$COOKIE_FILE" \
  "$BASE_URL/api/modules/module_orders/$ORDER_ID/submodules/module_order_items" | jq .

echo "6) Update item"
curl -s -b "$COOKIE_FILE" -H "Content-Type: application/json" -X PUT \
  "$BASE_URL/api/modules/module_orders/$ORDER_ID/submodules/module_order_items/$ITEM_ID" \
  -d '{"product_id":2,"quantity":5}' | jq .

echo "7) Update order"
curl -s -b "$COOKIE_FILE" -H "Content-Type: application/json" -X PUT \
  "$BASE_URL/api/modules/module_orders/$ORDER_ID" \
  -d '{"order_number":"ORD-AUTO-1001-REV1","customer_id":1}' | jq .

echo "8) Delete item"
curl -s -b "$COOKIE_FILE" -X DELETE \
  "$BASE_URL/api/modules/module_orders/$ORDER_ID/submodules/module_order_items/$ITEM_ID" | jq .

echo "9) Delete order"
curl -s -b "$COOKIE_FILE" -X DELETE \
  "$BASE_URL/api/modules/module_orders/$ORDER_ID" | jq .

echo "Done."
```
