package sandbox

const SBXPreparedGoModuleCacheEnvironment = "TRAVERSE_PREPARED_GOMODCACHE"

// The guest mount's absolute path is selected by sbx. After the fixed cwd
// change, resolve the Go-owned relative cache binding against its real PWD.
const sbxGuestWorkspaceCommand = `cd -- "$1" || exit 125; shift; if [ -n "${TRAVERSE_PREPARED_GOMODCACHE-}" ]; then export GOMODCACHE="$PWD/$TRAVERSE_PREPARED_GOMODCACHE"; fi; unset TRAVERSE_PREPARED_GOMODCACHE; exec "$@"`

// Verify effective guest state before dispatch, since daemon settings may have
// changed on disk without a restart. This fixed script reads only local socket
// presence and credential-mode metadata; it never prints a credential or loads
// a profile. The entire command environment is then replaced with env -i.
const sbxGuestEnvironmentGuard = `
for socket in /run/ssh-agent.sock /run/host-services/ssh-auth.sock "${SSH_AUTH_SOCK-}"; do
  if [ -n "$socket" ] && [ -S "$socket" ]; then exit 125; fi
done
seen_modes='|'
while IFS= read -r -d '' binding; do
  case "$binding" in
    SBX_CRED_*_MODE=*)
      [ "${binding#*=}" = none ] || exit 125
      seen_modes="${seen_modes}${binding%%=*}|"
      ;;
  esac
done < /proc/self/environ || exit 125
for mode in SBX_CRED_GITHUB_MODE SBX_CRED_OPENROUTER_MODE SBX_CRED_ANTHROPIC_MODE SBX_CRED_NEBIUS_MODE SBX_CRED_XAI_MODE SBX_CRED_OPENAI_MODE SBX_CRED_MISTRAL_MODE SBX_CRED_GOOGLE_MODE; do
  case "$seen_modes" in *"|$mode|"*) ;; *) exit 125 ;; esac
done
exec /usr/bin/env -i "$@"
`
