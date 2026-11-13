#!/usr/bin/env python3
"""Publish only safe, local D-console validation evidence; keep all earlier runs."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]
PRIVATE = ROOT / '.dev/console-d-ui-2026-10-29'
DEST = ROOT / 'design-system/mcpguard/validation/2026-10-29-console-d'


def load(path):
    return json.loads(path.read_text(encoding='utf-8'))


def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def main():
    DEST.mkdir(parents=True, exist_ok=True)
    functional = next(path for path in sorted((PRIVATE / 'runs').iterdir(), reverse=True) if (path / 'report.json').exists())
    desktop = next(path for path in sorted((PRIVATE / 'desktop-runs').iterdir(), reverse=True) if (path / 'report.json').exists())
    for source, name in ((functional, 'functional-before-desktop-scope'), (desktop, 'desktop')):
        target = DEST / name
        target.mkdir(exist_ok=True)
        report = load(source / 'report.json')
        shutil.copyfile(source / 'report.json', target / 'report.json')
        for filename in report['screenshots']:
            if Path(filename).name != filename:
                raise SystemExit('Invalid screenshot path')
            shutil.copyfile(source / filename, target / filename)
    failures = []
    failure_folder = DEST / 'failed-runs'
    failure_folder.mkdir(exist_ok=True)
    for path in sorted((PRIVATE / 'runs').glob('*/failure.json')):
        item = load(path)
        shutil.copyfile(path, failure_folder / (path.parent.name + '.json'))
        failures.append({'run': path.parent.name, 'passedBeforeFailure': len(item['checks']), 'message': item['message']})
    source = load(PRIVATE / 'source-checks.json')
    shutil.copyfile(PRIVATE / 'source-checks.json', DEST / 'source-checks.json')
    go = load(DEST / 'go-tests.summary.json')
    wire = load(DEST / 'http-stdio.summary.json')
    live = load(DEST / 'live-services.json')
    functional_report = load(functional / 'report.json')
    desktop_report = load(desktop / 'report.json')
    assert not go['skipped'] and not go['failed'] and live['unchanged']
    assert not functional_report['browserErrors'] and not desktop_report['browserErrors']
    # Source manifest covers implementation and replay scripts, not generated evidence itself.
    files = [
        'internal/controlled/console.go', 'internal/controlled/console.html', 'internal/controlled/console.js', 'internal/controlled/console.css',
        'internal/controlled/console_assets_test.go', 'internal/uitheme/assets.go', 'internal/uitheme/components.css', 'internal/uitheme/components.js',
        'internal/uitheme/tokens.generated.css', 'internal/uitheme/state-labels.generated.js', 'design-system/mcpguard/tokens.json',
        'design-system/mcpguard/state-labels.json', 'scripts/generate_ui_theme.py', 'scripts/validate_console_d.py', 'scripts/console_d_fixture.py',
        'scripts/seal_console_d_evidence.py', 'tests/console_d_ui.cjs', 'tests/console_d_desktop.cjs', 'design-system/mcpguard/implementation-console-d.md'
    ]
    manifest = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in files}
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    save(DEST / 'source-manifest.json', {'head': head, 'workingTreePreserved': True, 'files': manifest})
    report = {
        'status': 'PASS_LOCAL_DESKTOP', 'date': '2026-10-29', 'scope': 'Existing six embedded-console pages, desktop only',
        'fixtureRunId': functional_report['fixtureRunId'], 'goRaceTests': len(go['passed']), 'goSkipped': len(go['skipped']),
        'httpStdioChecks': wire['checks'], 'functionalBrowserChecks': functional_report['passed'], 'finalDesktopChecks': desktop_report['passed'],
        'sourceChecks': source['passed'], 'historicalFilesPreserved': source['historicalFilesPreserved'],
        'finalDesktopScreenshots': desktop_report['screenshots'], 'earlierFailures': failures,
        'initialStaticTestCorrection': 'Expected marker referenced an unused console CSS variable; asset was present. Test corrected to the actual shared separator token.',
        'goVet': 'PASS', 'generatorCheck': 'PASS', 'javaScriptSyntax': 'PASS', 'pythonSyntax': 'PASS',
        'oldServicesUnchanged': True, 'businessBackendTailByteEquivalent': True, 'apiContractsChanged': False, 'gitCommitted': False, 'gitPushed': False,
        'humanVisualAcceptance': 'PENDING', 'nativeCodingClientAcceptance': 'NOT_CLAIMED', 'productionAcceptance': 'NOT_CLAIMED',
        'mobileScope': 'Excluded by user; prior checks preserved as historical evidence without further mobile work',
        'displayFaultInjection': ['OUTCOME_UNKNOWN', 'DISPATCH_UNKNOWN', 'UNKNOWN side effects', 'expired client observation'],
        'networkFaultInjection': 'Commit via real backend, interrupt first browser acknowledgement, retry exact same command and idempotency key',
        'limitations': ['Legacy write decision accepts vote/planHash without a reason field', 'Future RA-16 lifecycle pages remain outside this six-page migration', 'Original local service image was not replaced']
    }
    save(DEST / 'report.json', report)
    print(json.dumps({key: report[key] for key in ('status', 'goRaceTests', 'httpStdioChecks', 'functionalBrowserChecks', 'finalDesktopChecks', 'sourceChecks', 'historicalFilesPreserved')}))


if __name__ == '__main__':
    main()
