# Fejlesztési terv – hoex

Alap: [PRD.md](PRD.md) · 2026-10-16

## Áttekintés

A fejlesztés hét mérföldkőben halad. Az első egy kockázatcsökkentő próba (hálózat a sandbox és a host között), utána a lehető leghamarabb egy végponttól végpontig működő, TUI nélküli MVP készül (M2), majd erre épülnek rá a naplózás, a TUI, a konfigurációváltozás jóváhagyása, a skillgenerálás és a terjesztés.

| # | Mérföldkő | Eredmény |
| --- | --- | --- |
| M0 | Váz és hálózati próba | Go modul, CI, bizonyított sandbox → host gRPC kapcsolat macOS-en és Linuxon |
| M1 | Konfiguráció és szabálymotor | YAML betöltés, összefésülés, validálás sorszámmal, illesztés |
| M2 | MVP: futtatás végponttól végpontig | `hoex --mode host` (sima naplókimenettel) és `hoex --mode sandbox -- …` működik, token, kilépési kódok |
| M3 | Megszakítás, párhuzamosság, napló | Ctrl+C leállítja a host folyamatot, párhuzamos futások, `.hoex/hoex.log` |
| M4 | Host TUI | Bubble Tea felület: futások listája, élő kimenet |
| M5 | Konfigurációváltozás jóváhagyása | Ellenőrző összeg, fájlfigyelés, jóváhagyó párbeszéd a TUI-ban |
| M6 | Skillgenerálás | `hoex --skill claude`, Claude Code engedélyek, automatikus frissítés |
| M7 | Terjesztés és dokumentáció | GoReleaser, GitHub Release binárisok, angol nyelvű README |

## Architektúra

### Könyvtárszerkezet

```
cmd/hoex/main.go           belépési pont, flag-feldolgozás, módválasztás
proto/hoex/v1/hoex.proto   a PRD-beli HostRunner szolgáltatás
gen/hoex/v1/               generált Go kód (verziókezelve)
internal/config/           YAML betöltés, két szint összefésülése, validálás
internal/policy/           szabályillesztés (prefix, pattern, deny_chaining)
internal/runner/           shell nélküli futtatás, stdout/stderr stream, leállítás
internal/server/           gRPC szerver, token-ellenőrző interceptor, Run handler
internal/client/           sandbox mód: kapcsolódás, stream kiírása, kilépési kód
internal/connfile/         .hoex/connection írása, keresése, törlése
internal/audit/            .hoex/hoex.log
internal/events/           host oldali eseménybusz (futás indult/kimenet/vége/elutasítva)
internal/tui/              Bubble Tea + Lip Gloss felület
internal/watch/            konfiguráció ellenőrző összege és fájlfigyelés
internal/skill/            agentenkénti sablonok, telepítés, Claude engedélyek
internal/exitcode/         dokumentált kilépési kódok
```

### Függőségek

- `google.golang.org/grpc`, `google.golang.org/protobuf` (kódgenerálás `buf`-fal, a generált kód commitolva)
- `gopkg.in/yaml.v3` (a `yaml.Node` adja a hibás sor számát)
- `github.com/charmbracelet/bubbletea`, `lipgloss`, `bubbles` (viewport, lista)
- `github.com/fsnotify/fsnotify`
- CLI: standard `flag` csomag (a `--` után megáll, a maradék `flag.Args()` lesz az argv)

### Host oldali adatfolyam

```
gRPC Run ──► token interceptor ──► cwd ellenőrzés ──► policy (3 ellenőrzés) ──► runner
                                                          │                       │
                                                          ▼                       ▼
                                                    events bus ◄──────── stdout/stderr/exit
                                                     │      │
                                                    TUI   audit log
```

Az `events` busz választja le a végrehajtást a megjelenítéstől: M2-ben egy egyszerű soros naplózó fogyasztja, M4-től a TUI. Így a szerver és a runner tesztelhető felület nélkül.

