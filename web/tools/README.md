# Map data for the status page globe

Both files come from Natural Earth 1:50m land (public domain), through world-atlas' `land-50m.json`
(ISC licence, see `world-atlas-LICENSE.txt`). Regenerate them only when the look should change:

```bash
python3 tools/make-land.py tools/land-50m.json public/status/land.json 0.1        # land mask, 0.1 degree cells
python3 tools/make-coast.py tools/land-50m.json public/status/coast.json 0.05     # coastlines, 0.05 degree tolerance
```

At the globe's largest zoom 0.05 degree is under a pixel, so the coastlines are as exact as the screen.
The globe lays its own dots (a Fibonacci lattice, a few pixels apart at any size) and asks the land
mask which of them are on land, so the mask only needs to be finer than the closest dots (0.3 degree).
