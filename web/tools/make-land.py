#!/usr/bin/env python3
"""Build public/status/land.json - the land mask behind the status page globe's dots.

Input: world-atlas land-50m.json (Natural Earth 1:50m land, public domain; world-atlas ISC, see
world-atlas-LICENSE.txt). Output: an equirectangular raster of RES-degree cells (land = the cell's
centre is on land), run-length encoded row by row from the north:

    {"v": 3, "kind": "mask", "res": RES, "w": 360/RES, "h": 180/RES, "rows": [[water, land, water, land, ...], ...]}

Each row alternates run lengths in cells, starting with water (0 when the row starts on land) and
leaving out the water at the end. The globe lays its dots along parallels at a spacing that suits its
size on screen and asks this mask which of them are on land, so the dots stay even at every size.

    python3 tools/make-land.py tools/land-50m.json public/status/land.json [res_deg]
"""
import json
import math
import sys

MASK_STEP = 0.05          # degrees between the parallels the outlines are cut at


def rings_from_topojson(topo):
    sx, sy = topo["transform"]["scale"]
    tx, ty = topo["transform"]["translate"]
    arcs = []
    for arc in topo["arcs"]:
        x = y = 0
        pts = []
        for dx, dy in arc:
            x += dx
            y += dy
            pts.append((x * sx + tx, y * sy + ty))
        arcs.append(pts)

    def ring(idx):
        out = []
        for i in idx:
            pts = arcs[i] if i >= 0 else arcs[~i][::-1]
            out.extend(pts if not out else pts[1:])
        return out

    rings = []
    for geom in topo["objects"]["land"]["geometries"]:
        polys = geom["arcs"] if geom["type"] == "MultiPolygon" else [geom["arcs"]]
        for poly in polys:
            for r in poly:
                rings.append(ring(r))
    return rings


def unwrap(ring):
    """Rings that cross the antimeridian jump from +180 to -180; make their longitudes continuous.
    Rings around a pole (Antarctica) don't close when unwrapped - those stay as they are: in plain
    lon/lat they are ordinary polygons whose bottom edge runs along the map's lower border."""
    out, off = [ring[0]], 0.0
    for (x1, _), (x2, y2) in zip(ring, ring[1:]):
        if x2 - x1 > 180:
            off -= 360
        elif x2 - x1 < -180:
            off += 360
        out.append((x2 + off, y2))
    closing = ring[0][0] - ring[-1][0]
    if abs(closing) > 180:
        off += 360 if closing < 0 else -360
    return out if abs(off) < 1e-6 else ring


def row_crossings(rings):
    """For every mask row (latitude band centre), the sorted crossing longitudes of each ring.
    Edges are walked once and dropped into the rows they span, so this stays fast at 1:50m."""
    nrows = int(round(180 / MASK_STEP))
    rows = [dict() for _ in range(nrows)]
    for ri, r in enumerate(rings):
        for (x1, y1), (x2, y2) in zip(r, r[1:] + r[:1]):
            if y1 == y2:
                continue
            lo, hi = min(y1, y2), max(y1, y2)
            k0 = max(0, int(math.ceil((lo + 90) / MASK_STEP - 0.5)))
            k1 = min(nrows - 1, int(math.floor((hi + 90) / MASK_STEP - 0.5)))
            for k in range(k0, k1 + 1):
                lat = -90 + (k + 0.5) * MASK_STEP
                if (y1 > lat) != (y2 > lat):
                    rows[k].setdefault(ri, []).append(x1 + (lat - y1) * (x2 - x1) / (y2 - y1))
    for row in rows:
        for xs in row.values():
            xs.sort()
    return rows


def build_mask(rows, res):
    """Each raster row's land runs. A ring's crossings pair up into the stretches inside it; counting
    how many rings cover each cell's centre, even-odd, leaves lakes out of the land around them."""
    w, h = int(round(360 / res)), int(round(180 / res))
    out, land = [], 0
    for r in range(h):
        lat = 90 - (r + 0.5) * res
        k = min(len(rows) - 1, max(0, int((lat + 90) / MASK_STEP)))
        cover = [0] * (w + 1)
        for xs in rows[k].values():
            for a, b in zip(xs[0::2], xs[1::2]):
                for off in (-360, 0, 360):              # unwrapped rings run past the antimeridian
                    lo, hi = a + off, b + off
                    if hi < -180 or lo > 180:
                        continue
                    c0 = max(0, math.ceil((lo + 180) / res - 0.5))
                    c1 = min(w - 1, math.floor((hi + 180) / res - 0.5))
                    if c1 >= c0:
                        cover[c0] += 1
                        cover[c1 + 1] -= 1
        runs, depth, cur, run = [], 0, 0, 0
        for c in range(w):
            depth += cover[c]
            if depth & 1 != cur:
                runs.append(run)
                run, cur = 0, depth & 1
            run += 1
        if cur:
            runs.append(run)
        land += sum(runs[1::2])
        out.append(runs)
    return w, h, out, land


def main():
    src, dst = sys.argv[1], sys.argv[2]
    res = float(sys.argv[3]) if len(sys.argv) > 3 else 0.1
    rings = [unwrap(r) for r in rings_from_topojson(json.load(open(src)))]
    w, h, data, land = build_mask(row_crossings(rings), res)
    with open(dst, "w") as f:
        json.dump({"v": 3, "kind": "mask", "res": res, "w": w, "h": h,
                   "src": "Natural Earth 1:50m land (public domain)", "rows": data}, f, separators=(",", ":"))
    print("raster", w, "x", h, "land cells", land)


if __name__ == "__main__":
    main()
