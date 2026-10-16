# PRD – hoex

Oct 15, 2026 · @Bálint Böde

## Áttekintés

A hoex egy CLI eszköz, amelyen keresztül egy Docker sandboxban futó agent (első körben a Claude Code) előre engedélyezett parancsokat futtathat a host gépen, és a kimenetet úgy kapja vissza, mintha helyben futtatta volna.

**Probléma.**

A sandbox védi a hostot, de bizonyos eszközök csak a hoston érhetők el vagy csak ott működnek megfelelően (például Flutter és Dart toolchain, platformspecifikus build). Ma az agentnek vagy teljes host-hozzáférést kell kapnia, vagy a felhasználónak kézzel kell futtatnia ezeket.

**Cél.**

Szűk, konfigurálható átjárót nyitni a sandbox és a host között: csak a felhasználó által engedélyezett parancsok futhatnak, minden futás látható a hoston, és az agent automatikusan megtanulja az eszköz használatát.

**Sikerkritériumok**

- Az agent a hoston futtatott parancs kimenetét helyi parancsként pipe-olhatja tovább a sandboxban (például `grep`), és a kilépési kód megegyezik a host oldalival.
- Engedélyezés nélküli parancs soha nem fut le a hoston.
- A felhasználónak nem kell kézzel skillt írnia: egy paranccsal telepíthető, és a konfigurációval szinkronban marad.

## Célközönség és használati eset

A célfelhasználó az a fejlesztő, aki AI agentet futtat Docker sandboxban, és bizonyos eszközöket tudatosan a hostra bízna.

**Fő használati eset (Flutter fejlesztő).**

1. A fejlesztő a projekt gyökerében elindítja a host démont: `hoex --mode host`.
2. A konfigurációban engedélyezi a `flutter` és `dart` prefixet.
3. Egyszer telepíti a skillt: `hoex --skill claude`.
4. A sandboxban futó Claude Code a skill alapján minden `flutter` és `dart` parancsot így futtat: `hoex --mode sandbox -- flutter test | grep FAIL`.
5. A fejlesztő a host termináljában élőben látja, mi fut és mit ír ki; az agent megkapja a kimenetet és a kilépési kódot.

## Működési modell és CLI

Egyetlen bináris, amelynek a szerepét a `--mode` paraméter dönti el; a skilltelepítés külön paraméter.

| Használat | Hol fut | Mit csinál |
| --- | --- | --- |
| `hoex --mode host` | Host, a projekt gyökerében | Folyamatosan futó démon. Fogadja a kéréseket, ellenőrzi őket a konfiguráció alapján, lefuttatja a parancsot, és a termináljában megjeleníti a parancsot és a kimenetét. |
| `hoex --mode sandbox -- <parancs>` | Sandbox | Elküldi a `--` utáni parancsot a hostnak, a stdoutot a saját stdoutjára, a stderrt a saját stderrjére írja, és a host oldali kilépési kóddal lép ki. |
| `hoex --skill claude` | Host | Legenerálja és a projekt `.claude/skills/` könyvtárába telepíti a skillt, és beállítja a Claude Code engedélyeit a konfigurációs fájlra. |

**Követelmények**

- A `--` utáni részt a sandbox oldal változtatás nélkül, argumentumlistaként továbbítja.
- A parancs a hoston a projektkönyvtárban fut; mivel a Docker sandbox ugyanazon az útvonalon csatolja fel a projektet, az abszolút útvonalak is érvényesek.
- A host termináljában minden futásnál látszik: időpont, a teljes parancs, a kimenet és a kilépési kód, illetve elutasításnál az ok.
- Ha a host nem elérhető, a sandbox oldal egyértelmű hibaüzenettel és külön, dokumentált kilépési kóddal áll le.

**Naplózás.**

A host minden futtatást és elutasítást tartós naplófájlba ír (`.hoex/hoex.log`): időpont, teljes parancs, kilépési kód, elutasításnál az ok. A parancsok kimenete nem kerül a naplóba. A napló a host leállása után is megmarad, csak a kapcsolati fájl törlődik.

## Kommunikáció

A két oldal gRPC-n kommunikál TCP felett; a host csak a helyi gépen figyel, a sandbox a `host.docker.internal` címen éri el. A Unix socket azért esett ki, mert macOS-en a Docker virtuális gépe miatt nem biztos, hogy átjut a konténerbe.

**Kapcsolódás és hitelesítés**

- A host indításkor véletlen portot és véletlen tokent választ, és ezeket a projektkönyvtárba írja (`.hoex/connection`).
- A sandbox oldal innen olvassa ki a portot és a tokent, külön beállítás nem kell.
- Token nélküli vagy hibás tokennel érkező kérést a host elutasít.
- A host leálláskor törli a kapcsolati fájlt; a `.hoex/` könyvtár nem kerül verziókezelésbe.

