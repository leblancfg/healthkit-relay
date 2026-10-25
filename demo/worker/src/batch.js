// Upload validation, ported from server/model.go and the checks in server/handler.go
// (samples): a batch is accepted whole or refused with the message the Go server gives.
// The demo keeps nothing, so this only decides the answer: 200 with the counts, or 400.

export const WireVersion = 2;
// MaxBatchPartitions bounds the archive partitions one batch may touch (handler.go).
export const MaxBatchPartitions = 2000;

const kindRE = /^[a-z][a-z0-9_]{0,63}$/;
const typeRE = /^HK[A-Za-z0-9]{1,120}$/;
const uuidRE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const rfc3339RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{1,2}):(\d{2}):(\d{2})(?:[.,](\d+))?(Z|([+-])(\d{2}):(\d{2}))$/;
const utf8 = new TextEncoder();

export class BadRequest extends Error {}

// Field types, as Go's encoding/json checks them when it decodes into a struct.
const T = {
  string: (v) => typeof v === "string",
  number: (v) => typeof v === "number",
  int: (v) => Number.isInteger(v),
  bool: (v) => typeof v === "boolean",
  any: () => true,
  array: (v) => Array.isArray(v),
  object: (v) => typeof v === "object" && !Array.isArray(v),
};

const sampleSchema = {
  uuid: T.string, type: T.string, kind: T.string, start: T.string, end: T.string, value: T.number,
  unit: T.string, value_label: T.string,
  source: { name: T.string, bundle_id: T.string },
  workout: { activity_type: T.int, activity_name: T.string, duration_s: T.number, energy_kcal: T.number, distance_m: T.number },
};
const recordSchema = {
  kind: T.string, key: T.string, start: T.string, values: T.any, sample_uuid: T.string,
  sample_type: T.string, series: T.string, points: T.array,
};
const deletionSchema = { uuid: T.string, type: T.string };
const batchSchema = {
  version: T.int, device: T.any, samples: T.array, records: T.array, deletions: T.array,
  trigger: T.string, app_state: T.string,
};
export const pairSchema = { code: T.string, name: T.string, model: T.string, replaces: T.string };
export const heartbeatSchema = {
  trigger: T.string, app_state: T.string, protected_data: T.bool, outcome: T.string, error: T.string,
  last_error: T.string, pending: T.string, uploaded: T.int, seconds: T.number,
  background_time_left: T.number, process_age: T.int, app_version: T.string,
  wakes: (v) => T.object(v) && Object.values(v).every((w) => w === null || wakeOK(w)),
  battery: T.string, low_power_mode: T.bool, background_refresh: T.string,
};
const wakeOK = (w) => {
  if (!T.object(w)) return false;
  const f = fields(w, { count: T.int, last: T.string });
  return f.last === undefined || f.last === null || parseTime(f.last) !== null;
};

// fields decodes obj like Go decodes JSON into a struct: keys match field names exactly or
// without case, later keys win, null leaves a field unset, and a value of the wrong type
// fails. Unknown keys are ignored. Nested schemas are structs (an object or null).
export function fields(obj, schema) {
  if (obj === null) return {};
  if (!T.object(obj)) throw new BadRequest(`json: cannot unmarshal ${jsonKind(obj)} into Go value of type struct`);
  const out = {};
  for (const [k, v] of Object.entries(obj)) {
    const name = Object.hasOwn(schema, k) ? k : Object.keys(schema).find((f) => f === k.toLowerCase());
    if (!name || v === null) continue;
    const want = schema[name];
    if (typeof want === "function") {
      if (!want(v)) throw new BadRequest(`json: cannot unmarshal ${jsonKind(v)} into Go struct field .${name}`);
      out[name] = v;
    } else {
      out[name] = fields(v, want);
    }
  }
  return out;
}

function jsonKind(v) {
  if (Array.isArray(v)) return "array";
  return typeof v === "boolean" ? "bool" : typeof v;
}

// quote is Go's %q for the strings these messages carry.
const quote = (s) => JSON.stringify(s);

// parseTime parses an RFC 3339 time like Go's time.Parse(time.RFC3339Nano, s), which also
// takes a one-digit hour, a comma before the fraction and offsets up to 24:60: it returns
// nanoseconds since the Unix epoch (a BigInt), or null.
export function parseTime(s) {
  const m = rfc3339RE.exec(s);
  if (!m) return null;
  const [y, mo, d, h, mi, sec] = m.slice(1, 7).map(Number);
  if (mo < 1 || mo > 12 || d < 1 || d > daysIn(y, mo) || h > 23 || mi > 59 || sec > 59) return null;
  let offset = 0;
  if (m[8] !== "Z") {
    const oh = Number(m[10]), om = Number(m[11]);
    if (oh > 24 || om > 60) return null; // Go's ranges
    offset = (oh * 60 + om) * 60 * (m[9] === "-" ? -1 : 1);
  }
  const frac = BigInt(((m[7] || "") + "000000000").slice(0, 9));
  const secs = BigInt(daysFromCivil(y, mo, d)) * 86400n + BigInt(h * 3600 + mi * 60 + sec - offset);
  return secs * 1000000000n + frac;
}

