---
date: {{.Date}}
time: {{.Time}}
type: meeting
title: {{.Title}}
description: {{.Description}}
tags:{{range .Tags}}
  - "{{.}}"{{end}}
participants: {{.Participants}}
---

# {{.Title}}

> {{.Description}}

**Transcript**: [[meetings/{{.MeetingID}}-transcript|View Transcript]]

{{.Summary}}
