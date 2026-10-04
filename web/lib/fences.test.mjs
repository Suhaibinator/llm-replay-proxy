import test from "node:test";
import assert from "node:assert/strict";
import { splitFences } from "./fences.ts";

test("fences nested in list items render as code without their indentation", () => {
  const chunks = splitFences(
    "- Install:\n    ```bash\n    npm i\n      --save\n    ```\n- Done",
  );
  assert.deepEqual(chunks, [
    { code: false, lang: "", text: "- Install:" },
    { code: true, lang: "bash", text: "npm i\n  --save" },
    { code: false, lang: "", text: "- Done" },
  ]);
});

test("content is only dedented up to the opening fence's indentation", () => {
  const [code] = splitFences("  ```\nflush\n x\n     deep\n```");
  assert.equal(code.text, "flush\nx\n   deep");
});

test("closing fences need the same marker and at least the same length", () => {
  const [, code] = splitFences("a\n````py\n```\n~~~~\n````  \nb");
  assert.equal(code.lang, "py");
  assert.equal(code.text, "```\n~~~~");
});

test("tilde info strings may contain backticks; backtick ones may not", () => {
  assert.equal(splitFences("~~~ js `x`\ncode\n~~~")[0].lang, "js `x`");
  // The first line is prose, so the last line opens an unclosed fence.
  assert.deepEqual(splitFences("``` js `x`\ncode\n```"), [
    { code: false, lang: "", text: "``` js `x`\ncode\n```" },
  ]);
});

test("an unclosed fence leaves the rest as prose", () => {
  assert.deepEqual(splitFences("intro\n```js\nconst a = 1;"), [
    { code: false, lang: "", text: "intro\n```js\nconst a = 1;" },
  ]);
});

test("CRLF fences close and pathological input stays fast", () => {
  assert.equal(splitFences("```js\r\nx\r\n```\r")[0].code, true);
  const started = performance.now();
  splitFences(`${"`".repeat(50_000)}\r\n`.repeat(4));
  splitFences("```\n".repeat(1) + "x\n".repeat(200_000));
  assert.ok(performance.now() - started < 2000);
});
