"""Use the installed DeepSeek Harness registry and executor in isolated MCP fixtures."""

import json
import os
from pathlib import Path
import queue
import subprocess
import threading


SCRIPTS = Path(__file__).resolve().parent
EXECUTABLE = 'DeepSeek Harness.exe'
WIRE_PREFIX = 'MCPGUARD_DSH '


def install_path():
    configured = os.environ.get('MCPGUARD_DSH_INSTALL')
    if configured:
        return Path(configured)
    if os.name == 'nt':
        local_app_data = Path(os.environ['LOCALAPPDATA'])
    else:
        windows_app_data = subprocess.check_output(
            ['/mnt/c/Windows/System32/cmd.exe', '/d', '/c', 'echo %LOCALAPPDATA%'],
            cwd='/mnt/c', text=True, timeout=10,
        ).strip()
        local_app_data = Path(subprocess.check_output(
            ['wslpath', '-u', windows_app_data], text=True, timeout=10,
        ).strip())
    return local_app_data / 'Programs' / 'DeepSeek Harness'


def windows(path):
    if os.name == 'nt':
        return str(Path(path).resolve())
    return subprocess.check_output(['wslpath', '-w', str(path)], text=True, timeout=10).strip()


def runtime_env(install=None, home=None):
    install = Path(install) if install is not None else install_path()
    home = Path(home) if home is not None else SCRIPTS.parent / '.dev' / 'dsh-runtime'
    private_env = {
        'ELECTRON_RUN_AS_NODE': '1',
        'DSH_TELEMETRY': 'off',
        'DSH_HOME': windows(home),
        'MCPGUARD_DSH_ASAR': windows(install / 'resources' / 'app.asar'),
    }
    env = {**os.environ, **private_env}
    if os.name != 'nt':
        entries = [key for key in env.get('WSLENV', '').split(':') if key and key.split('/')[0] not in private_env]
        env['WSLENV'] = ':'.join(entries + [key + '/w' for key in private_env])
    return env


class DSHClient:
    """Keeps one native MCP registry/executor alive for an entire fixture."""

    def __init__(self, config, log, *, install=None, workspace=None, home=None):
        install = Path(install) if install is not None else install_path()
        executable = install / EXECUTABLE
        if not executable.is_file():
            raise FileNotFoundError('DeepSeek Harness is missing from the install path; set MCPGUARD_DSH_INSTALL')
        self.pending = queue.Queue()
        self.process = subprocess.Popen(
            [str(executable), '--expose-internals', windows(SCRIPTS / 'dsh-tool-host.mjs')],
            cwd=workspace, env=runtime_env(install, home), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log,
            text=True, encoding='utf-8',
        )
        self.reader = threading.Thread(target=self._collect, daemon=True)
        self.reader.start()
        try:
            self.send(config)
        except (OSError, ValueError):
            self.close()
            raise

    def _collect(self):
        try:
            for line in self.process.stdout:
                if line.startswith(WIRE_PREFIX):
                    self.pending.put(json.loads(line.removeprefix(WIRE_PREFIX)))
        except (OSError, ValueError):
            self.pending.put({'event': 'failed', 'phase': 'receipt'})
        finally:
            self.pending.put({'event': 'closed', 'phase': 'process'})

    def send(self, value):
        self.process.stdin.write(json.dumps(value, ensure_ascii=True) + '\n')
        self.process.stdin.flush()

    def receive(self, wanted, *, timeout=24):
        try:
            message = self.pending.get(timeout=timeout)
        except queue.Empty as error:
            raise AssertionError('Timed out waiting for installed Harness result') from error
        if message.get('event') != wanted:
            failures = ' / '.join(message.get('failureTypes', []))
            detail = f' ({failures})' if failures else ''
            raise AssertionError(f"Harness {wanted} failed during {message.get('phase', 'startup')}{detail}")
        return message

    def call(self, tool, args):
        self.send({'tool': tool, 'args': args})
        return self.receive('result')

    def close(self):
        if not self.process.stdin.closed:
            try:
                self.process.stdin.close()
            except BrokenPipeError:
                pass
        try:
            self.process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=6)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=6)
        self.reader.join(2)
        self.process.stdout.close()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, traceback):
        self.close()
