# Лабораторная — отчёт

Стек для замеров: локальный compose, 26.10.2026. Railway не использовался.
Окружение: Windows, Go 1.26.3, Docker Engine 29.7.2, gateway на
`http://localhost:8080`. Временные метки логов приведены в UTC (МСК = UTC+3).

Нагрузка создавалась утилитой `lab/load` через HTTP. Redis CLI и psql вызывались
в контейнерах, Kafka CLI — из `apache/kafka:3.9.2` в сети `compose_default`.
Команды Bash ниже показывают сценарии повторения с теми же параметрами.
На Windows утилита предварительно собиралась и запускалась напрямую:

```powershell
go -C lab/load build -buildvcs=false -o "$env:TEMP\marketplace-lab-load.exe" .
& "$env:TEMP\marketplace-lab-load.exe" -base http://localhost:8080 -mode get -n 5000 -c 50 -listings 100
```

`-buildvcs=false` потребовался из-за проверки владельца репозитория Git в среде
исполнения. Неудачные прогоны приведены вместе с повторными. Код сервисов
во время лабораторной не менялся.

---

## 1. Кэш: hit rate и хвост задержек

При работающем Redis выполнено 5000 GET по 100 новым листингам:

```bash
make load ARGS="-mode get -n 5000 -c 50 -listings 100"
```

Вывод `load` (распределение `200 HIT` / `200 MISS`, p50/p95/p99):

```
5000 requests in 9.074s  (551 req/s)
p50 55.4ms   p95 285.2ms   p99 822.9ms   max 1.0665s
  200 HIT        4874  (97.5%)
  200 MISS        126  (2.5%)
```

Статистика Redis до и после нагрузки:

```bash
docker compose -f compose/docker-compose.yml exec -T redis redis-cli INFO stats | grep keyspace
```

```
До:
keyspace_hits:0
keyspace_misses:0

После:
keyspace_hits:4874
keyspace_misses:126
```

При остановленном Redis повторена нагрузка с теми же параметрами:

```bash
docker compose -f compose/docker-compose.yml stop redis
make load ARGS="-mode get -n 5000 -c 50 -listings 100"
docker compose -f compose/docker-compose.yml start redis
```

```
5000 requests in 4.911s  (1018 req/s)
p50 32.1ms   p95 162.5ms   p99 337.2ms   max 753.1ms
  200 BYPASS     5000  (100.0%)
```

Redis после проверки запущен. Деградация прошла без HTTP-ошибок: все 5000
запросов вернули 200 BYPASS.

**Ответы.**

1. **Hit rate — 97,48%.** Утилита при каждом запуске создаёт новые 100 листингов
   и отдельно не прогревает все ключи. Даже при одном MISS на ключ максимум
   составит `(5000−100)/5000 = 98%`, а не 99%. Дополнительные 26 MISS согласуются
   с параллельными чтениями холодных ключей: несколько запросов успевают пройти
   в БД до заполнения кэша. TTL — 30 секунд; истечений ключей за этот прогон
   Redis не зарегистрировал.
2. **Без Redis p50 уменьшился на 23,3 мс, p99 — на 485,7 мс.** Это результат
   одного последовательного замера на локальной VM. Порядок запусков, прогрев
   соединений и нагрузка окружения не контролировались, поэтому он не доказывает,
   что Redis в целом замедляет систему. p50 описывает типичный запрос, p99 — хвост
   задержек. Для пользователя важны оба; p99 особенно существенен при оценке SLO
   и экранов с несколькими зависимыми запросами.
3. **Возможны до 100 чтений Postgres на один холодный ключ.** Это cache stampede:
   координации между MISS в реализации нет. Singleflight объединяет запросы
   внутри процесса, распределённый lock координирует экземпляры сервиса.
   Jitter TTL разносит истечения разных ключей, но сам по себе не устраняет
   штурм одного явно инвалидированного ключа. Это объяснение поведения кода,
   отдельный замер 100 GET на один ключ не выполнялся.

