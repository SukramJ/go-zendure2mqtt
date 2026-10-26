// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ
//
// Embedded dashboard logic for go-zendure2mqtt. Plain ES module-free
// vanilla JS: load language + items + config once, then live-update from
// the SSE stream. No framework, no build step. Structure and helpers follow
// go-mtec2mqtt / go-keba2mqtt; what differs is the unit picker — one bridge
// serves several units, and every section shows the selected one.
//
// Localisation: the server reports the configured LANGUAGE via
// /api/config; the matching bundle in i18n/<lang>.json drives all static
// chrome (data-i18n attributes) and the dynamic strings via t()/tf().
// Value names and select labels are localised server-side.
"use strict";

// Group → page-section mapping. Battery-pack entries (pack_sn set) always
// go to the pack section; groups not listed here render under "System".
const GROUPS = {
  now: "live",
  evcc: "evcc",
  config: "system",
  static: "system",
  misc: "system",
};

const SECTION_EL = {
  live: () => document.getElementById("live-groups"),
  packs: () => document.getElementById("pack-groups"),
  evcc: () => document.getElementById("evcc-groups"),
  system: () => document.getElementById("system-groups"),
};

const THEME_KEY = "zendure-theme";
const DEVICE_KEY = "zendure-device";

// i18n + catalog state, loaded once.
let I18N = {};
let LANG = "en";
let LOCALE = "en-US";
let itemsByKey = {}; // item key -> writable item info
let items = []; // writable items, catalog order
let latest = { health: {}, snapshot: { devices: [] } };
let selected = null; // selected unit serial
let hosts = {}; // unit serial -> configured local host

// ---------- i18n ----------
function t(key) {
  return key in I18N ? I18N[key] : key;
}

// tf looks up key and substitutes {name} placeholders from params.
function tf(key, params) {
  let s = t(key);
  // Replacement is passed as a function, not a string: params[k] may be
  // user-supplied (e.g. a value typed into the write form), and
  // String.replace() would otherwise interpret "$&", "$$", "$1" etc. in a
  // string replacement as special patterns.
  for (const k in params) s = s.replace("{" + k + "}", () => params[k]);
  return s;
}

async function loadI18n(lang) {
  LANG = lang === "de" ? "de" : "en";
  LOCALE = LANG === "de" ? "de-DE" : "en-US";
  document.documentElement.lang = LANG;
  try {
    I18N = await fetchJSON("i18n/" + LANG + ".json");
  } catch (e) {
    I18N = {}; // fall back to raw keys / English HTML defaults
  }
  applyStaticI18n();
}

// applyStaticI18n fills every element carrying a data-i18n / data-i18n-title
// attribute from the loaded bundle. Missing keys leave the HTML default.
function applyStaticI18n() {
  document.querySelectorAll("[data-i18n]").forEach((el) => {
    const key = el.dataset.i18n;
    if (key in I18N) el.textContent = I18N[key];
  });
  document.querySelectorAll("[data-i18n-title]").forEach((el) => {
    const key = el.dataset.i18nTitle;
    if (key in I18N) el.title = I18N[key];
  });
}

// ---------- bootstrap ----------
document.addEventListener("DOMContentLoaded", init);

async function init() {
  initTheme();
  initNav();
  selected = storageGet(DEVICE_KEY);
  // Language first so all subsequent rendering is localised. Config is
  // otherwise non-critical, so a failure just leaves English defaults.
  let config = null;
  try {
    // Relative URL (no leading slash) so the SPA works both when served
    // directly and behind the Home-Assistant ingress path prefix.
    config = await fetchJSON("api/config");
  } catch (e) {
    /* non-critical */
  }
  await loadI18n((config && config.language) || "en");
  try {
    indexItems(await fetchJSON("api/items"));
  } catch (e) {
    toast(t("toast.itemsFail") + e.message, "err");
  }
  if (config) renderConfig(config);
  connectSSE();
}