## Tervezési döntések (a PRD-ben nyitva hagyott részletek)

| Téma | Döntés |
| --- | --- |
| Kapcsolati fájl keresése | A sandbox oldal a munkakönyvtárból felfelé haladva keresi a `.hoex/connection` fájlt (mint a git a `.git`-et). A fájl formátuma JSON: `{"port": …, "token": "…", "pid": …}`, jogosultsága `0600`. |
| Projektgyökér | A host a saját indítási könyvtárát tekinti projektgyökérnek. |
| `cwd` mező | Ha a kért `cwd` a projektgyökéren belül van (`filepath.EvalSymlinks` után), ott fut a parancs; ha kívül, a kérés elutasítva. Üres `cwd` → projektgyökér. |
| A három ellenőrzés | 1) token (interceptor), 2) `deny_chaining`, 3) engedélyezési lista illesztése. Bármelyik bukása `error` eseményt ad, a folyamat nem indul. |
| Illesztett szöveg | A `pattern` és a `prefix` az argv elemeinek szóközzel összefűzött alakjára illeszkedik. A `pattern` automatikusan `^(?:…)$` közé kerül. A `prefix` szavakra bontva az argv első N elemével egyezik meg pontosan. |
| `bash`/`sh` figyelmeztetés | Induláskor figyelmeztetés, ha egy szabály prefixe vagy mintája shell-értelmezőt engedélyez (`bash`, `sh`, `zsh`, `dash`, `fish`, `env`, `xargs`). |
| Kilépési kódok (sandbox) | A host oldali kód változatlanul; jelzéssel leállított folyamatnál `128+jel`. Saját kódok: `64` hibás használat, `69` host nem elérhető / nincs kapcsolati fájl, `77` elutasított parancs, `70` belső/protokollhiba (sysexits). Az ütközés a parancs saját kódjaival elkerülhetetlen, a hibaüzenet stderr-en egyértelműsít. |
| Leállítás | A runner saját folyamatcsoportba indítja a parancsot (`Setpgid`); megszakításkor SIGTERM a csoportnak, 5 mp után SIGKILL. |
| Kimeneti sorrend | Két goroutine olvassa a pipe-okat, egy közös csatornán át, egyetlen küldő goroutine írja a streamet, így a sorrend az olvasás sorrendje. |
| Token | 32 bájt `crypto/rand`, hex; összehasonlítás `subtle.ConstantTimeCompare`-rel, metaadat kulcs: `authorization: Bearer …`. |
| Port | **Eltérés a PRD-től (M0 eredménye):** rögzített alapport (`47800`), `127.0.0.1`-en. Felülírható a `--port` kapcsolóval vagy a konfiguráció `port` kulcsával, ha több projekt fut egyszerre. A token továbbra is futásonként véletlen; a sandbox a portot a kapcsolati fájlból olvassa. Foglalt portnál a host egyértelmű hibával áll le. |

## Mérföldkövek

### M0 – Váz és hálózati próba

- `go mod init`, könyvtárszerkezet, `Makefile` (build, test, lint, proto), `.gitignore` (`.hoex/`), golangci-lint, GitHub Actions (Linux + macOS, `go test ./...`).
- `hoex.proto` a PRD szerint, `buf generate`.
- **Hálózati próba** (a PRD fő kockázata): minimális gRPC szerver a hoston, kliens Docker sandboxban `host.docker.internal`-lal, macOS-en (Docker Desktop) és Linuxon is. Kérdések:
  - Eléri-e a konténer a hoston `127.0.0.1`-re kötött portot? (Linuxon a `host.docker.internal` gyakran a bridge gateway-re mutat, és a loopbackre kötött port ott nem látszik.)
  - A Docker Sandboxes hálózati szabályzata portonként engedélyez (`localhost:<port>`); véletlen port mellett ez minden indításkor új engedélyt kérne.
  - Átmegy-e a gRPC a sandbox proxyján (`HTTPS_PROXY`), vagy `grpc.WithNoProxy()` kell.