---

## 2. Outbox: отказ Kafka и at-least-once

Kafka остановлена, затем созданы 300 заказов:

```bash
docker compose -f compose/docker-compose.yml stop kafka
make load ARGS="-mode orders -n 300 -c 20 -stock 1000"
```

Вывод `load` при остановленной Kafka:

```
listing 0646b0bc-4b1f-4b83-b937-b26a72b114c9 stock=1000, firing 300 POST /orders with concurrency 20

300 requests in 632ms  (475 req/s)
p50 34.1ms   p95 79.2ms   p99 105.8ms   max 119.2ms
  201             300  (100.0%)
```

Все 300 заказов созданы без Kafka. Первый контроль с живой Kafka дал 220 ответов
201 и 80 ответов 503 из-за DeadlineExceeded при Reserve; повтор после пробного
успешного заказа дал 300 ответов 201, p50 107.6ms и p99 598.6ms. В повторе топик
уже имел три партиции вместо одной, поэтому это не чистое сравнение задержек.

Размер очереди: `psql postgres://app:app@localhost:5433/orders -c "select count(*) from outbox where published_at is null"`:

```
300
```

После запуска Kafka измерено время опустошения outbox и схождения sold:

```bash
docker compose -f compose/docker-compose.yml start kafka
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d orders -At -c "select count(*) from outbox where published_at is null"
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d catalog -At -c "select stock,sold from listings where id='0646b0bc-4b1f-4b83-b937-b26a72b114c9'"
```

```
listing=0646b0bc-4b1f-4b83-b937-b26a72b114c9
До восстановления Kafka: stock=700 sold=0

seconds  pending
0.656    300
3.266    300
5.703    300
6.812      0

После опустошения outbox ещё через 34.704s: stock=700 sold=300
```

Outbox опустел через 6.812 секунды после команды запуска Kafka. Consumer догнал
очередь позднее: нулевой outbox ещё не означает, что sold обновлён.

Для проверки дублей запущена новая серия из 300 заказов при живой Kafka,
orders-svc завершён SIGKILL и запущен снова:

```bash
make load ARGS="-mode orders -n 300 -c 20 -stock 1000" &
sleep 1; docker compose -f compose/docker-compose.yml kill -s SIGKILL orders-svc
docker compose -f compose/docker-compose.yml start orders-svc
```

При фактическом запуске секунда отсчитывалась от начала POST-нагрузки,
после подготовки аккаунтов. Перед SIGKILL в очереди было 70 записей.
Сообщения топика прочитаны с начала, подсчитаны копии каждого order_id:

```bash
docker run --rm --network compose_default apache/kafka:3.9.2 /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka:9092 --topic order.created --from-beginning --timeout-ms 10000 2>/dev/null \
  | grep -o '"order_id":"[^"]*"' | sort | uniq -c | sort -rn | head
```

```
Все серии в топике:
messages=1118 unique_order_ids=820 duplicate_copies=298

Примеры order_id с повторами:
4 c785c249-3348-4d1d-abe1-39c9044994d9
4 cb2a3587-7bec-4c88-a68e-80dbad863812
4 35ca8251-cd5c-425e-9616-9f6fca60c4aa

Серия остановки/восстановления Kafka:
listing=0646b0bc-4b1f-4b83-b937-b26a72b114c9
messages=598 unique_order_ids=300 max_copies=4
stock=700 sold=300

Серия SIGKILL orders-svc:
listing=f6184b2f-63fb-4cfa-8314-c7b243589864
pending перед SIGKILL=70
300 requests in 2.05s  (146 req/s)
p50 102.7ms   p95 261.1ms   p99 374.8ms   max 552.5ms
  201             300  (100.0%)
messages=300 unique_order_ids=300
stock=700 sold=300
```

Дублей именно серии SIGKILL не обнаружено. Найденные 298 повторных копий относятся
к восстановлению Kafka; при этом sold увеличился только на 300.