function indexItems(list) {
  items = list || [];
  itemsByKey = {};
  for (const it of items) itemsByKey[it.key] = it;
}

// ---------- SSE ----------
function connectSSE() {
  const es = new EventSource("api/events");
  es.addEventListener("update", (ev) => {
    streamDot(true);
    try {
      render(JSON.parse(ev.data));
    } catch (e) {
      /* ignore malformed frame */
    }
  });
  es.onerror = () => streamDot(false); // EventSource auto-reconnects
}

// ---------- render ----------
function render(view) {
  latest = {
    health: view.health || {},
    snapshot: view.snapshot || { devices: [] },
  };
  const devices = latest.snapshot.devices || [];
  // Keep the choice while that unit exists; otherwise prefer one that has
  // reported over one only known to be unreachable.
  if (!devices.some((d) => d.sn === selected)) {
    const first = devices.find((d) => !isZeroTime(d.updated_at)) || devices[0];
    selected = first ? first.sn : null;
  }
  const dev = devices.find((d) => d.sn === selected) || null;

  renderHeader(latest.health, dev);
  renderChips(devices);
  renderHealth(latest.health);
  renderDevices(devices);
  renderProblems(latest.health.problems || []);
  renderGroups(dev);
  renderControls(dev);
  document.getElementById("last-update").textContent =
    t("footer.updated") + new Date().toLocaleTimeString(LOCALE);
}

function renderHeader(h, dev) {
  let title = "Zendure";
  const sub = [];
  if (dev) {
    title = "Zendure " + (dev.product || dev.model || "");
    sub.push("SN " + dev.sn);
    if (dev.model && dev.product && dev.model !== dev.product) sub.push(dev.model);
    if (dev.updated_at && !isZeroTime(dev.updated_at)) {
      sub.push(tf("group.age", { age: formatAge(ageSeconds(dev.updated_at)) }));
    }
  }
  document.getElementById("device-title").textContent = title.trim();
  document.getElementById("device-sub").textContent = sub.join("  ·  ");
  if (h.version) document.getElementById("version").textContent = "v" + h.version;

  const badge = document.getElementById("conn-badge");
  const map = {
    ok: ["badge-ok", t("status.ok")],
    degraded: ["badge-warn", t("status.degraded")],
    starting: ["badge-neutral", t("status.starting")],
  };
  const [cls, text] = map[h.status] || ["badge-neutral", h.status || "—"];
  badge.className = "badge " + cls;
  badge.textContent = text;
}

function renderChips(devices) {
  const host = document.getElementById("device-chips");
  host.hidden = devices.length < 2;
  host.innerHTML = "";
  if (host.hidden) return;
  for (const d of devices) {
    const chip = el("button", "chip" + (d.sn === selected ? " active" : ""));
    chip.type = "button";
    chip.appendChild(el("span", "dot " + onlineTone(d.online)));
    chip.appendChild(document.createTextNode(d.product ? d.product + " · " + d.sn : d.sn));
    chip.addEventListener("click", () => {
      selected = d.sn;
      storageSet(DEVICE_KEY, d.sn);
      render(latest);
    });
    host.appendChild(chip);
  }
}

function renderHealth(h) {
  const upstream = h.upstream === null || h.upstream === undefined
    ? tile(t("health.upstream"), t("val.unknown"), null, h.connection)
    : tile(t("health.upstream"), h.upstream ? t("val.connected") : t("val.disconnected"),
      h.upstream ? "ok" : "err", h.connection);
  const items = [
    tile(t("health.status"), h.status ? t("status." + h.status) : "—", statusTone(h.status)),
    upstream,
    tile(t("health.mqtt"), h.mqtt_connected ? t("val.connected") : t("val.disconnected"),
      h.mqtt_connected ? "ok" : "err", h.mqtt_server),
    tile(t("health.topic"), h.mqtt_topic || "—", null),
    tile(t("health.hass"), h.hass_enabled ? t("val.active") : t("val.off"), h.hass_enabled ? "ok" : null),
    tile(t("health.evcc"), h.evcc_enabled ? t("val.active") : t("val.off"), h.evcc_enabled ? "ok" : null),
    tile(t("health.uptime"), formatUptime(h.uptime_seconds), null),
    tile(t("health.devices"), String(h.devices || 0), null),
  ];
  setGrid("health-grid", items);
}

