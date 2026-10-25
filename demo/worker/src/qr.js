// A port of server/qr.go: a QR code encoder (ISO/IEC 18004), just enough for pairing links:
// byte mode, error correction level M, versions 1 to 20 (up to 666 bytes), automatic mask
// choice. It follows the structure of Project Nayuki's reference implementation.

// Per version (index 1 to 20), for level M.
const qrECCPerBlock = [0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26];
const qrBlocks = [0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16];

const qrMaxVersion = 20;

const div = (a, b) => Math.trunc(a / b);

// encodeQR returns the smallest QR code holding data, as {size, dark(x, y)} (true = dark).
export function encodeQR(data) {
  for (let v = 1; v <= qrMaxVersion; v++) {
    const countBits = v >= 10 ? 16 : 8;
    const capacity = qrDataCodewords(v) * 8;
    if (4 + countBits + 8 * data.length > capacity) {
      continue;
    }
    const bits = [];
    appendBits(bits, 0b0100, 4); // byte mode
    appendBits(bits, data.length, countBits);
    for (const b of data) {
      appendBits(bits, b, 8);
    }
    appendBits(bits, 0, Math.min(4, capacity - bits.length));
    appendBits(bits, 0, (8 - (bits.length % 8)) % 8);
    for (let pad = 0xec; bits.length < capacity; pad ^= 0xec ^ 0x11) {
      appendBits(bits, pad, 8);
    }
    return newQR(v, bitsToBytes(bits));
  }
  throw new Error("qr: data too long");
}

// qrRawModules is the number of modules available for data and error correction.
function qrRawModules(v) {
  let n = (16 * v + 128) * v + 64;
  if (v >= 2) {
    const align = div(v, 7) + 2;
    n -= (25 * align - 10) * align - 55;
    if (v >= 7) {
      n -= 36;
    }
  }
  return n;
}

function qrDataCodewords(v) {
  return div(qrRawModules(v), 8) - qrECCPerBlock[v] * qrBlocks[v];
}

function appendBits(bits, value, n) {
  for (let i = n - 1; i >= 0; i--) {
    bits.push(((value >> i) & 1) === 1);
  }
}

function bitsToBytes(bits) {
  const out = new Uint8Array(div(bits.length, 8));
  bits.forEach((bit, i) => {
    if (bit) {
      out[i >> 3] |= 0x80 >> (i % 8);
    }
  });
  return out;
}

function newQR(version, data) {
  const size = version * 4 + 17;
  const q = {
    size,
    version,
    modules: Array.from({ length: size }, () => new Array(size).fill(false)),
    function: Array.from({ length: size }, () => new Array(size).fill(false)),
  };
  drawFunctionPatterns(q);
  drawCodewords(q, withECC(q, data));

  let best = 0;
  let bestPenalty = -1;
  for (let mask = 0; mask < 8; mask++) {
    applyMask(q, mask);
    drawFormat(q, mask);
    const p = penalty(q);
    if (bestPenalty < 0 || p < bestPenalty) {
      best = mask;
      bestPenalty = p;
    }
    applyMask(q, mask); // XOR again: undo
  }
  applyMask(q, best);
  drawFormat(q, best);
  const modules = q.modules;
  return { size, dark: (x, y) => modules[y][x] };
}

function set(q, x, y, dark) {
  q.modules[y][x] = dark;
  q.function[y][x] = true;
}