**Ответы.**

4. **Заказ и outbox фиксируются одной транзакцией Postgres.** Публикация выполняется
   отдельным воркером, поэтому HTTP не ждёт Kafka. Пока событие не доставлено
   consumer, sold не увеличивается. Синхронный publish в хендлере добавил бы
   ожидание брокера и ошибки/таймауты в POST. Без outbox также остаётся окно между
   двумя независимыми записями: можно потерять событие или отправить событие
   о заказе, который не зафиксирован в БД.
5. **После SIGKILL дублей этой серии не найдено.** Характерное окно дубля — Kafka
   уже приняла событие, а orders-svc ещё не зафиксировал published_at. Повтор
   возможен и при неоднозначном результате publish после таймаута, как при
   восстановлении Kafka в этом замере. Consumer регистрирует event_id в
   processed_events и увеличивает sold одной транзакцией. Повторная копия
   не увеличивает sold снова: для 300 уникальных заказов stock=700, sold=300.
6. **SKIP LOCKED позволяет воркерам брать разные свободные строки.** Если убрать
   только SKIP LOCKED, сохранив FOR UPDATE, второй воркер будет ждать блокировки,
   и параллелизм снизится. Если убрать блокировки целиком, воркеры смогут
   одновременно публиковать одни и те же строки. Два экземпляра orders-svc
   в этом эксперименте не запускались; ответ основан на SQL воркера.

---

## 3. Идемпотентность и saga

Выполнены 50 параллельных `POST /orders` от одного покупателя с одним
`Idempotency-Key` на листинг со stock=1. Потоки синхронизированы барьером перед
отправкой. Эквивалент команды для повторения: TOKEN — JWT покупателя,
LISTING_ID — UUID нового листинга со stock=1, оба переданы в окружение Python.

```bash
TOKEN="$TOKEN" LISTING_ID="$LISTING_ID" python - <<'PY'
import collections, concurrent.futures, json, os, threading, urllib.error, urllib.request, uuid
barrier, key = threading.Barrier(50), str(uuid.uuid4())
body = json.dumps({"listing_id": os.environ["LISTING_ID"], "qty": 1}).encode()
headers = {"Content-Type": "application/json", "Authorization": "Bearer " + os.environ["TOKEN"], "Idempotency-Key": key}
def send(_):
    barrier.wait()
    req = urllib.request.Request("http://localhost:8080/orders", data=body, headers=headers)
    try:
        response = urllib.request.urlopen(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = json.load(response)
        return response.status, response.headers.get("Idempotent-Replayed"), data.get("id")
with concurrent.futures.ThreadPoolExecutor(max_workers=50) as pool:
    replies = list(pool.map(send, range(50)))
print(collections.Counter((status, replay) for status, replay, _ in replies))
print("unique_order_ids=", len({identifier for _, _, identifier in replies if identifier}))
PY
```

```
Первый прогон:
201=0  200 с Idempotent-Replayed=true=0  409=0  503=50
orders=0 stock=1 sold=0

Повторный прогон:
listing=4b570d04-09df-4445-8ad7-6c1aa403399c
Idempotency-Key=2763b781-513c-4862-9fe8-8303e6ec9bc9
201=1  200 с Idempotent-Replayed=true=49  409=0  503=0
unique_response_order_ids=1
orders=1 stock=0 sold=1
```

Первый прогон завершился таймаутами и отменами RPC. Повтор подтвердил
идемпотентность: один заказ и одно списание остатка на 50 запросов.

Количество заказов и состояние листинга проверены в БД:

```bash
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d orders -At -c "select count(*) from orders where listing_id='4b570d04-09df-4445-8ad7-6c1aa403399c'"
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d catalog -At -c "select stock,sold from listings where id='4b570d04-09df-4445-8ad7-6c1aa403399c'"
```

```text
1
0|1
```

**Ответы.**

