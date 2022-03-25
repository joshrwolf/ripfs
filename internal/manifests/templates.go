package manifests

const baseKustomize = `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- generated.yaml
images:
{{- if .ManagerImage }}
- name: controller
  newName: {{ .ManagerImage }}
{{- end }}
`
