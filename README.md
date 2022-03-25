# Registry on IPFS

A distributed, replicated, and resilient internal Kubernetes registry with zero dependencies, backed by IPFS.

## Usage

Installation in a cluster is simple, it even works offline:

```bash
# Install in an existing connected cluster
ripfs install

# Install in an existing disconnected cluster (get the payload for your clusters architecture from the releases page)
ripfs install --offline payload.tar.gz
```

Add images to the registry:

```bash
# Add a remote image from dockerhub
ripfs add alpine:latest

# Add a set of images from a local oci layout
ripfs add path/to/layout

# Add images from a tarball created from docker save
ripfs add path/to/images.tar.gz
```
