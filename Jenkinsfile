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
      resources:
        requests: {cpu: "500m", memory: "1Gi"}
        limits: {memory: "3Gi"}
      volumeMounts:
        - {name: docker-storage, mountPath: /var/lib/docker}
    # Build + test container: nix (images), the repo devShell (go), and the
    # docker CLI / gotestsum installed below. Talks to the dind daemon.
    - name: nix
      image: nixos/nix:2.28.3
      command: ["sleep"]
      args: ["infinity"]
      env:
        - {name: NIX_CONFIG, value: "experimental-features = nix-command flakes"}
        - {name: DOCKER_HOST, value: "tcp://localhost:2375"}
      resources:
        requests: {cpu: "1", memory: "2Gi"}
        limits: {memory: "6Gi"}
  volumes:
    - name: docker-storage
      emptyDir: {}
'''
        }
      }
      steps {
        container('nix') {
          sh '''
            set -eu
            NIXPKGS=https://channels.nixos.org/nixos-25.05/nixexprs.tar.xz
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

            # build BOTH images with nix and load them into dind. --accept-flake-config
            # trusts the numtide cache (root is a trusted nix user here), so the agents
            # come prebuilt and gemini-cli never compiles from source.
            nix build --accept-flake-config .#proxyImage -o result-proxy
            nix build --accept-flake-config .#image      -o result-toolbox
            docker load -i result-proxy
            docker load -i result-toolbox

            # the smoke image the client-side probes need (else those tests skip)
            docker pull alpine:latest

            # run the whole integration suite against the real daemon. Both images
            # are already loaded, so nothing rebuilds during the run. gotestsum
            # emits JUnit and exits non-zero on any failure.
            nix develop --accept-flake-config --command bash -c 'export PATH="$HOME/.nix-profile/bin:$PATH"; export SANDBOXER_ITEST_ENGINE=docker; gotestsum --junitfile itest-report.xml --format standard-verbose -- -tags integration -count=1 -timeout 40m ./...'
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
