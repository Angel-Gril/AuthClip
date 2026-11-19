import test from "node:test";
import assert from "node:assert/strict";
import { chooseLanguage, languages, messages } from "../web/locale.mjs";

test("manual choice, IP region, browser preferences and default are applied in order", () => {
  assert.equal(chooseLanguage("ja", "CN", ["zh-CN"]), "ja");
  assert.equal(chooseLanguage("", "JP", ["en-US"]), "ja");
  assert.equal(chooseLanguage("", "CA", ["fr-CA", "en-US"]), "fr");
  assert.equal(chooseLanguage("", "HK", ["en-GB"]), "en");
  assert.equal(chooseLanguage("", "", ["zh-HK"]), "zh-Hant");
  assert.equal(chooseLanguage("unsupported", "", ["sv-SE"]), "en");
});

test("every switchable language covers the interface and both tutorials", () => {
  const keys = Object.keys(messages.en).sort();
  for (const language of Object.keys(languages)) {
    assert.deepEqual(Object.keys(messages[language]).sort(), keys, language);
    assert.ok(Object.values(messages[language]).every((value) => value.trim()), language);
  }
});