- **Tartalék, ha a próba elbukik:** rögzített vagy konfigurálható port (`--port`, alapból egy állandó érték), illetve Linuxon a docker bridge címére kötés. Az eredményt és a döntést ebbe a fájlba kell visszaírni.
- Eszköz: `cmd/hoex-probe` (`server` a hoston, `client` a sandboxban) a valódi `HostRunner` protokollal; tesztek: hibás token, stdout + stderr + kilépési kód, 16 MiB stream, megszakítás menet közben.

**Eredmény – Linux host, Docker Sandboxes (2026-10-16)**

| Kérdés | Eredmény |
| --- | --- |
| `127.0.0.1`-re kötött port elérhető? | **Igen.** A sandbox a `host.docker.internal`-t a proxyn át a host `localhost`-jára továbbítja, `0.0.0.0`-ra kötés nem kell. |
| gRPC (HTTP/2 titkosítás nélkül) átmegy a proxyn? | **Igen**, mind a négy teszt átment; a megszakítás eljut a hostig. |
| Proxy nélkül (`grpc.WithNoProxy()`)? | Szintén működik: a közvetlen kapcsolatot is ugyanaz a szabályzat engedi át. A kliens marad az alapbeállításnál (környezeti proxy), külön kezelés nem kell. |
| Hálózati szabályzat | **Portonként jóváhagyás kell** (`Approval required for localhost:<port>`, utána `sbx policy approval respond …` vagy `sbx policy allow network localhost:<port>`). Jóváhagyás előtt a kliens csak `connection reset` / `EOF` hibát lát. |

**Következmények**
- Véletlen port helyett rögzített alapport (lásd Tervezési döntések), így a jóváhagyás egyszeri.
- A sandbox oldal hibaüzenete elérhetetlen hostnál említse a lehetséges okokat: nem fut a host, vagy a port nincs engedélyezve (`sbx policy allow network localhost:<port>`). Ha a proxy 403-at ad, ezt a kliens nem látja közvetlenül, ezért a hibaüzenet mindkét okot felsorolja.
- A README hálózati része (M7) dokumentálja az egyszeri engedélyezést.
- Hátra van: ugyanez macOS-en (Docker Desktop).

**Kész, ha:** CI zöld, a próba eredménye dokumentálva mindkét platformon. Állapot: Linux ✓, macOS függőben, CI még nem futott (nincs távoli repó).

### M1 – Konfiguráció és szabálymotor

