// Points the main download button at the archive for the visitor's system.
(async () => {
  "use strict";
  const box = document.getElementById("dl");
  const best = document.getElementById("dl-best");
  if (!box || !best) return;
  const assets = JSON.parse(box.dataset.assets || "[]");
  const label = document.getElementById("dl-best-label");
  const names = { linux: "Linux", darwin: "macOS", windows: "Windows", amd64: "x86-64", arm64: "ARM64" };

  let os = "linux";
  let arch = "amd64";
  const ua = navigator.userAgent;
  if (/Windows/i.test(ua)) os = "windows";
  else if (/Mac OS X|Macintosh/i.test(ua)) { os = "darwin"; arch = "arm64"; }
  if (/aarch64|arm64/i.test(ua)) arch = "arm64";
  const uad = navigator.userAgentData;
  if (uad && uad.getHighEntropyValues) {
    try {
      const v = await uad.getHighEntropyValues(["architecture", "bitness", "platform"]);
      if (v.platform === "Windows") os = "windows";
      else if (v.platform === "macOS") os = "darwin";
      else if (v.platform === "Linux") os = "linux";
      if (v.architecture === "arm") arch = "arm64";
      else if (v.architecture === "x86") arch = "amd64";
    } catch { /* keep the user-agent guess */ }
  }
  // An Intel Mac cannot be told apart from Apple silicon by its user agent;
  // the table below lists both.
  const pick = assets.find((a) => a.os === os && a.arch === arch);
  if (!pick) return;
  best.href = pick.url;
  label.textContent = `${names[os]} ${names[arch]}`;
  document.getElementById("dl-os").textContent = pick.name;
})();