**Szolgáltatás.**

Egyetlen szerveroldali streamelő hívás; kétirányú streamre az első verzióban nincs szükség.

```proto
service HostRunner {
  rpc Run(RunRequest) returns (stream RunEvent);
}

message RunRequest {
  repeated string argv = 1;  // a -- utáni parancs
  string cwd = 2;            // a sandbox munkakönyvtára
}

message RunEvent {
  oneof event {
    bytes stdout = 1;
    bytes stderr = 2;
    int32 exit_code = 3;  // a stream utolsó üzenete
    string error = 4;     // pl. elutasított parancs
  }
}
```

- A stdout és a stderr külön üzenettípus, és folyamatosan, a keletkezés sorrendjében érkezik.
- Az elutasított parancs `error` üzenetet kap, ami elkülönül attól, ha a parancs lefutott, de hibával állt le.
- A token a gRPC hívás metaadatában utazik.

## Konfiguráció

A konfiguráció YAML, két szinten adható meg, és a két szint szabályai összeadódnak.

| Szint | Hely | Hatókör |
| --- | --- | --- |
| Felhasználói | A host felhasználójának config könyvtára (`~/.config/hoex/config.yaml`) | Csak a fájlban abszolút útvonallal megadott projektkönyvtárakra |
| Projekt | A projekt gyökerében (`.hoex.yaml`) | Az adott projekt; hozzáadódik a felhasználói szinthez |

**Felhasználói szintű példa**

```yaml
projects:
  - path: /Users/te/dev/my_flutter_app
    deny_chaining: true
    allow:
      - prefix: flutter
        description: Flutter build, teszt, futtatás
        example: flutter test
      - prefix: dart
        description: Dart eszközök
        example: dart format .
```

**Projektszintű példa**

```yaml
deny_chaining: true
allow:
  - pattern: '^npm (test|run build)$'
    description: Tesztek és build futtatása
    example: npm test
  - pattern: '^\./scripts/release\.sh$'
    description: Release script
    example: ./scripts/release.sh
```

**Szabályok**

- Minden szabályban pontosan egy `pattern` (regex) vagy `prefix` szerepel, és kötelező a `description` és az `example`; ezekből készül a skill.
- A `pattern` mindig a teljes parancsra illeszkedik, akkor is, ha a `^` és `$` hiányzik.
- A `prefix` teljes szavakra illeszkedik: a `flutter` engedélyezi a `flutter test` parancsot, de a `flutterx` nevűt nem.
- A `deny_chaining: true` elutasítja azokat a kéréseket, amelyek argumentumai parancsösszefűző jelet tartalmaznak (`;`, `&&`, `||`, `|`, sortörés, `$(`, backtick). Shell nélküli futtatásnál ezek amúgy sem hajtódnának végre, de így az agent egyértelmű hibát kap.
- Érvénytelen YAML vagy regex esetén a host nem indul el, és megnevezi a hibás sort.

## Parancsvégrehajtás

A host minden kérést három ellenőrzésen visz át, és csak ezután indítja a programot, shell nélkül.

&#91;embedded content: kérés feldolgozása · 3 ellenőrzés\]

Ha bármelyik ellenőrzés elbukik, a parancs nem indul el, és a sandbox error üzenetet kap.

- **Shell nélkül.** A host az argumentumlistát közvetlenül az operációs rendszernek adja át; nincs változókifejtés, átirányítás, pipe vagy glob a hoston. A pipe-olás a sandbox oldalon történik.
- **Shell scriptek.** A shebanggel (`#!/bin/bash`) és futtatási joggal rendelkező scriptek shell nélkül is futnak, ezért a `./scripts/build.sh` engedélyezhető. A `bash` vagy `sh` prefixet nem szabad engedélyezni, mert a `bash -c` bármit futtatna; a host erre figyelmeztet.
- **Környezet.** A parancs a projektkönyvtárban, a host démon környezeti változóival fut; a sandbox nem küldhet környezeti változót.
- **Megszakítás.** Ha a sandbox oldali folyamat leáll (pl. Ctrl+C vagy az agent megszakítja), a host leállítja a hozzá tartozó folyamatot is.
- **Párhuzamosság.** Több kérés futhat egyszerre; a host termináljában a kimenetek futásonként megkülönböztethetők.

## Skillgenerálás

A `hoex --skill claude` a konfigurációból dinamikusan generálja a skillt, és mindig projektszinten telepíti, mert az engedélyezett parancsok projektenként eltérnek.

