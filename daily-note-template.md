# {{.Date}}

## Meetings

```dataview
TABLE WITHOUT ID
  link(file.path, title) as "Meeting",
  description as "Description",
  time as "Time",
  participants as "Participants"
FROM "{{.YearPath}}/{{.MonthPath}}/meetings"
WHERE type = "meeting" AND date = date("{{.Date}}")
SORT time ASC
```
