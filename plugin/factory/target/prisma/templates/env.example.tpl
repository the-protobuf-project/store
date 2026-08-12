# Connection URL for the {{.Database}} database.
#
# Copy this file to .env and replace the placeholder with your real credentials:
#
#     cp .env.example .env
#
# .env is git-ignored and is never generated, so your edits survive the next
# `buf generate`. This file is regenerated every time and is committed, so keep
# real credentials out of it. {{.Database}}.config.ts reads the value via
# env("{{.EnvVar}}").
{{.EnvVar}}="{{.URL}}"