7. **Остаток уже уменьшен, автоматической компенсации в текущем коде нет.**
   Нужны идентификатор резерва и идемпотентный RPC наподобие
   ReleaseReservation(reservation_id), долговечная задача компенсации и повторные
   попытки. Ошибка компенсации не должна удалять задачу; нужна последующая сверка.
   Идентификатор резерва нужен и для безопасного повтора Reserve после
   неоднозначного сетевого ответа. В сценарии аварий и отмен запросов из раздела 4
   зафиксированы 3282 заказа, а stock уменьшился на 3287: пять резервов остались
   без заказа даже после полной обработки событий.
8. **Резервирование ключа до Reserve выбирает один запрос, которому разрешено
   списывать stock.** Остальные ждут результат и возвращают тот же заказ. Если
   записывать ключ только после COMMIT, параллельные запросы успеют выполнить
   несколько Reserve до обнаружения повтора.

Подтверждение расхождения после завершения обработки событий:

```bash
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d orders -At -c "select count(*),sum(qty) from orders where listing_id='5128efd5-d888-4cea-b93d-56a7c6ef1c42'"
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d catalog -At -c "select stock,sold,stock+sold from listings where id='5128efd5-d888-4cea-b93d-56a7c6ef1c42'"
```

```text
3282|3282
996713|3282|999995
```

Исходный stock был 1 000 000. Из этих агрегатов нельзя установить, на каком
именно аварийном запросе возник каждый из пяти незавершённых резервов.

---

## 4. Один заказ в логах трёх сервисов

В проверенной реализации RPC всех трёх сервисов пишутся в JSON через slog.
Это отличается от условия задания, где orders-svc должен писать текстом через
log.Printf. Ниже приведено фактическое поведение; сравнение с текстовыми логами
объяснено отдельно, без выдуманного результата эксперимента.

Для повторения TOKEN — JWT покупателя, LISTING_ID — листинг с доступным остатком.
Создание заказа и поиск его request_id во всех трёх сервисах:

```bash
RID=$(uuidgen)
curl -s -X POST http://localhost:8080/orders -H "Authorization: Bearer $TOKEN" \
     -H "Idempotency-Key: $(uuidgen)" -H "X-Request-ID: $RID" \
     -H 'Content-Type: application/json' -d "{\"listing_id\":\"$LISTING_ID\",\"qty\":1}"
docker compose -f compose/docker-compose.yml --profile services logs \
  --no-color --no-log-prefix --since 1m gateway orders-svc catalog-svc | grep -F "$RID"
```

В выполненном прогоне HTTP вернул 201, order_id —
`f0bb2055-eeb5-4b06-aced-04db80dc3609`, request_id —
`a1ec6f4a-e626-408e-8639-8696ea4c6bbf`. Найдены три строки, по порядку сервисов
gateway, orders-svc, catalog-svc:

```json
{"time":"2026-10-26T14:00:49.227689552Z","level":"INFO","msg":"HTTP request","request_id":"a1ec6f4a-e626-408e-8639-8696ea4c6bbf","method":"POST","path":"/orders","status":201,"dur_ms":9.316275,"remote_addr":"172.19.0.1:38980"}
{"time":"2026-10-26T14:00:49.227356291Z","level":"INFO","msg":"gRPC request completed","request_id":"a1ec6f4a-e626-408e-8639-8696ea4c6bbf","method":"/marketplace.orders.v1.OrdersService/Create","code":"OK","dur_ms":8}
{"time":"2026-10-26T14:00:49.222837824Z","level":"INFO","msg":"gRPC request completed","request_id":"a1ec6f4a-e626-408e-8639-8696ea4c6bbf","method":"/marketplace.catalog.v1.CatalogService/Reserve","code":"OK","dur_ms":2}
```

Одинаковый request_id связывает HTTP, создание заказа и резервирование остатка.

### Нагрузка 30 секунд

```powershell
& "$env:TEMP\marketplace-lab-load.exe" -base http://localhost:8080 -mode orders -n 1000000 -c 20 -stock 1000000
```

