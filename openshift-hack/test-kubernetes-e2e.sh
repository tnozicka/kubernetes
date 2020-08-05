#!/bin/bash

set -o nounset
set -o errexit
set -o pipefail

# This script is executes kubernetes e2e tests against an openshift
# cluster. It is intended to be copied to the kubernetes-tests image
# for use in CI and should have no dependencies beyond oc, kubectl and
# k8s-e2e.test.

# Identify the platform under test to allow skipping tests that are
# not compatible.
PLATFORM="${PLATFORM:-gce}"

# As a starting point, only support a parallel suite of tests that are
# not disabled or skipped.
DEFAULT_TEST_ARGS="-skip=\[Slow\]|\[Serial\]|\[Disruptive\]|\[Flaky\]|\[Disabled:.+\]|\[Skipped:${PLATFORM}\]"

# Set KUBE_E2E_TEST_ARGS to configure test arguments like
# -skip and -focus.
KUBE_E2E_TEST_ARGS="${KUBE_E2E_TEST_ARGS:-${DEFAULT_TEST_ARGS}}"

# k8s-e2e.test and ginkgo are expected to be in the path in
# CI. Outside of CI, ensure k8s-e2e.test and ginkgo are built and
# available in PATH.
if ! which k8s-e2e.test &> /dev/null; then
  make WHAT=vendor/github.com/onsi/ginkgo/ginkgo
  make WHAT=openshift-hack/e2e/k8s-e2e.test
  ROOT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")/.."; pwd -P)"
  PATH="${ROOT_PATH}/_output/local/bin/$(go env GOHOSTOS)/$(go env GOARCH):${PATH}"
  export PATH
fi

# Execute OpenShift prerequisites
# Disable container security
oc adm policy add-scc-to-group privileged system:authenticated system:serviceaccounts
oc adm policy add-scc-to-group anyuid system:authenticated system:serviceaccounts
# Mark the master nodes as unschedulable so tests ignore them
oc get nodes -o name -l 'node-role.kubernetes.io/master' | xargs -L1 oc adm cordon
unschedulable="$( ( oc get nodes -o name -l 'node-role.kubernetes.io/master'; ) | wc -l )"

test_report_dir="${ARTIFACTS:-/tmp/artifacts}"
mkdir -p "${test_report_dir}"

# Retrieve the hostname of the server to enable kubectl testing
SERVER=
SERVER="$( kubectl config view | grep server | head -n 1 | awk '{print $2}' )"

# Use the same number of nodes - 30 - as specified for the parallel
# suite defined in origin.
# shellcheck disable=SC2086
ginkgo \
  -nodes 30 -noColor ${KUBE_E2E_TEST_ARGS} \
  "$( which k8s-e2e.test )" -- \
  -report-dir "${test_report_dir}" \
  -host "${SERVER}" \
  -allowed-not-ready-nodes ${unschedulable} \
  2>&1 | tee -a "${test_report_dir}/k8s-e2e.log"
