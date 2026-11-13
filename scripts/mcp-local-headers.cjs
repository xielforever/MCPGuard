// stdout is reserved for the private HTTP authentication header object.
const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');
const root = path.resolve(__dirname, '..');
try {
  if (process.platform !== 'win32') throw new Error('Use the Windows local service launcher.');
  cp.execFileSync('powershell.exe', [
    '-NoProfile', '-NonInteractive', '-File', path.join(__dirname, 'local-service.ps1'), '-Action', 'Start',
  ], {windowsHide: true, timeout: 80000, stdio: ['ignore', 'pipe', 'pipe']});
  const cfg = JSON.parse(fs.readFileSync(path.join(root, '.dev', 'env.json'), 'utf8'));
  if (cfg.MCPGUARD_AUTH_MODE !== 'dev-tokens' || cfg.MCPGUARD_LISTEN !== '127.0.0.1:18080'
      || typeof cfg.alice_token !== 'string' || cfg.alice_token.length < 32) throw new Error('Invalid local identity configuration.');
  process.stdout.write(JSON.stringify({Authorization: 'Bearer ' + cfg.alice_token}) + '\n');
} catch {
  process.stderr.write('MCPGuard local startup or private credentials are unavailable; inspect .dev/local-service.\n');
  process.exitCode = 1;
}
