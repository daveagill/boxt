import assert from "node:assert/strict";
import { after, before, test } from "node:test";

import { createServer } from "../src/app.js";

let server;
let base;

before(async () => {
  server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${server.address().port}`;
});

after(async () => {
  await new Promise((resolve) => server.close(resolve));
});

test("root responds", async () => {
  const response = await fetch(base);
  assert.equal(response.status, 200);
  assert.match(await response.text(), /Hello from boxt/);
});

test("health responds", async () => {
  const response = await fetch(`${base}/health`);
  assert.deepEqual(await response.json(), { ok: true });
});
