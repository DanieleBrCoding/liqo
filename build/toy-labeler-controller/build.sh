#!/bin/bash
# Build script for toy-labeler-controller

set -e

REPO="${DOCKER_REPO:-localhost:5000}"
IMAGE_NAME="toy-labeler-controller"
VERSION="${VERSION:-latest}"

echo "Building toy-labeler-controller..."
echo "  Repository: $REPO"
echo "  Image: $IMAGE_NAME:$VERSION"

# Build the Docker image
docker build \
  -t "$REPO/$IMAGE_NAME:$VERSION" \
  -t "$REPO/$IMAGE_NAME:latest" \
  -f build/toy-labeler-controller/Dockerfile \
  .

echo ""
echo "Build complete!"
echo "Image: $REPO/$IMAGE_NAME:$VERSION"
echo ""
echo "To push the image to a registry:"
echo "  docker push $REPO/$IMAGE_NAME:$VERSION"
echo ""
echo "To deploy on a Provider cluster:"
echo "  kubectl --kubeconfig <provider-kubeconfig> apply -f examples/toy-labeler/manifests/toy-labeler-controller-deployment.yaml"
