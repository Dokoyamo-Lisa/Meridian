"use strict";
/* Meridian status page - globe.
   A dotted Earth drawn with WebGL in one draw call, lit by the real day/night terminator, with the coastlines
   (Natural Earth 1:50m) drawn exactly over it. The dots are spread evenly (a Fibonacci lattice) at a spacing
   chosen for the globe's size on screen - a few pixels apart whether it is a small panel or full screen and
   zoomed in - and a 0.1 degree land mask decides which of them are land.
   On top, in a 2D overlay: an exact pin for each server city (dashed when the location is only approximate),
   thin great-circle arcs from each city to the panel that fade out towards the horizon, a pulse that runs
   along an arc whenever that city's servers report, and labels joined to their pins by leader lines.
   Per frame the CPU only sets a few uniforms and draws the coastlines and a handful of arcs; nothing is drawn
   while the globe is off screen or the tab is hidden, and it runs at 30 fps unless it is being dragged or
   turned to a place. Without WebGL the dots fall back to canvas 2D, a little further apart.

   const g = new LSGlobe(host, { landUrl, coastUrl, colors: () => ({...}), onHover(item, x, y), onSelect(place) });
   g.setPlaces({ hub: {lat, lon, label, sub} | null, places: [{key, lat, lon, label, sub, state, rate, approx}] });
   g.setClients([{key?, lat, lon, n, to}]);  g.pulse([key, ...]);  g.focus(key | null);  g.setColors();
   The wheel zooms whenever the pointer is over the globe (zooming out at full size passes the wheel on to the
   page); g.setZoomable(true) adds pinch zoom (full screen); g.zoomBy(f) / g.setZoom(z) step it;
   onEmptyClick() fires for a click on the globe that hits no marker.  */
