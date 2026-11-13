#!/usr/bin/env python3
"""Check shared D theme integration and preserve pre-migration evidence."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import zipfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    checks = []

    def check(value, label):
        if not value:
            raise SystemExit('FAIL: ' + label)
        checks.append(label)

    spec = importlib.util.spec_from_file_location('ui_generator', ROOT / 'scripts/generate_ui_theme.py')
    generator = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(generator)
    for name, content in generator.generate().items():
        check((ROOT / 'internal/uitheme' / name).read_bytes() == content.encode(), 'generated theme matches canonical source: ' + name)
    theme = (ROOT / 'internal/uitheme/tokens.generated.css').read_text(encoding='utf-8')
    defined = set(re.findall(r'(--mg-[\w-]+)\s*:', theme))
    for filename in ('internal/uitheme/components.css', 'internal/controlled/console.css'):
        css = (ROOT / filename).read_text(encoding='utf-8')
        for name in sorted(set(re.findall(r'var\((--mg-[\w-]+)', css))):
            check(name in defined, filename + ' uses defined token ' + name)
        check(not re.search(r'#[\da-fA-F]{3,8}\b|\brgba?\(', css), filename + ' contains no parallel color palette')
    html = (ROOT / 'internal/controlled/console.html').read_text(encoding='utf-8')
    check('lang="zh-CN"' in html and 'initial-scale=1' in html, 'console declares Chinese and a zoomable viewport')
    check(not re.search(r'<style\b|\sstyle=|\son\w+=|<script(?![^>]*\bsrc=)', html), 'HTML has no inline scripts, styles or handlers')
    check('/web/ui/tokens.css' in html and '/web/ui/components.js' in html, 'real console loads the shared theme and components')
    for filename in ('internal/uitheme/components.js', 'internal/controlled/console.js'):
        source = (ROOT / filename).read_text(encoding='utf-8')
        check(not re.search(r'innerHTML|outerHTML|insertAdjacentHTML|\beval\(|localStorage|sessionStorage|document\.cookie', source), filename + ' avoids HTML injection and sensitive browser persistence')
    source = (ROOT / 'internal/controlled/console.js').read_text(encoding='utf-8')
    for name in ('操作与审批', '资源', '规则', '用户与权限', '本人审计', '通知与设置'):
        check(name in source, 'existing page preserved: ' + name)
    for kind in ('resource', 'rule', 'principal', 'grant', 'reviewer', 'member', 'group', 'groupMember', 'groupGrant', 'groupReviewer'):
        check("'" + kind + "'" in source, 'existing configuration operation preserved: ' + kind)
    for marker in ('assertEpoch', 'detailSeq', 'stateSeq', 'If-Match', 'Idempotency-Key', 'planHash', 'canReview', 'expiresAt'):
        check(marker in source, 'identity, concurrency and plan guard retained: ' + marker)
    archive = ROOT / '.dev/console-d-ui-2026-10-29/original-sources.zip'
    if archive.exists():
        with zipfile.ZipFile(archive) as old:
            before = old.read('internal/controlled/console.go').decode('utf-8').replace('\r\n', '\n')
        after = (ROOT / 'internal/controlled/console.go').read_text(encoding='utf-8').replace('\r\n', '\n')
        marker = 'func (c *Console) login('
        check(before[before.index(marker):] == after[after.index(marker):], 'all login, authorization, decision and configuration backend logic remains byte-equivalent')
    baseline_path = ROOT / '.dev/console-d-ui-2026-10-29/baseline.json'
    preserved = []
    changed = []
    if baseline_path.exists():
        baseline = json.loads(baseline_path.read_text(encoding='utf-8'))['files']
        for name, digest in baseline.items():
            path = ROOT / name
            same = path.exists() and hashlib.sha256(path.read_bytes()).hexdigest() == digest
            historical = name.startswith(('docs/validation/', 'design-system/mcpguard/proposals/', 'design-system/mcpguard/validation/'))
            if historical:
                check(same, 'historical evidence unchanged: ' + name)
                preserved.append(name)
            if not same:
                changed.append(name)
        check(not any(name.startswith(('web/', 'internal/contracts/', 'internal/resources/', 'internal/clientadapter/', 'internal/metadata/')) for name in changed), 'historical Vue prototype, RA business code, contracts and migrations remain unchanged')
    report = {'kind': 'CONSOLE_D_SOURCE_VALIDATION', 'passed': len(checks), 'checks': checks, 'historicalFilesPreserved': len(preserved), 'baselineFilesChanged': changed}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps({'status': 'PASS', 'checks': len(checks), 'historicalFilesPreserved': len(preserved), 'changedBaselineFiles': changed}, ensure_ascii=False))


if __name__ == '__main__':
    main()
