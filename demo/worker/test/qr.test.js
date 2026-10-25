import { test } from "node:test";
import assert from "node:assert/strict";
import { encodeQR, qrSVG } from "../src/qr.js";

const link = "healthrelay://pair?code=ABCDEFGHIJKLMNOP&server=https%3A%2F%2Fhealth-relay-demo.leblancfg.com";

test("pairing link SVG matches server/qr.go output", async () => {
  const qr = encodeQR(new TextEncoder().encode(link));
  assert.equal(qr.size, 41);
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(qrSVG(qr)));
  const hex = [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
  assert.equal(hex, "04b85fb861f34e7304087b34521a1c8d2daac2693a6210b0cda88e4a3c061d9a");
});

test("too long", () => {
  assert.throws(() => encodeQR(new Uint8Array(667)), /qr: data too long/);
});
