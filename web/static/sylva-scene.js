/* ====================================================================== *
 * Sylva — living world scene, scene-only entry.
 *
 * The moss root is grown, not photographed: a tapered tube swept along a
 * measured centreline, a second tube that loops over it to make the arch,
 * recursive offshoots, and ~190 000 instanced blades of moss planted on
 * whatever part of that surface faces the light.
 *
 * This is the scene half of the authored Sylva document, mounted as a
 * background. The landing-page chrome it originally carried — dock, cards,
 * headline, stats — is absent here, and every one of those code paths was
 * already guarded in the source (`if (!root) return`, empty querySelectorAll
 * loops), so nothing had to be cut to make it stand alone.
 *
 * DOM contract: #hero (sized), #scene (canvas inside it), #stage (the
 * 1600x880 reference frame the unit maths is written against).
 * ====================================================================== */
(function () {
  'use strict';

  var REDUCED = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  var pointer = { x: 0, y: 0 }, smooth = { x: 0, y: 0 };
  var heroEl = document.getElementById('hero');
  var lastX = null, lastY = null;
  var ticking = false, parOn = false;

  function startTick() {
    if (ticking) return;
    ticking = true;
    (function loop() { requestAnimationFrame(loop); tick(); })();
  }

  function tick() {
    if (parOn) {
      smooth.x += (pointer.x - smooth.x) * 0.055;
      smooth.y += (pointer.y - smooth.y) * 0.055;
      var nx = Math.round(smooth.x * 1000) / 1000, ny = Math.round(smooth.y * 1000) / 1000;
      if (nx !== lastX || ny !== lastY) {
        lastX = nx; lastY = ny;
        heroEl.style.setProperty('--px', nx);
        heroEl.style.setProperty('--py', ny);
      }
    }
    if (renderer && clock) renderFrame();
  }

  function startParallax() {
    startTick();
    if (REDUCED || parOn) return;
    parOn = true;

    window.addEventListener('pointermove', function (e) {
      if (e.pointerType === 'touch') return;
      pointer.x = (e.clientX / window.innerWidth) * 2 - 1;
      pointer.y = (e.clientY / window.innerHeight) * 2 - 1;
      var r = hero.getBoundingClientRect();
      ndc.x =  ((e.clientX - r.left) / r.width) * 2 - 1;
      ndc.y = -((e.clientY - r.top) / r.height) * 2 + 1;
    }, { passive: true });

    window.addEventListener('pointerleave', function () {
      pointer.x = pointer.y = 0; ndc.x = 10;
    });
  }

  var canvas   = document.getElementById('scene');
  var hero     = document.getElementById('hero');
  var stageEl  = document.getElementById('stage');
  var NARROW   = window.matchMedia('(max-width: 900px)');

  var ARCH   = { w: 1900, left: -180, top: 306, aspect: 2800 / 1377 };
  var ARCH_N = { w: 1120, left: -290, top: 555, aspect: 2800 / 1377 };
  var FAR    = { w: 1150, left:  -40, top: 320, aspect: 1600 /  757, z: -260 };
  var FAR_N  = { w:  780, left: -110, top: 600, aspect: 1600 /  757, z: -260 };

  var renderer, scene, camera;
  var nearGroup, farGroup, motes, shadowMesh, glowMesh;
  var W = 1, H = 1, DIST = 1400;
  var poleTex = null;
  var scanning = false, scanT = 0, scanMax = 3000;
  var SCAN_DUR = 3.4;
  var clock = null;
  var readyStarted = false;

  function ready() {
    if (readyStarted) return;
    readyStarted = true;
    void document.body.offsetHeight;
    document.body.classList.add('is-ready');
    startParallax();
  }

  var ndc = { x: 10, y: 10 };

  if (!window.THREE) { ready(); return; }

  /* deterministic noise — the same meadow grows on every reload */
  var rng = (function () {
    var a = 0x3f9a1c7b;
    return function () {
      a |= 0; a = (a + 0x6d2b79f5) | 0;
      var t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  })();
  function rand(lo, hi) { return lo + (hi - lo) * rng(); }
  function sstep(a, b, x) { var t = Math.min(Math.max((x - a) / (b - a), 0), 1); return t * t * (3 - 2 * t); }
  function clamp01(x) { return x < 0 ? 0 : (x > 1 ? 1 : x); }

  function hash2(x, y) {
    var n = Math.imul(x | 0, 374761393) + Math.imul(y | 0, 668265263);
    n = Math.imul(n ^ (n >>> 13), 1274126177);
    return ((n ^ (n >>> 16)) >>> 0) / 4294967296;
  }
  function vnoise(x, y) {
    var ix = Math.floor(x), iy = Math.floor(y), fx = x - ix, fy = y - iy;
    var ux = fx * fx * (3 - 2 * fx), uy = fy * fy * (3 - 2 * fy);
    var a = hash2(ix, iy), b = hash2(ix + 1, iy), c = hash2(ix, iy + 1), d = hash2(ix + 1, iy + 1);
    var t = a + (b - a) * ux;
    return t + ((c + (d - c) * ux) - t) * uy;
  }
  function fbm2(x, y) {
    var s = 0, amp = 0.5, nx, ny;
    for (var i = 0; i < 4; i++) {
      s += amp * vnoise(x, y);
      nx = 0.80 * x + 0.60 * y; ny = -0.60 * x + 0.80 * y;
      x = nx * 2.07 + 3.1; y = ny * 2.07 - 1.7; amp *= 0.5;
    }
    return s / 0.9375;
  }

  var UP = new THREE.Vector3(0, 1, 0);
  var TAU = Math.PI * 2;
  var BOXW = 10;

  function makeP(aspect) {
    var bh = BOXW / aspect;
    return function (fx, fy, z) {
      return new THREE.Vector3((fx - 0.5) * BOXW, (0.5 - fy) * bh, z || 0);
    };
  }

  function transportFrames(curve, segs) {
    var pts = [], tans = [], nrms = [], i;
    for (i = 0; i <= segs; i++) {
      pts.push(curve.getPointAt(i / segs));
      tans.push(curve.getTangentAt(i / segs).normalize());
    }
    var ref = Math.abs(tans[0].y) < 0.9 ? UP : new THREE.Vector3(1, 0, 0);
    nrms.push(new THREE.Vector3().crossVectors(tans[0], ref).normalize());
    for (i = 1; i <= segs; i++) {
      var axis = new THREE.Vector3().crossVectors(tans[i - 1], tans[i]);
      var n = nrms[i - 1].clone();
      if (axis.lengthSq() > 1e-12) {
        axis.normalize();
        n.applyAxisAngle(axis, Math.acos(Math.min(1, Math.max(-1, tans[i - 1].dot(tans[i])))));
      }
      nrms.push(n.normalize());
    }
    return { pts: pts, tans: tans, nrms: nrms };
  }

  function mossCap(p, n, steep) {
    var upness = n.y + n.z * (0.10 + 0.42 * steep) - n.x * (0.05 + 0.45 * steep);
    var fray = fbm2(p.x * 2.30 + 4.4, p.z * 2.30 - p.y * 1.90) - 0.5;
    var tongue = fbm2(p.x * 0.95 + 21.0, p.z * 0.95 - p.y * 0.80) - 0.5;
    var patch = fbm2(p.x * 0.52 + 9.3, p.z * 0.52 + p.y * 0.44);
    var c = sstep(0.16, 0.70, upness + fray * 0.40 + tongue * 0.52);
    return c * sstep(0.10, 0.50, patch);
  }
  function mossLump(p) {
    return 0.66 + 0.48 * fbm2(p.x * 2.4 - 2.2, p.z * 2.4 + p.y * 2.0)
                + 0.18 * fbm2(p.x * 7.3 + 5.1, p.z * 7.3 - p.y * 4.4) - 0.09;
  }

  function table(vals) {
    return function (t) {
      var x = clamp01(t) * (vals.length - 1);
      var i = Math.min(vals.length - 2, Math.floor(x));
      return vals[i] + (vals[i + 1] - vals[i]) * (x - i);
    };
  }

  var knot = function (t, a, b) {
    return 1 + a * Math.sin(t * 23.0 + 1.3) + b * Math.sin(t * 57.0 + 0.4) + b * 0.5 * Math.sin(t * 103.0 + 2.2);
  };

  function makeLimb(P, pts, opt) {
    var v3 = pts.map(function (q) { return P(q[0], q[1], q[2]); });
    var curve = new THREE.CatmullRomCurve3(v3, false, 'centripetal', 0.5);
    var rw = opt.rw, moss = opt.moss;
    if (opt.rt) {
      var rt = table(opt.rt);
      rw = function (t) { return rt(t) * 0.52 * knot(t, 0.05, 0.024); };
      moss = function (t) { return rt(t) * 0.88; };
    }
    return {
      curve: curve, segs: opt.segs, radial: opt.radial, rw: rw, moss: moss,
      blade: opt.blade || function (t) { return moss(t) * 0.055 + 0.014; },
      sink: opt.sink || 0, vScale: opt.vScale,
      fr: transportFrames(curve, opt.segs), len: curve.getLength()
    };
  }

  var _fp = new THREE.Vector3(), _ft = new THREE.Vector3(), _fn = new THREE.Vector3(), _fb = new THREE.Vector3();
  function limbFrame(L, t) {
    var f = clamp01(t) * L.segs;
    var i = Math.min(L.segs - 1, Math.floor(f)), a = f - i;
    _fp.copy(L.fr.pts[i]).lerp(L.fr.pts[i + 1], a);
    if (L.sink) _fp.y -= L.moss(t) * L.sink;
    _ft.copy(L.fr.tans[i]).lerp(L.fr.tans[i + 1], a).normalize();
    _fn.copy(L.fr.nrms[i]).lerp(L.fr.nrms[i + 1], a);
    _fn.addScaledVector(_ft, -_fn.dot(_ft)).normalize();
    _fb.crossVectors(_ft, _fn).normalize();
  }

  function limbSurface(L, t, th, outP, outN) {
    limbFrame(L, t);
    var steep = Math.min(1, Math.abs(_ft.y) * 1.15);
    var c = Math.cos(th), s = Math.sin(th);
    outN.set(_fn.x * c + _fb.x * s, _fn.y * c + _fb.y * s, _fn.z * c + _fb.z * s).normalize();
    var rw = L.rw(t);
    outP.copy(_fp).addScaledVector(outN, rw);
    var cap = mossCap(outP, outN, steep);
    var d = rw + L.moss(t) * cap * mossLump(outP);
    outP.copy(_fp).addScaledVector(outN, d);
    return cap;
  }

  function tessellate(L, bag) {
    var S = L.segs, R = L.radial;
    var base = bag.pos.length / 3;
    var grid = new Float32Array((S + 1) * (R + 1) * 3);
    var gnrm = new Float32Array((S + 1) * (R + 1) * 3);
    var caps = new Float32Array((S + 1) * (R + 1));
    var p = new THREE.Vector3(), n = new THREE.Vector3();
    var i, j, k;

    for (i = 0; i <= S; i++) {
      for (j = 0; j <= R; j++) {
        var cap = limbSurface(L, i / S, (j / R) * TAU, p, n);
        k = (i * (R + 1) + j) * 3;
        grid[k] = p.x; grid[k + 1] = p.y; grid[k + 2] = p.z;
        caps[i * (R + 1) + j] = cap;
      }
    }

    var a = new THREE.Vector3(), b = new THREE.Vector3();
    function get(i2, j2, out) {
      i2 = Math.min(S, Math.max(0, i2));
      j2 = (j2 + R) % R;
      var q = (i2 * (R + 1) + j2) * 3;
      return out.set(grid[q], grid[q + 1], grid[q + 2]);
    }

    var du = new THREE.Vector3(), dv = new THREE.Vector3();
    for (i = 0; i <= S; i++) {
      for (j = 0; j <= R; j++) {
        get(i + 1, j, a); get(i - 1, j, b); du.subVectors(a, b);
        get(i, j + 1, a); get(i, j - 1, b); dv.subVectors(a, b);
        n.crossVectors(dv, du);
        if (n.lengthSq() < 1e-12) { limbSurface(L, i / S, (j / R) * TAU, p, n); } else n.normalize();
        k = (i * (R + 1) + j) * 3;
        bag.pos.push(grid[k], grid[k + 1], grid[k + 2]);
        bag.nor.push(n.x, n.y, n.z);
        bag.inf.push(1 - Math.abs(2 * (j / R) - 1), (i / S) * L.vScale, caps[i * (R + 1) + j]);
        gnrm[k] = n.x; gnrm[k + 1] = n.y; gnrm[k + 2] = n.z;
      }
    }
    for (i = 0; i < S; i++) for (j = 0; j < R; j++) {
      var q0 = base + i * (R + 1) + j, q1 = q0 + R + 1;
      bag.idx.push(q0, q1, q0 + 1, q1, q1 + 1, q0 + 1);
    }
    L.grid = grid; L.gnrm = gnrm; L.gcaps = caps; L.S = S; L.R = R;
  }

  function plantBlades(L, count, bag) {
    var S = L.S, R = L.R, grid = L.grid, gn = L.gnrm, caps = L.gcaps;
    if (!grid) return 0;
    var cells = S * R, cdf = new Float64Array(cells), total = 0;
    var ax, ay, az, bx, by, bz, cx, cy, cz, i, j;

    for (i = 0; i < S; i++) for (j = 0; j < R; j++) {
      var q00 = (i * (R + 1) + j) * 3, q10 = q00 + 3, q01 = ((i + 1) * (R + 1) + j) * 3;
      ax = grid[q10] - grid[q00]; ay = grid[q10 + 1] - grid[q00 + 1]; az = grid[q10 + 2] - grid[q00 + 2];
      bx = grid[q01] - grid[q00]; by = grid[q01 + 1] - grid[q00 + 1]; bz = grid[q01 + 2] - grid[q00 + 2];
      cx = ay * bz - az * by; cy = az * bx - ax * bz; cz = ax * by - ay * bx;
      var area = Math.sqrt(cx * cx + cy * cy + cz * cz);
      var cap = 0.25 * (caps[i * (R + 1) + j] + caps[i * (R + 1) + j + 1] +
                        caps[(i + 1) * (R + 1) + j] + caps[(i + 1) * (R + 1) + j + 1]);
      total += area * cap * cap;
      cdf[i * R + j] = total;
    }
    if (total <= 0) return 0;

    var planted = 0;
    for (var b = 0; b < count; b++) {
      var target = rng() * total, lo = 0, hi = cells - 1;
      while (lo < hi) { var mid = (lo + hi) >> 1; if (cdf[mid] < target) lo = mid + 1; else hi = mid; }
      i = (lo / R) | 0; j = lo - i * R;
      var u = rng(), v = rng();

      var i0 = i * (R + 1) + j, i1 = i0 + 1, i2 = i0 + R + 1, i3 = i2 + 1;
      var w0 = (1 - u) * (1 - v), w1 = u * (1 - v), w2 = (1 - u) * v, w3 = u * v;
      var cap2 = caps[i0] * w0 + caps[i1] * w1 + caps[i2] * w2 + caps[i3] * w3;
      if (cap2 < 0.05) continue;

      var p0 = i0 * 3, p1 = i1 * 3, p2 = i2 * 3, p3 = i3 * 3;
      var px = grid[p0] * w0 + grid[p1] * w1 + grid[p2] * w2 + grid[p3] * w3;
      var py = grid[p0 + 1] * w0 + grid[p1 + 1] * w1 + grid[p2 + 1] * w2 + grid[p3 + 1] * w3;
      var pz = grid[p0 + 2] * w0 + grid[p1 + 2] * w1 + grid[p2 + 2] * w2 + grid[p3 + 2] * w3;
      var nx = gn[p0] * w0 + gn[p1] * w1 + gn[p2] * w2 + gn[p3] * w3;
      var ny = gn[p0 + 1] * w0 + gn[p1 + 1] * w1 + gn[p2 + 1] * w2 + gn[p3 + 1] * w3;
      var nz = gn[p0 + 2] * w0 + gn[p1 + 2] * w1 + gn[p2 + 2] * w2 + gn[p3 + 2] * w3;
      var nl = Math.sqrt(nx * nx + ny * ny + nz * nz) || 1;

      bag.off.push(px, py, pz);
      bag.nrm.push(nx / nl, ny / nl, nz / nl);
      var stray = rng() < 0.06 ? rand(1.4, 1.9) : 1.0;
      bag.rnd.push(
        rng() * TAU,
        L.blade((i + v) / S) * (0.45 + 0.60 * cap2) * (0.58 + 0.50 * rng()) * stray,
        (rng() - 0.5) * 1.15,
        rng()
      );
      bag.aux.push(fbm2(px * 0.85 + 17.0, pz * 0.85 - py * 0.7) * 0.62 +
                   fbm2(px * 5.60 - 3.3, pz * 5.60 + py * 2.1) * 0.38);
      planted++;
    }
    return planted;
  }

  function growOffshoot(list, start, dir, len, r0, gen) {
    var side = new THREE.Vector3().crossVectors(dir, UP);
    if (side.lengthSq() < 1e-6) side.set(1, 0, 0);
    side.normalize();
    var up = new THREE.Vector3().crossVectors(side, dir).normalize();
    var bow = gen === 0 ? rand(0.10, 0.46) : rand(-0.34, 0.42);
    var kink = rand(-0.26, 0.26);

    function node(f, u2, k) {
      return start.clone()
        .addScaledVector(dir, len * f)
        .addScaledVector(up, len * u2)
        .addScaledVector(side, len * k);
    }
    var pts = [start.clone(), node(0.32, bow * 0.30, kink * 0.70), node(0.68, bow * 0.85, kink * 0.24), node(1.0, bow, kink * 0.44)];
    var curve = new THREE.CatmullRomCurve3(pts, false, 'centripetal', 0.5);
    var r1 = r0 * 0.52;
    var L = {
      curve: curve, segs: gen === 0 ? 16 : 11, radial: gen === 0 ? 9 : 7,
      rw: function (t) { return (r0 + (r1 - r0) * t) * (1 - 0.86 * sstep(0.90, 1.0, t)); },
      moss: function (t) { return (r0 + (r1 - r0) * t) * 0.95 * (1 - 0.55 * t); },
      blade: function (t) { return (r0 + (r1 - r0) * t) * 0.30 * (1 - 0.55 * t) + 0.035; },
      vScale: len * 7.0
    };
    L.fr = transportFrames(curve, L.segs);
    L.len = curve.getLength();
    list.push(L);

    if (gen >= 1) return;
    var kids = Math.round(rand(1, 2));
    for (var i = 0; i < kids; i++) {
      var tt = 0.34 + (i / Math.max(kids, 1)) * 0.5 + rand(-0.06, 0.06);
      var pt = curve.getPointAt(Math.min(tt, 0.98));
      var tan = curve.getTangentAt(Math.min(tt, 0.98)).normalize();
      var ax = new THREE.Vector3().crossVectors(tan, UP);
      if (ax.lengthSq() < 1e-6) ax.set(1, 0, 0);
      ax.normalize().applyAxisAngle(tan, rng() * TAU);
      var kdir = tan.clone().applyAxisAngle(ax, rand(0.45, 1.05)).addScaledVector(UP, 0.16).normalize();
      growOffshoot(list, pt, kdir, len * rand(0.50, 0.74), (r0 + (r1 - r0) * tt) * rand(0.58, 0.78), gen + 1);
    }
  }

  function buildNearRoot() {
    var P = makeP(ARCH.aspect);
    var limbs = [];

    limbs.push(makeLimb(P, [
      [-0.075, 0.845, -0.62], [0.000, 0.790, -0.38], [0.107, 0.695, 0.04],
      [0.196, 0.588, 0.28], [0.250, 0.566, 0.34], [0.304, 0.603, 0.22],
      [0.411, 0.733, -0.10], [0.500, 0.779, -0.28], [0.585, 0.742, -0.05],
      [0.696, 0.661, 0.20], [0.750, 0.672, 0.14], [0.850, 0.640, -0.08],
      [0.930, 0.626, -0.30], [1.030, 0.634, -0.55], [1.090, 0.638, -0.70]
    ], {
      segs: 300, radial: 26, vScale: 30,
      rt: [0.575, 0.590, 0.630, 0.680, 0.695, 0.615, 0.580, 0.480, 0.550, 0.550, 0.520], sink: 0.5
    }));

    var legRw   = table([0.30, 0.28, 0.26, 0.25, 0.24, 0.23, 0.22]);
    var legMoss = table([0.24, 0.24, 0.23, 0.22, 0.21, 0.20, 0.19]);
    limbs.push(makeLimb(P, [
      [0.532, 0.860, 0.20], [0.572, 0.700, 0.28], [0.612, 0.540, 0.34],
      [0.652, 0.390, 0.33], [0.690, 0.263, 0.26], [0.722, 0.180, 0.15], [0.752, 0.163, 0.02]
    ], {
      segs: 130, radial: 20, vScale: 22,
      rw: function (t) { return legRw(t) * knot(t, 0.05, 0.022); }, moss: legMoss
    }));

    var legR  = table([0.23, 0.25, 0.27, 0.30, 0.33, 0.36, 0.40]);
    var legRm = table([0.19, 0.20, 0.21, 0.22, 0.24, 0.25, 0.26]);
    limbs.push(makeLimb(P, [
      [0.706, 0.176, -0.02], [0.740, 0.158, 0.02], [0.772, 0.245, -0.08],
      [0.797, 0.400, -0.18], [0.816, 0.570, -0.22], [0.836, 0.760, -0.18],
      [0.858, 0.950, -0.08], [0.888, 1.180, 0.04]
    ], {
      segs: 150, radial: 20, vScale: 22,
      rw: function (t) { return legR(t) * knot(t, 0.05, 0.022); }, moss: legRm
    }));

    return limbs;
  }

  function buildFarRoot() {
    var P = makeP(FAR.aspect);
    return [makeLimb(P, [
      [-0.060, 0.880, -0.35], [0.100, 0.762, -0.05], [0.210, 0.698, 0.22],
      [0.300, 0.570, 0.30], [0.410, 0.467, 0.18], [0.500, 0.500, -0.05],
      [0.600, 0.622, -0.22], [0.720, 0.748, -0.26], [0.800, 0.788, -0.08],
      [0.900, 0.660, 0.14], [0.990, 0.454, 0.28]
    ], {
      segs: 220, radial: 20, vScale: 26,
      rt: [0.760, 0.900, 0.900, 0.960, 0.925, 0.950, 1.020, 1.020, 0.990, 1.100, 1.300], sink: 0.5
    })];
  }

  var LIGHT_GLSL = [
    'uniform vec3 uKeyDir, uKeyCol, uFillDir, uFillCol, uAmbCol, uHazeCol;',
    'uniform float uHaze, uFog, uMaskOn, uHazeLift;',
    'uniform vec4 uMask;',
    'vec3 litSurface(vec3 N, vec3 albedo, float ao){',
    '  float k = max(dot(N, uKeyDir), 0.0);',
    '  float f = max(dot(N, uFillDir), 0.0);',
    '  float sky = 0.5 + 0.5 * N.y;',
    '  return albedo * (uKeyCol * (0.09 + 1.05 * k) + uFillCol * (0.04 + 0.34 * f) + uAmbCol * (0.35 + 0.65 * sky)) * ao;',
    '}',
    'vec3 aerial(vec3 c, float h){',
    '  float amt = clamp(uFog + uHaze * smoothstep(0.05, 0.95, h), 0.0, 1.0);',
    '  float gain = smoothstep(0.003, 0.075, dot(c, vec3(0.30, 0.59, 0.11)));',
    '  return mix(c, uHazeCol, amt * mix(uHazeLift, 1.0, gain));',
    '}',
    'uniform vec3 uScanO;',
    'uniform float uScanR, uScanOn;',
    'bool unscanned(vec3 w, float lag){',
    '  if (uScanOn < 0.5) return false;',
    '  float wob = sin(w.y * 0.011 + w.x * 0.007) * 36.0 + sin(w.z * 0.021 + w.y * 0.013) * 17.0;',
    '  return distance(w, uScanO) > uScanR - lag + wob;',
    '}',
    'float maskAt(vec3 lp, float boxH){',
    '  if (uMaskOn < 0.5) return 1.0;',
    '  float e = 1.0 - smoothstep(uMask.x, uMask.y, lp.x);',
    '  float l = smoothstep(uMask.z, uMask.w, lp.y / boxH + 0.5);',
    '  return clamp(e * l, 0.0, 1.0);',
    '}'
  ].join('\n');

  var NOISE_GLSL = [
    'vec2 hash22(vec2 p){',
    '  p = vec2(dot(p, vec2(127.1, 311.7)), dot(p, vec2(269.5, 183.3)));',
    '  return -1.0 + 2.0 * fract(sin(p) * 43758.5453123);',
    '}',
    'float gnoise(vec2 p){',
    '  vec2 i = floor(p), f = fract(p);',
    '  vec2 u = f * f * (3.0 - 2.0 * f);',
    '  return mix(mix(dot(hash22(i + vec2(0,0)), f - vec2(0,0)),',
    '                 dot(hash22(i + vec2(1,0)), f - vec2(1,0)), u.x),',
    '             mix(dot(hash22(i + vec2(0,1)), f - vec2(0,1)),',
    '                 dot(hash22(i + vec2(1,1)), f - vec2(1,1)), u.x), u.y);',
    '}',
    'const mat2 ROT = mat2(0.80, 0.60, -0.60, 0.80);',
    'float gfbm(vec2 p){ float a = 0.5, s = 0.0; for (int i = 0; i < 5; i++){ s += a * gnoise(p); p = ROT * p * 2.03; a *= 0.5; } return s; }',
    'float ridged(vec2 p){ float a = 0.5, s = 0.0; for (int i = 0; i < 4; i++){ s += a * (1.0 - abs(gnoise(p) * 2.0)); p = ROT * p * 2.11; a *= 0.5; } return s; }'
  ].join('\n');

  var WIND_GLSL = [
    'uniform float uTime;',
    'uniform float uWind;',
    'vec3 windOffset(vec3 p){',
    '  float ph = p.x * 0.42 + p.y * 0.30 + p.z * 0.70;',
    '  float a = 0.030 * uWind;',
    '  return vec3((sin(uTime * 0.58 + ph) + 0.45 * sin(uTime * 1.37 + ph * 2.3)) * a,',
    '              sin(uTime * 0.79 + ph * 1.7) * a * 0.42,',
    '              sin(uTime * 0.51 + ph * 0.9) * a * 0.55);',
    '}'
  ].join('\n');

  function barkMaterial(cfg) {
    return new THREE.ShaderMaterial({
      uniforms: cfg.uniforms,
      extensions: { derivatives: true },
      vertexShader: WIND_GLSL + [
        'attribute vec3 inf;',
        'varying vec3 vN; varying vec3 vW; varying vec3 vInf; varying float vH; varying vec3 vL;',
        'uniform float uBoxH;',
        'void main(){',
        '  vInf = inf;',
        '  vN = normalize(normal);',
        '  vec3 p = position + windOffset(position) * (0.35 + 0.65 * inf.z);',
        '  vL = p;',
        '  vH = clamp(p.y / uBoxH + 0.5, 0.0, 1.0);',
        '  vec4 wp = modelMatrix * vec4(p, 1.0);',
        '  vW = wp.xyz;',
        '  gl_Position = projectionMatrix * viewMatrix * wp;',
        '}'
      ].join('\n'),
      fragmentShader: NOISE_GLSL + LIGHT_GLSL + [
        'precision highp float;',
        'uniform float uAlpha; uniform float uBoxH;',
        'varying vec3 vN; varying vec3 vW; varying vec3 vInf; varying float vH; varying vec3 vL;',
        'vec2 barkDomain(vec2 uv){ return vec2(uv.x * 7.0, uv.y * 0.62); }',
        'float barkHeight(vec2 uv){',
        '  vec2 q = barkDomain(uv);',
        '  vec2 w = vec2(gfbm(q * 0.5), gfbm(q * 0.5 + 9.1));',
        '  vec2 p = q + w * 0.60;',
        '  float ridge = ridged(p);',
        '  float plate = smoothstep(-0.25, 0.45, gfbm(q * 0.34));',
        '  float crack = smoothstep(0.30, 0.86, ridged(p * 1.9 + 4.0));',
        '  float fine  = gfbm(p * 5.5) * 0.5 + 0.5;',
        '  return (ridge - 0.5) * 1.85 * mix(0.35, 1.0, plate) - crack * 0.42 + fine * 0.20;',
        '}',
        'vec3 bumped(vec3 N, vec3 p, float h, float k){',
        '  vec3 dpx = dFdx(p), dpy = dFdy(p);',
        '  float dhx = dFdx(h) * k, dhy = dFdy(h) * k;',
        '  vec3 r1 = cross(dpy, N), r2 = cross(N, dpx);',
        '  float det = dot(dpx, r1);',
        '  vec3 grad = sign(det) * (dhx * r1 + dhy * r2);',
        '  return normalize(abs(det) * N - grad);',
        '}',
        'void main(){',
        '  if (unscanned(vW, 520.0)) discard;',
        '  vec2 uv = vInf.xy;',
        '  float cap = vInf.z;',
        '  float m = smoothstep(0.05, 0.42, cap);',
        '  vec3 N = normalize(vN);',
        '  float h = barkHeight(uv);',
        '  N = bumped(N, vW, h, mix(0.26, 0.06, m));',
        '  vec2 q = barkDomain(uv);',
        '  float grain  = gfbm(q * 1.25) * 0.5 + 0.5;',
        '  float mottle = gfbm(q * 0.28 + 21.0) * 0.5 + 0.5;',
        '  float crack  = smoothstep(0.30, 0.86, ridged(q * 1.9 + 4.0));',
        '  vec3 silver = mix(vec3(0.020, 0.019, 0.018), vec3(0.290, 0.283, 0.264), grain);',
        '  vec3 umber  = mix(vec3(0.024, 0.019, 0.016), vec3(0.175, 0.140, 0.110), grain);',
        '  vec3 wood   = mix(silver, umber, mottle * 0.78);',
        '  wood *= 1.0 - 0.70 * crack;',
        '  float mo = gfbm(vec2(vW.x * 2.6, vW.z * 2.6 + vW.y * 1.9)) * 0.5 + 0.5;',
        '  vec3 moss = mix(vec3(0.0204, 0.0311, 0.0050), vec3(0.0914, 0.1392, 0.0227), mo);',
        '  moss *= 0.80 + 0.42 * cap;',
        '  vec3 col = mix(wood, moss, m);',
        '  float lich = smoothstep(0.56, 0.84, gfbm(q * 0.62 + 31.0) * 0.5 + 0.5);',
        '  lich *= (1.0 - m) * smoothstep(-0.10, 0.70, N.y) * smoothstep(0.15, 0.50, h);',
        '  col = mix(col, vec3(0.162, 0.176, 0.132), lich * 0.78);',
        '  float contact = smoothstep(0.0, 0.16, cap) * (1.0 - smoothstep(0.16, 0.60, cap));',
        '  col *= 1.0 - 0.48 * contact;',
        '  float ao = mix(0.30, 1.02, smoothstep(-0.40, 0.62, h)) * mix(1.0, 0.86, m);',
        '  vec3 lit = litSurface(N, col, ao);',
        '  vec3 V = normalize(cameraPosition - vW);',
        '  lit += col * uAmbCol * pow(1.0 - max(dot(N, V), 0.0), 4.0) * 0.85;',
        '  float spec = pow(max(dot(reflect(-uKeyDir, N), V), 0.0), 20.0);',
        '  lit += uKeyCol * spec * 0.045 * (1.0 - m) * ao;',
        '  float a = uAlpha * maskAt(vL, uBoxH);',
        '  if (a < 0.004) discard;',
        '  gl_FragColor = vec4(aerial(lit, vH), a);',
        '  #include <tonemapping_fragment>',
        '  #include <encodings_fragment>',
        '}'
      ].join('\n'),
      transparent: cfg.transparent === true,
      depthWrite: cfg.depthWrite !== false,
      side: THREE.DoubleSide
    });
  }

  function grassMaterial(cfg) {
    return new THREE.ShaderMaterial({
      uniforms: cfg.uniforms,
      side: THREE.DoubleSide,
      transparent: cfg.transparent === true,
      depthWrite: cfg.depthWrite !== false,
      vertexShader: WIND_GLSL + [
        'attribute vec3 offset;',
        'attribute vec3 nrm;',
        'attribute vec4 rnd;',
        'attribute float aux;',
        'uniform vec3 uMouse;',
        'uniform float uMouseR;',
        'uniform float uBoxH;',
        'varying float vT; varying float vShade; varying float vDark;',
        'varying float vTone; varying float vH; varying vec3 vN; varying vec3 vW; varying vec3 vL;',
        'void main(){',
        '  float t = uv.y; vT = t;',
        '  float len = rnd.y;',
        '  vec3 ref = abs(nrm.y) < 0.95 ? vec3(0.0, 1.0, 0.0) : vec3(1.0, 0.0, 0.0);',
        '  vec3 T0 = normalize(cross(nrm, ref));',
        '  vec3 B0 = cross(nrm, T0);',
        '  float ca = cos(rnd.x), sa = sin(rnd.x);',
        '  vec3 widthDir = T0 * ca + B0 * sa;',
        '  vec3 leanDir  = T0 * -sa + B0 * ca;',
        '  float bend = t * t;',
        '  float gust = (sin(uTime * 1.75 + offset.x * 1.6 + rnd.x) * 0.12',
        '             +  sin(uTime * 0.85 + offset.x * 0.55) * 0.07) * uWind;',
        '  vec3 world = offset + windOffset(offset)',
        '             + nrm * (t * len)',
        '             + widthDir * (position.x * len * 0.62)',
        '             + leanDir * (rnd.z * 0.42 * len) * bend',
        '             + (T0 * gust + B0 * gust * 0.6) * bend * len * 1.6;',
        '  vec3 toB = offset - uMouse;',
        '  float infl = smoothstep(uMouseR, 0.0, length(toB * vec3(1.0, 1.0, 0.30)));',
        '  infl *= infl;',
        '  vec3 push = toB - nrm * dot(toB, nrm);',
        '  float pl = length(push);',
        '  push = pl > 0.0001 ? push / pl : T0;',
        '  world += push * infl * bend * len * 2.2;',
        '  world -= nrm * infl * bend * len * 1.0;',
        '  vDark = infl;',
        '  vShade = (0.66 + 0.34 * rnd.w) * (0.82 + 0.18 * sin(rnd.x * 2.0));',
        '  vShade *= 0.46 + 0.54 * clamp(nrm.y * 0.5 + 0.62, 0.0, 1.0);',
        '  vTone = smoothstep(0.16, 0.86, aux);',
        '  vN = normalize(mix(nrm, normalize(leanDir * rnd.z + nrm), 0.35));',
        '  vL = world;',
        '  vH = clamp(world.y / uBoxH + 0.5, 0.0, 1.0);',
        '  vec4 wp = modelMatrix * vec4(world, 1.0);',
        '  vW = wp.xyz;',
        '  gl_Position = projectionMatrix * viewMatrix * wp;',
        '}'
      ].join('\n'),
      fragmentShader: LIGHT_GLSL + [
        'precision highp float;',
        'uniform float uAlpha; uniform float uBoxH;',
        'varying float vT; varying float vShade; varying float vDark;',
        'varying float vTone; varying float vH; varying vec3 vN; varying vec3 vW; varying vec3 vL;',
        'void main(){',
        '  if (unscanned(vW, 520.0)) discard;',
        '  vec3 deep = vec3(0.0126, 0.0192, 0.0031);',
        '  vec3 mid  = vec3(0.0488, 0.0744, 0.0121);',
        '  vec3 tip  = vec3(0.1222, 0.1860, 0.0304);',
        '  vec3 tipHi = vec3(0.2600, 0.3900, 0.0640);',
        '  vec3 col = mix(deep, mid, smoothstep(0.0, 0.62, vT));',
        '  col = mix(col, tip, smoothstep(0.38, 1.0, vT) * (0.35 + 0.65 * vTone));',
        '  col *= 0.62 + 0.72 * vTone;',
        '  col *= vShade;',
        '  col *= 1.0 - vDark * 0.55;',
        '  vec3 N = normalize(vN);',
        '  vec3 lit = litSurface(N, col, mix(0.40, 1.10, smoothstep(0.0, 0.88, vT)) * (0.70 + 0.52 * vTone));',
        '  lit += tipHi * smoothstep(0.68, 1.0, vT) * vTone',
        '       * (0.30 + 0.70 * max(dot(N, uKeyDir), 0.0)) * 0.95;',
        '  vec3 V = normalize(cameraPosition - vW);',
        '  lit += col * uKeyCol * pow(max(dot(V, -uKeyDir), 0.0), 2.2) * 0.55 * vT;',
        '  float a = uAlpha * maskAt(vL, uBoxH);',
        '  if (a < 0.004) discard;',
        '  gl_FragColor = vec4(aerial(lit, vH), a);',
        '  #include <tonemapping_fragment>',
        '  #include <encodings_fragment>',
        '}'
      ].join('\n')
    });
  }

  var uTime  = { value: 0 };
  var uWind  = { value: REDUCED ? 0.0 : 1.0 };
  var uMouseNear = { value: new THREE.Vector3(9999, 9999, 9999) };
  var uMouseFar  = { value: new THREE.Vector3(9999, 9999, 9999) };
  var uScanO  = { value: new THREE.Vector3(-900, -260, 240) };
  var uScanR  = { value: 0 };
  var uScanOn = { value: 0 };

  var KEY  = new THREE.Vector3(-0.30, 0.92, 0.28).normalize();
  var FILL = new THREE.Vector3( 0.12, -0.86, 0.50).normalize();

  function lightUniforms(extra) {
    var u = {
      uTime: uTime, uWind: uWind,
      uKeyDir:  { value: KEY.clone() },
      uKeyCol:  { value: new THREE.Color(1.14, 1.06, 0.88) },
      uFillDir: { value: FILL.clone() },
      uFillCol: { value: new THREE.Color(0.78, 0.78, 0.62) },
      uAmbCol:  { value: new THREE.Color(0.086, 0.090, 0.080) },
      uHazeCol: { value: new THREE.Color(0.176, 0.195, 0.145) },
      uHaze:    { value: 0.14 },
      uHazeLift:{ value: 0.20 },
      uFog:     { value: 0.0 },
      uAlpha:   { value: 1.0 },
      uBoxH:    { value: BOXW / ARCH.aspect },
      uMask:    { value: new THREE.Vector4(0, 1, 0, 1) },
      uMaskOn:  { value: 0 },
      uScanO:   uScanO, uScanR: uScanR, uScanOn: uScanOn,
      uMouse:   { value: uMouseNear.value },
      uMouseR:  { value: 1.5 }
    };
    for (var k in extra) if (extra.hasOwnProperty(k)) u[k] = extra[k];
    return u;
  }

  function bladeGeometry() {
    var SEGS = 3, verts = [], uvs = [], idx = [], i;
    for (i = 0; i <= SEGS; i++) {
      var t = i / SEGS, w = 0.5 * (1 - t * t);
      verts.push(-w, t, 0, w, t, 0);
      uvs.push(0, t, 1, t);
    }
    verts[verts.length - 6] = 0; verts[verts.length - 3] = 0;
    for (i = 0; i < SEGS; i++) {
      var a = i * 2, b = a + 1, c = a + 2, d = a + 3;
      idx.push(a, b, c, b, d, c);
    }
    var g = new THREE.InstancedBufferGeometry();
    g.setAttribute('position', new THREE.Float32BufferAttribute(verts, 3));
    g.setAttribute('uv', new THREE.Float32BufferAttribute(uvs, 2));
    g.setIndex(idx);
    return g;
  }

  function assembleRoot(limbs, opt) {
    var group = new THREE.Group();
    var uni = lightUniforms({
      uBoxH:   { value: BOXW / opt.aspect },
      uHaze:   { value: opt.haze },
      uFog:    { value: opt.fog },
      uHazeCol:{ value: new THREE.Color().fromArray(opt.hazeCol || [0.176, 0.195, 0.145]) },
      uHazeLift:{ value: opt.hazeLift === undefined ? 0.20 : opt.hazeLift },
      uAlpha:  { value: opt.alpha },
      uMask:   { value: new THREE.Vector4(opt.mask ? opt.mask[0] : 0, opt.mask ? opt.mask[1] : 1,
                                          opt.mask ? opt.mask[2] : 0, opt.mask ? opt.mask[3] : 1) },
      uMaskOn: { value: opt.mask ? 1 : 0 },
      uMouse:  { value: opt.mouse.value },
      uMouseR: { value: opt.mouseR }
    });
    var soft = !!opt.mask || opt.alpha < 1;

    var bag = { pos: [], nor: [], inf: [], idx: [] };
    for (var i = 0; i < limbs.length; i++) tessellate(limbs[i], bag);
    var geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.Float32BufferAttribute(bag.pos, 3));
    geo.setAttribute('normal', new THREE.Float32BufferAttribute(bag.nor, 3));
    geo.setAttribute('inf', new THREE.Float32BufferAttribute(bag.inf, 3));
    geo.setIndex(bag.idx);
    var shell = new THREE.Mesh(geo, barkMaterial({ uniforms: uni, transparent: soft, depthWrite: true }));
    shell.frustumCulled = false;
    shell.renderOrder = opt.order;
    group.add(shell);

    var fur = { off: [], nrm: [], rnd: [], aux: [] };
    var total = 0;
    for (i = 0; i < limbs.length; i++) total += limbs[i].len;
    for (i = 0; i < limbs.length; i++) {
      plantBlades(limbs[i], Math.round(opt.blades * limbs[i].len / total), fur);
    }
    var bg = bladeGeometry();
    bg.setAttribute('offset', new THREE.InstancedBufferAttribute(new Float32Array(fur.off), 3));
    bg.setAttribute('nrm',    new THREE.InstancedBufferAttribute(new Float32Array(fur.nrm), 3));
    bg.setAttribute('rnd',    new THREE.InstancedBufferAttribute(new Float32Array(fur.rnd), 4));
    bg.setAttribute('aux',    new THREE.InstancedBufferAttribute(new Float32Array(fur.aux), 1));
    bg.instanceCount = fur.off.length / 3;
    var grass = new THREE.Mesh(bg, grassMaterial({ uniforms: uni, transparent: soft, depthWrite: true }));
    grass.frustumCulled = false;
    grass.renderOrder = opt.order + 0.1;
    group.add(grass);

    for (i = 0; i < limbs.length; i++) { limbs[i].grid = limbs[i].gnrm = limbs[i].gcaps = null; }

    group.userData = { uni: uni, blades: bg.instanceCount };
    return group;
  }

  function radialTexture(size, stops) {
    var c = document.createElement('canvas'); c.width = c.height = size;
    var g = c.getContext('2d');
    var grad = g.createRadialGradient(size / 2, size / 2, 0, size / 2, size / 2, size / 2);
    stops.forEach(function (s) { grad.addColorStop(s[0], s[1]); });
    g.fillStyle = grad; g.fillRect(0, 0, size, size);
    var t = new THREE.CanvasTexture(c);
    t.minFilter = THREE.LinearFilter;
    if ('sRGBEncoding' in THREE) t.encoding = THREE.sRGBEncoding;
    return t;
  }

  function build() {
    var narrow = NARROW.matches;
    var small = narrow || (window.innerWidth * window.innerHeight) < 620000;
    /* A login page is the first thing an unauthenticated visitor loads, so the
       fur is planted lighter here than on the authored landing page — the
       silhouette and motion survive the cut, the wait to sign in does not. */
    var BLADES_NEAR = small ? 42000 : 110000;
    var BLADES_FAR  = small ? 14000 :  34000;

    renderer = new THREE.WebGLRenderer({ canvas: canvas, alpha: true, antialias: !small });
    renderer.setClearColor(0x000000, 0);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, small ? 1.6 : 2));
    renderer.toneMapping = THREE.ACESFilmicToneMapping;
    renderer.toneMappingExposure = 1.30;
    if ('sRGBEncoding' in THREE) renderer.outputEncoding = THREE.sRGBEncoding;

    scene = new THREE.Scene();
    camera = new THREE.PerspectiveCamera(40, 1, 10, 8000);
    camera.position.set(0, 0, DIST);

    var nearLimbs = buildNearRoot();
    var hp = new THREE.Vector3(), hn = new THREE.Vector3();
    var extra = [];
    for (var i = 0; i < 14; i++) {
      var r = rng();
      var src = nearLimbs[r < 0.62 ? 0 : (r < 0.82 ? 1 : 2)];
      var t = rand(0.04, 0.96), th = rng() * TAU;
      limbSurface(src, t, th, hp, hn);
      if (hn.y < -0.35) continue;
      limbFrame(src, t);
      var dir = hn.clone().multiplyScalar(rand(0.5, 1.2))
        .addScaledVector(_ft, rand(-0.6, 1.5))
        .addScaledVector(UP, rand(-0.5, 0.55)).normalize();
      hp.addScaledVector(hn, -src.rw(t) * 0.55);
      growOffshoot(extra, hp.clone(), dir, rand(0.28, 0.72), src.rw(t) * rand(0.22, 0.40), 0);
    }
    nearLimbs = nearLimbs.concat(extra);

    nearGroup = assembleRoot(nearLimbs, {
      aspect: ARCH.aspect, haze: 0.15, fog: 0.0, alpha: 1.0, order: 2,
      blades: BLADES_NEAR, mouse: uMouseNear, mouseR: 1.20
    });
    scene.add(nearGroup);

    farGroup = assembleRoot(buildFarRoot(), {
      aspect: FAR.aspect, haze: 0.16, fog: 0.26, alpha: 1.0, order: 0,
      hazeCol: [0.150, 0.164, 0.120], hazeLift: 0.92,
      blades: BLADES_FAR, mask: [0.4, 3.4, 0.0, 0.42],
      mouse: uMouseFar, mouseR: 1.4
    });
    scene.add(farGroup);

    buildAmbient();
    layout();
    window.addEventListener('resize', layout);
    clock = new THREE.Clock();

    /* The authored page opens with a survey pulse that draws the root in over
       3.4s. That is a landing-page reveal; on a login page the scene has to be
       there the moment the form is, so the pulse stays off and every fragment
       is drawn from frame one. unscanned() returns false while uScanOn is 0. */
    uScanOn.value = 0;
    scanning = false;

    renderFrame();
    startTick();
  }

  function buildAmbient() {
    var geo = new THREE.PlaneGeometry(1, 1, 1, 1);

    shadowMesh = new THREE.Mesh(geo, new THREE.MeshBasicMaterial({
      map: radialTexture(256, [[0, 'rgba(12,16,10,0.62)'], [0.45, 'rgba(12,16,10,0.26)'], [1, 'rgba(12,16,10,0)']]),
      transparent: true, depthWrite: false, depthTest: false
    }));
    shadowMesh.renderOrder = 1;
    shadowMesh.position.z = -70;
    scene.add(shadowMesh);

    glowMesh = new THREE.Mesh(geo, new THREE.MeshBasicMaterial({
      map: radialTexture(256, [[0, 'rgba(226,236,212,0.30)'], [0.42, 'rgba(214,226,200,0.10)'], [1, 'rgba(214,226,200,0)']]),
      transparent: true, depthWrite: false, depthTest: false,
      blending: THREE.AdditiveBlending
    }));
    glowMesh.renderOrder = -1;
    glowMesh.position.z = -320;
    scene.add(glowMesh);

    var COUNT = (NARROW.matches || (window.innerWidth * window.innerHeight) < 620000) ? 1200 : 3200;
    var pos = new Float32Array(COUNT * 3);
    var seed = new Float32Array(COUNT * 4);
    for (var i = 0; i < COUNT; i++) {
      pos[i * 3]     = (Math.random() - 0.5) * 3400;
      pos[i * 3 + 1] = (Math.random() - 0.5) * 1500;
      pos[i * 3 + 2] = -380 + Math.random() * 1000;
      seed[i * 4]     = Math.random() * 6.283;
      seed[i * 4 + 1] = 0.25 + Math.random() * 0.9;
      seed[i * 4 + 2] = 0.4 + Math.random() * 1.4;
      seed[i * 4 + 3] = 0.70 + 1.05 * Math.pow(Math.random(), 2.2);
    }
    var pg = new THREE.BufferGeometry();
    pg.setAttribute('position', new THREE.BufferAttribute(pos, 3));
    pg.setAttribute('seed', new THREE.BufferAttribute(seed, 4));

    poleTex = radialTexture(64, [[0, 'rgba(255,255,255,1)'], [0.35, 'rgba(236,244,224,0.5)'], [1, 'rgba(236,244,224,0)']]);
    motes = new THREE.Points(pg, new THREE.ShaderMaterial({
      uniforms: { uTime: uTime, uMap: { value: poleTex }, uSize: { value: 9 }, uScale: { value: 440 } },
      transparent: true, depthWrite: false, depthTest: true,
      blending: THREE.AdditiveBlending,
      vertexShader: [
        'attribute vec4 seed;',
        'uniform float uTime, uSize, uScale;',
        'varying float vFade;',
        'void main(){',
        '  float ph = seed.x, sp = seed.y, am = seed.z;',
        '  vec3 p = position;',
        '  p.x += sin(uTime * sp * 0.35 + ph) * 34.0 * am;',
        '  float climb = mod(uTime * 11.0 * sp + ph * 60.0, 1500.0) - 750.0;',
        '  p.y += climb;',
        '  p.z += cos(uTime * sp * 0.28 + ph) * 24.0 * am;',
        '  vec4 mv = modelViewMatrix * vec4(p, 1.0);',
        '  gl_PointSize = uSize * seed.w * (uScale / max(-mv.z, 1.0));',
        '  float edge = 1.0 - abs(climb) / 750.0;',
        '  float twinkle = 0.55 + 0.45 * sin(uTime * (0.7 + sp * 1.6) + ph * 3.1);',
        '  vFade = clamp(edge * 3.0, 0.0, 1.0) * twinkle;',
        '  gl_Position = projectionMatrix * mv;',
        '}'
      ].join('\n'),
      fragmentShader: [
        'precision highp float;',
        'uniform sampler2D uMap;',
        'varying float vFade;',
        'void main(){',
        '  vec4 t = texture2D(uMap, gl_PointCoord);',
        '  gl_FragColor = vec4(t.rgb, t.a * vFade * 0.52);',
        '}'
      ].join('\n')
    }));
    motes.frustumCulled = false;
    motes.renderOrder = 6;
    scene.add(motes);
  }

  function layout() {
    W = hero.clientWidth; H = hero.clientHeight;
    renderer.setSize(W, H, false);
    camera.fov = 2 * Math.atan((H / 2) / DIST) * 180 / Math.PI;
    camera.aspect = W / H;
    camera.updateProjectionMatrix();

    var narrow = NARROW.matches;
    var s = stageEl.getBoundingClientRect();
    var h = hero.getBoundingClientRect();
    var u = s.width / (narrow ? 760 : 1600);
    var ox = s.left - h.left, oy = s.top - h.top;
    function wx(px) { return ox + px * u - W / 2; }
    function wy(py) { return H / 2 - (oy + py * u); }

    var A = narrow ? ARCH_N : ARCH, F = narrow ? FAR_N : FAR;
    var cover = Math.max(1, W / s.width);

    function place(group, box, pinFx, pinFy, z) {
      var boxH = box.w / box.aspect;
      var scale = box.w * u * cover / BOXW;
      var k = (DIST - z) / DIST;
      var lx = (pinFx - 0.5) * BOXW, ly = (0.5 - pinFy) * (BOXW / box.aspect);
      var px = wx(box.left + pinFx * box.w), py = wy(box.top + pinFy * boxH);
      group.scale.setScalar(scale * k);
      group.position.set((px - lx * scale) * k, (py - ly * scale) * k, z);
    }

    place(nearGroup, A, 0.732, 0.06, 0);
    place(farGroup,  F, 0.410, 0.32, F.z);

    var aw = A.w * u * cover, ah = aw / A.aspect;
    var cx = wx(A.left + 0.5 * A.w), cy = wy(A.top + 0.5 * (A.w / A.aspect));

    shadowMesh.scale.set(aw * 1.02, ah * 0.72, 1);
    shadowMesh.position.set(cx, cy - ah * 0.40, -70);

    glowMesh.scale.set(aw * 1.15, ah * 1.5, 1);
    glowMesh.position.set(cx - aw * 0.06, cy - ah * 0.18, -320);

    nearGroup.updateMatrixWorld(true);
    uScanO.value.set(-5.2, -0.9, 1.8);
    nearGroup.localToWorld(uScanO.value);
    scanMax = Math.hypot(W, H) * 1.3 + 900;

    motes.material.uniforms.uSize.value = Math.max(5, 9 * u);
    var half = renderer.getDrawingBufferSize(new THREE.Vector2()).y * 0.5;
    motes.material.uniforms.uScale.value = half;
  }

  var raycaster = new THREE.Raycaster();
  var crownPlane = new THREE.Plane(new THREE.Vector3(0, 0, 1), 0);
  var hitWorld = new THREE.Vector3();
  var tmpLocal = new THREE.Vector3();
  var mouseLive = false;

  function updateMouse(dt) {
    if (ndc.x > 2 || REDUCED) { mouseLive = false; }
    else {
      raycaster.setFromCamera(ndc, camera);
      mouseLive = !!raycaster.ray.intersectPlane(crownPlane, hitWorld);
    }
    [[nearGroup, uMouseNear], [farGroup, uMouseFar]].forEach(function (pair) {
      var g = pair[0], u2 = pair[1];
      if (!g) return;
      if (!mouseLive) { u2.value.set(9999, 9999, 9999); return; }
      tmpLocal.copy(hitWorld);
      g.worldToLocal(tmpLocal);
      if (u2.value.x > 999) u2.value.copy(tmpLocal);
      else u2.value.lerp(tmpLocal, 1 - Math.pow(0.0002, dt));
    });
  }

  var frames = 0;
  function renderFrame() {
    var dt = Math.min(clock.getDelta(), 0.05);
    if (!REDUCED) uTime.value += dt;

    camera.position.x = -smooth.x * 26;
    camera.position.y =  smooth.y * 16;
    camera.lookAt(camera.position.x * 0.42, camera.position.y * 0.42, 0);

    if (!REDUCED) {
      nearGroup.rotation.y = smooth.x * 0.055;
      nearGroup.rotation.x = smooth.y * 0.026;
      nearGroup.rotation.z = Math.sin(uTime.value * 0.22) * 0.0022;
      farGroup.rotation.y  = smooth.x * 0.030;
    }

    if (scanning) {
      scanT += dt / SCAN_DUR;
      var e = Math.min(1, scanT);
      uScanR.value = (1 - Math.pow(1 - e, 1.35)) * scanMax;
      if (e >= 1) { scanning = false; uScanOn.value = 0; }
    }

    updateMouse(dt);
    renderer.render(scene, camera);
    if (++frames === 2) window.__ready = true;
  }

  ready();
  requestAnimationFrame(function () { requestAnimationFrame(function () {
    try { build(); }
    catch (err) { console.error(err); }
  }); });

  setTimeout(ready, 4000);
})();
