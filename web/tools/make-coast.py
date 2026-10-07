#!/usr/bin/env python3
"""Build public/status/coast.json - the coastlines drawn over the status page globe's dots.

Input: world-atlas land-50m.json (Natural Earth 1:50m land, public domain; world-atlas ISC, see
world-atlas-LICENSE.txt). The land's outlines are simplified (Douglas-Peucker, TOL degrees),
split where they cross the antimeridian, quantised to 1/Q degree and delta-encoded:

    {"v": 1, "kind": "coast", "q": Q, "lines": [[lon0, lat0, dlon1, dlat1, ...], ...]}

    python3 tools/make-coast.py tools/land-50m.json public/status/coast.json [tolerance_deg]
"""
import json
import sys

Q = 100


def arcs_from_topojson(topo):
    sx, sy = topo["transform"]["scale"]
    tx, ty = topo["transform"]["translate"]
    out = []
    for arc in topo["arcs"]:
        x = y = 0
        pts = []
        for dx, dy in arc:
            x += dx
            y += dy
            pts.append((x * sx + tx, y * sy + ty))
        out.append(pts)
    return out


def split_antimeridian(pts):
    lines, cur = [], [pts[0]]
    for a, b in zip(pts, pts[1:]):
        if abs(b[0] - a[0]) > 180:
            lines.append(cur)
            cur = [b]
        else:
            cur.append(b)
    lines.append(cur)
    return [l for l in lines if len(l) > 1]


def simplify(pts, tol):
    """Douglas-Peucker, iterative."""
    if len(pts) < 3:
        return pts
    keep = [False] * len(pts)
    keep[0] = keep[-1] = True
    stack = [(0, len(pts) - 1)]
    while stack:
        i, j = stack.pop()
        (x1, y1), (x2, y2) = pts[i], pts[j]
        dx, dy = x2 - x1, y2 - y1
        n2 = dx * dx + dy * dy
        best, bi = -1.0, -1
        for k in range(i + 1, j):
            px, py = pts[k]
            if n2 == 0:
                d = (px - x1) ** 2 + (py - y1) ** 2
            else:
                t = max(0.0, min(1.0, ((px - x1) * dx + (py - y1) * dy) / n2))
                d = (px - x1 - t * dx) ** 2 + (py - y1 - t * dy) ** 2
            if d > best:
                best, bi = d, k
        if best > tol * tol:
            keep[bi] = True
            stack += [(i, bi), (bi, j)]
    return [p for p, k in zip(pts, keep) if k]


def main():
    src, dst = sys.argv[1], sys.argv[2]
    tol = float(sys.argv[3]) if len(sys.argv) > 3 else 0.05
    lines, points = [], 0
    for arc in arcs_from_topojson(json.load(open(src))):
        for part in split_antimeridian(arc):
            s = simplify(part, tol)
            # tiny islands add noise, not information, at the globe's size
            xs, ys = [p[0] for p in s], [p[1] for p in s]
            if len(s) < 3 and max(max(xs) - min(xs), max(ys) - min(ys)) < 0.3:
                continue
            q = [(round(x * Q), round(y * Q)) for x, y in s]
            enc = [q[0][0], q[0][1]]
            for (x1, y1), (x2, y2) in zip(q, q[1:]):
                if (x1, y1) != (x2, y2):
                    enc += [x2 - x1, y2 - y1]
            if len(enc) >= 4:
                lines.append(enc)
                points += len(enc) // 2
    with open(dst, "w") as f:
        json.dump({"v": 1, "kind": "coast", "q": Q, "src": "Natural Earth 1:50m land (public domain)", "lines": lines},
                  f, separators=(",", ":"))
    print("lines", len(lines), "points", points)


if __name__ == "__main__":
    main()