Большое n использовано, чтобы нагрузка не завершилась раньше замера. Генератор
остановлен через 30.078 секунды после начала POST-нагрузки. Внутри окна:

```bash
docker compose -f compose/docker-compose.yml stop kafka
docker compose -f compose/docker-compose.yml kill -s SIGKILL catalog-svc
docker compose -f compose/docker-compose.yml start catalog-svc
```

SIGKILL catalog выполнен примерно на восьмой секунде, команда запуска — примерно
на четырнадцатой. Compose также запустил зависимую Kafka, поэтому брокер не был
остановлен все 30 секунд. Большинство ошибок получено при намеренном отказе
catalog и восстановлении соединения. Это не контрольный прогон исправного стека.

Окно сбора логов: `2026-10-26T14:00:50.793239Z` —
`2026-10-26T14:01:23.010085Z`. Оно включает подготовку аккаунтов и завершение
запросов при остановке генератора. Сохранение тех же логов для анализа:

```bash
SINCE=2026-10-26T14:00:50.793239Z
UNTIL=2026-10-26T14:01:23.010085Z
docker compose -f compose/docker-compose.yml logs --no-color --no-log-prefix --since "$SINCE" --until "$UNTIL" gateway > gateway.log 2>&1
docker compose -f compose/docker-compose.yml logs --no-color --no-log-prefix --since "$SINCE" --until "$UNTIL" orders-svc > orders.log 2>&1
docker compose -f compose/docker-compose.yml logs --no-color --no-log-prefix --since "$SINCE" --until "$UNTIL" catalog-svc > catalog.log 2>&1
```

Фактический разбор выполнен Python, поскольку jq в окружении не установлен.
Эквивалентный разбор сохранённых логов без внешних библиотек:

```bash
python - <<'PY'
import collections, json
def read(name):
    with open(name, encoding="utf-8") as source:
        return [json.loads(line) for line in source if line.startswith("{")]
gateway, orders, catalog = read("gateway.log"), read("orders.log"), read("catalog.log")
posts = [row for row in gateway if row.get("method") == "POST" and row.get("path") == "/orders"]
creates = [row for row in orders if row.get("method") == "/marketplace.orders.v1.OrdersService/Create"]
unavailable = {row["request_id"] for row in creates if row.get("code") == "Unavailable"}
failures = [row for row in orders if row.get("msg") == "outbox publication failed"]
print("gateway statuses:", dict(collections.Counter(str(row["status"]) for row in posts)))
print("orders codes:", dict(collections.Counter(row["code"] for row in creates)))
print("gateway correlated Unavailable:", sum(row["request_id"] in unavailable for row in posts))
print("outbox publication failed:", len(failures))
print("batch size median:", "unknown" if any("batch_size" not in row for row in failures) else "requires calculation")
PY
```

```text
gateway statuses: {'201': 3282, '503': 21404, '500': 15}
orders codes: {'OK': 3282, 'Unavailable': 21404, 'Canceled': 15}
gateway correlated Unavailable: 21404
outbox publication failed: 9
batch size median: unknown
```

Пятнадцать Canceled/HTTP 500 зарегистрированы при остановке генератора.
Автоматический разбор трёх файлов в выполненном прогоне занял 2.969 секунды;
это время вычисления статистики, не время ручного анализа.

### Примеры логов под нагрузкой

Gateway:

```json
{"time":"2026-10-26T14:00:52.157997251Z","level":"INFO","msg":"HTTP request","request_id":"a88c1c99-1e88-42cc-a15c-9e3f6c8741f2","method":"POST","path":"/orders","status":201,"dur_ms":32.355818,"remote_addr":"172.19.0.1:38988"}
{"time":"2026-10-26T14:00:52.158142162Z","level":"INFO","msg":"HTTP request","request_id":"58892daa-71ef-4e0f-89c0-a6784b27d52a","method":"POST","path":"/orders","status":201,"dur_ms":23.416761,"remote_addr":"172.19.0.1:39004"}
{"time":"2026-10-26T14:00:52.167185577Z","level":"INFO","msg":"HTTP request","request_id":"30cfd918-8b5b-4133-bbe2-fa7f38f1de76","method":"POST","path":"/orders","status":201,"dur_ms":20.177323,"remote_addr":"172.19.0.1:39170"}
```

