// TitanEdge CI/CD pipeline.
//
//   verify (Go, web, Helm, Terraform in parallel)
//     -> build + push 4 images with rootless BuildKit
//     -> Trivy scan (fail on fixable HIGH/CRITICAL)
//     -> promote: commit the new image tag to Git (Argo CD deploys it)
//                 or `helm upgrade` directly (DEPLOY_MODE=helm)
//     -> performance gate: titanload with p99 / error-rate SLOs
//
// Runs on the Jenkins installed by `scripts/install-addons.sh --profile full`.
// Credentials (Manage Jenkins -> Credentials):
//   git-push      username + token allowed to push to the repo (gitops mode)
//   argocd-token  secret text, an Argo CD API token for `argocd app wait`

pipeline {
  agent {
    kubernetes {
      yamlFile 'jenkins/agent-pod.yaml'
      defaultContainer 'tools'
    }
  }

  parameters {
    choice(name: 'DEPLOY_MODE', choices: ['gitops', 'helm', 'none'], description: 'How to roll out a main-branch build')
    string(name: 'ENVIRONMENT', defaultValue: 'kind', description: 'environments/<name>/values.yaml to promote into')
    booleanParam(name: 'RUN_LOAD_TEST', defaultValue: true, description: 'Gate the release on a titanload SLO run')
    string(name: 'LOAD_DURATION', defaultValue: '60s', description: 'Performance gate duration')
    string(name: 'LOAD_CONCURRENCY', defaultValue: '64', description: 'Performance gate concurrency')
    string(name: 'SLO_P99', defaultValue: '250ms', description: 'Maximum acceptable p99')
    string(name: 'SLO_ERROR_RATE', defaultValue: '1', description: 'Maximum error rate in percent')
  }

  options {
    timestamps()
    ansiColor('xterm')
    timeout(time: 60, unit: 'MINUTES')
    buildDiscarder(logRotator(numToKeepStr: '30'))
    disableConcurrentBuilds()
  }

  environment {
    // Pods push through the registry's in-cluster name; nodes pull it back as
    // localhost:5001 (see scripts/kind-up.sh). Override both for ECR.
    PUSH_REGISTRY = 'kind-registry:5000'
    PULL_REGISTRY = 'localhost:5001'
    APP_NAMESPACE = 'titanedge'
    ARGOCD_SERVER = 'argocd-server.argocd.svc.cluster.local'
  }

  stages {
    stage('Prepare') {
      steps {
        script {
          env.GIT_SHORT = sh(script: 'git rev-parse --short=8 HEAD', returnStdout: true).trim()
          env.IMAGE_TAG = "${env.BUILD_NUMBER}-${env.GIT_SHORT}"
          currentBuild.displayName = "#${env.BUILD_NUMBER} ${env.IMAGE_TAG}"
        }
        sh 'mkdir -p reports'
      }
    }

    stage('Verify') {
      parallel {
        stage('Go') {
          steps {
            container('golang') {
              sh '''
                unformatted=$(gofmt -l cmd internal)
                if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; exit 1; fi
                go vet ./...
                go test -count=1 -coverprofile=reports/coverage.out ./...
                go tool cover -func=reports/coverage.out | tail -1
              '''
            }
          }
        }
        stage('Web') {
          steps {
            container('node') {
              dir('web') {
                sh 'npm ci --no-audit --no-fund && npm run typecheck && npm run build'
              }
            }
          }
        }
        stage('Helm') {
          steps {
            sh '''
              helm lint charts/platform charts/titanload
              for env in environments/*/values.yaml; do
                echo "render $env"
                helm template titan charts/platform -f "$env" \
                  --api-versions monitoring.coreos.com/v1 \
                  --api-versions keda.sh/v1alpha1 \
                  --api-versions security.istio.io/v1 > /dev/null
              done
            '''
          }
        }
        stage('Terraform') {
          steps {
            container('terraform') {
              sh '''
                terraform -chdir=infra/terraform fmt -check -recursive
                for env in kind eks; do
                  terraform -chdir=infra/terraform/envs/$env init -backend=false -input=false
                  terraform -chdir=infra/terraform/envs/$env validate
                done
              '''
            }
          }
        }
      }
    }

    stage('Build images') {
      steps {
        container('buildkit') {
          sh '''
            set -e
            build() {
              name=$1; shift
              echo "==> ${PUSH_REGISTRY}/titanedge/${name}:${IMAGE_TAG}"
              buildctl-daemonless.sh build --frontend dockerfile.v0 "$@" \
                --opt build-arg:VERSION=${IMAGE_TAG} --opt build-arg:COMMIT=${GIT_SHORT} \
                --output type=image,name=${PUSH_REGISTRY}/titanedge/${name}:${IMAGE_TAG},push=true,registry.insecure=true
            }
            for cmd in api worker titanload; do
              build "$cmd" --local context=. --local dockerfile=build --opt filename=go.Dockerfile --opt build-arg:CMD=$cmd
            done
            build web --local context=web --local dockerfile=web
          '''
        }
      }
    }

    stage('Scan images') {
      steps {
        container('trivy') {
          sh '''
            for img in api worker titanload web; do
              trivy image --insecure --no-progress --ignore-unfixed \
                --severity HIGH,CRITICAL --exit-code 1 \
                --format table --output reports/trivy-$img.txt \
                ${PUSH_REGISTRY}/titanedge/$img:${IMAGE_TAG} || { cat reports/trivy-$img.txt; exit 1; }
            done
          '''
        }
      }
    }

    stage('Promote') {
      when { allOf { expression { onMain() }; not { equals expected: 'none', actual: params.DEPLOY_MODE } } }
      stages {
        stage('GitOps commit') {
          when { equals expected: 'gitops', actual: params.DEPLOY_MODE }
          steps {
            withCredentials([usernamePassword(credentialsId: 'git-push', usernameVariable: 'GIT_USER', passwordVariable: 'GIT_TOKEN')]) {
              sh '''
                set -e
                file="environments/${ENVIRONMENT}/values.yaml"
                yq -i '.global.imageTag = strenv(IMAGE_TAG)' "$file"
                git config user.email "jenkins@titanedge.local"
                git config user.name "Jenkins"
                git add "$file"
                git commit -m "deploy(${ENVIRONMENT}): titanedge ${IMAGE_TAG} [skip ci]"
                origin=$(git remote get-url origin | sed -E 's#https://#https://'"${GIT_USER}:${GIT_TOKEN}"'@#')
                git push "$origin" HEAD:main
              '''
            }
          }
        }
        stage('Argo CD sync') {
          when { equals expected: 'gitops', actual: params.DEPLOY_MODE }
          steps {
            withCredentials([string(credentialsId: 'argocd-token', variable: 'ARGOCD_AUTH_TOKEN')]) {
              sh '''
                argocd app get titanedge-platform --server ${ARGOCD_SERVER} --grpc-web --insecure --refresh >/dev/null
                argocd app wait titanedge-platform --server ${ARGOCD_SERVER} --grpc-web --insecure \
                  --sync --health --timeout 900
              '''
            }
          }
        }
        stage('Helm upgrade') {
          when { equals expected: 'helm', actual: params.DEPLOY_MODE }
          steps {
            sh '''
              helm upgrade --install titan charts/platform -n ${APP_NAMESPACE} \
                -f environments/${ENVIRONMENT}/values.yaml \
                --set global.imageTag=${IMAGE_TAG} --set global.imageRegistry=${PULL_REGISTRY} \
                --wait --timeout 15m
              helm test titan -n ${APP_NAMESPACE} --logs
            '''
          }
        }
      }
    }

    stage('Performance gate') {
      when { allOf { expression { onMain() }; expression { params.RUN_LOAD_TEST && params.DEPLOY_MODE != 'none' } } }
      steps {
        sh '''
          set -e
          pod=titanload-gate-${BUILD_NUMBER}
          overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}},
            "containers":[{"name":"'$pod'","image":"'${PULL_REGISTRY}'/titanedge/titanload:'${IMAGE_TAG}'",
            "args":["run","-u","http://titan-nginx.'${APP_NAMESPACE}'.svc.cluster.local/api/v1/ping",
                    "-c","'${LOAD_CONCURRENCY}'","-d","'${LOAD_DURATION}'","--no-dashboard",
                    "--slo-p99","'${SLO_P99}'","--slo-error-rate","'${SLO_ERROR_RATE}'","--json","-"],
            "securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}'
          kubectl -n titanload run "$pod" --rm -i --restart=Never \
            --image=${PULL_REGISTRY}/titanedge/titanload:${IMAGE_TAG} \
            --labels=sidecar.istio.io/inject=false \
            --overrides="$overrides" | tee reports/loadtest.txt
        '''
      }
    }
  }

  post {
    always {
      archiveArtifacts artifacts: 'reports/**', allowEmptyArchive: true, fingerprint: true
    }
    success {
      echo "TitanEdge ${env.IMAGE_TAG} built${env.BRANCH_NAME == 'main' ? ' and promoted' : ''}."
    }
    failure {
      echo 'Pipeline failed - check the stage logs and reports/ artifacts.'
    }
  }
}

// Works for multibranch jobs (BRANCH_NAME set) and the single-branch
// pipelineJob created by JCasC (tracks main, BRANCH_NAME unset).
def onMain() {
  return (env.BRANCH_NAME ?: 'main') == 'main'
}