- `internal/config`: felhasználói (`~/.config/hoex/config.yaml`, `os.UserConfigDir` helyett XDG/`~/.config` macOS-en is) és projektszint (`.hoex.yaml`) betöltése. A felhasználói szintből csak az a `projects` bejegyzés számít, amelynek abszolút `path`-ja (`EvalSymlinks` után) egyezik a projektgyökérrel.
- Összefésülés: a szabályok összeadódnak; a `deny_chaining` szintenként a saját szabályaira vonatkozik (a szigorúbb nyer, ha egy parancsra több szint szabálya illik).
- Validálás `yaml.Node` alapján, sorszámmal: ismeretlen kulcs, `pattern` és `prefix` egyszerre / egyik sem, hiányzó `description`/`example`, hibás regex, relatív `path`.
- `internal/policy`: `Check(argv) → (rule, reason)`; `deny_chaining` jelek: `;`, `&&`, `||`, `|`, `\n`, `$(`, `` ` ``.
- Táblázatos unit tesztek: `flutter` ↔ `flutter test` ✓, `flutterx` ✗, horgony nélküli minta teljes illesztése, láncolójelek, hibás YAML sorszáma.

**Kész, ha:** a PRD mindkét példakonfigurációja betölt, a hibás változatok a megfelelő sorszámot adják.

**Állapot: kész (2026-10-16).** Megvalósítás közben rögzített részletek:
- YAML: `go.yaml.in/yaml/v3` (a `gopkg.in/yaml.v3` karbantartott forkja); a fájl `yaml.Node` fán validálódik, minden hiba egyszerre, `fájl:sor: üzenet` alakban.
- `deny_chaining` alapértéke `true`; az `allow` lista opcionális (egy bejegyzés csak `port`-ot is megadhat).
- `port`: felhasználói szinten a `projects` bejegyzésben; a projektszint felülírja.
- Figyelmeztetések (`Config.Warnings`, nem akadályozzák az indulást): a szabály engedi a `<shell> -c …` alakot (`bash`, `sh`, `zsh`, `dash`, `ksh`, `fish`, `env`, `xargs`), illetve a szabály `example`-je nem illik a saját szabályára.
- A `pattern` `^(?:…)$` közé kerül, így a `|` alternatíva sem lóg ki a horgonyból.

### M2 – MVP végponttól végpontig

- `internal/connfile`: írás atomikusan (`0600`), keresés felfelé, törlés leálláskor (SIGINT/SIGTERM kezelése).
- `internal/server`: token interceptor, `Run` handler (cwd → policy → runner), elutasításnál `error` esemény, a stream utolsó üzenete mindig `exit_code` vagy `error`.
- `internal/runner`: `exec.CommandContext` shell nélkül, a host démon környezetével, a projekt/cwd könyvtárban.
- `internal/client`: kapcsolati fájl, dial `host.docker.internal:<port>`, stdout/stderr nyers kiírása, kilépés a megfelelő kóddal; elérhetetlen hostnál egyértelmű üzenet és `69`.
- Host oldal ideiglenes kimenete: időbélyeggel ellátott sorok stdoutra (parancs, kimenet, kilépési kód, elutasítás oka).
- Integrációs teszt (in-process szerver + kliens): kimenet byte-pontos, kilépési kód egyezik, rossz/hiányzó token elutasítva, nem engedélyezett parancs nem indul el (ellenőrzés egy marker-fájllal).

**Kész, ha:** a sandboxban `hoex --mode sandbox -- flutter test | grep FAIL` működik, és a kilépési kód megegyezik a host oldalival (PRD sikerkritérium 1–2).

**Állapot: kész (2026-10-16), Linux hoston Docker sandboxból ellenőrizve** (pipe + kilépési kód, alkönyvtár, elutasítás, láncolás, a 0600-as kapcsolati fájl olvasható a sandboxból, a sandbox oldal leállása a host folyamatot is leállítja). Megvalósítás közben rögzített részletek:
- Új csomagok: `internal/connfile`, `internal/runner`, `internal/events` (soros naplózó: `#<futás> │` stdout, `#<futás> ┃` stderr), `internal/server`, `internal/client`, `internal/host`.
- A `.hoex/` létrehozásakor egy `*` tartalmú `.hoex/.gitignore` kerül bele, így a könyvtár a projekt `.gitignore`-jának módosítása nélkül marad ki a verziókezelésből.
- Nem indítható parancs: a host a shellekhez hasonlóan 127-tel (nem található) vagy 126-tal (nem futtatható) és stderr-üzenettel válaszol; ez nem `error` esemény, mert a kérést engedélyezte.
- Hibás token (`Unauthenticated`) a sandboxban `69`, azzal az üzenettel, hogy valószínűleg egy másik projekt hostja fut ugyanazon a porton.
- `HOEX_HOST` környezeti változó felülírja a `host.docker.internal`-t (tesztek, Dockeren kívüli futás).
- Elutasított token is naplózódik a host kimenetén (`deny … (invalid token)`).

### M3 – Megszakítás, párhuzamosság, napló

