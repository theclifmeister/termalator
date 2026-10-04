#!/bin/sh
# Statusline probe: log the JSON Claude sends, print a short line.
in=$(cat)
printf '%s %s\n' "$(date +%s.%N 2>/dev/null || date +%s)" "$in" >> "${TERMALATOR_STATUS_LOG:-/dev/null}"
echo "termalator-status"