(() => {
  const RAD = Math.PI / 180, TAU = Math.PI * 2;
  const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
  const wrap180 = (d) => ((((d + 180) % 360) + 360) % 360) - 180;
  const smooth = (a, b, x) => { const t = clamp((x - a) / (b - a), 0, 1); return t * t * (3 - 2 * t); };
  const easeInOut = (t) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
  function unit(lat, lon) {
    const a = lat * RAD, b = lon * RAD, c = Math.cos(a);
    return [c * Math.sin(b), Math.sin(a), c * Math.cos(b)];
  }
  const dot3 = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
  function strHash(str) {                            // a stable phase in [0, 1) for a key
    let x = 2166136261;
    for (let i = 0; i < str.length; i++) x = Math.imul(x ^ str.charCodeAt(i), 16777619);
    return ((x >>> 0) % 1000) / 1000;
  }
  function hash(x) {                                  // murmur3 finaliser -> [0, 1)
    x = Math.imul(x ^ (x >>> 16), 0x85ebca6b);
    x = Math.imul(x ^ (x >>> 13), 0xc2b2ae35);
    return ((x ^ (x >>> 16)) >>> 0) / 4294967296;
  }
  function slerp(p, q, t) {
    const d = Math.acos(clamp(dot3(p, q), -1, 1));
    if (d < 1e-6) return p.slice();
    const s = Math.sin(d), a = Math.sin((1 - t) * d) / s, b = Math.sin(t * d) / s;
    return [p[0] * a + q[0] * b, p[1] * a + q[1] * b, p[2] * a + q[2] * b];
  }
  function hexRGB(hex, fallback) {
    const m = /^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(String(hex || "").trim());
    return m ? [parseInt(m[1], 16) / 255, parseInt(m[2], 16) / 255, parseInt(m[3], 16) / 255] : fallback;
  }
  const rgba = (rgb, a) => `rgba(${Math.round(rgb[0] * 255)},${Math.round(rgb[1] * 255)},${Math.round(rgb[2] * 255)},${a})`;

  /* Where the sun is overhead (low-precision solar ephemeris, error well under a degree). */
  function sunVector(ms) {
    const d = ms / 86400000 - 10957.5;                    // days since J2000.0
    const g = (357.529 + 0.98560028 * d) * RAD;
    const q = 280.459 + 0.98564736 * d;
    const L = (q + 1.915 * Math.sin(g) + 0.02 * Math.sin(2 * g)) * RAD;
    const e = (23.439 - 0.00000036 * d) * RAD;
    const ra = Math.atan2(Math.cos(e) * Math.sin(L), Math.cos(L)) / RAD;
    const dec = Math.asin(Math.sin(e) * Math.sin(L)) / RAD;
    const gmst = (280.46061837 + 360.98564736629 * d) % 360;
    return unit(dec, wrap180(ra - gmst));
  }

  /* View rotation: the point (lat0, lon0) faces the viewer; x right, y up, z towards the viewer. */
  function rotation(lat0, lon0) {
    const sl = Math.sin(lon0 * RAD), cl = Math.cos(lon0 * RAD), sp = Math.sin(lat0 * RAD), cp = Math.cos(lat0 * RAD);
    return [cl, 0, -sl, -sp * sl, cp, -sp * cl, cp * sl, sp, cp * cl];   // row-major 3x3
  }
  const apply = (m, v) => [m[0] * v[0] + m[1] * v[1] + m[2] * v[2], m[3] * v[0] + m[4] * v[1] + m[5] * v[2],
    m[6] * v[0] + m[7] * v[1] + m[8] * v[2]];

  // Each dot is painted on the sphere - round by default, or a rounded rectangle whose long side follows
  // the parallel - and foreshortened like the surface itself, so dots near the rim turn into slivers.
  const VS = `
attribute vec3 aPos;
uniform mat3 uRot;
uniform vec2 uScale;
uniform vec2 uCenter;
uniform float uSize;
uniform mediump float uAspect;
uniform float uBack;
uniform float uIntro;
uniform vec3 uSun;
varying float vA;
varying float vDay;
varying float vSprite;
varying vec4 vInv;
varying float vAA;
void main() {
  vec3 a = aPos;
  vec3 p = uRot * a;
  float front = step(0.0, p.z);
  float al = front * smoothstep(0.0, 0.42, p.z) + (1.0 - front) * uBack * smoothstep(0.0, 0.7, -p.z);
  float rev = 1.0 - smoothstep(uIntro * 1.35 - 0.3, uIntro * 1.35, 1.0 - p.z);   // centre first, then outwards
  vA = al * rev;
  vDay = smoothstep(-0.09, 0.09, dot(a, uSun));
  vec3 ew = normalize(vec3(a.z, 0.0, -a.x) + vec3(1e-5, 0.0, 0.0));
  vec3 nw = cross(a, ew);
  float hh = 0.5 * uSize * mix(0.3, 1.0, rev);
  vec2 cE = (uRot * ew).xy * hh * uAspect;      // half-extent vectors on screen, device px, y up
  vec2 cN = (uRot * nw).xy * hh;
  float det = cE.x * cN.y - cE.y * cN.x;
  if (abs(det) < 0.02) { vA = 0.0; det = 1.0; }
  vInv = vec4(cN.y, -cN.x, -cE.y, cE.x) / det;
  vAA = 1.0 / max(0.35, hh * min(length((uRot * ew).xy), length((uRot * nw).xy)));
  float ext = max(abs(cE.x) + abs(cN.x), abs(cE.y) + abs(cN.y));
  vSprite = 2.0 * ext + 2.0;
  gl_PointSize = vSprite;
  gl_Position = vA < 0.003 ? vec4(2.0, 2.0, 2.0, 1.0) : vec4(uCenter + p.xy * uScale, 0.0, 1.0);
}`;
  const FS = `
precision mediump float;
uniform vec3 uDay;
uniform vec3 uNight;
uniform float uNightLum;
uniform float uCorner;
uniform float uAspect;
varying float vA;
varying float vDay;
varying float vSprite;
varying vec4 vInv;
varying float vAA;
void main() {
  vec2 d = (gl_PointCoord - 0.5) * vSprite;
  d.y = -d.y;
  vec2 uv = vec2(dot(vInv.xy, d), dot(vInv.zw, d));          // rectangle space: [-1, 1] inside
  vec2 q = abs(vec2(uv.x * uAspect, uv.y)) - (vec2(uAspect, 1.0) - uCorner);
  float dist = length(max(q, 0.0)) + min(max(q.x, q.y), 0.0) - uCorner;
  float a = vA * (1.0 - smoothstep(-vAA, vAA, dist)) * mix(uNightLum, 1.0, vDay);
  gl_FragColor = vec4(mix(uNight, uDay, vDay) * a, a);
}`;

  class LSGlobe {
    constructor(host, opts) {
      this.host = host;
      this.o = Object.assign({ landUrl: "/status/land.json", coastUrl: "/status/coast.json", colors: () => ({}), onHover: null, onSelect: null,
        reduced: matchMedia("(prefers-reduced-motion: reduce)").matches, back: 0.07, fps: 30,
        layout: "fib", dotPx: 4, dotRatio: 0.55, aspect: 1, corner: 1, maxZoom: 3.2, onEmptyClick: null }, opts);
      // layout: "fib" (even, honeycomb-like) or "rows" (along the parallels); dotPx: CSS px between
      // neighbouring dots at the globe's centre; dotRatio: dot height / spacing; aspect: dot width /
      // height; corner: 1 = round ends (with aspect 1, round dots)
      this.places = []; this.hub = null; this.clients = []; this.comets = []; this.pings = [];
      this.view = { lat: 38, lon: -165 }; this.base = { lat: 38, lon: -165 }; this.amp = 14;
      this.t0 = performance.now(); this.lastDraw = 0; this.lastTs = 0;
      this.drag = null; this.vel = null; this.idleUntil = 0; this.focusKey = null; this.focusUntil = 0;
      this.visible = true; this.W = 0; this.H = 0; this.R = 0; this.baseR = 0; this.dpr = 1;
      this.zoom = 1; this.zoomTo = 1; this.zoomable = false; this.touches = new Map(); this.pinch = null;
      this.sun = sunVector(Date.now()); this.sunT = Date.now();
      this.frame = this.frame.bind(this);

      host.classList.add("lsg");
      this.cv = document.createElement("canvas");
      this.cv.className = "lsg-gl";
      this.ov = document.createElement("canvas");
      this.ov.className = "lsg-ov";
      this.labels = document.createElement("div");
      this.labels.className = "lsg-labels";
      for (const el of [this.cv, this.ov]) {
        el.setAttribute("aria-hidden", "true");
        Object.assign(el.style, { position: "absolute", inset: "0", width: "100%", height: "100%", display: "block" });
      }
      host.append(this.cv, this.ov, this.labels);
      this.ctx = this.ov.getContext("2d");
      this.setColors();
      this.initGL();

      this.ro = new ResizeObserver(() => this.resize());
      this.ro.observe(host);
      this.io = new IntersectionObserver((es) => { this.visible = es[es.length - 1].isIntersecting; this.kick(); });
      this.io.observe(host);
      this.onVis = () => this.kick();
      document.addEventListener("visibilitychange", this.onVis);
      this.bindPointer();
      this.resize();
      this.load();
    }

    // ------------------------------------------------------------------ data
    async load() {
      try {
        const r = await fetch(this.o.landUrl, { credentials: "same-origin" });
        this.setLand(await r.json());
      } catch (_) {
        this.host.classList.add("lsg-nodata");
      }
      try {
        const r = await fetch(this.o.coastUrl, { credentials: "same-origin" });
        if (r.ok) this.setCoast(await r.json());
      } catch (_) { /* the coastlines are a refinement: the dots still show the land */ }
    }
    // coastlines: delta-encoded lon/lat polylines (1/q degree), kept as unit vectors for projection
    setCoast(c) {
      const q = c.q || 100;
      let total = 0;
      for (const l of c.lines) total += l.length >> 1;
      const pts = new Float32Array(total * 3), starts = new Uint32Array(c.lines.length), counts = new Uint32Array(c.lines.length);
      let j = 0;
      c.lines.forEach((l, li) => {
        starts[li] = j / 3;
        counts[li] = l.length >> 1;
        let x = l[0], y = l[1];
        for (let k = 0; k < l.length; k += 2) {
          if (k) { x += l[k]; y += l[k + 1]; }
          const v = unit(y / q, x / q);
          pts[j++] = v[0]; pts[j++] = v[1]; pts[j++] = v[2];
        }
      });
      this.coast = { pts, starts, counts };
      this.kick(true);
    }
    setLand(land) {
      if (land.kind !== "mask" || !Array.isArray(land.rows)) throw new Error("unknown land data");
      // each mask row as the columns where land starts and ends: a column is land when an odd number
      // of these edges lie at or before it
      const edges = land.rows.map((runs) => {
        const e = new Int32Array(runs.length);
        let c = 0;
        runs.forEach((n, i) => { c += n; e[i] = c; });
        return e;
      });
      this.mask = { res: land.res, w: land.w, h: land.h, edges };
      this.level = null;
      this.buildDots(this.levelFor());
      this.startIntro();
      this.kick(true);
    }
    // levelFor: the dot spacing (degrees) for the globe's current size - one of a fixed series, so that
    // zooming rebuilds the dots only now and then; the dots are never closer than 0.3 degrees
    levelFor() {
      const px = this.gl ? this.o.dotPx : this.o.dotPx * 1.5;
      const want = px / (Math.max(10, this.R) * RAD);
      const k = clamp(Math.round(Math.log(2.4 / want) / Math.log(1.16)), 0, 14);   // 2.4 deg .. 0.30 deg
      const s = 2.4 / Math.pow(1.16, k);
      // hysteresis: keep the current level until the size has clearly moved past the next one
      if (this.level && Math.abs(Math.log(want / this.level)) < 0.62 * Math.log(1.16)) return this.level;
      return s;
    }
    // isLand: the mask's answer for a point (degrees)
    isLand(lat, lon) {
      const M = this.mask;
      const e = M.edges[clamp(Math.floor((90 - lat) / M.res), 0, M.h - 1)];
      if (!e.length) return false;
      const col = clamp(Math.floor((lon + 180) / M.res), 0, M.w - 1);
      let lo = 0, hi = e.length;                                  // edges at or before col
      while (lo < hi) { const mid = (lo + hi) >> 1; if (e[mid] <= col) lo = mid + 1; else hi = mid; }
      return (lo & 1) === 1;
    }
    // buildDots spreads dots `s` degrees apart over the sphere and keeps the ones on land. "fib" is a
    // Fibonacci lattice: evenly spread like a honeycomb, with no rows or columns to form moire bands;
    // "rows" lays them along parallels (a dot matrix)
    buildDots(s) {
      if (!this.mask) return;
      const out = [];
      if (this.o.layout === "rows") {
        const nRows = Math.round(180 / s);
        for (let r = 0; r < nRows; r++) {
          const lat = 90 - ((r + 0.5) * 180) / nRows;
          const n = Math.max(1, Math.round((360 * Math.cos(lat * RAD)) / s));
          const y = Math.sin(lat * RAD), c = Math.cos(lat * RAD);
          for (let i = 0; i < n; i++) {
            const lon = -180 + ((i + 0.5) * 360) / n;
            if (this.isLand(lat, lon)) out.push(c * Math.sin(lon * RAD), y, c * Math.cos(lon * RAD));
          }
        }
      } else {
        const d = s * RAD, N = Math.round((4 * Math.PI) / (d * d * Math.sqrt(3) / 2)), g = Math.PI * (3 - Math.sqrt(5));
        for (let i = 0; i < N; i++) {
          const z = 1 - (2 * i + 1) / N, c = Math.sqrt(1 - z * z), b = (i * g) % TAU;
          const lon = (b > Math.PI ? b - TAU : b) / RAD;
          if (this.isLand(Math.asin(z) / RAD, lon)) out.push(c * Math.sin(b), z, c * Math.cos(b));
        }
      }
      this.pos = new Float32Array(out);
      this.count = out.length / 3;
      this.level = s;
      this.builtAt = performance.now();
      this.uploadDots();
    }
    introAt(ts) {                                           // 0 -> 1 as the land comes in
      if (this.o.reduced || this.introT0 === -1) return 1;
      if (this.introT0 == null) return 0;
      const t = clamp((ts - this.introT0) / 1700, 0, 1);
      return 1 - Math.pow(1 - t, 3);
    }
    phase(ts, delay, dur) {                                 // a later part of the intro, eased
      if (this.o.reduced || this.introT0 === -1) return 1;
      if (this.introT0 == null) return 0;
      const t = clamp((ts - this.introT0 - delay) / dur, 0, 1);
      return 1 - Math.pow(1 - t, 3);
    }
    startIntro() {
      // waits for the land and the first setPlaces (which may have no places at all)
      if (this.introT0 != null || !this.pos || !this.placesSet) return;
      this.introT0 = this.o.reduced ? -1 : performance.now();
      this.kick(true);
    }
    setPlaces({ hub, places }) {
      this.placesSet = true;
      this.hub = hub ? Object.assign({}, hub, { v: unit(hub.lat, hub.lon) }) : null;
      // the same place keeps its label spot and glow, so an update never makes it jump
      const old = new Map(this.places.map((p) => [p.key, p]));
      this.places = (places || []).map((p) => {
        const o = old.get(p.key);
        return Object.assign({}, p, { v: unit(p.lat, p.lon), side: o ? o.side : null, glow: o ? o.glow : undefined });
      });
      for (const p of this.places) {
        p.arc = null;
        if (!this.hub) continue;
        const ang = Math.acos(clamp(dot3(p.v, this.hub.v), -1, 1));
        if (ang < 2 * RAD) continue;
        const lift = 0.035 + 0.16 * (ang / Math.PI), n = Math.max(16, Math.round(ang / RAD / 2));
        p.arc = [];
        for (let i = 0; i <= n; i++) {
          const t = i / n, s = slerp(p.v, this.hub.v, t), h = 1 + lift * Math.sin(Math.PI * t);
          p.arc.push([s[0] * h, s[1] * h, s[2] * h]);
        }
      }
      // pulses in flight follow their place to its new data, or end with it
      const byKey = new Map(this.places.map((p) => [p.key, p]));
      this.comets = this.comets.filter((c) => byKey.get(c.p.key)?.arc).map((c) => Object.assign(c, { p: byKey.get(c.p.key) }));
      this.pings = this.pings.filter((g) => byKey.has(g.p.key)).map((g) => Object.assign(g, { p: byKey.get(g.p.key) }));
      if (this.hoverKey && !byKey.has(this.hoverKey) && !this.clients.some((c) => c.key === this.hoverKey)) this.hoverKey = null;
      if (this.focusKey && !byKey.has(this.focusKey)) this.focusKey = null;
      this.linkClients();
      this.fitView();
      this.renderLabels();
      this.startIntro();
      this.kick(true);
    }
    // clients: [{key?, lat, lon, n, to}] - `to` is the key of the place they use. Their keys live apart
    // from the places' ("client:..."), so hovering one never lights up a place, or every client.
    setClients(list) {
      this.clients = (list || []).map((c, i) => Object.assign({}, c, {
        key: "client:" + (c.key != null ? c.key : `${i}@${c.lat},${c.lon}>${c.to}`), v: unit(c.lat, c.lon),
      }));
      this.linkClients();
      this.kick(true);
    }
    // linkClients draws each client's arc to the place it uses; run again whenever the places change
    linkClients() {
      for (const o of this.clients) {
        o.arc = null;
        const p = this.places.find((x) => x.key === o.to);
        if (!p) continue;
        const ang = Math.acos(clamp(dot3(o.v, p.v), -1, 1));
        if (ang <= 1.5 * RAD) continue;
        const lift = 0.02 + 0.1 * (ang / Math.PI), n = Math.max(10, Math.round(ang / RAD / 3));
        o.arc = [];
        for (let i = 0; i <= n; i++) {
          const t = i / n, q = slerp(o.v, p.v, t), h = 1 + lift * Math.sin(Math.PI * t);
          o.arc.push([q[0] * h, q[1] * h, q[2] * h]);
        }
      }
    }
    updatePlace(key, patch) {
      const p = this.places.find((x) => x.key === key);
      if (!p) return;
      if (patch.sub != null) this.setLabel(key, patch.sub);       // re-measures the label only when it changed
      if (patch.rate != null) p.rate = patch.rate;
      if (patch.state && patch.state !== p.state) {
        p.state = patch.state;
        if (p.el) p.el.className = "lsg-label place " + p.state + (p.key === this.focusKey ? " on" : "");
        this.kick(true);
      }
    }
    pulse(keys) {
      if (this.o.reduced || document.hidden || !this.visible) return;
      const now = performance.now(), set = new Set(keys);
      for (const p of this.places) {
        if (!set.has(p.key) || p.state === "crit") continue;
        if (p.arc) this.comets.push({ p, t0: now + Math.random() * 350, dur: 1500 + 700 * (p.arc.length / 90) });
        const k = clamp(Math.log10(1 + (p.rate || 0) / 1024) / 5, 0, 1);      // 0 at idle .. 1 near 100 MB/s
        this.pings.push({ p, t0: now, dur: 2100, amp: 7 + 18 * k });
      }
      if (this.comets.length > 60) this.comets.splice(0, this.comets.length - 60);
      if (this.pings.length > 60) this.pings.splice(0, this.pings.length - 60);
      this.kick();
    }
    focus(key) {
      const p = key == null ? null : this.places.find((x) => x.key === key);
      this.focusKey = p ? p.key : null;
      this.focusUntil = p ? performance.now() + 9000 : 0;
      this.renderLabels();
      this.kick();
    }
    setReduced(v) { this.o.reduced = !!v; this.kick(true); }
    setZoomable(v) {
      this.zoomable = !!v;
      this.host.style.touchAction = v ? "none" : "pan-y";
      if (!v) this.setZoom(1);
    }
    setZoom(z) {
      this.zoomTo = clamp(z, 1, this.o.maxZoom);
      if (this.o.reduced) this.zoom = this.zoomTo;
      this.kick(true);
    }
    zoomBy(f) { this.setZoom(this.zoomTo * f); }
    zoomAt(f, clientX, clientY) {
      // zoom towards the point under the cursor: turn the view part of the way to it as the scale grows
      const before = this.zoomTo;
      this.setZoom(before * f);
      const r = this.host.getBoundingClientRect();
      const px = (clientX - r.left - this.cx) / this.R, py = -(clientY - r.top - this.cy) / this.R;
      const d2 = px * px + py * py;
      if (d2 >= 1 || !this.m || this.zoomTo <= before) return;
      const pz = Math.sqrt(1 - d2), m = this.m;           // inverse rotation = transpose
      const v = [m[0] * px + m[3] * py + m[6] * pz, m[1] * px + m[4] * py + m[7] * pz, m[2] * px + m[5] * py + m[8] * pz];
      const lat = Math.asin(clamp(v[1], -1, 1)) / RAD, lon = Math.atan2(v[0], v[2]) / RAD;
      const k = 1 - before / this.zoomTo;
      this.view.lat = clamp(this.view.lat + (lat - this.view.lat) * k, -70, 80);
      this.view.lon = wrap180(this.view.lon + wrap180(lon - this.view.lon) * k);
      this.idleUntil = performance.now() + 5000;
      this.dirty = true;
    }
    setColors() {
      const c = this.o.colors() || {};
      this.C = {
        day: hexRGB(c.day, [0.86, 0.9, 0.95]), night: hexRGB(c.night, [0.42, 0.5, 0.6]), nightLum: c.nightLum ?? 0.42,
        rim: c.rim || "rgba(255,255,255,.12)", halo: c.halo || null, arc: hexRGB(c.arc, [0.55, 0.72, 0.95]),
        accent: hexRGB(c.accent, [0.55, 0.75, 1]), hub: c.hub || "#e8edf3", crit: hexRGB(c.crit, [0.9, 0.35, 0.33]),
        warn: hexRGB(c.warn, [0.95, 0.7, 0.2]), client: hexRGB(c.client, [0.95, 0.83, 0.62]), sphere: c.sphere || null,
        back: c.back ?? this.o.back, page: c.page || "#07090d", coast: hexRGB(c.coast || c.day, [0.86, 0.9, 0.95]),
        coastA: c.coastA ?? 0.5, ink: c.ink || "#e6ebf1",
      };
      this.kick(true);
    }

    // ------------------------------------------------------------------ view
    fitView() {
      // the view that keeps every location and the hub furthest from the rim, then the sway it can afford
      const pts = this.places.map((p) => p.v).concat(this.hub ? [this.hub.v] : []);
      if (!pts.length) return;
      let best = null;
      for (let lat = -20; lat <= 65; lat += 2.5) {
        for (let lon = -180; lon < 180; lon += 2.5) {
          const c = unit(lat, lon);
          let mn = 1, sum = 0;
          for (const v of pts) { const z = dot3(c, v); mn = Math.min(mn, z); sum += z; }
          const score = 0.6 * (sum / pts.length) + Math.min(mn, 0.25) - 0.01 * Math.max(0, lat - 32) - (mn < 0.08 ? 5 : 0);
          if (!best || score > best.score) best = { lat, lon, score, mn };
        }
      }
      this.base = { lat: best.lat, lon: best.lon };
      this.amp = 0;
      for (const a of [18, 15, 12, 9, 6, 3]) {
        const ok = [-a, a].every((d) => {
          const c = unit(best.lat, best.lon + d);
          return pts.every((v) => dot3(c, v) > 0.08);
        });
        if (ok) { this.amp = a; break; }
      }
      if (!this.placed) { this.view = { lat: this.base.lat, lon: this.base.lon }; this.placed = true; }
    }
    target(ts) {
      if (this.focusKey && ts < this.focusUntil) {
        const p = this.places.find((x) => x.key === this.focusKey);
        if (p) return { lat: clamp(p.lat, -35, 55), lon: p.lon, k: 900 };
      }
      if (this.focusKey && ts >= this.focusUntil) { this.focusKey = null; this.renderLabels(); }
      if (this.zoomTo > 1.02) return { lat: this.view.lat, lon: this.view.lon, k: 1000 };
      if (this.o.reduced) return { lat: this.base.lat, lon: this.base.lon, k: 1400 };
      const t = (ts - this.t0) / 1000;
      return { lat: this.base.lat + 3 * Math.sin((TAU * t) / 131), lon: this.base.lon + this.amp * Math.sin((TAU * t) / 97), k: 2200 };
    }
    step(ts) {
      const dt = this.lastTs ? clamp(ts - this.lastTs, 0, 100) : 16;
      this.lastTs = ts;
      if (this.zoom !== this.zoomTo) {                       // eased zoom
        this.zoom += (this.zoomTo - this.zoom) * (1 - Math.exp(-dt / 140));
        if (Math.abs(this.zoomTo - this.zoom) < 0.002) this.zoom = this.zoomTo;
        this.R = this.baseR * this.zoom;
      }
      for (const p of this.places) {                         // the traffic glow eases towards the live rate
        const target = p.state === "crit" ? 0 : clamp(Math.log10(1 + (p.rate || 0) / 1024) / 5, 0, 1);
        p.glow = (p.glow ?? target) + (target - (p.glow ?? target)) * (1 - Math.exp(-dt / 600));
      }
      if (this.drag) return;
      if (this.vel && ts < this.idleUntil) {                 // inertia after a drag
        this.view.lon = wrap180(this.view.lon + this.vel.lon * dt);
        this.view.lat = clamp(this.view.lat + this.vel.lat * dt, -70, 80);
        const k = Math.exp(-dt / 650);
        this.vel.lon *= k; this.vel.lat *= k;
        if (Math.abs(this.vel.lon) + Math.abs(this.vel.lat) < 0.0005) this.vel = null;
        return;
      }
      if (ts < this.idleUntil) return;                        // the user just moved it: stay a moment
      const tg = this.target(ts), f = 1 - Math.exp(-dt / tg.k);
      this.view.lat += (tg.lat - this.view.lat) * f;
      this.view.lon = wrap180(this.view.lon + wrap180(tg.lon - this.view.lon) * f);
    }

    // ------------------------------------------------------------------ webgl
    initGL() {
      let gl = null;
      try {
        gl = this.cv.getContext("webgl", { alpha: true, premultipliedAlpha: true, antialias: false, depth: false,
          stencil: false, powerPreference: "low-power" });
      } catch (_) { gl = null; }
      this.gl = gl;
      if (!gl) return;
      const sh = (type, src) => {
        const s = gl.createShader(type);
        gl.shaderSource(s, src);
        gl.compileShader(s);
        if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s));
        return s;
      };
      try {
        const pr = gl.createProgram();
        gl.attachShader(pr, sh(gl.VERTEX_SHADER, VS));
        gl.attachShader(pr, sh(gl.FRAGMENT_SHADER, FS));
        gl.linkProgram(pr);
        if (!gl.getProgramParameter(pr, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(pr));
        this.pr = pr;
        this.u = {};
        for (const n of ["uRot", "uScale", "uCenter", "uSize", "uAspect", "uBack", "uIntro", "uSun", "uDay",
          "uNight", "uNightLum", "uCorner"]) {
          this.u[n] = gl.getUniformLocation(pr, n);
        }
        this.aPos = gl.getAttribLocation(pr, "aPos");
        this.buf = gl.createBuffer();
        gl.enable(gl.BLEND);
        gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);
      } catch (e) {
        console.warn("globe: WebGL setup failed, drawing with canvas 2D:", e && e.message);
        this.gl = null;
        return;
      }
      if (!this.boundLoss) {
        this.boundLoss = true;
        this.cv.addEventListener("webglcontextlost", (e) => { e.preventDefault(); this.gl = null; });
        this.cv.addEventListener("webglcontextrestored", () => { this.initGL(); this.uploadDots(); this.kick(true); });
      }
      this.uploadDots();
    }
    uploadDots() {
      const gl = this.gl;
      if (!gl || !this.pos) return;
      gl.bindBuffer(gl.ARRAY_BUFFER, this.buf);
      gl.bufferData(gl.ARRAY_BUFFER, this.pos, gl.STATIC_DRAW);
    }
    drawDots(m) {
      const gl = this.gl, C = this.C;
      // a new spacing once the globe has settled at a new size (not on every frame of a zoom)
      if (this.mask && this.zoom === this.zoomTo && performance.now() - this.builtAt > 240) {
        const want = this.levelFor();
        if (want !== this.level) this.buildDots(want);
      }
      const spacing = this.R * (this.level || 1) * RAD;               // CSS px between neighbouring dots
      const css = clamp(spacing * this.o.dotRatio, 0.9, 9);
      if (!gl) return this.drawDots2D(m, css);
      const size = css * this.glScale;
      gl.viewport(0, 0, this.cv.width, this.cv.height);
      gl.clearColor(0, 0, 0, 0);
      gl.clear(gl.COLOR_BUFFER_BIT);
      if (!this.pos) return;
      gl.useProgram(this.pr);
      gl.bindBuffer(gl.ARRAY_BUFFER, this.buf);
      gl.enableVertexAttribArray(this.aPos);
      gl.vertexAttribPointer(this.aPos, 3, gl.FLOAT, false, 0, 0);
      gl.uniformMatrix3fv(this.u.uRot, false, [m[0], m[3], m[6], m[1], m[4], m[7], m[2], m[5], m[8]]);
      gl.uniform2f(this.u.uScale, (2 * this.R) / this.W, (2 * this.R) / this.H);
      gl.uniform2f(this.u.uCenter, (2 * this.cx) / this.W - 1, 1 - (2 * this.cy) / this.H);
      gl.uniform1f(this.u.uSize, size);
      gl.uniform1f(this.u.uBack, C.back);
      gl.uniform3fv(this.u.uSun, this.sun);
      gl.uniform3fv(this.u.uDay, C.day);
      gl.uniform3fv(this.u.uNight, C.night);
      gl.uniform1f(this.u.uNightLum, C.nightLum);
      gl.uniform1f(this.u.uCorner, this.o.corner);
      gl.uniform1f(this.u.uAspect, this.o.aspect);
      gl.uniform1f(this.u.uIntro, this.introAt(performance.now()));
      gl.drawArrays(gl.POINTS, 0, this.count);
    }
    drawDots2D(m, size) {
      // no WebGL: squares with fillRect, front hemisphere only
      if (!this.pos) return;
      const ctx = this.ctx, P = this.pos, C = this.C, R = this.R, cx = this.cx, cy = this.cy;
      const dayCol = rgba(C.day, 1), nightCol = rgba(C.night, 1);
      for (let pass = 0; pass < 2; pass++) {
        ctx.fillStyle = pass ? dayCol : nightCol;
        for (let i = 0; i < P.length; i += 3) {
          const x = m[0] * P[i] + m[1] * P[i + 1] + m[2] * P[i + 2];
          const y = m[3] * P[i] + m[4] * P[i + 1] + m[5] * P[i + 2];
          const z = m[6] * P[i] + m[7] * P[i + 1] + m[8] * P[i + 2];
          if (z <= 0) continue;
          const day = this.sun[0] * P[i] + this.sun[1] * P[i + 1] + this.sun[2] * P[i + 2] > 0;
          if (day !== !!pass) continue;
          ctx.globalAlpha = smooth(0, 0.3, z) * (pass ? 1 : C.nightLum);
          const s = size * 1.2;
          ctx.fillRect(cx + x * R - s / 2, cy - y * R - s / 2, s, s);
        }
      }
      ctx.globalAlpha = 1;
    }

    // ------------------------------------------------------------------ frame
    resize() {
      const r = this.host.getBoundingClientRect();
      if (!r.width || !r.height) return;
      const dpr = Math.min(2, window.devicePixelRatio || 1);
      this.W = r.width; this.H = r.height; this.dpr = dpr;
      // the dots are always drawn at 2x and scaled down by the browser: on 1x screens this supersampling
      // is what keeps a lattice this fine from turning into moire rings (points cost next to nothing)
      const gw = Math.min(2048, Math.round(r.width * 2)), gh = Math.min(2048, Math.round(r.height * 2));
      if (this.cv.width !== gw) this.cv.width = gw;
      if (this.cv.height !== gh) this.cv.height = gh;
      this.glScale = gw / r.width;
      const bw = Math.min(2048, Math.round(r.width * dpr)), bh = Math.min(2048, Math.round(r.height * dpr));
      if (this.ov.width !== bw) this.ov.width = bw;
      if (this.ov.height !== bh) this.ov.height = bh;
      this.ctx.setTransform(bw / r.width, 0, 0, bh / r.height, 0, 0);
      this.cx = r.width / 2; this.cy = r.height / 2;
      this.baseR = Math.max(10, Math.min(r.width, r.height) / 2 / 1.13);
      this.R = this.baseR * this.zoom;
      this.kick(true);
    }
    kick(force) {
      if (force) this.dirty = true;
      if (this.raf || !this.visible || document.hidden || !this.W) return;
      this.raf = requestAnimationFrame(this.frame);
    }
    busy(ts) {
      return !!(this.drag || this.vel || this.pinch || this.zoom !== this.zoomTo || (this.focusKey && ts < this.focusUntil + 3000) ||
        (this.introT0 > 0 && ts - this.introT0 < 3600));
    }
    frame(ts) {
      this.raf = 0;
      if (!this.visible || document.hidden) return;
      const animating = !this.o.reduced || this.busy(ts) || this.comets.length || this.pings.length;
      const minGap = this.busy(ts) ? 0 : 1000 / this.o.fps - 3;
      if (this.dirty || ts - this.lastDraw >= minGap) {
        this.step(ts);
        this.draw(ts);
        this.lastDraw = ts;
        this.dirty = false;
      }
      if (animating) this.raf = requestAnimationFrame(this.frame);
    }
    project(m, v) {
      const p = apply(m, v);
      return { x: this.cx + p[0] * this.R, y: this.cy - p[1] * this.R, z: p[2], r2: p[0] * p[0] + p[1] * p[1] };
    }
    draw(ts) {
      if (Date.now() - this.sunT > 60000) { this.sun = sunVector(Date.now()); this.sunT = Date.now(); }
      const m = rotation(this.view.lat, this.view.lon);
      this.m = m;
      const ctx = this.ctx, C = this.C, R = this.R, cx = this.cx, cy = this.cy;
      ctx.clearRect(0, 0, this.W, this.H);
      // sphere: optional faint fill, a hairline rim and a thin halo just outside it
      if (C.sphere) { ctx.beginPath(); ctx.arc(cx, cy, R, 0, TAU); ctx.fillStyle = C.sphere; ctx.fill(); }
      if (C.halo) {
        const g = ctx.createRadialGradient(cx, cy, R * 0.985, cx, cy, R * 1.08);
        g.addColorStop(0, C.halo); g.addColorStop(1, "rgba(0,0,0,0)");
        ctx.fillStyle = g;
        ctx.beginPath(); ctx.arc(cx, cy, R * 1.08, 0, TAU); ctx.arc(cx, cy, R * 0.985, 0, TAU, true); ctx.fill();
      }
      this.drawDots(m);
      this.drawCoast(m, ts);
      ctx.beginPath(); ctx.arc(cx, cy, R, 0, TAU); ctx.strokeStyle = C.rim; ctx.lineWidth = 1; ctx.stroke();

      const now = ts;
      const arcsIn = (i) => this.phase(ts, 650 + i * 170, 1000);
      const markIn = (i) => this.phase(ts, 420 + i * 130, 760);

      // report links: server -> panel, drawn in one after another
      this.places.forEach((p, i) => {
        if (!p.arc) return;
        const t = arcsIn(i);
        if (t <= 0) return;
        const col = p.state === "crit" ? C.crit : C.arc;
        ctx.setLineDash(p.state === "crit" ? [3, 4] : []);
        this.strokeArc(m, p.arc, 0, t, (vis) => rgba(col, (p.state === "crit" ? 0.6 : 0.46) * vis), 1.25);
      });
      ctx.setLineDash([]);

      // admin: clients -> the server they use, with particles running along each link
      for (const c of this.clients) {
        if (!c.arc) continue;
        const hot = c.key === this.hoverKey;
        this.strokeArc(m, c.arc, 0, 1, (vis) => rgba(C.client, (hot ? 0.85 : 0.4) * vis), hot ? 1.6 : 1.15);
        const n = Math.min(3, 1 + Math.floor(Math.log2(c.n || 1))), seed = c.seed ?? (c.seed = strHash(c.key));
        for (let k = 0; k < n; k++) {
          const t = ((now / 2800) + seed + k / n) % 1;
          this.strokeArc(m, c.arc, Math.max(0, t - 0.08), t, (vis) => rgba(C.client, 0.75 * vis), 1.6);
          const hp = this.arcPoint(m, c.arc, t);
          if (hp.vis > 0) { ctx.beginPath(); ctx.arc(hp.x, hp.y, 1.8, 0, TAU); ctx.fillStyle = rgba(C.client, hp.vis); ctx.fill(); }
        }
      }

      // pulses: each report travels server -> panel
      this.comets = this.comets.filter((c) => now - c.t0 < c.dur);
      for (const c of this.comets) {
        const t = (now - c.t0) / c.dur;
        if (t < 0) continue;
        const e = easeInOut(t), a0 = Math.max(0, e - 0.2);
        const fade = Math.min(1, t * 6, (1 - t) * 5);
        for (let k = 0; k < 6; k++) {
          const s0 = a0 + ((e - a0) * k) / 6, s1 = a0 + ((e - a0) * (k + 1)) / 6;
          this.strokeArc(m, c.p.arc, s0, s1, (vis) => rgba(C.accent, 0.95 * fade * vis * ((k + 1) / 6)), 1.7);
        }
        const hp = this.arcPoint(m, c.p.arc, e);
        if (hp.vis > 0) { ctx.beginPath(); ctx.arc(hp.x, hp.y, 2.3, 0, TAU); ctx.fillStyle = rgba(C.accent, fade * hp.vis); ctx.fill(); }
      }

      // pings
      this.pings = this.pings.filter((g) => now - g.t0 < g.dur);
      for (const g of this.pings) {
        const q = this.project(m, g.p.v);
        if (q.z <= 0.02) continue;
        const t = (now - g.t0) / g.dur, r = 6 + g.amp * (1 - Math.pow(1 - t, 3));
        ctx.beginPath(); ctx.arc(q.x, q.y, r, 0, TAU);
        ctx.strokeStyle = rgba(C.accent, 0.6 * (1 - t) * smooth(0.02, 0.2, q.z)); ctx.lineWidth = 1.3; ctx.stroke();
      }

      // the panel
      if (this.hub) {
        const q = this.project(m, this.hub.v);
        this.hub.q = q;
        const mk = markIn(this.places.length);
        if (q.z > 0 && mk > 0) {
          const a = smooth(0, 0.2, q.z) * mk, s = 6 * (0.6 + 0.4 * mk);
          ctx.globalAlpha = a;
          ctx.beginPath(); ctx.arc(q.x, q.y, 6, 0, TAU); ctx.fillStyle = C.page; ctx.fill();
          ctx.save(); ctx.translate(q.x, q.y); ctx.rotate(Math.PI / 4);
          ctx.fillStyle = C.hub; ctx.fillRect(-s / 2, -s / 2, s, s);
          ctx.restore();
          ctx.beginPath(); ctx.arc(q.x, q.y, 9.5, 0, TAU); ctx.strokeStyle = C.hub; ctx.globalAlpha = 0.4 * a; ctx.lineWidth = 1; ctx.stroke();
          ctx.globalAlpha = 1;
        }
      }

      // server cities: an exact pin at the coordinates - a core on a halo of page colour, and a ring that is
      // dashed when the location is only approximate (from the IP database). Live traffic glows around it.
      this.places.forEach((p, i) => {
        const q = this.project(m, p.v);
        p.q = q;
        const mk = markIn(i);
        if (q.z <= 0 || mk <= 0) return;
        const a = smooth(0, 0.2, q.z) * mk;
        const col = p.state === "crit" ? C.crit : p.state === "warn" ? C.warn : C.accent;
        const hot = p.key === this.hoverKey || p.key === this.focusKey;
        const pop = 0.55 + 0.45 * mk + (mk < 1 ? Math.sin(mk * Math.PI) * 0.25 : 0);
        const k = p.glow || 0;
        if (p.state !== "crit" && (k > 0.01 || hot)) {
          const rg = (12 + 16 * k + (hot ? 6 : 0)) * pop;
          const gl = ctx.createRadialGradient(q.x, q.y, 0, q.x, q.y, rg);
          gl.addColorStop(0, rgba(col, (0.12 + 0.26 * k) * a)); gl.addColorStop(1, rgba(col, 0));
          ctx.fillStyle = gl; ctx.beginPath(); ctx.arc(q.x, q.y, rg, 0, TAU); ctx.fill();
        }
        ctx.globalAlpha = a;
        ctx.beginPath(); ctx.arc(q.x, q.y, 4.8 * pop, 0, TAU); ctx.fillStyle = C.page; ctx.fill();
        ctx.globalAlpha = 1;
        ctx.beginPath(); ctx.arc(q.x, q.y, 2.7 * pop, 0, TAU);
        if (p.state === "crit") { ctx.strokeStyle = rgba(col, a); ctx.lineWidth = 1.4; ctx.stroke(); }
        else { ctx.fillStyle = rgba(col, a); ctx.fill(); }
        ctx.setLineDash(p.approx ? [2.2, 2.2] : []);
        ctx.beginPath(); ctx.arc(q.x, q.y, (hot ? 9 : 7.2) * pop, 0, TAU);
        ctx.strokeStyle = rgba(col, (hot ? 0.95 : 0.62) * a); ctx.lineWidth = 1.15; ctx.stroke();
        ctx.setLineDash([]);
        if (hot) {                                             // a fine cross marks the exact point
          ctx.strokeStyle = rgba(col, 0.75 * a); ctx.lineWidth = 1;
          ctx.beginPath();
          for (const [dx, dy] of [[1, 0], [-1, 0], [0, 1], [0, -1]]) { ctx.moveTo(q.x + dx * 11, q.y + dy * 11); ctx.lineTo(q.x + dx * 15, q.y + dy * 15); }
          ctx.stroke();
        }
        p.mk = mk;
      });

      // admin: client locations
      for (const c of this.clients) {
        const q = this.project(m, c.v);
        c.q = q;
        if (q.z <= 0) continue;
        const a = smooth(0, 0.25, q.z), hot = c.key === this.hoverKey;
        const r = 2.4 + Math.min(3.2, Math.sqrt(c.n || 1)) + (hot ? 1 : 0);
        const gl = ctx.createRadialGradient(q.x, q.y, 0, q.x, q.y, r * 4);
        gl.addColorStop(0, rgba(C.client, 0.24 * a)); gl.addColorStop(1, rgba(C.client, 0));
        ctx.fillStyle = gl; ctx.beginPath(); ctx.arc(q.x, q.y, r * 4, 0, TAU); ctx.fill();
        ctx.globalAlpha = 0.85 * a;
        ctx.beginPath(); ctx.arc(q.x, q.y, r + 1.6, 0, TAU); ctx.fillStyle = C.page; ctx.fill();
        ctx.globalAlpha = 1;
        ctx.beginPath(); ctx.arc(q.x, q.y, r, 0, TAU); ctx.fillStyle = rgba(C.client, a); ctx.fill();
      }
      const leaders = this.placeLabels();
      ctx.lineWidth = 1;
      for (const l of leaders) {
        ctx.strokeStyle = l.color;
        ctx.beginPath(); ctx.moveTo(l.x0, l.y0); ctx.lineTo(l.x1, l.y1); ctx.stroke();
      }
    }
    drawCoast(m, ts) {
      const cs = this.coast;
      if (!cs) return;
      const intro = this.introAt(ts);
      if (intro <= 0) return;
      const ctx = this.ctx, R = this.R, cx = this.cx, cy = this.cy, P = cs.pts;
      const [m0, m1, m2, m3, m4, m5, m6, m7, m8] = m;
      ctx.save();
      ctx.lineWidth = Math.min(1.15, 0.6 + 0.2 * this.zoom);
      ctx.lineJoin = "round";
      ctx.strokeStyle = rgba(this.C.coast, this.C.coastA * intro);
      ctx.beginPath();
      for (let li = 0; li < cs.starts.length; li++) {
        let k = cs.starts[li] * 3, open = false;
        for (let n = cs.counts[li]; n > 0; n--, k += 3) {
          const x = P[k], y = P[k + 1], z = P[k + 2];
          if (m6 * x + m7 * y + m8 * z <= 0.015) { open = false; continue; }   // behind the globe
          const px = cx + (m0 * x + m1 * y + m2 * z) * R, py = cy - (m3 * x + m4 * y + m5 * z) * R;
          if (open) ctx.lineTo(px, py); else { ctx.moveTo(px, py); open = true; }
        }
      }
      ctx.stroke();
      ctx.restore();
    }
    arcPoint(m, arc, t) {
      const f = clamp(t, 0, 1) * (arc.length - 1), i = Math.min(arc.length - 2, Math.floor(f)), u = f - i;
      const a = arc[i], b = arc[i + 1];
      const v = [a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u, a[2] + (b[2] - a[2]) * u];
      const q = this.project(m, v);
      return { x: q.x, y: q.y, vis: this.arcVis(q, v) };
    }
    arcVis(q, v) {
      // by the ground under the point: an arc fades out as it nears the horizon (like the pins), rather
      // than running on past the rim into empty space
      return smooth(-0.02, 0.2, q.z / Math.max(1e-6, Math.hypot(v[0], v[1], v[2])));
    }
    strokeArc(m, arc, t0, t1, colorFor, width) {
      const ctx = this.ctx, n = arc.length - 1;
      const i0 = Math.floor(t0 * n), i1 = Math.ceil(t1 * n);
      let open = false, lastVis = -1;
      ctx.lineWidth = width;
      const pts = [];
      for (let i = i0; i <= i1; i++) {
        const t = clamp(i / n, t0, t1);
        const f = t * n, k = Math.min(n - 1, Math.floor(f)), u = f - k;
        const a = arc[k], b = arc[Math.min(n, k + 1)];
        const v = [a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u, a[2] + (b[2] - a[2]) * u];
        const q = this.project(m, v);
        pts.push({ x: q.x, y: q.y, vis: this.arcVis(q, v) });
      }
      // draw visible runs; the colour follows the mean visibility of the run's ends
      for (let i = 0; i < pts.length - 1; i++) {
        const a = pts[i], b = pts[i + 1], vis = Math.min(a.vis, b.vis);
        if (vis <= 0.02) { if (open) { ctx.stroke(); open = false; } continue; }
        const bucket = Math.round(vis * 4) / 4;
        if (!open || bucket !== lastVis) {
          if (open) ctx.stroke();
          ctx.beginPath(); ctx.moveTo(a.x, a.y);
          ctx.strokeStyle = colorFor(bucket);
          open = true; lastVis = bucket;
        }
        ctx.lineTo(b.x, b.y);
      }
      if (open) ctx.stroke();
    }

    // ------------------------------------------------------------------ labels
    renderLabels() {
      const host = this.labels;
      host.replaceChildren();
      const mk = (item, cls) => {
        const el = document.createElement("div");
        el.className = "lsg-label " + cls;
        el.style.opacity = "0";                 // hidden until the next frame places it next to its marker
        const b = document.createElement("b");
        b.textContent = item.label || "";
        el.append(b);
        if (item.sub) { const s = document.createElement("span"); s.textContent = item.sub; el.append(s); }
        host.append(el);
        return el;
      };
      if (this.hub) this.hub.el = mk(this.hub, "hub");
      for (const p of this.places) {
        p.el = mk(p, "place " + (p.state || "") + (p.key === this.focusKey ? " on" : ""));
      }
      for (const it of (this.hub ? [this.hub] : []).concat(this.places)) it.lw = 0;
    }
    // placeLabels puts each label next to its pin without covering another label or pin, trying the sides
    // nearest first and then a little further out; a leader line joins every label to its exact point.
    placeLabels() {
      const items = (this.hub ? [this.hub] : []).concat(this.places);
      const marks = items.filter((it) => it.q && it.q.z > 0.02).map((it) => it.q);
      const placed = [], leaders = [];
      const sides = ["r", "tr", "br", "t", "b", "l", "tl", "bl"];
      const mirror = { r: "l", l: "r", tr: "tl", tl: "tr", br: "bl", bl: "br", t: "t", b: "b" };
      for (const it of items) {
        const el = it.el, q = it.q;
        if (!el || !q) continue;
        const a = (q.z > 0 ? smooth(0.02, 0.22, q.z) : 0) * (it === this.hub ? this.phase(performance.now(), 900, 700) : (it.mk ?? 0));
        if (a <= 0.01) { if (el.style.opacity !== "0") el.style.opacity = "0"; it.side = null; continue; }
        if (!it.lw) { it.lw = el.offsetWidth; it.lh = el.offsetHeight; }
        const w = it.lw, h = it.lh;
        const at = (side, gap) => {
          const d = gap * 0.72;
          switch (side) {
            case "r": return [q.x + gap, q.y - h / 2];
            case "l": return [q.x - gap - w, q.y - h / 2];
            case "t": return [q.x - w / 2, q.y - gap - h];
            case "b": return [q.x - w / 2, q.y + gap];
            case "tr": return [q.x + d, q.y - d - h];
            case "tl": return [q.x - d - w, q.y - d - h];
            case "br": return [q.x + d, q.y + d];
            default: return [q.x - d - w, q.y + d];      // bl
          }
        };
        const pref = q.x >= this.cx ? sides : sides.map((x) => mirror[x]);
        const tries = [];
        if (it.side) tries.push(it.side);                  // keep the last good spot: no jitter
        for (const gap of [13, 24, 38]) for (const sd of pref) tries.push(sd + ":" + gap);
        let pick = null;
        for (const t of tries) {
          const [side, g] = t.split(":"), gap = Number(g);
          const [x, y] = at(side, gap), box = [x - 3, y - 2, x + w + 3, y + h + 2];
          if (box[0] < 0 || box[2] > this.W || box[1] < 0 || box[3] > this.H) continue;
          if (placed.some((b) => box[0] < b[2] && box[2] > b[0] && box[1] < b[3] && box[3] > b[1])) continue;
          if (marks.some((m) => m !== q && m.x > box[0] - 9 && m.x < box[2] + 9 && m.y > box[1] - 9 && m.y < box[3] + 9)) continue;
          pick = { key: side + ":" + gap, x, y, box };
          break;
        }
        if (!pick) { const [x, y] = at(pref[0], 13); pick = { key: pref[0] + ":13", x, y, box: null }; }
        if (pick.box) placed.push(pick.box);
        it.side = pick.key;
        el.style.opacity = a.toFixed(2);
        el.style.transform = `translate(${pick.x.toFixed(1)}px, ${pick.y.toFixed(1)}px)`;
        const side = pick.key.split(":")[0];
        if (el.dataset.side !== side) el.dataset.side = side;
        // the leader: from the pin's ring to the nearest point of the label
        const nx = clamp(q.x, pick.x, pick.x + w), ny = clamp(q.y, pick.y, pick.y + h);
        const dx = nx - q.x, dy = ny - q.y, len = Math.hypot(dx, dy);
        if (len > 10) {
          const r0 = it === this.hub ? 8 : 8.5;
          leaders.push({ x0: q.x + (dx / len) * r0, y0: q.y + (dy / len) * r0, x1: nx, y1: ny,
            color: rgba(it === this.hub ? hexRGB(this.C.hub, [0.9, 0.93, 0.96]) : this.C.coast, 0.5 * a) });
        }
      }
      return leaders;
    }
    setLabel(key, sub) {
      const it = key === "hub" ? this.hub : this.places.find((p) => p.key === key);
      if (!it || !it.el || it.sub === sub) return;
      it.sub = sub;
      const span = it.el.querySelector("span");
      if (span) span.textContent = sub;
      it.lw = 0;
    }

    // ------------------------------------------------------------------ pointer
    bindPointer() {
      const host = this.host;
      host.style.touchAction = "pan-y";
      let moved = 0, last = null;
      const pinchDist = () => { const [a, b] = [...this.touches.values()]; return Math.hypot(a.x - b.x, a.y - b.y); };
      host.addEventListener("pointerdown", (e) => {
        if (e.button !== 0) return;
        this.touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
        if (this.touches.size === 2 && this.zoomable) {          // second finger: pinch, not rotate
          this.drag = null;
          this.pinch = { d: pinchDist(), z: this.zoomTo };
          moved = 99;
          return;
        }
        this.drag = { x: e.clientX, y: e.clientY, lat: this.view.lat, lon: this.view.lon };
        last = { x: e.clientX, y: e.clientY, t: performance.now() };
        moved = 0; this.vel = null;
        try { host.setPointerCapture(e.pointerId); } catch (_) { /* pointer already gone */ }
        host.classList.add("dragging");
        this.kick();
      });
      host.addEventListener("pointermove", (e) => {
        if (this.touches.has(e.pointerId)) this.touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
        if (this.pinch && this.touches.size === 2) {
          this.setZoom(this.pinch.z * pinchDist() / Math.max(1, this.pinch.d));
          this.zoom = this.zoomTo;
          this.R = this.baseR * this.zoom;
          this.dirty = true;
          this.kick();
          return;
        }
        if (this.drag) {
          const dx = e.clientX - this.drag.x, dy = e.clientY - this.drag.y;
          moved = Math.max(moved, Math.abs(dx) + Math.abs(dy));
          const k = 1 / RAD / this.R;
          this.view.lon = wrap180(this.drag.lon - dx * k);
          this.view.lat = clamp(this.drag.lat + dy * k, -70, 80);
          const now = performance.now(), dt = Math.max(8, now - last.t);
          this.vel = { lon: (-(e.clientX - last.x) * k) / dt, lat: ((e.clientY - last.y) * k) / dt };
          last = { x: e.clientX, y: e.clientY, t: now };
          this.dirty = true;
          this.kick();
          return;
        }
        this.hoverAt(e);
      });
      const end = (e) => {
        this.touches.delete(e.pointerId);
        if (this.pinch) {
          if (this.touches.size < 2) { this.pinch = null; this.idleUntil = performance.now() + 5000; }
          return;
        }
        if (!this.drag) return;
        this.drag = null;
        host.classList.remove("dragging");
        this.idleUntil = performance.now() + 5000;
        if (performance.now() - last.t > 80) this.vel = null;
        if (moved < 5 && e.type === "pointerup") {
          this.vel = null; this.idleUntil = 0;
          const p = this.hit(e);
          if (p && !p.to) { if (this.o.onSelect) this.o.onSelect(p); }      // places only: a client is not a destination
          else if (!p && this.o.onEmptyClick) this.o.onEmptyClick(e);
        }
        this.kick();
      };
      host.addEventListener("pointerup", end);
      host.addEventListener("pointercancel", end);
      host.addEventListener("pointerleave", () => {
        if (!this.drag) this.setHover(null);
        // on the dashboard a zoomed-in globe eases back to the whole earth a moment after the pointer leaves
        clearTimeout(this.unzoomT);
        this.unzoomT = setTimeout(() => { if (!this.zoomable && !this.drag && this.zoomTo > 1) this.setZoom(1); }, 3500);
      });
      host.addEventListener("pointerenter", () => clearTimeout(this.unzoomT));
      host.addEventListener("dblclick", () => { this.vel = null; this.idleUntil = 0; this.focus(null); this.setZoom(1); });
      host.addEventListener("wheel", (e) => {
        const f = Math.exp(-e.deltaY * (e.deltaMode ? 0.05 : 0.0016));
        if (!this.zoomable && f < 1 && this.zoomTo <= 1.001) return;   // nothing to zoom out of: let the page scroll
        e.preventDefault();
        clearTimeout(this.unzoomT);
        this.zoomAt(f, e.clientX, e.clientY);
      }, { passive: false });
    }
    // hit finds the pin or client under the pointer: the nearest one drawn there (faded-in, on the
    // near side), within a finger's reach on touch screens; places win ties with clients
    hit(e) {
      const r = this.host.getBoundingClientRect(), x = e.clientX - r.left, y = e.clientY - r.top;
      const reach = e.pointerType === "touch" ? 24 : 16;
      let best = null, bd = reach * reach;
      for (const p of this.places) {
        if (!p.q || p.q.z <= 0.05 || (p.mk ?? 0) < 0.5) continue;
        const d = (p.q.x - x) ** 2 + (p.q.y - y) ** 2;
        if (d < bd) { bd = d; best = p; }
      }
      for (const c of this.clients) {
        if (!c.q || c.q.z <= 0.05) continue;
        const d = (c.q.x - x) ** 2 + (c.q.y - y) ** 2 + 30;
        if (d < bd) { bd = d; best = c; }
      }
      return best;
    }
    hoverAt(e) {
      const p = this.hit(e);
      this.setHover(p, e.clientX, e.clientY);
    }
    setHover(p, x, y) {
      const key = p ? p.key : null;
      this.host.classList.toggle("pointing", !!p);
      if (key !== this.hoverKey) { this.hoverKey = key; this.kick(true); }
      if (this.o.onHover) this.o.onHover(p, x, y);
    }

    destroy() {
      cancelAnimationFrame(this.raf);
      this.ro.disconnect();
      this.io.disconnect();
      document.removeEventListener("visibilitychange", this.onVis);
      this.host.replaceChildren();
    }
  }
  LSGlobe.sunVector = sunVector;
  window.LSGlobe = LSGlobe;
})();
