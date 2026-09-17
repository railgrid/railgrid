#!/bin/sh
# Copyright 2026 The Railgrid Authors.
# Disposable, local-only acceptance fixture. No credentials leave this script.
set -eu

case "$(uname -s)" in
  Darwin) host_os=macos ;;
  Linux) host_os=linux ;;
  *)
    echo 'This fixture setup is for MacOS or Linux.' >&2
    exit 1
    ;;
esac
command -v git >/dev/null
command -v openssl >/dev/null
command -v codex >/dev/null
command -v python3 >/dev/null
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY
unset GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
runner_binary="$script_dir/railgrid-runner"
if [ ! -x "$runner_binary" ]; then
  echo 'Place setup.sh beside the matching railgrid-runner binary.' >&2
  exit 1
fi

fixture_root="${RAILGRID_RUNNER_FIXTURE_ROOT:-$HOME/.railgrid-runner-preview}"
umask 077
mkdir -p "$fixture_root"
fixture_root=$(CDPATH='' cd -- "$fixture_root" && pwd)
if [ ! -f "$fixture_root/token" ]; then
  openssl rand -hex 32 > "$fixture_root/token"
fi
chmod 600 "$fixture_root/token"
mkdir -p "$fixture_root/source" "$fixture_root/codex-home" "$fixture_root/state"
if [ ! -d "$fixture_root/source/.git" ]; then
  printf '#!/bin/sh\nprintf "TODO\\n"\n' > "$fixture_root/source/greet.sh"
  cat > "$fixture_root/source/verify.sh" <<'EOF'
#!/bin/sh
set -eu
test "$(sh ./greet.sh)" = 'Hello from the runner.'
printf 'fixture check passed\n'
EOF
  git -C "$fixture_root/source" -c init.defaultBranch=main init --quiet
  git -C "$fixture_root/source" add greet.sh verify.sh
  git -C "$fixture_root/source" -c user.name='Runner Fixture' \
    -c user.email='fixture@localhost' -c core.hooksPath=/dev/null \
    -c commit.gpgSign=false commit --quiet -m 'Add disposable runner fixture'
fi
base_commit=$(git -C "$fixture_root/source" rev-parse HEAD)
python3 - "$fixture_root" "$base_commit" "$host_os" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
config = {
    'protocolVersion': 'runner/v1',
    'runnerID': sys.argv[3] + '-acceptance-runner',
    'stateDir': str(root / 'state'),
    'tokenFile': str(root / 'token'),
    'toolchains': ['git', 'sh'],
    'verificationCapabilities': ['fixture-shell'],
    'repositories': {'acceptance-fixture': {
        'source': str(root / 'source'), 'baseCommit': sys.argv[2]}},
}
(root / 'runner.json').write_text(json.dumps(config, indent=2) + '\n')
PY

printf 'Fixture base commit: %s\n' "$base_commit"
printf 'Runner token file (keep private): %s/token\n' "$fixture_root"
printf 'Copy the token locally into your Railgrid Service credential field. Do not send it in chat.\n'
printf 'If the dedicated Codex home is not signed in, run:\n'
printf '  CODEX_HOME="%s/codex-home" codex login\n' "$fixture_root"
printf 'Then start or restart the runner with:\n'
printf '  "%s" --config "%s/runner.json" --codex-home "%s/codex-home"\n' "$runner_binary" "$fixture_root" "$fixture_root"
