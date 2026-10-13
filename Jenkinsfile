pipeline {
    tools {
        git "Default"
    }
    agent {
        label "stargaze-builder"
    }
    environment {
        ENV_FILE=credentials("verdale-judge-service-prod")
    }
    stages {
        stage("Checkout") {
            steps {
                echo "🏷️ Checking out branch: main"
                checkout([
                    $class: 'GitSCM',
                    branches: [[name: '*/main']],
                    userRemoteConfigs: scm.userRemoteConfigs
                ])
            }
        }
        stage("Setup Environment") {
            steps {
                echo "⚙️ Creating environment file with credentials..."
                sh 'cp $ENV_FILE .env'
            }
        }
        stage("Build and Run Container") {
            steps {
                echo "🐳 Building and starting Docker container..."
                sh '''
                export BUILD_VERSION=$(git rev-parse --short=12 HEAD)
                docker compose -f docker-compose.deploy.yml up -d --build || docker-compose -f docker-compose.deploy.yml up -d --build
                '''
            }
        }
    }
    post {
        success {
            echo "🎉 Deployment successful!"
        }
        failure {
            echo "🚨 Deployment failed!"
        }
        always {
            echo "🧹 Cleaning up environment file..."
            sh 'rm -f .env'
        }
    }
}
