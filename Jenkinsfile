// sandboxer CI — the container-backed e2e suite, on the homelab Jenkins.
//
// Discovered by a Jenkins multibranch job that points at
// github.com/irasikhin/sandboxer (the GitHub PAT credential + the job itself are
// provisioned as code in homelab-k8s JCasC — the standing rgband Organization
// Folder only scans Forgejo). GitHub Actions still runs unit + lint + nix and
// cuts releases; this job runs ONLY the `-tags integration` suite
// (scripts/itest.sh) that the GitHub 90% coverage gate deliberately excludes
// (that gate stays engine-free).
//
// It builds the toolbox and squid proxy images with nix — as root in the
// nixos/nix container the numtide binary cache is trusted, so gemini-cli is
// fetched prebuilt rather than compiled from source (which OOMs the builder) —
// loads them into a privileged docker:dind daemon, and runs the whole suite
// against it. Full suite on every PR/push (the maintainer's choice); heavy, so
// serialized with a generous timeout.
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
          yaml '''
apiVersion: v1
kind: Pod
metadata:
  labels: {jenkins-agent: sandboxer-e2e}
  namespace: jenkins-agents
spec:
  serviceAccountName: jenkins-agent
  containers:
    # Privileged Docker-in-Docker daemon: the jenkins-agents namespace is
    # PSA:privileged precisely to allow this. TLS off => listens on tcp 2375.
    - name: dind
      image: docker:27-dind
      securityContext: {privileged: true}
      env:
        - {name: DOCKER_TLS_CERTDIR, value: ""}
        # This cluster's direct external egress is blocked/degraded (RU); route
        # dockerd's registry pulls (alpine) through the AmneziaWG HTTP proxy.
        - {name: HTTP_PROXY,  value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: HTTPS_PROXY, value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: NO_PROXY, value: "localhost,127.0.0.1,::1,.svc,.cluster.local,10.42.0.0/16,10.43.0.0/16,.rgband.ru"}
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
        - {name: DOCKER_HOST, value: "tcp://localhost:2375"}
        # Route external egress (nix caches, channels, git) through the AmneziaWG
        # HTTP proxy — direct external egress from this cluster is blocked/degraded.
        # Both cases: nix/libcurl + git honour lower-case, some tools upper-case.
        - {name: HTTP_PROXY,  value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: HTTPS_PROXY, value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: http_proxy,  value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: https_proxy, value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: NO_PROXY, value: "localhost,127.0.0.1,::1,.svc,.cluster.local,10.42.0.0/16,10.43.0.0/16,.rgband.ru"}
        - {name: no_proxy, value: "localhost,127.0.0.1,::1,.svc,.cluster.local,10.42.0.0/16,10.43.0.0/16,.rgband.ru"}
      resources:
        requests: {cpu: "1", memory: "2Gi"}
        limits: {memory: "6Gi"}
      volumeMounts:
        - {name: itest-tmp, mountPath: /itest-tmp}
    # The auto-injected agent container — declared here only to give the SCM
    # checkout (git clone of the repo) the egress proxy; direct external egress
    # is blocked/degraded, so without this the checkout fails to resolve the host.
    # The kubernetes plugin fills in the inbound-agent image + connection args.
    - name: jnlp
      env:
        - {name: HTTP_PROXY,  value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: HTTPS_PROXY, value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: http_proxy,  value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: https_proxy, value: "http://awg-http-proxy.media.svc.cluster.local:8888"}
        - {name: NO_PROXY, value: "localhost,127.0.0.1,::1,.svc,.cluster.local,10.42.0.0/16,10.43.0.0/16,.rgband.ru"}
        - {name: no_proxy, value: "localhost,127.0.0.1,::1,.svc,.cluster.local,10.42.0.0/16,10.43.0.0/16,.rgband.ru"}
  volumes:
    - name: docker-storage
      emptyDir: {}
    - name: itest-tmp
      emptyDir: {}
'''
        }
      }
      steps {
        container('nix') {
          sh '''
            set -eu
            NIXPKGS=https://channels.nixos.org/nixos-25.05/nixexprs.tar.xz
            # More patient/resilient nix downloads — the cluster egress throttles
            # the binary caches (slow, occasionally truncated transfers).
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

            # the smoke image the client-side probes need (pulled first so the
            # smoke-tier tests run even if the image builds below cannot)
            docker pull alpine:latest

            # Build the images BEST-EFFORT and load whatever succeeds. The cluster
            # egress can starve the nix caches; when an image can't be built its
            # tests SKIP (SANDBOXER_ITEST_BUILD_IMAGE is left unset) rather than
            # failing the whole run. Each attempt is time-bounded. --accept-flake-config
            # trusts the numtide cache (root is trusted here) so agents come prebuilt.
            proxy=no; toolbox=no
            # Build the toolbox FIRST — it pulls the full set of flake inputs
            # (slow through the proxy); the proxy image then reuses them from the
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
            nix develop --accept-flake-config --command bash -c 'export PATH="$HOME/.nix-profile/bin:$PATH"; export SANDBOXER_ITEST_ENGINE=docker; export TMPDIR=/itest-tmp TMP=/itest-tmp TEMP=/itest-tmp; gotestsum --junitfile itest-report.xml --format standard-verbose -- -tags integration -count=1 -timeout 40m ./...'
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