function renderDevices(devices) {
  const host = document.getElementById("device-grid");
  host.innerHTML = "";
  if (!devices.length) {
    host.appendChild(el("div", "empty", t("dev.none")));
    return;
  }
  for (const d of devices) {
    const tone = onlineTone(d.online);
    const item = el("div", "status-item" + (tone !== "neutral" ? " tone-" + tone : ""));
    item.appendChild(el("div", "label wrap", hosts[d.sn] ? d.sn + " · " + hosts[d.sn] : d.sn));
    const state = d.online === undefined || d.online === null
      ? t("val.unknown")
      : d.online ? t("val.online") : t("val.offline");
    item.appendChild(el("div", "value", state));
    const age = d.updated_at && !isZeroTime(d.updated_at)
      ? tf("group.age", { age: formatAge(ageSeconds(d.updated_at)) })
      : t("dev.noReport");
    item.appendChild(el("div", "label wrap", [d.product, age].filter(Boolean).join(" · ")));
    host.appendChild(item);
  }
}

function renderProblems(problems) {
  const card = document.getElementById("problems-card");
  const list = document.getElementById("problem-list");
  card.hidden = !problems.length;
  list.innerHTML = "";
  for (const p of problems) {
    const li = el("li");
    const head = el("div", "p-head");
    head.appendChild(el("span", "p-event", p.event));
    const when = new Date(p.at);
    head.appendChild(el("span", "muted", (p.sn ? p.sn + " · " : "") + when.toLocaleTimeString(LOCALE)));
    li.appendChild(head);
    if (p.detail) li.appendChild(el("span", "p-detail", p.detail));
    list.appendChild(li);
  }
}

// renderGroups buckets the selected unit's entries: by group, and pack
// entries by pack serial.
function renderGroups(dev) {
  for (const fn of Object.values(SECTION_EL)) fn().innerHTML = "";
  const entries = (dev && dev.entries) || [];
  const age = dev && dev.updated_at && !isZeroTime(dev.updated_at) ? ageSeconds(dev.updated_at) : null;

  const groups = {};
  const packs = {};
  for (const e of entries) {
    if (e.pack_sn) (packs[e.pack_sn] = packs[e.pack_sn] || []).push(e);
    else (groups[e.group] = groups[e.group] || []).push(e);
  }

  // Order: known groups first (GROUPS order), then any extras.
  const order = Object.keys(GROUPS).filter((g) => g in groups);
  for (const g of Object.keys(groups)) if (!order.includes(g)) order.push(g);
  for (const g of order) {
    const section = GROUPS[g] || "system";
    SECTION_EL[section]().appendChild(groupCard(groupLabel(g), groups[g], age));
  }
  for (const sn of Object.keys(packs).sort()) {
    SECTION_EL.packs().appendChild(groupCard(tf("pack.title", { sn }), packs[sn], age));
  }

  for (const fn of Object.values(SECTION_EL)) {
    if (!fn().children.length) {
      fn().innerHTML = `<div class="card empty">${t("group.empty")}</div>`;
    }
  }
}

function groupLabel(g) {
  const key = "group." + g;
  return key in I18N ? I18N[key] : prettify(g);
}

function groupCard(label, entries, age) {
  const card = el("div", "card group-card");
  const head = el("div", "group-head");
  head.appendChild(el("h3", null, label));
  if (age !== null) {
    head.appendChild(el("span", "group-age", tf("group.age", { age: formatAge(age) })));
  }
  card.appendChild(head);

  const grid = el("div", "status-grid");
  const sorted = [...entries].sort((a, b) =>
    entryLabel(a).localeCompare(entryLabel(b), LOCALE));
  if (!sorted.length) grid.appendChild(el("div", "empty", "—"));
  for (const e of sorted) grid.appendChild(valueTile(e));
  card.appendChild(grid);
  return card;
}

