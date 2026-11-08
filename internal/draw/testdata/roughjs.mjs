// Generates internal/draw/testdata/roughjs.json: rough.js 4.6.6's output for
// the generator calls desk ports, so the Go port can be checked against it.
import rough from 'roughjs/bundled/rough.esm.js';
import { writeFileSync } from 'fs';

const gen = rough.generator();
const cases = [];
const opts = (c) => {
  const o = { seed: c.seed, roughness: c.roughness, strokeWidth: c.sw, hachureGap: c.sw * 4, fillWeight: c.sw / 2,
    preserveVertices: !!c.preserve, disableMultiStroke: !!c.single };
  if (c.curveFitting !== undefined) o.curveFitting = c.curveFitting;
  if (c.fill) { o.fill = '#ff0000'; o.fillStyle = c.fill; }
  return o;
};
const add = (c, d) => {
  c.sets = d.sets.map((s) => ({ type: s.type, ops: s.ops.map((op) => [op.op, ...op.data]) }));
  cases.push(c);
};

for (const [seed, roughness] of [[1, 1], [123456, 2], [987654321, 1.5], [42, 0.5]]) {
  const base = { seed, roughness, sw: 2 };
  for (const [x1, y1, x2, y2] of [[10, 20, 300, 80], [0, 0, 700, 0], [5, 5, 8, 9], [100, 100, 100, 400]]) {
    for (const preserve of [false, true]) {
      for (const single of [false, true]) {
        const c = { ...base, call: 'line', pts: [[x1, y1], [x2, y2]], preserve, single };
        add(c, gen.line(x1, y1, x2, y2, opts(c)));
      }
    }
  }
  const zig = [[0, 0], [80, 40], [160, -20], [260, 60]];
  { const c = { ...base, call: 'linearPath', pts: zig }; add(c, gen.linearPath(zig, opts(c))); }
  const poly = [[0, 0], [120, 10], [140, 90], [20, 110], [-10, 50]];
  for (const fill of ['solid', 'hachure', 'cross-hatch']) {
    const c = { ...base, call: 'polygon', pts: poly, fill }; add(c, gen.polygon(poly, opts(c)));
    const r = { ...base, call: 'rectangle', box: [10, 20, 160, 90], fill }; add(r, gen.rectangle(10, 20, 160, 90, opts(r)));
  }
  for (const [w, h, fit] of [[200, 120, 1], [40, 300, 0.95], [12, 12, 0.95]]) {
    for (const fill of [undefined, 'solid', 'hachure']) {
      const c = { ...base, call: 'ellipse', box: [100, 80, w, h], curveFitting: fit, fill };
      add(c, gen.ellipse(100, 80, w, h, opts(c)));
    }
  }
  const wave = [[0, 0], [60, 80], [140, -30], [220, 40], [300, 0]];
  for (const pts of [wave, wave.slice(0, 2), wave.slice(0, 3), [[0, 0], [100, 0], [100, 100], [0, 0]]]) {
    for (const single of [false, true]) {
      const c = { ...base, call: 'curve', pts, single, fill: pts.length === 4 ? 'solid' : undefined };
      add(c, gen.curve(pts, opts(c)));
    }
  }
  // Excalidraw's rounded rectangle path (r 24) and a rounded diamond's
  const d = 'M 24 0 L 176 0 Q 200 0, 200 24 L 200 96 Q 200 120, 176 120 L 24 120 Q 0 120, 0 96 L 0 24 Q 0 0, 24 0';
  const dd = 'M 125 25 L 175 75 C 200 100 200 100 175 125 L 125 175 C 100 200 100 200 75 175 L 25 125 C 0 100 0 100 25 75 L 75 25 C 100 0 100 0 125 25';
  for (const [path, name] of [[d, 'rect'], [dd, 'diamond']]) {
    for (const fill of [undefined, 'solid']) {
      for (const preserve of [false, true]) {
        const c = { ...base, call: 'path', path: name, fill, preserve };
        add(c, gen.path(path, opts(c)));
      }
    }
  }
}
writeFileSync(process.argv[2], JSON.stringify(cases));
console.log(cases.length, 'cases');
