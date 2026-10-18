// The contact form needs no server of ours: it opens the visitor's email or
// WhatsApp with the message filled in, or posts to a form service when one
// is configured (site.Params.contact in hugo.toml).
(() => {
  "use strict";
  const form = document.getElementById("contact");
  if (!form) return;
  const status = document.getElementById("contact-status");
  const strings = JSON.parse(document.getElementById("strings").textContent);
  const t = (k) => strings[k] || k;
  const fields = ["name", "company", "role", "email", "phone", "interest", "message"];
  const labels = { name: "contact_name", company: "contact_company", role: "contact_role", email: "contact_email", phone: "contact_phone", interest: "contact_interest", message: "contact_message" };

  function values() {
    const v = {};
    for (const f of fields) v[f] = (form.elements[f].value || "").trim();
    return v;
  }

  function body(v) {
    return fields.filter((f) => v[f]).map((f) => `${t(labels[f])}: ${v[f]}`).join("\n");
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!form.reportValidity()) return;
    const via = (e.submitter && e.submitter.dataset.via) || "email";
    const v = values();
    const subject = `${t("contact_subject")} ${v.company || v.name}`;
    if (via === "whatsapp") {
      window.open(`https://wa.me/${form.dataset.whatsapp}?text=${encodeURIComponent(`${subject}\n\n${body(v)}`)}`, "_blank", "noopener");
      return;
    }
    if (via === "form" && form.dataset.endpoint) {
      try {
        const resp = await fetch(form.dataset.endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/json", Accept: "application/json" },
          body: JSON.stringify({ ...v, _subject: subject }),
        });
        if (!resp.ok) throw new Error(String(resp.status));
        form.reset();
        status.textContent = t("contact_sent");
      } catch {
        status.textContent = t("contact_error");
      }
      return;
    }
    location.href = `mailto:${form.dataset.email}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body(v))}`;
  });
})();
