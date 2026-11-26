# Пакеты озвучки

Агент 0.27+ ставит и переключает голоса робота сам, без облака: панель агента → «Голос». Облачная загрузка прошивки (робот сам скачивает пакет по ссылке) не используется.

## Как прошивка говорит

Фраза — отдельный файл `N.ogg`, где N — номер события: 45 — «я здесь» (поиск робота), 47 — «начинаю зарядку», 110 — «начинаю выгрузку пыли». Плеер прошивки берёт файл так (проверено на r2505 4.3.9_2021):

- встроенные голоса EN, JA, KO, ZH — из `/audio/<код>/N.ogg` (раздел только для чтения);
- любой другой код (RU, UK, …) — из `/data/personalized_voice/<код>/N.ogg`.

Агент хранит загруженные пакеты в `/data/dreame-local/voices/<код>/`, а включённый копирует в `/data/personalized_voice/` (прошивка держит там один голос) и переключает язык тем же сообщением, что её скрипт загрузки. Выбранный код хранится в `/data/config/ava/language_in_use` и переживает перезагрузку. Если для номера нет файла, робот на этом событии молчит.

## Что умеет панель «Голос»

- список голосов: встроенные и загруженные, текущий отмечен; «Включить», «Удалить» (только загруженные и не текущий);
- «Загрузить пакет»: код голоса и архив;
- «Фразы»: номера выбранного голоса с английским текстом, где он известен; ▶ проигрывает файл в браузере, 🔊 — на роботе текущим голосом. Так можно услышать, что означает номер без текста: выберите голос EN и нажмите ▶.

Всё это — только с основным токеном; изменения ещё требуют `allow_control` и закреплённой модели, как команды.

## Как подготовить пакет

1. Список номеров — таблица ниже или «Фразы» в панели (голос EN). Переводить имеет смысл все номера, которые есть у встроенного голоса: на r2505 их 540.
2. Озвучка: любой синтезатор или запись. Длинные паузы в начале и конце не нужны: робот часто говорит несколько фраз подряд (например, «начинаю зарядку» и «начинаю выгрузку пыли»).
3. Формат, как у встроенных голосов: Ogg Vorbis, моно, 16 кГц, около 48 кбит/с, громкость выровнена между файлами. Пример с ffmpeg:

   ```sh
   ffmpeg -i 45.wav -af loudnorm=I=-16:TP=-1.5 -ac 1 -ar 16000 -c:a libvorbis -b:a 48k 45.ogg
   ```

4. Имена — только номер: `0.ogg`, `45.ogg`, без ведущих нулей и папок.
5. Архив — `.tar.gz` с файлами в корне:

   ```sh
   tar -czf ru.tar.gz -C папка_с_ogg .
   ```

   В архив можно положить `tts.json` — ответы голосового помощника (как в пакетах Dreame); агент поставит его так же, как прошивка. Другие файлы агент пропускает и называет в ответе. Ограничения: архив до 64 МБ, до 2000 файлов по 8 МБ.
6. В панели: «Голос» → «Загрузить пакет», код (например RU), файл → «Загрузить» → «Включить». Проверить: «Фразы» → 🔊 у номера 45 или кнопка «Найти робот».

Код — 2–8 латинских заглавных букв и цифр, с буквы. Встроенные EN, JA, KO, ZH заняты: свой английский пакет загрузите, например, как EN2. Пакет с тем же кодом заменяет прежний; если он включён, нажмите «Включить» ещё раз.

Готовые пакеты Dreame (облачный список для r2505 в регионе CN: ZH, ZHM, YUE, SC) — тот же формат: архив, скачанный по ссылке `download` из `https://dreame-cn.iot.dreame.tech/dreame-product/dreame.vacuum.r2505/voices/soundpackage.json`, загружается в агент как есть. Пакеты других моделей Dreame обычно подходят по номерам, но новые события у них могут отсутствовать или быть другими.

## Номера фраз r2505