Orders-svc — успешный RPC, Unavailable и отменённый запрос:

```json
{"time":"2026-10-26T14:00:52.157234315Z","level":"INFO","msg":"gRPC request completed","request_id":"58892daa-71ef-4e0f-89c0-a6784b27d52a","method":"/marketplace.orders.v1.OrdersService/Create","code":"OK","dur_ms":21}
{"time":"2026-10-26T14:01:00.656810192Z","level":"INFO","msg":"gRPC request completed","request_id":"ac168f7d-d4a5-4ee4-9fbf-c3f4ff1fc9b4","method":"/marketplace.orders.v1.OrdersService/Create","code":"Unavailable","dur_ms":13,"error":"rpc error: code = Unavailable desc = reserve listing: rpc error: code = Unavailable desc = error reading from server: connection reset by peer"}
{"time":"2026-10-26T14:01:22.139410311Z","level":"INFO","msg":"gRPC request completed","request_id":"50264e19-4ba5-4d22-b050-89b869558703","method":"/marketplace.orders.v1.OrdersService/Create","code":"Canceled","dur_ms":2,"error":"rpc error: code = Canceled desc = reserve listing: rpc error: code = Canceled desc = context canceled"}
```

Catalog-svc:

```json
{"time":"2026-10-26T14:00:52.133969185Z","level":"INFO","msg":"gRPC request completed","request_id":"a88c1c99-1e88-42cc-a15c-9e3f6c8741f2","method":"/marketplace.catalog.v1.CatalogService/Reserve","code":"OK","dur_ms":6}
{"time":"2026-10-26T14:00:52.141235265Z","level":"INFO","msg":"gRPC request completed","request_id":"58892daa-71ef-4e0f-89c0-a6784b27d52a","method":"/marketplace.catalog.v1.CatalogService/Reserve","code":"OK","dur_ms":5}
{"time":"2026-10-26T14:00:52.153043421Z","level":"INFO","msg":"gRPC request completed","request_id":"36aeb332-0fac-4e6d-b3d6-25b186a7d69e","method":"/marketplace.catalog.v1.CatalogService/Reserve","code":"OK","dur_ms":11}
```

Ошибки воркера outbox:

```json
{"time":"2026-10-26T14:00:54.242570278Z","level":"WARN","msg":"outbox publication failed","error":"publish outbox events: context deadline exceeded"}
{"time":"2026-10-26T14:00:56.245486239Z","level":"WARN","msg":"outbox publication failed","error":"publish outbox events: context deadline exceeded"}
{"time":"2026-10-26T14:00:58.247118436Z","level":"WARN","msg":"outbox publication failed","error":"publish outbox events: context deadline exceeded"}
```

### Ответы

1. **Unavailable получили 21404 запроса создания заказа.** В gateway можно
   посчитать HTTP 503 одной строкой jq:

   ```bash
   jq -s '[.[] | select(.method=="POST" and .path=="/orders" and .status==503)] | length' gateway.log
   ```

   ```text
   21404
   ```

   Это эквивалент выполненного Python-разбора; jq-команда не запускалась.
   В текущем логе gateway нет поля code, а DeadlineExceeded также преобразуется
   в 503. Поэтому только этот файл не позволяет точно отличить Unavailable.
   В данном прогоне результат дополнительно подтверждён объединением с
   orders-svc по request_id, как показано выше.
