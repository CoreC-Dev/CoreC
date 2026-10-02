#!/usr/bin/env bash
# check-docs.sh — CoreC 文档防腐校验
#
# 检查项：
#   1. 死链：Markdown 中引用的 .md 文件路径是否存在
#   2. 必填节：AGENTS.md / ARCHITECTURE.md 必须包含规定章节
#   3. 孤儿文档：docs/ 下每个 .md 至少有一条入站链接（sidebar/config/其他文档引用）
#   4. 包注释：所有 Go 包有 // Package 注释（revive package-comments 规则）
#
# 用法：./scripts/check-docs.sh
# 退出码：0=全通过，1=有失败

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

errors=0
report() { echo "  ❌ $1"; errors=$((errors + 1)); }
ok() { echo "  ✅ $1"; }

echo "=== 1. 死链检查 ==="
# Collect all .md files in repo
mapfile -t all_md < <(find . -name '*.md' -not -path './vendor/*' -not -path './.git/*' | sed 's|^\./||')
dead_links=0
for f in "${all_md[@]}"; do
  # Extract markdown links to .md files: [text](path.md) or [text](path)
  while IFS= read -r link; do
    # Skip URLs and anchors-only
    [[ "$link" =~ ^https?:// ]] && continue
    [[ "$link" =~ ^# ]] && continue
    [[ "$link" =~ ^mailto: ]] && continue
    # Strip anchor
    target="${link%%#*}"
    [[ -z "$target" ]] && continue
    # VitePress route: /path → docs/path.md or docs/path/index.md
    if [[ "$target" =~ ^/ ]]; then
      route="${target#/}"
      if [[ -f "docs/${route}.md" || -f "docs/${route}/index.md" || -f "${route}.md" || -f "${route}" ]]; then
        continue
      fi
      echo "  ❌ 死链: $f → $link"
      dead_links=$((dead_links + 1))
      continue
    fi
    # Relative path
    dir=$(dirname "$f")
    resolved="$dir/$target"
    # Normalize
    resolved=$(realpath -q -- "$resolved" 2>/dev/null || echo "")
    if [[ -n "$resolved" && -f "$resolved" ]]; then
      continue
    fi
    # Try appending .md (VitePress convention: ./observability → ./observability.md)
    resolved_md="$dir/${target}.md"
    resolved_md=$(realpath -q -- "$resolved_md" 2>/dev/null || echo "")
    if [[ -n "$resolved_md" && -f "$resolved_md" ]]; then
      continue
    fi
    # Try as-is (might be absolute from repo root)
    if [[ -f "$target" || -f "${target}.md" ]]; then
      continue
    fi
    echo "  ❌ 死链: $f → $link"
    dead_links=$((dead_links + 1))
  done < <(grep -oP '\[.*?\]\(\K[^)]+' "$f" 2>/dev/null || true)
done
if [[ $dead_links -eq 0 ]]; then ok "无死链"; else errors=$((errors + dead_links)); fi

echo "=== 2. 必填节检查 ==="
check_section() {
  local file="$1" section="$2"
  if grep -q "^## $section" "$file" 2>/dev/null; then
    ok "$file 含 '## $section'"
  else
    report "$file 缺少 '## $section'"
  fi
}
check_section "AGENTS.md" "这是什么项目"
check_section "AGENTS.md" "如何开始"
check_section "AGENTS.md" "硬性约束（不可违反）"
check_section "AGENTS.md" "常用命令"
check_section "AGENTS.md" "目录地图"
check_section "AGENTS.md" "工作方式"
check_section "ARCHITECTURE.md" "架构风格"
check_section "ARCHITECTURE.md" "包分层与依赖方向"
check_section "ARCHITECTURE.md" "插件注册机制"
check_section "ARCHITECTURE.md" "核心接口缝隙"
check_section "docs/design-docs/core-beliefs.md" "通用原则（附录 F）"
check_section "docs/QUALITY_SCORE.md" "评分总览"
check_section "docs/QUALITY_SCORE.md" "目标与差距"

echo "=== 3. 孤儿文档检查 ==="
# Check that key docs are referenced somewhere
orphan_count=0
for f in "${all_md[@]}"; do
  [[ "$f" == "docs/index.md" ]] && continue
  [[ "$f" == "AGENTS.md" ]] && continue  # root entry point
  [[ "$f" == "README.md" ]] && continue
  [[ "$f" == "docs/HARNESS-RULES.md" ]] && continue  # construction-phase reference
  [[ "$f" == "docs/license.md" ]] && continue  # linked from nav/footer
  # Search for basename or path in other files + config.ts
  basename=$(basename "$f")
  found=0
  # Check if referenced in any other .md file
  for g in "${all_md[@]}"; do
    [[ "$g" == "$f" ]] && continue
    if grep -q "$basename" "$g" 2>/dev/null; then found=1; break; fi
  done
  # Check VitePress config (by basename or by route path)
  if [[ $found -eq 0 ]] && grep -q "$basename" docs/.vitepress/config.ts 2>/dev/null; then found=1; fi
  # Check VitePress route: docs/guide/intro.md → /guide/intro
  if [[ $found -eq 0 && "$f" == docs/* ]]; then
    route="/${f#docs/}"; route="${route%.md}"
    if grep -q "$route" docs/.vitepress/config.ts 2>/dev/null; then found=1; fi
  fi
  # Check AGENTS.md references
  if [[ $found -eq 0 ]] && grep -q "$basename" AGENTS.md 2>/dev/null; then found=1; fi
  # Check exec-plans references
  if [[ $found -eq 0 ]] && grep -rq "$basename" docs/exec-plans/ 2>/dev/null; then found=1; fi
  if [[ $found -eq 0 ]]; then
    echo "  ⚠️  孤儿文档: $f（无入站链接）"
    orphan_count=$((orphan_count + 1))
  fi
done
if [[ $orphan_count -eq 0 ]]; then ok "无孤儿文档"; else echo "  ⚠️  $orphan_count 个孤儿文档（警告，不阻断）"; fi

echo "=== 4. 包注释检查 ==="
missing_pkg=0
while IFS= read -r dir; do
  [[ -z "$dir" ]] && continue
  # Skip vendor, test-only packages, and nested modules
  [[ "$dir" =~ /vendor/ ]] && continue
  [[ "$dir" =~ /node_modules/ ]] && continue
  # Check if any .go file (non-test) has // Package comment
  has_pkg=0
  for gofile in "$dir"/*.go; do
    [[ -f "$gofile" ]] || continue
    [[ "$gofile" == *_test.go ]] && continue
    if head -1 "$gofile" | grep -q '^// Package '; then has_pkg=1; break; fi
  done
  if [[ $has_pkg -eq 0 ]]; then
    # Check if it's a real package (has non-test .go files)
    has_go=0
    for gofile in "$dir"/*.go; do
      [[ -f "$gofile" ]] && [[ "$gofile" != *_test.go ]] && has_go=1 && break
    done
    if [[ $has_go -eq 1 ]]; then
      echo "  ❌ 缺包注释: $dir"
      missing_pkg=$((missing_pkg + 1))
    fi
  fi
done < <(find . -type d -not -path './.git/*' -not -path './vendor/*' | sed 's|^\./||')
if [[ $missing_pkg -eq 0 ]]; then ok "所有包有注释"; else errors=$((errors + missing_pkg)); fi

echo ""
echo "=== 结果 ==="
if [[ $errors -eq 0 ]]; then
  echo "✅ 文档校验通过（$orphan_count 个孤儿警告）"
  exit 0
else
  echo "❌ 文档校验失败：$errors 个错误"
  exit 1
fi
