// Verdale judge service: check (gofmt, vet, unit tests, contract check), integration
// tests (real isolate + Redis in a privileged container), then build and push the image.
//
// Agent requirements: Docker + Docker Pipeline plugin, Linux with cgroup v2
// (the integration container is privileged), docker compose v2.
// Credentials: `registry-credentials` (username/password for REGISTRY).
//
// Note: production runs the binary under systemd (deploy/systemd/README.md);
// the image is for dev, CI and container-based environments.

pipeline {
  agent any

  options {
    timestamps()
    timeout(time: 45, unit: 'MINUTES')
    disableConcurrentBuilds(abortPrevious: true)
    buildDiscarder(logRotator(numToKeepStr: '30'))
  }

  parameters {
    string(name: 'REGISTRY', defaultValue: 'registry.example.com', description: 'Docker registry host')
    string(name: 'IMAGE_NAME', defaultValue: 'verdale/judge-service', description: 'Image repository')
    string(name: 'REGISTRY_CREDENTIALS_ID', defaultValue: 'registry-credentials', description: 'Jenkins credentials ID for the registry')
    string(name: 'PUSH_BRANCH', defaultValue: 'main', description: 'Only this branch pushes images')
    booleanParam(name: 'RUN_INTEGRATION', defaultValue: true, description: 'Run sandbox integration tests (needs privileged Docker, cgroup v2)')
  }

  environment {
    GO_IMAGE = 'golang:1.24.1-bookworm'
    IMAGE_TAG = "${env.GIT_COMMIT?.take(12) ?: env.BUILD_NUMBER}"
    COMPOSE_PROJECT_NAME = "judge-it-${env.BUILD_NUMBER}"
  }

  stages {
    stage('Check') {
      steps {
        script {
          docker.image(env.GO_IMAGE).inside("-e HOME=${env.WORKSPACE} -e GOCACHE=${env.WORKSPACE}/.cache/go-build -e GOPATH=${env.WORKSPACE}/.cache/go") {
            sh 'make check'
          }
        }
      }
    }

    stage('Integration') {
      when { expression { params.RUN_INTEGRATION } }
      steps {
        sh 'make test-integration'
      }
    }

    stage('Build image') {
      steps {
        sh 'docker build --target judge --build-arg VERSION="${IMAGE_TAG}" -t "${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}" .'
      }
    }

    stage('Push image') {
      when { branch params.PUSH_BRANCH }
      steps {
        withCredentials([usernamePassword(credentialsId: params.REGISTRY_CREDENTIALS_ID, usernameVariable: 'REGISTRY_USER', passwordVariable: 'REGISTRY_PASSWORD')]) {
          sh '''
            echo "$REGISTRY_PASSWORD" | docker login "$REGISTRY" -u "$REGISTRY_USER" --password-stdin
            docker push "${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
            docker tag "${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}" "${REGISTRY}/${IMAGE_NAME}:latest"
            docker push "${REGISTRY}/${IMAGE_NAME}:latest"
            docker logout "$REGISTRY"
          '''
        }
      }
    }
  }

  post {
    always {
      sh '''
        docker compose -f docker-compose.test.yml down -v --remove-orphans >/dev/null 2>&1 || true
        docker image rm -f "${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}" "${REGISTRY}/${IMAGE_NAME}:latest" || true
      '''
      cleanWs()
    }
  }
}
