---
description: Prometheus file_sd scrape targets generated from Incus instance labels, written into Prometheus's own volume.
editor: markdown
published: true
title: Prometheus
leafwiki_id: O3n1H3Z3t
leafwiki_title: Prometheus
leafwiki_created_at: "2026-11-15T14:46:23Z"
leafwiki_updated_at: "2026-11-15T14:46:23Z"
leafwiki_creator_id: system
leafwiki_last_author_id: system
---

# Prometheus

Prometheus discovers scrape targets from JSON files with
[`file_sd_configs`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#file_sd_config)
and re-reads them when they change. incus-templated keeps that file in step with
the instances carrying a label: start an exporter and it is scraped, stop it and
it is gone.

Prometheus watches the file itself, so the target has no format, validate or
reload command.

## Layout

```text
.
├── compose.yaml
├── config.yaml
└── templates/
    └── file_sd.json.tmpl
```

## `compose.yaml`

```yaml
name: monitoring

services:
  prometheus:
    image: quay.io/prometheus/prometheus:v3.5.0
    container_name: prometheus
    restart: unless-stopped
    configs:
      - source: prometheus
        target: /etc/prometheus/prometheus.yml
    volumes:
      - prom-sd:/etc/prometheus/sd

  node1:
    image: quay.io/prometheus/node-exporter:v1.9.1
    container_name: node1
    restart: unless-stopped
    labels:
      prom: node
      prom.port: "9100"

  node2:
    image: quay.io/prometheus/node-exporter:v1.9.1
    container_name: node2
    restart: unless-stopped
    labels:
      prom: node
      prom.port: "9100"

  incus-templated:
    image: ghcr.io/jochumdev/incus-templated/incus-templated:1.0.0-beta.2
    container_name: incus-templated
    restart: unless-stopped
    environment:
      INCUS_TEMPLATED_INCUS: "${INCUS_TEMPLATED_INCUS:?Incus HTTPS API endpoint is required, e.g. https://10.0.0.1:8443}"
      INCUS_TEMPLATED_PROJECTS: monitoring
      INCUS_TEMPLATED_CONFIG: /etc/incus-templated/config.yaml
    configs:
      - source: config
        target: /etc/incus-templated/config.yaml
      - source: file_sd
        target: /etc/incus-templated/templates/file_sd.json.tmpl
    secrets:
      - token
    volumes:
      - state:/var/lib/incus-templated

configs:
  config:
    file: ./config.yaml
  file_sd:
    file: ./templates/file_sd.json.tmpl
  prometheus:
    content: |
      global:
        scrape_interval: 15s
      scrape_configs:
        - job_name: incus
          file_sd_configs:
            - files: [/etc/prometheus/sd/*.json]

secrets:
  token:
    environment: INCUS_TOKEN

volumes:
  prom-sd:
  state:
```

`prom-sd` is a named volume, so incus-templated writes the file straight into it
over SFTP, and Prometheus has it from its first start. See
[Architecture](/architecture).

## `config.yaml`

```yaml
templates_dir: templates

targets:
  - label: prom
    instance: prometheus
    project: monitoring
    path: /etc/prometheus/sd/incus.json
    template: file_sd.json.tmpl
    flags:
      job: node
```

`templates_dir` is relative, so it resolves against the config file's directory,
`/etc/incus-templated/templates`. The `job` flag reaches the template as
`.Flags.job`.

## `templates/file_sd.json.tmpl`

```text
[
{{- range $i, $inst := .Instances }}{{ if $i }},{{ end }}
  {
    "targets": [{{ json (printf "%s:%s" ($inst.IPv4 "") $inst.Labels.port) }}],
    "labels": {
      "job": {{ json $.Flags.job }},
      "instance": {{ json $inst.Name }},
      "project": {{ json $inst.Project }},
      "location": {{ json $inst.Location }}
    }
  }
{{- end }}
]
```

`.Labels.port` is the `prom.port` label with the `prom.` prefix cut. `location`
is the cluster member the exporter runs on, `none` on a standalone server. See
[Templates](/templates#template-data) for everything an instance carries.

## Run it

Mint a trust token (see [Getting Started](/getting-started)),
then:

```bash
echo "INCUS_TOKEN=$(incus config trust add --quiet incus-templated)" > .env
INCUS_TEMPLATED_INCUS=https://10.0.0.1:8443 incus-compose up -d
```

The file lands in Prometheus's volume:

```bash
incus-compose exec prometheus cat /etc/prometheus/sd/incus.json
```

```json
[
  {
    "targets": ["10.68.54.185:9100"],
    "labels": {
      "job": "node",
      "instance": "node1",
      "project": "monitoring",
      "location": "none"
    }
  },
  {
    "targets": ["10.68.54.176:9100"],
    "labels": {
      "job": "node",
      "instance": "node2",
      "project": "monitoring",
      "location": "none"
    }
  }
]
```

Both exporters show up as targets, and follow the instances:

```bash
incus-compose exec prometheus wget -qO- http://127.0.0.1:9090/api/v1/targets
incus-compose stop node1   # node1 leaves incus.json and the target list
incus-compose start node1  # and comes back
```

## Notes

- **File mode**: Prometheus runs as `nobody`. The file is written `0644`, or
  with the mode of the file it replaces, so it stays readable.
- **More jobs**: give each its own label and target, writing its own file into
  the same directory, e.g. `label: blackbox` to
  `/etc/prometheus/sd/blackbox.json`. Two targets may not write the same file.
- **Prometheus on the host**: use a local target instead,
  `--target prom,path=/etc/prometheus/sd/incus.json,template=file_sd.json.tmpl,job=node`.