function drawFunctionPatterns(q) {
  for (let i = 0; i < q.size; i++) {
    set(q, 6, i, i % 2 === 0);
    set(q, i, 6, i % 2 === 0);
  }
  for (const [cx, cy] of [[3, 3], [q.size - 4, 3], [3, q.size - 4]]) {
    for (let dy = -4; dy <= 4; dy++) {
      for (let dx = -4; dx <= 4; dx++) {
        const x = cx + dx;
        const y = cy + dy;
        if (x >= 0 && x < q.size && y >= 0 && y < q.size) {
          const d = Math.max(Math.abs(dx), Math.abs(dy));
          set(q, x, y, d !== 2 && d !== 4);
        }
      }
    }
  }
  const pos = alignmentPositions(q);
  const n = pos.length;
  for (let i = 0; i < n; i++) {
    for (let j = 0; j < n; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === n - 1) || (i === n - 1 && j === 0)) {
        continue; // finder corners
      }
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          set(q, pos[i] + dx, pos[j] + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
        }
      }
    }
  }
  drawFormat(q, 0); // reserves the format areas; redrawn with the chosen mask
  if (q.version >= 7) {
    let rem = q.version;
    for (let i = 0; i < 12; i++) {
      rem = (rem << 1) ^ ((rem >> 11) * 0x1f25);
    }
    const bits = (q.version << 12) | rem;
    for (let i = 0; i < 18; i++) {
      const dark = ((bits >> i) & 1) === 1;
      const a = q.size - 11 + (i % 3);
      const b = div(i, 3);
      set(q, a, b, dark);
      set(q, b, a, dark);
    }
  }
}

function alignmentPositions(q) {
  if (q.version === 1) {
    return [];
  }
  const n = div(q.version, 7) + 2;
  const step = div(q.version * 8 + n * 3 + 5, n * 4 - 4) * 2;
  const pos = new Array(n);
  pos[0] = 6;
  for (let i = n - 1, p = q.size - 7; i >= 1; i--, p -= step) {
    pos[i] = p;
  }
  return pos;
}

// drawFormat writes level M (format bits 00) and the mask, BCH-protected, twice.
function drawFormat(q, mask) {
  const data = (0 << 3) | mask;
  let rem = data;
  for (let i = 0; i < 10; i++) {
    rem = (rem << 1) ^ ((rem >> 9) * 0x537);
  }
  const bits = ((data << 10) | rem) ^ 0x5412;
  const bit = (i) => ((bits >> i) & 1) === 1;
  for (let i = 0; i <= 5; i++) {
    set(q, 8, i, bit(i));
  }
  set(q, 8, 7, bit(6));
  set(q, 8, 8, bit(7));
  set(q, 7, 8, bit(8));
  for (let i = 9; i < 15; i++) {
    set(q, 14 - i, 8, bit(i));
  }
  for (let i = 0; i < 8; i++) {
    set(q, q.size - 1 - i, 8, bit(i));
  }
  for (let i = 8; i < 15; i++) {
    set(q, 8, q.size - 15 + i, bit(i));
  }
  set(q, 8, q.size - 8, true); // the dark module
}

// withECC splits data into blocks, appends each block's Reed-Solomon codewords and
// interleaves them.
function withECC(q, data) {
  const blocks = qrBlocks[q.version];
  const ecc = qrECCPerBlock[q.version];
  const raw = div(qrRawModules(q.version), 8);
  const short = blocks - (raw % blocks);
  const shortLen = div(raw, blocks) - ecc;
  const divisor = rsDivisor(ecc);
  const datas = [];
  const eccs = [];
  for (let i = 0, k = 0; i < blocks; i++) {
    let n = shortLen;
    if (i >= short) {
      n++;
    }
    const d = data.subarray(k, k + n);
    k += n;
    datas.push(d);
    eccs.push(rsRemainder(d, divisor));
  }
  const out = [];
  for (let i = 0; i <= shortLen; i++) {
    for (const d of datas) {
      if (i < d.length) {
        out.push(d[i]);
      }
    }
  }
  for (let i = 0; i < ecc; i++) {
    for (const e of eccs) {
      out.push(e[i]);
    }
  }
  return Uint8Array.from(out);
}

function drawCodewords(q, data) {
  let i = 0;
  for (let right = q.size - 1; right >= 1; right -= 2) {
    if (right === 6) {
      right = 5;
    }
    for (let vert = 0; vert < q.size; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        let y = vert;
        if (((right + 1) & 2) === 0) {
          y = q.size - 1 - vert; // upward
        }
        if (!q.function[y][x] && i < data.length * 8) {
          q.modules[y][x] = ((data[i >> 3] >> (7 - (i & 7))) & 1) === 1;
          i++;
        }
      }
    }
  }
}

