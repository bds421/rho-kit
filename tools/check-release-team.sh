#!/usr/bin/env bash
set -euo pipefail

# Verify release-time team and branch-protection invariants before tagging.
#
# CODEOWNERS lists @bds421/security as the required reviewer for security-
# sensitive files. That reference is decorative until two things are true on
# the actual GitHub org:
#
#   1. The team @bds421/security exists and has at least one member.
#   2. Branch protection on `main` requires CODEOWNERS reviews.
#
# This script proves both via `gh api`. It is a release-time preflight, not a
# CI gate — running it requires `gh` to be authenticated with a token that can
# read the org and repo branch-protection settings.

ORG="${RHO_KIT_ORG:-bds421}"
TEAM="${RHO_KIT_SECURITY_TEAM:-security}"
REPO="${RHO_KIT_REPO:-$ORG/rho-kit}"
BRANCH="${RHO_KIT_RELEASE_BRANCH:-main}"

if ! command -v gh >/dev/null 2>&1; then
  echo "ERROR: gh CLI is not installed; cannot verify release team." >&2
  echo "       Install via https://cli.github.com or skip with RHO_KIT_SKIP_TEAM_CHECK=1." >&2
  exit 2
fi

if [[ "${RHO_KIT_SKIP_TEAM_CHECK:-0}" == "1" ]]; then
  echo "WARN: RHO_KIT_SKIP_TEAM_CHECK=1 set; skipping release-team verification."
  exit 0
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "ERROR: gh is not authenticated; run 'gh auth login' first." >&2
  exit 2
fi

team_path="orgs/$ORG/teams/$TEAM"
if ! gh api "$team_path" >/dev/null 2>&1; then
  echo "FAIL: team @$ORG/$TEAM does not exist or is not visible." >&2
  echo "      Create it before tagging; CODEOWNERS references it." >&2
  exit 1
fi

members="$(gh api "$team_path/members" --jq 'length' 2>/dev/null || echo 0)"
if [[ "$members" -lt 1 ]]; then
  echo "FAIL: team @$ORG/$TEAM has 0 members; review enforcement will block every PR." >&2
  exit 1
fi

protection_path="repos/$REPO/branches/$BRANCH/protection"
protection_json="$(gh api "$protection_path" 2>/dev/null || echo '{}')"
if [[ "$protection_json" == '{}' ]]; then
  echo "FAIL: branch protection on $REPO@$BRANCH is not configured." >&2
  echo "      Enable required reviews + CODEOWNERS enforcement before tagging." >&2
  exit 1
fi

requires_codeowners="$(printf '%s' "$protection_json" | jq -r '.required_pull_request_reviews.require_code_owner_reviews // false')"
if [[ "$requires_codeowners" != "true" ]]; then
  echo "FAIL: branch protection on $REPO@$BRANCH does NOT require CODEOWNERS reviews." >&2
  echo "      CODEOWNERS coverage is not enforced; security-sensitive files can merge without review." >&2
  exit 1
fi

echo "OK: team @$ORG/$TEAM exists ($members member(s)); CODEOWNERS enforced on $REPO@$BRANCH."