function daysIn(y, m) {
  if (m === 2) return (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0 ? 29 : 28;
  return [4, 6, 9, 11].includes(m) ? 30 : 31;
}

// daysFromCivil is the day number of a date from 1970-01-01 (Howard Hinnant's algorithm).
function daysFromCivil(y, m, d) {
  y -= m <= 2 ? 1 : 0;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * (m + (m > 2 ? -3 : 9)) + 2) / 5) + d - 1;
  const doe = yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy;
  return era * 146097 + doe - 719468;
}

// month is the UTC year and month of a time in Unix nanoseconds, "2006-01"; Go's zero time
// (null) is "0001-01".
function month(ns) {
  if (ns === null) return "0001-01";
  const days = Number((ns < 0n ? ns - 86399999999999n : ns) / 86400000000000n);
  const z = days + 719468;
  const era = Math.floor(z / 146097);
  const doe = z - era * 146097;
  const yoe = Math.floor((doe - Math.floor(doe / 1460) + Math.floor(doe / 36524) - Math.floor(doe / 146096)) / 365);
  const doy = doe - (365 * yoe + Math.floor(yoe / 4) - Math.floor(yoe / 100));
  const mp = Math.floor((5 * doy + 2) / 153);
  const m = mp + (mp < 10 ? 3 : -9);
  const y = yoe + era * 400 + (m <= 2 ? 1 : 0);
  return String(y).padStart(4, "0") + "-" + String(m).padStart(2, "0");
}

const timeError = (s) => `parsing time ${quote(s)} as "2006-01-02T15:04:05.999999999Z07:00": not an RFC 3339 time`;

function normalizeUUID(s) {
  if (!uuidRE.test(s ?? "")) throw new BadRequest(`invalid uuid ${quote(s ?? "")}`);
  return s.toUpperCase();
}

function parseSample(raw) {
  const s = fields(raw, sampleSchema);
  const uuid = normalizeUUID(s.uuid);
  const type = s.type ?? "";
  if (type === "") throw new BadRequest(`sample ${uuid}: missing type`);
  if (!typeRE.test(type)) throw new BadRequest(`sample ${uuid}: invalid type ${quote(type)}`);
  const start = parseTime(s.start ?? "");
  if (start === null) throw new BadRequest(`sample ${uuid}: bad start: ${timeError(s.start ?? "")}`);
  const end = parseTime(s.end ?? "");
  if (end === null) throw new BadRequest(`sample ${uuid}: bad end: ${timeError(s.end ?? "")}`);
  if (end < start) throw new BadRequest(`sample ${uuid}: end before start`);
  return { type, start };
}

function parseRecord(raw) {
  const r = fields(raw, recordSchema);
  const kind = r.kind ?? "";
  if (!kindRE.test(kind)) throw new BadRequest(`invalid kind ${quote(kind)}`);
  const key = r.key ?? "";
  const keyBytes = utf8.encode(key).length;
  if (keyBytes === 0 || keyBytes > 512) throw new BadRequest(`record ${kind}: key must be 1 to 512 bytes`);
  let start = null;
  if (r.start) {
    start = parseTime(r.start);
    if (start === null) throw new BadRequest(`record ${kind}/${key}: bad start: ${timeError(r.start)}`);
  }
  if (start === null && r.points?.length > 0) {
    // Route and quantity-series points carry their own time: the first one dates the chunk.
    const p = r.points[0];
    if (T.object(p) && p !== null) {
      const t = typeof p.t === "string" ? p.t : "";
      start = parseTime(t || (typeof p.start === "string" ? p.start : ""));
    }
  }
  return { kind, start };
}

function parseDeletion(raw) {
  normalizeUUID(fields(raw, deletionSchema).uuid);
}

// checkBatch validates an upload's decoded body like the Go server and returns the answer to
// send; it throws BadRequest with the Go server's message otherwise.
export function checkBatch(body) {
  let b;
  try {
    b = fields(body, batchSchema);
  } catch (e) {
    throw new BadRequest("invalid JSON: " + e.message);
  }
  const samples = b.samples ?? [], records = b.records ?? [], deletions = b.deletions ?? [];
  const parts = new Set();
  samples.forEach((raw, i) => {
    const s = at("samples", i, () => parseSample(raw));
    parts.add("sample/" + s.type + "/" + month(s.start));
  });
  records.forEach((raw, i) => {
    const r = at("records", i, () => parseRecord(raw));
    parts.add("record/" + r.kind + "/" + month(r.start));
  });
  deletions.forEach((raw, i) => at("deletions", i, () => parseDeletion(raw)));
  if (parts.size > MaxBatchPartitions) {
    throw new BadRequest(`batch spans ${parts.size} (type, month) pairs, more than ${MaxBatchPartitions}`);
  }
  const items = samples.length + records.length + deletions.length;
  // Every item is "stored": the demo validated it and drops it.
  return { received: items, stored: items, duplicates: 0, deletions: deletions.length,
    records: records.length, version: WireVersion };
}

function at(field, i, f) {
  try {
    return f();
  } catch (e) {
    if (e instanceof BadRequest) throw new BadRequest(`${field}[${i}]: ${e.message}`);
    throw e;
  }
}