- Kliens: SIGINT/SIGTERM → a gRPC context megszakítása; szerver: context vége → folyamatcsoport leállítása.
- Futásonkénti azonosító; párhuzamos futások egymás mellett.
- `internal/audit`: `.hoex/hoex.log` hozzáfűzéssel, soronként egy JSON rekord (időpont, futásazonosító, argv, cwd, kilépési kód vagy elutasítás oka), kimenet nélkül; leálláskor megmarad.
- Tesztek: `sleep 100` megszakítása után a folyamat nem él; két párhuzamos futás kimenete nem keveredik; naplórekord minden futásra és elutasításra.

**Állapot: kész (2026-10-16), a sandboxon belül binárissal is ellenőrizve.** Megvalósítás közben rögzített részletek:
- Sandbox oldal: SIGINT/SIGTERM/SIGHUP → a hívás megszakítása, kilépés `128+jel` kóddal (Ctrl+C → 130), mint egy shellnél.
- Runner: a parancs saját folyamatcsoportban fut; megszakításkor a csoport SIGTERM-et kap, 5 mp után SIGKILL-t. A kimenet `exec.Cmd` írókon át érkezik, és a `WaitDelay` miatt egy háttérben hagyott gyerekfolyamat legfeljebb 6 mp-ig tartja nyitva a futást (utána a kimenet lezárul, a kilépési kód megmarad). A parancs lefutása után megmaradt háttérfolyamatokat a host nem állítja le, ahogy egy shell sem.
- Napló (`internal/audit`): JSON Lines a `.hoex/hoex.log`-ban, eseménytípusok: `start` (port, szabályok száma), `run` (argv, cwd, `exit_code` vagy `aborted`, `duration_ms`), `deny` (argv, cwd, `reason`), `stop`. A kimenet nem kerül bele.
- Megszakított futás a host kimenetén: `#N abort canceled by the sandbox`.

### M4 – Host TUI

- Bubble Tea modell az `events` buszra feliratkozva: bal oldalon a futások listája (állapot, időpont, parancs, kilépési kód / elutasítás), jobb oldalon a kiválasztott futás élő kimenete (viewport, stderr eltérő színnel).
- Fejléc: projektgyökér, port, betöltött szabályok száma, figyelmeztetések.
- Nem TTY környezetben (vagy `--no-tui` jelzővel) az M2-es soros kimenet marad.
- Teszt: `teatest`-tel a modell állapotátmenetei.

