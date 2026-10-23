#!/usr/bin/env bash
set -euo pipefail

# ADR-0001 dependency guard:
# cmd/ovn-topologyd and pkg/mcp (T0 only) must NEVER depend on pkg/diag or
# k8s.io/client-go/tools/remotecommand. pkg/mcp is checked once it exists (M9).

FORBIDDEN='(github.com/coulof/ovn-topology/pkg/diag|k8s.io/client-go/tools/remotecommand)'
status=0
for pkg in ./cmd/ovn-topologyd ./pkg/mcp/...; do
  dir="${pkg%/...}"
  [[ -d "${dir}" ]] || continue
  deps=$(go list -deps "${pkg}" | grep -E "${FORBIDDEN}" || true)
  if [[ -n "${deps}" ]]; then
    echo "ERROR: ${dir} violates ADR-0001 by depending on forbidden packages:"
    echo "${deps}"
    status=1
  fi
done
[[ ${status} -eq 0 ]] && echo "ADR-0001 dependency check passed: cmd/ovn-topologyd and pkg/mcp do not depend on pkg/diag or remotecommand."
exit ${status}
