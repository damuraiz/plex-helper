# FileBot → независимый Plex Helper

Исследовано 14 октября 2026. Приложение выбрано пользователем:
`/Applications/FileBot.app`, версия **5.3.0**, build **11096**, manifest build
date **2026-09-10**. Основной Java-архив `Contents/Java/filebot.jar`:

```
SHA256 ad3399da8357d38bf542c56693422b9f8a6ffee0ae162519907787b52704bd37
```

## Выполненные операции REA

REA **6.5.0**, официальный CLI, работающий через установленный Node24:

| Операция | Evidence ID | Подтверждает |
| --- | --- | --- |
| inspect-artifact FileBot.app | ev_97c284f9de8e56305a3c1fdbcb3c74cf3c26ec89b2882dfa001d913fce4fbc47 | Статический граф пакета |
| inventory внутри app inspection | ev_314993521884098a69b4615114a7b182a6b5fa4d72a9fc388bff4b3428df1714 | Manifest agm_1049e517465601b36d06cf6f2477f23d27bba6145012aa1a85bf6e19c625b32c, 207 узлов/272 вхождения |
| inspect-plist FileBot.app | ev_4a159f0dafa9a1b43772e445b5273216619a7db30460d65aba56f7fa419e7a1a | Версия, Java main class/runtime и native launcher |
| inspect-artifact filebot.jar | ev_b8dfdffcf3b105a6cd643fbe8292b9d9ae50c8328ef33afc760663156cd5324a | ZIP/JAR manifest agm_3442a4aaef4db7984b0ef0adbfa9b949ef87e2d81dd7e5a29a2c4b4413071153; 1512 узлов/1521 вхождение |
| capture-process synthetic formatting probe | ev_745d2bae7dcb2b9eae73b0ae6b1177fce82191360fe6f2d9e66859303e246828 | Наблюдаемый stdout Java-пробы; exit0; cleanup verified |

Полные локальные Evidence и артефакты анализа сохраняются в
`research/evidence/` и `research/derived/`, исключённых из Git. Поставка
содержит этот аналитический отчёт, без FileBot JAR/ресурсов, дизассемблированных
листингов или встроенных секретов. Сводки хэшей привязывают наблюдения к версии,
но сами по себе не заменяют полные Evidence при дальнейшем исследовании.

Первый inspect-plist внутри sandbox не смог скомпилировать служебный macOS
helper REA. Повтор вне sandbox прошёл; FileBot при статической проверке не
запускался. Native декомпиляторы Ghidra/Hopper/IDA не требовались для JAR.

## Что установлено статически

JAR содержит **1132 Java class entries**. Через `javap` установленного JDK
выполнен дополнительный просмотр сигнатур и ограниченного байткода. Эти
наблюдения являются отдельным инструментальным анализом, а не результатом
Java-декомпиляции в REA:

- `MediaDetection`: отдельные процедуры извлечения года, определения
  фильма/серии, поиска сериала и сопоставления названий фильма.
- `SeasonEpisodeMatcher` и `SmartSeasonEpisodeMatcher`: отдельные механизмы
  single/multi/range/special. Сигнатуры подтверждают существование методов,
  но не доказывают точность распознавания на произвольных файлах.
- `TMDbMovieClient`, `TMDbTVClient`, `TheTVDBClientV4`: клиенты метаданных.
  Их наличие не доказывает успешный сетевой запрос в исследованном запуске.
- `NamingStandard.Plex`, `MediaBindingBean`, `CmdlineOperations`: выделенные
  форматирование, данные выражений и операции переименования.
- `StandardConflictAction`: режимы SKIP, REPLACE, AUTO, INDEX, FAIL. Их
  байткод не является основанием разрешать перезапись в новой программе.

Вывод для архитектуры Plex Helper: разделить разбор имени, получение и
выбор метаданных, генерацию целевого пути и фактическую файловую операцию.
Это проектное решение на основании наблюдений, а не перенос реализации.

## Наблюдаемое форматирование

Собственная маленькая Java-проба обращалась к публичным модельным API
установленного FileBot на трёх вымышленных объектах. REA записал:

```
movie=Movies/Example Movie (2024)/Example Movie (2024)
episode=TV Shows/Example Show/Season 01/Example Show - S01E02 - Pilot
special=TV Shows/Example Show/Season 00/Example Show - S00E01 - Bonus
```

Это конкретный вызов `NamingStandard.Plex.getPath`; он не доказывает
эквивалентность всем Groovy-выражениям FileBot, GUI-параметрам или действиям
над реальными файлами. Объект Episode пробы не содержал дату старта сериала,
поэтому по этому выводу нельзя заключать, что FileBot никогда не добавляет год.
Сетевую активность process capture не измеряет. Никакие файлы настоящей
медиатеки не подавались на вход.

## Независимая реализация

Правила новой программы основаны на текущей документации Plex:
[фильмы](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/),
[сериалы](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/),
[субтитры](https://support.plex.tv/articles/200471133-adding-local-subtitles-to-your-media/).
В частности, Plex Helper добавляет год сериала при наличии выбранных данных и
provider ID для более однозначного сопоставления. TMDB использует
[собственный токен пользователя](https://developer.themoviedb.org/docs/authentication-application)
и [поиск с последующим запросом деталей](https://developer.themoviedb.org/docs/search-and-query-for-details).

Код Plex Helper написан независимо на Go. Закрытый Java-код FileBot,
встроенные API-ключи, механизм лицензий и закрытые ресурсы не используются в
приложении или SPK. Не выполнялись патчи лицензирования или обходы активации.

## Осталось за границами исследования

- Точность сопоставления FileBot на пользовательских неоднозначных названиях.
- Эквивалентность всех выражений, alternate/anime порядков и xattr/NFO fallback.
- Сетевые обращения и актуальность ответов всех провайдеров FileBot.
- Поведение полного GUI FileBot при конфликтах или прерываниях записи.
- Полное восстановление исходного проекта: оно не выполнялось и не заявляется.

Для выбранной первой версии достаточно исследованных границ; добавлять
неподтверждённое автоматическое поведение в новую программу не требуется.