function valueTile(e) {
  const item = el("div", "status-item");
  item.appendChild(el("div", "label", entryLabel(e)));
  const v = el("div", "value");
  v.textContent = displayValue(e.topic, e.value);
  if (e.unit) {
    const u = el("span", "unit");
    u.textContent = e.unit;
    v.appendChild(u);
  }
  item.appendChild(v);
  return item;
}

function entryLabel(e) {
  return e.name || prettify(e.topic);
}

// ---------- control (writable items) ----------
// renderControls lists the writable items the selected unit reports. Rows
// are rebuilt only when the unit or its item set changes, so a value typed
// into a field survives the live updates in between.
let controlSig = "";
function renderControls(dev) {
  const host = document.getElementById("control-list");
  const values = dev ? currentValues(dev) : {};
  const present = items.filter((it) => it.key in values);
  const sig = (dev ? dev.sn : "") + "|" + present.map((it) => it.key).join(",");
  if (sig !== controlSig) {
    controlSig = sig;
    host.innerHTML = "";
    if (!present.length) {
      host.innerHTML = `<div class="empty">${t(dev ? "ctl.none" : "dev.none")}</div>`;
    }
    for (const it of present) host.appendChild(controlRow(it));
  }
  updateControlValues(values);
}

// currentValues maps a unit's top-level entries (no pack entries) by topic.
function currentValues(dev) {
  const out = {};
  for (const e of dev.entries || []) {
    if (!e.pack_sn) out[e.topic] = e.value;
  }
  return out;
}

function controlRow(it) {
  const row = el("div", "control-row");

  const name = el("div", "c-name");
  name.appendChild(el("strong", null, it.name || it.key));
  const sub = [it.key];
  if (it.unit) sub.push(it.unit);
  if (it.min !== undefined && it.max !== undefined) sub.push(it.min + "–" + it.max);
  if (it.confirm) sub.push(t("ctl.confirmed"));
  name.appendChild(el("small", null, sub.join(" · ")));
  row.appendChild(name);

  const current = el("span", "c-current");
  current.dataset.key = it.key;
  current.textContent = "—";
  row.appendChild(current);

  const ctrls = el("div", "c-ctrls");
  // A switch renders as an immediate on/off toggle.
  if (it.component === "switch") {
    const toggle = el("input");
    toggle.type = "checkbox";
    toggle.className = "c-switch";
    toggle.dataset.key = it.key;
    toggle.addEventListener("change", async () => {
      const value = toggle.checked;
      if (!confirmWrite(it, t(value ? "val.on" : "val.off"))) {
        toggle.checked = !value;
        return;
      }
      await writeValue(it, value);
    });
    ctrls.appendChild(toggle);
    row.appendChild(ctrls);
    return row;
  }

  let input;
  if (it.options && it.options.length) {
    input = el("select");
    for (const o of it.options) {
      const opt = el("option", null, o.label);
      opt.value = o.value; // the wire token the write path takes
      input.appendChild(opt);
    }
    input.dataset.key = it.key;
    input.className = "c-select";
    input.addEventListener("change", () => (input.dataset.touched = "1"));
  } else {
    input = el("input");
    input.type = "number";
    input.step = it.step !== undefined ? String(it.step) : "any";
    if (it.min !== undefined) input.min = String(it.min);
    if (it.max !== undefined) input.max = String(it.max);
    input.placeholder = it.unit ? tf("ctl.placeholderUnit", { unit: it.unit }) : t("ctl.placeholder");
  }

  const btn = el("button", "btn", t("btn.set"));
  btn.addEventListener("click", () => writeItem(it, input, btn));

  ctrls.appendChild(input);
  ctrls.appendChild(btn);
  row.appendChild(ctrls);
  return row;
}

