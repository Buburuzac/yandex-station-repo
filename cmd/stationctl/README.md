# stationctl — веб-пульт **внутри** станции

Лёгкий HTTP-сервер, который крутится **на самой Яндекс.Станции** (один статический
бинарь aarch64, только stdlib Go, без зависимостей и без интернета). Открываешь в
браузере `http://<ip-станции>:8080` — и управляешь колонкой из локальной сети.

Рассчитан на уже **отрутованную** станцию (процесс идёт от root — см.
`quasar-root-kit`). Ставится как обычный пакет через `apkg`.

## Что умеет

- **Статус**: `device_id`, платформа, аптайм, температура (`/sys/class/thermal`),
  память (`/proc/meminfo`), `df /data`, текущая громкость, маркеры рута в `/data/quasar`.
- **Громкость** — чтение/установка через `amixer` (ALSA): слайдер + ±10, автоподбор контрола.
- **Сигнал «найти станцию»** — короткий тон через `speaker-test`.
- **LED-цвет** — чтение через `yandex-getcolor`.
- **Логи демонов** — хвост файлов из `/data/quasar/daemons_logs` (glagold/maind/mediad…).
- **ADB** — включение/выключение (в т.ч. **сетевого** adbd `:5555`) штатным
  `activate_adb.sh`: ставит/снимает маркеры в `/data` (переживают ребут) и
  поднимает/гасит `yandex-debugctl adb-net`. Статус показывает, слушает ли `:5555`.
- **Медиа / Алиса** *(experimental)* — play/pause/next/prev, громкость и «сказать
  текст»/команда Алисе через локальный **glagol** `:1961`. Нужен токен (ниже).
- **Bluetooth** — вкл/выкл, поиск, пара/подключение (`bluetoothctl`): можно
  подключить станцию к колонке или наоборот.
- **Файлы** — просмотр и скачивание из UI под белым списком (`/data`, логи,
  `vendor/quasar`), read-only.
- **Spotify Connect** *(experimental)* — управление `librespot`: станция
  появляется как колонка Spotify Connect в приложении Spotify (одна сеть).
  Старт/стоп/рестарт, имя, выбор ALSA-устройства, лог. Сам `librespot` — отдельно.
- **Автозапуск** — кнопка «поставить себя в `@reboot` crontab» / убрать.
- **LED** *(experimental)* — чтение цвета (`yandex-getcolor`); установка только
  через заданную `--led-cmd` (интерфейс LED на прошивке проприетарный).
- **Root-шелл** — `/api/exec` (по флагу `--shell`), с веб-консолью.
- **Питание** — `reboot` / `poweroff` (с подтверждением).

Всё — локальными бинарями самой станции (`amixer`, `speaker-test`, `reboot`,
`device_id`, `yandex-getcolor`), которые есть в прошивке quasar.

## Сборка

Нужен Go 1.22+ на машине сборки (не на станции):

```sh
./scripts/build-stationctl.sh          # -> packages/stationctl/stationctl (aarch64, static)
```

Вручную:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
  -o packages/stationctl/stationctl ./cmd/stationctl
```

## Установка на станцию

Через `apkg` (после публикации `pack-packages.sh` + `push-pkg-branch.sh`):

```sh
apkg update && apkg install stationctl
```

Или вручную (push бинаря по ADB):

```sh
adb -s <ip>:5555 push packages/stationctl/stationctl /data/local/bin/stationctl
adb -s <ip>:5555 shell chmod 755 /data/local/bin/stationctl
```

## Запуск

```sh
stationctl                      # :8080, токен генерируется и пишется в $APKG_STATE/stationctl.token
stationctl --shell              # + включить root-шелл /api/exec
stationctl --addr 0.0.0.0:8777  # другой порт
stationctl --mixer Digital      # явно указать ALSA-контрол громкости
STATIONCTL_NOAUTH=1 stationctl  # БЕЗ токена (не для публичной сети!)
```

### Медиа / Алиса (glagol)

Для карточки «Медиа / Алиса» нужен токен локального протокола glagol:

```sh
stationctl --glagol-token <conversationToken>     # прямой токен
stationctl --yandex-token <OAuth-токен аккаунта>  # авто-получение токена с бэкенда
```

Прямой токен можно получить запросом (device_id и platform берутся с самой станции):
`GET https://quasar.yandex.net/glagol/token?device_id=…&platform=…` с заголовком
`Authorization: OAuth <токен аккаунта>`. **Experimental:** WebSocket-клиент glagol
написан на stdlib и не прогнан на железе — при сбоях проверь ответ `/api/media`.

### Spotify Connect (librespot)

stationctl **не содержит** librespot — он им управляет. Поставь бинарь librespot
(aarch64) на станцию и укажи путь при необходимости:

```sh
# если librespot в PATH (/data/local/bin) — найдётся сам; иначе:
stationctl --spotify-bin /data/local/bin/librespot --spotify-name "Станция"
```

Где взять `librespot`: статическая/musl aarch64-сборка (releases проекта librespot
или собрать под musl). Положить, как любой бинарь, в `/data/local/bin` и `chmod 755`.
Запуск/остановку делает UI-карточка «Spotify Connect» (или `POST /api/spotify`).
После старта станция видна как колонка в приложении Spotify в той же сети.

Нюансы (experimental, проверять на железе):
- Аудио-тракт принадлежит стеку Алисы (`mediad`). Если librespot не слышно —
  выбери другое ALSA-устройство в выпадашке (список из `aplay -L`) или поставь
  воспроизведение Алисы на паузу.
- Память: librespot добавляет ~15–30 МБ RSS поверх stationctl.
- Тонкая настройка — через `--spotify-args` (сырые аргументы librespot целиком).

### LED (experimental)

Интерфейс LED на прошивке проприетарный (`led_util` принимает калибровочные
матрицы, не «цвет»). Рабочую команду задаёшь сам, `{arg}` подставляется:

```sh
stationctl --led-cmd 'led_util /data/local/my_pattern.json'
```

Токен печатается в stdout при первом старте и лежит в `stationctl.token`. В веб-UI
нажми 🔑 и вставь его (хранится в localStorage браузера).

Автозапуск после ребута — положи в init станции (`/data`-сторож из `quasar-root-kit`
или crontab `@reboot`), например:

```sh
# crontab -e  (busybox crond)
@reboot /data/local/bin/stationctl --shell >/data/local/var/stationctl.log 2>&1
```

## API (JSON, заголовок `Authorization: Bearer <токен>`)

| Метод | Путь | Тело / параметры |
|-------|------|------------------|
| GET  | `/api/status` | — |
| GET  | `/api/volume` | — |
| POST | `/api/volume` | `{"percent":50}` или `{"delta":-10}` |
| GET  | `/api/mixer`  | список ALSA-контролов |
| POST | `/api/beep`   | тон «найти станцию» |
| GET  | `/api/led`    | цвет кольца |
| GET  | `/api/adb`    | статус ADB (маркеры, network, слушает ли :5555) |
| POST | `/api/adb`    | `{"action":"enable","network":true}` / `{"action":"disable"}` (channel: quasmodrom\|sound\|glagol\|all) |
| POST | `/api/media`  | `{"command":"play\|pause\|next\|prev\|volume\|say\|ask","volume":50,"text":"…"}` (glagol) |
| GET  | `/api/bt`     | статус Bluetooth (powered, paired, connected) |
| POST | `/api/bt`     | `{"action":"power_on\|power_off\|scan\|connect\|disconnect\|pair\|remove","mac":"…"}` |
| GET  | `/api/files?path=…` | листинг каталога (белый список) |
| GET  | `/api/download?path=…` | скачать файл |
| GET  | `/api/autostart` | статус автозапуска |
| POST | `/api/autostart` | `{"action":"install\|uninstall"}` |
| GET  | `/api/spotify`  | статус librespot (present, running, pid, alsa_devices, log) |
| POST | `/api/spotify`  | `{"action":"start\|stop\|restart","name":"…","device":"…","bitrate":160,"volume":50}` |
| POST | `/api/led/set` | `{"arg":"…"}` — запускает `--led-cmd` (experimental) |
| GET  | `/api/logs`   | список логов |
| GET  | `/api/logs?file=glagold.log&lines=200` | хвост файла |
| POST | `/api/exec`   | `{"cmd":"ps"}` (нужен `--shell`) |
| POST | `/api/reboot` | — |
| POST | `/api/poweroff` | — |

## Безопасность

- Сервер идёт от **root** и (с `--shell`) отдаёт root-команды по сети —
  **только домашняя сеть/VLAN**, не публичный Wi-Fi.
- Токен обязателен (кроме `STATIONCTL_NOAUTH=1`). Logs-эндпоинт читает трафик
  syncd/glagold — там бывает OAuth-токен станции; не публикуй.
- Мутирующие запросы (`/api/exec|reboot|poweroff|beep`, POST `/api/volume`)
  защищены от **CSRF** (проверка `Origin`) и **DNS-rebinding** (Host обязан быть
  IP/localhost), только POST. Это работает даже при `STATIONCTL_NOAUTH=1`, но
  `NOAUTH` всё равно не для недоверенной сети.
- Вывод команд обрезается до 1 МБ; `/api/exec` таймаут ≤ 120 c, процесс-группа
  убивается целиком по таймауту.
- `amixer` правит ALSA-микшер; на части прошивок громкость Алисы отдельная —
  если слайдер не влияет на звук, укажи другой контрол (`--mixer`, см. `/api/mixer`).

AS IS, без гарантий. Только для своих устройств.
