# Instructions for AI agents

This repository is **Meridian**, a panel for running proxy and VPN servers (Go panel + Go agent +
web UI). What to read depends on the job:

| Job | Read and follow |
| --- | --- |
| Deploy, install, upgrade, back up, move or configure a Meridian panel and its servers | [skills/meridian-deploy/SKILL.md](skills/meridian-deploy/SKILL.md) - step by step, with checks |
| Operate a running panel (users, servers, traffic, alerts) through its MCP tools | [skills/meridian-admin/SKILL.md](skills/meridian-admin/SKILL.md) |
| Change Meridian's code | [CLAUDE.md](CLAUDE.md) - product rules, conventions, `make check` |

When deploying or configuring, never edit Meridian's installed files, its database, or the
configurations it writes on servers: everything is done through the release installer, the
`meridian` command, the REST API and the panel's settings. Ask the human before anything that
disconnects people, and never put passwords, tokens, install commands or subscription links
anywhere but your reply to the human who asked.