Номера — файлы встроенного английского голоса r2505 (4.3.9_2021), 540 шт. Английский текст известен для 144: он взят из `en_voice_mapping.csv` проекта [dreame-vacuum-uk-voice-packs](https://github.com/oleksandr-belei/dreame-vacuum-uk-voice-packs) (MIT, © 2023 Oleksandr), номера 27, 28, 29, 45, 47 и 110 сверены с роботом. «—» — текст неизвестен: прослушайте номер в панели. 0 — звук включения, 200 — звук выключения.

| № | Фраза (EN) |
|---|---|
| 0 | Startup sound |
| 1 | Waiting for the network configuration. |
| 2 | Unable to connect to the wifi network. Please check wifi password and try again later. |
| 3 | Unable to connect to the server. Please check your local network and try again later. |
| 4 | Robot has exited network configuration mode. |
| 5 | Network connected successfully. |
| 6 | Network connection failed. Please try again later. |
| 7 | Start cleaning. |
| 8 | Start spot cleaning. |
| 9 | Start scheduled cleaning. |
| 10 | Start resuming cleaning. |
| 11 | Paused. |
| 12 | Cleaning task completed. |
| 13 | Returning to the dock to charge. |
| 14 | Low battery. Returning to the dock to charge. Robot will resume working after charging. |
| 17 | Please move robot to the dock to charge. |
| 18 | Charging. |
| 19 | Please move robot from the dock before turning it off. |
| 20 | Low battery. |
| 21 | Low battery. Robot will turn off. |
| 25 | Low battery. Unable to carry out the scheduled cleanup. |
| 26 | Restoring to factory settings. |
| 27 | Updating. Taking about five minutes. Do not turn off nor start a cleaning task. |
| 28 | Updated successfully. |
| 29 | Update failed, please try again. |
| 30 | Positioning, please wait. |
| 31 | Positioning failed. Map is invalid. Robot will start a new cleanup. |
| 34 | Dustbin not installed. Please install it. |
| 35 | Main brush error. Please check and clean it. |
| 36 | Side brush error. Please check and clean it. |
| 37 | Right wheel error. Please check and clean it. |
| 38 | Left wheel error. Please check and clean it. |
| 39 | Please clean cliff sensor. |
| 40 | Robot stuck. Please move it to a new position to restart. |
| 41 | Robot's wheels suspended. Please move robot back on the ground. |
| 42 | Please check and clean filter. |
| 43 | Bumper error. Please gently tap to see if it rebounds. |
| 44 | Error. Please check the user manual or contact customer service. |
| 45 | I am here. |
| 46 | Robot and phone connected. Please return to App to wait for the result. |
| 47 | Start charging. |
| 48 | Low battery. Returning to the dock. |
| 50 | Charging error. Please clean charging contacts. |
| 52 | Robot tilted. Please put it on a level ground to restart. |
| 54 | Positioning succeeded. Resuming cleaning. |
| 55 | Positioning succeeded. Resuming returning to the dock. |
| 56 | Start selected room cleaning. |
| 57 | Start zoned cleaning. |
| 58 | Proceed with cleaning task. |
| 59 | Mopping completed. Please remove water tank timely. |
| 61 | Start mopping. |
| 62 | Start remote control cleaning. |
| 64 | Positioning failed. Map is invalid. Robot will return to dock. |
| 65 | Resume returning to the dock. |
| 66 | Filter worn out. Please replace it timely. |
| 67 | Side brush worn out. Please replace it timely. |
| 68 | Main brush worn out. Please replace it timely. |
| 71 | Laser sensor error. Please check and clean the laser sensor. |
| 72 | Please check whether laser distance sensor cover is jammed. |
| 73 | Edge sensor error. Please check and clean it. |
| 75 | Filter may not be dry or may be blocked. |
| 77 | Please start robot in non-carpet area. |
| 81 | Ultrasonic sensor error. Please restart robot or contact customer service. |
| 82 | Start mapping. |
| 83 | Proceed with mapping. |
| 84 | Mapping completed. |
| 85 | Positioning succeeded. Proceeding with mapping. |
| 86 | Positioning failed. Restarting mapping. |
| 87 | Low battery. Returning to the dock. |
| 91 | The clean water tank is not installed. Please install it before the robot carries on working. |
| 93 | There is not enough water in the clean water tank. Add water to the tank before the robot carries on working. |
| 97 | The dust collection bag is not installed. Please install it before the robot carries on working. |
| 101 | The upper cover of auto-empty base is not closed or the dust collection bag is not installed. Please close the upper cover or install the bag. |
| 102 | The dust collection bag is full or the air duct is blocked. Please replace the bag with a new one or clean the air duct in time. |
| 105 | Start cleaning. |
| 106 | Start cleaning the mop. |
| 107 | Start dehydrating the mop. |
| 108 | The water level of mop pad wash board is abnormal. Please clean it in time to avoid blockage. |
| 109 | An exception occurred in the dirty water tank. Please ensure that a dirty water tank is installed and timely empty the dirty water inside. |
| 110 | Start auto empty. |
| 111 | Mop pads off. Please install them before resuming working. |
| 112 | Place robot back onto the self-wash base before use. |
| 113 | Task completed. Please empty waste tank timely. |
| 114 | Mop pads installed. |
| 115 | Mop pads removed. |
| 116 | Child lock on. Buttons locked. |
| 117 | Child lock off. Buttons unlocked. |
| 118 | Buttons locked. Press and hold the dock button to unlock. |
| 126 | Start cleaning. |
| 127 | Start spot cleaning. |
| 128 | Start spot mopping. |
| 129 | Start scheduled cleaning. |
| 130 | Start scheduled mopping. |
| 133 | Start selected room cleaning. |
| 134 | Start selected room mopping. |
| 135 | Start zoned cleaning. |
| 136 | Start zoned mopping. |
| 137 | Resume cleaning. |
| 138 | Resume mopping. |
| 139 | Resume returning to the base for self-cleaning. |
| 140 | New environment is detected. Returning to the self-wash base. |
| 142 | Mop pad worn out. Please replace it timely. |
| 143 | Cleaning completed. |
| 144 | Mopping completed. |
| 145 | Positioning successful. Resume cleaning. |
| 146 | Positioning successful. Resume mopping. |
| 148 | Return to the base for self-cleaning. |
| 149 | Unable to reach the specified area. Please try to clear obstacles in the path. |
| 150 | Unable to reach the specified area. Please try to delete no-go zones in the path. |
| 151 | The path is blocked. Please try to clear obstacles around your robot. |
| 152 | The path is blocked. Please try to delete no-go zones or move your robot out of this area. |
| 153 | Your robot is detected in the no-go zone. Please move your robot out of this area. |
| 156 | Mop pad tray uninstalled. |
| 162 | Camera is on |
| 163 | Camera monitoring |
| 164 | Task ends |
| 166 | Please clean the foreign objects tangled in the mop pad |
| 169 | Mop pad drying complete |
| 171 | AI recognition function on |
| 172 | AI recognition function off |
| 173 | Please replace the Silver Ion sterilizer |
| 177 | Leaving the base station |
| 179 | Mop pad cleaned. The water in the clean water tank is insufficient. Please fill the clean water tank and empty the used water tank |
| 181 | Insufficient water. Please fill the clean water tank |
| 186 | Please check the fresh tank |
| 187 | It will about 5 to 10 minutes |
| 188 | Automatic docking engaged |
| 190 | — |
| 191 | — |
| 192 | — |
| 193 | — |
| 194 | — |
| 195 | — |
| 196 | — |
| 197 | — |
| 198 | — |
| 199 | — |
| 200 | Shutdown sound |
| 201 | — |
| 202 | — |
| 203 | — |
| 204 | — |
| 205 | — |
| 206 | — |
| 207 | — |
| 208 | — |
| 209 | — |
| 210 | — |
| 211 | — |
| 212 | — |
| 213 | — |
| 214 | — |
| 215 | — |
| 216 | — |
| 217 | — |
| 218 | — |
| 219 | — |
| 220 | — |
| 221 | — |
| 222 | — |
| 223 | — |
| 224 | — |
| 225 | — |
| 226 | — |
| 227 | — |
| 228 | — |
| 229 | — |
| 230 | — |
| 231 | — |
| 232 | — |
| 233 | — |
| 234 | — |
| 235 | — |
| 236 | — |
| 237 | — |
| 238 | — |
| 244 | Detergent bottle installed |
| 245 | — |
| 246 | — |
| 247 | — |
| 248 | — |
| 249 | — |
| 250 | — |
| 251 | — |
| 252 | — |
| 253 | — |
| 254 | — |
| 255 | — |
| 257 | Camera monitoring exited |
| 258 | Self-positioned. Cruising started |
| 259 | Self-positioned. Proceeding to the designated location |
| 260 | Self-positioned. Cruising resumed |
| 261 | Self-positioned. Resume proceeding to the designated location |
| 262 | Self-positioning failed |
| 266 | Please replace the detergent |
| 267 | Please clean the sensors |
| 269 | — |
| 274 | *Ding sound* |
| 275 | — |
| 276 | — |
| 277 | — |
| 278 | — |
| 279 | — |
| 280 | Start custom cleaning |
| 281 | The robot is auto emptying. Please try again later |
| 282 | The robot is cleaning the mop pad. Please try again later |
| 283 | The robot is not in the base station. Please try again later |
| 284 | — |
| 285 | — |
| 286 | — |
| 287 | — |
| 288 | — |
| 289 | — |
| 290 | — |
| 291 | — |
| 292 | — |
| 293 | Calling enabled |
| 294 | Calling disabled |
| 296 | — |
| 297 | — |
| 298 | — |
| 299 | — |
| 300 | — |
| 301 | — |
| 302 | — |
| 303 | — |
| 304 | — |
| 305 | — |
| 306 | — |
| 307 | — |
| 308 | — |
| 309 | — |
| 310 | — |
| 311 | — |
| 312 | — |
| 313 | — |
| 314 | — |
| 315 | — |
| 316 | — |
| 317 | — |
| 318 | — |
| 319 | — |
| 320 | — |
| 321 | — |
| 322 | — |
| 323 | — |
| 324 | — |
| 328 | — |
| 329 | — |
| 330 | — |
| 331 | — |
| 332 | — |
| 338 | — |
| 339 | — |
| 340 | — |
| 341 | — |
| 342 | — |
| 343 | — |
| 344 | — |
| 346 | — |
| 349 | — |
| 350 | — |
| 351 | — |
| 352 | — |
| 353 | — |
| 354 | — |
| 355 | — |
| 356 | — |
| 358 | — |
| 360 | — |
| 361 | — |
| 362 | — |
| 363 | — |
| 364 | — |
| 365 | — |
| 366 | — |
| 367 | — |
| 368 | — |
| 369 | — |
| 371 | — |
| 372 | — |
| 373 | — |
| 374 | — |
| 375 | — |
| 376 | — |
| 377 | — |
| 378 | — |
| 379 | — |
| 380 | — |
| 381 | — |
| 382 | — |
| 383 | — |
| 384 | — |
| 385 | — |
| 386 | — |
| 387 | — |
| 388 | — |
| 389 | — |
| 390 | — |
| 391 | — |
| 392 | — |
| 393 | — |
| 394 | — |
| 396 | — |
| 397 | — |
| 398 | — |
| 399 | — |
| 400 | — |
| 401 | — |
| 421 | — |
| 424 | — |
| 426 | — |
| 433 | — |
| 434 | — |
| 435 | — |
| 436 | — |
| 437 | — |
| 438 | — |
| 439 | — |
| 440 | — |
| 441 | — |
| 442 | — |
| 443 | — |
| 444 | — |
| 445 | — |
| 446 | — |
| 447 | — |
| 448 | — |
| 449 | — |
| 450 | — |
| 451 | — |
| 452 | — |
| 453 | — |
| 454 | — |
| 455 | — |
| 456 | — |
| 457 | — |
| 458 | — |
| 459 | — |
| 460 | — |
| 463 | — |
| 464 | — |
| 465 | — |
| 466 | — |
| 467 | — |
| 468 | — |
| 478 | — |
| 479 | — |
| 480 | — |
| 481 | — |
| 482 | — |
| 483 | — |
| 484 | — |
| 485 | — |
| 486 | — |
| 487 | — |
| 488 | — |
| 489 | — |
| 490 | — |
| 491 | — |
| 492 | — |
| 493 | — |
| 494 | — |
| 495 | — |
| 496 | — |
| 497 | — |
| 498 | — |
| 499 | — |
| 500 | — |
| 501 | — |
| 502 | — |
| 503 | — |
| 504 | — |
| 505 | — |
| 506 | — |
| 507 | — |
| 508 | — |
| 509 | — |
| 510 | — |
| 511 | — |
| 512 | — |
| 513 | — |
| 515 | — |
| 516 | — |
| 517 | — |
| 518 | — |
| 522 | — |
| 523 | — |
| 524 | — |
| 525 | — |
| 526 | — |
| 527 | — |
| 528 | — |
| 529 | — |
| 530 | — |
| 531 | — |
| 532 | — |
| 533 | — |
| 534 | — |
| 535 | — |
| 536 | — |
| 537 | — |
| 538 | — |
| 539 | — |
| 540 | — |
| 541 | — |
| 542 | — |
| 543 | — |
| 544 | — |
| 545 | — |
| 546 | — |
| 547 | — |
| 548 | — |
| 549 | — |
| 550 | — |
| 551 | — |
| 552 | — |
| 553 | — |
| 554 | — |
| 555 | — |
| 556 | — |
| 557 | — |
| 558 | — |
| 559 | — |
| 560 | — |
| 561 | — |
| 562 | — |
| 563 | — |
| 564 | — |
| 565 | — |
| 566 | — |
| 567 | — |
| 568 | — |
| 569 | — |
| 570 | — |
| 571 | — |
| 572 | — |
| 573 | — |
| 574 | — |
| 575 | — |
| 576 | — |
| 577 | — |
| 578 | — |
| 579 | — |
| 580 | — |
| 581 | — |
| 582 | — |
| 583 | — |
| 584 | — |
| 585 | — |
| 586 | — |
| 587 | — |
| 588 | — |
| 589 | — |
| 590 | — |
| 591 | — |
| 592 | — |
| 593 | — |
| 594 | — |
| 595 | — |
| 596 | — |
| 597 | — |
| 598 | — |
| 599 | — |
| 600 | — |
| 601 | — |
| 602 | — |
| 603 | — |
| 604 | — |
| 605 | — |
| 606 | — |
| 607 | — |
| 608 | — |
| 609 | — |
| 610 | — |
| 611 | — |
| 612 | — |
| 613 | — |
| 614 | — |
| 615 | — |
| 616 | — |
| 617 | — |
| 618 | — |
| 619 | — |
| 620 | — |
| 621 | — |
| 622 | — |
| 623 | — |
| 624 | — |
| 625 | — |
| 626 | — |
| 630 | — |
| 631 | — |
| 632 | — |
| 633 | — |
| 634 | — |
| 636 | — |
| 638 | — |
| 639 | — |
| 641 | — |
| 642 | — |
| 643 | — |
| 644 | — |
| 645 | — |
| 646 | — |
| 647 | — |
| 648 | — |
| 658 | — |
| 659 | — |
| 660 | — |
| 661 | — |
| 662 | — |
| 663 | — |
| 664 | — |
| 665 | — |
| 666 | — |
| 667 | — |
| 668 | — |
| 669 | — |
| 670 | — |
| 671 | — |
| 672 | — |
| 673 | — |
| 674 | — |
| 675 | — |
| 677 | — |
| 678 | — |
| 679 | — |
| 680 | — |
| 681 | — |
| 682 | — |
| 683 | — |
| 684 | — |
| 685 | — |
| 686 | — |
| 687 | — |
| 688 | — |
| 694 | — |
| 711 | — |
| 718 | — |
