// Fails when a link or #anchor within the built site leads nowhere: a page,
// asset or example report that was not generated, or a heading that moved.
//
//   node scripts/check-links.cjs <public dir> <base URL>
"use strict";
const fs = require("fs");
const path = require("path");

const root = path.resolve(process.argv[2]);
const baseURL = new URL(process.argv[3]);
const base = baseURL.pathname.endsWith("/") ? baseURL.pathname : baseURL.pathname + "/";

const pages = [];
(function walk(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p);
    else if (e.name.endsWith(".html")) pages.push(p);
  }
})(root);

const attr = (name) => new RegExp(`\\s(?:${name})=(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, "g");
const value = (m) => m[1] ?? m[2] ?? m[3];

const idCache = new Map();
function ids(file) {
  if (!idCache.has(file)) {
    const html = fs.readFileSync(file, "utf8");
    idCache.set(file, new Set([...html.matchAll(attr("id|name"))].map(value)));
  }
  return idCache.get(file);
}

// The file a URL path under the base is served from, or null.
function served(urlPath) {
  if (!urlPath.startsWith(base)) return null;
  const rel = decodeURIComponent(urlPath.slice(base.length));
  const candidates = rel === "" || rel.endsWith("/") ? [rel + "index.html"] : [rel, rel + "/index.html"];
  for (const c of candidates) {
    const f = path.join(root, c);
    if (fs.existsSync(f) && fs.statSync(f).isFile()) return f;
  }
  return null;
}

let links = 0;
const broken = new Set();
for (const page of pages) {
  const rel = path.relative(root, page).split(path.sep).join("/");
  const pageURL = new URL(base + rel.replace(/(^|\/)index\.html$/, "$1"), baseURL.origin);
  const html = fs.readFileSync(page, "utf8")
    .replace(/<script\b[\s\S]*?<\/script>/g, "") // data and code, not links
    .replace(/\ssrcdoc=("[^"]*"|'[^']*')/g, ""); // embedded reports
  for (const m of html.matchAll(attr("href|src"))) {
    const raw = value(m).replace(/&amp;/g, "&");
    if (/^(mailto:|tel:|javascript:|data:)/.test(raw)) continue;
    let u;
    try {
      u = new URL(raw, pageURL);
    } catch {
      broken.add(`${rel}: unparsable link ${raw}`);
      continue;
    }
    if (u.origin !== baseURL.origin || !u.pathname.startsWith(base)) continue;
    links++;
    const target = served(u.pathname);
    if (!target) broken.add(`${rel} -> ${u.pathname} (not generated)`);
    else if (u.hash && target.endsWith(".html") && !ids(target).has(decodeURIComponent(u.hash.slice(1))))
      broken.add(`${rel} -> ${u.pathname}${u.hash} (no such anchor)`);
  }
}

if (broken.size) {
  console.error([...broken].sort().join("\n"));
  console.error(`check-links: ${broken.size} broken link(s) out of ${links}`);
  process.exit(1);
}
console.log(`check-links: ${links} links in ${pages.length} pages, all resolve`);
