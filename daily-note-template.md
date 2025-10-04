# {{.Date}}

## Meetings

```dataview
TABLE WITHOUT ID
  link(file.path, description) as "Meeting",
  time as "Time",
  participants as "Participants"
FROM "{{.YearPath}}/{{.MonthPath}}/meetings"
WHERE type = "meeting" AND date = date("{{.Date}}")
SORT time ASC
```
