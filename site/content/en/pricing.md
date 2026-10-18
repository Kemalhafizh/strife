---
title: The engine is free. The expertise is what you pay for.
description: The Strife engine is free and open under Apache 2.0. Paid plans cover hands-on testing on your staging, compliance dossiers and support with an SLA.
eyebrow: Pricing
lead: The whole engine (simulator, checkers, agent, CLI) is Apache 2.0 and will stay open. What we offer is the expertise to run it against your systems and take the results into an audit.
layout: pricing
translationKey: pricing
plans:
  - name: Community
    price: Free
    note: Forever, open source (Apache 2.0)
    features:
      - Deterministic simulator, checkers, shrinking and regression campaigns
      - "Real clusters: etcd, PostgreSQL, MySQL/TiDB, Valkey, Kafka, Kubernetes"
      - strife-agent, the GitHub Action, interactive HTML reports
      - Signatures and compliance dossiers with your own keys
      - Community support
    cta: Download
    link: /download
  - name: Assurance
    price: Scoped to your systems
    note: Annual contract, for one team and its staging environment
    featured: true
    features:
      - Hands-on setup of tests on your staging
      - Test scenarios for your architecture (databases, replication, message queues)
      - Findings reviewed with our engineers, down to the root cause
      - Compliance dossiers assembled and reviewed with you for your audit
      - Support with an SLA during business hours
    cta: Book a demo
    link: /contact
  - name: Enterprise
    price: Talk to us
    note: For several teams, environments or core systems
    features:
      - Everything in Assurance
      - Custom protocol clients and storage adapters for internal systems
      - Regression gates in your release pipeline (CI/CD)
      - A managed runner inside your network <span class="soon">in development</span>
      - On-site workshops for engineering and risk teams
    cta: Talk to us
    link: /contact
---

## Frequently asked questions

### Is Strife really free?

Yes. The full engine is Apache 2.0: you may use it, change it and run it in production without paying. What we charge for is expertise, dossiers and support.

### How do we buy?

Through a written quote and an invoice, following your company's procurement process. No credit card needed.

### What does it cost?

That depends on how many systems and environments are tested. We send a quote after a first conversation, which is free and commits you to nothing.

### Is our data sent to Strife?

No. Tests run on your infrastructure, and we work with your team inside your environment. See [Security & data](/security).

### Does a dossier mean we pass the audit?

No. A dossier is technical evidence for an auditor, not a certification. Its limits are spelled out on the [Compliance](/compliance) page.
