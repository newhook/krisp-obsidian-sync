These tags were used across meeting summaries:

{{range .Tags}}
- {{.Tag}} ({{.Count}} occurrences)
{{end}}

Consolidate these into a clean, canonical tag set by:
- Merging synonyms (e.g., 'planning' + 'plan' + 'plans' → 'planning')
- Standardizing format (e.g., '1-on-1' vs 'one-on-one' → '1-on-1')
- Using kebab-case format: lowercase with hyphens instead of spaces (e.g., 'database-design', 'product-roadmap')
- Keeping vocabulary concise (aim for 30-50 core tags)

IMPORTANT: All canonical tags MUST be in kebab-case format (lowercase, hyphens only, no spaces).
