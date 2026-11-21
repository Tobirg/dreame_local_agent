# Результаты операций карты и разрешения

Для `action` с siid6/aiid2 и для `local.thresholds` агент проверяет два уровня ответа прошивки:

- Внешний `code` должен быть целым0.
- В `out` должна быть ровно одна запись piid6 с целочисленным `value=0`.

Ненулевой внешний code или ненулевой op_ret возвращает HTTP502 и `outcome: "rejected"`. Отсутствующий, неоднозначный или некорректный operation result возвращает HTTP502 и `outcome: "indeterminate"`. Исходный firmware result сохраняется в поле `result`; обычные ошибки сохраняют прежний формат с полем `error`.

Пример ответа агента на отказ операции — синтетические данные:

```json
{
  "error": "Map operation rejected (op_ret=-7)",
  "outcome": "rejected",
  "result": {
    "siid": 6,
    "aiid": 2,
    "code": 0,
    "out": [{"piid": 6, "value": -7}]
  }
}
```

Неизвестные поля ответа сохраняются; значения других out/piid не интерпретируются как op_ret. Успешный ответ остаётся в прежнем envelope `{"result": ...}`. Веб-интерфейс дополнительно проверяет operation result, в том числе при работе с mock/старым backend, который ещё возвращает HTTP200 при отказе карты.

Даже code0/op_ret0 означает лишь принятие на известной стадии. Некоторые firmware paths оставляют default0; физическое изменение карты нужно подтверждать отдельно. При indeterminate или timeout команда могла выполниться. Агент не повторяет её автоматически.

## Map request6:1

`action(6,1)` больше не является исключением из контроля действий: нужны `allow_control` и pinned model/DID/MAC. Имя запроса карты не означает отсутствие эффектов: исследованный firmware handler может публиковать запрос дальнейшей обработки карты. Файловые чтения `local.maps`/`local.map_view` и `get_properties` сохраняют прежнее поведение.

Запрос6:1 с `X-Dreame-Request-ID` теперь проходит через ту же ограниченную in-memory дедупликацию, что другие actions. Совпадающий ID/запрос возвращает сохранённый ответ без нового avacmd; конфликт payload даёт409. Срок жизни процесса и лимит memo по-прежнему ограничивают эту гарантию; durable exactly-once не обещается. На map edit ошибки с operation result сохраняются в memo вместе с исходным firmware result в пределах существующего лимита размера.

## Проверки

Регрессии покрывают dispatch/operation rejection, missing/null/empty/duplicate/malformed out, неизвестные дополнительные поля, сохранение ответа в HTTP/memo, отсутствие повторного исполнения6:1 и блокировку при disabled/unpinned control. Fixtures используют только временные файлы и fake avacmd.

```sh
cd agent
go test ./...
go test -race ./...
go vet ./...
cd ..
node tests/test_command_results.cjs
node tests/test_web_ui.cjs
```

Если Playwright browser не установлен, существующая переменная `DREAME_TEST_CHROMIUM` задаёт путь к локальному Chromium/Chrome для headless suite. Тестовый сервер слушает loopback и отдаёт синтетические состояния/карты. Эти проверки не являются испытаниями на реальном роботе.