- **Hely:** `<projekt>/.claude/skills/hoex/SKILL.md`.
- **Leírás (frontmatter).** Név szerint felsorolja a parancsokat, hogy a Claude Code már a parancs nevéből tudja, mikor kell a skillt használnia. Példa: „Használd minden `flutter` és `dart` parancs futtatásához; ezeket ne futtasd közvetlenül a sandboxban.”
- **Törzs.** A hívás formája (`hoex --mode sandbox -- <parancs>`), szabályonként a leírás és a példa, a pipe-olás lehetősége, a shell nélküli futás következményei, valamint az elutasítás és az elérhetetlen host jelentése.
- **Engedélyek.** Projektszintű konfigurációnál a telepítés a Claude Code projektbeállításaiban tiltja a konfigurációs fájl olvasását és írását.
- **Automatikus frissítés.** A host indításkor ellenőrzi, hogy a skill telepítve van-e a projektben; ha igen, a konfiguráció alapján újragenerálja.
- **Bővíthetőség.** Az első verzió csak a `claude` értéket támogatja, de a generálás agentenkénti sablonra épül, hogy később más agent is felvehető legyen a konfiguráció módosítása nélkül.

## Biztonság

Az eszköz szándékosan rést nyit a sandboxon, ezért a védelem több rétegre épül, és az ismert korlátok dokumentáltak.

**Védelmi rétegek**

1. Csak engedélyezési lista: ami nem illeszkedik egy szabályra, nem fut.
2. Shell nélküli futtatás és `deny_chaining`: egy engedélyezett parancs nem indíthat másodikat.
3. Csak helyi gépen figyelő port, futásonként generált tokennel.
4. A projektszintű konfiguráció olvasása és írása tiltott a Claude Code beállításaiban.
5. A host indításkor rögzíti a projektszintű konfiguráció ellenőrző összegét. Ha a fájl futás közben megváltozik, nem veszi át automatikusan: a saját felületén megmutatja a változást, és jóváhagyást kér. Ez akkor is véd, ha az agent a Claude Code engedélyeit megkerülve, parancsból írja a fájlt.
6. Minden futás látható a host termináljában.

**Ismert korlát: projektfájlokból dolgozó parancsok.**

Ha egy engedélyezett parancs a projekt fájljait hajtja végre (shell script, `npm run`, `make`, build scriptek), az agent a sandboxban átírhatja ezeket, és a host azt futtatja le. Ilyenkor az engedélyezési lista csak látszólag korlátoz. Az első verzióban ez a dokumentációban és a skillben is szerepel; javasolt bővítés, hogy a host a konfigurációban megjelölt fájlokat ugyanúgy figyelje és jóváhagyathassa, mint a konfigurációt.

## Technológia és terjesztés

Az eszköz Go nyelven készül, mert egyetlen statikus binárist ad macOS-re és Linuxra, és jól támogatja a gRPC-t.

- A host mód terminálfelülete a Charm [Bubble Tea](https://github.com/charmbracelet/bubbletea) és [Lip Gloss](https://github.com/charmbracelet/lipgloss) csomagjaira épül (futások listája, élő kimenet, konfigurációváltozás jóváhagyása).
- A sandbox mód nem használ TUI-t és stílusozást: a kimenetet változtatás nélkül írja a stdoutra és a stderrre, hogy a pipe-olás és az agent feldolgozása ne sérüljön.
- Ugyanaz a bináris fut a hoston és a sandboxban; a sandboxba Linux build kerül.
- Terjesztés Homebrew-n és a GitHubról letölthető binárisként (macOS: arm64, amd64; Linux: arm64, amd64).

## Hatókörön kívül (első verzió)

- Más agentek skillje (pl. Codex, Gemini CLI); a generálás fel van készítve rá.
- Interaktív parancsok és stdin továbbítása a hostra (ehhez kétirányú stream kellene).
- Shell-szolgáltatások a hoston (változókifejtés, átirányítás, pipe).
- Futásonkénti jóváhagyás; a döntés a konfigurációban születik.
- Távoli host vagy hálózaton keresztüli elérés.

## Előfeltételek, kockázatok, nyitott kérdések

**Előfeltételek**

- A host macOS-en és Linuxon fut; Windows nem támogatott.
- A Docker sandbox a projektet a hosttól azonos abszolút útvonalon csatolja fel.
- A konténerből elérhető a host a `host.docker.internal` címen.

**Kockázatok**

- A Docker sandbox hálózati korlátozása megakadályozhatja a kapcsolódást a hosthoz; ezt a fejlesztés elején ki kell próbálni macOS-en és Linuxon is.
- A projektfájlokból dolgozó parancsok megkerülhetik az engedélyezési listát (lásd Biztonság).

**Nyitott kérdések**

Jelenleg nincs nyitott kérdés.