2. **Orders-svc уже пишет структурированный RPC-лог.** Эквивалент jq:

   ```bash
   jq -s '[.[] | select(.method=="/marketplace.orders.v1.OrdersService/Create" and .code=="Unavailable")] | length' orders.log
   ```

   ```text
   21404
   ```

   Grep/awk для позиционного разбора не понадобились. На текстовом логе такая
   команда зависит от шаблона сообщения и расположения полей; изменение текста
   или вставка поля могут её сломать. Практическое сравнение JSON и legacy-текста
   в текущей реализации не воспроизводится — это расхождение с заданием.
3. **Найдено девять записей об ошибке публикации** между 14:00:54.242Z и
   14:01:10.260Z в окне отказа и восстановления Kafka. Это число ошибок вызова
   воркера, а не число сетевых попыток внутреннего Kafka writer. Успешные вызовы,
   размер батча и точная граница остановленного состояния Kafka в этих строках
   не записаны. Поэтому точное число отправок только при полностью остановленной
   Kafka и медиану размера батча вычислить нельзя. Лимит 100 в коде не является
   измеренным размером каждого батча. На разбор всех трёх логов ушло 2.969 секунды.
4. **Три приведённые строки orders-svc уже являются JSON.** Перевод из Printf
   не потребовался. Для единой схемы обязательны time, level, service, стабильный
   тип события, request_id, method, code/status и dur_ms; при ошибке — тип и текст
   ошибки. Request_id связывает синхронный запрос между сервисами. Для outbox
   нужны batch_size, длительность, результат, event_id/order_id. Чтобы продолжить
   исходную трассу в фоне, request_id нужно переносить в событии: сейчас его
   в payload нет. Пустое поле request_id само по себе корреляцию не создаст.
