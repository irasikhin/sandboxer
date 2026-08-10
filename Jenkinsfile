// sandboxer CI — the microVM-backed e2e suite, on a Kubernetes Jenkins.
//
// GitHub Actions runs unit + lint + nix, the engine-level msb slice on KVM
// runners, and cuts releases. This job runs the FULL `-tags integration` suite
// against the REAL toolbox image — the part GitHub deliberately skips (the
// image is a multi-minute nix build, and the 90% coverage gate stays
// engine-free). That includes TestMSB_NestedContainer_RealEngine: docker and
// podman running INSIDE the msb guest, postgres switching uids against the
// guest kernel — the soak that gates the container-backend removal.
//
// It builds the toolbox image and the pinned microsandbox runtime with nix in
// the nixos/nix container, loads the image into msb's store, and runs the
// whole suite with /dev/kvm from the host. No docker daemon anywhere: the
// machine under test is a microVM, and nested containers run on the guest's
// own kernel.
//
// Deployment specifics are NOT hardcoded here — this file is public. Set them
// as environment variables on the controller (JCasC globalNodeProperties, or
// Manage Jenkins → System → Global properties):
//
//   SANDBOXER_CI_AGENT_NAMESPACE  namespace the agent pods run in
//                                 (default: jenkins-agents)
//   SANDBOXER_CI_HTTP_PROXY       egress proxy URL, when the cluster cannot
//                                 reach the internet directly. Unset = no proxy
//                                 env is injected at all, which is correct for
//                                 any normally-connected cluster.
//   SANDBOXER_CI_NO_PROXY         proxy bypass list (defaults to loopback +
//                                 in-cluster suffixes; add internal domains and
//                                 pod/service CIDRs here)
//
// The pod needs a privileged container for /dev/kvm, so its namespace must
// permit that (Pod Security Admission: privileged) — the same grant the old
// dind pod used.

def agentNS = env.SANDBOXER_CI_AGENT_NAMESPACE ?: 'jenkins-agents'
def proxyURL = env.SANDBOXER_CI_HTTP_PROXY ?: ''
def noProxy = env.SANDBOXER_CI_NO_PROXY ?: 'localhost,127.0.0.1,::1,.svc,.cluster.local'

// proxyEnv renders the proxy environment for one container at the given YAML
// indent, or nothing at all when no proxy is configured. Both cases are needed:
// nix/libcurl and git honour the lower-case names, some tools only upper-case.
def proxyEnv = { String indent ->
  if (!proxyURL) {
    return ''
  }
  def lines = []
  ['HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy'].each {
    lines << "${indent}- {name: ${it}, value: \"${proxyURL}\"}"
  }
  ['NO_PROXY', 'no_proxy'].each {
    lines << "${indent}- {name: ${it}, value: \"${noProxy}\"}"
  }
  return '\n' + lines.join('\n')
}

