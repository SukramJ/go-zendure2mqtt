# Konzept: go-zendure2mqtt

Bridge zwischen Zendure-Geräten (z. B. SolarFlow 2400 AC) und einem lokalen
MQTT-Broker, mit optionaler Home-Assistant-Auto-Discovery. Zwillingsprojekt zu
[`go-daikin2mqtt`](https://github.com/SukramJ/go-daikin2mqtt) (Cloud-Blueprint)
und [`go-mtec2mqtt`](https://github.com/SukramJ/go-mtec2mqtt) (Local-Blueprint);
es übernimmt deren Projekt-Setup.

**Stand:** M0–M4 umgesetzt und gegen ein echtes SolarFlow 2400 AC verifiziert —
lokaler + Cloud-Transport, HA-Discovery (Sub-Devices + i18n), virtuelle Switches,
Re-Read nach Write, Diagnose-Web-UI, mDNS-Browser und HA-Add-on.

## 1. Kernidee

Lokal und Cloud nutzen **dasselbe Property-Datenmodell** (`electricLevel`,
`solarInputPower`, `acMode`, `inputLimit`/`outputLimit`, `socSet`/`minSoc`,
`packData[]` …) und nahezu identische Payloads (`{"properties": {…},
"packData": […]}`). Sie unterscheiden sich nur im **Transport**:

| | Lokal | Cloud |
|---|---|---|
| Transport | HTTP request/response | MQTT pub/sub (TLS) |
| Telemetrie | `GET /properties/report` (Poll) | subscribe `iot/{prodKey}/{deviceId}/properties/report` |
| Steuern | `POST /properties/write {sn, properties}` | publish `iot/{prodKey}/{deviceId}/properties/write` |
| Discovery | mDNS `_zendure._tcp` / feste IP | REST `POST {api_url}/api/ha/deviceList` |
| Auth | keine (LAN) | App-Token → Signatur-Login → MQTT-Creds |

Daraus folgt ein gemeinsames Geräte-/Property-Modell plus ein
`Source`/`Controller`-Interface (`internal/source`) mit zwei Implementierungen
(`local`, `cloud`). Coordinator, Katalog, HA-Discovery, Process und der
**Ausgabe-MQTT** (lokaler Mosquitto) sind transportneutral.

**Zwei MQTT-Rollen** im Cloud-Modus:
- **Ausgabe-Broker** (lokaler Mosquitto): publish normalisierter State +
  HA-Discovery, subscribe `set`-Befehle (`<name>/set/…`). Nutzt MQTT 5.0 (go-mqtt
  v1.0.0 Default).
- **Zendure-Cloud-Broker** (dynamisch aus dem Login, z. B.
  `mqtteu.zen-iot.com:8883`, TLS): subscribe Telemetrie, publish Steuerung.
  Reine Eingangsquelle. Fest auf `ProtocolV311` gepinnt — der
  Drittanbieter-Broker hat unbestätigten MQTT-5.0-Support.

## 2. Architektur / Pakete

```
cmd/zendure2mqtt        Daemon (Wiring: config→backend→coordinator→mqtt)
cmd/zendure2mqtt-util   Diagnose-CLI (report, set, cloud-login, catalog-check)
internal/
  config                YAML + ENV (ZENDURE_*) + XDG + Validate
  zendure/model         Report/WriteRequest (gemeinsames Datenmodell)
  zendure/local         HTTP-Client + Poll-Backend (GET report / POST write)
  zendure/cloud         Token-Decode + Signatur-Login; Cloud-MQTT-Stream (M3)
  source                Source/Controller/Backend-Interfaces
  catalog               zendure.yaml: property → topic/group/platform/unit/scale/…
  process               Resolve: Skalierung, value_map, packData → Points; Topic-Helfer
  coordinator           Report→publish ; set→Write + sofortiges Re-Read ; HA-Discovery
  hass                  HA Auto-Discovery (sensor/number/select/switch, Sub-Devices)
  discovery             dependency-freier mDNS-Browser (_zendure._tcp)
  state                 thread-safer Snapshot-Cache (nur bei aktivem Web-UI)
  web                   optionale Diagnose-Web-UI (embedded SPA, /api/health|snapshot)
  mqtt                  eigener MQTT-Client (protocol-Codec + Lifecycle, TLS-fähig)
  version               Build-Metadaten (LDFLAGS)
```

## 3. MQTT-Topic-Schema (Ausgabe)

Seit 0.10.0 nach der Konvention mqtt-smarthome 2.0 (openccu-loom ADR 0083),
`<name>/<funktion>/<item…>`; `<name>` = `MQTT_TOPIC`, Default `zendure`:

```
zendure/status/<sn>/now/<key>                Leistung, SoC, Status
zendure/status/<sn>/config/<key>             schreibbare Settings (Spiegel)
zendure/status/<sn>/static/<key>             rssi, Identität
zendure/status/<sn>/battery/<packSn>/<key>
zendure/status/<sn>/misc/<property>          nicht katalogisiert (ohne HA-Entity)
zendure/status/<sn>/online                   true|false, Gerät erreichbar
zendure/set/<sn>/<group>/<key>               ← Befehlstopic (gleicher Item-Pfad, QoS 1)
zendure/connected                            0|1|2 (LWT 0, retained)
zendure/info                                 Instanz-Info (retained)
zendure/maintenance/set/{loglevel,restart}   Wartung (abschaltbar)
zendure/maintenance/stats                    Prozessstatistik (retained)
homeassistant/device/zendure2mqtt_<sn>/config            retained Discovery (ein Dokument je Gerät)
homeassistant/device/zendure2mqtt_<sn>_pack_<packSn>/config   dito je Batteriepack
```

Bis 0.9.x: `zendure2mqtt/<sn>/<group>/<key>/state` bzw. `…/set` und
`zendure2mqtt/bridge/status` (`online`/`offline`). Jeder Status ist ein
Objekt `{"val","ts","lc"}` (ms); Zahlen als JSON-Zahl in Anzeigeeinheit,
Enums als Token (englischer `value_map`-Eintrag), Switches als JSON-Boolean.
`connected` = 2 solange der Upstream nutzbar ist (lokal: mind. ein Gerät
antwortet; Cloud: Cloud-Session steht), sonst 1. Die HA-Identitäten (unique_id,
Identifier, node id) hängen an der **Identity-Root** — `MQTT_TOPIC` falls
gesetzt, sonst `zendure2mqtt` —, nicht am neuen Default-Namen. Beim Start
räumt ein Sweep die retained Topics des alten Layouts unter der alten Root
(nur exakte alte Formen bekannter Seriennummern, nie per Präfix).

## 4. Property-/Steuerungs-Modell (SolarFlow 2400 AC)

- **Sensoren (RO):** `electricLevel` (% SoC), `solarInputPower`,
  `packInputPower`, `outputPackPower`, `gridInputPower`, `outputHomePower`,
  `gridOffPower`, `remainOutTime`, `hyperTmp` (→ °C: `(raw-2731)/10`),
  `BatVolt` (V, ÷100), `chargeMaxLimit` (W), `packNum`, `rssi`.
- **Steuerbar (RW):** `acMode` (1=Laden/2=Entladen), `inputLimit`/`outputLimit`
  (W), `socSet` / `minSoc` (Gerät liefert **Deci-Prozent** → `scale: 10`; beim
  Schreiben wird zurückskaliert: `raw = wert*scale + offset`), `inverseMaxPower`,
  `smartMode`.
- **Batterie-Packs:** `packData[]` → je Pack-SN eigene Sub-Entitäten
  (`socLevel`, `power`, `maxTemp`, `totalVol`, `maxVol`, `minVol`, `batcur`/A ÷10).
- Nicht katalogisierte Properties werden roh unter `…/status/<sn>/misc/<name>`
  publiziert (kein HA-Entity) — so geht nichts verloren.

Alles deklarativ in [`zendure.yaml`](../zendure.yaml).

## 4.1 HA-Discovery-Konventionen (wie go-daikin2mqtt)

- **`default_entity_id`** (ersetzt das von HA entfernte `object_id`): englisch,
  sprachneutral = `<platform>.<slug(Gerätename)>_<englischer Topic>`. Damit
  bleiben `entity_id`s stabil, während der Anzeige-`name` lokalisiert wird
  (deutsch bei `LANGUAGE: de`).
- **Availability** (seit 0.10.0): Liste aus `<name>/connected` (verfügbar ab 2)
  und `<name>/status/<sn>/online` (`true`), `availability_mode: all`; ein
  Batteriepack folgt seiner Einheit. Bis 0.9.x: `<root>/bridge/status`.
- **Batterie-Packs als Sub-Devices:** jedes Pack ist ein eigenes HA-Gerät
  (`identifiers: <identity-root>_<sn>_pack_<packSn>`), per `via_device` unter dem
  Hauptgerät verschachtelt; Haupt-Properties bleiben am Hauptgerät.
- **Reiche Geräte-Registry:** Hauptgerät mit `serial_number`, `model_id`
  (= `product`, z. B. `solarFlow2400AC`) und `configuration_url` (lokale
  Geräte-IP); Pack mit `serial_number` und `sw_version` (Pack-`softVersion`).
- **Virtuelle Switches** (`internal/virtual`): `charge_active` / `discharge_active`
  sind synthetische HA-Switches ohne Backing-Property. ON schreibt ein
  Property-Set (`acMode` + `inputLimit`/`outputLimit` + `smartMode`), der State
  wird aus dem Report abgeleitet (`acMode==1 && inputLimit>0` bzw.
  `acMode==2 && outputLimit>0`). Limits via `CHARGE_ACTIVE_VALUE` /
  `DISCHARGE_ACTIVE_VALUE`. Sie laufen als synthetische Points durch denselben
  Publish-/Discovery-Pfad; nur der `/set`-Schreibweg ist im Coordinator
  sondergehandhabt.
- **Select-i18n:** `value_map` (en) liefert die Tokens, die auf dem Draht
  stehen; `value_map_de` (de) nur die Anzeige-Labels in der Discovery
  (`options` + `value_template`/`command_template` mappen Token ↔ Label). Der
  `set`-Rückweg nimmt den Token (Groß-/Kleinschreibung egal), beide Labels oder
  den Rohcode. Nur Labels werden lokalisiert, nie Topics/IDs/Codes/Werte.
- **HA-Caveat:** Home Assistant verschiebt bereits registrierte Entitäten nicht
  auf ein anderes Gerät und benennt `entity_id`s nicht um. Schema-Änderungen
  erfordern ein einmaliges Zurücksetzen: retained `homeassistant/.../config`
  leeren, dann neu publizieren.
- **Discovery-Format:** Geräte-basiert, ein retained Dokument je HA-Gerät unter
  `homeassistant/device/<node_id>/config` (node id = Device-Identifier). Es hat
  die 29 Einzel-Configs unter `homeassistant/<platform>/<unique_id>/config`
  ersetzt (ADR 0070 Phase 5, Schritt 5); `unique_id` und `default_entity_id`
  sind unverändert, deshalb bleiben Historie, Namen und Automationen erhalten.
  **Reihenfolge ist zwingend:** erst die alten Configs zurückziehen, dann das
  Dokument publizieren — Home Assistant verweigert sonst das Dokument und
  schreibt dazu nur eine `WARNING [mqtt.entity]`-Zeile.

## 5. Cloud-Login (App-Token-Weg)

1. **Token** aus der Zendure-App ist Base64; dekodiert ergibt
   `"<api_url>.<appKey>"` (Split am letzten `.`).
2. **Signatur-Login:** `POST {api_url}/api/ha/deviceList`, Body `{"appKey": …}`,
   Header `timestamp`, `nonce`, `clientid: zenHa`, `sign`, mit
   `sign = SHA1(HAKEY + concat(sortByKey({appKey,timestamp,nonce})) + HAKEY)`
   (Hex, Upper-Case; `HAKEY = "C*dafwArEOXK"`).
3. **Response:** `mqtt{clientId,url,username,password}` +
   `deviceList[]{deviceKey, snNumber, productKey, productModel}`.
4. **Cloud-MQTT (implementiert):** MQTT/TLS-Verbindung zu `url` (z. B.
   `mqtteu.zen-iot.com:8883`), subscribe `iot/{productKey}/{deviceId}/#`,
   eingehend `…/properties/report` → gleiches Datenmodell wie lokal. Steuerung
   via publish `iot/{productKey}/{deviceId}/properties/write` mit
   `{deviceId, messageId, timestamp, properties}`.

Login **und** Cloud-MQTT-Stream sind in `internal/zendure/cloud` real
implementiert (wiederverwendet den eigenen MQTT-Client + Lifecycle; Login
testbar via `zendure2mqtt-util cloud-login`).

**TLS-Hinweis:** Der Zendure-Cloud-Broker präsentiert ein **nicht
standardkonformes Zertifikat** (`x509: … not standards compliant`). Daher ist
die Zertifikatsprüfung per Default aus (`CLOUD_TLS_VERIFY: false`) — die
Verbindung bleibt TLS-verschlüsselt, nur unverifiziert; opt-in strikt via
`CLOUD_TLS_VERIFY: true`.

**Reconnect-Härtung:** Das Cloud-Backend fährt eine eigene, **ereignisgetriebene
stabilitätsbewusste** Reconnect-Schleife (statt der generischen Lifecycle):
neuer `TCPClient.ConnectionLost()`-Channel signalisiert Drops sofort; ein
gelegentlicher Drop wird in ~1 s aufgefangen, dauerhaftes Flapping per
Exponential-Backoff gedrosselt (1→2→…→30 s), Reset nach einer stabilen Session
(≥60 s). **Bestätigt:** der Zendure-Broker trennt diesen clientId **persistent
alle ~1 s** (Single-Session-Politik) → im Cloud-Modus kommt Telemetrie nur
tröpfchenweise; **für dieses Gerät ist der lokale Modus der empfohlene Weg.**

## 6. Roadmap

- **M0 — Gerüst:** ✓ Projekt-Setup, eigener MQTT-Client, gemeinsames Modell,
  lokales HTTP-Poll-Backend + Write, Katalog, Process, HA-Discovery,
  Coordinator, util-CLI, Tests, CI/Makefile/Dockerfile.
- **M1 — Lokal end-to-end:** ✓ gegen echten SolarFlow 2400 AC verifiziert
  (State-Publish, HA-Discovery inkl. Sub-Devices + i18n + korrekte Skalierung,
  Graceful-Offline). Schreibpfad (`/set`) implementiert, am Gerät noch nicht
  final getestet.
- **M2 — Komfort:** virtuelle Lade/Entlade-Switches ✓ (`internal/virtual`);
  mDNS-**Browser** + `discover`-CLI gebaut ✓ (noch nicht ins Backend als
  Auto-Discovery verdrahtet; dieses Gerät annonciert ohnehin kein mDNS);
  Diagnose-Web-UI ✓ (`internal/web` + `internal/state`, read-only embedded SPA).
  Offen ggf.: Live-Push (SSE) statt 5-s-Polling im UI, Schreibzugriff im UI.
- **M3 — Cloud:** ✓ TLS-Cloud-MQTT-Source/Controller; `CONNECTION: cloud` gegen
  echte Zendure-Cloud verifiziert (TLS-Connect, Subscribe, Telemetrie) inkl.
  gehärtetem, ereignisgetriebenem Reconnect mit stabilitätsbewusstem Backoff.
  Begrenzung: Zendure-Cloud trennt diesen clientId ~alle 1 s → lokal bevorzugt.
- **M4 — Release:** ✓ HA-Add-on (`addon/config.yaml` Options-Schema,
  `build.yaml`, `Dockerfile`, `DOCS.md`/`README.md`), Root-`repository.yaml`,
  `script/run.sh` (Options → `ZENDURE_*` + generierte `LOCAL_DEVICES`-Datei),
  Release-/Docker-/Add-on-CI. Offen: ersten Release-Tag schneiden (Multi-Arch-
  Image-Build läuft in CI auf Tag; lokal mangels Docker nicht gebaut),
  optional Add-on-Branding (`icon.png`/`logo.png`).

## 6.1 Sofortiges Re-Read nach Write

Ein erfolgreicher `/set` (Katalog-Property oder virtueller Switch) löst nach
`reReadDelay` (≈750 ms, damit das Gerät den Write übernimmt) ein One-Shot-`Read`
+ Republish aus — HA spiegelt die Änderung sub-Sekunde statt erst beim nächsten
Poll. Dafür hat `source.Source` eine `Read(ctx, dev)`-Methode: lokal ein
HTTP-`GET /properties/report`, Cloud (push-basiert) liefert `ErrNotImplemented`
und fällt auf Stream/Poll zurück. Best-effort: ein transienter Lesefehler wird
geloggt und vom periodischen Poll aufgefangen.

## 7. Offene Annahmen

- Der lokale Report kommt komplett pro GET → **eine** Poll-Kadenz (`REFRESH`);
  Gruppierung erfolgt rein über den Katalog.
- Cloud-Broker-Host kommt dynamisch aus der Login-Response (nicht hartkodiert).
- `HAKEY` / `clientid: zenHa` / `/api/ha/deviceList` stammen aus dem
  Home-Assistant-Integrationspfad der Zendure-App (siehe `../Zendure-HA`).