function updateControlValues(values) {
  document.querySelectorAll(".c-switch[data-key]").forEach((cb) => {
    const k = cb.dataset.key;
    if (k in values) cb.checked = !!values[k];
  });
  // A select follows the unit until the user picks something else.
  document.querySelectorAll(".c-select[data-key]").forEach((sel) => {
    const k = sel.dataset.key;
    if (!(k in values) || sel === document.activeElement || sel.dataset.touched) return;
    sel.value = String(values[k]);
  });
  document.querySelectorAll(".c-current[data-key]").forEach((sp) => {
    const k = sp.dataset.key;
    if (!(k in values)) return;
    const it = itemsByKey[k];
    if (it && it.component === "switch") {
      sp.textContent = t("ctl.current") + t(values[k] ? "val.on" : "val.off");
      return;
    }
    sp.textContent = t("ctl.current") + displayValue(k, values[k]) + (it && it.unit ? " " + it.unit : "");
  });
}

// writeItem is the input+button flow: validate, confirm, disable, write.
async function writeItem(it, input, btn) {
  const value = input.value;
  if (value === "" || value === null || value === undefined) {
    toast(t("toast.enterValue"), "err");
    return;
  }
  if (!confirmWrite(it, displayValue(it.key, value) + (it.unit ? " " + it.unit : ""))) return;
  btn.disabled = true;
  try {
    await writeValue(it, value);
  } finally {
    btn.disabled = false;
    delete input.dataset.touched;
  }
}

// confirmWrite asks before an item the catalog marks as needing it: it
// changes how the unit operates or what it persists to flash.
function confirmWrite(it, shown) {
  if (!it.confirm) return true;
  return window.confirm(tf("ctl.confirm", { name: it.name || it.key, value: shown, sn: selected }));
}

// writeValue posts a single write for the selected unit and toasts the result.
async function writeValue(it, value) {
  try {
    const res = await fetch("api/write", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sn: selected, key: it.key, value }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || res.statusText);
    toast(tf("toast.set", { key: it.name || it.key, value: displayValue(it.key, value) }), "ok");
  } catch (e) {
    toast(t("toast.errorPrefix") + e.message, "err");
  }
}

// ---------- config ----------
function renderConfig(c) {
  const n = (c.local_devices || []).length;
  hosts = {};
  for (const d of c.local_devices || []) hosts[d.sn] = d.host;
  const conn = c.connection === "cloud"
    ? tile(t("cfg.connection"), "cloud", null,
      (c.cloud_token_set ? t("cfg.tokenSet") : t("cfg.tokenMissing")) +
      " · TLS " + (c.cloud_tls_verify ? t("val.on") : t("val.off")))
    : tile(t("cfg.connection"), "local", null, n ? tf("cfg.devices", { n }) : t("cfg.noDevices"));
  const items = [
    conn,
    tile(t("cfg.refresh"), c.refresh + " " + t("unit.seconds"), null),
    tile(t("cfg.mqtt"), c.mqtt_server + ":" + c.mqtt_port, null, c.mqtt_topic),
    tile(t("cfg.hass"), c.hass_enable ? t("val.active") : t("val.off"), null, c.hass_base_topic),
    tile(t("cfg.chargeActive"), c.charge_active_w + " W", null),
    tile(t("cfg.dischargeActive"), c.discharge_active_w + " W", null),
    tile(t("cfg.evcc"), c.evcc_enable ? t("val.active") : t("val.off"), null,
      c.evcc_enable && c.evcc_batterymode_timeout
        ? tf("cfg.evccTimeout", { s: c.evcc_batterymode_timeout })
        : null),
    tile(t("cfg.webAuth"), c.web_auth ? t("val.on") : t("val.off"), c.web_auth ? null : "warn"),
    tile(t("cfg.language"), c.language || "en", null),
    tile(t("cfg.debug"), c.debug ? t("val.on") : t("val.off"), null),
  ];
  setGrid("config-grid", items);
  // The evcc section only exists with the profile on.
  document.getElementById("sec-evcc").hidden = !c.evcc_enable;
  document.querySelector('.nav-item[data-target="sec-evcc"]').hidden = !c.evcc_enable;
}