pipeline {
  agent none
  options {
    // 120: a COLD node cache rebuilds the from-source agents at max-jobs 2,
    // which does not fit in 60. A warm cache substitutes and finishes fast.
    timeout(time: 120, unit: 'MINUTES')
    disableConcurrentBuilds()
  }
  stages {
    stage('e2e') {
      agent {
        kubernetes {
          yaml """
apiVersion: v1
kind: Pod
metadata:
  labels: {jenkins-agent: sandboxer-e2e}
  namespace: ${agentNS}
spec:
  serviceAccountName: jenkins-agent
  containers:
    # Build + test container: nix (image + msb runtime), the repo devShell
    # (go) and gotestsum installed below. Privileged for /dev/kvm — the agent
    # namespace is PSA:privileged precisely to allow this.
    - name: nix
      image: nixos/nix:2.28.3
      command: ["sleep"]
      args: ["infinity"]
      securityContext: {privileged: true}
      env:
        - {name: NIX_CONFIG, value: "experimental-features = nix-command flakes"}${proxyEnv('        ')}
      resources:
        requests: {cpu: "1", memory: "3Gi"}
        limits: {memory: "8Gi"}
      volumeMounts:
        - {name: dev-kvm, mountPath: /dev/kvm}
        # Persistent file:// nix binary cache on the node. The pod's /nix is
        # containerfs and starts EMPTY every run, so without this each push
        # recompiles the from-source agents (llm-agents publishes no usable
        # cache) — tens of minutes of CPU, and the crush Go build alone OOMed
        # the 8Gi pod. Populated by `nix copy` after a successful build.
        - {name: nix-cache, mountPath: /nix-cache}
    # The auto-injected agent container — declared here only so the SCM checkout
    # (git clone of the repo) inherits the egress proxy when one is configured.
    # The kubernetes plugin fills in the inbound-agent image + connection args.
    - name: jnlp
      env:${proxyEnv('        ') ?: ' []'}
  volumes:
    - name: dev-kvm
      hostPath: {path: /dev/kvm, type: CharDevice}
    - name: nix-cache
      hostPath: {path: /var/lib/sandboxer-ci/nix-cache, type: DirectoryOrCreate}
"""
        }
      }
      steps {
        container('nix') {
          sh '''
            set -eu
            NIXPKGS=https://channels.nixos.org/nixos-25.05/nixexprs.tar.xz
            # More patient/resilient nix downloads — a throttled or proxied
            # egress makes binary-cache transfers slow and occasionally truncated.
            # max-jobs 2: the from-source agent builds (Go/npm) are memory-hogs
            # and two of them in parallel already OOMed this pod at 8Gi —
            # serialize the heavy tail instead of raising the limit forever.
            # The file:// substituter is the node-local cache seeded by the
            # `nix copy` below; require-sigs off because it is unsigned and
            # writable only by this pipeline's node path.
            export NIX_CONFIG="experimental-features = nix-command flakes
download-attempts = 5
connect-timeout = 30
stalled-download-timeout = 120
http-connections = 10
max-jobs = 2
extra-substituters = file:///nix-cache
require-sigs = false"
            # gotestsum: writes JUnit and propagates the exit code.
            nix-env -iA gotestsum -f "$NIXPKGS" >/dev/null 2>&1
            export PATH="$HOME/.nix-profile/bin:$PATH"

            ls -l /dev/kvm

            # The workspace is checked out as the jenkins uid but this container
            # runs as root, so nix's flake git read fails with "dubious ownership".
            # Trust it (written directly — the base image may not have the git CLI).
            printf '[safe]\n\tdirectory = *\n' > "$HOME/.gitconfig"

            # The pinned microsandbox runtime, from the repo's own nix package —
            # the exact binary the backend is written against. MSB_HOME must stay
            # short: every sandbox's agent-relay unix socket derives from it and
            # sun_path caps at 108 bytes.
            nix build --accept-flake-config .#microsandbox -o result-msb
            export SANDBOXER_MSB="$PWD/result-msb/bin/msb"
            export MSB_HOME=/tmp/msb
            "$SANDBOXER_MSB" --version

            # The REAL toolbox image. The nix output is a GZIPPED docker tarball
            # and msb load reads the outer archive raw, so decompress first
            # (the sandboxer binary does the same at import). Loaded under a
            # dedicated ref the suite is then pointed at.
            nix build --accept-flake-config .#image -o result-toolbox
            # Seed the node-local cache so the NEXT run substitutes instead of
            # rebuilding the agents from source: --all copies every path this
            # run realized (agents included), so even an images.nix edit only
            # rebuilds the layers that actually changed. Best-effort: a full
            # cache disk must not fail a green build.
            nix copy --all --to file:///nix-cache || true
            gunzip -c result-toolbox > /var/tmp/toolbox.tar
            "$SANDBOXER_MSB" load -i /var/tmp/toolbox.tar -t sandboxer-toolbox:itest -q
            rm /var/tmp/toolbox.tar
            export SANDBOXER_ITEST_MSB_IMAGE=sandboxer-toolbox:itest

            # The whole -tags integration suite. Container-engine tests skip
            # (no docker/podman here — that is the point); the msb suite runs
            # for real, nested containers included. The log capture avoids
            # `| tee` on purpose: this shell has no pipefail, and a pipe would
            # swallow the test exit code. The required-tests assert below runs
            # even on failure — it names WHAT broke — and rc fails the stage.
            rc=0
            nix develop --accept-flake-config --command bash -c 'export PATH="$HOME/.nix-profile/bin:$PATH"; gotestsum --junitfile itest-report.xml --format standard-verbose -- -tags integration -count=1 -timeout 40m ./...' > itest.log 2>&1 || rc=$?
            tail -n 100 itest.log

            scripts/assert-tests-ran.sh itest.log \\
              TestMSB_Lifecycle_RealEngine \\
              TestMSB_NarrowingWall_RealEngine \\
              TestMSB_GuestWriteUID_RealEngine \\
              TestMSB_EgressAllowlist_RealEngine \\
              TestMSB_SecretsMode_RealEngine \\
              TestMSB_NestedContainer_RealEngine
            [ "$rc" -eq 0 ]
          '''
        }
      }
      post {
        always {
          // Archive the JUnit XML as a build artifact. (The `junit` step needs
          // the junit plugin, which is not installed on this controller; add it
          // to additionalPlugins to get test-result trends in the UI.)
          archiveArtifacts artifacts: 'itest-report.xml,itest.log', allowEmptyArchive: true
        }
      }
    }
  }
}
