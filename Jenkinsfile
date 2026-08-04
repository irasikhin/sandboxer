// sandboxer CI — the container-backed e2e suite, on a Kubernetes Jenkins.
//
// GitHub Actions runs unit + lint + nix, an engine-level integration slice, and
// cuts releases. This job runs the FULL `-tags integration` suite — including
// the parts that need the toolbox image, which the GitHub jobs deliberately
// skip (that image is a multi-minute nix-in-container build, and the 90%
// coverage gate stays engine-free).
//
// It builds the toolbox and squid proxy images with nix — as root in the
// nixos/nix container (agents compile from source: llm-agents' binary cache
// was dropped from the flake nixConfig because it stalls on some networks) —
// loads them into a privileged docker:dind daemon, and runs the whole suite
// against it. Full suite on every PR/push (the maintainer's choice); heavy, so
// serialized with a generous timeout.
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
// The pod needs a privileged dind container, so its namespace must permit that
// (Pod Security Admission: privileged).

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
    timeout(time: 60, unit: 'MINUTES')
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
    # Privileged Docker-in-Docker daemon: the agent namespace is PSA:privileged
    # precisely to allow this. TLS off => listens on tcp 2375.
    - name: dind
      image: docker:27-dind
      securityContext: {privileged: true}
      env:
        - {name: DOCKER_TLS_CERTDIR, value: ""}${proxyEnv('        ')}
      resources:
        requests: {cpu: "500m", memory: "1Gi"}
        limits: {memory: "3Gi"}
      volumeMounts:
        - {name: docker-storage, mountPath: /var/lib/docker}
        # shared with the nix container so bind mounts the tests create resolve here
        - {name: itest-tmp, mountPath: /itest-tmp}
    # Build + test container: nix (images), the repo devShell (go), and the
    # docker CLI / gotestsum installed below. Talks to the dind daemon.
    - name: nix
      image: nixos/nix:2.28.3
      command: ["sleep"]
      args: ["infinity"]
      env:
        - {name: NIX_CONFIG, value: "experimental-features = nix-command flakes"}
        - {name: DOCKER_HOST, value: "tcp://localhost:2375"}${proxyEnv('        ')}
      resources:
        requests: {cpu: "1", memory: "2Gi"}
        limits: {memory: "6Gi"}
      volumeMounts:
        - {name: itest-tmp, mountPath: /itest-tmp}
    # The auto-injected agent container — declared here only so the SCM checkout
    # (git clone of the repo) inherits the egress proxy when one is configured.
    # The kubernetes plugin fills in the inbound-agent image + connection args.
    - name: jnlp
      env:${proxyEnv('        ') ?: ' []'}
  volumes:
    - name: docker-storage
      emptyDir: {}
    - name: itest-tmp
      emptyDir: {}
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
            export NIX_CONFIG="experimental-features = nix-command flakes
download-attempts = 5
connect-timeout = 30
stalled-download-timeout = 120
http-connections = 10"
            # docker CLI (drives the dind daemon; the itest harness shells out to
            # `docker`) + gotestsum (writes JUnit and propagates the exit code).
            nix-env -iA docker gotestsum -f "$NIXPKGS" >/dev/null 2>&1
            export PATH="$HOME/.nix-profile/bin:$PATH"

            # wait for the dind daemon to accept connections
            for i in $(seq 1 60); do docker info >/dev/null 2>&1 && break; sleep 2; done
            docker version

            # The workspace is checked out as the jenkins uid but this container
            # runs as root, so nix's flake git read fails with "dubious ownership".
            # Trust it (written directly — the base image may not have the git CLI).
            printf '[safe]\n\tdirectory = *\n' > "$HOME/.gitconfig"

            # The smoke image the client-side probes need (loaded first so the
            # smoke-tier tests run even if the heavier image builds below cannot).
            # Built from nix (cache.nixos.org) and loaded as alpine:latest rather
            # than `docker pull`ed from Docker Hub: a restricted egress can 503 on
            # registry-1.docker.io, while the nix binary caches that already back
            # the toolbox build stay reliable. `.#smokeImage` is a busybox image
            # tagged alpine:latest (internal/itest/engine.go accepts a busybox
            # smoke image).
            nix build --accept-flake-config .#smokeImage -o result-smoke
            docker load -i result-smoke
            docker image inspect alpine:latest >/dev/null 2>&1 || { echo "ERROR: smoke image build/load failed"; exit 1; }

            # Build the images BEST-EFFORT and load whatever succeeds. A degraded
            # egress can starve the nix caches; when an image can't be built its
            # tests SKIP (SANDBOXER_ITEST_BUILD_IMAGE is left unset) rather than
            # failing the whole run. The flakes declare no nixConfig today, so
            # --accept-flake-config applies nothing (agents build from source);
            # the flag stays for any future flake nixConfig.
            proxy=no; toolbox=no
            # Build the toolbox FIRST — it pulls the full set of flake inputs
            # (slow through a proxy); the proxy image then reuses them from the
            # nix store and builds fast. No per-image timeout (the pipeline
            # timeout is the backstop); best-effort so a miss skips, not fails.
            if nix build --accept-flake-config .#image -o result-toolbox; then
              docker load -i result-toolbox && toolbox=yes
            else echo "WARN: toolbox image unavailable — sandbox/toolbox tests will skip"; fi
            if nix build --accept-flake-config .#proxyImage -o result-proxy; then
              docker load -i result-proxy && proxy=yes
            else echo "WARN: proxy image unavailable — egress sidecar tests will skip"; fi
            echo "=== images ready: proxy=$proxy toolbox=$toolbox ==="

            # Run the whole suite against the real daemon. TMPDIR points at the
            # emptyDir SHARED with the dind container (mounted at the same path in
            # both), so the sandbox/home/dep dirs the tests bind-mount into
            # containers resolve on dind's fs too — without this, dind runs
            # containers on its own fs and the bind mounts are empty.
            # SANDBOXER_ITEST_SKIP_LIVE_EGRESS: where container egress must
            # traverse a proxy the nested dind daemon's containers cannot use,
            # the squid sidecar can't reach an allowlisted host. Those
            # live-egress allow-path tests skip; the deny path + orchestration
            # still run. Drop the export on a directly-connected cluster.
            nix develop --accept-flake-config --command bash -c 'export PATH="$HOME/.nix-profile/bin:$PATH"; export SANDBOXER_ITEST_ENGINE=docker; export SANDBOXER_ITEST_SKIP_LIVE_EGRESS=1; export TMPDIR=/itest-tmp TMP=/itest-tmp TEMP=/itest-tmp; gotestsum --junitfile itest-report.xml --format standard-verbose -- -tags integration -count=1 -timeout 40m ./...'
          '''
        }
      }
      post {
        always {
          // Archive the JUnit XML as a build artifact. (The `junit` step needs
          // the junit plugin, which is not installed on this controller; add it
          // to additionalPlugins to get test-result trends in the UI.)
          archiveArtifacts artifacts: 'itest-report.xml', allowEmptyArchive: true
        }
      }
    }
  }
}