function applyMask(q, mask) {
  for (let y = 0; y < q.size; y++) {
    for (let x = 0; x < q.size; x++) {
      let invert = false;
      switch (mask) {
        case 0:
          invert = (x + y) % 2 === 0;
          break;
        case 1:
          invert = y % 2 === 0;
          break;
        case 2:
          invert = x % 3 === 0;
          break;
        case 3:
          invert = (x + y) % 3 === 0;
          break;
        case 4:
          invert = (div(x, 3) + div(y, 2)) % 2 === 0;
          break;
        case 5:
          invert = ((x * y) % 2) + ((x * y) % 3) === 0;
          break;
        case 6:
          invert = (((x * y) % 2) + ((x * y) % 3)) % 2 === 0;
          break;
        case 7:
          invert = (((x + y) % 2) + ((x * y) % 3)) % 2 === 0;
          break;
      }
      if (invert && !q.function[y][x]) {
        q.modules[y][x] = !q.modules[y][x];
      }
    }
  }
}

const finderLike = [true, false, true, true, true, false, true, false, false, false, false];

// penalty scores how hard the symbol is to scan (runs, 2x2 blocks, finder look-alikes,
// dark/light balance); the mask with the lowest score is used.
function penalty(q) {
  let p = 0;
  const line = (get) => {
    let run = 1;
    for (let i = 1; i <= q.size; i++) {
      if (i < q.size && get(i) === get(i - 1)) {
        run++;
        continue;
      }
      if (run >= 5) {
        p += run - 2;
      }
      run = 1;
    }
    // 1:1:3:1:1 finder-like patterns with four light modules on one side.
    for (let i = 0; i + 11 <= q.size; i++) {
      let a = true;
      let b = true;
      finderLike.forEach((v, k) => {
        if (get(i + k) !== v) {
          a = false;
        }
        if (get(i + 10 - k) !== v) {
          b = false;
        }
      });
      if (a) {
        p += 40;
      }
      if (b) {
        p += 40;
      }
    }
  };
  for (let y = 0; y < q.size; y++) {
    line((i) => q.modules[y][i]);
  }
  for (let x = 0; x < q.size; x++) {
    line((i) => q.modules[i][x]);
  }
  let dark = 0;
  for (let y = 0; y < q.size; y++) {
    for (let x = 0; x < q.size; x++) {
      const c = q.modules[y][x];
      if (c) {
        dark++;
      }
      if (x + 1 < q.size && y + 1 < q.size && c === q.modules[y][x + 1] && c === q.modules[y + 1][x] && c === q.modules[y + 1][x + 1]) {
        p += 3;
      }
    }
  }
  const total = q.size * q.size;
  const k = div(Math.abs(dark * 20 - total * 10) + total - 1, total) - 1;
  return p + k * 10;
}

function rsDivisor(degree) {
  const out = new Uint8Array(degree);
  out[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < out.length; j++) {
      out[j] = gfMul(out[j], root);
      if (j + 1 < out.length) {
        out[j] ^= out[j + 1];
      }
    }
    root = gfMul(root, 2);
  }
  return out;
}

function rsRemainder(data, divisor) {
  const out = new Uint8Array(divisor.length);
  for (const b of data) {
    const factor = b ^ out[0];
    out.copyWithin(0, 1);
    out[out.length - 1] = 0;
    divisor.forEach((d, i) => {
      out[i] ^= gfMul(d, factor);
    });
  }
  return out;
}

// gfMul multiplies in GF(2^8) modulo x^8 + x^4 + x^3 + x^2 + 1.
function gfMul(x, y) {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >> 7) * 0x11d);
    z ^= ((y >> i) & 1) * x;
  }
  return z & 0xff;
}

// qrSVG renders the code with a quiet zone of 4 modules, scaled by the viewer.
export function qrSVG(qr) {
  const n = qr.size + 8;
  let s = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${n} ${n}" shape-rendering="crispEdges" role="img" aria-label="QR code">`;
  s += `<rect width="${n}" height="${n}" fill="#fff"/><path fill="#000" d="`;
  for (let y = 0; y < qr.size; y++) {
    for (let x = 0; x < qr.size; x++) {
      if (qr.dark(x, y)) {
        s += `M${x + 4} ${y + 4}h1v1h-1z`;
      }
    }
  }
  return s + `"/></svg>`;
}
