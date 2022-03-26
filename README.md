# Registry on IPFS

An easy to use distributed, highly available internal Kubernetes registry with zero dependencies, backed by [IPFS](https://ipfs.io).

## Demo

[![asciicast](https://asciinema.org/a/2bHLwI7vFmiJsqliVnITUop95.svg)](https://asciinema.org/a/2bHLwI7vFmiJsqliVnITUop95)

## Usage

Installation in a cluster is simple, regardless of your clusters access to a registry (airgap vs online):

```bash
# Install in a cluster with access to ripfs registry
ripfs install

# Install in a disconnected cluster without a pre-existing registry 
#   (get the payload from the ripfs releases page)
ripfs install --offline offline-payload.tar.gz
```

Add images to the `ripfs` registry:

```bash
# Add a remote image from dockerhub
ripfs add alpine:latest

# Add a set of images from a local oci layout
ripfs add path/to/layout

# Add images from a tarball created from "docker save"
ripfs add path/to/images.tar.gz
```