// ---------- small helpers ----------
// displayValue renders a value, showing a select token as its label.
function displayValue(key, v) {
  const it = itemsByKey[key];
  if (it && it.options && typeof v === "string") {
    const o = it.options.find((o) => o.value === v);
    if (o) return o.label;
  }
  return fmtValue(v);
}

function fmtValue(v) {
  if (v === null || v === undefined || v === "") return "–";
  if (typeof v === "boolean") return v ? t("val.yes") : t("val.no");
  if (typeof v === "number") {
    if (Number.isInteger(v)) return v.toLocaleString(LOCALE);
    return v.toLocaleString(LOCALE, { maximumFractionDigits: 2 });
  }
  return String(v);
}

function statusTone(s) {
  return { ok: "ok", degraded: "warn", starting: null }[s] || null;
}

function onlineTone(online) {
  if (online === undefined || online === null) return "neutral";
  return online ? "ok" : "err";
}

function tile(label, value, tone, sub) {
  return { label, value: sub ? value + "  ·  " + sub : value, tone };
}

function setGrid(id, items) {
  const host = document.getElementById(id);
  host.innerHTML = "";
  for (const it of items) {
    const item = el("div", "status-item" + (it.tone ? " tone-" + it.tone : ""));
    item.appendChild(el("div", "label", it.label));
    item.appendChild(el("div", "value", it.value));
    host.appendChild(item);
  }
}

function isZeroTime(iso) {
  return !iso || iso.startsWith("0001-01-01");
}

function ageSeconds(iso) {
  return Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
}

function formatUptime(sec) {
  sec = Number(sec) || 0;
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return `${d} d ${h} h`;
  if (h) return `${h} h ${m} min`;
  if (m) return `${m} min`;
  return `${Math.floor(sec)} s`;
}

function formatAge(sec) {
  sec = Math.max(0, Math.floor(Number(sec) || 0));
  if (sec < 60) return sec + " s";
  if (sec < 3600) return Math.floor(sec / 60) + " min";
  return Math.floor(sec / 3600) + " h";
}

function prettify(key) {
  return String(key)
    .replace(/^.*\//, "")
    .replace(/[_\-]+/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined && text !== null) e.textContent = text;
  return e;
}

async function fetchJSON(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(res.status + " " + res.statusText);
  return res.json();
}

// storageGet / storageSet keep per-viewer conveniences; a browser without
// storage (private window, blocked site data) simply forgets them.
function storageGet(key) {
  try {
    return localStorage.getItem(key);
  } catch (e) {
    return null;
  }
}

function storageSet(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch (e) {
    /* not persisted */
  }
}

let toastTimer = null;
function toast(msg, kind) {
  const box = document.getElementById("toast");
  box.textContent = msg;
  box.className = "toast " + (kind || "ok");
  box.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (box.hidden = true), 3500);
}

function streamDot(live) {
  document.getElementById("stream-dot").classList.toggle("live", !!live);
}

// ---------- nav + theme ----------
function initNav() {
  const navItems = [...document.querySelectorAll(".nav-item")];
  const sections = navItems
    .map((i) => document.getElementById(i.dataset.target))
    .filter(Boolean);
  const obs = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (e.isIntersecting) {
          navItems.forEach((i) =>
            i.classList.toggle("active", i.dataset.target === e.target.id));
        }
      }
    },
    { rootMargin: "-40% 0px -55% 0px" }
  );
  sections.forEach((s) => obs.observe(s));
}

function initTheme() {
  const saved = storageGet(THEME_KEY);
  if (saved) document.documentElement.dataset.theme = saved;
  document.getElementById("theme-toggle").addEventListener("click", () => {
    const cur = document.documentElement.dataset.theme === "light" ? "dark" : "light";
    document.documentElement.dataset.theme = cur;
    storageSet(THEME_KEY, cur);
  });
}
