# Dreame Local Agent

Локальный агент для робота Dreame X50 Pioneer Edition (R2505) с root-доступом. Версия **0.6.0**: к исходникам 0.5.0 из `dreame-local.zip` добавлены слой модели устройства и `/api/v1`.

Агент написан на Go, работает **на самом пылесосе**, вызывает штатный `avacmd` и предоставляет HTTP API и встроенный веб-интерфейс. Отдельный мост на Raspberry Pi для работы агента не требуется.

## Возможности

- Состояние робота, управление уборкой, зарядкой и станцией через локальный HTTP API.
- Веб-интерфейс с выбором комнат и сохранённой картой, редактирование комнат и настроек.
- Настройка Wi-Fi и captive portal в режиме точки доступа.
- Отдельный опциональный перехватчик региональной блокировки (`agent/guard/region_guard.c`).
- Проверка ожидаемых model/DID/MAC и Bearer-токена; без облачного API в рабочем режиме.

Сохранённая карта доступна локально; **живое положение робота и маршрут пока не реализованы**. Часть команд станции, Wi-Fi и редактирования карты проверялась на тестовом стенде, но не на всех прошивках и состояниях устройства.

## API для адаптеров (`/api/v1`)

Все запросы с заголовком `Authorization: Bearer <token>`, ответ `{"result": ...}` или `{"error": ...}`.

- `GET /api/v1/state` — кэш состояния. Агент сам опрашивает робот раз в 5 с и сразу после команды, клиенты робот не нагружают. Поля: `activity` (`cleaning|paused|returning|docked|idle|error`), `status`/`status_name`, `battery`, `charging`, `suction`, `water`, `station`, `volume`, `error`, `region`, `region_code`. Свойства, на которые робот не ответил, отсутствуют. `stale: true` — последний опрос не удался, `seq` растёт при каждом изменении.
- `GET /api/v1/capabilities` — модель и прошивка, совпадение прошивки с проверенным профилем (`firmware.verified`), аппаратная таблица модели из самой прошивки (`/ava/conf/dconf_runtime.json`), читаемые свойства и доступность команд.
- `POST /api/v1/command` — `{"name": "start|pause|stop|dock|locate|mapping|empty|wash_mop|dry_start|dry_stop"}`, `{"name": "set_suction|set_volume", "value": N}`, `{"name": "clean_rooms", "rooms": [ids], "repeats": 1..3, "suction": 0..3, "water": 1..3}`. Те же правила, что у `/rpc`: нужен `allow_control`, поддерживается `X-Dreame-Request-ID`, ненулевой код робота возвращается ошибкой. Команда блокируется, только если аппаратная таблица явно говорит, что узла нет.

`/rpc` остаётся без изменений для веб-интерфейса и отладки.

## Сборка

Нужен Go 1.24 или новее. Для целевого устройства требуется Linux ARM64:

```sh
cd agent
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o dreame-local-agent-linux-arm64 .
```

Библиотека регионального перехватчика распространяется **в исходном виде**. Для установщика собери `agent/region-guard.so` с подходящим компилятором aarch64-linux-gnu или на самом ARM64 Linux (с системными заголовками и библиотеками, совместимыми с устройством):

```sh
cd agent
# При наличии подходящего toolchain:
aarch64-linux-gnu-gcc -shared -fPIC -O2 -o region-guard.so guard/region_guard.c -ldl
```

Перед использованием библиотеки проверь совместимость с libc и окружением конкретной прошивки. Готовые исполняемые файлы и `.so` в этот репозиторий не включены.

## Установка

Создай `bridge/config.json` на основе `bridge/config.example.json`; задай случайный `token` длиной не менее 32 символов и значения `expected_model`, `expected_did`, `expected_mac`, соответствующие твоему устройству. **Не публикуй этот файл**.

После сборки агента и библиотеки (см. выше), имея ADB с root:

```sh
python3 tools/install_agent.py --serial ROBOT_IP:4567 --config bridge/config.json
```

Установщик проверяет root, ARM64 и идентичность устройства, затем развёртывает агент в `/data/dreame-local`, проверяет SHA-256 и HTTP-ответ. При обычном режиме добавляет команды запуска в существующий `/data/test/ava.sh`, сохраняя резервную копию. Для ручного запуска без изменения boot hook используй `--no-boot-hook`.

Веб-интерфейс после запуска: `http://ROBOT_IP:8765/`.
Не открывай порт 8765 в интернет: интерфейс предназначен для доверенной LAN.

## Тесты

Контракт ошибок операций карты и разрешения для map request описаны в [docs/map-results.md](docs/map-results.md). Для карты одного внешнего code0 недостаточно: проверяется отдельный operation result, а неизвестный исход не повторяется автоматически.

```sh
cd agent && go test ./... && go vet ./...
cd .. && python3 -m unittest discover -s tests -p 'test_region_guard.py' -v
# Дополнительные UI-тесты используют Playwright:
node tests/test_command_results.cjs
node tests/test_web_ui.cjs
```

Go-тесты рассчитаны на Linux (`Setsid`, mock `avacmd` на Python). На Windows собери тестовый бинарник и запусти его в WSL:

```sh
cd agent && GOOS=linux GOARCH=amd64 go test -c -o agent.test . && wsl ./agent.test
```

## Состав

- `agent/` — Go-сервис, встроенный веб-интерфейс, shell-скрипты и C-перехватчик.
- `tools/install_agent.py` — установка через ADB, с проверками идентичности.
- `tools/configure.py` — подготовка конфигурации из локального диагностического отчёта.
- `tests/` — тесты перехватчика и веб-интерфейса.
- `bridge/config.example.json` — **только шаблон**, без личных данных и токенов.
- `custom_components/dreame_local/` — минимальная интеграция Home Assistant поверх `/api/v1`: штатная сущность `vacuum` (старт, пауза, стоп, на базу, найти, мощность) и сенсор батареи. Скопируй папку в `config/custom_components/`, перезапусти HA и добавь «Dreame Local» с IP, портом и токеном агента. Агент должен слушать LAN (`bind`), а для команд нужен `allow_control`.

Это репозиторий **исходников агента**, а не полного пакета `dreame-local`: внешний Python-мост и диагностические данные сюда не включены. Сведения об атрибуции находятся в `NOTICE.md` и файлах лицензий.
