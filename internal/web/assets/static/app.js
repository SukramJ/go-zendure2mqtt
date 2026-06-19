"use strict";

const GROUP_ORDER = ["now", "config", "static", "battery", "misc"];

async function getJSON(path) {
  const res = await fetch(path, { cache: "no-store" });
  if (!res.ok) throw new Error(path + ": " + res.status);
  return res.json();
}

function fmtUptime(s) {
  s = Math.max(0, Math.floor(s));
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60), sec = s % 60;
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${sec}s`;
  return `${sec}s`;
}

function ago(iso) {
  const sec = (Date.now() - new Date(iso).getTime()) / 1000;
  return fmtUptime(sec) + " ago";
}

function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const k in (attrs || {})) {
    if (k === "class") e.className = attrs[k];
    else e.setAttribute(k, attrs[k]);
  }
  for (const c of kids) e.append(c);
  return e;
}

function renderHealth(h) {
  document.getElementById("mqtt-dot").className = "dot " + (h.mqtt_connected ? "up" : "down");
  document.getElementById("meta").textContent =
    `${h.connection} · MQTT ${h.mqtt_server} ${h.mqtt_connected ? "✓" : "✗"} · ${h.devices} device(s) · up ${fmtUptime(h.uptime_seconds)}`;
  document.getElementById("version").textContent = h.version;
}

function valueCell(e) {
  const td = el("td", { class: "v" }, String(e.value));
  if (e.unit) td.append(el("span", { class: "unit" }, e.unit));
  return td;
}

function renderDevice(dev) {
  const card = el("div", { class: "device" });
  card.append(el("h2", {}, dev.sn,
    el("small", {}, [dev.product, dev.model].filter(Boolean).join(" · ") + " — " + ago(dev.updated_at))));

  // Bucket entries: by group, and battery additionally by pack serial.
  const buckets = {};
  for (const e of dev.entries) {
    const key = e.pack_sn ? `battery · ${e.pack_sn}` : e.group;
    (buckets[key] = buckets[key] || []).push(e);
  }
  const keys = Object.keys(buckets).sort((a, b) => {
    const ga = GROUP_ORDER.indexOf(a.split(" · ")[0]);
    const gb = GROUP_ORDER.indexOf(b.split(" · ")[0]);
    return (ga - gb) || a.localeCompare(b);
  });

  for (const key of keys) {
    const g = el("div", { class: "group" }, el("h3", {}, key));
    const tbl = el("table");
    for (const e of buckets[key].sort((a, b) => (a.name || a.topic).localeCompare(b.name || b.topic))) {
      tbl.append(el("tr", {}, el("td", { class: "k" }, e.name || e.topic), valueCell(e)));
    }
    g.append(tbl);
    card.append(g);
  }
  return card;
}

async function tick() {
  try {
    const [h, snap] = await Promise.all([getJSON("api/health"), getJSON("api/snapshot")]);
    renderHealth(h);
    const main = document.getElementById("devices");
    main.replaceChildren(...(snap.devices || []).map(renderDevice));
    document.getElementById("updated").textContent = "refreshed " + new Date().toLocaleTimeString();
  } catch (err) {
    document.getElementById("meta").textContent = "error: " + err.message;
    document.getElementById("mqtt-dot").className = "dot down";
  }
}

tick();
setInterval(tick, 5000);
