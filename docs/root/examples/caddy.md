---
description: Caddy reverse proxy routes generated from instance labels with the built-in preset.
editor: markdown
published: true
title: Caddy
leafwiki_id: JOQbf-iFR
leafwiki_title: Caddy
leafwiki_created_at: "2026-11-15T14:46:23Z"
leafwiki_updated_at: "2026-11-15T14:46:23Z"
leafwiki_creator_id: system
leafwiki_last_author_id: system
---

# Caddy

The Caddy preset is the walkthrough in [Getting Started](/getting-started): a
Caddy instance, incus-templated, and an application whose label becomes a site
block. A target needs nothing beyond the label, the instance and its project:

```bash
--target edge,preset=caddy,instance=caddy,project=default
```

- [Caddy Labels & Routing](/labels) - the label syntax, load balancing,
  redirects and network selection.
- [Templates](/templates#caddy-preset) - the preset's template and per-vhost
  templates.
- [Deployment & Operations](/deployment) - a production stack, several Caddy
  servers, and troubleshooting.
