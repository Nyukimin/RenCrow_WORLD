#!/usr/bin/env bash
# R09 integration check: real SQLite EventStore -> stored projection heads ->
# production projection parsers -> deterministic life assessment.
set -euo pipefail
cd "$(dirname "$0")/.."

go test ./internal/life -run TestE2EProjectionToLifeAssessment -count=1 -v
