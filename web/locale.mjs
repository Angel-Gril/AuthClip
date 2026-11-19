import { translations } from "./translations.mjs";

export const languages = {
  en: "English",
  "zh-Hans": "简体中文",
  "zh-Hant": "繁體中文",
  ja: "日本語",
  ko: "한국어",
  es: "Español",
  pt: "Português",
  fr: "Français",
  de: "Deutsch",
  ar: "العربية",
  id: "Bahasa Indonesia",
  vi: "Tiếng Việt",
};

export const messages = Object.fromEntries(Object.keys(languages).map((locale, index) => [
  locale,
  Object.fromEntries(Object.entries(translations).map(([key, row]) => [key, row[index]])),
]));

const regionalLanguages = {
  "zh-Hans": "CN SG MY",
  "zh-Hant": "TW HK MO",
  en: "US GB AU NZ IE IN PH ZA NG KE GH PK BD LK NP",
  ja: "JP",
  ko: "KR KP",
  es: "ES MX AR BO CL CO CR CU DO EC GQ GT HN NI PA PE PR PY SV UY VE",
  pt: "PT BR AO CV GW MZ ST TL",
  fr: "FR MC BF BJ CF CG CI CM CD DJ GA GN ML NE SN TD TG",
  de: "DE AT LI",
  ar: "AE BH DZ EG IQ JO KM KW LB LY MA MR OM PS QA SA SD SO SY TN YE",
  id: "ID",
  vi: "VN",
};
const regionLanguages = Object.fromEntries(Object.entries(regionalLanguages).flatMap(
  ([locale, codes]) => codes.split(" ").map((code) => [code, [locale]]),
));
Object.assign(regionLanguages, {
  CA: ["en", "fr"], CH: ["de", "fr"], BE: ["fr", "de"], HK: ["zh-Hant", "en"],
  MO: ["zh-Hant", "pt"], SG: ["en", "zh-Hans"], MY: ["en", "zh-Hans"],
  LU: ["fr", "de"], RW: ["en", "fr"], BI: ["fr", "en"], IL: ["ar", "en"],
});

function supportedBrowserLanguage(tag) {
  const parts = tag.toLowerCase().split("-");
  if (parts[0] === "zh") {
    return parts.some((part) => ["hant", "tw", "hk", "mo"].includes(part)) ? "zh-Hant" : "zh-Hans";
  }
  return Object.hasOwn(languages, parts[0]) ? parts[0] : undefined;
}

export function chooseLanguage(manual, country, preferred) {
  if (Object.hasOwn(languages, manual)) return manual;
  const browser = preferred.map(supportedBrowserLanguage).filter(Boolean);
  const options = regionLanguages[country] ?? [];
  return browser.find((locale) => options.includes(locale)) ?? options[0] ?? browser[0] ?? "en";
}

export const regions = (
  "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ " +
  "CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR " +
  "GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP " +
  "KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS " +
  "MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS " +
  "RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ " +
  "UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW"
).split(" ");