5. **Railway собирает stdout/stderr в журналы платформы.** Через сутки логи
   доступны в пределах срока хранения тарифа: Free — 3 дня, Trial/Hobby — 7,
   Pro — 30, Enterprise — до 90. Это справка по
   [официальной документации Railway](https://docs.railway.com/observability/logs),
   проверенной 26.10.2026, а не результат облачного эксперимента. Для общего
   поиска можно собирать JSON через Vector в Loki и фильтровать по service,
   code и request_id.

Структура даёт надёжный поиск по полям и объединение сервисов без зависимости
от формулировки сообщения. Однако полезны только записанные поля: без batch_size
не получить медиану, без gRPC-кода не различить причины 503. Printf подходит для
небольших утилит и редких диагностических сообщений, которые не служат
интерфейсом для метрик и межсервисного поиска.

## 5. Партиции и порядок (для пятёрки)

Топик пересоздан с тремя партициями. Перед удалением остановлены producer и
consumer, outbox был пуст. Сохранена копия 5202 сообщений с 4104 уникальными
order_id. После пересоздания история воспроизведена с прежними ключами и payload.
Повторная обработка не увеличила sold повторно благодаря event_id.

```bash
docker run --rm --network compose_default apache/kafka:3.9.2 /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server kafka:9092 --delete --topic order.created
docker run --rm --network compose_default apache/kafka:3.9.2 /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server kafka:9092 --create --topic order.created --partitions 3
```

При первой попытке удалить consumer group получен GroupNotEmptyException.
После остановки consumer и ожидания состояния Empty выполнен сброс offsets:

```bash
docker run --rm --network compose_default apache/kafka:3.9.2 /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server kafka:9092 --describe --group catalog-svc --state
docker run --rm --network compose_default apache/kafka:3.9.2 /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server kafka:9092 --reset-offsets --group catalog-svc --topic order.created --to-earliest --execute
```

После запуска сервисов и успешного пробного заказа выполнена серия:

```bash
make load ARGS="-mode orders -n 30 -c 20 -stock 1000 -buyers 1"
docker run --rm --network compose_default apache/kafka:3.9.2 /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka:9092 --topic order.created --from-beginning --timeout-ms 10000 \
  --property print.partition=true --property print.offset=true --property print.key=true > topic.log
```

Для подсчёта выбраны только сообщения листинга из этой серии:

```bash
LISTING_ID=4c27d325-0dfb-419d-b854-9a377df0d3b9 python - <<'PY'
import collections, json, os, re
events = []
with open("topic.log", encoding="utf-8") as source:
    for line in source:
        match = re.search(r"Partition:(\d+)\s+Offset:(\d+)\s+([^\t]+)\t(\{.*\})", line)
        if match:
            event = json.loads(match[4])
            if event["listing_id"] == os.environ["LISTING_ID"]:
                events.append((int(match[1]), int(match[2]), match[3], event))
print("partitions:", dict(sorted(collections.Counter(row[0] for row in events).items())))
print("unique_order_ids:", len({row[3]["order_id"] for row in events}))
print("keys_match:", all(row[2] == row[3]["order_id"] for row in events))
for partition in range(3):
    print("offsets", partition, sorted(row[1] for row in events if row[0] == partition))
PY
```

```
До успешного прогона:
первый запуск: /healthz returned 503
следующий запуск: 30 ответов 503 при Reserve, stock=1000 sold=0

После успешного пробного заказа:
listing=4c27d325-0dfb-419d-b854-9a377df0d3b9
30 requests in 107ms  (281 req/s)
p50 55.7ms   p95 84.6ms   p99 87.7ms   max 91.8ms
  201              30  (100.0%)

partition  events  offsets
0               8  1718..1725
1               9  1792..1800
2              13  1693..1705

events=30 unique_order_ids=30
message key == order_id: true для всех 30 сообщений
stock=970 sold=30
consumer lines with partition/offset: 0
```

Учтены только 30 событий этого листинга. История топика сохранена и восстановлена
после пересоздания, поэтому offset начинаются не с нуля. Порядок записей внутри
партиций подтверждён экспортом Kafka; фактический порядок успешной обработки
из логов consumer восстановить нельзя — такие сообщения не логируются.

**Ответ.**

9. **Ключ order_id не гарантирует общий порядок событий покупателя или листинга.**
   Их разные заказы распределились по трём партициям. Для порядка по листингу
   ключом нужен listing_id при согласованном partitioner и стабильном числе
   партиций. Гарантия относится к порядку записи внутри партиции, а не к порядку
   начала параллельных HTTP-запросов. Горячий листинг сосредоточит нагрузку на
   одной партиции и ограничит параллелизм обработки. Порядок успешной обработки
   именно consumer в этом запуске не доказан: соответствующих логов нет.

## Итог проверки

На момент завершения лабораторной, 26.10.2026 в 17:12:54 МСК, все шесть сервисов
были запущены, `/healthz` возвращал `200 ok`. Топик оставлен с тремя партициями.
Итоговая сверка выполнена командами:

```bash
curl -s http://localhost:8080/healthz
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d orders -At -c "select count(*) from orders; select count(*) from outbox; select count(*) from outbox where published_at is null"
docker compose -f compose/docker-compose.yml exec -T postgres psql -U app -d catalog -At -c "select count(*) from processed_events"
docker run --rm --network compose_default apache/kafka:3.9.2 /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server kafka:9092 --describe --group catalog-svc
```

```text
healthz: 200 ok
orders=4439
outbox=4439
outbox published_at IS NULL=0
processed_events=4439

partition  current-offset  log-end-offset  lag
0                   1824            1824    0
1                   1898            1898    0
2                   1812            1812    0
```

Это итоговые количества в локальных БД, а не число запросов одной серии.
Все зафиксированные заказы получили обработанное событие, очередь и lag равны
нулю. Пять резервов без заказов остались: отсутствие компенсации не устраняется
доставкой Kafka. После простоя/перезапуска наблюдались таймауты пути
orders-svc → catalog-svc даже при зелёном `/healthz`; причина ниже уровня RPC
по этим данным не установлена. Недостающие поля логов и расхождение формата
orders-svc с заданием перечислены в разделе 4. Облачные проверки не выполнялись.
