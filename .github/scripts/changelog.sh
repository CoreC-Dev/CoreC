#!/bin/bash
# Release Changelog & Download Table Generator
#
# Parses Conventional Commits between two tags, generates categorized changelog,
# and appends a shields.io badge download table.
#
# Usage: changelog.sh <current_tag>
#   e.g. changelog.sh v1.0.0

set -euo pipefail

CURRENT_TAG="${1:-}"
if [ -z "$CURRENT_TAG" ]; then
    echo "Usage: changelog.sh <current_tag>"
    exit 1
fi

VERSION="${CURRENT_TAG#v}"
SHIELDS="https://img.shields.io/badge"
BASE_URL="https://github.com/${GITHUB_REPOSITORY}/releases/download/${CURRENT_TAG}"

# ============================================================
# Part 1: Conventional Commits Changelog
# ============================================================

# Find the previous tag
PREV_TAG=$(git describe --tags --abbrev=0 "${CURRENT_TAG}^" 2>/dev/null || echo "")

if [ -n "$PREV_TAG" ]; then
    RANGE="${PREV_TAG}..${CURRENT_TAG}"
    echo "Generating changelog: ${RANGE}" >&2
else
    RANGE="${CURRENT_TAG}"
    echo "Generating changelog: first release (${CURRENT_TAG})" >&2
fi

# Category definitions: prefix => (emoji, title)
declare -a CATEGORIES=(
    "feat|🚀|Features"
    "fix|🐛|Bug Fixes"
    "perf|⚡|Performance"
    "refactor|♻️|Code Refactoring"
    "docs|📝|Documentation"
    "test|✅|Tests"
    "ci|👷|CI/CD"
    "build|📦|Build"
    "chore|🔧|Maintenance"
    "style|🎨|Style"
)

declare -A CAT_LINES
for cat in feat fix perf refactor docs test ci build chore style; do
    CAT_LINES[$cat]=""
done

OTHER_LINES=""

while IFS= read -r commit_line; do
    subject=$(echo "$commit_line" | sed 's/^[a-f0-9]* //')

    [[ "$subject" =~ ^Merge ]] && continue

    cc_regex='^([a-z]+)(\([^)]+\))?(!)?:\ (.+)$'
    if [[ "$subject" =~ $cc_regex ]]; then
        type="${BASH_REMATCH[1]}"
        scope="${BASH_REMATCH[2]}"
        breaking_marker="${BASH_REMATCH[3]}"
        desc="${BASH_REMATCH[4]}"

        scope_name="${scope#(}"; scope_name="${scope_name%)}"
        if [ -n "$scope" ]; then
            formatted="- **${scope_name}** ${desc}"
        else
            formatted="- ${desc}"
        fi
        [ -n "$breaking_marker" ] && formatted="${formatted} ⚠️ **BREAKING**"

        found=false
        for cat_def in "${CATEGORIES[@]}"; do
            IFS='|' read -r cat_prefix cat_emoji cat_title <<< "$cat_def"
            if [ "$type" = "$cat_prefix" ]; then
                CAT_LINES[$type]="${CAT_LINES[$type]}${formatted}"$'\n'
                found=true; break
            fi
        done
        $found || OTHER_LINES="${OTHER_LINES}- ${subject}"$'\n'
    else
        OTHER_LINES="${OTHER_LINES}- ${subject}"$'\n'
    fi
done < <(git log --format="%h %s" "$RANGE" 2>/dev/null)

# --- Output changelog ---
echo "## Release v${VERSION}"
echo ""
total_commits=$(git log --oneline "$RANGE" 2>/dev/null | grep -v "^.*Merge" | wc -l | tr -d ' ')
echo "> ${total_commits} commits since ${PREV_TAG:-initial commit}"
echo ""

has_content=false
for cat_def in "${CATEGORIES[@]}"; do
    IFS='|' read -r cat_prefix cat_emoji cat_title <<< "$cat_def"
    lines="${CAT_LINES[$cat_prefix]}"
    if [ -n "$lines" ]; then
        echo "### ${cat_emoji} ${cat_title}"
        echo ""
        echo "$lines" | sed '/^$/d'
        echo ""
        has_content=true
    fi
done

if [ -n "$OTHER_LINES" ]; then
    echo "### 📦 Other Changes"
    echo ""
    echo "$OTHER_LINES" | sed '/^$/d'
    echo ""
    has_content=true
fi

if ! $has_content; then
    echo "_No changes found in this range._"
    echo ""
fi

echo "---"
echo ""

# ============================================================
# Part 2: Download Table (shields.io badges)
# ============================================================

echo "## 📦 Downloads"
echo ""
echo "Download based on your OS:"
echo ""
echo "| OS | 下载 |"
echo "|:---|:-----|"

# Windows — blue #0078D6
echo "| Windows | [![x64](${SHIELDS}/x64-0078D6?style=flat-square&logo=windows)](${BASE_URL}/corec-v${VERSION}-windows-amd64.zip) [![arm64](${SHIELDS}/arm64-0078D6?style=flat-square&logo=windows)](${BASE_URL}/corec-v${VERSION}-windows-arm64.zip) |"

# macOS — black #000000
echo "| macOS | [![Apple_Silicon](${SHIELDS}/Apple_Silicon-000000?style=flat-square&logo=apple)](${BASE_URL}/corec-v${VERSION}-darwin-arm64.tar.gz) [![Intel_x64](${SHIELDS}/Intel_x64-000000?style=flat-square&logo=apple)](${BASE_URL}/corec-v${VERSION}-darwin-amd64.tar.gz) |"

# Linux — Ubuntu orange #E95420
echo "| Linux | [![x64](${SHIELDS}/x64-E95420?style=flat-square&logo=linux)](${BASE_URL}/corec-v${VERSION}-linux-amd64.tar.gz) [![arm64](${SHIELDS}/arm64-E95420?style=flat-square&logo=linux)](${BASE_URL}/corec-v${VERSION}-linux-arm64.tar.gz) [![armv7](${SHIELDS}/armv7-E95420?style=flat-square&logo=linux)](${BASE_URL}/corec-v${VERSION}-linux-arm-arm7.tar.gz) |"

echo ""
echo "校验文件: [![checksums](${SHIELDS}/checksums.txt-blue?style=flat-square)](${BASE_URL}/checksums.txt)"
echo ""
echo "验证下载完整性:"
echo ""
echo '```bash'
echo "sha256sum -c checksums.txt --ignore-missing"
echo '```'