**Állapot: kész (2026-10-16), pszeudoterminálban binárissal is kipróbálva.** Megvalósítás közben rögzített részletek:
- Charm v2 csomagok: `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `charm.land/bubbles/v2` (viewport).
- A host a saját üzeneteit is eseményként küldi (`HostStarted`, `Warning`, `Stopping`, `HostStopped`), így ugyanaz a kód hajtja a soros naplózót (`--no-tui`, vagy ha a stdout nem terminál) és a TUI-t.
- `tui.Feed`: az események pufferbe kerülnek, a TUI kötegekben veszi ki őket, így a lassú terminál nem lassítja a parancsokat.
- Felület: fejléc (projekt `~`-vel rövidítve, port, szabályok, futások), legfeljebb 3 figyelmeztetés, bal oldalt a futások (● fut, ✓ 0, ✗ hibakód, ⊘ elutasítva, ■ megszakítva), jobb oldalt a kiválasztott futás (könyvtár a projekthez képest, idő, időtartam, eredmény) és a kimenet (stderr pirossal).
- Billentyűk: ↑/↓ kiválasztás (követés ki), `f`/End a legutóbbi követése, PgUp/PgDn és Ctrl+U/D görgetés, `q`/Ctrl+C kilépés (leállítja a hostot).
- A kimenet követi a végét, hacsak a felhasználó nem görgetett feljebb. Futásonként legfeljebb 10 000 sor marad meg; az ANSI-szekvenciák és vezérlőkarakterek kiszűrve, `\r` után csak a sor utolsó állapota (folyamatjelzők).
- Teszt: `teatest` helyett a modell közvetlen meghajtása üzenetekkel és a kirajzolt képernyő ellenőrzése (sorok száma, szélessége, tartalma).
- Megjegyzés: a stdout és a stderr két külön csövön érkezik, ezért egymáshoz viszonyított sorrendjük nem garantált (csövön belül igen); ez a sandboxba érkező streamre is igaz.

### M5 – Konfigurációváltozás jóváhagyása

- Induláskor SHA-256 a `.hoex.yaml`-ról; `fsnotify` figyeli a fájlt (a könyvtárat, hogy az atomikus csere is látsszon).
- Változáskor az új konfiguráció validálása, a régi és új szabálylista különbségének megjelenítése a TUI-ban, jóváhagyás/elutasítás. Amíg nincs döntés, a régi konfiguráció érvényes. Érvénytelen új fájl: figyelmeztetés, a régi marad.
- Jóváhagyás után a skill újragenerálása (ha telepítve van) és naplóbejegyzés.
- Nem TTY módban a változás nem kerül át; figyelmeztetés, hogy újraindítás szükséges.

**Állapot: kész (2026-10-16), a skill újragenerálása az M6-ra marad. Pszeudoterminálban binárissal is kipróbálva.** Megvalósítás közben rögzített részletek:
- `internal/watch`: `fsnotify` a fájl könyvtárán (az atomikus csere is látszik), mellette 2 mp-enkénti ellenőrzés. A sandbox mountján nem biztos, hogy a fájlesemények átjutnak a hosthoz. 100 ms várakozás az esemény után, hogy a szerkesztő befejezze az írást.
- Az összehasonlítás a fájltartalom SHA-256 összegével történik. A hiányzó fájl üres tartalomnak számít. Az összeg a napló `start` rekordjában is szerepel.
- A jóváhagyásra felkínált konfiguráció abból a tartalomból készül (`config.LoadData`), amelyet a host megmutatott. Ha a fájl a döntés előtt újra megváltozik, a host újraolvassa: a régi ajánlat visszavonódik (`withdrawn`), és az új jelenik meg helyette. Az időközben megérkező, régi ajánlatra vonatkozó döntést a host figyelmen kívül hagyja.
- Az eltérés szabályonként jelenik meg: `+` új, `-` törölt, `~` csak leírás/példa változott vagy új port. A port csak újraindítás után érvényes. A `deny_chaining: false` a szabály mellett látszik („chaining allowed”). Ha nincs érdemi eltérés (csak megjegyzés, formázás vagy kulcssorrend változott), a host jóváhagyás nélkül átveszi a fájlt.
- Elutasított tartalmat a host nem ajánl fel újra. Ha a fájl visszaáll az aktív tartalomra, a függő ajánlat visszavonódik.
- Érvénytelen új fájl: időbélyeges figyelmeztetés a fejlécben (csak a legutóbbi futás közbeni figyelmeztetés látszik), a régi szabályok maradnak.
- A felhasználói szintű fájlt a host nem figyeli, mert a sandbox nem látja. Egy projektfájl-változáskor viszont újraolvassa, így az eltérésben az ottani változások is megjelennek.
- A szerver szabályai atomikusan cserélődnek (`Server.SetPolicy`); a már futó parancsokat a csere nem érinti.
- TUI: a függő változás a jobb oldali panelen jelenik meg, `a` jóváhagy, `r` elutasít; az ablak címe is jelzi. Jóváhagyás után a fejléc szabályszáma és a konfigurációs figyelmeztetései frissülnek.
- Napló: `config_change` (sorszám, ellenőrző összeg, eltérés), illetve `config_approved` / `config_rejected` / `config_withdrawn`.
- Induláskori ellenőrzés (`internal/approval`): a jóváhagyott tartalom projektenként a `$XDG_STATE_HOME/hoex/approved/` (alapból `~/.local/state/hoex/approved/`) alatt, a projektgyökér hash-ével elnevezett fájlban. Induláskor ez az érvényes konfiguráció; ha a projektfájl eltér tőle, a host azonnal jóváhagyásra kínálja a változást. Az első indítás egy projektben jóváhagyásnak számít. Ha a tárolt tartalom már nem tölthető be, a host csak a felhasználói szintű szabályokkal indul, és a teljes projektfájlt kínálja jóváhagyásra.
- `--no-tui` módban nincs jóváhagyás: a host jelzi, hogy a változáshoz TUI-val kell indítani, addig a jóváhagyott szabályok érvényesek.

### M6 – Skillgenerálás

- `internal/skill`: agentenkénti `text/template` sablon (`embed`-del), regiszter `claude` kulccsal; ismeretlen érték → hiba a támogatott listával.
- `SKILL.md` frontmatter: `name: hoex`, `description` a parancsok felsorolásával: `prefix` szabálynál a teljes, akár többszavas prefix („Használd minden `fvm flutter` és `fvm dart` parancs futtatásához…”), `pattern` szabálynál a szabály `example`-je. Csak az első szó (pl. `fvm`) félrevezető lenne, mert az `fvm install` nincs engedélyezve.
- Törzs: hívásforma, szabályonként leírás + példa, pipe-olás, shell nélküli futás következményei (nincs változókifejtés, átirányítás, glob a hoston), kilépési kódok (`69`, `77`) jelentése, a projektfájlokból dolgozó parancsok korlátja.
- Engedélyek: `.claude/settings.json` összefésülése (meglévő tartalom megtartásával) `permissions.deny` bejegyzésekkel: `Read(/.hoex.yaml)`, `Edit(/.hoex.yaml)`.
- Host induláskor és konfigurációváltozás jóváhagyásakor: ha `.claude/skills/hoex/SKILL.md` létezik, újragenerálja.
- Golden-file tesztek a generált skillre és a settings-összefésülésre.

**Kész, ha:** egy paranccsal telepíthető, és a konfiguráció módosítása után a host újraindítása frissíti (PRD sikerkritérium 3).

**Állapot: kész (2026-10-16).** Megvalósítás közben rögzített részletek:
- A skill angol nyelvű (a felhasználó kérése); a szabályok `description` mezője változatlanul kerül bele.
- Engedélyszabályok a Claude Code dokumentációja alapján: projektbeállításban a `/` a projektgyökérhez horgonyoz (a `./` az aktuális könyvtárhoz, ami alkönyvtárból nem védene). A `Write(path)` szabályt a Claude Code nem nézi; az `Edit` minden szerkesztő eszközre, a `Read` tiltás az írásra is vonatkozik, és a Bashben felismert fájlparancsokra (`cat`, `sed`, átirányítás) is. Tetszőleges alfolyamatot (pl. Python script) nem véd; ezt a host jóváhagyása fedi le.
- A `settings.json` csak akkor íródik, ha hiányzik valamelyik szabály; ilyenkor a kulcsok ábécérendbe kerülnek. Érvénytelen JSON vagy nem várt szerkezet esetén hiba, a fájl nem változik.
- A `--skill claude` az érvényes konfigurációból dolgozik: a jóváhagyott tartalomból, ha van, különben a fájlból; jóváhagyást nem rögzít. Ha a fájl eltér a jóváhagyottól, ezt jelzi.
- A host indításkor és minden konfigurációcserénél (jóváhagyás, illetve érdemi eltérés nélküli átvétel) újragenerálja a telepített skillt; hiba esetén figyelmeztetés.
- Az írás atomikus (ideiglenes fájl + átnevezés).
- **Biztonsági javítás (`internal/projfs`):** a sandbox a projektben bármit szimbolikus linkre cserélhet, például a `.hoex/hoex.log`-ot a host `~/.bashrc`-jére. Korábban a host oda fűzte volna a naplósorokat, bennük az elutasított parancs argumentumaival, és egy `"$(…)"` argumentum a következő shell indításakor lefutott volna. A host minden projektbe írása (`.hoex/` könyvtár, kapcsolati fájl, napló, skill, `.claude/settings.json`) most `os.Root`-on keresztül megy, ami a művelet pillanatában utasítja el a projektből kivezető utakat. Ha a napló vagy a `.hoex` ilyen link, a host nem indul el.
- Leírás: `prefix` szabálynál a teljes prefix, `pattern` szabálynál az `example` kerül a felsorolásba; a törzsben a minta `^(?:…)$` alakban szerepel.

### M7 – Terjesztés és dokumentáció

- GoReleaser: `CGO_ENABLED=0`, darwin/linux × arm64/amd64, checksumok, GitHub Release tagre.
- Homebrew nincs (a felhasználó döntése): a GitHub Release a terjesztés egyetlen módja.
- README angolul: telepítés (host + a Linux bináris bejuttatása a sandboxba), gyors kezdés a Flutter példával, konfigurációs referencia, kilépési kódok, biztonsági modell és ismert korlátok, hálózati beállítás (az M0 eredménye alapján).

**Állapot: a konfiguráció és a README kész (2026-10-21), helyi `goreleaser release --snapshot` lefutott; éles kiadás a GitHub repó után.** Megvalósítás közben rögzített részletek:
- `.goreleaser.yaml`: csak a `cmd/hoex` (a `hoex-probe` nem), `CGO_ENABLED=0`, `-trimpath`, a verzió `-X main.version`-nel (`hoex --version`). Az archívumok neve verzió nélküli (`hoex_linux_amd64.tar.gz`), így a `releases/latest/download/…` URL mindig a legutóbbi kiadásra mutat; a README telepítési parancsa erre épül.
- `.github/workflows/release.yml`: `v*` tagre teszt, majd GoReleaser a beépített `GITHUB_TOKEN`-nel.
- A sandboxba a bináris letöltéssel kerül (ugyanaz a parancs, mint a hoston); ehhez a sandboxnak el kell érnie a `github.com`-ot.

## Tesztelési stratégia

- **Unit:** config, policy, connfile, skill (golden), exitcode.
- **Integráció:** in-process gRPC szerver + kliens, valós folyamatokkal (`echo`, `sh -c 'exit 3'` csak tesztben, `sleep`).
- **Végponttól végpontig (CI, Linux):** a Linux bináris egy Docker konténerben fut kliensként, a host a runneren; ellenőrzi a pipe-olást, a kilépési kódot, az elutasítást, a megszakítást és az elérhetetlen hostot.
- **Kézi (macOS, Docker Desktop):** a Flutter használati eset végigjátszása minden kiadás előtt.

## Kockázatok

| Kockázat | Kezelés |
| --- | --- |
| A sandbox nem éri el a hoston loopbackre kötött portot | Linuxon cáfolva (M0); macOS-en még ellenőrizni kell |
| Portonkénti hálózati jóváhagyás a sandboxban | Rögzített alapport, egyszeri engedélyezés, a hibaüzenet és a README utal rá |
| Projektfájlokból dolgozó parancsok megkerülik a listát | Dokumentálva README-ben és skillben; későbbi bővítés: megjelölt fájlok figyelése az M5 mechanizmusával |
| A sandbox késve látja a hoston írt változásokat (virtiofs: elavult könyvtárlista, a hozzáfűzött fájl régi mérete; a 2026-10-21-i Flutter-próbán észlelve) | A hoex kimenete és kilépési kódja mindig teljes; README és skill leírja, a skill szerint az agent ezekre támaszkodjon, és elavultnak tűnő fájlnál ürítse a VM gyorsítótárát (`sudo sh -c 'sync; echo 3 > /proc/sys/vm/drop_caches'`; az alap sandbox-image-ben az `agent` felhasználónak jelszó nélküli `sudo`-ja van, kipróbálva: azonnal friss tartalmat ad) |
| Kilépési kód ütközés a saját kódokkal | Stderr-üzenet egyértelműsít; kódok dokumentálva |
